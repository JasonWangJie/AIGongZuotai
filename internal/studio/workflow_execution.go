package studio

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
)

type workflowDetail struct {
	WorkflowExecution
	TextRun   *Run  `json:"text_run,omitempty"`
	ImageTask *Task `json:"image_task,omitempty"`
	VideoTask *Task `json:"video_task,omitempty"`
}

type workflowInput struct {
	ResourceType string     `json:"resource_type"`
	Run          *RunInput  `json:"run_input,omitempty"`
	Task         *TaskInput `json:"task_input,omitempty"`
}

type workflowActionLocks struct {
	mu     sync.Mutex
	active map[string]bool
}

// An action may wait on external input moderation. Only mutations of the same
// workflow are refused while it is in flight. Reads never wait on this lock.
func (g *workflowActionLocks) begin(id string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[id] {
		return nil, failure(409, "WORKFLOW_BUSY", "当前流程正在受理操作，请读取原收据")
	}
	if len(g.active) >= 64 {
		return nil, failure(429, "WORKFLOW_BUSY", "工作流操作繁忙，请稍后核对")
	}
	if g.active == nil {
		g.active = make(map[string]bool)
	}
	g.active[id] = true
	return func() { g.mu.Lock(); delete(g.active, id); g.mu.Unlock() }, nil
}

type workflowGuardKey struct{}
type workflowChildGuard struct {
	scope                    subscriptionScope
	id, kind, key, actionKey string
	version                  uint64
	actor                    *principal
}

func workflowChildFrom(ctx context.Context) *workflowChildGuard {
	g, _ := ctx.Value(workflowGuardKey{}).(*workflowChildGuard)
	return g
}

// Recheck authority inside the normal child admission transaction, after
// lock-free input moderation and before any child, credit or outbox is written.
func (g *workflowChildGuard) check(ctx context.Context, tx pgx.Tx, workspace, project, kind string, hash [32]byte) error {
	if g == nil {
		return nil
	}
	if g.scope.WorkspaceID != workspace || g.scope.ProjectID != project || g.kind != kind {
		return workflowStageConflict()
	}
	if err := workflowAuthorizeSQL(ctx, tx, g.scope, g.actor); err != nil {
		return err
	}
	run, err := workflowExecutionSQL(ctx, tx, g.scope, g.id)
	if err != nil {
		return err
	}
	if run.Version != g.version || run.Status != workflowReadyStage(kind) {
		return workflowStageConflict()
	}
	intent, found, err := workflowIntentSQL(ctx, tx, g.scope, g.id, kind)
	if err != nil {
		return err
	}
	if !found || intent.Key != g.key || intent.ActionKey != g.actionKey {
		return workflowStageConflict()
	}
	if kind == "text" {
		if intent.Run == nil || payloadHash(*intent.Run) != hash {
			return workflowStageConflict()
		}
	} else if intent.Task == nil || payloadHash(*intent.Task) != hash {
		return workflowStageConflict()
	}
	if kind == "video" {
		if intent.Task == nil || intent.Task.Operation != "first_frame" || intent.Task.Inputs.WorkflowID != g.id || intent.Task.Inputs.FirstFrame == nil {
			return workflowStageConflict()
		}
		_, err = workflowFirstFrameSQL(ctx, tx, g.scope, run, *intent.Task.Inputs.FirstFrame, true)
		return err
	}
	return nil
}

func workflowStageConflict() error {
	return failure(409, "WORKFLOW_STATE_CHANGED", "流程状态或节点版本已变化，请重新读取")
}

func workflowReadyStage(kind string) string {
	switch kind {
	case "text":
		return "TEXT_READY"
	case "image":
		return "IMAGE_READY"
	case "video":
		return "VIDEO_READY"
	case "approval":
		return "WAITING_APPROVAL"
	}
	return ""
}

func workflowNodeFor(g WorkflowGraph, id string) (WorkflowNode, error) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, nil
		}
	}
	return WorkflowNode{}, failure(404, "WORKFLOW_NODE_NOT_FOUND", "流程内没有此节点")
}

func workflowPrompt(instruction, input string) string {
	if instruction == "" {
		return input
	}
	if input == "" {
		return instruction
	}
	return instruction + "\n\n" + input
}

