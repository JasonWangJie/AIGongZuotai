package studio

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// These are the actual prepared production selectors. LIMIT bounds the keyset
// page before due/lease/cooldown tests; the cursor advances past skipped work.
const mediaScheduleWorkspaceSelectSQL = `SELECT workspace_id,task_cursor FROM studio_workspace_schedule WHERE lane=$1 AND provider_mode=$2 AND active_count>0 AND workspace_id>$3 ORDER BY workspace_id LIMIT $4`

var mediaScheduleTaskSelectSQL = [4]string{
	`SELECT ` + mediaScheduleColumns + ` FROM studio_task_schedule WHERE active AND lane='submit' AND provider_mode=$1 AND workspace_id=$2 AND task_id>$3 ORDER BY task_id LIMIT $4`,
	`SELECT ` + mediaScheduleColumns + ` FROM studio_task_schedule WHERE active AND lane='query' AND provider_mode=$1 AND workspace_id=$2 AND task_id>$3 ORDER BY task_id LIMIT $4`,
	`SELECT ` + mediaScheduleColumns + ` FROM studio_task_schedule WHERE active AND lane='delivery' AND provider_mode=$1 AND workspace_id=$2 AND task_id>$3 ORDER BY task_id LIMIT $4`,
	`SELECT ` + mediaScheduleColumns + ` FROM studio_task_schedule WHERE active AND lane='review' AND provider_mode=$1 AND workspace_id=$2 AND task_id>$3 ORDER BY task_id LIMIT $4`,
}

