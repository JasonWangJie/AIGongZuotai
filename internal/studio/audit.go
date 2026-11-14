package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditDetails is deliberately a closed data structure: never replace this
// with a generic request/payload map that can carry prompts or credentials.
type AuditDetails struct {
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Revision   uint64 `json:"revision,omitempty"`
	NodeCount  int    `json:"node_count,omitempty"`
}
type AuditEvent struct {
	ID           string       `json:"id"`
	WorkspaceID  string       `json:"workspace_id"`
	ProjectID    string       `json:"project_id"`
	ActorID      string       `json:"actor_id"`
	ActorType    string       `json:"actor_type"`
	Action       string       `json:"action"`
	ResourceType string       `json:"resource_type"`
	ResourceID   string       `json:"resource_id"`
	CreatedAt    time.Time    `json:"created_at"`
	Details      AuditDetails `json:"details"`
}

func auditActorID(r *http.Request) string {
	if p := principalFrom(r); p != nil && p.User != nil {
		return p.User.ID
	}
	return ""
}
func firstActor(actorIDs []string) string {
	if len(actorIDs) > 0 {
		return actorIDs[0]
	}
	return ""
}
func newAuditEvent(workspaceID, projectID, actorID, action, resourceType, resourceID string, details AuditDetails) AuditEvent {
	actorType := "local"
	if actorID != "" {
		actorType = "user"
	}
	return AuditEvent{ID: newID("audit"), WorkspaceID: workspaceID, ProjectID: projectID, ActorID: actorID, ActorType: actorType, Action: action, ResourceType: resourceType, ResourceID: resourceID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Details: details}
}
func insertAuditSQL(ctx context.Context, tx pgx.Tx, event AuditEvent) error {
	details, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO studio_audit_events(id,workspace_id,project_id,actor_id,actor_type,action,resource_type,resource_id,created_at,details) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", event.ID, event.WorkspaceID, event.ProjectID, event.ActorID, event.ActorType, event.Action, event.ResourceType, event.ResourceID, event.CreatedAt, details)
	if err != nil {
		return err
	}
	return appendOutboxSQL(ctx, tx, "audit:"+event.ID, DurableEvent{WorkspaceID: event.WorkspaceID, ProjectID: event.ProjectID, Kind: "audit.created", ResourceType: event.ResourceType, ResourceID: event.ResourceID, ResourceVersion: event.Details.Revision, Status: event.Details.ToStatus, Action: event.Action})
}

const memoryAuditLimit = 512

func (a *App) appendMemoryAuditLocked(events ...AuditEvent) {
	if a.store != nil {
		return
	}
	for _, event := range events {
		if len(a.auditEvents) < memoryAuditLimit {
			a.auditEvents = append(a.auditEvents, event)
		} else {
			a.auditEvents[a.auditStart] = event
			a.auditStart = (a.auditStart + 1) % memoryAuditLimit
		}
	}
}

type auditCursor struct {
	WorkspaceID string    `json:"w"`
	ProjectID   string    `json:"p"`
	ID          string    `json:"i"`
	CreatedAt   time.Time `json:"t"`
}
type auditPage struct {
	Items      []AuditEvent `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
	Storage    string       `json:"storage"`
}

func parseAuditCursor(raw, workspaceID, projectID string) (*auditCursor, error) {
	if raw == "" {
		return nil, nil
	}
	bad := func() (*auditCursor, error) {
		return nil, failure(400, "INVALID_AUDIT_CURSOR", "活动游标无效或不属于当前查询范围")
	}
	if len(raw) > 1024 {
		return bad()
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return bad()
	}
	var cursor auditCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || cursor.ID == "" || len(cursor.ID) > 80 || cursor.CreatedAt.IsZero() || cursor.WorkspaceID != workspaceID || cursor.ProjectID != projectID {
		return bad()
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return bad()
	}
	return &cursor, nil
}
func (a *App) listAuditHTTP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	projectID := p.Project.ID
	switch r.URL.Query().Get("scope") {
	case "", "project":
	case "workspace":
		projectID = ""
	default:
		writeError(w, failure(400, "INVALID_AUDIT_SCOPE", "scope必须为project或workspace"))
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, failure(400, "INVALID_AUDIT_LIMIT", "limit需要1至100"))
			return
		}
		limit = value
	}
	cursor, err := parseAuditCursor(r.URL.Query().Get("cursor"), p.Workspace.ID, projectID)
	if err != nil {
		writeError(w, err)
		return
	}
	items := []AuditEvent{}
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var before *time.Time
			id := ""
			if cursor != nil {
				before = &cursor.CreatedAt
				id = cursor.ID
			}
			rows, err := conn.Query(ctx, "SELECT id,workspace_id,project_id,actor_id,actor_type,action,resource_type,resource_id,created_at,details FROM studio_audit_events WHERE workspace_id=$1 AND ($2='' OR project_id=$2) AND ($3::timestamptz IS NULL OR (created_at,id)<($3::timestamptz,$4)) ORDER BY created_at DESC,id DESC LIMIT $5", p.Workspace.ID, projectID, before, id, limit+1)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var event AuditEvent
				var details []byte
				if err = rows.Scan(&event.ID, &event.WorkspaceID, &event.ProjectID, &event.ActorID, &event.ActorType, &event.Action, &event.ResourceType, &event.ResourceID, &event.CreatedAt, &details); err != nil {
					return err
				}
				if err = json.Unmarshal(details, &event.Details); err != nil {
					return err
				}
				items = append(items, event)
			}
			return rows.Err()
		})
		if err != nil {
			writeError(w, err)
			return
		}
	} else {
		a.mu.Lock()
		for _, event := range a.auditEvents {
			if event.WorkspaceID != p.Workspace.ID || projectID != "" && event.ProjectID != projectID {
				continue
			}
			if cursor != nil && (!event.CreatedAt.Before(cursor.CreatedAt) && (!event.CreatedAt.Equal(cursor.CreatedAt) || event.ID >= cursor.ID)) {
				continue
			}
			items = append(items, event)
		}
		a.mu.Unlock()
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].ID > items[j].ID
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	}
	page := auditPage{Items: items, Storage: a.storageMode()}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		blob, _ := json.Marshal(auditCursor{WorkspaceID: p.Workspace.ID, ProjectID: projectID, ID: last.ID, CreatedAt: last.CreatedAt})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(blob)
	}
	writeJSON(w, 200, page)
}

// saveWithAudit is used only when a state transition needs an audit entry.
// The state mutation and every audit row share the same PostgreSQL transaction.
func (s *postgresStore) saveWithAudit(change func(context.Context, pgx.Tx) error, events []AuditEvent) error {
	if len(events) == 0 {
		return errors.New("audit transaction requires an event")
	}
	return s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = change(ctx, tx); err != nil {
			return err
		}
		for _, event := range events {
			if err = insertAuditSQL(ctx, tx, event); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
}
