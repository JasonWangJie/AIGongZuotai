package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (p *taskLeasePool) claimScheduled(ctx context.Context, conn *pgx.Conn, c mediaScheduleCandidate) (*taskRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var generation string
	if err = tx.QueryRow(ctx, `SELECT generation FROM studio_task_executor WHERE singleton=true FOR SHARE`).Scan(&generation); err != nil {
		return nil, err
	}
	if generation != p.generation {
		return nil, nil
	}
	var doc, input, handle, submission []byte
	r := &taskRecord{}
	var epoch int64
	var available bool
	err = tx.QueryRow(ctx, `SELECT document,input,handle,submission,next_poll,lease_epoch,lease_until IS NULL OR lease_until<=clock_timestamp() FROM studio_tasks WHERE id=$1 FOR UPDATE SKIP LOCKED`, c.TaskID).Scan(&doc, &input, &handle, &submission, &r.NextPoll, &epoch, &available)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, nil
	}
	s, err := scanMediaSchedule(tx.QueryRow(ctx, `SELECT `+mediaScheduleColumns+` FROM studio_task_schedule WHERE task_id=$1 FOR UPDATE`, c.TaskID))
	if err != nil {
		return nil, err
	}
	if s == nil || !s.Active || s.Version != c.ProjectionVersion || s.ChannelKey != c.ChannelKey || s.WorkspaceID != c.WorkspaceID || s.ProviderMode != c.ProviderMode || s.Lane != mediaLaneName(c.Lane) {
		return nil, nil
	}
	var due bool
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT GREATEST($1::timestamptz,$2::timestamptz)<=clock_timestamp(),clock_timestamp()`, s.EligibleAt, s.ServerNotBefore).Scan(&due, &now); err != nil {
		return nil, err
	}
	if !due {
		return nil, nil
	}
	for _, v := range []struct {
		raw []byte
		dst any
	}{{doc, &r.Task}, {input, &r.Input}, {handle, &r.Handle}, {submission, &r.Submission}} {
		if len(v.raw) > 0 {
			if err = json.Unmarshal(v.raw, v.dst); err != nil {
				return nil, err
			}
		}
	}
	if terminal(r.Task.Status) || r.Task.Status == "SUBMIT_UNKNOWN" || r.Task.Status == "RECONCILE_REQUIRED" || r.Task.ProviderMode != c.ProviderMode {
		return nil, nil
	}
	r.Delivery, err = loadMediaDeliverySQL(ctx, tx, c.TaskID)
	if err != nil {
		return nil, err
	}
	r.ReviewDispatch, err = loadMediaReviewDispatchSQL(ctx, tx, c.TaskID)
	if err != nil {
		return nil, err
	}
	if r.Task.Moderation != nil {
		r.MediaModeration, err = loadTaskMediaModerationSQL(ctx, tx, c.TaskID)
		if err != nil {
			return nil, err
		}
	}
	if mediaLaneForTask(r) != c.Lane {
		return nil, nil
	}
	key, keyErr := mediaFrozenChannel(r, c.Lane)
	if keyErr != nil || key != c.ChannelKey {
		return nil, failure(409, "MEDIA_SCHEDULE_IDENTITY_CHANGED", "原执行渠道身份不一致，保持隔离")
	}
	// A missing row cannot be locked by SELECT FOR UPDATE. Materialize the
	// healthy scheduling baseline first: a concurrent first 429 INSERT must
	// serialize before this claim is allowed to observe/lock channel state.
	limit := c.HardLimit
	if limit < 1 || limit > 32 {
		return nil, errors.New("invalid media channel hard limit")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_media_channel_runtime(scope,lane,channel_key,effective_limit) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, mediaScheduleScope(c.Lane), s.Lane, c.ChannelKey, limit); err != nil {
		return nil, err
	}
	channel, err := loadMediaChannelSQL(ctx, tx, mediaScheduleScope(c.Lane), s.Lane, c.ChannelKey, true)
	if err != nil {
		return nil, err
	}
	if channel.Parked || now.Before(channel.CooldownUntil) || channel.ProbeID != "" && channel.ProbeUntil != nil && now.Before(*channel.ProbeUntil) {
		return nil, nil
	}
	reservedLimit := c.EffectiveLimit
	if reservedLimit == 0 {
		reservedLimit = c.HardLimit
	}
	if channel.EffectiveLimit > 0 && channel.EffectiveLimit < reservedLimit {
		return nil, nil
	}
	c.ChannelVersion = channel.Version
	if channel.ProbeRequired && c.Lane == mediaQueryLane {
		c.ProbeID = newID("media_probe")
		until := now.Add(p.duration)
		channel.ProbeID = c.ProbeID
		channel.ProbeUntil = &until
		channel.Version++
		c.ChannelVersion = channel.Version
		if err = saveMediaChannelSQL(ctx, tx, mediaScheduleScope(c.Lane), s.Lane, c.ChannelKey, channel); err != nil {
			return nil, err
		}
	}
	lease := &TaskLease{TaskID: r.Task.ID, Owner: newID("worker"), Epoch: epoch + 1, Generation: p.generation, Version: r.Task.Version, ProbeID: c.ProbeID, ChannelKey: c.ChannelKey, Lane: c.Lane}
	err = tx.QueryRow(ctx, `UPDATE studio_tasks SET lease_owner=$2,lease_epoch=lease_epoch+1,lease_until=clock_timestamp()+$3::interval WHERE id=$1 RETURNING lease_until`, c.TaskID, lease.Owner, fmt.Sprintf("%d milliseconds", p.duration.Milliseconds())).Scan(&lease.Until)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	r.Lease = lease
	r.LeaseStatus = r.Task.Status
	r.InFlight = true
	r.Schedule = s
	r.ScheduleClaim = &c
	return r, nil
}
