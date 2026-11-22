package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	videoTimelineVersionLimit = 128
	videoTimelineSourceLimit  = 32 << 20
	videoTimelineTotalLimit   = 128 << 20
)

type VideoTimelineClip struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	AssetID        string `json:"asset_id"`
	AssetRevision  uint64 `json:"asset_revision"`
	InMS           int64  `json:"in_ms"`
	OutMS          int64  `json:"out_ms"`
	ContentVersion string `json:"content_version,omitempty"`
	AssetAvailable *bool  `json:"asset_available,omitempty"`
}
type VideoTimelineDocument struct {
	Title string              `json:"title"`
	Clips []VideoTimelineClip `json:"clips"`
}
type VideoTimelineSaveInput struct {
	TimelineID      string                `json:"timeline_id,omitempty"`
	ExpectedVersion *uint64               `json:"expected_version"`
	Document        VideoTimelineDocument `json:"document"`
}
type VideoTimelineVersion struct {
	ID        string                `json:"id"`
	ProjectID string                `json:"project_id"`
	Version   uint64                `json:"version"`
	CreatedAt time.Time             `json:"created_at"`
	Document  VideoTimelineDocument `json:"document"`
}
type VideoTimelineSummary struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Version   uint64    `json:"version"`
	Title     string    `json:"title"`
	ClipCount int       `json:"clip_count"`
	CreatedAt time.Time `json:"created_at"`
}
type VideoTimelinePage struct {
	Items      []VideoTimelineSummary `json:"items"`
	NextCursor *string                `json:"next_cursor"`
}
type VideoTimelineHistoryPage struct {
	Items             []VideoTimelineSummary `json:"items"`
	NextBeforeVersion *uint64                `json:"next_before_version"`
}

func videoTimelineMissing() error {
	return failure(404, "VIDEO_TIMELINE_NOT_FOUND", "本项目中没有此剪辑稿、版本、镜头或收据")
}
func videoTimelineAssetUnavailable() error {
	return failure(409, "VIDEO_TIMELINE_ASSET_UNAVAILABLE", "视频不可见、原件已变动或修订已过期")
}
func videoTimelineReadError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return videoTimelineMissing()
	}
	return e
}
func validVideoTimelineVersion(id string, v uint64) bool {
	return validOperationsID(id, "vtl") && v >= 1 && v <= videoTimelineVersionLimit
}
func validateVideoTimeline(in VideoTimelineSaveInput) error {
	invalid := func() error {
		return failure(422, "INVALID_VIDEO_TIMELINE", "请核对剪辑稿版本、镜头、视频修订与剪辑范围")
	}
	if in.ExpectedVersion == nil || (in.TimelineID == "" && *in.ExpectedVersion != 0) || (in.TimelineID != "" && !validVideoTimelineVersion(in.TimelineID, *in.ExpectedVersion)) {
		return invalid()
	}
	d := in.Document
	if !validPresentationText(d.Title, 120, true) || len(d.Clips) < 1 || len(d.Clips) > 8 {
		return invalid()
	}
	seen := map[string]bool{}
	var duration int64
	for _, c := range d.Clips {
		if !presentationSlideID.MatchString(c.ID) || seen[c.ID] || !validPresentationText(c.Title, 120, true) || !libraryIDPattern.MatchString(c.AssetID) || c.AssetRevision < 1 || c.AssetRevision > libraryRevisionLimit || c.AssetAvailable != nil || c.ContentVersion != "" {
			return invalid()
		}
		if c.InMS < 0 || c.OutMS <= c.InMS || c.OutMS > 300000 {
			return invalid()
		}
		seen[c.ID] = true
		previousFrames := (duration*24 + 500) / 1000
		duration += c.OutMS - c.InMS
		if (duration*24+500)/1000 <= previousFrames {
			return invalid()
		}
	}
	if duration > 30000 {
		return failure(422, "VIDEO_TIMELINE_DURATION_LIMIT", "剪辑后总时长不能超过30秒")
	}
	return nil
}

// Both creative editors share the current-session check and the stable
// project-row lock. Translate only missing project/storage errors to this API.
func (a *App) videoTimelineTx(p *principal, project string, save bool, fn func(context.Context, pgx.Tx) error) error {
	e := a.presentationTx(p, project, save, fn)
	var op *operationError
	if errors.As(e, &op) {
		switch op.code {
		case "PRESENTATION_NOT_FOUND":
			return videoTimelineMissing()
		case "PRESENTATION_UNAVAILABLE":
			return failure(503, "VIDEO_TIMELINE_UNAVAILABLE", "剪辑稿需要持久账户模式")
		}
	}
	return e
}

func videoTimelineKeySQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, actor, key string) (id string, version uint64, hash []byte, err error) {
	digest := sha256.Sum256([]byte(key))
	err = tx.QueryRow(ctx, `SELECT timeline_id,version,request_hash FROM studio_video_timeline_keys
	 WHERE workspace_id=$1 AND project_id=$2 AND actor_id=$3 AND key_hash=$4`, scope.WorkspaceID, scope.ProjectID, actor, digest[:]).Scan(&id, &version, &hash)
	return
}

