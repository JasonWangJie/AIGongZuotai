package studio

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

func mediaDeliveryFailure() error {
	return &MediaProviderError{Code: "MEDIA_DELIVERY_REVIEW", Message: "私有交付无法确认，保留原结果且不重新查询或生成", ReconcileRequired: true}
}

func (a *App) saveDeliveryClaim(ctx context.Context, t *taskRecord, complete bool, lost *atomic.Bool) error {
	for {
		a.mu.Lock()
		if ctx.Err() != nil || a.ctx.Err() != nil || lost.Load() {
			a.mu.Unlock()
			return context.Canceled
		}
		err := a.persistTaskClaim(t, complete)
		a.mu.Unlock()
		if !isEventBacklog(err) {
			return err
		}
		if !waitTaskPoll(ctx, 250*time.Millisecond) {
			return context.Canceled
		}
	}
}

func (a *App) freezeMediaDelivery(ctx context.Context, t *taskRecord, result MediaQueryResult, lost *atomic.Bool) error {
	r, err := a.delivery.seal(t, result)
	if err != nil {
		return mediaDeliveryFailure()
	}
	t.Delivery = r
	t.Task.Status = "INGESTING"
	t.Task.Progress = 99
	t.Task.Artifacts = []Artifact{}
	t.Task.CanReconcile = false
	t.Task.Error = nil
	t.Task.Version++
	t.Task.UpdatedAt = time.Now().UTC()
	t.NextPoll = time.Now()
	return a.saveDeliveryClaim(ctx, t, true, lost)
}

// checkpoint persists one complete receipt before proceeding to another item.
// On restart, a completed storage marker may be recovered if its SQL receipt
// acknowledgement was lost, but a persisted receipt can never be redownloaded.
func (a *App) importMediaDelivery(ctx context.Context, t *taskRecord, checkpoint func() error) (MediaResult, error) {
	provider, ok := a.media.(MediaDeliveryProvider)
	if !ok || t.Delivery == nil || t.Delivery.State != "READY" {
		return MediaResult{}, mediaDeliveryFailure()
	}
	items, err := a.delivery.open(t)
	if err != nil {
		return MediaResult{}, mediaDeliveryFailure()
	}
	result := MediaResult{Status: t.Delivery.ResultStatus, Progress: 100}
	if result.Status == "PARTIAL" {
		result.Error = &APIError{Code: "PARTIAL_OUTPUT", Message: "网关交付少于请求数量，已保留原完整结果集合"}
	}
	for i, item := range items {
		if ctx.Err() != nil {
			return MediaResult{}, ctx.Err()
		}
		artifact, found, err := provider.LoadDeliveryReceipt(ctx, t.Delivery.ID, item)
		if err != nil {
			return MediaResult{}, err
		}
		prior := t.Delivery.Items[i].Receipt
		if prior != nil && !found {
			return MediaResult{}, mediaDeliveryFailure()
		}
		if !found {
			if observed, ok := provider.(MediaDeliveryObservedProvider); ok {
				var observation *MediaScheduleObservation
				artifact, observation, err = observed.ImportDeliveryItemObserved(ctx, t.Delivery.ID, item)
				result.Observation = mergeMediaScheduleObservations(result.Observation, observation)
			} else {
				artifact, err = provider.ImportDeliveryItem(ctx, t.Delivery.ID, item)
			}
			if err != nil {
				return result, err
			}
		}
		info := artifact.StorageInfo
		if info == nil || artifact.ID != t.Delivery.Items[i].AssetID || artifact.URL != "/api/v1/assets/"+artifact.ID || artifact.Kind != t.Task.Kind || !validAssetInfo(*info, artifact.ID) || info.Delivery == nil || !validDeliveryMarker(info.Delivery, artifact.ID) || info.Delivery.ItemSHA256 != mediaDeliveryItemDigest(item) || info.Delivery.DeliveryID != t.Delivery.ID || info.Delivery.Slot != i {
			return MediaResult{}, mediaDeliveryFailure()
		}
		if prior != nil {
			if !sameMediaDeliveryReceipt(info, prior) {
				return MediaResult{}, mediaDeliveryFailure()
			}
		} else {
			t.Delivery = cloneMediaDelivery(t.Delivery)
			t.Delivery.Items[i].Receipt = info
			t.Task.Version++
			t.Task.UpdatedAt = time.Now().UTC()
			if err = checkpoint(); err != nil {
				return MediaResult{}, err
			}
		}
		result.Artifacts = append(result.Artifacts, mediaDeliveryArtifact(item, *t.Delivery.Items[i].Receipt))
	}
	return result, nil
}

