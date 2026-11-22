package studio

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func existingPanelQuoteSQL(ctx context.Context, tx pgx.Tx, row ledgerQuoteRow) (ledgerQuoteRow, bool, error) {
	q, e := scanLedgerQuote(tx.QueryRow(ctx, `SELECT id,workspace_id,project_id,resource_type,conversation_id,model_id,model_version,price_version,request_hash,price,reserve_micros,expires_at,created_at,COALESCE(reservation_id,''),COALESCE(owner_user_id,'')
	 FROM studio_ledger_quotes WHERE workspace_id=$1 AND project_id=$2 AND owner_user_id=$3 AND request_hash=$4
	 AND price_version=$5 AND expires_at>$6 AND reservation_id IS NULL ORDER BY created_at DESC,id LIMIT 1`,
		row.WorkspaceID, row.ProjectID, row.OwnerUserID, row.RequestHash[:], row.PriceVersion, row.CreatedAt))
	if errors.Is(e, pgx.ErrNoRows) {
		return ledgerQuoteRow{}, false, nil
	}
	return q, e == nil, e
}

// Read the bounded immutable document without loading image records or bytes.
// Confirmation of this precise version is retained when the head moves on.
func panelVideoDocumentSQL(ctx context.Context, tx pgx.Tx, s panelVideoSource, ws string) (ComicDocument, error) {
	var d ComicDocument
	var confirmed bool
	e := tx.QueryRow(ctx, `SELECT v.document,EXISTS(SELECT 1 FROM studio_comic_receipts r
	 WHERE r.project_id=v.project_id AND r.version=v.version AND r.operation='confirm')
	 FROM studio_comic_versions v WHERE v.project_id=$1 AND v.workspace_id=$2 AND v.version=$3`, s.ProjectID, ws, s.ComicVersion).Scan(&d, &confirmed)
	if e != nil {
		return d, comicReadError(e)
	}
	if !confirmed {
		return d, failure(409, "COMIC_NOT_CONFIRMED", "请先由作者确认本版分镜")
	}
	return d, nil
}

func (g *panelVideoGuard) nextSQL(ctx context.Context, tx pgx.Tx, lock bool) (int, error) {
	var last int
	var task string
	e := tx.QueryRow(ctx, `SELECT attempt,task_id FROM studio_comic_panel_videos WHERE project_id=$1 AND workspace_id=$2 AND comic_version=$3 AND panel_id=$4 ORDER BY attempt DESC LIMIT 1`,
		g.source.ProjectID, g.workspace, g.source.ComicVersion, g.source.PanelID).Scan(&last, &task)
	if errors.Is(e, pgx.ErrNoRows) {
		return 1, nil
	}
	if e != nil {
		return 0, e
	}
	if last >= maxPanelVideoAttempts {
		return 0, failure(409, "PANEL_VIDEO_ATTEMPT_LIMIT", "此版本的镜头已达到 32 次受理上限")
	}
	// This is the same durable media evidence used by explicit workflow redo,
	// including a queued cancellation without a provider handle.
	e = mediaTaskRedoSafeSQL(ctx, tx, subscriptionScope{WorkspaceID: g.workspace, ProjectID: g.source.ProjectID}, task, lock)
	var op *operationError
	if errors.As(e, &op) && op.code == "WORKFLOW_SOURCE_UNRESOLVED" {
		return 0, panelVideoUnresolved()
	}
	return last + 1, e
}

