package studio

import (
	"context"
	"io"
	"sync"
)

// A permit is bound to exactly one engine and occupies its existing input /
// output pool. Copies share the state so neither copied values nor concurrent
// Close calls can release the slot twice. A batch may check items sequentially,
// but cannot use one permit to launch concurrent checks.
type mediaModerationPermit struct {
	state *mediaModerationPermitState
}

type mediaModerationPermitState struct {
	mu       sync.Mutex
	engine   *mediaModerationEngine
	slots    chan struct{}
	active   bool
	closed   bool
	released bool
	cancel   context.CancelFunc
}

// compatiblePolicy intentionally accepts a valid frozen policy from the same
// adapter, even after the current rules/version changed. It never substitutes
// the current policy for a task's snapshot.
func (e *mediaModerationEngine) compatiblePolicy(p *MediaModerationPolicy) bool {
	return e != nil && validMediaModerationPolicy(p) && p.ProviderMode == e.providerMode && p.Mode == e.policy.Mode && p.AdapterIdentity == e.policy.AdapterIdentity
}

// Disabled/uninitialized engines do not wait for capacity. The caller can
// claim and quarantine their READY work without ever starting a check. Busy
// engines return immediately, leaving the durable work unclaimed.
func (e *mediaModerationEngine) tryAcquireReviewPermit() (*mediaModerationPermit, bool) {
	if e == nil || e.slots == nil {
		return nil, true
	}
	e.permits.mu.Lock()
	defer e.permits.mu.Unlock()
	e.dispatchPermitsLocked()
	if e.permits.inputWaiting > 0 || len(e.permits.outputs) > 0 {
		return nil, false
	}
	select {
	case e.slots <- struct{}{}:
		return e.newPermitLocked(), true
	default:
		return nil, false
	}
}

// Close forbids new checks immediately and cancels any active one. An active
// check keeps the physical slot until its byte reader / HTTP call has actually
// returned, even if that reader does not promptly obey context cancellation.
func (p *mediaModerationPermit) Close() {
	if p == nil || p.state == nil {
		return
	}
	s := p.state
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	cancel := s.cancel
	if !s.active {
		s.releaseLocked()
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *mediaModerationPermitState) releaseLocked() {
	if !s.released {
		s.released = true
		s.engine.releasePermit(s.slots)
	}
}

func (p *mediaModerationPermit) Check(ctx context.Context, policy *MediaModerationPolicy, phase string, content MediaModerationContent, reader io.ReadSeeker) ModerationDecision {
	decision, _ := p.CheckObserved(ctx, policy, phase, content, reader)
	return decision
}
func (p *mediaModerationPermit) CheckObserved(ctx context.Context, policy *MediaModerationPolicy, phase string, content MediaModerationContent, reader io.ReadSeeker) (ModerationDecision, *MediaScheduleObservation) {
	observation := &MediaScheduleObservation{Scope: "review", Class: "protocol"}
	if p == nil || p.state == nil {
		observation.Class = "identity"
		return moderationReview("policy_unavailable"), observation
	}
	decision := p.state.engine.checkWithPermitObservation(ctx, p, policy, phase, content, reader, observation)
	return decision, observation
}

// Keeping the engine check here prevents an accidentally passed permit from
// borrowing another engine's budget, including engines with identical policies.
func (e *mediaModerationEngine) checkWithReviewPermit(ctx context.Context, p *mediaModerationPermit, policy *MediaModerationPolicy, phase string, content MediaModerationContent, reader io.ReadSeeker) ModerationDecision {
	return e.checkWithPermitObservation(ctx, p, policy, phase, content, reader, &MediaScheduleObservation{Scope: "review", Class: "protocol"})
}
func (e *mediaModerationEngine) checkWithPermitObservation(ctx context.Context, p *mediaModerationPermit, policy *MediaModerationPolicy, phase string, content MediaModerationContent, reader io.ReadSeeker, observation *MediaScheduleObservation) ModerationDecision {
	if p == nil || p.state == nil || p.state.engine != e || !e.compatiblePolicy(policy) {
		observation.Class = "identity"
		return moderationReview("policy_unavailable")
	}
	if (phase != "input" && phase != "output") || !validMediaModerationContent(content, policy) {
		return moderationReview("invalid_content")
	}
	s := p.state
	s.mu.Lock()
	if s.closed || s.released {
		s.mu.Unlock()
		return moderationReview("policy_unavailable")
	}
	if s.active {
		s.mu.Unlock()
		return moderationReview("check_busy")
	}
	checkCtx, cancel := context.WithTimeout(ctx, mediaModerationTimeout)
	s.active = true
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()
	// The deferred cleanup also protects capacity if a caller-provided reader
	// panics. The permit itself creates no background waiter; cleanup runs on
	// the check's own stack after its byte/protocol work returns.
	defer func() {
		s.mu.Lock()
		s.active = false
		s.cancel = nil
		if s.closed {
			s.releaseLocked()
		}
		s.mu.Unlock()
	}()
	decision := e.checkMediaWithObservation(checkCtx, policy, phase, content, reader, observation)
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed || checkCtx.Err() != nil {
		observation.Class = "temporary"
		if closed || ctx.Err() != nil {
			observation.Class = "cancelled"
		}
		return moderationReview("check_timeout")
	}
	return decision
}
