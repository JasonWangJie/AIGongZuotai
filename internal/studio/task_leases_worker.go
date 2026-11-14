package studio

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) runPersistentTaskWorker() {
	if !waitTaskPoll(a.ctx, a.config.PollInterval) {
		return
	}
	for a.ctx.Err() == nil {
		if a.media == nil {
			if !waitTaskPoll(a.ctx, a.config.PollInterval) {
				return
			}
			continue
		}
		worked := a.executePersistentTaskStep("")
		if !worked && !waitTaskPoll(a.ctx, a.config.PollInterval) {
			return
		}
	}
}
func waitTaskPoll(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isEventBacklog(err error) bool {
	var op *operationError
	return errors.As(err, &op) && op.code == "EVENT_BACKLOG_FULL"
}

func (a *App) executePersistentTaskStep(id string) bool {
	pool := a.taskLeases
	if pool == nil || a.media == nil || a.ctx.Err() != nil {
		return false
	}
	conn, err := pool.acquire(a.ctx)
	if err != nil {
		return false
	}
	defer pool.release(conn)
	record, err := pool.claim(a.ctx, conn, a.config.Mode, id)
	if err != nil {
		a.taskLeaseDatabaseFailure(err)
		return false
	}
	if record == nil {
		return false
	}
	a.obs.recordLease("claimed", 1)
	a.executeTaskClaim(conn, record)
	return true
}

func (a *App) taskLeaseDatabaseFailure(err error) {
	if err == nil || isLeaseLost(err) || a.ctx.Err() != nil {
		return
	}
	a.store.failed.Store(true)
	a.cancel()
}

func cloneLeasedRecord(record *taskRecord) *taskRecord {
	copy := *record
	copy.Task = copyTask(record.Task)
	if record.Lease != nil {
		lease := *record.Lease
		copy.Lease = &lease
	}
	return &copy
}

// All business writes still use the advisory-lock owner's transaction. Only
// committed snapshots replace the cache or become workspace notifications.
func (a *App) persistTaskClaim(record *taskRecord, complete bool) error {
	record.LeaseComplete = complete
	if err := a.store.saveTask(record); err != nil {
		if isLeaseLost(err) {
			a.obs.recordLease("rejected", 1)
		}
		return err
	}
	record.Lease.Version = record.Task.Version
	record.LeaseStatus = record.Task.Status
	cached := cloneLeasedRecord(record)
	if complete {
		cached.Lease = nil
		cached.LeaseComplete = false
		cached.InFlight = false
	}
	a.evictTerminalsLocked()
	a.tasks[record.Task.ID] = cached
	a.publishTaskLocked(record.Task)
	return nil
}

func (a *App) renewTaskClaim(ctx context.Context, conn *pgx.Conn, token TaskLease) error {
	err := a.taskLeases.renew(ctx, conn, token)
	if err == nil {
		a.obs.recordLease("renewed", 1)
	} else if isLeaseLost(err) {
		a.obs.recordLease("lost", 1)
	}
	return err
}

func (a *App) renewTaskClaimForStep(ctx context.Context, conn *pgx.Conn, token TaskLease) bool {
	err := a.renewTaskClaim(ctx, conn, token)
	if err != nil && ctx.Err() == nil {
		a.taskLeaseDatabaseFailure(err)
	}
	return err == nil
}

func (a *App) executeTaskClaim(conn *pgx.Conn, record *taskRecord) {
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if cached := a.tasks[record.Task.ID]; cached != nil && cached.Lease != nil && cached.Lease.Owner == record.Lease.Owner && cached.Lease.Epoch == record.Lease.Epoch {
			cached.InFlight = false
			cached.Lease = nil
			cached.LeaseComplete = false
			cached.LeaseStatus = ""
		}
	}()
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	// A reclaimed SUBMITTING lease can never authorize a second POST. Losing
	// the original successful response is ambiguous even with an exact body/key.
	if record.Handle.ID == "" && (record.Task.Status == "SUBMITTING" || a.config.Mode == "live" && record.Submission == nil) {
		record.Task.Status = "SUBMIT_UNKNOWN"
		record.Task.Error = &APIError{Code: "SUBMISSION_UNKNOWN", Message: "提交租约失效且无受理ID，禁止自动重新提交"}
		if record.LeaseStatus != "SUBMITTING" {
			record.Task.Status = "RECONCILE_REQUIRED"
			record.Task.Error = &APIError{Code: "EXECUTION_SNAPSHOT_MISSING", Message: "原执行快照缺失，禁止重新构造提交"}
		}
		record.Task.Version++
		record.Task.UpdatedAt = time.Now().UTC()
		record.Task.CanReconcile = false
		a.persistTaskClaim(record, true)
		a.mu.Unlock()
		return
	}
	if record.Handle.ID == "" {
		record.Task.Status = "SUBMITTING"
		record.Task.Version++
		record.Task.UpdatedAt = time.Now().UTC()
		if a.persistTaskClaim(record, false) != nil {
			a.mu.Unlock()
			return
		}
	}
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	// Revalidate immediately before external work. A paused worker whose
	// durable SUBMITTING snapshot has been reclaimed must never send its POST.
	if !a.renewTaskClaimForStep(ctx, conn, *record.Lease) {
		return
	}
	beatCtx, stopBeat := context.WithCancel(ctx)
	done := make(chan struct{})
	var lost atomic.Bool
	token := *record.Lease
	go func() {
		defer close(done)
		timer := time.NewTicker(a.taskLeases.duration / 3)
		defer timer.Stop()
		for {
			select {
			case <-beatCtx.Done():
				return
			case <-timer.C:
				if err := a.renewTaskClaim(beatCtx, conn, token); err != nil {
					if beatCtx.Err() != nil {
						return
					}
					lost.Store(true)
					cancel()
					a.taskLeaseDatabaseFailure(err)
					return
				}
			}
		}
	}()
	var result MediaResult
	var err error
	handle := record.Handle
	if handle.ID == "" {
		if prepared, ok := a.media.(interface {
			SubmitPrepared(context.Context, MediaSubmission) (MediaHandle, error)
		}); ok && record.Submission != nil {
			handle, err = prepared.SubmitPrepared(ctx, *record.Submission)
		} else {
			handle, err = a.media.Submit(ctx, record.Input)
		}
		result = MediaResult{Status: "QUEUED", RetryAfter: handle.RetryAfter}
	} else {
		result, err = a.media.Query(ctx, handle)
	}
	stopBeat()
	<-done
	a.mu.Lock()
	if lost.Load() || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	applyLeasedMediaResult(record, handle, result, err, a.config.Mode)
	record.Task.Version++
	record.Task.UpdatedAt = time.Now().UTC()
	record.Task.CanReconcile = a.canReconcileTask(record)
	interval := a.config.PollInterval
	if a.config.Mode == "live" && interval < 3*time.Second {
		interval = 3 * time.Second
	}
	if result.RetryAfter > interval {
		interval = result.RetryAfter
	}
	var providerError *MediaProviderError
	if errors.As(err, &providerError) && providerError.RetryAfter > interval {
		interval = providerError.RetryAfter
	}
	record.NextPoll = time.Now().Add(interval)
	for {
		persistErr := a.persistTaskClaim(record, true)
		a.mu.Unlock()
		if !isEventBacklog(persistErr) {
			return
		}
		// Retain the known handle/result in this claim and retry only its
		// transaction, never Submit/Query. A shutdown leaves SUBMITTING parked.
		if !waitTaskPoll(ctx, 250*time.Millisecond) {
			return
		}
		if !a.renewTaskClaimForStep(ctx, conn, *record.Lease) {
			return
		}
		a.mu.Lock()
		if a.ctx.Err() != nil {
			a.mu.Unlock()
			return
		}
	}
}

