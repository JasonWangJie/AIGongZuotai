package studio

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/jackc/pgx/v5"
)

func comicAssetIDs(d ComicDocument, panelID string) []string {
	var ids []string
	for _, p := range d.Panels {
		if p.AssetID != "" && (panelID == "" || panelID == p.ID) {
			ids = append(ids, p.AssetID)
		}
	}
	return ids
}

func freezeComicAssetsSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, d *ComicDocument) (map[string]presentationImageSource, error) {
	current, e := presentationAssetsSQL(ctx, tx, scope, comicAssetIDs(*d, ""))
	if e != nil {
		return nil, e
	}
	frozen := map[string]presentationImageSource{}
	var total int64
	for i := range d.Panels {
		p := &d.Panels[i]
		if p.AssetID == "" {
			continue
		}
		r, ok := current[p.AssetID]
		if !ok || r.revision != p.AssetRevision {
			return nil, comicUnavailable()
		}
		raw, e := json.Marshal(r.source)
		if e != nil || len(raw) > 32768 {
			return nil, comicUnavailable()
		}
		p.ContentVersion = r.source.SHA256
		frozen[p.ID] = r.source
		total += r.source.ByteSize
	}
	if total > presentationMaxImageTotal {
		return nil, failure(422, "COMIC_IMAGE_LIMIT", "每版引用原图最多 32 MiB")
	}
	return frozen, nil
}

type comicFrozenAsset struct {
	panelID, assetID, sha string
	revision              uint64
	source                presentationImageSource
}

// Version zero selects the committed head inside the same query. Only asset
// visibility can change after a version was saved. Original bytes and private
// storage identity are never returned as part of the public comic document.
func readComicSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, v uint64, panelID string, strict bool) (out ComicVersion, image presentationImageSource, err error) {
	err = tx.QueryRow(ctx, `SELECT v.project_id,v.version,v.created_at,v.document,EXISTS(SELECT 1 FROM studio_comic_receipts r
	 WHERE r.project_id=v.project_id AND r.version=v.version AND r.operation='confirm')
	 FROM studio_comics h JOIN studio_comic_versions v ON v.project_id=h.project_id AND v.version=CASE WHEN $3::bigint=0 THEN h.version ELSE $3 END
	 WHERE h.project_id=$1 AND h.workspace_id=$2`, scope.ProjectID, scope.WorkspaceID, v).Scan(&out.ProjectID, &out.Version, &out.CreatedAt, &out.Document, &out.Confirmed)
	if err != nil {
		return out, image, comicReadError(err)
	}
	if panelID != "" && !out.Confirmed {
		return out, image, failure(409, "COMIC_NOT_CONFIRMED", "请先由作者确认本版分镜")
	}
	ids := comicAssetIDs(out.Document, panelID)
	if len(ids) == 0 {
		if panelID != "" {
			return out, image, comicMissing()
		}
		return out, image, nil
	}
	rows, e := tx.Query(ctx, `SELECT panel_id,asset_id,asset_revision,content_version,original FROM studio_comic_assets WHERE project_id=$1 AND workspace_id=$2 AND version=$3 AND ($4::text='' OR panel_id=$4)`, scope.ProjectID, scope.WorkspaceID, out.Version, panelID)
	if e != nil {
		return out, image, e
	}
	frozen := make(map[string]comicFrozenAsset, len(ids))
	for rows.Next() {
		var f comicFrozenAsset
		if err = rows.Scan(&f.panelID, &f.assetID, &f.revision, &f.sha, &f.source); err != nil {
			rows.Close()
			return out, image, err
		}
		frozen[f.panelID] = f
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, image, err
	}
	current, e := presentationAssetsSQL(ctx, tx, scope, ids)
	if e != nil {
		return out, image, e
	}
	for i := range out.Document.Panels {
		p := &out.Document.Panels[i]
		if p.AssetID == "" || panelID != "" && p.ID != panelID {
			continue
		}
		f, bound := frozen[p.ID]
		r, visible := current[p.AssetID]
		available := bound && visible && f.assetID == p.AssetID && f.revision == p.AssetRevision && f.sha == p.ContentVersion &&
			r.revision >= p.AssetRevision && r.source.SHA256 == p.ContentVersion && reflect.DeepEqual(r.source, f.source)
		p.AssetAvailable = &available
		if strict && !available {
			return out, image, comicUnavailable()
		}
		if available && panelID != "" {
			image = f.source
		}
	}
	return out, image, nil
}

func (a *App) comicVersion(p *principal, project string, v uint64, requireAssets bool) (out ComicVersion, err error) {
	if v > comicVersionLimit {
		return out, comicMissing()
	}
	err = a.comicTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, _, e = readComicSQL(ctx, tx, subscriptionScope{p.Workspace.ID, project}, v, "", requireAssets)
		return e
	})
	if err != nil {
		return ComicVersion{}, err
	}
	return
}

func (a *App) comicPanelImage(p *principal, project string, v uint64, panelID string) (out presentationImageSource, err error) {
	if v == 0 || v > comicVersionLimit || !presentationSlideID.MatchString(panelID) {
		return out, comicMissing()
	}
	err = a.comicTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		_, out, e = readComicSQL(ctx, tx, subscriptionScope{p.Workspace.ID, project}, v, panelID, true)
		return e
	})
	if err != nil {
		return presentationImageSource{}, err
	}
	return
}

func (a *App) comicReceipt(p *principal, project, operation, key string) (out ComicVersion, err error) {
	if (operation != "save" && operation != "confirm") || !workflowKey.MatchString(key) {
		return out, comicMissing()
	}
	err = a.comicTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		v, _, e := comicReceiptSQL(ctx, tx, scope, p.User.ID, operation, key)
		if e != nil {
			return comicReadError(e)
		}
		out, _, e = readComicSQL(ctx, tx, scope, v, "", false)
		return e
	})
	if err != nil {
		return ComicVersion{}, err
	}
	return
}

func (a *App) comicHistory(p *principal, project string, before uint64, limit int) (out ComicHistoryPage, err error) {
	if before > comicVersionLimit {
		return out, comicMissing()
	}
	limit, err = presentationPageLimit(limit)
	if err != nil {
		return out, err
	}
	err = a.comicTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT v.project_id,v.version,v.document->>'title',jsonb_array_length(v.document->'panels'),
		 EXISTS(SELECT 1 FROM studio_comic_receipts r WHERE r.project_id=v.project_id AND r.version=v.version AND r.operation='confirm'),v.created_at
		 FROM studio_comic_versions v WHERE v.workspace_id=$1 AND v.project_id=$2 AND ($3::bigint=0 OR v.version<$3) ORDER BY v.version DESC LIMIT $4`, p.Workspace.ID, project, before, limit+1)
		if e != nil {
			return e
		}
		out.Items, e = pgx.CollectRows(rows, pgx.RowToStructByPos[ComicSummary])
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
