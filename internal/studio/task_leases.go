package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// TaskLease is never sent to users. Version is the expected business version;
// epoch/generation protect ownership independently of that public version.
type TaskLease struct {
	ProbeID    string
	ChannelKey string
	Lane       mediaExecutionLane
	TaskID     string
	Owner      string
	Epoch      int64
	Until      time.Time
	Generation string
	Version    uint64
}

type taskLeasePool struct {
	connections    [4]chan *pgx.Conn
	connectionLane map[*pgx.Conn]mediaExecutionLane
	all            []*pgx.Conn
	generation     string
	duration       time.Duration
	closeOnce      sync.Once
}

func leaseLost() error {
	return failure(409, "TASK_LEASE_LOST", "任务执行租约或版本已变化，迟到结果已丢弃")
}
func isLeaseLost(err error) bool {
	var op *operationError
	return errors.As(err, &op) && op.code == "TASK_LEASE_LOST"
}

// Called inside the existing control connection's transaction, followed by
// catalog, audit and outbox writes. Losing a lease is not a database outage.
func writeTaskWithLeaseSQL(ctx context.Context, conn sqlExecer, t *taskRecord) error {
	doc, err := json.Marshal(copyTask(t.Task))
	if err != nil {
		return err
	}
	handle, err := json.Marshal(t.Handle)
	if err != nil {
		return err
	}
	submission, err := json.Marshal(t.Submission)
	if err != nil {
		return err
	}
	args := []any{t.Task.ID, doc, handle, submission, t.NextPoll, t.Task.UpdatedAt, t.Task.WorkspaceID}
	query := `UPDATE studio_tasks SET document=$2,handle=$3,submission=$4,next_poll=$5,updated_at=$6,lease_epoch=lease_epoch+1,lease_owner='',lease_until=NULL WHERE id=$1 AND workspace_id=$7`
	if l := t.Lease; l != nil {
		query = `WITH gate AS MATERIALIZED (SELECT generation FROM studio_task_executor WHERE singleton=true AND generation=$10 FOR SHARE)
UPDATE studio_tasks SET document=$2,handle=$3,submission=$4,next_poll=$5,updated_at=$6,
lease_epoch=lease_epoch+CASE WHEN $13 THEN 1 ELSE 0 END,
lease_owner=CASE WHEN $13 THEN '' ELSE lease_owner END,
lease_until=CASE WHEN $13 THEN NULL ELSE lease_until END
WHERE id=$1 AND workspace_id=$7 AND lease_owner=$8 AND lease_epoch=$9 AND lease_until>clock_timestamp()
AND EXISTS(SELECT 1 FROM gate)
AND (document->>'version')::bigint=$11 AND document->>'status'=$12`
		// The original business status is retained before a mutation advances it.
		args = append(args, l.Owner, l.Epoch, l.Generation, l.Version, t.leasePreviousStatus(), t.LeaseComplete)
	}
	tag, err := conn.Exec(ctx, query, args...)
	if err == nil && tag.RowsAffected() != 1 {
		if t.Lease != nil {
			return leaseLost()
		}
		return fmt.Errorf("task missing during control checkpoint")
	}
	return err
}

func (t *taskRecord) leasePreviousStatus() string { return t.LeaseStatus }

