package studio

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) registerWorkflowCancelRoutes(mux *http.ServeMux) {
	const path = "/api/v1/projects/{project}/workflow-runs/{workflow}/cancel"
	mux.HandleFunc("POST "+path, a.cancelWorkflowHTTP)
	mux.HandleFunc("GET "+path+"/receipt", a.workflowCancelReceiptHTTP)
}

func (a *App) cancelWorkflowHTTP(w http.ResponseWriter, r *http.Request) {
	scope, x, err := a.workflowRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input struct {
		Version uint64 `json:"expected_version"`
	}
	if err = decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	replayed, err := a.cancelWorkflow(scope, x, input.Version, key, principalFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := a.workflowDetail(scope, x)
	if err != nil {
		writeError(w, err)
		return
	}
	workflowResponse(w, 200, d, replayed)
}

func (a *App) workflowCancelReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	scope, x, err := a.workflowRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		receipt, e := readWorkflowReceipt(ctx, c, scope, "cancel", key)
		if e != nil {
			return workflowReadError(e)
		}
		if receipt.id != x.ID {
			return workflowMissing()
		}
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := a.workflowDetail(scope, x)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func workflowCancellable(s string) bool {
	switch s {
	case "TEXT_READY", "TEXT_ACTIVE", "IMAGE_READY", "IMAGE_ACTIVE", "WAITING_APPROVAL", "VIDEO_READY", "VIDEO_ACTIVE", "REVIEW":
		return true
	}
	return false
}

// Do not wait on the node admission lock. A user can cancel while the original
// action is waiting for input moderation; its eventual child guard will fail.
func (a *App) cancelWorkflow(scope subscriptionScope, original WorkflowExecution, version uint64, key string, actor *principal) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if hot := a.runs[original.TextRunID]; original.Status == "TEXT_ACTIVE" && hot != nil {
		a.waitRunStreamIdleLocked(hot)
	}
	hash := payloadHash(struct {
		ID      string
		Version uint64
	}{original.ID, version})
	var replayed bool
	var cancelledRun *Run
	var changedTask *taskRecord
	var childAudit AuditEvent
	err := a.store.workflowWrite(scope, []*principal{actor}, func(ctx context.Context, tx pgx.Tx) error {
		_, found, err := replayWorkflowReceipt(ctx, tx, scope, "cancel", key, hash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			return nil
		}
		x, err := workflowExecutionSQL(ctx, tx, scope, original.ID)
		if err != nil {
			return err
		}
		if x.Version != version || !workflowCancellable(x.Status) {
			return workflowStageConflict()
		}
		// Child writers lock the resource before updating their workflow. Take
		// the same locks before fencing the parent to avoid a cross-writer cycle.
		if x.Status == "TEXT_ACTIVE" && x.TextRunID != "" {
			if err = a.store.lockRunWriterSQL(ctx, tx, x.TextRunID); err != nil {
				return err
			}
			if _, err = getRunStreamHead(ctx, tx, x.TextRunID, true); err != nil {
				return err
			}
			run, err := readRunDocumentSQL(ctx, tx, x.TextRunID)
			if err != nil {
				return err
			}
			if run.ProjectID != scope.ProjectID || run.WorkspaceID != scope.WorkspaceID {
				return workflowMissing()
			}
			if !terminal(run.Status) {
				childAudit = newAuditEvent(scope.WorkspaceID, scope.ProjectID, actor.User.ID, "run.cancelled", "run", run.ID, AuditDetails{FromStatus: run.Status, ToStatus: "CANCELLED"})
				run.Status, run.UpdatedAt = "CANCELLED", time.Now().UTC()
				cancelledRun = &run
			}
		}
		if x.Status == "IMAGE_ACTIVE" || x.Status == "VIDEO_ACTIVE" {
			id := x.ImageTaskID
			if x.Status == "VIDEO_ACTIVE" {
				id = x.VideoTaskID
			}
			task := &taskRecord{}
			if err = readTaskRecordSQL(ctx, tx, task, "SELECT document,input,handle,submission,next_poll FROM studio_tasks WHERE id=$1 AND workspace_id=$2 AND project_id=$3 FOR UPDATE", id, scope.WorkspaceID, scope.ProjectID); err != nil {
				return workflowReadError(err)
			}
			from := task.Task.Status
			if stopWorkflowMedia(task) {
				event := "task.cancelled"
				if task.Task.Status == "SUBMIT_UNKNOWN" {
					event = "task.submit_unknown"
				}
				childAudit = newAuditEvent(scope.WorkspaceID, scope.ProjectID, actor.User.ID, event, "task", id, AuditDetails{FromStatus: from, ToStatus: task.Task.Status, Kind: task.Task.Kind})
				changedTask = task
			}
		}
		tag, err := tx.Exec(ctx, "UPDATE studio_workflow_executions SET status='CANCELLED',version=version+1,updated_at=clock_timestamp() WHERE id=$1 AND workspace_id=$2 AND project_id=$3 AND version=$4 AND status=$5", x.ID, scope.WorkspaceID, scope.ProjectID, version, x.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return workflowStageConflict()
		}
		if cancelledRun != nil {
			result, err := a.store.stoppedRunSQL(ctx, tx, cancelledRun, "cancelled", nil, []AuditEvent{childAudit})
			if err != nil {
				return err
			}
			cancelledRun = &result
		}
		if changedTask != nil {
			if err = saveBilledTaskSQL(ctx, tx, changedTask); err != nil {
				return err
			}
			if err = insertAuditSQL(ctx, tx, childAudit); err != nil {
				return err
			}
			if err = appendTaskOutboxSQL(ctx, tx, changedTask.Task); err != nil {
				return err
			}
		}
		if err = insertWorkflowReceipt(ctx, tx, scope, "cancel", key, hash, x.ID, version+1); err != nil {
			return err
		}
		return appendWorkflowOutboxSQL(ctx, tx, scope, x.ID, version+1, "CANCELLED")
	})
	if err != nil {
		return false, err
	}
	if cancelledRun != nil {
		if hot := a.runs[cancelledRun.ID]; hot != nil {
			hot.Run = *cancelledRun
			hot.CheckpointSeq = cancelledRun.Seq
			a.publishFinishedRunLocked(hot)
			a.execution.cancelLocked(cancelledRun.ID)
			if hot.Cancel != nil {
				hot.Cancel()
			}
			a.releaseRunCacheLocked(hot)
		}
		a.appendMemoryAuditLocked(childAudit)
	}
	if changedTask != nil {
		a.cacheIdleTaskLocked(changedTask)
		a.appendMemoryAuditLocked(childAudit)
		a.publishTaskLocked(changedTask.Task)
	}
	return replayed, nil
}

// A queued submission has not crossed the dispatch fence. Once SUBMITTING was
// persisted without a handle, the provider may already have accepted it. A known
// handle or registered delivery continues under its original task and may settle.
func stopWorkflowMedia(t *taskRecord) bool {
	if t.Handle.ID != "" || t.Delivery != nil || t.ReviewDispatch != nil {
		return false
	}
	switch t.Task.Status {
	case "QUEUED":
		t.Task.Status = "CANCELLED"
		quarantineMediaTask(t, "cancelled")
	case "SUBMITTING":
		t.Task.Status = "SUBMIT_UNKNOWN"
		t.Task.Error = &APIError{Code: "SUBMISSION_UNKNOWN", Message: "停止流程时上游受理尚未确认，请保留原任务核对"}
		quarantineMediaTask(t, "execution_incomplete")
	default:
		return false
	}
	t.Task.CanReconcile = false
	t.Task.Version++
	t.Task.UpdatedAt = time.Now().UTC()
	return true
}
