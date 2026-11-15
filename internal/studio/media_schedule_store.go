package studio

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const mediaScheduleColumns = `task_id,workspace_id,provider_mode,lane,channel_key,eligible_at,failure_count,unchanged_count,last_progress,active,version,last_observation_id,server_not_before`

func scanMediaSchedule(row pgx.Row) (*mediaTaskSchedule, error) {
	s := &mediaTaskSchedule{}
	err := row.Scan(&s.TaskID, &s.WorkspaceID, &s.ProviderMode, &s.Lane, &s.ChannelKey, &s.EligibleAt, &s.FailureCount, &s.UnchangedCount, &s.LastProgress, &s.Active, &s.Version, &s.LastObservationID, &s.ServerNotBefore)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}
func loadMediaScheduleSQL(ctx context.Context, q sqlQueryRower, id string) (*mediaTaskSchedule, error) {
	return scanMediaSchedule(q.QueryRow(ctx, `SELECT `+mediaScheduleColumns+` FROM studio_task_schedule WHERE task_id=$1`, id))
}
func loadMediaChannelSQL(ctx context.Context, q sqlQueryRower, scope, lane, key string, lock bool) (mediaChannelRuntime, error) {
	r := mediaChannelRuntime{}
	sql := `SELECT cooldown_until,parked,failure_count,success_count,effective_limit,version,probe_required,probe_id,probe_until FROM studio_media_channel_runtime WHERE scope=$1 AND lane=$2 AND channel_key=$3`
	if lock {
		sql += ` FOR UPDATE`
	}
	err := q.QueryRow(ctx, sql, scope, lane, key).Scan(&r.CooldownUntil, &r.Parked, &r.FailureCount, &r.SuccessCount, &r.EffectiveLimit, &r.Version, &r.ProbeRequired, &r.ProbeID, &r.ProbeUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return r, err
}
func saveMediaChannelSQL(ctx context.Context, tx pgx.Tx, scope, lane, key string, r mediaChannelRuntime) error {
	r.CooldownUntil = mediaScheduleTimestamp(r.CooldownUntil)
	_, err := tx.Exec(ctx, `UPDATE studio_media_channel_runtime SET cooldown_until=$4,parked=$5,failure_count=$6,success_count=$7,effective_limit=$8,version=$9,probe_required=$10,probe_id=$11,probe_until=$12 WHERE scope=$1 AND lane=$2 AND channel_key=$3`, scope, lane, key, r.CooldownUntil, r.Parked, r.FailureCount, r.SuccessCount, r.EffectiveLimit, r.Version, r.ProbeRequired, r.ProbeID, r.ProbeUntil)
	return err
}

func applyMediaChannelObservation(r mediaChannelRuntime, o *mediaScheduleOutcome, c *mediaScheduleCandidate, limit int) mediaChannelRuntime {
	obs := o.Observation
	if obs == nil || obs.Scope != mediaScheduleScope(o.Lane) || obs.Class == "cancelled" || obs.Class == "identity" {
		return r
	}
	if r.EffectiveLimit == 0 {
		r.EffectiveLimit = limit
	}
	r.CooldownUntil = maxTime(r.CooldownUntil, time.Unix(0, 0).UTC())
	if obs.DelayExceeded {
		// Representable long delays keep their exact original deadline. Invalid
		// unrepresentable protocol values park only the task, never permanently
		// disable all future work on this credential.
		r.Parked = false
		r.CooldownUntil = maxTime(r.CooldownUntil, o.At.Add(3*time.Second))
		if !obs.RetryNotBefore.IsZero() {
			r.CooldownUntil = maxTime(r.CooldownUntil, obs.RetryNotBefore)
		}
		r.EffectiveLimit = 1
		r.ProbeRequired = o.Lane == mediaQueryLane
		r.ProbeID = ""
		r.ProbeUntil = nil
		r.Version++
		return r
	}
	if obs.Class == "rate_limit" || obs.Class == "temporary" || obs.Class == "protocol" {
		r.FailureCount = min(30, r.FailureCount+1)
		r.SuccessCount = 0
		d := min(5*time.Minute, 3*time.Second*time.Duration(1<<min(7, r.FailureCount-1)))
		until := o.At.Add(d)
		if obs.HasServerDelay {
			until = maxTime(until, obs.RetryNotBefore)
		}
		r.CooldownUntil = maxTime(r.CooldownUntil, until)
		if obs.Class == "rate_limit" || r.FailureCount >= 3 {
			r.EffectiveLimit = max(1, min(limit, r.EffectiveLimit)/2)
		}
		r.ProbeRequired = o.Lane == mediaQueryLane
		r.ProbeID = ""
		r.ProbeUntil = nil
		r.Version++
		return r
	}
	if obs.Class != "success" {
		return r
	}
	// Only the matching half-open query can recover a failed channel. Ordinary
	// successes that began before a newer failure never erase its cooldown.
	probe := c != nil && c.ProbeID != "" && r.ProbeID == c.ProbeID
	current := c != nil && c.ChannelVersion == r.Version
	if r.ProbeRequired && !probe {
		return r
	}
	if !probe && r.Version > 0 && !current {
		return r
	}
	if probe {
		r.ProbeRequired = false
		r.ProbeID = ""
		r.ProbeUntil = nil
		r.CooldownUntil = maxTime(o.At, time.Unix(0, 0).UTC())
	}
	r.FailureCount = 0
	r.SuccessCount++
	if r.SuccessCount >= 8 {
		r.SuccessCount = 0
		r.EffectiveLimit = min(limit, r.EffectiveLimit+1)
	}
	r.Version++
	return r
}

func checkMediaReconcileScheduleSQL(ctx context.Context, tx pgx.Tx, t *taskRecord) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM studio_tasks WHERE id=$1 FOR UPDATE`, t.Task.ID).Scan(&id); err != nil {
		return err
	}
	s, err := loadMediaScheduleSQL(ctx, tx, t.Task.ID)
	if err != nil || s == nil {
		return err
	}
	r, err := loadMediaChannelSQL(ctx, tx, mediaScheduleScope(mediaLaneFromName(s.Lane)), s.Lane, s.ChannelKey, true)
	if err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT GREATEST($1::timestamptz,$2::timestamptz)<=clock_timestamp()`, s.ServerNotBefore, maxTime(r.CooldownUntil, time.Unix(0, 0))).Scan(&allowed); err != nil {
		return err
	}
	if !allowed || r.Parked {
		return failure(429, "MEDIA_PROVIDER_COOLDOWN", "原服务端等待尚未结束，未重新安排查询")
	}
	return nil
}
func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

