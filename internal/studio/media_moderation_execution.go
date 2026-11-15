package studio

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type taskMediaModerationAdmission struct {
	workspaceID string
	input       TaskInput
	refs        []referenceRecord
	hash        [32]byte
	checked     bool
	policy      *MediaModerationPolicy
	textPolicy  *ModerationPolicy
	err         error
}

func isMediaModerationPreparation(err error) bool {
	var op *operationError
	return errors.As(err, &op) && op.code == "MEDIA_MODERATION_PREPARATION_REQUIRED"
}

func (p *taskMediaModerationAdmission) prepare(input TaskInput, parameters TaskParameters, model catalogEntry, refs []referenceRecord) error {
	resolved := input
	resolved.Parameters = parameters
	resolved.ModelVersion = model.Model.Version
	// The original HTTP hash is unchanged. This additional digest seals the
	// normalized execution and the ordered, private immutable reference bytes.
	identities := make([]MediaModerationContent, 0, len(refs))
	for _, ref := range refs {
		identities = append(identities, MediaModerationContent{Kind: "image", ContentType: ref.Blob.ContentType, ByteSize: ref.Blob.ByteSize, SHA256: ref.Blob.SHA256})
	}
	hash := payloadHash(struct {
		Input      TaskInput
		Upstream   string
		References []MediaModerationContent
	}{resolved, model.UpstreamModel, identities})
	if !p.checked {
		p.input, p.hash = resolved, hash
		p.refs = append([]referenceRecord{}, refs...)
		return failure(409, "MEDIA_MODERATION_PREPARATION_REQUIRED", "媒体审核准备中")
	}
	if p.err != nil {
		return p.err
	}
	if p.hash != hash {
		return failure(409, "MEDIA_MODERATION_CONTEXT_CHANGED", "媒体规格或引用已有变化，请核对后重新提交")
	}
	return nil
}

func (p *taskMediaModerationAdmission) check(ctx context.Context, a *App) {
	p.checked = true
	p.policy = a.mediaModeration.Snapshot()
	p.textPolicy, p.err = a.moderation.CheckInput(ctx, ChatRequest{Messages: []ChatMessage{{Role: "user", Content: p.input.Inputs.Prompt}}, MaxOutputTokens: 128, OutputLimitField: "max_tokens"})
	if p.err != nil {
		return
	}
	for _, ref := range p.refs {
		permit, permitErr := a.mediaModeration.acquireInputPermit(ctx, p.workspaceID)
		if permitErr != nil {
			p.err = permitErr
			return
		}
		file, err := a.references.OpenVerified(ctx, ref.Blob)
		if err != nil {
			permit.Close()
			p.err = mediaModerationInputError(ModerationDecision{State: "REVIEW", ReasonCode: "check_unavailable"})
			return
		}
		decision := permit.Check(ctx, p.policy, "input", MediaModerationContent{Kind: "image", ContentType: ref.Blob.ContentType, ByteSize: ref.Blob.ByteSize, SHA256: ref.Blob.SHA256}, file)
		_ = file.Close()
		permit.Close()
		if decision.State != "ALLOWED" {
			p.err = mediaModerationInputError(decision)
			return
		}
	}
}

func mediaModerationInputError(decision ModerationDecision) error {
	if decision.State == "BLOCKED" {
		return failure(422, "MEDIA_MODERATION_INPUT_BLOCKED", "参考素材未通过当前政策，未发起生成")
	}
	switch decision.ReasonCode {
	case "policy_review":
		return failure(409, "MEDIA_MODERATION_INPUT_REVIEW", "参考素材需要进一步核实，未发起生成")
	case "invalid_content", "content_too_large":
		return failure(422, "MEDIA_MODERATION_INPUT_INVALID", "参考素材不符合当前审核格式或大小限制，未发起生成")
	case "check_busy":
		return failure(429, "MEDIA_MODERATION_BUSY", "媒体审核繁忙，未发起生成")
	default:
		return failure(503, "MEDIA_MODERATION_UNAVAILABLE", "媒体审核未能确认，未发起生成")
	}
}

func (p *taskMediaModerationAdmission) attach(t *taskRecord) {
	if p.policy == nil {
		return
	}
	t.Task.Moderation = InitialMediaModerationSummary(p.policy, "output")
	t.MediaModeration = &MediaModerationRecord{Policy: p.policy, TextPolicy: p.textPolicy, Summary: copyRunModeration(t.Task.Moderation), Candidates: []Artifact{}}
}

func cloneTaskMediaModeration(source *MediaModerationRecord) *MediaModerationRecord {
	if source == nil {
		return nil
	}
	next := *source
	next.Summary = copyRunModeration(source.Summary)
	next.Candidates = append([]Artifact{}, source.Candidates...)
	return &next
}

func quarantineMediaTask(t *taskRecord, reason string) {
	if t.Task.Moderation == nil {
		return
	}
	t.Task.Artifacts = []Artifact{}
	t.Task.Moderation = copyRunModeration(t.Task.Moderation)
	if t.Task.Moderation.State == "PENDING" {
		t.Task.Moderation.State = "REVIEW"
		t.Task.Moderation.ReasonCode = reason
		t.Task.Moderation.Version++
	}
	t.Task.CanReconcile = false
	t.MediaModeration = cloneTaskMediaModeration(t.MediaModeration)
	if t.MediaModeration != nil {
		t.MediaModeration.Summary = copyRunModeration(t.Task.Moderation)
	}
}

