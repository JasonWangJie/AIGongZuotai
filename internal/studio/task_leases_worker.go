package studio

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

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
	return a.executePersistentTaskLane(mediaSubmitLane, id) || a.executePersistentTaskLane(mediaQueryLane, id) || a.executePersistentTaskLane(mediaDeliveryLane, id) || a.executePersistentTaskLane(mediaReviewLane, id)
}

func (a *App) executePersistentTaskLane(lane mediaExecutionLane, id string) bool {
	pool := a.taskLeases
	if pool == nil || a.media == nil && lane != mediaReviewLane || a.ctx.Err() != nil {
		return false
	}
	release, ok := a.mediaExecution.reserve(lane)
	if !ok {
		return false
	}
	defer release()
	// A deliberately held/maintained connection must not make the generic
	// explicit step wait on the wrong lane before trying an accepted handle.
	conn, available := pool.tryAcquireLane(lane)
	if !available {
		return false
	}
	defer pool.release(conn)
	var candidate *mediaScheduleCandidate
	var err error
	if id == "" {
		candidate, err = a.selectMediaScheduleCandidate(a.ctx, conn, lane)
	} else {
		candidate, err = a.mediaScheduleCandidateByID(a.ctx, conn, lane, id)
	}
	if err != nil {
		a.taskLeaseDatabaseFailure(err)
		return false
	}
	if candidate == nil {
		return false
	}
	scopeRelease, ok := a.mediaScheduler.tryReserve(*candidate)
	if !ok {
		return false
	}
	defer scopeRelease()
	var permit *mediaModerationPermit
	if lane == mediaReviewLane {
		// A bounded wait owns no database transaction or business lock.
		permitCtx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		permit, err = a.mediaModeration.acquireReviewPermit(permitCtx, candidate.WorkspaceID)
		cancel()
		if err != nil {
			return false
		}
		defer permit.Close()
	}
	record, err := pool.claimScheduled(a.ctx, conn, *candidate)
	if err != nil {
		var op *operationError
		if errors.As(err, &op) && op.code == "MEDIA_SCHEDULE_IDENTITY_CHANGED" {
			a.parkMediaScheduleIdentity(candidate.TaskID)
			return false
		}
		a.taskLeaseDatabaseFailure(err)
		return false
	}
	if record == nil {
		return false
	}
	a.obs.recordLease("claimed", 1)
	a.mediaScheduler.claimed(lane)
	a.mediaExecution.steps[lane].Add(1)
	a.executeReservedTaskClaim(conn, record, permit)
	return true
}

func (a *App) taskLeaseDatabaseFailure(err error) {
	if err == nil || isLeaseLost(err) || a.ctx.Err() != nil {
		return
	}
	var operation *operationError
	if errors.As(err, &operation) {
		return
	}
	a.store.failed.Store(true)
	a.cancel()
}

func cloneLeasedRecord(record *taskRecord) *taskRecord {
	copy := *record
	copy.Task = copyTask(record.Task)
	copy.MediaModeration = cloneTaskMediaModeration(record.MediaModeration)
	copy.Delivery = cloneMediaDelivery(record.Delivery)
	copy.ReviewDispatch = cloneMediaReviewDispatch(record.ReviewDispatch)
	copy.Schedule = cloneMediaTaskSchedule(record.Schedule)
	if record.ScheduleClaim != nil {
		c := *record.ScheduleClaim
		copy.ScheduleClaim = &c
	}
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
	if a.tasks[record.Task.ID] == nil {
		a.evictTerminalsLocked()
	}
	a.cacheIdleTaskLocked(cached)
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
	if record == nil || record.Lease == nil {
		return
	}
	lane := mediaLaneForTask(record)
	candidate, err := a.mediaScheduleCandidateByID(a.ctx, conn, lane, record.Task.ID)
	if err != nil || candidate == nil {
		return
	}
	scopeRelease, ok := a.mediaScheduler.tryReserve(*candidate)
	if !ok {
		return
	}
	defer scopeRelease()
	record.ScheduleClaim = candidate
	record.Schedule, err = loadMediaScheduleSQL(a.ctx, conn, record.Task.ID)
	if err != nil {
		return
	}
	release, ok := a.mediaExecution.reserve(lane)
	if !ok {
		return
	}
	defer release()
	var permit *mediaModerationPermit
	if lane == mediaReviewLane {
		permit, err = a.mediaModeration.acquireReviewPermit(a.ctx, candidate.WorkspaceID)
		if err != nil {
			return
		}
		defer permit.Close()
	}
	a.mediaExecution.steps[lane].Add(1)
	a.executeReservedTaskClaim(conn, record, permit)
}

