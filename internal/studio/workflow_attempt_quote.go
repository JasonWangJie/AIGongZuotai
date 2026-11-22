package studio

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// An omitted marker preserves the original payload hashes of ordinary and
// 023/024 requests. A new fork always binds its own id to its quote digest.
func (x WorkflowExecution) attemptInputID() string {
	if x.SourceWorkflowID == "" {
		return ""
	}
	return x.ID
}

func authorizeWorkflowAttempt(ctx context.Context, id, kind string) error {
	if id == "" {
		return nil
	}
	g := workflowChildFrom(ctx)
	if g == nil || g.id != id || g.kind != kind {
		return failure(409, "WORKFLOW_STEP_REQUIRED", "请从对应工作流尝试执行该节点")
	}
	return nil
}

func verifyWorkflowAttemptQuoteSQL(ctx context.Context, tx pgx.Tx, p *principal, scope subscriptionScope, id, kind string, in workflowChildRequest, mode string) error {
	if err := workflowAuthorizeSQL(ctx, tx, scope, p); err != nil {
		return err
	}
	x, err := workflowExecutionSQL(ctx, tx, scope, id)
	if err != nil {
		return err
	}
	if x.attemptInputID() != id || x.Status != workflowReadyStage(kind) || x.ProviderMode != mode {
		return workflowStageConflict()
	}
	return verifyWorkflowIntent(ctx, tx, scope, x, kind, in)
}
