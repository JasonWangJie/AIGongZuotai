package studio

import (
	"errors"
	"sync/atomic"
)

// MaxMediaQueryWorkers caps simultaneous original-handle queries in one controller.
const MaxMediaQueryWorkers = 1536

// These execution budgets are separate from accepted-resource admission limits.
type MediaSchedulingConfig struct {
	WorkspaceConcurrency      int `json:"workspace_concurrency"`
	ChannelConcurrency        int `json:"channel_concurrency"`
	ReviewWorkers             int `json:"review_workers"`
	DeliveryWorkers           int `json:"delivery_workers"`
	SubmitWorkers             int `json:"submit_workers"`
	QueryWorkers              int `json:"query_workers"`
	QueryDBConnections        int `json:"query_db_connections,omitempty"`
	QueryWorkspaceConcurrency int `json:"query_workspace_concurrency,omitempty"`
	QueryChannelConcurrency   int `json:"query_channel_concurrency,omitempty"`
}

func (c *MediaSchedulingConfig) normalize(legacyWorkers int) error {
	if c.WorkspaceConcurrency == 0 {
		c.WorkspaceConcurrency = 1
	}
	if c.ChannelConcurrency == 0 {
		c.ChannelConcurrency = 2
	}
	if c.WorkspaceConcurrency < 1 || c.WorkspaceConcurrency > 32 || c.ChannelConcurrency < 1 || c.ChannelConcurrency > 32 {
		return errors.New("invalid media scoped execution limits")
	}
	if c.ReviewWorkers == 0 {
		c.ReviewWorkers = 2
	}
	if c.DeliveryWorkers == 0 {
		c.DeliveryWorkers = 2
	}
	if c.SubmitWorkers == 0 {
		c.SubmitWorkers = max(1, (legacyWorkers+1)/2)
	}
	if c.QueryWorkers == 0 {
		c.QueryWorkers = max(1, legacyWorkers/2)
	}
	if c.QueryWorkspaceConcurrency < 0 || c.QueryWorkspaceConcurrency > MaxMediaQueryWorkers || c.QueryChannelConcurrency < 0 || c.QueryChannelConcurrency > MaxMediaQueryWorkers {
		return errors.New("query scope ceilings must be between 0 and 1536")
	}
	if c.QueryWorkers > 32 && c.QueryDBConnections == 0 {
		return errors.New("more than 32 query workers requires a bounded query database pool")
	}
	if c.QueryDBConnections < 0 || c.QueryDBConnections > min(c.QueryWorkers, 32) {
		return errors.New("query database connections must fit the query worker budget")
	}
	if c.SubmitWorkers < 1 || c.SubmitWorkers > 32 || c.QueryWorkers < 1 || c.QueryWorkers > MaxMediaQueryWorkers || c.DeliveryWorkers < 1 || c.DeliveryWorkers > 16 || c.ReviewWorkers < 1 || c.ReviewWorkers > 2 {
		return errors.New("invalid media execution limits")
	}
	return nil
}

type mediaExecutionLane uint8

const (
	mediaSubmitLane mediaExecutionLane = iota
	mediaQueryLane
	mediaDeliveryLane
	mediaReviewLane
)

func mediaLaneForHandle(handle MediaHandle) mediaExecutionLane {
	if handle.ID == "" {
		return mediaSubmitLane
	}
	return mediaQueryLane
}

type MediaExecutionSnapshot struct {
	QueryDispatchAttempts uint64
	ReviewInFlight        int
	ReviewLimit           int
	ReviewSteps           uint64
	DeliveryInFlight      int
	DeliveryLimit         int
	DeliverySteps         uint64
	SubmitInFlight        int
	QueryInFlight         int
	SubmitLimit           int
	QueryLimit            int
	SubmitSteps           uint64
	QuerySteps            uint64
}

// A reservation starts before selecting/claiming work and ends only when the
// entire step returns, including heartbeat shutdown, review and persistence.
// It never requires App.mu, and full lanes never spawn a waiting goroutine.
type mediaExecutionLanes struct {
	queryDispatchAttempts atomic.Uint64
	slots                 [4]chan struct{}
	steps                 [4]atomic.Uint64
	// App.mu protects these attempt identities; no task body or credential.
	memoryAttempts map[string]string
}