// Called only after the task fence and private stage saves, in the same control
// transaction as billing and Outbox. All mutation is on a transaction candidate.
func saveMediaScheduleSQL(ctx context.Context, tx pgx.Tx, t *taskRecord, o *mediaScheduleOutcome) error {
	if _, err := tx.Exec(ctx, `SELECT studio_sync_task_schedule($1)`, t.Task.ID); err != nil {
		return err
	}
	s, err := scanMediaSchedule(tx.QueryRow(ctx, `SELECT `+mediaScheduleColumns+` FROM studio_task_schedule WHERE task_id=$1 FOR UPDATE`, t.Task.ID))
	if err != nil {
		return err
	}
	if s == nil {
		return errors.New("media schedule missing")
	}
	if o != nil && s.LastObservationID != o.ID {
		if o.Observation != nil && o.Observation.HasServerDelay && !o.Observation.RetryNotBefore.IsZero() && (s.Lane == mediaLaneName(o.Lane) || o.Lane == mediaSubmitLane && s.Lane == "query") {
			if _, err = tx.Exec(ctx, `UPDATE studio_task_schedule SET server_not_before=GREATEST(server_not_before,$2) WHERE task_id=$1`, t.Task.ID, mediaScheduleTimestamp(o.Observation.RetryNotBefore)); err != nil {
				return err
			}
		}
		if mediaLaneFromName(s.Lane) == o.Lane && s.Active {
			due, fail, unchanged := mediaScheduleDelay(o, *s)
			due = mediaScheduleTimestamp(due)
			t.NextPoll = due
			if _, err = tx.Exec(ctx, `UPDATE studio_tasks SET next_poll=$2 WHERE id=$1`, t.Task.ID, due); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE studio_task_schedule SET failure_count=$2,unchanged_count=$3,last_progress=$4,last_observation_id=$5 WHERE task_id=$1`, t.Task.ID, fail, unchanged, max(s.LastProgress, min(100, max(0, o.Progress))), o.ID); err != nil {
				return err
			}
		} else {
			if _, err = tx.Exec(ctx, `UPDATE studio_task_schedule SET last_observation_id=$2 WHERE task_id=$1`, t.Task.ID, o.ID); err != nil {
				return err
			}
		}
		obs := o.Observation
		if obs != nil && obs.Scope == mediaScheduleScope(o.Lane) && obs.Class != "cancelled" && obs.Class != "identity" {
			limit := 2
			if t.ScheduleClaim != nil && t.ScheduleClaim.EffectiveLimit > 0 {
				limit = t.ScheduleClaim.EffectiveLimit
			}
			// EffectiveLimit here is the configured hard ceiling, carried from
			// the scheduler; a separate claim field stores adaptive capacity.
			if t.ScheduleClaim != nil && t.ScheduleClaim.HardLimit > 0 {
				limit = t.ScheduleClaim.HardLimit
			}
			scope, lane := mediaScheduleScope(o.Lane), mediaLaneName(o.Lane)
			if _, err = tx.Exec(ctx, `INSERT INTO studio_media_channel_runtime(scope,lane,channel_key,effective_limit) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope, lane, o.ChannelKey, limit); err != nil {
				return err
			}
			r, loadErr := loadMediaChannelSQL(ctx, tx, scope, lane, o.ChannelKey, true)
			if loadErr != nil {
				return loadErr
			}
			r = applyMediaChannelObservation(r, o, t.ScheduleClaim, limit)
			if err = saveMediaChannelSQL(ctx, tx, scope, lane, o.ChannelKey, r); err != nil {
				return err
			}
		}
	}
	t.Schedule, err = loadMediaScheduleSQL(ctx, tx, t.Task.ID)
	return err
}
