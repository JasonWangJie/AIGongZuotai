package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type durableCursor struct {
	Version     int    `json:"v"`
	StorageID   string `json:"d"`
	UserID      string `json:"u"`
	WorkspaceID string `json:"w"`
	ProjectID   string `json:"p"`
	Seq         uint64 `json:"s"`
}
type durablePage struct {
	StorageID   string
	High, Floor uint64
	Items       []DurableEvent
}

func encodeDurableCursor(c durableCursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}
func parseDurableCursor(raw string, expected durableCursor) (durableCursor, error) {
	invalid := func() (durableCursor, error) {
		return durableCursor{}, failure(400, "INVALID_EVENT_CURSOR", "通知游标无效或不属于当前账户和项目")
	}
	if len(raw) == 0 || len(raw) > 1024 {
		return invalid()
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return invalid()
	}
	var c durableCursor
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil {
		return invalid()
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return invalid()
	}
	if c.Version != 1 || c.StorageID != expected.StorageID || c.UserID != expected.UserID || c.WorkspaceID != expected.WorkspaceID || c.ProjectID != expected.ProjectID || c.Seq > 9007199254740991 {
		return invalid()
	}
	return c, nil
}

func (s *postgresStore) readEventPage(scope subscriptionScope, after uint64) (durablePage, error) {
	page := durablePage{Items: []DurableEvent{}}
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = tx.QueryRow(ctx, "SELECT storage_id::text FROM studio_storage_identity WHERE singleton").Scan(&page.StorageID); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT last_seq FROM studio_event_consumers WHERE consumer='project-feed-v1' AND workspace_id=$1 AND project_id=$2),0),coalesce((SELECT floor_seq FROM studio_event_heads WHERE workspace_id=$1 AND project_id=$2),0)`, scope.WorkspaceID, scope.ProjectID).Scan(&page.High, &page.Floor); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT e.event_id,e.project_seq,e.kind,e.resource_type,e.resource_id,e.resource_version,e.status,e.action FROM studio_project_event_feed f JOIN studio_outbox_events e ON e.event_id=f.event_id WHERE f.workspace_id=$1 AND f.project_id=$2 AND f.project_seq>$3 AND f.project_seq<=$4 ORDER BY f.project_seq LIMIT $5`, scope.WorkspaceID, scope.ProjectID, after, page.High, outboxBatchSize)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e DurableEvent
			e.WorkspaceID, e.ProjectID = scope.WorkspaceID, scope.ProjectID
			if err = rows.Scan(&e.EventID, &e.Seq, &e.Kind, &e.ResourceType, &e.ResourceID, &e.ResourceVersion, &e.Status, &e.Action); err != nil {
				rows.Close()
				return err
			}
			page.Items = append(page.Items, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return page, err
}

func (a *App) durableWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if !a.acquireStream(w, r) {
		return
	}
	defer a.releaseObservedStream(p.Workspace.ID)
	scope := subscriptionScope{WorkspaceID: p.Workspace.ID, ProjectID: p.Project.ID}
	wake, unsubscribe := a.store.subscribeOutbox(scope)
	defer unsubscribe()
	page, err := a.store.readEventPage(scope, 0)
	if err != nil {
		writeError(w, err)
		return
	}
	cursor := durableCursor{Version: 1, StorageID: page.StorageID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, UserID: "local"}
	if p.User != nil {
		cursor.UserID = p.User.ID
	}
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	initial := raw == ""
	if !initial {
		cursor, err = parseDurableCursor(raw, cursor)
		if err != nil {
			writeError(w, err)
			return
		}
		if cursor.Seq > page.High {
			writeError(w, failure(409, "EVENT_CURSOR_AHEAD", "通知游标超出持久记录，请重新同步项目"))
			return
		}
		page, err = a.store.readEventPage(scope, cursor.Seq)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if !startSSE(w) {
		return
	}
	heartbeat := time.NewTicker(a.config.Heartbeat)
	defer heartbeat.Stop()
	fallback := time.NewTicker(time.Second)
	defer fallback.Stop()
	nextSessionCheck := time.Now().Add(a.config.Heartbeat)
	for {
		if initial || cursor.Seq < page.Floor {
			reason := "cursor_expired"
			if initial {
				reason = "initial"
			}
			initial = false
			// This is the CONSUMED watermark, never the producer head. Pending events
			// committed before this reset will still arrive after they are projected.
			cursor.Seq = page.High
			token := encodeDurableCursor(cursor)
			data, _ := json.Marshal(map[string]string{"reason": reason, "cursor": token})
			if !writeSSE(w, event{Cursor: token, Name: "stream.reset", Data: data}) {
				return
			}
			a.obs.recordDurable("reset", 1)
			page.Items = nil
		}
		for _, e := range page.Items {
			if time.Now().After(nextSessionCheck) {
				if !a.sessionStillValid(r) {
					return
				}
				nextSessionCheck = time.Now().Add(a.config.Heartbeat)
			}
			cursor.Seq = e.Seq
			token := encodeDurableCursor(cursor)
			data, _ := json.Marshal(e)
			if !writeSSE(w, event{Cursor: token, Name: "resource.changed", Data: data}) {
				return
			}
			if e.Kind == "task.updated" {
				// Compatibility snapshot has its own Task.Version and can be newer than
				// this notification. Clients must not depend on receiving both frames.
				record, loadErr := a.store.loadTask(e.ResourceID)
				if loadErr != nil {
					return
				}
				if record.Task.WorkspaceID != scope.WorkspaceID || record.Task.ProjectID != scope.ProjectID {
					return
				}
				payload, _ := json.Marshal(map[string]any{"task": record.Task})
				if !writeSSE(w, event{Cursor: token, Name: "task.updated", Data: payload}) {
					return
				}
			}
		}
		if len(page.Items) == outboxBatchSize {
			page, err = a.store.readEventPage(scope, cursor.Seq)
			if err != nil {
				return
			}
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-wake:
		case <-fallback.C:
		case <-heartbeat.C:
			if !a.sessionStillValid(r) {
				return
			}
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}
		page, err = a.store.readEventPage(scope, cursor.Seq)
		if err != nil {
			return
		}
	}
}