func (g *panelVideoGuard) checkSQL(ctx context.Context, tx pgx.Tx, p *principal, task Task) error {
	if p == nil || p.User == nil || g.actor != p.User.ID || g.workspace != p.Workspace.ID || task.ProjectID != g.source.ProjectID || task.WorkspaceID != g.workspace || task.Kind != "video" || task.Operation != "generate" {
		return comicMissing()
	}
	// Serialize the first and all later attempts on this exact immutable
	// version. Ordinary task admission has already locked the current actor.
	var present int
	if e := tx.QueryRow(ctx, `SELECT 1 FROM studio_comic_versions WHERE project_id=$1 AND workspace_id=$2 AND version=$3 FOR NO KEY UPDATE`, g.source.ProjectID, g.workspace, g.source.ComicVersion).Scan(&present); e != nil {
		return comicReadError(e)
	}
	d, e := panelVideoDocumentSQL(ctx, tx, g.source, g.workspace)
	if e != nil {
		return e
	}
	prompt, e := panelVideoPrompt(d, g.source.PanelID)
	if e != nil {
		return e
	}
	if prompt != task.Prompt || prompt != g.prompt {
		return panelVideoConflict()
	}
	next, e := g.nextSQL(ctx, tx, true)
	if e != nil {
		return e
	}
	if next != g.attempt {
		return panelVideoConflict()
	}
	return nil
}

func (g *panelVideoGuard) insertSQL(ctx context.Context, tx pgx.Tx, task Task) error {
	_, e := tx.Exec(ctx, `INSERT INTO studio_comic_panel_videos(task_id,workspace_id,project_id,comic_version,panel_id,attempt,actor_id,key_hash,request_hash,quote_id)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''))`, task.ID, g.workspace, g.source.ProjectID, g.source.ComicVersion, g.source.PanelID, g.attempt, g.actor, g.keyHash[:], g.requestHash[:], task.BillingQuoteID)
	return e
}

const panelSelect = `SELECT b.project_id,b.comic_version,b.panel_id,b.attempt,b.task_id,t.document->>'model_id',t.document->>'model_version',
 t.document->'parameters',t.document->>'status',COALESCE(b.quote_id,''),b.created_at,b.request_hash
 FROM studio_comic_panel_videos b JOIN studio_tasks t ON t.id=b.task_id`

func scanPanelVideo(r pgx.Row) (out panelVideoResult, digest []byte, e error) {
	e = r.Scan(&out.ProjectID, &out.ComicVersion, &out.PanelID, &out.Attempt, &out.TaskID, &out.ModelID, &out.ModelVersion, &out.Parameters, &out.Status, &out.QuoteID, &out.CreatedAt, &digest)
	return
}

func (a *App) panelVideoReceipt(p *principal, g *panelVideoGuard, compare bool) (out panelVideoResult, found bool, err error) {
	err = a.comicTx(p, g.source.ProjectID, false, func(ctx context.Context, tx pgx.Tx) error {
		var digest []byte
		var e error
		out, digest, e = scanPanelVideo(tx.QueryRow(ctx, panelSelect+` WHERE b.workspace_id=$1 AND b.project_id=$2 AND b.actor_id=$3 AND b.key_hash=$4`, g.workspace, g.source.ProjectID, g.actor, g.keyHash[:]))
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if out.panelVideoSource != g.source || (compare && !bytes.Equal(digest, g.requestHash[:])) {
			if compare {
				return panelVideoKeyConflict()
			}
			out = panelVideoResult{}
			return nil
		}
		found = true
		return nil
	})
	return
}

func (a *App) panelVideoList(p *principal, s panelVideoSource, before, limit int) (out panelVideoPage, err error) {
	out.Items = []panelVideoResult{}
	err = a.comicTx(p, s.ProjectID, false, func(ctx context.Context, tx pgx.Tx) error {
		d, e := panelVideoDocumentSQL(ctx, tx, s, p.Workspace.ID)
		if e != nil {
			return e
		}
		found := false
		for _, p := range d.Panels {
			if p.ID == s.PanelID {
				found = true
				break
			}
		}
		if !found {
			return comicMissing()
		}
		rows, e := tx.Query(ctx, panelSelect+` WHERE b.project_id=$1 AND b.workspace_id=$2 AND b.comic_version=$3 AND b.panel_id=$4 AND b.attempt<$5
		 ORDER BY b.attempt DESC LIMIT $6`, s.ProjectID, p.Workspace.ID, s.ComicVersion, s.PanelID, before, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, _, e := scanPanelVideo(rows)
			if e != nil {
				return e
			}
			if len(out.Items) == limit {
				last := out.Items[limit-1].Attempt
				out.Next = &last
				break
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	return
}
