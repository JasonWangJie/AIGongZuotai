package studio

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
)

type presentationImageSource struct {
	SHA256, ContentType string
	ByteSize            int64
	ReferenceBlob       *ReferenceBlob
	AssetInfo           *AssetInfo
}
type presentationAsset struct {
	revision uint64
	source   presentationImageSource
}

func presentationSourceOK(v presentationImageSource) bool {
	if (v.ContentType != "image/png" && v.ContentType != "image/jpeg") || v.ByteSize <= 0 || v.ByteSize > presentationMaxImageBytes || !mediaContentHash.MatchString(v.SHA256) {
		return false
	}
	if b := v.ReferenceBlob; b != nil {
		if b.ID != "blob_"+b.SHA256 || b.Width < 1 || b.Height < 1 || int64(b.Width) > 16_000_000/int64(b.Height) {
			return false
		}
	}
	return true
}

// Lock source identities and moderation rows before catalog entries, matching
// delivery publication. An existing policy decision and its parent are both
// fenced. A missing initial decision cannot be inserted through its FK while
// this transaction holds the parent row. All access is scoped before locking.
func presentationAssetsSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, ids []string) (map[string]presentationAsset, error) {
	out := map[string]presentationAsset{}
	if len(ids) == 0 {
		return out, nil
	}
	sort.Strings(ids)
	const taskIDs = `SELECT task_id FROM studio_library_entries WHERE workspace_id=$1 AND project_id=$2 AND id=ANY($3::text[]) AND task_id IS NOT NULL`
	const refIDs = `SELECT reference_id FROM studio_library_entries WHERE workspace_id=$1 AND project_id=$2 AND id=ANY($3::text[]) AND reference_id IS NOT NULL`
	for _, q := range []string{
		`SELECT id FROM studio_tasks WHERE id IN(` + taskIDs + `) ORDER BY id FOR UPDATE`,
		`SELECT id FROM studio_references WHERE id IN(` + refIDs + `) ORDER BY id FOR UPDATE`,
		`SELECT task_id FROM studio_task_media_moderation WHERE task_id IN(` + taskIDs + `) ORDER BY task_id FOR SHARE`,
		`SELECT reference_id FROM studio_reference_media_moderation WHERE reference_id IN(` + refIDs + `) ORDER BY reference_id FOR SHARE`,
		`SELECT id FROM studio_library_entries WHERE workspace_id=$1 AND project_id=$2 AND id=ANY($3::text[]) ORDER BY id FOR SHARE`,
	} {
		if _, e := tx.Exec(ctx, q, scope.WorkspaceID, scope.ProjectID, ids); e != nil {
			return nil, e
		}
	}
	rows, e := tx.Query(ctx, `SELECT l.id,l.revision,l.source,l.reference_id,l.task_id,l.artifact_id,l.content_type,l.byte_size,r.blob,l.storage_info
 FROM studio_library_entries l
 LEFT JOIN studio_references r ON r.id=l.reference_id AND r.workspace_id=l.workspace_id AND r.project_id=l.project_id
 LEFT JOIN studio_reference_media_moderation rm ON rm.reference_id=r.id
 LEFT JOIN studio_tasks t ON t.id=l.task_id AND t.workspace_id=l.workspace_id AND t.project_id=l.project_id
 LEFT JOIN studio_task_media_moderation tm ON tm.task_id=t.id
 WHERE l.workspace_id=$1 AND l.project_id=$2 AND l.id=ANY($3::text[]) AND l.state='active' AND l.kind IN('image','poster') AND l.content_type IN('image/png','image/jpeg') AND l.byte_size BETWEEN 1 AND $4
 AND ((l.source='reference' AND r.id IS NOT NULL AND (rm.state IS NULL OR rm.state='ALLOWED'))
 OR (l.source='generated' AND l.storage_info IS NOT NULL AND t.document->>'status' IN('SUCCEEDED','PARTIAL') AND COALESCE(tm.state,'ALLOWED')='ALLOWED'
 AND (t.document->'moderation' IS NULL OR t.document->'moderation'='null'::jsonb OR t.document->'moderation'->>'state'='ALLOWED')
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(t.document->'artifacts')='array' THEN t.document->'artifacts' ELSE '[]'::jsonb END) a
   WHERE a->>'id'=l.artifact_id AND a->>'kind'=l.kind)))`, scope.WorkspaceID, scope.ProjectID, ids, presentationMaxImageBytes)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, contentType string
		var ref, task, artifact *string
		var revision uint64
		var size int64
		var blob, info []byte
		if e = rows.Scan(&id, &revision, &kind, &ref, &task, &artifact, &contentType, &size, &blob, &info); e != nil {
			return nil, e
		}
		v := presentationImageSource{ContentType: contentType, ByteSize: size}
		if kind == "reference" {
			var b ReferenceBlob
			if ref == nil || id != libraryID("reference", *ref, "") || json.Unmarshal(blob, &b) != nil || b.ByteSize != size || b.ContentType != contentType {
				continue
			}
			v.ReferenceBlob = &b
			v.SHA256 = b.SHA256
		} else {
			var b AssetInfo
			if task == nil || artifact == nil || id != libraryID("generated", *task, *artifact) || json.Unmarshal(info, &b) != nil || b.ID != *artifact || b.ByteSize != size || b.ContentType != contentType {
				continue
			}
			v.AssetInfo = &b
			v.SHA256 = b.SHA256
		}
		if presentationSourceOK(v) {
			out[id] = presentationAsset{revision: revision, source: v}
		}
	}
	return out, rows.Err()
}
func projectPresentationAssets(ctx context.Context, tx pgx.Tx, scope subscriptionScope, d *PresentationDocument, freeze, require bool) error {
	ids := []string{}
	for _, s := range d.Slides {
		if s.AssetID != "" {
			ids = append(ids, s.AssetID)
		}
	}
	if len(ids) > 4 {
		return failure(422, "PRESENTATION_IMAGE_LIMIT", "每份文稿最多四个图片页")
	}
	records, e := presentationAssetsSQL(ctx, tx, scope, ids)
	if e != nil {
		return e
	}
	var total int64
	for i := range d.Slides {
		s := &d.Slides[i]
		if s.AssetID == "" {
			continue
		}
		r, found := records[s.AssetID]
		if freeze {
			if !found || r.revision != s.AssetRevision {
				return presentationAssetUnavailable()
			}
			s.ContentVersion = r.source.SHA256
		} else {
			available := found && r.revision >= s.AssetRevision && r.source.SHA256 == s.ContentVersion
			s.AssetAvailable = &available
			if !available {
				if require {
					return presentationAssetUnavailable()
				}
				continue
			}
		}
		total += r.source.ByteSize
	}
	if total > presentationMaxImageTotal {
		return failure(422, "PRESENTATION_IMAGE_LIMIT", "文稿引用图片总大小超过上限")
	}
	return nil
}
func (a *App) presentationImage(p *principal, project, id string, version uint64, slideID string) (out presentationImageSource, err error) {
	if !validOperationsID(id, "pres") || version == 0 || version > presentationVersionLimit || !presentationSlideID.MatchString(slideID) {
		return out, presentationMissing()
	}
	err = a.presentationTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		var asset, sha string
		var revision uint64
		e := tx.QueryRow(ctx, `SELECT asset_id,asset_revision,content_version FROM studio_presentation_assets WHERE workspace_id=$1 AND project_id=$2 AND presentation_id=$3 AND version=$4 AND slide_id=$5`, p.Workspace.ID, project, id, version, slideID).Scan(&asset, &revision, &sha)
		if errors.Is(e, pgx.ErrNoRows) {
			return presentationMissing()
		}
		if e != nil {
			return e
		}
		records, e := presentationAssetsSQL(ctx, tx, scope, []string{asset})
		if e != nil {
			return e
		}
		row, ok := records[asset]
		if !ok || row.revision < revision || row.source.SHA256 != sha {
			return presentationAssetUnavailable()
		}
		out = row.source
		return nil
	})
	return
}