func newMediaExecutionLanes(c MediaSchedulingConfig) *mediaExecutionLanes {
	return &mediaExecutionLanes{slots: [4]chan struct{}{make(chan struct{}, c.SubmitWorkers), make(chan struct{}, c.QueryWorkers), make(chan struct{}, c.DeliveryWorkers), make(chan struct{}, c.ReviewWorkers)}, memoryAttempts: make(map[string]string)}
}

// The bounded task cache itself is the memory-mode due queue. Selecting work
// only after reserving a lane avoids a second queue and stale queued claims.
func (a *App) executeMemoryTaskLane(lane mediaExecutionLane, id string) bool {
	// Media moderation requires authenticated PostgreSQL; no in-memory first
	// review queue is manufactured for unsupported demo configurations.
	if lane == mediaReviewLane {
		return false
	}
	release, ok := a.mediaExecution.reserve(lane)
	if !ok {
		return false
	}
	defer release()
	a.mu.Lock()
	if a.ctx.Err() != nil || a.media == nil {
		a.mu.Unlock()
		return false
	}
	for _, t := range a.tasks {
		if _, err := mediaFrozenChannel(t, mediaLaneForTask(t)); err != nil && markMediaScheduleIdentity(t) {
			a.publishTaskLocked(t.Task)
		}
	}
	selected, candidate := a.selectMemoryScheduleLocked(lane, id)
	if selected == nil {
		a.mu.Unlock()
		return false
	}
	scopeRelease, ok := a.mediaScheduler.tryReserve(*candidate)
	if !ok {
		a.mu.Unlock()
		return false
	}
	defer scopeRelease()
	selected.ScheduleClaim = candidate
	a.claimMemoryScheduleProbe(candidate)
	defer a.finishMemoryScheduleProbe(candidate)
	id = selected.Task.ID
	attempt := newID("media_attempt")
	a.mediaExecution.memoryAttempts[id] = attempt
	selected.InFlight = true
	a.mediaExecution.steps[lane].Add(1)
	a.mediaScheduler.claimed(lane)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.mediaExecution.memoryAttempts[id] == attempt {
			delete(a.mediaExecution.memoryAttempts, id)
			if current := a.tasks[id]; current != nil {
				current.InFlight = false
			}
		}
	}()
	if lane == mediaDeliveryLane {
		a.executeMemoryDeliveryRecord(id, selected, attempt)
	} else {
		a.executeMemoryTaskRecord(id, selected, attempt)
	}
	return true
}

func (m *mediaExecutionLanes) reserve(lane mediaExecutionLane) (func(), bool) {
	if lane > mediaReviewLane {
		return nil, false
	}
	select {
	case m.slots[lane] <- struct{}{}:
		return func() { <-m.slots[lane] }, true
	default:
		return nil, false
	}
}

func (m *mediaExecutionLanes) snapshot() MediaExecutionSnapshot {
	return MediaExecutionSnapshot{QueryDispatchAttempts: m.queryDispatchAttempts.Load(), SubmitInFlight: len(m.slots[mediaSubmitLane]), QueryInFlight: len(m.slots[mediaQueryLane]), SubmitLimit: cap(m.slots[mediaSubmitLane]), QueryLimit: cap(m.slots[mediaQueryLane]), SubmitSteps: m.steps[mediaSubmitLane].Load(), QuerySteps: m.steps[mediaQueryLane].Load(), DeliveryInFlight: len(m.slots[mediaDeliveryLane]), DeliveryLimit: cap(m.slots[mediaDeliveryLane]), DeliverySteps: m.steps[mediaDeliveryLane].Load(), ReviewInFlight: len(m.slots[mediaReviewLane]), ReviewLimit: cap(m.slots[mediaReviewLane]), ReviewSteps: m.steps[mediaReviewLane].Load()}
}

func mediaLaneForTask(t *taskRecord) mediaExecutionLane {
	if t.ReviewDispatch != nil {
		return mediaReviewLane
	}
	if t.Delivery != nil {
		return mediaDeliveryLane
	}
	return mediaLaneForHandle(t.Handle)
}

func (a *App) mediaTaskWorker(lane mediaExecutionLane) {
	defer a.wg.Done()
	if !waitTaskPoll(a.ctx, a.config.PollInterval) {
		return
	}
	for a.ctx.Err() == nil {
		worked := false
		if a.store != nil {
			worked = a.executePersistentTaskLane(lane, "")
		} else {
			worked = a.executeMemoryTaskLane(lane, "")
		}
		if !worked && !waitTaskPoll(a.ctx, a.config.PollInterval) {
			return
		}
	}
}