func (a *App) checkTaskMediaCandidates(ctx context.Context, t *taskRecord, permits ...*mediaModerationPermit) ModerationDecision {
	state := t.MediaModeration
	if state == nil || state.Policy == nil || a.mediaModeration == nil || len(state.Candidates) == 0 || len(state.Candidates) > 4 {
		return moderationReview("policy_unavailable")
	}
	for _, artifact := range state.Candidates {
		itemCtx, cancelItem := context.WithTimeout(ctx, 2*time.Minute+mediaModerationTimeout)
		defer cancelItem() // At most four bounded candidate contexts per batch.
		frozen := artifact.StorageInfo
		if frozen == nil || frozen.ID != artifact.ID || artifact.Kind != t.Task.Kind {
			return moderationReview("invalid_content")
		}
		file, info, err := OpenMediaModerationAsset(itemCtx, a.assets, artifact.ID)
		if err != nil {
			return moderationReview("check_unavailable")
		}
		// A changed manifest, object generation or body is never a substitute
		// for the immutable bytes accepted as this candidate.
		if !sameMediaDeliveryReceipt(&info, frozen) {
			_ = file.Close()
			return moderationReview("invalid_content")
		}
		if err = verifyMediaReviewThumbnail(itemCtx, a.assets, info); err != nil {
			_ = file.Close()
			return moderationReview("check_unavailable")
		}
		content := MediaModerationContent{Kind: artifact.Kind, ContentType: info.ContentType, ByteSize: info.ByteSize, SHA256: info.SHA256}
		var decision ModerationDecision
		if len(permits) > 0 {
			var observation *MediaScheduleObservation
			decision, observation = permits[0].CheckObserved(itemCtx, state.Policy, "output", content, file)
			if observation != nil {
				if t.ScheduleOutcome == nil {
					a.observeMediaStep(t, MediaResult{Observation: observation}, nil)
				} else {
					t.ScheduleOutcome.Observation = mergeMediaScheduleObservations(t.ScheduleOutcome.Observation, observation)
					t.ScheduleOutcome.At = time.Now().UTC()
				}
			}
		} else {
			decision = a.mediaModeration.Check(itemCtx, state.Policy, "output", content, file)
		}
		_ = file.Close()
		cancelItem()
		if decision.State != "ALLOWED" {
			return decision
		}
	}
	return ModerationDecision{State: "ALLOWED", ReasonCode: "allowed"}
}

// New candidates and READY are one fenced commit. This ends the producer step;
// only the independent review lane can confirm STARTED and invoke the checker.
func (a *App) reviewTaskMediaResult(ctx context.Context, t *taskRecord, result MediaResult, lost *atomic.Bool) (MediaResult, bool) {
	decision := moderationReview("invalid_content")
	valid := t.MediaModeration != nil && t.MediaModeration.Policy != nil && len(result.Artifacts) > 0 && len(result.Artifacts) <= 4 && t.Task.Parameters != nil && len(result.Artifacts) <= t.Task.Parameters.Count
	if valid {
		valid = result.Status == "SUCCEEDED" && len(result.Artifacts) == t.Task.Parameters.Count || result.Status == "PARTIAL" && len(result.Artifacts) < t.Task.Parameters.Count
	}
	seen := map[string]bool{}
	for _, artifact := range result.Artifacts {
		if artifact.StorageInfo == nil || !validAssetInfo(*artifact.StorageInfo, artifact.ID) || artifact.Kind != t.Task.Kind || seen[artifact.ID] {
			valid = false
		}
		seen[artifact.ID] = true
	}
	if valid {
		t.MediaModeration = cloneTaskMediaModeration(t.MediaModeration)
		t.MediaModeration.Candidates = append([]Artifact{}, result.Artifacts...)
		t.MediaModeration.Summary = copyRunModeration(t.Task.Moderation)
		t.Task.Artifacts = []Artifact{}
		t.Task.Status = "MODERATING"
		t.Task.Progress = 99
		t.Task.CanReconcile = false
		t.Task.Version++
		t.Task.UpdatedAt = time.Now().UTC()
		var dispatchErr error
		t.ReviewDispatch, dispatchErr = newMediaReviewDispatch(t, result.Status)
		if dispatchErr != nil {
			return MediaResult{}, false
		}
		t.NextPoll = time.Now()
		for {
			a.mu.Lock()
			if lost.Load() || ctx.Err() != nil || a.ctx.Err() != nil {
				a.mu.Unlock()
				return MediaResult{}, false
			}
			err := a.persistTaskClaim(t, true)
			a.mu.Unlock()
			if err == nil {
				break
			}
			if !isEventBacklog(err) || !waitTaskPoll(ctx, 250*time.Millisecond) {
				return MediaResult{}, false
			}
		}
		return MediaResult{}, false
	}
	if lost.Load() || ctx.Err() != nil || a.ctx.Err() != nil {
		return MediaResult{}, false
	}
	t.Task.Moderation = copyRunModeration(t.Task.Moderation)
	t.Task.Moderation.State = decision.State
	t.Task.Moderation.ReasonCode = decision.ReasonCode
	t.Task.Moderation.Version++
	t.MediaModeration = cloneTaskMediaModeration(t.MediaModeration)
	if t.MediaModeration != nil {
		t.MediaModeration.Summary = copyRunModeration(t.Task.Moderation)
	}
	if decision.State != "ALLOWED" {
		result.Status = "FAILED"
		result.Artifacts = []Artifact{}
		result.Error = &APIError{Code: "MEDIA_MODERATION_REVIEW", Message: "媒体结果未完成审核，原候选保持隔离"}
		if decision.State == "BLOCKED" {
			result.Error = &APIError{Code: "MEDIA_MODERATION_OUTPUT_BLOCKED", Message: "媒体结果未通过当前政策，原候选保持隔离"}
		}
	}
	return result, true
}
