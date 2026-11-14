package studio

import (
	"context"
	"errors"
)

// Admission is private to one HTTP attempt. The first pass performs only reads;
// the second repeats idempotency and context/CAS checks before writing anything.
type runModerationAdmission struct {
	request ChatRequest
	hash    [32]byte
	checked bool
	policy  *ModerationPolicy
	err     error
}

func isModerationPreparation(err error) bool {
	var op *operationError
	return errors.As(err, &op) && op.code == "MODERATION_PREPARATION_REQUIRED"
}

func (p *runModerationAdmission) prepare(request ChatRequest) error {
	if !p.checked {
		p.request = request
		p.hash = payloadHash(request)
		return failure(409, "MODERATION_PREPARATION_REQUIRED", "审核准备中")
	}
	if p.err != nil {
		return p.err
	}
	if p.hash != payloadHash(request) {
		return failure(409, "MODERATION_CONTEXT_CHANGED", "对话上下文已有变化，请核对后重新提交")
	}
	return nil
}

func (p *runModerationAdmission) check(ctx context.Context, engine *moderationEngine) {
	p.policy, p.err = engine.CheckInput(ctx, p.request)
	p.checked = true
}

func (p *runModerationAdmission) attach(run *Run) {
	if p.policy == nil {
		return
	}
	run.ModerationPolicy = p.policy
	run.Moderation = &ModerationSummary{PolicyID: p.policy.PolicyID, PolicyVersion: p.policy.PolicyVersion, Phase: "output", State: "PENDING", Version: 1}
}

func quarantineInterruptedRun(run *Run, reason string) {
	if run.Moderation == nil {
		return
	}
	run.Output = ""
	run.Moderation = copyRunModeration(run.Moderation)
	if run.Moderation.State != "REVIEW" || run.Moderation.ReasonCode != reason {
		run.Moderation.Version++
	}
	run.Moderation.State = "REVIEW"
	run.Moderation.ReasonCode = reason
}
