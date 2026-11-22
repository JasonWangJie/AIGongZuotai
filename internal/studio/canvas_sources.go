package studio

import (
	"context"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
)

type canvasBinding struct {
	Node          string                   `json:"node_id"`
	Asset         string                   `json:"asset_id,omitempty"`
	AssetRevision uint64                   `json:"asset_revision,omitempty"`
	Original      *presentationImageSource `json:"original,omitempty"`
	Run           string                   `json:"run_id,omitempty"`
}

func canvasThumbInfo(s presentationImageSource) (size int64, digest string) {
	if s.ReferenceBlob != nil && s.ReferenceBlob.Thumbnail != nil {
		t := s.ReferenceBlob.Thumbnail
		if t.ContentType == "image/png" {
			size, digest = t.ByteSize, t.SHA256
		}
	}
	if s.AssetInfo != nil && s.AssetInfo.Thumbnail != nil {
		t := s.AssetInfo.Thumbnail
		if t.ContentType == "image/png" {
			size, digest = t.ByteSize, t.SHA256
		}
	}
	if size < 1 || size > 2<<20 || !mediaContentHash.MatchString(digest) {
		return 0, ""
	}
	return
}

// presentationAssetsSQL checks scope, private original identity, registered
// published artifacts and current visibility. Canvas additionally requires a
// recorded ALLOWED decision, including when reading historical versions.
func canvasAssetsSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, ids []string) (map[string]presentationAsset, error) {
	assets, e := presentationAssetsSQL(ctx, tx, scope, ids)
	if e != nil || len(assets) == 0 {
		return assets, e
	}
	rows, e := tx.Query(ctx, `SELECT l.id FROM studio_library_entries l
  LEFT JOIN studio_reference_media_moderation rm ON rm.reference_id=l.reference_id
  LEFT JOIN studio_task_media_moderation tm ON tm.task_id=l.task_id
  WHERE l.workspace_id=$1 AND l.project_id=$2 AND l.id=ANY($3::text[]) AND
  ((l.source='reference' AND rm.state='ALLOWED') OR (l.source='generated' AND tm.state='ALLOWED'))`, scope.WorkspaceID, scope.ProjectID, ids)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	allowed := map[string]presentationAsset{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		if r, ok := assets[id]; ok {
			if size, _ := canvasThumbInfo(r.source); size > 0 {
				allowed[id] = r
			}
		}
	}
	return allowed, rows.Err()
}

func canvasRunsSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, ids []string) (map[string]CanvasSource, error) {
	out := map[string]CanvasSource{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.document->>'status',COALESCE(h.head_seq,(r.document->>'seq')::bigint,0)
  FROM studio_runs r LEFT JOIN studio_run_streams h ON h.run_id=r.id WHERE r.workspace_id=$1 AND r.project_id=$2 AND r.id=ANY($3::text[])`, scope.WorkspaceID, scope.ProjectID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		var seq uint64
		if err = rows.Scan(&id, &status, &seq); err != nil {
			return nil, err
		}
		switch status {
		case "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELLED", "INTERRUPTED":
			out[id] = CanvasSource{Available: true, Status: status, Seq: &seq}
		}
	}
	return out, rows.Err()
}

func canvasProjectSources(ctx context.Context, tx pgx.Tx, scope subscriptionScope, revision uint64, links []canvasBinding, freeze bool) (map[string]CanvasSource, error) {
	var assetIDs, runIDs []string
	for _, b := range links {
		if b.Asset != "" {
			assetIDs = append(assetIDs, b.Asset)
		} else {
			runIDs = append(runIDs, b.Run)
		}
	}
	assets, err := canvasAssetsSQL(ctx, tx, scope, assetIDs)
	if err != nil {
		return nil, err
	}
	runs, err := canvasRunsSQL(ctx, tx, scope, runIDs)
	if err != nil {
		return nil, err
	}
	projections := make(map[string]CanvasSource, len(links))
	for i := range links {
		b := &links[i]
		source := runs[b.Run]
		if b.Asset != "" {
			current, found := assets[b.Asset]
			if freeze && found && current.revision == b.AssetRevision {
				original := current.source
				b.Original = &original
			}
			if found && b.Original != nil && current.revision >= b.AssetRevision && reflect.DeepEqual(*b.Original, current.source) {
				source = CanvasSource{Available: true, ContentVersion: current.source.SHA256, ThumbnailURL: fmt.Sprintf("/api/v1/projects/%s/canvas/revisions/%d/nodes/%s/thumbnail?project_id=%s", scope.ProjectID, revision, b.Node, scope.ProjectID)}
			}
		}
		if freeze && !source.Available {
			return nil, canvasUnavailable()
		}
		projections[b.Node] = source
	}
	return projections, nil
}

func canvasFreezeSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, revision uint64, doc CanvasDocument) (map[string]CanvasSource, error) {
	var links []canvasBinding
	for _, node := range doc.Nodes {
		if node.AssetID != "" || node.RunID != "" {
			links = append(links, canvasBinding{Node: node.ID, Asset: node.AssetID, AssetRevision: node.AssetRevision, Run: node.RunID})
		}
	}
	out, err := canvasProjectSources(ctx, tx, scope, revision, links, true)
	if err != nil || len(links) == 0 {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_canvas_sources(project_id,workspace_id,revision,node_id,asset_id,asset_revision,original,run_id)
  SELECT $1,$2,$3,node_id,asset_id,asset_revision,original,run_id FROM jsonb_to_recordset($4::jsonb)
  AS source(node_id text,asset_id text,asset_revision bigint,original jsonb,run_id text)`, scope.ProjectID, scope.WorkspaceID, revision, links)
	return out, err
}

func canvasCurrentSourcesSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, revision uint64, node string) (map[string]CanvasSource, *presentationImageSource, error) {
	rows, err := tx.Query(ctx, `SELECT node_id,COALESCE(asset_id,''),COALESCE(asset_revision,0),original,COALESCE(run_id,'')
  FROM studio_canvas_sources WHERE project_id=$1 AND workspace_id=$2 AND revision=$3 AND ($4::text='' OR node_id=$4)`, scope.ProjectID, scope.WorkspaceID, revision, node)
	if err != nil {
		return nil, nil, err
	}
	var links []canvasBinding
	for rows.Next() {
		var b canvasBinding
		if err = rows.Scan(&b.Node, &b.Asset, &b.AssetRevision, &b.Original, &b.Run); err != nil {
			rows.Close()
			return nil, nil, err
		}
		links = append(links, b)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	out, err := canvasProjectSources(ctx, tx, scope, revision, links, false)
	if err != nil {
		return nil, nil, err
	}
	if node != "" {
		if len(links) != 1 || links[0].Original == nil || !out[node].Available {
			return nil, nil, canvasMissing()
		}
		return out, links[0].Original, nil
	}
	return out, nil, nil
}

func (a *App) canvasSources(p *principal, project string, v uint64, node string) (out CanvasSources, original *presentationImageSource, err error) {
	out.ProjectID = project
	out.Revision = v
	err = a.canvasTx(p, project, false, func(ctx context.Context, tx pgx.Tx, scope subscriptionScope) error {
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_canvas_versions WHERE project_id=$1 AND workspace_id=$2 AND revision=$3)`, project, scope.WorkspaceID, v).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return canvasMissing()
		}
		var e error
		out.Sources, original, e = canvasCurrentSourcesSQL(ctx, tx, scope, v, node)
		return e
	})
	return
}
