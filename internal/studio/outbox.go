package studio

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	outboxPendingLimit    = 10000
	outboxBatchSize       = 100
	eventProjectRetention = 2048
	eventGlobalRetention  = 100000
)

// DurableEvent is deliberately metadata only. Never add prompts, outputs,
// arbitrary audit details, credential identities or storage locators here.
type DurableEvent struct {
	EventID         string `json:"event_id"`
	Seq             uint64 `json:"seq"`
	Kind            string `json:"kind"`
	ResourceType    string `json:"resource_type"`
	ResourceID      string `json:"resource_id"`
	ResourceVersion uint64 `json:"resource_version"`
	Status          string `json:"status"`
	Action          string `json:"action"`
	WorkspaceID     string `json:"-"`
	ProjectID       string `json:"-"`
}

type outboxNotifications struct {
	mu       sync.Mutex
	watchers map[chan struct{}]subscriptionScope
}

func appendOutboxSQL(ctx context.Context, tx pgx.Tx, key string, e DurableEvent) error {
	// The global row is a bounded pending counter, not a commit cursor. All
	// producers/consumers lock it before touching project sequence heads.
	var pending int
	if err := tx.QueryRow(ctx, "SELECT pending FROM studio_outbox_limits WHERE singleton FOR UPDATE").Scan(&pending); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_outbox_events WHERE event_key=$1)", key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if pending >= outboxPendingLimit {
		return failure(503, "EVENT_BACKLOG_FULL", "持久通知队列已满，本次变更未受理；请稍后使用原请求重试")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO studio_event_heads(workspace_id,project_id) VALUES($1,$2) ON CONFLICT DO NOTHING", e.WorkspaceID, e.ProjectID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, "UPDATE studio_event_heads SET last_seq=last_seq+1 WHERE workspace_id=$1 AND project_id=$2 RETURNING last_seq", e.WorkspaceID, e.ProjectID).Scan(&e.Seq); err != nil {
		return err
	}
	if e.EventID == "" {
		e.EventID = newID("evt")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO studio_outbox_events(event_id,event_key,workspace_id,project_id,project_seq,kind,resource_type,resource_id,resource_version,status,action) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, e.EventID, key, e.WorkspaceID, e.ProjectID, e.Seq, e.Kind, e.ResourceType, e.ResourceID, e.ResourceVersion, e.Status, e.Action); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE studio_outbox_limits SET pending=pending+1 WHERE singleton")
	return err
}

func appendWorkflowOutboxSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, version uint64, status string) error {
	return appendOutboxSQL(ctx, tx, "workflow:"+id+":"+strconv.FormatUint(version, 10), DurableEvent{
		WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Kind: "workflow.updated",
		ResourceType: "workflow", ResourceID: id, ResourceVersion: version, Status: status,
	})
}

func appendTaskOutboxSQL(ctx context.Context, tx pgx.Tx, t Task) error {
	if err := workflowImageOutcomeSQL(ctx, tx, t); err != nil {
		return err
	}
	return appendOutboxSQL(ctx, tx, "task:"+t.ID+":"+strconv.FormatUint(t.Version, 10), DurableEvent{WorkspaceID: t.WorkspaceID, ProjectID: t.ProjectID, Kind: "task.updated", ResourceType: "task", ResourceID: t.ID, ResourceVersion: t.Version, Status: t.Status})
}
func appendRunOutboxSQL(ctx context.Context, tx pgx.Tx, r Run) error {
	if err := workflowTextOutcomeSQL(ctx, tx, r); err != nil {
		return err
	}
	return appendOutboxSQL(ctx, tx, "run:"+r.ID+":"+r.Status+":"+strconv.FormatUint(r.Seq, 10), DurableEvent{WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID, Kind: "run.updated", ResourceType: "run", ResourceID: r.ID, ResourceVersion: r.Seq, Status: r.Status})
}

