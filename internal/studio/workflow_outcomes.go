package studio

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The parent link and the ordinary child's acceptance, reservation and
// idempotency record commit together, before either worker can dispatch.
func (g *workflowChildGuard) attach(ctx context.Context, tx pgx.Tx, child string) error {
	if g == nil {
		return nil
	}
	col := "text_run_id"
	if g.kind == "image" {
		col = "image_task_id"
	} else if g.kind == "video" {
		col = "video_task_id"
	}
	next := workflowKindState(g.kind, "ACTIVE")
	tag, err := tx.Exec(ctx, "UPDATE studio_workflow_executions SET "+col+"=$1,status=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$3 AND workspace_id=$4 AND project_id=$5 AND version=$6 AND status=$7 AND "+col+" IS NULL",
		child, next, g.id, g.scope.WorkspaceID, g.scope.ProjectID, g.version, workflowReadyStage(g.kind))
	if err == nil && tag.RowsAffected() != 1 {
		return workflowConflict()
	}
	if err != nil {
		return err
	}
	return appendWorkflowOutboxSQL(ctx, tx, g.scope, g.id, g.version+1, next)
}

// These hooks run after the child's public state has been written, but before
// committing its Outbox event. A repeated observation affects only ACTIVE.
func workflowTextOutcomeSQL(ctx context.Context, tx pgx.Tx, r Run) error {
	if !terminal(r.Status) {
		return nil
	}
	next, code := "FAILED", "WORKFLOW_TEXT_UNAVAILABLE"
	if r.Status == "COMPLETED" && runOutputReleased(r) && validatePrompt(r.Output) {
		next, code = "IMAGE_READY", ""
	} else if r.Moderation != nil && r.Moderation.State == "REVIEW" {
		next, code = "REVIEW", "WORKFLOW_TEXT_REVIEW"
	}
	var id string
	var version uint64
	err := tx.QueryRow(ctx, `UPDATE studio_workflow_executions SET status=$4,error_code=$5,version=version+1,updated_at=clock_timestamp()
	 WHERE text_run_id=$1 AND workspace_id=$2 AND project_id=$3 AND status='TEXT_ACTIVE' RETURNING id,version`, r.ID, r.WorkspaceID, r.ProjectID, next, code).Scan(&id, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return appendWorkflowOutboxSQL(ctx, tx, subscriptionScope{r.WorkspaceID, r.ProjectID}, id, version, next)
}

func workflowImageOutcomeSQL(ctx context.Context, tx pgx.Tx, t Task) error {
	if !terminal(t.Status) && t.Status != "SUBMIT_UNKNOWN" && t.Status != "RECONCILE_REQUIRED" {
		return nil
	}
	if t.Kind == "video" {
		return workflowVideoOutcomeSQL(ctx, tx, t)
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM studio_workflow_executions
	 WHERE image_task_id=$1 AND workspace_id=$2 AND project_id=$3 AND status='IMAGE_ACTIVE' FOR UPDATE`,
		t.ID, t.WorkspaceID, t.ProjectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next, code := "FAILED", "WORKFLOW_IMAGE_UNAVAILABLE"
	candidates := []WorkflowCandidate{}
	switch {
	case (t.Status == "SUCCEEDED" || t.Status == "PARTIAL") && taskArtifactsReleased(t):
		candidates, err = workflowCandidatesSQL(ctx, tx, subscriptionScope{t.WorkspaceID, t.ProjectID}, t.ID, true)
		if err != nil {
			return err
		}
		if len(candidates) > 0 {
			next, code = "WAITING_APPROVAL", ""
		}
	case t.Status == "SUBMIT_UNKNOWN" || t.Status == "RECONCILE_REQUIRED" || t.Moderation != nil && t.Moderation.State == "REVIEW":
		next, code = "REVIEW", "WORKFLOW_IMAGE_REVIEW"
	}
	var version uint64
	if err = tx.QueryRow(ctx, "UPDATE studio_workflow_executions SET status=$2,error_code=$3,candidates=$4,version=version+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING version", id, next, code, candidates).Scan(&version); err != nil {
		return err
	}
	return appendWorkflowOutboxSQL(ctx, tx, subscriptionScope{t.WorkspaceID, t.ProjectID}, id, version, next)
}

func workflowVideoOutcomeSQL(ctx context.Context, tx pgx.Tx, t Task) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM studio_workflow_executions WHERE video_task_id=$1 AND workspace_id=$2 AND project_id=$3 AND status='VIDEO_ACTIVE' FOR UPDATE`, t.ID, t.WorkspaceID, t.ProjectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next, code := "FAILED", "WORKFLOW_VIDEO_UNAVAILABLE"
	artifacts := []WorkflowCandidate{}
	if t.Status == "SUCCEEDED" && taskArtifactsReleased(t) {
		accepted, e := workflowVideoArtifactsSQL(ctx, tx, subscriptionScope{t.WorkspaceID, t.ProjectID}, t.ID, true)
		if e != nil {
			return e
		}
		if len(accepted) == 1 {
			artifacts = accepted
			next, code = "COMPLETED", ""
		}
	} else if t.Status == "SUBMIT_UNKNOWN" || t.Status == "RECONCILE_REQUIRED" || t.Moderation != nil && t.Moderation.State == "REVIEW" {
		next, code = "REVIEW", "WORKFLOW_VIDEO_REVIEW"
	}
	var version uint64
	if err = tx.QueryRow(ctx, "UPDATE studio_workflow_executions SET status=$2,error_code=$3,video_artifacts=$4,version=version+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING version", id, next, code, artifacts).Scan(&version); err != nil {
		return err
	}
	return appendWorkflowOutboxSQL(ctx, tx, subscriptionScope{t.WorkspaceID, t.ProjectID}, id, version, next)
}
