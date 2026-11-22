package studio

import "time"

func markMediaScheduleIdentity(t *taskRecord) bool {
	if terminal(t.Task.Status) || t.Task.Status == "RECONCILE_REQUIRED" || t.Task.Status == "SUBMIT_UNKNOWN" {
		return false
	}
	t.Task.Status = "RECONCILE_REQUIRED"
	t.Task.Error = &APIError{Code: "MEDIA_SCHEDULE_IDENTITY_CHANGED", Message: "原提交与查询渠道身份不一致，已停止自动执行待核实"}
	t.Task.CanReconcile = false
	t.Task.Version++
	t.Task.UpdatedAt = time.Now().UTC()
	if len(t.MediaModerationCandidates()) > 0 {
		quarantineMediaTask(t, "execution_incomplete")
	}
	return true
}
func appendUniqueScheduleTask(tasks []*taskRecord, t *taskRecord) []*taskRecord {
	for _, old := range tasks {
		if old.Task.ID == t.Task.ID {
			return tasks
		}
	}
	return append(tasks, t)
}

func (a *App) parkMediaScheduleIdentity(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.store.loadTask(id)
	if err != nil {
		return
	}
	if _, err = mediaFrozenChannel(t, mediaLaneForTask(t)); err == nil || !markMediaScheduleIdentity(t) {
		return
	}
	t.Lease = nil
	if err = a.store.saveTask(t); err != nil {
		return
	}
	a.cacheIdleTaskLocked(t)
	a.publishTaskLocked(t.Task)
}
