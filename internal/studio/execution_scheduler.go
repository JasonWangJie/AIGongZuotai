package studio

import (
	"errors"
	"time"
)

// ChatSchedulingConfig bounds execution, separately from V05's accepted but
// unfinished resources. Zero values are defaults for in-package callers.
type ChatSchedulingConfig struct {
	Workers              int `json:"workers"`
	QueueSize            int `json:"queue_size"`
	WorkspaceConcurrency int `json:"workspace_concurrency"`
	ModelConcurrency     int `json:"model_concurrency"`
	WorkspaceQueueSize   int `json:"workspace_queue_size"`
}

func (c *ChatSchedulingConfig) normalize(workers, queue int) error {
	if c.Workers == 0 {
		c.Workers = workers
	}
	if c.QueueSize == 0 {
		c.QueueSize = queue
	}
	if c.WorkspaceConcurrency == 0 {
		c.WorkspaceConcurrency = min(2, c.Workers)
	}
	if c.ModelConcurrency == 0 {
		c.ModelConcurrency = c.Workers
	}
	if c.WorkspaceQueueSize == 0 {
		c.WorkspaceQueueSize = min(8, c.QueueSize)
	}
	if c.Workers < 1 || c.Workers > 64 || c.QueueSize < 1 || c.QueueSize > 4096 || c.WorkspaceConcurrency < 1 || c.WorkspaceConcurrency > 64 || c.ModelConcurrency < 1 || c.ModelConcurrency > 64 || c.WorkspaceQueueSize < 1 || c.WorkspaceQueueSize > 4096 {
		return errors.New("invalid chat scheduling limits")
	}
	return nil
}

type executionJob struct {
	ID, WorkspaceID, ModelKey string
	EnqueuedAt                time.Time
	childEligible             bool
	dispatchedToChild         bool
}
type ExecutionSchedulerSnapshot struct {
	Queued           int                  `json:"queued"`
	Running          int                  `json:"running"`
	WorkspaceCount   int                  `json:"workspace_count"`
	Limits           ChatSchedulingConfig `json:"limits"`
	Dispatched       uint64               `json:"dispatched"`
	WaitSecondsTotal float64              `json:"wait_seconds_total"`
	WaitSecondsMax   float64              `json:"wait_seconds_max"`
}

// All methods require App.mu. The scheduler never calls providers, storage or
// other locks. Workers copy wake under that lock and wait only after unlocking.
type executionScheduler struct {
	limits                           ChatSchedulingConfig
	queued                           map[string]executionJob
	workspaces                       map[string][]string
	order                            []string
	next                             int
	running                          map[string]executionJob
	workspaceRunning                 map[string]int
	modelRunning                     map[string]int
	localRunning                     int
	childDispatches, localFallbacks  uint64
	wake                             chan struct{}
	dispatched                       uint64
	waitSecondsTotal, waitSecondsMax float64
}

func newExecutionScheduler(c ChatSchedulingConfig) *executionScheduler {
	return &executionScheduler{limits: c, queued: map[string]executionJob{}, workspaces: map[string][]string{}, running: map[string]executionJob{}, workspaceRunning: map[string]int{}, modelRunning: map[string]int{}, wake: make(chan struct{})}
}
func (s *executionScheduler) signalLocked() { close(s.wake); s.wake = make(chan struct{}) }
func (s *executionScheduler) checkLocked(workspace string) error {
	if len(s.queued) >= s.limits.QueueSize {
		return failure(503, "RUN_QUEUE_FULL", "聊天等待队列已满，本次未受理；请稍后使用原请求核对")
	}
	if len(s.workspaces[workspace]) >= s.limits.WorkspaceQueueSize {
		return failure(429, "EXECUTION_WORKSPACE_QUEUE_LIMIT", "当前工作区聊天等待队列已满，本次未受理；已受理请求可按原键核对")
	}
	return nil
}

// The caller checked capacity while holding App.mu and kept that lock through
// its commit. Thus publishing this committed job cannot fail or block.
func (s *executionScheduler) enqueueLocked(job executionJob) {
	if _, exists := s.queued[job.ID]; exists {
		return
	}
	if _, exists := s.running[job.ID]; exists {
		return
	}
	if len(s.workspaces[job.WorkspaceID]) == 0 {
		s.order = append(s.order, job.WorkspaceID)
	}
	s.workspaces[job.WorkspaceID] = append(s.workspaces[job.WorkspaceID], job.ID)
	s.queued[job.ID] = job
	s.signalLocked()
}
func (s *executionScheduler) removeWorkspaceLocked(index int) {
	delete(s.workspaces, s.order[index])
	s.order = append(s.order[:index], s.order[index+1:]...)
	if index < s.next {
		s.next--
	}
	if len(s.order) == 0 {
		s.next = 0
	} else {
		s.next %= len(s.order)
	}
}