func (a *App) initTaskLeases() error {
	// Some fault-injection fixtures initialize only the control/lease layer.
	// Apply the same bounds here so no internal caller creates a zero-capacity
	// connection pool or runs a claimed step without an execution budget.
	if err := a.config.MediaScheduling.normalize(a.config.TaskWorkers); err != nil {
		return err
	}
	if a.mediaExecution == nil {
		a.mediaExecution = newMediaExecutionLanes(a.config.MediaScheduling)
	}
	if a.mediaScheduler == nil {
		a.mediaScheduler = newMediaScheduler(a.config.MediaScheduling)
	}
	pool := &taskLeasePool{connections: [4]chan *pgx.Conn{make(chan *pgx.Conn, a.config.MediaScheduling.SubmitWorkers), make(chan *pgx.Conn, a.config.MediaScheduling.QueryWorkers), make(chan *pgx.Conn, a.config.MediaScheduling.DeliveryWorkers), make(chan *pgx.Conn, a.config.MediaScheduling.ReviewWorkers)}, connectionLane: make(map[*pgx.Conn]mediaExecutionLane), generation: newID("executor"), duration: a.config.TaskLeaseDuration}
	if err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "UPDATE studio_task_executor SET generation=$1 WHERE singleton=true", pool.generation); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}); err != nil {
		return err
	}
	// The short generation transaction already fenced every old executor.
	// Clear obsolete ownership in bounded transactions before opening workers.
	for after := ""; ; {
		var ids []string
		var cleared int64
		err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			rows, err := tx.Query(ctx, "SELECT id FROM studio_tasks WHERE id>$1 AND lease_owner<>'' ORDER BY id LIMIT $2", after, recoveryMediaPageRecords)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(ids) > 0 {
				tag, err := tx.Exec(ctx, "UPDATE studio_tasks SET lease_epoch=lease_epoch+1,lease_owner='',lease_until=NULL WHERE id=ANY($1::text[]) AND lease_owner<>''", ids)
				if err != nil {
					return err
				}
				cleared = tag.RowsAffected()
			}
			return tx.Commit(ctx)
		})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		a.recoveryStats.LeasesCleared += cleared
		after = ids[len(ids)-1]
	}
	for _, lane := range []mediaExecutionLane{mediaSubmitLane, mediaQueryLane, mediaDeliveryLane, mediaReviewLane} {
		for range cap(pool.connections[lane]) {
			ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
			conn, err := pgx.Connect(ctx, a.config.DatabaseURL)
			cancel()
			if err != nil {
				pool.close()
				return errors.New("task lease database connections unavailable")
			}
			pool.all = append(pool.all, conn)
			pool.connectionLane[conn] = lane
			pool.connections[lane] <- conn
		}
	}
	a.taskLeases = pool
	return nil
}

