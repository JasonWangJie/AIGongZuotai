package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	return s.readFeedPage(context.Background(), scope, after)
}

func (s *postgresStore) readFeedPage(readCtx context.Context, scope subscriptionScope, after uint64) (durablePage, error) {
	page := durablePage{Items: []DurableEvent{}}
	err := s.withVerifiedSnapshot(readCtx, s.feedReads, func(ctx context.Context, conn pgx.Tx) error {
		// A single statement sees one MVCC snapshot, including scopes with no feed rows.
		rows, err := conn.Query(ctx, `SELECT i.storage_id::text,coalesce(c.last_seq,0),coalesce(h.floor_seq,0),
			coalesce(e.event_id,''),coalesce(e.project_seq,0),coalesce(e.kind,''),coalesce(e.resource_type,''),
			coalesce(e.resource_id,''),coalesce(e.resource_version,0),coalesce(e.status,''),coalesce(e.action,'')
			FROM studio_storage_identity i
			LEFT JOIN studio_event_consumers c ON c.consumer='project-feed-v1' AND c.workspace_id=$1 AND c.project_id=$2
			LEFT JOIN studio_event_heads h ON h.workspace_id=$1 AND h.project_id=$2
			LEFT JOIN LATERAL (
				SELECT o.event_id,f.project_seq,o.kind,o.resource_type,o.resource_id,o.resource_version,o.status,o.action
				FROM studio_project_event_feed f JOIN studio_outbox_events o ON o.event_id=f.event_id
				WHERE f.workspace_id=$1 AND f.project_id=$2 AND f.project_seq>$3 AND f.project_seq<=c.last_seq
				ORDER BY f.project_seq LIMIT $4
			) e ON true WHERE i.singleton ORDER BY e.project_seq`, scope.WorkspaceID, scope.ProjectID, after, outboxBatchSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e DurableEvent
			e.WorkspaceID, e.ProjectID = scope.WorkspaceID, scope.ProjectID
			if err = rows.Scan(&page.StorageID, &page.High, &page.Floor, &e.EventID, &e.Seq, &e.Kind, &e.ResourceType, &e.ResourceID, &e.ResourceVersion, &e.Status, &e.Action); err != nil {
				return err
			}
			if e.Seq != 0 {
				page.Items = append(page.Items, e)
			}
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if page.StorageID == "" {
			return pgx.ErrNoRows
		}
		return nil
	})
	return page, err
}

func (a *App) durableWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if !a.acquireStream(w, r) {
		return
	}
	defer a.releaseObservedStream(p.Workspace.ID)
	r, stopAccountStream := a.accountStreamContext(r)
	defer stopAccountStream()
	scope := subscriptionScope{WorkspaceID: p.Workspace.ID, ProjectID: p.Project.ID}
	feed, wake, unsubscribe := a.store.notifications.subscribe(scope)
	defer unsubscribe()
	read := func(after uint64) (durablePage, time.Time, error) {
		if a.store.failed.Load() {
			return durablePage{}, time.Time{}, a.store.stopOnDatabaseFailure()
		}
		page, expires, err := feed.page(r.Context(), after, a.store.readFeedPage)
		var op *operationError
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &op) && op.code == "RUN_REPLAY_UNAVAILABLE" {
			return durablePage{}, time.Time{}, failure(503, "PROJECT_FEED_UNAVAILABLE", "暂时无法读取项目通知，请保留原游标后重连")
		}
		return page, expires, err
	}
	page, expires, err := read(0)
	if r.Context().Err() != nil {
		return
	}
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
		page, expires, err = read(cursor.Seq)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		if cursor.Seq > page.High {
			writeError(w, failure(409, "EVENT_CURSOR_AHEAD", "通知游标超出持久记录，请重新同步项目"))
			return
		}
	}
	if r.Context().Err() != nil || !startSSE(w) {
		return
	}
	heartbeat := time.NewTicker(a.config.Heartbeat)
	defer heartbeat.Stop()
	fallback := time.NewTimer(time.Until(expires))
	defer fallback.Stop()

	for {
		if r.Context().Err() != nil {
			return
		}
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
			if r.Context().Err() != nil {
				return
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
				payload, loadErr := feed.taskFrame(r.Context(), e.ResourceID, a.store.readFeedTask)
				if loadErr != nil {
					return
				}
				if r.Context().Err() != nil || !writeSSE(w, event{Cursor: token, Name: "task.updated", Data: payload}) {
					return
				}
			}
		}
		if len(page.Items) == outboxBatchSize {
			page, expires, err = read(cursor.Seq)
			if err != nil {
				return
			}
			continue
		}
		// Every reader checks at the shared snapshot deadline, even if a wake hint was lost.
		fallback.Reset(time.Until(expires))
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-wake:
		case <-fallback.C:
		case <-heartbeat.C:
			if r.Context().Err() != nil {
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
		page, expires, err = read(cursor.Seq)
		if err != nil {
			return
		}
	}
}
