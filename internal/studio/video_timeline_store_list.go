package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

func (a *App) listVideoTimelines(p *principal, project, cursor string, limit int) (out VideoTimelinePage, err error) {
	limit, err = presentationPageLimit(limit)
	if err != nil {
		return out, err
	}
	err = a.videoTimelineTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		var c presentationCursor
		if cursor != "" {
			if len(cursor) > 1024 {
				return failure(400, "INVALID_CURSOR", "剪辑稿分页游标无效")
			}
			raw, e := base64.RawURLEncoding.DecodeString(cursor)
			if e != nil || json.Unmarshal(raw, &c) != nil || c.Actor != p.User.ID || c.Project != project || !validOperationsID(c.ID, "vtl") || c.Created.Year() < 2000 || c.Created.Year() > 9999 {
				return failure(400, "INVALID_CURSOR", "剪辑稿分页游标无效")
			}
		}
		rows, e := tx.Query(ctx, `SELECT h.id,h.project_id,h.version,v.title,v.clip_count,h.created_at
		 FROM studio_video_timelines h JOIN studio_video_timeline_versions v ON (v.timeline_id,v.version)=(h.id,h.version)
		 WHERE h.workspace_id=$1 AND h.project_id=$2 AND ($3::text='' OR (h.created_at,h.id)<($4,$3))
		 ORDER BY h.created_at DESC,h.id DESC LIMIT $5`, p.Workspace.ID, project, c.ID, c.Created, limit+1)
		if e != nil {
			return e
		}
		out.Items, e = pgx.CollectRows(rows, pgx.RowToStructByPos[VideoTimelineSummary])
		if e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			raw, _ := json.Marshal(presentationCursor{ID: last.ID, Created: last.CreatedAt, Project: project, Actor: p.User.ID})
			next := base64.RawURLEncoding.EncodeToString(raw)
			out.NextCursor = &next
		}
		return nil
	})
	return
}
func (a *App) videoTimelineHistory(p *principal, project, id string, before uint64, limit int) (out VideoTimelineHistoryPage, err error) {
	if !validOperationsID(id, "vtl") || before > videoTimelineVersionLimit {
		return out, videoTimelineMissing()
	}
	limit, err = presentationPageLimit(limit)
	if err != nil {
		return out, err
	}
	err = a.videoTimelineTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		var item string
		if e := tx.QueryRow(ctx, `SELECT id FROM studio_video_timelines WHERE id=$1 AND project_id=$2 AND workspace_id=$3`, id, project, p.Workspace.ID).Scan(&item); e != nil {
			return videoTimelineReadError(e)
		}
		rows, e := tx.Query(ctx, `SELECT timeline_id,project_id,version,title,clip_count,created_at FROM studio_video_timeline_versions
		 WHERE timeline_id=$1 AND project_id=$2 AND workspace_id=$3 AND ($4::bigint=0 OR version<$4) ORDER BY version DESC LIMIT $5`, id, project, p.Workspace.ID, before, limit+1)
		if e != nil {
			return e
		}
		out.Items, e = pgx.CollectRows(rows, pgx.RowToStructByPos[VideoTimelineSummary])
		if e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			v := out.Items[limit-1].Version
			out.NextBeforeVersion = &v
		}
		return nil
	})
	return
}