func applyMediaDeliveryError(t *taskRecord, err error) {
	if t.Delivery == nil || err == nil {
		return
	}
	var providerError *MediaProviderError
	if errors.As(err, &providerError) && providerError.Retryable && len(t.MediaModerationCandidates()) == 0 {
		t.Task.Status = "INGESTING"
		t.Task.Error = &APIError{Code: "MEDIA_DELIVERY_TEMPORARY", Message: "原交付暂不可用，将仅继续原文件交付；不重新生成"}
	} else {
		t.Task.Status = "RECONCILE_REQUIRED"
		t.Task.Error = &APIError{Code: "MEDIA_DELIVERY_REVIEW", Message: "原交付需要核实，内容保持隔离；不会重新查询换批或生成"}
		quarantineMediaTask(t, "execution_incomplete")
	}
	t.Task.Artifacts = []Artifact{}
	t.Task.CanReconcile = false
}

func (t *taskRecord) MediaModerationCandidates() []Artifact {
	if t.MediaModeration != nil {
		return t.MediaModeration.Candidates
	}
	return nil
}

func (a *App) executeMemoryDeliveryRecord(id string, original *taskRecord, attempt string) {
	a.mu.Lock()
	if a.tasks[id] != original || terminal(original.Task.Status) || a.ctx.Err() != nil || a.mediaExecution.memoryAttempts[id] != attempt {
		a.mu.Unlock()
		return
	}
	t := cloneLeasedRecord(original)
	expected := original.Task.Version
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	checkpoint := func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.tasks[id] != original || original.Task.Version != expected || a.mediaExecution.memoryAttempts[id] != attempt || ctx.Err() != nil {
			return context.Canceled
		}
		t.Delivery.Version++
		original.Task = copyTask(t.Task)
		original.Delivery = cloneMediaDelivery(t.Delivery)
		expected = t.Task.Version
		a.publishTaskLocked(original.Task)
		return nil
	}
	result, err := a.importMediaDelivery(ctx, t, checkpoint)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tasks[id] != original || original.Task.Version != expected || a.mediaExecution.memoryAttempts[id] != attempt || a.ctx.Err() != nil {
		return
	}
	applyLeasedMediaResult(t, t.Handle, result, err, a.config.Mode)
	a.observeMediaStep(t, result, err)
	applyMediaDeliveryError(t, err)
	applyMediaScheduleExceeded(t)
	t.Task.Version++
	t.Task.UpdatedAt = time.Now().UTC()
	t.Task.CanReconcile = false
	if t.Task.Status == "SUCCEEDED" || t.Task.Status == "PARTIAL" {
		t.Delivery.State = "COMPLETE"
	} else if t.Task.Status != "INGESTING" {
		t.Delivery.State = "PARKED"
	}
	t.Delivery.Version++
	interval := max(a.config.PollInterval, 3*time.Second)
	var e *MediaProviderError
	if errors.As(err, &e) && e.RetryAfter > interval {
		interval = e.RetryAfter
	}
	t.NextPoll = time.Now().Add(interval)
	a.applyMemoryMediaSchedule(t)
	original.Task = copyTask(t.Task)
	original.Delivery = cloneMediaDelivery(t.Delivery)
	original.NextPoll = t.NextPoll
	original.Schedule = cloneMediaTaskSchedule(t.Schedule)
	a.registerMemoryTaskLibraryLocked(original.Task)
	a.publishTaskLocked(original.Task)
}