// A revoked durable lease and a finished HTTP/checker call are independent.
// Count each executing attempt, including a superseded one still returning
// from I/O. A fresh claim snapshot is never installed here: cancellation may
// already have committed between the independent claim transaction and this lock.
func (a *App) pinTaskClaim(lease TaskLease) func() {
	a.mu.Lock()
	if a.activeTaskClaims == nil {
		a.activeTaskClaims = make(map[string]int)
	}
	a.activeTaskClaims[lease.TaskID]++
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		id := lease.TaskID
		if cached := a.tasks[id]; cached != nil && cached.Lease != nil && cached.Lease.Generation == lease.Generation && cached.Lease.Epoch == lease.Epoch && cached.Lease.Owner == lease.Owner {
			cached.InFlight = false
			cached.Lease = nil
			cached.LeaseComplete = false
			cached.LeaseStatus = ""
		}
		a.activeTaskClaims[id]--
		if a.activeTaskClaims[id] == 0 {
			delete(a.activeTaskClaims, id)
			if len(a.tasks) > a.config.MaxRecords {
				delete(a.tasks, id)
			}
		}
	}
}

// Only callers holding the selected lane budget may enter this function.
func (a *App) executeReservedTaskClaim(conn *pgx.Conn, record *taskRecord, permits ...*mediaModerationPermit) {
	release := a.pinTaskClaim(*record.Lease)
	defer release()
	if record.ReviewDispatch != nil {
		var permit *mediaModerationPermit
		if len(permits) > 0 {
			permit = permits[0]
		}
		a.executeMediaReviewClaim(conn, record, permit)
		return
	}
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	// A previous owner persisted private candidates but did not commit a
	// decision. Reclaim is not permission to repeat review or query/generate.
	if record.MediaModeration != nil && len(record.MediaModeration.Candidates) > 0 {
		quarantineMediaTask(record, "execution_incomplete")
		record.Task.Status = "FAILED"
		record.Task.Error = &APIError{Code: "MEDIA_MODERATION_REVIEW", Message: "原媒体候选保持隔离，执行中断后不自动重审或生成"}
		record.Task.Version++
		record.Task.UpdatedAt = time.Now().UTC()
		a.persistTaskClaim(record, true)
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
	defer func() { stopBeat(); <-done }()
	var result MediaResult
	var err error
	handle := record.Handle
	if record.Delivery != nil {
		result, err = a.importMediaDelivery(ctx, record, func() error { return a.saveDeliveryClaim(ctx, record, false, &lost) })
	} else if handle.ID == "" {
		if prepared, ok := a.media.(interface {
			SubmitPrepared(context.Context, MediaSubmission) (MediaHandle, error)
		}); ok && record.Submission != nil {
			handle, err = prepared.SubmitPrepared(ctx, *record.Submission)
		} else {
			handle, err = a.media.Submit(ctx, record.Input)
		}
		result = MediaResult{Status: "QUEUED", RetryAfter: handle.RetryAfter, Observation: handle.Observation}
	} else if provider, ok := a.media.(MediaDeliveryProvider); ok {
		queried, queryErr := provider.QueryStatus(ctx, handle)
		result, err = queried.Result, queryErr
		if err == nil && (result.Status == "SUCCEEDED" || result.Status == "PARTIAL") {
			a.observeMediaStep(record, result, nil)
			if !validMediaDeliverySources(handle, queried) {
				err = mediaDeliveryFailure()
			} else {
				err = a.freezeMediaDelivery(ctx, record, queried, &lost)
				// Once a transaction was attempted, retain its exact evidence and
				// stop even when its acknowledgement is unknown. A seal failure
				// before any handoff instead becomes a safe parked query result.
				if err == nil || record.Delivery != nil {
					return
				}
			}
		} else if err == nil && (len(queried.Items) != 0 || len(result.Artifacts) != 0) {
			err = mediaDeliveryFailure()
		}
	} else {
		result, err = a.media.Query(ctx, handle)
	}
	if err == nil && record.Task.Moderation != nil && (result.Status == "SUCCEEDED" || result.Status == "PARTIAL") {
		a.observeMediaStep(record, result, nil)
		var proceed bool
		result, proceed = a.reviewTaskMediaResult(ctx, record, result, &lost)
		if !proceed {
			return
		}
	}
	stopBeat()
	<-done
	a.mu.Lock()
	if lost.Load() || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	applyLeasedMediaResult(record, handle, result, err, a.config.Mode)
	a.observeMediaStep(record, result, err)
	applyMediaDeliveryError(record, err)
	applyMediaScheduleExceeded(record)
	if record.Task.Moderation != nil && terminal(record.Task.Status) && record.Task.Moderation.State == "PENDING" {
		quarantineMediaTask(record, "generation_incomplete")
	}
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