// Read the execution, its public children and its selectable assets at one
// committed point. Admission and terminal transitions write their parent in
// the same transaction. Reconnecting readers never advance the workflow.
func (a *App) workflowDetail(scope subscriptionScope, saved WorkflowExecution) (workflowDetail, error) {
	var d workflowDetail
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, err := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		d.WorkflowExecution, err = workflowExecutionSQL(ctx, tx, scope, saved.ID)
		if err != nil {
			return err
		}
		if d.TextRunID != "" {
			var r Run
			err = tx.QueryRow(ctx, "SELECT document FROM studio_runs WHERE id=$1 AND workspace_id=$2 AND project_id=$3", d.TextRunID, scope.WorkspaceID, scope.ProjectID).Scan(&r)
			if err != nil {
				return workflowReadError(err)
			}
			if _, err = loadRunStreamPrefixSQL(ctx, tx, &r); err != nil {
				return err
			}
			public := copyRun(r)
			d.TextRun = &public
		}
		if d.ImageTaskID != "" {
			var task Task
			err = tx.QueryRow(ctx, "SELECT document FROM studio_tasks WHERE id=$1 AND workspace_id=$2 AND project_id=$3", d.ImageTaskID, scope.WorkspaceID, scope.ProjectID).Scan(&task)
			if err != nil {
				return workflowReadError(err)
			}
			public := copyTask(task)
			d.ImageTask = &public
			if len(d.Candidates) > 0 {
				current, err := workflowCandidatesSQL(ctx, tx, scope, task.ID, false)
				if err != nil {
					return err
				}
				d.Candidates = visibleWorkflowCandidates(d.Candidates, current)
			}
		}
		if d.VideoTaskID != "" {
			var task Task
			err = tx.QueryRow(ctx, "SELECT document FROM studio_tasks WHERE id=$1 AND workspace_id=$2 AND project_id=$3", d.VideoTaskID, scope.WorkspaceID, scope.ProjectID).Scan(&task)
			if err != nil {
				return workflowReadError(err)
			}
			public := copyTask(task)
			d.VideoTask = &public
			if len(d.VideoArtifacts) > 0 {
				current, e := workflowVideoArtifactsSQL(ctx, tx, scope, task.ID, false)
				if e != nil {
					return e
				}
				d.VideoArtifacts = visibleWorkflowCandidates(d.VideoArtifacts, current)
			}
		}
		return tx.Commit(ctx)
	})
	return d, err
}

func (a *App) workflowNodeInput(d workflowDetail, node WorkflowNode) (workflowInput, error) {
	var result workflowInput
	if node.Kind == "approval" || d.Status != workflowReadyStage(node.Kind) {
		return result, workflowStageConflict()
	}
	model, ok := d.FrozenModels[node.Kind]
	if !ok || model.ID != node.ModelID || model.Version != node.ModelVersion || d.ProviderMode != a.config.Mode {
		return result, failure(409, "WORKFLOW_MODEL_CHANGED", "原流程冻结的模型版本或渠道模式不可用")
	}
	base, kind := d.Input, "chat"
	if node.Kind == "image" || node.Kind == "video" {
		if d.TextRun == nil || d.TextRun.Status != "COMPLETED" || !runOutputReleased(*d.TextRun) {
			return result, workflowStageConflict()
		}
		base, kind = d.TextRun.Output, node.Kind
	}
	prompt := workflowPrompt(node.Prompt, base)
	if _, err := a.resolveModel(model.ID, kind, model.Version, prompt); err != nil {
		return result, err
	}
	if node.Kind == "text" {
		result.ResourceType = "run"
		result.Run = &RunInput{WorkflowAttemptID: d.attemptInputID(), ProjectID: d.ProjectID, ModelID: model.ID, ModelVersion: model.Version, Input: prompt}
		return result, nil
	}
	if node.Parameters == nil {
		return result, workflowStageConflict()
	}
	parameters, err := resolveTaskParameters(*node.Parameters, model)
	if err != nil {
		return result, err
	}
	result.ResourceType = "task"
	result.Task = &TaskInput{WorkflowAttemptID: d.attemptInputID(), ProjectID: d.ProjectID, Kind: "image", ModelID: model.ID, ModelVersion: model.Version, Operation: "generate", Parameters: parameters}
	result.Task.Inputs.Prompt = prompt
	if node.Kind == "video" {
		if !d.Graph.hasVideo() || d.Selected == nil || !model.Capabilities.VideoFirstFrameSupported {
			return result, workflowFirstFrameUnavailable()
		}
		source := *d.Selected
		if len(visibleWorkflowCandidates([]WorkflowCandidate{source}, d.Candidates)) == 0 {
			return result, workflowFirstFrameUnavailable()
		}
		result.Task.Kind, result.Task.Operation = "video", "first_frame"
		result.Task.Inputs.FirstFrame = &source
		result.Task.Inputs.WorkflowID = d.ID
	}
	return result, nil
}

