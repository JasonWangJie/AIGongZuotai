package studio

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type queryWireGuardKey struct{}

func isQueryLeaseBusy(err error) bool {
	var op *operationError
	return errors.As(err, &op) && op.code == "MEDIA_QUERY_LEASE_BUSY"
}

// Only the new pooled query takes these locks. NOWAIT lets the caller retain
// the known HTTP result and retry just the control transaction outside App.mu.
// With both locks held, the subsequent UPDATE evaluates the actual expiry.
func lockPooledQueryForSave(ctx context.Context, conn sqlExecer, token TaskLease) error {
	lock := func(query string, value string) error {
		tag, err := conn.Exec(ctx, query, value)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return queryLeaseBusy()
		}
		if err == nil && tag.RowsAffected() != 1 {
			return leaseLost()
		}
		return err
	}
	if err := lock("SELECT generation FROM studio_task_executor WHERE singleton AND generation=$1 FOR SHARE NOWAIT", token.Generation); err != nil {
		return err
	}
	return lock("SELECT id FROM studio_tasks WHERE id=$1 FOR UPDATE NOWAIT", token.TaskID)
}

// Pooled original-handle queries own a connection only for the admission or
// renewal transaction. A blocked GET retains its execution and scope permits.
func (p *taskLeasePool) renewPooledQuery(ctx context.Context, token TaskLease) error {
	if token.Lane != mediaQueryLane || token.queryHandleJSON == "" || token.queryStatus == "" || token.Version == 0 {
		return leaseLost()
	}
	wait, cancel := context.WithTimeout(ctx, min(p.duration, 5*time.Second))
	defer cancel()
	timing := token.queryTiming
	started := timing.start()
	conn, err := p.acquireLane(wait, mediaQueryLane)
	timing.end(queryLeasePoolWait, started)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return queryLeaseBusy()
	}
	defer p.release(conn)
	if err := wait.Err(); err != nil {
		return queryLeaseBusy()
	}
	// A normal HTTP completion cancels its concurrent heartbeat. Let a short
	// transaction already on a physical connection finish or roll back cleanly,
	// instead of aborting the socket that the next query will borrow.
	remaining, _ := wait.Deadline()
	left := time.Until(remaining)
	if left < 5*time.Millisecond {
		return queryLeaseBusy()
	}
	sqlCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), left+time.Second)
	defer stop()
	started = timing.start()
	err = pgx.BeginFunc(sqlCtx, conn, func(tx pgx.Tx) error {
		setRemainingTimeout := func() error {
			ms := time.Until(remaining).Milliseconds()
			if ms < 2 || ctx.Err() != nil {
				return queryLeaseBusy()
			}
			_, e := tx.Exec(sqlCtx, "SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$1,true)", fmt.Sprintf("%dms", ms-1))
			return e
		}
		if err := setRemainingTimeout(); err != nil {
			return err
		}
		var current string
		if err := tx.QueryRow(sqlCtx, "SELECT generation FROM studio_task_executor WHERE singleton FOR SHARE").Scan(&current); err != nil {
			return err
		}
		if current != token.Generation {
			return leaseLost()
		}
		if err := setRemainingTimeout(); err != nil {
			return err
		}
		if e := tx.QueryRow(sqlCtx, "SELECT id FROM studio_tasks WHERE id=$1 FOR UPDATE", token.TaskID).Scan(&current); e != nil {
			return e
		}
		interval := fmt.Sprintf("%d milliseconds", p.duration.Milliseconds())
		if token.queryDispatch != nil {
			if err := setRemainingTimeout(); err != nil {
				return err
			}
			if e := tx.QueryRow(sqlCtx, "SELECT task_id FROM studio_task_schedule WHERE task_id=$1 FOR SHARE", token.TaskID).Scan(&current); e != nil {
				return e
			}
		}
		if token.ProbeID != "" || token.queryDispatch != nil {
			// Obtain the channel row before the final task expiry check. A lock
			// waiter can outlive either the probe deadline or the task lease.
			if err := setRemainingTimeout(); err != nil {
				return err
			}
			if e := tx.QueryRow(sqlCtx, `SELECT channel_key FROM studio_media_channel_runtime WHERE scope=$1 AND lane=$2 AND channel_key=$3 FOR UPDATE`, mediaScheduleScope(token.Lane), mediaLaneName(token.Lane), token.ChannelKey).Scan(&current); e != nil {
				return e
			}
		}
		if c := token.queryDispatch; c != nil {
			if err := setRemainingTimeout(); err != nil {
				return err
			}
			// The handoff runs after the selector/claim transaction. Recheck both
			// locked identities at database time, including cooldowns established
			// by intervening responses from this very same frozen channel.
			limit := c.HardLimit
			if c.EffectiveLimit > 0 {
				limit = min(limit, c.EffectiveLimit)
			}
			var allowed bool
			if e := tx.QueryRow(sqlCtx, `SELECT s.active AND s.version=$2 AND s.workspace_id=$3 AND s.provider_mode=$4
 AND s.channel_key=$5 AND GREATEST(s.eligible_at,s.server_not_before,r.cooldown_until)<=clock_timestamp()
 AND NOT r.parked AND r.effective_limit >= $6 AND
 (CASE WHEN $7='' THEN NOT r.probe_required AND (r.probe_id='' OR r.probe_until<=clock_timestamp())
 ELSE r.probe_required AND r.probe_id=$7 AND r.probe_until>clock_timestamp() END)
 FROM studio_task_schedule s JOIN studio_media_channel_runtime r ON r.scope='gateway' AND r.lane='query' AND r.channel_key=s.channel_key
 WHERE s.task_id=$1 AND s.lane='query'`, token.TaskID, c.ProjectionVersion, c.WorkspaceID, c.ProviderMode, token.ChannelKey, limit, token.ProbeID).Scan(&allowed); e != nil {
				return e
			}
			if !allowed {
				return leaseLost()
			}
		}
		if token.ProbeID != "" {
			tag, e := tx.Exec(sqlCtx, `UPDATE studio_media_channel_runtime SET probe_until=clock_timestamp()+$1::interval WHERE scope=$2 AND lane=$3 AND channel_key=$4 AND probe_id=$5 AND probe_until>clock_timestamp()`, interval, mediaScheduleScope(token.Lane), mediaLaneName(token.Lane), token.ChannelKey, token.ProbeID)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return leaseLost()
			}
		}
		if err := setRemainingTimeout(); err != nil {
			return err
		}
		// These predicates run after all potentially blocking row lock reads.
		// The full original JSON includes the endpoint/credential identity and
		// preserves the shape and optional fields of accepted legacy handles.
		tag, e := tx.Exec(sqlCtx, `UPDATE studio_tasks SET lease_until=clock_timestamp()+$1::interval
 WHERE id=$2 AND lease_owner=$3 AND lease_epoch=$4 AND lease_until>clock_timestamp()
 AND (document->>'version')::bigint=$5 AND handle=$6::jsonb AND document->>'status'=$7`, interval, token.TaskID, token.Owner, token.Epoch, token.Version, token.queryHandleJSON, token.queryStatus)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return leaseLost()
		}
		if wait.Err() != nil {
			return queryLeaseBusy()
		}
		return nil
	})
	timing.end(queryLeaseTransaction, started)
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) {
		return leaseLost()
	}
	if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "57014") {
		return queryLeaseBusy()
	}
	return err
}

func queryLeaseBusy() error {
	return failure(503, "MEDIA_QUERY_LEASE_BUSY", "原任务查询的租约核验未能按时完成")
}