func (p *taskLeasePool) close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		for _, conn := range p.all {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = conn.Close(ctx)
			cancel()
		}
	})
}
func (p *taskLeasePool) acquire(ctx context.Context) (*pgx.Conn, error) {
	select {
	case conn := <-p.connections[mediaSubmitLane]:
		return conn, nil
	case conn := <-p.connections[mediaQueryLane]:
		return conn, nil
	case conn := <-p.connections[mediaDeliveryLane]:
		return conn, nil
	case conn := <-p.connections[mediaReviewLane]:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (p *taskLeasePool) acquireLane(ctx context.Context, lane mediaExecutionLane) (*pgx.Conn, error) {
	select {
	case conn := <-p.connections[lane]:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (p *taskLeasePool) tryAcquireLane(lane mediaExecutionLane) (*pgx.Conn, bool) {
	select {
	case conn := <-p.connections[lane]:
		return conn, true
	default:
		return nil, false
	}
}
func (p *taskLeasePool) release(conn *pgx.Conn) { p.connections[p.connectionLane[conn]] <- conn }

// Worker-only connections change lease metadata, never task business results.
// The optional ID bypasses due-time only for explicit internal step execution;
// production workers always pass an empty ID and consume the durable due queue.
func (p *taskLeasePool) claim(ctx context.Context, conn *pgx.Conn, mode, id string, lanes ...mediaExecutionLane) (*taskRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// An absent selector is retained only for independent lease/fence tests.
	// Every production execution path supplies a reserved lane.
	laneFilter := 0
	if len(lanes) > 0 {
		if lanes[0] > mediaReviewLane {
			return nil, errors.New("invalid media execution lane")
		}
		laneFilter = int(lanes[0]) + 1
	}
	query := `WITH gate AS MATERIALIZED (SELECT generation FROM studio_task_executor WHERE singleton=true AND generation=$3 FOR SHARE), picked AS (
 SELECT id FROM studio_tasks
 WHERE ($1='' OR id=$1) AND ($1<>'' OR next_poll<=clock_timestamp())
 AND document->>'provider_mode'=$2
 AND ($6::int=0 OR ($6::int<4 AND NOT EXISTS(SELECT 1 FROM studio_media_review_dispatch r WHERE r.task_id=studio_tasks.id) AND (($6::int=1 AND COALESCE(handle->>'ID','')='' AND NOT EXISTS(SELECT 1 FROM studio_media_deliveries d WHERE d.task_id=studio_tasks.id)) OR ($6::int=2 AND COALESCE(handle->>'ID','')<>'' AND NOT EXISTS(SELECT 1 FROM studio_media_deliveries d WHERE d.task_id=studio_tasks.id)) OR ($6::int=3 AND EXISTS(SELECT 1 FROM studio_media_deliveries d WHERE d.task_id=studio_tasks.id AND d.state='READY')))) OR ($6::int=4 AND EXISTS(SELECT 1 FROM studio_media_review_dispatch r WHERE r.task_id=studio_tasks.id AND r.state IN('READY','STARTED'))))
 AND document->>'status' NOT IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED')
 AND (lease_until IS NULL OR lease_until<=clock_timestamp())
	 AND EXISTS(SELECT 1 FROM gate)
 ORDER BY next_poll,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE studio_tasks t SET lease_owner=$4,lease_epoch=t.lease_epoch+1,lease_until=clock_timestamp()+$5::interval
FROM picked WHERE t.id=picked.id
RETURNING t.document,t.input,t.handle,t.submission,t.next_poll,t.lease_epoch,t.lease_until`
	owner := newID("worker")
	record := &taskRecord{}
	lease := &TaskLease{Owner: owner, Generation: p.generation}
	var doc, input, handle, submission []byte
	err := conn.QueryRow(ctx, query, id, mode, p.generation, owner, fmt.Sprintf("%d milliseconds", p.duration.Milliseconds()), laneFilter).Scan(&doc, &input, &handle, &submission, &record.NextPoll, &lease.Epoch, &lease.Until)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, pair := range []struct {
		data []byte
		out  any
	}{{doc, &record.Task}, {input, &record.Input}, {handle, &record.Handle}, {submission, &record.Submission}} {
		if len(pair.data) > 0 {
			if err = json.Unmarshal(pair.data, pair.out); err != nil {
				return nil, err
			}
		}
	}
	lease.TaskID = record.Task.ID
	if record.Task.Moderation != nil {
		record.MediaModeration, err = loadTaskMediaModerationSQL(ctx, conn, record.Task.ID)
		if err != nil {
			return nil, err
		}
	}
	lease.Version = record.Task.Version
	record.Delivery, err = loadMediaDeliverySQL(ctx, conn, record.Task.ID)
	if err != nil {
		return nil, err
	}
	record.ReviewDispatch, err = loadMediaReviewDispatchSQL(ctx, conn, record.Task.ID)
	if err != nil {
		return nil, err
	}
	record.Lease = lease
	record.LeaseStatus = record.Task.Status
	record.InFlight = true
	return record, nil
}

func (p *taskLeasePool) renew(ctx context.Context, conn *pgx.Conn, lease TaskLease) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tag, err := conn.Exec(ctx, `WITH gate AS MATERIALIZED (SELECT generation FROM studio_task_executor WHERE singleton=true AND generation=$4 FOR SHARE) UPDATE studio_tasks SET lease_until=clock_timestamp()+$1::interval WHERE id=$5 AND lease_owner=$2 AND lease_epoch=$3 AND lease_until>clock_timestamp() AND EXISTS(SELECT 1 FROM gate)`, fmt.Sprintf("%d milliseconds", p.duration.Milliseconds()), lease.Owner, lease.Epoch, lease.Generation, lease.TaskID)
	if err == nil && tag.RowsAffected() != 1 {
		return leaseLost()
	}
	if err == nil && lease.ProbeID != "" {
		_, err = conn.Exec(ctx, `UPDATE studio_media_channel_runtime SET probe_until=clock_timestamp()+$1::interval WHERE scope=$2 AND lane=$3 AND channel_key=$4 AND probe_id=$5`, fmt.Sprintf("%d milliseconds", p.duration.Milliseconds()), mediaScheduleScope(lease.Lane), mediaLaneName(lease.Lane), lease.ChannelKey, lease.ProbeID)
	}
	return err
}