func (a *App) executeWorkflowNode(ctx context.Context, scope subscriptionScope, saved WorkflowExecution, node WorkflowNode, version uint64, quote, key string, actor *principal) (workflowDetail, bool, error) {
	empty := workflowDetail{}
	if node.Kind != "text" && node.Kind != "image" && node.Kind != "video" {
		return empty, false, workflowStageConflict()
	}
	intent, found, err := a.store.workflowIntent(scope, saved.ID, node.Kind)
	if err != nil {
		return empty, false, err
	}
	if found && intent.ActionKey == key {
		if intent.ExpectedVersion != version || intent.Run != nil && intent.Run.QuoteID != quote || intent.Task != nil && intent.Task.QuoteID != quote {
			return empty, false, failure(409, "IDEMPOTENCY_CONFLICT", "原操作键对应其他报价或运行版本")
		}
		if childID, err := a.store.workflowIntentChild(scope, saved.ID, node.Kind); err != nil {
			return empty, false, err
		} else if childID != "" {
			detail, err := a.workflowDetail(scope, saved)
			return detail, true, err
		}
	}
	d, err := a.workflowDetail(scope, saved)
	if err != nil {
		return empty, false, err
	}
	saved = d.WorkflowExecution
	if saved.Version != version || d.Status != workflowReadyStage(node.Kind) {
		return empty, false, workflowStageConflict()
	}
	input, err := a.workflowNodeInput(d, node)
	if err != nil {
		return empty, false, err
	}
	if err = a.validateBillingAdmission(quote, node.Kind == "text"); err != nil {
		return empty, false, err
	}
	if input.Run != nil {
		input.Run.QuoteID = quote
	} else {
		input.Task.QuoteID = quote
	}
	// A different explicit operation can replace a pre-admission intent only
	// after the database proves there is still no admitted child for its key.
	if found && intent.ActionKey != key {
		if err = a.store.releaseWorkflowIntent(scope, saved.ID, node.Kind, intent, actor); err != nil {
			return empty, false, err
		}
	}
	intent, err = a.store.prepareWorkflowIntent(scope, saved.ID, saved.Version, node.Kind, key, input.Run, input.Task, actor)
	if err != nil {
		return empty, false, err
	}
	guard := workflowChildGuard{scope: scope, id: saved.ID, kind: node.Kind, key: intent.Key, actionKey: key, version: saved.Version, actor: actor}
	ctx = context.WithValue(ctx, workflowGuardKey{}, &guard)
	if intent.Run != nil {
		_, _, err = a.createRunContext(ctx, *intent.Run, intent.Key, actor.User.ID)
	} else if intent.Task != nil {
		_, _, err = a.createTaskContext(ctx, *intent.Task, intent.Key, actor.User.ID)
	} else {
		return empty, false, workflowStageConflict()
	}
	if err != nil {
		var op *operationError
		if errors.As(err, &op) && op.status < 500 {
			if clearErr := a.store.releaseWorkflowIntent(scope, saved.ID, node.Kind, intent, actor); clearErr != nil {
				return empty, false, clearErr
			}
		}
		return empty, false, err
	}
	d, err = a.workflowDetail(scope, saved)
	return d, false, err
}

func (a *App) confirmWorkflowNode(scope subscriptionScope, saved WorkflowExecution, node WorkflowNode, version uint64, asset string, revision uint64, key string, actor *principal) (workflowDetail, bool, error) {
	if node.Kind != "approval" {
		return workflowDetail{}, false, workflowStageConflict()
	}
	saved, replayed, err := a.store.confirmWorkflow(scope, saved.ID, version, asset, revision, key, actor)
	if err != nil {
		return workflowDetail{}, false, err
	}
	d, err := a.workflowDetail(scope, saved)
	return d, replayed, err
}
