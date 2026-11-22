package studio

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type timelineVideoAsset struct {
	revision uint64
	info     AssetInfo
}

// Match delivery publication's lock order. Locking the parent also excludes
// insertion of a first moderation row between the decision check and commit.
func videoTimelineAssetsSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, clips []VideoTimelineClip) (map[string]timelineVideoAsset, error) {
	result := make(map[string]timelineVideoAsset)
	ids := make([]string, 0, len(clips))
	for _, c := range clips {
		ids = append(ids, c.AssetID)
	}
	const taskIDs = `SELECT task_id FROM studio_library_entries WHERE workspace_id=$1 AND project_id=$2 AND id=ANY($3::text[]) AND task_id IS NOT NULL`
	for _, q := range []string{
		`SELECT id FROM studio_tasks WHERE id IN(` + taskIDs + `) ORDER BY id FOR UPDATE`,
		`SELECT task_id FROM studio_task_media_moderation WHERE task_id IN(` + taskIDs + `) ORDER BY task_id FOR SHARE`,
		`SELECT id FROM studio_library_entries WHERE workspace_id=$1 AND project_id=$2 AND id=ANY($3::text[]) ORDER BY id FOR SHARE`,
	} {
		if _, e := tx.Exec(ctx, q, scope.WorkspaceID, scope.ProjectID, ids); e != nil {
			return nil, e
		}
	}
	rows, e := tx.Query(ctx, `SELECT l.id,l.revision,l.task_id,l.artifact_id,l.byte_size,l.storage_info
	 FROM studio_library_entries l JOIN studio_tasks t ON (t.id,t.project_id,t.workspace_id)=(l.task_id,l.project_id,l.workspace_id)
	 LEFT JOIN studio_task_media_moderation m ON m.task_id=t.id
	 WHERE l.workspace_id=$1 AND l.project_id=$2 AND l.id=ANY($3::text[]) AND l.state='active' AND l.source='generated'
	 AND l.kind='video' AND l.content_type='video/mp4' AND l.byte_size BETWEEN 1 AND $4 AND jsonb_typeof(l.storage_info)='object'
	 AND octet_length(l.storage_info::text)<=32768 AND t.document->>'kind'='video' AND t.document->>'status' IN ('SUCCEEDED','PARTIAL')
	 AND COALESCE(m.state,'ALLOWED')='ALLOWED' AND (t.document->'moderation' IS NULL OR t.document->'moderation'='null'::jsonb OR t.document->'moderation'->>'state'='ALLOWED')
	 AND EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(t.document->'artifacts')='array' THEN t.document->'artifacts' ELSE '[]'::jsonb END) a
	  WHERE a->>'id'=l.artifact_id AND a->>'kind'='video')`, scope.WorkspaceID, scope.ProjectID, ids, videoTimelineSourceLimit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, task, artifact string
		var size int64
		var r timelineVideoAsset
		var raw []byte
		if e = rows.Scan(&id, &r.revision, &task, &artifact, &size, &raw); e != nil {
			return nil, e
		}
		if id != libraryID("generated", task, artifact) || json.Unmarshal(raw, &r.info) != nil || r.info.ID != artifact || !mediaContentHash.MatchString(r.info.SHA256) || r.info.ByteSize != size || r.info.ContentType != "video/mp4" {
			continue
		}
		result[id] = r
	}
	return result, rows.Err()
}

