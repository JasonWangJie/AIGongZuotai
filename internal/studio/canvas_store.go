package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"github.com/jackc/pgx/v5"
)

func canvasReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return canvasMissing()
	}
	return err
}

func (a *App) canvasTx(p *principal, project string, write bool, fn func(context.Context, pgx.Tx, subscriptionScope) error) error {
	if a.store == nil || a.config.AuthMode != "required" {
		return failure(501, "CANVAS_STORAGE_REQUIRED", "项目自由画布需要 PostgreSQL 与账户认证")
	}
	if p == nil || p.User == nil {
		return failure(401, "AUTH_REQUIRED", "请先登录")
	}
	if !validOperationsID(project, "prj") {
		return canvasMissing()
	}
	return a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if err := checkOrdinaryAccountSQL(ctx, tx, p); err != nil {
				return err
			}
			q := `SELECT pr.id FROM studio_projects pr JOIN studio_workspaces w ON w.id=pr.workspace_id
      WHERE pr.id=$1 AND w.id=$2 AND w.owner_id=$3`
			if write {
				q += " FOR NO KEY UPDATE OF pr"
			}
			var id string
			if err := tx.QueryRow(ctx, q, project, p.Workspace.ID, p.User.ID).Scan(&id); err != nil {
				return canvasReadError(err)
			}
			return fn(ctx, tx, subscriptionScope{p.Workspace.ID, project})
		})
	})
}

func canvasVersionSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, revision uint64) (out CanvasVersion, err error) {
	err = tx.QueryRow(ctx, `SELECT project_id,revision,created_at,document FROM studio_canvas_versions
    WHERE workspace_id=$1 AND project_id=$2 AND ($3::bigint=0 OR revision=$3) ORDER BY revision DESC LIMIT 1`, scope.WorkspaceID, scope.ProjectID, revision).Scan(&out.ProjectID, &out.Revision, &out.CreatedAt, &out.Document)
	if err != nil {
		return out, canvasReadError(err)
	}
	out.Sources, _, err = canvasCurrentSourcesSQL(ctx, tx, scope, out.Revision, "")
	return
}

func canvasKeySQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, actor, key string) (revision uint64, hash []byte, err error) {
	digest := sha256.Sum256([]byte(key))
	err = tx.QueryRow(ctx, `SELECT revision,request_hash FROM studio_canvas_versions WHERE project_id=$1 AND workspace_id=$2 AND created_by=$3 AND key_hash=$4`, scope.ProjectID, scope.WorkspaceID, actor, digest[:]).Scan(&revision, &hash)
	return
}

func (a *App) saveCanvas(p *principal, project, key string, in CanvasSaveInput) (out CanvasVersion, replay bool, err error) {
	if err = validateCanvas(in); err != nil {
		return
	}
	if !workflowKey.MatchString(key) {
		return out, false, failure(400, "INVALID_IDEMPOTENCY_KEY", "保存需要原操作键")
	}
	hash := payloadHash(in)
	err = a.canvasTx(p, project, true, func(ctx context.Context, tx pgx.Tx, scope subscriptionScope) error {
		version, old, e := canvasKeySQL(ctx, tx, scope, p.User.ID, key)
		if e == nil {
			if !bytes.Equal(old, hash[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "保存键已用于其他画布内容")
			}
			replay = true
			out, e = canvasVersionSQL(ctx, tx, scope, version)
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var current uint64
		if e = tx.QueryRow(ctx, `SELECT COALESCE(max(revision),0) FROM studio_canvas_versions WHERE project_id=$1 AND workspace_id=$2`, project, scope.WorkspaceID).Scan(&current); e != nil {
			return e
		}
		if current != *in.ExpectedRevision {
			return failure(409, "CANVAS_CONFLICT", "画布已有新的修订，请保留编辑并核对原稿")
		}
		if current >= canvasMaxRevision {
			return failure(409, "CANVAS_REVISION_LIMIT", "该项目已保存 64 个修订，历史和原保存回执仍可读取")
		}
		version = current + 1
		keyHash := sha256.Sum256([]byte(key))
		out = CanvasVersion{CanvasSources: CanvasSources{ProjectID: project, Revision: version}, Document: in.Document}
		if e = tx.QueryRow(ctx, `INSERT INTO studio_canvas_versions(project_id,workspace_id,revision,created_by,key_hash,request_hash,title,node_count,edge_count,document)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at`, project, scope.WorkspaceID, version, p.User.ID, keyHash[:], hash[:], in.Document.Title, len(in.Document.Nodes), len(in.Document.Edges), in.Document).Scan(&out.CreatedAt); e != nil {
			return e
		}
		out.Sources, e = canvasFreezeSQL(ctx, tx, scope, version, in.Document)
		if e != nil {
			return e
		}
		return insertAuditSQL(ctx, tx, newAuditEvent(scope.WorkspaceID, project, p.User.ID, "canvas.saved", "canvas", project, AuditDetails{Revision: version, NodeCount: len(in.Document.Nodes)}))
	})
	return
}

func (a *App) readCanvas(p *principal, project string, revision uint64, key string) (out CanvasVersion, err error) {
	err = a.canvasTx(p, project, false, func(ctx context.Context, tx pgx.Tx, scope subscriptionScope) error {
		var e error
		if key != "" {
			revision, _, e = canvasKeySQL(ctx, tx, scope, p.User.ID, key)
			if e != nil {
				return canvasReadError(e)
			}
		}
		out, e = canvasVersionSQL(ctx, tx, scope, revision)
		return e
	})
	return
}

func (a *App) canvasHistory(p *principal, project string, before uint64, limit int) (out CanvasHistory, err error) {
	out.Items = []CanvasSummary{}
	err = a.canvasTx(p, project, false, func(ctx context.Context, tx pgx.Tx, scope subscriptionScope) error {
		rows, e := tx.Query(ctx, `SELECT revision,title,node_count,edge_count,created_at FROM studio_canvas_versions WHERE project_id=$1 AND workspace_id=$2 AND ($3::bigint=0 OR revision<$3) ORDER BY revision DESC LIMIT $4`, scope.ProjectID, scope.WorkspaceID, before, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v CanvasSummary
			if e = rows.Scan(&v.Revision, &v.Title, &v.NodeCount, &v.EdgeCount, &v.CreatedAt); e != nil {
				return e
			}
			if len(out.Items) == limit {
				out.NextBeforeRevision = out.Items[len(out.Items)-1].Revision
				break
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	return
}