func (a *App) saveVideoTimeline(p *principal, project string, in VideoTimelineSaveInput, key string) (out VideoTimelineVersion, replayed bool, err error) {
	if !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效的保存原键")
	}
	err = a.videoTimelineTx(p, project, true, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		requestHash := payloadHash(in)
		var e error
		out, replayed, e = originalVideoTimelineSaveSQL(ctx, tx, scope, p.User.ID, key, requestHash)
		if e != nil || replayed {
			return e
		}
		if e = validateVideoTimeline(in); e != nil {
			return e
		}
		id := in.TimelineID
		if id == "" {
			if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x56544c4e4557)); e != nil {
				return e
			}
			var own, total int
			e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE project_id=$1),count(*) FROM (SELECT project_id FROM studio_video_timelines LIMIT 4097)s`, project).Scan(&own, &total)
			if e != nil {
				return e
			}
			if own >= 64 || total >= 4096 {
				return failure(429, "VIDEO_TIMELINE_LIMIT", "剪辑稿数量已达上限")
			}
			id = newID("vtl")
		} else {
			if e = checkVideoTimelineEditSQL(ctx, tx, scope, id, *in.ExpectedVersion); e != nil {
				return e
			}
		}
		recs, e := videoTimelineAssetsSQL(ctx, tx, scope, in.Document.Clips)
		if e != nil {
			return e
		}
		var total int64
		sources := make([]AssetInfo, 0, len(in.Document.Clips))
		for _, clip := range in.Document.Clips {
			r, ok := recs[clip.AssetID]
			if !ok || r.revision != clip.AssetRevision {
				return videoTimelineAssetUnavailable()
			}
			sources = append(sources, r.info)
			total += r.info.ByteSize
		}
		if total > videoTimelineTotalLimit {
			return failure(422, "VIDEO_TIMELINE_SOURCE_LIMIT", "镜头原件累计大小超过128 MiB")
		}
		out, e = persistVideoTimelineVersionSQL(ctx, tx, scope, p.User.ID, id, *in.ExpectedVersion+1, in.Document, sources, key, requestHash)
		return e
	})
	if err != nil {
		return VideoTimelineVersion{}, false, err
	}
	return
}

func originalVideoTimelineSaveSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, actor, key string, hash [32]byte) (VideoTimelineVersion, bool, error) {
	id, version, original, err := videoTimelineKeySQL(ctx, tx, scope, actor, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return VideoTimelineVersion{}, false, nil
	}
	if err != nil {
		return VideoTimelineVersion{}, false, err
	}
	if !bytes.Equal(hash[:], original) {
		return VideoTimelineVersion{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "原键已用于不同剪辑稿输入")
	}
	out, _, err := readVideoTimelineSQL(ctx, tx, scope, id, version, "", false)
	return out, true, err
}

func checkVideoTimelineEditSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, expected uint64) error {
	var version uint64
	e := tx.QueryRow(ctx, `SELECT version FROM studio_video_timelines WHERE id=$1 AND workspace_id=$2 AND project_id=$3 FOR UPDATE`, id, scope.WorkspaceID, scope.ProjectID).Scan(&version)
	if e != nil {
		return videoTimelineReadError(e)
	}
	if version != expected {
		return failure(409, "VIDEO_TIMELINE_VERSION_CONFLICT", "剪辑稿已更新，请读取最新版本")
	}
	if version >= videoTimelineVersionLimit {
		return failure(429, "VIDEO_TIMELINE_VERSION_LIMIT", "剪辑稿版本数已达上限")
	}
	return nil
}

func persistVideoTimelineVersionSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, actor, id string, version uint64, document VideoTimelineDocument, sources []AssetInfo, key string, requestHash [32]byte) (out VideoTimelineVersion, e error) {
	if version == 1 {
		_, e = tx.Exec(ctx, `INSERT INTO studio_video_timelines(id,workspace_id,project_id,version) VALUES($1,$2,$3,1)`, id, scope.WorkspaceID, scope.ProjectID)
	} else {
		_, e = tx.Exec(ctx, `UPDATE studio_video_timelines SET version=$2 WHERE id=$1`, id, version)
	}
	if e != nil {
		return out, e
	}
	out = VideoTimelineVersion{ID: id, ProjectID: scope.ProjectID, Version: version, Document: VideoTimelineDocument{Title: document.Title, Clips: append([]VideoTimelineClip(nil), document.Clips...)}}
	e = tx.QueryRow(ctx, `INSERT INTO studio_video_timeline_versions(timeline_id,version,workspace_id,project_id,created_by,title,clip_count)
		 VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`, id, version, scope.WorkspaceID, scope.ProjectID, actor, out.Document.Title, len(out.Document.Clips)).Scan(&out.CreatedAt)
	if e != nil {
		return out, e
	}
	for i := range out.Document.Clips {
		clip := &out.Document.Clips[i]
		info := sources[i]
		available := true
		clip.ContentVersion, clip.AssetAvailable = info.SHA256, &available
		_, e = tx.Exec(ctx, `INSERT INTO studio_video_timeline_clips(timeline_id,version,project_id,workspace_id,position,clip_id,title,asset_id,asset_revision,in_ms,out_ms,content_version,asset_info)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, version, scope.ProjectID, scope.WorkspaceID, i, clip.ID, clip.Title, clip.AssetID, clip.AssetRevision, clip.InMS, clip.OutMS, clip.ContentVersion, info)
		if e != nil {
			return out, e
		}
	}
	keyHash := sha256.Sum256([]byte(key))
	_, e = tx.Exec(ctx, `INSERT INTO studio_video_timeline_keys(workspace_id,project_id,actor_id,key_hash,request_hash,timeline_id,version) VALUES($1,$2,$3,$4,$5,$6,$7)`, scope.WorkspaceID, scope.ProjectID, actor, keyHash[:], requestHash[:], id, version)
	return out, e
}