// The single-clip branch intentionally reads only that clip. A revoked sibling
// blocks whole-timeline export but does not grant or revoke an unrelated clip.
func frozenVideoTimelineSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, version uint64, clipID string) (out VideoTimelineVersion, sources []AssetInfo, err error) {
	var count int
	err = tx.QueryRow(ctx, `SELECT timeline_id,project_id,version,title,clip_count,created_at FROM studio_video_timeline_versions
	  WHERE timeline_id=$1 AND version=$2 AND project_id=$3 AND workspace_id=$4`, id, version, scope.ProjectID, scope.WorkspaceID).Scan(&out.ID, &out.ProjectID, &out.Version, &out.Document.Title, &count, &out.CreatedAt)
	if err != nil {
		return out, nil, videoTimelineReadError(err)
	}
	rows, err := tx.Query(ctx, `SELECT clip_id,title,asset_id,asset_revision,in_ms,out_ms,content_version,asset_info
	 FROM studio_video_timeline_clips WHERE timeline_id=$1 AND version=$2 AND project_id=$3 AND workspace_id=$4 AND ($5::text='' OR clip_id=$5) ORDER BY position`, id, version, scope.ProjectID, scope.WorkspaceID, clipID)
	if err != nil {
		return out, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c VideoTimelineClip
		var info AssetInfo
		if err = rows.Scan(&c.ID, &c.Title, &c.AssetID, &c.AssetRevision, &c.InMS, &c.OutMS, &c.ContentVersion, &info); err != nil {
			return out, nil, err
		}
		out.Document.Clips = append(out.Document.Clips, c)
		sources = append(sources, info)
	}
	if err = rows.Err(); err != nil {
		return out, nil, err
	}
	if clipID != "" && len(sources) == 0 {
		return out, nil, videoTimelineMissing()
	}
	if clipID == "" && len(sources) != count {
		return out, nil, failure(409, "VIDEO_TIMELINE_INCOMPLETE", "此修订的镜头记录不完整")
	}
	return out, sources, nil
}

func readVideoTimelineSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, version uint64, clipID string, strict bool) (out VideoTimelineVersion, sources []AssetInfo, err error) {
	out, sources, err = frozenVideoTimelineSQL(ctx, tx, scope, id, version, clipID)
	if err != nil {
		return out, nil, err
	}
	now, err := videoTimelineAssetsSQL(ctx, tx, scope, out.Document.Clips)
	if err != nil {
		return out, nil, err
	}
	for i := range out.Document.Clips {
		c := &out.Document.Clips[i]
		record, found := now[c.AssetID]
		available := found && record.revision >= c.AssetRevision && record.info.SHA256 == c.ContentVersion && sameMediaDeliveryReceipt(&record.info, &sources[i])
		c.AssetAvailable = &available
		if !available && strict {
			return out, nil, videoTimelineAssetUnavailable()
		}
	}
	return out, sources, nil
}

func (a *App) videoTimelineRead(p *principal, project, id string, version uint64, clipID string, strict bool) (out VideoTimelineVersion, sources []AssetInfo, err error) {
	if !validVideoTimelineVersion(id, version) {
		return out, nil, videoTimelineMissing()
	}
	err = a.videoTimelineTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, sources, e = readVideoTimelineSQL(ctx, tx, subscriptionScope{p.Workspace.ID, project}, id, version, clipID, strict)
		return e
	})
	if err != nil {
		return VideoTimelineVersion{}, nil, err
	}
	return
}
func (a *App) videoTimelineVersion(p *principal, project, id string, version uint64, requireAssets bool) (VideoTimelineVersion, error) {
	v, _, err := a.videoTimelineRead(p, project, id, version, "", requireAssets)
	return v, err
}
func (a *App) videoTimelineSources(p *principal, project, id string, version uint64) (VideoTimelineVersion, []AssetInfo, error) {
	return a.videoTimelineRead(p, project, id, version, "", true)
}
func (a *App) videoTimelineSource(p *principal, project, id string, version uint64, clipID string) (AssetInfo, error) {
	if !presentationSlideID.MatchString(clipID) {
		return AssetInfo{}, videoTimelineMissing()
	}
	_, sources, err := a.videoTimelineRead(p, project, id, version, clipID, true)
	if err != nil {
		return AssetInfo{}, err
	}
	return sources[0], nil
}
func (a *App) videoTimelineReceipt(p *principal, project, key string) (out VideoTimelineVersion, err error) {
	if !workflowKey.MatchString(key) {
		return out, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效的保存原键")
	}
	err = a.videoTimelineTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		id, v, _, e := videoTimelineKeySQL(ctx, tx, scope, p.User.ID, key)
		if e != nil {
			return videoTimelineReadError(e)
		}
		out, _, e = readVideoTimelineSQL(ctx, tx, scope, id, v, "", false)
		return e
	})
	if err != nil {
		return VideoTimelineVersion{}, err
	}
	return
}
