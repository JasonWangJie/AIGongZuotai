package studio

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type panelClipReplacement struct {
	ComicVersion  uint64 `json:"comic_version"`
	PanelID       string `json:"panel_id"`
	TaskID        string `json:"task_id"`
	AssetID       string `json:"asset_id"`
	AssetRevision uint64 `json:"asset_revision"`
	InMS          *int64 `json:"in_ms"`
	OutMS         *int64 `json:"out_ms"`
}

func (a *App) replacePanelVideoHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	version, err := timelineVersionNumber(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	var input panelClipReplacement
	if err = decodeGenerationJSON(w, r, &input, "comic_version", "panel_id", "task_id", "asset_id", "asset_revision", "in_ms", "out_ms"); err != nil {
		writeError(w, err)
		return
	}
	result, replayed, err := a.replacePanelClip(principalFrom(r), r.PathValue("project"), r.PathValue("timeline"), version, r.PathValue("clip"), input, key)
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *App) replacePanelClip(p *principal, project, timeline string, version uint64, clipID string, in panelClipReplacement, key string) (out VideoTimelineVersion, replayed bool, err error) {
	if !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效的保存原键")
	}
	err = a.videoTimelineTx(p, project, true, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		hash := payloadHash(struct {
			Operation, TimelineID, ClipID string
			Version                       uint64
			Input                         panelClipReplacement
		}{"replace_from_panel", timeline, clipID, version, in})
		var e error
		out, replayed, e = originalVideoTimelineSaveSQL(ctx, tx, scope, p.User.ID, key, hash)
		if e != nil || replayed {
			return e
		}
		if !validVideoTimelineVersion(timeline, version) || !presentationSlideID.MatchString(clipID) {
			return videoTimelineMissing()
		}
		if in.ComicVersion < 1 || in.ComicVersion > comicVersionLimit || !presentationSlideID.MatchString(in.PanelID) ||
			!validOperationsID(in.TaskID, "task") || in.InMS == nil || in.OutMS == nil {
			return failure(422, "INVALID_PANEL_REPLACEMENT", "请核对分镜版本、生成任务和镜头入出点")
		}
		if e = checkVideoTimelineEditSQL(ctx, tx, scope, timeline, version); e != nil {
			return e
		}
		prior, sources, e := frozenVideoTimelineSQL(ctx, tx, scope, timeline, version, "")
		if e != nil {
			return e
		}
		document, index, e := panelReplacementDocument(prior, clipID, in)
		if e != nil {
			return e
		}
		// Lock the complete final set once in the same task, decision and asset
		// order as ordinary 030 saves and delivery publication.
		current, e := videoTimelineAssetsSQL(ctx, tx, scope, document.Clips)
		if e != nil {
			return e
		}
		var total int64
		for i, clip := range document.Clips {
			actual, found := current[clip.AssetID]
			if !found {
				return videoTimelineAssetUnavailable()
			}
			if i == index {
				if actual.revision != in.AssetRevision || libraryID("generated", in.TaskID, actual.info.ID) != in.AssetID {
					return videoTimelineAssetUnavailable()
				}
				sources[i] = actual.info
			} else if actual.revision < clip.AssetRevision || prior.Document.Clips[i].ContentVersion != actual.info.SHA256 ||
				!sameMediaDeliveryReceipt(&actual.info, &sources[i]) {
				return videoTimelineAssetUnavailable()
			}
			total += sources[i].ByteSize
		}
		if total > videoTimelineTotalLimit {
			return failure(422, "VIDEO_TIMELINE_SOURCE_LIMIT", "镜头原件累计大小超过128 MiB")
		}
		var bound bool
		selected := sources[index]
		content := map[string]any{"id": selected.ID, "content_type": selected.ContentType, "byte_size": selected.ByteSize, "sha256": selected.SHA256}
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_comic_panel_videos b
			JOIN studio_tasks t ON (t.id,t.project_id,t.workspace_id)=(b.task_id,b.project_id,b.workspace_id)
			LEFT JOIN studio_task_media_moderation m ON m.task_id=t.id
			LEFT JOIN studio_media_deliveries d ON d.task_id=t.id
			WHERE b.workspace_id=$1 AND b.project_id=$2 AND b.comic_version=$3 AND b.panel_id=$4 AND b.task_id=$5
			AND (d.task_id IS NULL OR (d.state='COMPLETE' AND EXISTS(
				SELECT 1 FROM studio_media_delivery_items i WHERE i.task_id=t.id AND i.receipt @> $6::jsonb)))
			AND ((m.task_id IS NULL AND (t.document->'moderation' IS NULL OR t.document->'moderation'='null'::jsonb))
				OR (m.state='ALLOWED' AND t.document->'moderation'->>'state'='ALLOWED' AND EXISTS(
					SELECT 1 FROM studio_task_media_candidates c, jsonb_array_elements(c.candidates) AS candidate
					WHERE c.task_id=t.id AND candidate->'storage_info' @> $6::jsonb))))`,
			scope.WorkspaceID, project, in.ComicVersion, in.PanelID, in.TaskID, content).Scan(&bound)
		if e != nil {
			return e
		}
		if !bound {
			return videoTimelineAssetUnavailable()
		}
		out, e = persistVideoTimelineVersionSQL(ctx, tx, scope, p.User.ID, timeline, version+1, document, sources, key, hash)
		return e
	})
	if err != nil {
		return VideoTimelineVersion{}, false, err
	}
	return
}

func panelReplacementDocument(prior VideoTimelineVersion, clipID string, in panelClipReplacement) (VideoTimelineDocument, int, error) {
	doc := VideoTimelineDocument{Title: prior.Document.Title, Clips: append([]VideoTimelineClip(nil), prior.Document.Clips...)}
	index := -1
	for i := range doc.Clips {
		c := &doc.Clips[i]
		c.ContentVersion, c.AssetAvailable = "", nil // reattached from the frozen original at commit
		if c.ID == clipID {
			index = i
			c.AssetID, c.AssetRevision = in.AssetID, in.AssetRevision
			c.InMS, c.OutMS = *in.InMS, *in.OutMS
		}
	}
	if index < 0 {
		return doc, index, videoTimelineMissing()
	}
	err := validateVideoTimeline(VideoTimelineSaveInput{TimelineID: prior.ID, ExpectedVersion: &prior.Version, Document: doc})
	return doc, index, err
}
