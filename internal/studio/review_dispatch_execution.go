package studio

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

type mediaReviewCancellation struct {
	attempt string
	cancel  context.CancelFunc
}

func (a *App) cancelMediaReviewLocked(id string) {
	if active, ok := a.activeReviews[id]; ok {
		active.cancel()
	}
}

func mediaReviewTimeout(t *taskRecord) time.Duration {
	count := 1
	if t.MediaModeration != nil {
		count = max(1, min(4, len(t.MediaModeration.Candidates)))
	}
	return time.Duration(count)*(2*time.Minute+mediaModerationTimeout) + 30*time.Second
}

func (a *App) executeMediaReviewClaim(conn *pgx.Conn, t *taskRecord, permit *mediaModerationPermit) {
	ctx, cancel := context.WithTimeout(a.ctx, mediaReviewTimeout(t))
	defer cancel()
	if !a.renewTaskClaimForStep(ctx, conn, *t.Lease) {
		return
	}
	activeID := newID("review_active")
	a.mu.Lock()
	if a.activeReviews == nil {
		a.activeReviews = make(map[string]mediaReviewCancellation)
	}
	a.activeReviews[t.Task.ID] = mediaReviewCancellation{attempt: activeID, cancel: cancel}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.activeReviews[t.Task.ID].attempt == activeID {
			delete(a.activeReviews, t.Task.ID)
		}
		a.mu.Unlock()
	}()
	beatCtx, stopBeat := context.WithCancel(ctx)
	done := make(chan struct{})
	var lost atomic.Bool
	token := *t.Lease
	go func() {
		defer close(done)
		ticker := time.NewTicker(a.taskLeases.duration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-beatCtx.Done():
				return
			case <-ticker.C:
				if err := a.renewTaskClaim(beatCtx, conn, token); err != nil {
					if beatCtx.Err() == nil {
						lost.Store(true)
						cancel()
						a.taskLeaseDatabaseFailure(err)
					}
					return
				}
			}
		}
	}()
	defer func() { stopBeat(); <-done }()
	decision := moderationReview("execution_incomplete")
	if t.ReviewDispatch.State == "READY" {
		if t.MediaModeration == nil || !a.mediaModeration.compatiblePolicy(t.MediaModeration.Policy) || permit == nil {
			decision = moderationReview("policy_unavailable")
		} else {
			now := time.Now().UTC().Truncate(time.Microsecond)
			t.ReviewDispatch = cloneMediaReviewDispatch(t.ReviewDispatch)
			t.ReviewDispatch.State = "STARTED"
			t.ReviewDispatch.AttemptID = newID("review")
			t.ReviewDispatch.StartedAt = &now
			t.Task.Version++
			t.Task.UpdatedAt = now
			if a.saveDeliveryClaim(ctx, t, false, &lost) != nil {
				return
			}
			// A confirmed STARTED is necessary but not sufficient: cancellation
			// may have revoked this lease while COMMIT was being acknowledged.
			// The heartbeat owns conn now; use the fenced business snapshot check
			// below without racing an independent command on that pgx session.
			a.mu.Lock()
			current := a.tasks[t.Task.ID]
			valid := ctx.Err() == nil && !lost.Load() && current != nil && current.Task.Version == t.Task.Version && current.Task.Status == "MODERATING"
			a.mu.Unlock()
			if !valid {
				return
			}
			decision = a.checkTaskMediaCandidates(ctx, t, permit)
		}
	}
	if ctx.Err() != nil || a.ctx.Err() != nil || lost.Load() {
		return
	}
	t.Task.Moderation = copyRunModeration(t.Task.Moderation)
	if t.Task.Moderation == nil {
		return
	}
	t.Task.Moderation.State = decision.State
	t.Task.Moderation.ReasonCode = decision.ReasonCode
	t.Task.Moderation.Version++
	t.MediaModeration = cloneTaskMediaModeration(t.MediaModeration)
	if t.MediaModeration == nil {
		return
	}
	t.MediaModeration.Summary = copyRunModeration(t.Task.Moderation)
	t.Task.Status = "FAILED"
	t.Task.Artifacts = []Artifact{}
	t.Task.CanReconcile = false
	t.Task.Error = &APIError{Code: "MEDIA_MODERATION_REVIEW", Message: "媒体结果未完成审核，原候选保持隔离"}
	if decision.State == "BLOCKED" {
		t.Task.Error = &APIError{Code: "MEDIA_MODERATION_OUTPUT_BLOCKED", Message: "媒体结果未通过当前政策，原候选保持隔离"}
	}
	if decision.State == "ALLOWED" {
		t.Task.Status = t.ReviewDispatch.ResultStatus
		t.Task.Progress = 100
		t.Task.Artifacts = append([]Artifact{}, t.MediaModeration.Candidates...)
		t.Task.Error = nil
		if t.Task.Status == "PARTIAL" {
			t.Task.Error = &APIError{Code: "PARTIAL_OUTPUT", Message: "供应商实际交付少于请求数量，已保留通过审核的整组结果"}
		}
	}
	t.Task.Version++
	t.Task.UpdatedAt = time.Now().UTC()
	// The same decision stays in this attempt while bounded Outbox pressure
	// clears. Neither bytes nor the external checker are invoked again.
	_ = a.saveDeliveryClaim(ctx, t, true, &lost)
}