// projectOutboxBatch commits the durable effect, deduplication and cursor
// together. Waking an HTTP reader is optional and is never the durable effect.
func (s *postgresStore) projectOutboxBatch() (int, error) {
	count := 0
	scopes := map[subscriptionScope]bool{}
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		var pending, retained int
		if err = tx.QueryRow(ctx, "SELECT pending,retained FROM studio_outbox_limits WHERE singleton FOR UPDATE").Scan(&pending, &retained); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT event_id,workspace_id,project_id,project_seq FROM studio_outbox_events WHERE projected_at IS NULL ORDER BY queue_order LIMIT $1`, outboxBatchSize)
		if err != nil {
			return err
		}
		var batch []DurableEvent
		for rows.Next() {
			var e DurableEvent
			if err = rows.Scan(&e.EventID, &e.WorkspaceID, &e.ProjectID, &e.Seq); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range batch {
			if _, err = tx.Exec(ctx, "INSERT INTO studio_event_consumers(consumer,workspace_id,project_id) VALUES('project-feed-v1',$1,$2) ON CONFLICT DO NOTHING", e.WorkspaceID, e.ProjectID); err != nil {
				return err
			}
			var previous uint64
			if err = tx.QueryRow(ctx, "SELECT last_seq FROM studio_event_consumers WHERE consumer='project-feed-v1' AND workspace_id=$1 AND project_id=$2 FOR UPDATE", e.WorkspaceID, e.ProjectID).Scan(&previous); err != nil {
				return err
			}
			if e.Seq > previous {
				if e.Seq != previous+1 {
					return errors.New("outbox project sequence gap; refusing to advance consumer")
				}
				tag, insertErr := tx.Exec(ctx, "INSERT INTO studio_project_event_feed(event_id,workspace_id,project_id,project_seq) VALUES($1,$2,$3,$4) ON CONFLICT(event_id) DO NOTHING", e.EventID, e.WorkspaceID, e.ProjectID, e.Seq)
				if insertErr != nil {
					return insertErr
				}
				retained += int(tag.RowsAffected())
				if _, err = tx.Exec(ctx, "UPDATE studio_event_consumers SET last_seq=$3,updated_at=clock_timestamp() WHERE consumer='project-feed-v1' AND workspace_id=$1 AND project_id=$2", e.WorkspaceID, e.ProjectID, e.Seq); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, "UPDATE studio_outbox_events SET projected_at=clock_timestamp() WHERE event_id=$1", e.EventID); err != nil {
				return err
			}
			count++
			scopes[subscriptionScope{WorkspaceID: e.WorkspaceID, ProjectID: e.ProjectID}] = true
		}
		for scope := range scopes {
			rows, err = tx.Query(ctx, `DELETE FROM studio_outbox_events WHERE workspace_id=$1 AND project_id=$2 AND projected_at IS NOT NULL AND project_seq<=(SELECT last_seq-$3 FROM studio_event_consumers WHERE consumer='project-feed-v1' AND workspace_id=$1 AND project_id=$2) RETURNING project_seq`, scope.WorkspaceID, scope.ProjectID, eventProjectRetention)
			if err != nil {
				return err
			}
			var floor uint64
			for rows.Next() {
				var seq uint64
				if err = rows.Scan(&seq); err != nil {
					rows.Close()
					return err
				}
				if seq > floor {
					floor = seq
				}
				retained--
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if floor > 0 {
				if _, err = tx.Exec(ctx, "UPDATE studio_event_heads SET floor_seq=greatest(floor_seq,$3) WHERE workspace_id=$1 AND project_id=$2", scope.WorkspaceID, scope.ProjectID, floor); err != nil {
					return err
				}
			}
		}
		if retained > eventGlobalRetention {
			rows, err = tx.Query(ctx, `DELETE FROM studio_outbox_events WHERE event_id IN (SELECT event_id FROM studio_outbox_events WHERE projected_at IS NOT NULL ORDER BY queue_order LIMIT $1) RETURNING workspace_id,project_id,project_seq`, retained-eventGlobalRetention)
			if err != nil {
				return err
			}
			floors := map[subscriptionScope]uint64{}
			for rows.Next() {
				var scope subscriptionScope
				var seq uint64
				if err = rows.Scan(&scope.WorkspaceID, &scope.ProjectID, &seq); err != nil {
					rows.Close()
					return err
				}
				if seq > floors[scope] {
					floors[scope] = seq
				}
				retained--
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for scope, floor := range floors {
				if _, err = tx.Exec(ctx, "UPDATE studio_event_heads SET floor_seq=greatest(floor_seq,$3) WHERE workspace_id=$1 AND project_id=$2", scope.WorkspaceID, scope.ProjectID, floor); err != nil {
					return err
				}
				scopes[scope] = true
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE studio_outbox_limits SET pending=pending-$1,retained=$2 WHERE singleton", count, retained); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err == nil && count > 0 {
		s.notifyOutbox(scopes)
	}
	return count, err
}

func (s *postgresStore) prepareOutbox() error {
	for i := 0; i <= outboxPendingLimit/outboxBatchSize; i++ {
		n, err := s.projectOutboxBatch()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
	return fmt.Errorf("outbox startup drain exceeded bounded queue")
}
func (a *App) dispatchOutbox() {
	defer a.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			n, err := a.store.projectOutboxBatch()
			if err != nil {
				return
			}
			a.obs.recordDurable("projected", uint64(n))
		}
	}
}
func (s *postgresStore) subscribeOutbox(scope subscriptionScope) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.notifications.mu.Lock()
	if s.notifications.watchers == nil {
		s.notifications.watchers = make(map[chan struct{}]subscriptionScope)
	}
	s.notifications.watchers[ch] = scope
	s.notifications.mu.Unlock()
	return ch, func() { s.notifications.mu.Lock(); delete(s.notifications.watchers, ch); s.notifications.mu.Unlock() }
}
func (s *postgresStore) notifyOutbox(scopes map[subscriptionScope]bool) {
	s.notifications.mu.Lock()
	defer s.notifications.mu.Unlock()
	for ch, scope := range s.notifications.watchers {
		if scopes[scope] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}