// Work-conserving among eligible groups: round robin across workspaces, FIFO
// within one workspace/model. A saturated model cannot block a different model
// behind it, but no running request is preempted or transparently retried.
func (s *executionScheduler) pickLocked(now time.Time) (executionJob, bool) {
	if s.localRunning >= s.limits.Workers {
		return executionJob{}, false
	}
	return s.pickRunnableLocked(now, func(executionJob) chatDispatchRoute { return chatLocal })
}

type chatDispatchRoute uint8

const (
	chatUnavailable chatDispatchRoute = iota
	chatLocal
	chatChild
)

// reserve examines only work that already fits both shared scope limits. It
// must return immediately and may reserve one actual child slot on success.
func (s *executionScheduler) pickRunnableLocked(now time.Time, reserve func(executionJob) chatDispatchRoute) (executionJob, bool) {
	for offset := 0; offset < len(s.order); offset++ {
		index := (s.next + offset) % len(s.order)
		workspace := s.order[index]
		if s.workspaceRunning[workspace] >= s.limits.WorkspaceConcurrency {
			continue
		}
		ids := s.workspaces[workspace]
		for pos, id := range ids {
			job := s.queued[id]
			if s.modelRunning[job.ModelKey] >= s.limits.ModelConcurrency {
				continue
			}
			route := reserve(job)
			if route == chatUnavailable {
				continue
			}
			job.dispatchedToChild = route == chatChild
			if job.dispatchedToChild {
				s.childDispatches++
			} else {
				s.localRunning++
				if job.childEligible {
					s.localFallbacks++
				}
			}
			delete(s.queued, id)
			ids = append(ids[:pos], ids[pos+1:]...)
			s.workspaces[workspace] = ids
			if len(ids) == 0 {
				s.removeWorkspaceLocked(index)
				if len(s.order) > 0 {
					s.next = index % len(s.order)
				}
			} else {
				s.next = (index + 1) % len(s.order)
			}
			s.running[id] = job
			s.workspaceRunning[workspace]++
			s.modelRunning[job.ModelKey]++
			wait := now.Sub(job.EnqueuedAt).Seconds()
			if wait < 0 {
				wait = 0
			}
			s.dispatched++
			s.waitSecondsTotal += wait
			s.waitSecondsMax = max(s.waitSecondsMax, wait)
			return job, true
		}
	}
	return executionJob{}, false
}
func (s *executionScheduler) cancelLocked(id string) bool {
	job, exists := s.queued[id]
	if !exists {
		return false
	}
	ids := s.workspaces[job.WorkspaceID]
	for i, value := range ids {
		if value == id {
			ids = append(ids[:i], ids[i+1:]...)
			break
		}
	}
	delete(s.queued, id)
	s.workspaces[job.WorkspaceID] = ids
	if len(ids) == 0 {
		for i, workspace := range s.order {
			if workspace == job.WorkspaceID {
				s.removeWorkspaceLocked(i)
				break
			}
		}
	}
	s.signalLocked()
	return true
}
func (s *executionScheduler) releaseLocked(id string) {
	job, exists := s.running[id]
	if !exists {
		return
	}
	delete(s.running, id)
	if !job.dispatchedToChild {
		s.localRunning--
	}
	s.workspaceRunning[job.WorkspaceID]--
	s.modelRunning[job.ModelKey]--
	if s.workspaceRunning[job.WorkspaceID] == 0 {
		delete(s.workspaceRunning, job.WorkspaceID)
	}
	if s.modelRunning[job.ModelKey] == 0 {
		delete(s.modelRunning, job.ModelKey)
	}
	s.signalLocked()
}
func (s *executionScheduler) snapshotLocked(_ time.Time) ExecutionSchedulerSnapshot {
	workspaces := map[string]struct{}{}
	for w := range s.workspaces {
		workspaces[w] = struct{}{}
	}
	for w := range s.workspaceRunning {
		workspaces[w] = struct{}{}
	}
	return ExecutionSchedulerSnapshot{Queued: len(s.queued), Running: len(s.running), WorkspaceCount: len(workspaces), Limits: s.limits, Dispatched: s.dispatched, WaitSecondsTotal: s.waitSecondsTotal, WaitSecondsMax: s.waitSecondsMax}
}

// Unlike the global metrics snapshot, this projection never reveals other
// tenants' counts or their dispatch timing. Limits are configuration, not use.
func (s *executionScheduler) workspaceSnapshotLocked(workspace string, _ time.Time) ExecutionSchedulerSnapshot {
	q, r := len(s.workspaces[workspace]), s.workspaceRunning[workspace]
	n := 0
	if q+r > 0 {
		n = 1
	}
	return ExecutionSchedulerSnapshot{Queued: q, Running: r, WorkspaceCount: n, Limits: s.limits}
}