func (a *App) selectMediaScheduleCandidate(ctx context.Context, conn *pgx.Conn, lane mediaExecutionLane) (*mediaScheduleCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	name, mode := mediaLaneName(lane), a.config.Mode
	var cursor string
	// Cursor changes never change the referenced lane/workspace keys. Keep
	// selectors serialized while allowing the projection's FK KEY SHARE locks:
	// a business writer can already own a workspace row while we wait for it.
	if err = tx.QueryRow(ctx, `SELECT workspace_cursor FROM studio_lane_schedule WHERE lane=$1 AND provider_mode=$2 FOR NO KEY UPDATE`, name, mode).Scan(&cursor); err != nil {
		return nil, err
	}
	type wsPage struct{ id, cursor string }
	var spaces []wsPage
	for pass := 0; pass < 2; pass++ {
		rows, e := tx.Query(ctx, mediaScheduleWorkspaceSelectSQL, name, mode, cursor, mediaScheduleWorkspacePage)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var w wsPage
			if e = rows.Scan(&w.id, &w.cursor); e != nil {
				rows.Close()
				return nil, e
			}
			spaces = append(spaces, w)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if len(spaces) > 0 || cursor == "" {
			break
		}
		cursor = ""
	}
	var selected *mediaScheduleCandidate
	now := time.Now()
	channels := map[string]mediaChannelRuntime{}
	for _, w := range spaces {
		// No task row is locked by this transaction. Business writers may hold
		// task locks while waiting for this short workspace cursor transaction.
		if err = tx.QueryRow(ctx, `SELECT task_cursor FROM studio_workspace_schedule WHERE lane=$1 AND provider_mode=$2 AND workspace_id=$3 FOR NO KEY UPDATE`, name, mode, w.id).Scan(&w.cursor); err != nil {
			return nil, err
		}
		var page []*mediaTaskSchedule
		for pass := 0; pass < 2; pass++ {
			rows, e := tx.Query(ctx, mediaScheduleTaskSelectSQL[lane], mode, w.id, w.cursor, mediaScheduleTaskPage)
			if e != nil {
				return nil, e
			}
			for rows.Next() {
				s, e := scanMediaSchedule(rows)
				if e != nil {
					rows.Close()
					return nil, e
				}
				page = append(page, s)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, e
			}
			if len(page) > 0 || w.cursor == "" {
				break
			}
			w.cursor = ""
		}
		eligibleIDs := []string{}
		for _, s := range page {
			if !now.Before(s.EligibleAt) && !now.Before(s.ServerNotBefore) {
				eligibleIDs = append(eligibleIDs, s.TaskID)
			}
		}
		leases := map[string]bool{}
		if len(eligibleIDs) > 0 {
			rows, e := tx.Query(ctx, `SELECT id,lease_until IS NOT NULL AND lease_until>clock_timestamp() FROM studio_tasks WHERE id=ANY($1::text[])`, eligibleIDs)
			if e != nil {
				return nil, e
			}
			for rows.Next() {
				var id string
				var held bool
				if e = rows.Scan(&id, &held); e != nil {
					rows.Close()
					return nil, e
				}
				leases[id] = held
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, e
			}
		}
		for _, s := range page {
			w.cursor = s.TaskID
			if now.Before(s.EligibleAt) || now.Before(s.ServerNotBefore) {
				a.mediaScheduler.examined(lane, false)
				continue
			}
			r, found := channels[s.ChannelKey]
			if !found {
				var e error
				r, e = loadMediaChannelSQL(ctx, tx, mediaScheduleScope(lane), name, s.ChannelKey, false)
				if e != nil {
					return nil, e
				}
				channels[s.ChannelKey] = r
			}
			cold := r.Parked || now.Before(r.CooldownUntil) || r.ProbeID != "" && r.ProbeUntil != nil && now.Before(*r.ProbeUntil)
			a.mediaScheduler.examined(lane, cold)
			if cold {
				continue
			}
			if leased, exists := leases[s.TaskID]; !exists || leased {
				continue
			}
			selected = &mediaScheduleCandidate{TaskID: s.TaskID, WorkspaceID: s.WorkspaceID, ProviderMode: s.ProviderMode, Lane: lane, ChannelKey: s.ChannelKey, ProjectionVersion: s.Version, ChannelVersion: r.Version, EffectiveLimit: r.EffectiveLimit, HardLimit: a.mediaScheduler.channelLimit(lane)}
			break
		}
		if _, err = tx.Exec(ctx, `UPDATE studio_workspace_schedule SET task_cursor=$4 WHERE lane=$1 AND provider_mode=$2 AND workspace_id=$3`, name, mode, w.id, w.cursor); err != nil {
			return nil, err
		}
		cursor = w.id
		if selected != nil {
			break
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE studio_lane_schedule SET workspace_cursor=$3 WHERE lane=$1 AND provider_mode=$2`, name, mode, cursor); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return selected, nil
}

// Explicit internal steps retain targeting, never a due-time/cooldown bypass.
func (a *App) mediaScheduleCandidateByID(ctx context.Context, q sqlQueryRower, lane mediaExecutionLane, id string) (*mediaScheduleCandidate, error) {
	s, err := loadMediaScheduleSQL(ctx, q, id)
	if err != nil || s == nil {
		return nil, err
	}
	if !s.Active || s.Lane != mediaLaneName(lane) || s.ProviderMode != a.config.Mode || time.Now().Before(s.EligibleAt) || time.Now().Before(s.ServerNotBefore) {
		return nil, nil
	}
	r, err := loadMediaChannelSQL(ctx, q, mediaScheduleScope(lane), s.Lane, s.ChannelKey, false)
	if err != nil {
		return nil, err
	}
	if r.Parked || time.Now().Before(r.CooldownUntil) || r.ProbeID != "" && r.ProbeUntil != nil && time.Now().Before(*r.ProbeUntil) {
		return nil, nil
	}
	return &mediaScheduleCandidate{TaskID: s.TaskID, WorkspaceID: s.WorkspaceID, ProviderMode: s.ProviderMode, Lane: lane, ChannelKey: s.ChannelKey, ProjectionVersion: s.Version, ChannelVersion: r.Version, EffectiveLimit: r.EffectiveLimit, HardLimit: a.mediaScheduler.channelLimit(lane)}, nil
}

func scheduleClaimChanged(err error) bool { return errors.Is(err, pgx.ErrNoRows) || isLeaseLost(err) }
