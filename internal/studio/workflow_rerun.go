package studio

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type workflowRerunInput struct {
	ExpectedVersion uint64  `json:"expected_version"`
	FromNode        string  `json:"from_node"`
	Prompt          *string `json:"prompt,omitempty"`
	Input           *string `json:"input,omitempty"`
}

type workflowRerunPreview struct {
	SourceWorkflowID string   `json:"source_workflow_id"`
	SourceVersion    uint64   `json:"source_version"`
	FromNode         string   `json:"from_node"`
	AffectedNodes    []string `json:"affected_nodes"`
	InheritedNodes   []string `json:"inherited_nodes"`
}

func workflowUnresolved() error {
	return failure(409, "WORKFLOW_SOURCE_UNRESOLVED", "原运行或已受理的生成仍待核对，无法开始新尝试")
}

// A workflow may have been stopped while the original provider call was still
// in flight. Check the child records as well as the visible parent status.
func workflowRerunSafeSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, x WorkflowExecution, lock bool) error {
	if x.Status != "COMPLETED" && x.Status != "FAILED" && x.Status != "CANCELLED" {
		return workflowUnresolved()
	}
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	if x.TextRunID != "" {
		var started bool
		// Match the stream-head-before-Run order used by the fenced writer.
		if err := tx.QueryRow(ctx, "SELECT execution_id<>'' FROM studio_run_streams WHERE run_id=$1"+suffix, x.TextRunID).Scan(&started); err != nil {
			return workflowReadError(err)
		}
		var state, code string
		if err := tx.QueryRow(ctx, `SELECT document->>'status',COALESCE(document->'error'->>'code','') FROM studio_runs WHERE id=$1 AND workspace_id=$2 AND project_id=$3`+suffix, x.TextRunID, scope.WorkspaceID, scope.ProjectID).Scan(&state, &code); err != nil {
			return workflowReadError(err)
		}
		// An interrupted/failed transport cannot prove that the provider
		// stopped. A completed generation subsequently blocked by policy can
		// only be attempted anew from text and remains subject to moderation.
		if state != "COMPLETED" && !(state == "CANCELLED" && !started) && !(state == "FAILED" && (!started || code == "MODERATION_OUTPUT_BLOCKED")) {
			return workflowUnresolved()
		}
	}
	for _, id := range []string{x.ImageTaskID, x.VideoTaskID} {
		if id == "" {
			continue
		}
		if err := mediaTaskRedoSafeSQL(ctx, tx, scope, id, lock); err != nil {
			return err
		}
	}
	return nil
}

func mediaTaskRedoSafeSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var state string
	var hasHandle bool
	err := tx.QueryRow(ctx, `SELECT document->>'status',COALESCE(handle->>'ID',handle->>'id','')<>'' FROM studio_tasks WHERE id=$1 AND workspace_id=$2 AND project_id=$3`+suffix, id, scope.WorkspaceID, scope.ProjectID).Scan(&state, &hasHandle)
	if err != nil {
		return workflowReadError(err)
	}
	switch state {
	case "SUCCEEDED", "PARTIAL", "FAILED":
	case "CANCELLED":
		if hasHandle {
			return workflowUnresolved()
		}
		var queued bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_audit_events WHERE workspace_id=$1 AND project_id=$2 AND resource_id=$3 AND action='task.cancelled' AND details->>'from_status'='QUEUED')`, scope.WorkspaceID, scope.ProjectID, id).Scan(&queued); err != nil {
			return err
		}
		if !queued {
			return workflowUnresolved()
		}
	default:
		return workflowUnresolved()
	}
	return nil
}

func workflowAffected(g WorkflowGraph, node string) (workflowRerunPreview, WorkflowNode, error) {
	var result workflowRerunPreview
	n, err := workflowNodeFor(g, node)
	if err != nil {
		return result, n, err
	}
	result.FromNode = node
	result.AffectedNodes = []string{}
	result.InheritedNodes = []string{}
	affected := false
	for _, kind := range []string{"text", "image", "approval", "video"} {
		for _, v := range g.Nodes {
			if v.Kind != kind {
				continue
			}
			if v.ID == node {
				affected = true
			}
			if affected {
				result.AffectedNodes = append(result.AffectedNodes, v.ID)
			} else {
				result.InheritedNodes = append(result.InheritedNodes, v.ID)
			}
		}
	}
	return result, n, nil
}

// Build only the state a new attempt will own or inherit. Reads, including
// preview, do not insert a child or generate an upstream request.
func workflowForkSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, source WorkflowExecution, in workflowRerunInput, lock bool) (WorkflowExecution, workflowRerunPreview, error) {
	var next WorkflowExecution
	p, n, err := workflowAffected(source.Graph, in.FromNode)
	if err != nil {
		return next, p, err
	}
	p.SourceWorkflowID, p.SourceVersion = source.ID, source.Version
	if err = workflowRerunSafeSQL(ctx, tx, scope, source, lock); err != nil {
		return next, p, err
	}
	if (in.Input != nil && (n.Kind != "text" || !validatePrompt(*in.Input))) || (in.Prompt != nil && (n.Kind == "approval" || !workflowText(*in.Prompt, 4096))) {
		return next, p, failure(422, "INVALID_WORKFLOW_RERUN", "运行输入只可在文本起点修改，生成指令须属于起点并符合字节上限")
	}
	next = WorkflowExecution{DefinitionID: source.DefinitionID, DefinitionVersion: source.DefinitionVersion, Graph: source.Graph, Input: source.Input, FrozenModels: source.FrozenModels, ProviderMode: source.ProviderMode, SourceWorkflowID: source.ID, Attempt: source.Attempt + 1, FromNode: n.ID, Status: workflowReadyStage(n.Kind), Candidates: []WorkflowCandidate{}}
	if in.Input != nil {
		next.Input = *in.Input
	}
	if in.Prompt != nil {
		next.Graph.Nodes = append([]WorkflowNode(nil), next.Graph.Nodes...)
		for i := range next.Graph.Nodes {
			if next.Graph.Nodes[i].ID == n.ID {
				next.Graph.Nodes[i].Prompt = *in.Prompt
			}
		}
		n.Prompt = *in.Prompt
		if err = validateWorkflowGraph(next.Graph); err != nil {
			return next, p, err
		}
	}
	base := next.Input
	if n.Kind != "text" {
		if source.TextRunID == "" {
			return next, p, failure(409, "WORKFLOW_TEXT_UNAVAILABLE", "原运行没有可继承的已公开脚本")
		}
		base, err = workflowPublicTextSQL(ctx, tx, scope, source.TextRunID, lock)
		if err != nil {
			return next, p, err
		}
		next.TextRunID = source.TextRunID
	}
	if lock && n.Kind != "approval" {
		prompt := workflowPrompt(n.Prompt, base)
		if !validatePrompt(prompt) || len(prompt) > source.FrozenModels[n.Kind].Capabilities.MaxPromptBytes {
			return next, p, failure(422, "INVALID_MODEL_PARAMETERS", "修改后提示词超过原模型预算")
		}
	}
	if n.Kind == "approval" || n.Kind == "video" {
		if source.ImageTaskID == "" {
			return next, p, failure(409, "WORKFLOW_CANDIDATE_CHANGED", "原运行没有可继承的候选图片")
		}
		current, e := workflowCandidatesSQL(ctx, tx, scope, source.ImageTaskID, lock)
		if e != nil {
			return next, p, e
		}
		if len(visibleWorkflowCandidates(source.Candidates, current)) == 0 {
			return next, p, failure(409, "WORKFLOW_CANDIDATE_CHANGED", "原候选图片已无可用版本")
		}
		next.ImageTaskID, next.Candidates = source.ImageTaskID, source.Candidates
	}
	if n.Kind == "video" {
		if source.Selected == nil || source.Selected.TaskID != source.ImageTaskID || len(visibleWorkflowCandidates([]WorkflowCandidate{*source.Selected}, source.Candidates)) != 1 {
			return next, p, workflowFirstFrameUnavailable()
		}
		if _, err = workflowSelectedAssetSQL(ctx, tx, scope, *source.Selected, lock); err != nil {
			return next, p, err
		}
		next.Selected = source.Selected
	}
	return next, p, nil
}

func (s *postgresStore) workflowRerunPreview(scope subscriptionScope, id, node string) (result workflowRerunPreview, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginTxFunc(ctx, c, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			source, e := workflowExecutionSQL(ctx, tx, scope, id)
			if e != nil {
				return e
			}
			_, result, e = workflowForkSQL(ctx, tx, scope, source, workflowRerunInput{FromNode: node}, false)
			return e
		})
	})
	return
}

func (s *postgresStore) rerunWorkflow(scope subscriptionScope, id, key string, in workflowRerunInput, actors ...*principal) (WorkflowExecution, bool, error) {
	var result WorkflowExecution
	var replay bool
	hash := payloadHash(struct {
		ID    string
		Input workflowRerunInput
	}{id, in})
	err := s.workflowWrite(scope, actors, func(ctx context.Context, tx pgx.Tx) error {
		r, found, err := replayWorkflowReceipt(ctx, tx, scope, "rerun", key, hash)
		if err != nil {
			return err
		}
		if found {
			result, err = workflowExecutionSQL(ctx, tx, scope, r.id)
			replay = true
			return err
		}
		source, err := workflowExecutionSQL(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if source.Version != in.ExpectedVersion {
			return workflowConflict()
		}
		next, p, err := workflowForkSQL(ctx, tx, scope, source, in, true)
		if err != nil {
			return err
		}
		if err = checkWorkspaceGenerationSQL(ctx, tx, scope.WorkspaceID); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM studio_workflow_executions WHERE workspace_id=$1 AND project_id=$2", scope.WorkspaceID, scope.ProjectID).Scan(&count); err != nil {
			return err
		}
		if count >= workflowProjectExecutions || next.Attempt > 128 {
			return failure(429, "WORKFLOW_EXECUTION_LIMIT", "项目工作流运行已达上限")
		}
		next.ID = newID("wfr")
		var selected any
		if next.Selected != nil {
			selected = *next.Selected
		}
		_, err = tx.Exec(ctx, `INSERT INTO studio_workflow_executions(id,workspace_id,project_id,definition_id,definition_version,input,models,provider_mode,status,source_workflow_id,attempt,from_node,graph_override,inherited_text_run_id,inherited_image_task_id,candidates,selected,source_version)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),NULLIF($15,''),$16,$17,$18)`,
			next.ID, scope.WorkspaceID, scope.ProjectID, next.DefinitionID, next.DefinitionVersion, next.Input, next.FrozenModels, next.ProviderMode, next.Status, id, next.Attempt, next.FromNode, next.Graph, next.TextRunID, next.ImageTaskID, next.Candidates, selected, source.Version)
		if err != nil {
			return err
		}
		for _, nodeID := range p.AffectedNodes {
			n, _ := workflowNodeFor(next.Graph, nodeID)
			if n.Kind == "approval" {
				continue
			}
			if _, err = tx.Exec(ctx, "INSERT INTO studio_workflow_intents(execution_id,kind,child_key) VALUES($1,$2,$3)", next.ID, n.Kind, newID("wfstep")); err != nil {
				return err
			}
		}
		result, err = workflowExecutionSQL(ctx, tx, scope, next.ID)
		if err != nil {
			return err
		}
		if err = appendWorkflowOutboxSQL(ctx, tx, scope, result.ID, result.Version, result.Status); err != nil {
			return err
		}
		return insertWorkflowReceipt(ctx, tx, scope, "rerun", key, hash, next.ID, result.Version)
	})
	if err != nil {
		return WorkflowExecution{}, false, err
	}
	return result, replay, nil
}

func (s *postgresStore) workflowRerunReceipt(scope subscriptionScope, id, key string) (result WorkflowExecution, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		r, e := readWorkflowReceipt(ctx, c, scope, "rerun", key)
		if e != nil {
			return workflowReadError(e)
		}
		result, e = workflowExecutionSQL(ctx, c, scope, r.id)
		if e == nil && result.SourceWorkflowID != id {
			return workflowMissing()
		}
		return e
	})
	return
}