func applyLeasedMediaResult(t *taskRecord, handle MediaHandle, result MediaResult, err error, mode string) {
	if err == nil {
		t.Handle = handle
		t.Task.Error = result.Error
		t.Task.Status = result.Status
		t.Task.Progress = result.Progress
		if len(result.Artifacts) > 0 {
			t.Task.Artifacts = result.Artifacts
		}
		return
	}
	var providerError *MediaProviderError
	if errors.As(err, &providerError) {
		t.Task.Error = &APIError{Code: providerError.Code, Message: providerError.Message}
		switch {
		case providerError.SubmissionUnknown:
			t.Task.Status = "SUBMIT_UNKNOWN"
		case providerError.ReconcileRequired:
			t.Task.Status = "RECONCILE_REQUIRED"
		case providerError.Retryable && handle.ID != "":
			t.Task.Status = "PROVIDER_STATE_UNKNOWN"
		default:
			t.Task.Status = "FAILED"
		}
	} else if handle.ID == "" && mode == "live" {
		t.Task.Status = "SUBMIT_UNKNOWN"
		t.Task.Error = &APIError{Code: "SUBMISSION_UNKNOWN", Message: "提交结果未知，禁止自动重提"}
	} else {
		t.Task.Status = "FAILED"
		t.Task.Error = &APIError{Code: "PROVIDER_FAILED", Message: "媒体任务执行失败"}
	}
}
