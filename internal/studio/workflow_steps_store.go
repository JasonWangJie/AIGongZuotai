package studio

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type workflowChildRequest struct {
	ExpectedVersion uint64     `json:"expected_version"`
	Run             *RunInput  `json:"run,omitempty"`
	Task            *TaskInput `json:"task,omitempty"`
}

func workflowIntentSQL(ctx context.Context, q sqlQueryRower, scope subscriptionScope, id, kind string) (WorkflowIntent, bool, error) {
	var i WorkflowIntent
	var body *workflowChildRequest
	err := q.QueryRow(ctx, `SELECT i.child_key,i.action_key,i.body FROM studio_workflow_intents i JOIN studio_workflow_executions x ON x.id=i.execution_id
	 WHERE x.workspace_id=$1 AND x.project_id=$2 AND x.id=$3 AND i.kind=$4`, scope.WorkspaceID, scope.ProjectID, id, kind).Scan(&i.Key, &i.ActionKey, &body)
	if err != nil {
		return i, false, workflowReadError(err)
	}
	if body == nil {
		return i, false, nil
	}
	i.Run, i.Task, i.ExpectedVersion = body.Run, body.Task, body.ExpectedVersion
	return i, true, nil
}
func (s *postgresStore) workflowIntent(scope subscriptionScope, id, kind string) (i WorkflowIntent, found bool, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		var e error
		i, found, e = workflowIntentSQL(ctx, c, scope, id, kind)
		return e
	})
	return
}

func workflowPublicTextSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, lock bool) (string, error) {
	var r Run
	q := "SELECT document FROM studio_runs WHERE workspace_id=$1 AND project_id=$2 AND id=$3"
	if lock {
		q += " FOR SHARE"
	}
	err := tx.QueryRow(ctx, q, scope.WorkspaceID, scope.ProjectID, id).Scan(&r)
	if err != nil {
		return "", workflowReadError(err)
	}
	if r.Status != "COMPLETED" || !runOutputReleased(r) || !validatePrompt(r.Output) {
		return "", failure(409, "WORKFLOW_TEXT_UNAVAILABLE", "原文本步骤尚无可用于图片生成的公开输出")
	}
	return r.Output, nil
}

func workflowMediaInputSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, x WorkflowExecution, n WorkflowNode, t TaskInput, lock bool) (AssetInfo, error) {
	text, err := workflowPublicTextSQL(ctx, tx, scope, x.TextRunID, lock)
	if err != nil {
		return AssetInfo{}, err
	}
	parameters, err := resolveTaskParameters(*n.Parameters, x.FrozenModels[n.Kind])
	if err != nil {
		return AssetInfo{}, err
	}
	if t.WorkflowAttemptID != x.attemptInputID() || !workflowText(t.QuoteID, 128) || t.ProjectID != x.ProjectID || t.Kind != n.Kind || t.ModelID != n.ModelID || t.ModelVersion != n.ModelVersion ||
		len(t.Inputs.ReferenceAssetIDs) != 0 || t.Parameters != parameters || t.Inputs.Prompt != workflowPrompt(n.Prompt, text) || !validatePrompt(t.Inputs.Prompt) {
		return AssetInfo{}, failure(409, "WORKFLOW_INPUT_CHANGED", "节点输入与已保存的运行版本不一致")
	}
	if n.Kind == "video" {
		if t.Operation != "first_frame" || t.Inputs.WorkflowID != x.ID || t.Inputs.FirstFrame == nil {
			return AssetInfo{}, workflowStageConflict()
		}
		return workflowFirstFrameSQL(ctx, tx, scope, x, *t.Inputs.FirstFrame, lock)
	}
	if t.Operation != "generate" || t.Inputs.WorkflowID != "" || t.Inputs.FirstFrame != nil {
		return AssetInfo{}, workflowStageConflict()
	}
	return AssetInfo{}, nil
}

func verifyWorkflowIntent(ctx context.Context, tx pgx.Tx, scope subscriptionScope, x WorkflowExecution, kind string, body workflowChildRequest) error {
	for _, n := range x.Graph.Nodes {
		if n.Kind != kind {
			continue
		}
		switch kind {
		case "text":
			r := body.Run
			if r != nil && r.WorkflowAttemptID == x.attemptInputID() && workflowText(r.QuoteID, 128) && body.Task == nil && r.ProjectID == x.ProjectID && r.ModelID == n.ModelID && r.ModelVersion == n.ModelVersion && r.Input == workflowPrompt(n.Prompt, x.Input) && validatePrompt(r.Input) {
				return nil
			}
		case "image", "video":
			if body.Task != nil && body.Run == nil {
				_, err := workflowMediaInputSQL(ctx, tx, scope, x, n, *body.Task, true)
				return err
			}
		}
	}
	return failure(409, "WORKFLOW_INPUT_CHANGED", "节点输入与已保存的运行版本不一致")
}
func (s *postgresStore) prepareWorkflowIntent(scope subscriptionScope, id string, expected uint64, kind, actionKey string, run *RunInput, task *TaskInput, actors ...*principal) (WorkflowIntent, error) {
	var out WorkflowIntent
	err := s.workflowWrite(scope, actors, func(ctx context.Context, tx pgx.Tx) error {
		if !workflowKey.MatchString(actionKey) {
			return failure(400, "INVALID_IDEMPOTENCY_KEY", "节点操作键无效")
		}
		x, err := workflowExecutionSQL(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		i, found, err := workflowIntentSQL(ctx, tx, scope, id, kind)
		if err != nil {
			return err
		}
		body := workflowChildRequest{expected, run, task}
		if found {
			if i.ActionKey != actionKey || payloadHash(workflowChildRequest{i.ExpectedVersion, i.Run, i.Task}) != payloadHash(body) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原节点请求已经绑定不同输入或操作键")
			}
			out = i
			return nil
		}
		if x.Version != expected || x.Status != workflowKindState(kind, "READY") {
			return workflowConflict()
		}
		if err = verifyWorkflowIntent(ctx, tx, scope, x, kind, body); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE studio_workflow_intents SET action_key=$3,body=$4 WHERE execution_id=$1 AND kind=$2", id, kind, actionKey, body)
		out = WorkflowIntent{Key: i.Key, ActionKey: actionKey, ExpectedVersion: expected, Run: run, Task: task}
		return err
	})
	if err != nil {
		return WorkflowIntent{}, err
	}
	return out, nil
}

func workflowChildSQL(ctx context.Context, q sqlQueryRower, scope subscriptionScope, i WorkflowIntent) (string, error) {
	var table, resourceType string
	var hash [32]byte
	switch {
	case i.Run != nil:
		table, resourceType = "studio_runs", "run"
		hash = payloadHash(*i.Run)
	case i.Task != nil:
		table, resourceType = "studio_tasks", "task"
		hash = payloadHash(*i.Task)
	default:
		return "", nil
	}
	var child string
	var stored []byte
	err := q.QueryRow(ctx, "SELECT r.id,k.payload_hash FROM studio_idempotency k JOIN "+table+" r ON r.id=k.resource_id WHERE k.scope_key=$1 AND k.resource_type=$2 AND r.workspace_id=$3 AND r.project_id=$4",
		scope.WorkspaceID+":"+resourceType+"s:"+i.Key, resourceType, scope.WorkspaceID, scope.ProjectID).Scan(&child, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !bytes.Equal(stored, hash[:]) {
		return "", workflowConflict()
	}
	return child, nil
}
func (s *postgresStore) workflowIntentChild(scope subscriptionScope, id, kind string) (child string, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		i, found, e := workflowIntentSQL(ctx, c, scope, id, kind)
		if e != nil || !found {
			return e
		}
		child, e = workflowChildSQL(ctx, c, scope, i)
		return e
	})
	return
}
func (s *postgresStore) releaseWorkflowIntent(scope subscriptionScope, id, kind string, original WorkflowIntent, actors ...*principal) error {
	return s.workflowWrite(scope, actors, func(ctx context.Context, tx pgx.Tx) error {
		x, err := workflowExecutionSQL(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		i, found, err := workflowIntentSQL(ctx, tx, scope, id, kind)
		if err != nil {
			return err
		}
		if x.Status != workflowKindState(kind, "READY") || i.Key != original.Key {
			return workflowConflict()
		}
		if !found {
			return nil
		}
		if i.ActionKey != original.ActionKey || payloadHash(workflowChildRequest{i.ExpectedVersion, i.Run, i.Task}) != payloadHash(workflowChildRequest{original.ExpectedVersion, original.Run, original.Task}) {
			return workflowConflict()
		}
		child, err := workflowChildSQL(ctx, tx, scope, i)
		if err != nil {
			return err
		}
		if child != "" {
			return workflowConflict()
		}
		_, err = tx.Exec(ctx, "UPDATE studio_workflow_intents SET body=NULL,action_key='' WHERE execution_id=$1 AND kind=$2", id, kind)
		return err
	})
}

func workflowAssetsSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, taskID, kind string, lock bool) ([]workflowAssetRecord, error) {
	var task Task
	taskQuery := "SELECT document FROM studio_tasks WHERE id=$1 AND workspace_id=$2 AND project_id=$3"
	if lock {
		taskQuery += " FOR SHARE"
	}
	if err := tx.QueryRow(ctx, taskQuery, taskID, scope.WorkspaceID, scope.ProjectID).Scan(&task); err != nil {
		return nil, workflowReadError(err)
	}
	out := []workflowAssetRecord{}
	if task.Kind != kind || (task.Status != "SUCCEEDED" && task.Status != "PARTIAL") || !taskArtifactsReleased(task) {
		return out, nil
	}
	artifacts := map[string]bool{}
	for _, a := range task.Artifacts {
		if a.Kind == kind {
			artifacts[a.ID] = true
		}
	}
	query := "SELECT " + libraryColumns + " FROM studio_library_entries WHERE workspace_id=$1 AND project_id=$2 AND task_id=$3 AND source='generated' AND kind=$4 AND state='active' ORDER BY id LIMIT 17"
	if lock {
		query += " FOR SHARE OF studio_library_entries"
	}
	rows, err := tx.Query(ctx, query, scope.WorkspaceID, scope.ProjectID, taskID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		if !artifacts[e.ArtifactID] || !libraryContentReleased(e) {
			continue
		}
		// Source identity is immutable. Local/S3 migrations may change the
		// locator while retaining identical source bytes.
		identity := e.OriginalURL
		if e.StorageInfo != nil && e.StorageInfo.SHA256 != "" {
			identity = e.StorageInfo.SHA256
		}
		c := WorkflowCandidate{AssetID: e.ID, ArtifactID: e.ArtifactID, TaskID: taskID, Revision: e.Revision, ContentVersion: contentVersion([]string{e.ID, identity})}
		out = append(out, workflowAssetRecord{c, e.StorageInfo})
		if len(out) > 16 {
			return nil, failure(422, "WORKFLOW_CANDIDATE_LIMIT", "产物超过支持上限")
		}
	}
	return out, rows.Err()
}

type workflowAssetRecord struct {
	candidate WorkflowCandidate
	info      *AssetInfo
}

func workflowCandidatesSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, taskID string, lock bool) ([]WorkflowCandidate, error) {
	records, err := workflowAssetsSQL(ctx, tx, scope, taskID, "image", lock)
	if err != nil {
		return nil, err
	}
	result := make([]WorkflowCandidate, 0, len(records))
	for _, r := range records {
		result = append(result, r.candidate)
	}
	return result, nil
}

// Keep the original handoff order and content identities. Renaming or
// restoring an asset advances its editable metadata revision independently.
func visibleWorkflowCandidates(originals, current []WorkflowCandidate) []WorkflowCandidate {
	byID := make(map[string]WorkflowCandidate, len(current))
	for _, c := range current {
		byID[c.AssetID] = c
	}
	visible := []WorkflowCandidate{}
	for _, frozen := range originals {
		c, ok := byID[frozen.AssetID]
		if ok && c.TaskID == frozen.TaskID && c.ArtifactID == frozen.ArtifactID && c.ContentVersion == frozen.ContentVersion {
			visible = append(visible, c)
		}
	}
	return visible
}
func (s *postgresStore) confirmWorkflow(scope subscriptionScope, id string, expected uint64, assetID string, revision uint64, key string, actors ...*principal) (WorkflowExecution, bool, error) {
	var out WorkflowExecution
	var replay bool
	hash := payloadHash(struct {
		ID, Asset          string
		Expected, Revision uint64
	}{id, assetID, expected, revision})
	err := s.workflowWrite(scope, actors, func(ctx context.Context, tx pgx.Tx) error {
		r, found, err := replayWorkflowReceipt(ctx, tx, scope, "confirm", key, hash)
		if err != nil {
			return err
		}
		if found {
			replay = true
			out, err = workflowExecutionSQL(ctx, tx, scope, r.id)
			return err
		}
		x, err := workflowExecutionSQL(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if x.Version != expected || x.Status != "WAITING_APPROVAL" {
			return workflowConflict()
		}
		entries, err := workflowCandidatesSQL(ctx, tx, scope, x.ImageTaskID, true)
		if err != nil {
			return err
		}
		for _, visible := range visibleWorkflowCandidates(x.Candidates, entries) {
			if visible.AssetID != assetID || visible.Revision != revision {
				continue
			}
			next := "COMPLETED"
			if x.Graph.hasVideo() {
				next = "VIDEO_READY"
			}
			if _, err = tx.Exec(ctx, "UPDATE studio_workflow_executions SET status=$3,selected=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$1", id, visible, next); err != nil {
				return err
			}
			out, err = workflowExecutionSQL(ctx, tx, scope, id)
			if err != nil {
				return err
			}
			if err = appendWorkflowOutboxSQL(ctx, tx, scope, id, out.Version, out.Status); err != nil {
				return err
			}
			return insertWorkflowReceipt(ctx, tx, scope, "confirm", key, hash, id, out.Version)
		}
		return failure(409, "WORKFLOW_CANDIDATE_CHANGED", "选中的原候选已不可见或资产版本已变化")
	})
	if err != nil {
		return WorkflowExecution{}, false, err
	}
	return out, replay, nil
}

func (s *postgresStore) workflowActionReceipt(scope subscriptionScope, id, kind, key string) (out WorkflowExecution, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if kind == "approval" {
			r, e := readWorkflowReceipt(ctx, tx, scope, "confirm", key)
			if e != nil {
				return workflowReadError(e)
			}
			if r.id != id {
				return workflowMissing()
			}
		} else {
			i, found, e := workflowIntentSQL(ctx, tx, scope, id, kind)
			if e != nil {
				return e
			}
			if !found || i.ActionKey != key {
				return workflowMissing()
			}
			child, e := workflowChildSQL(ctx, tx, scope, i)
			if e != nil {
				return e
			}
			if child == "" {
				return workflowMissing()
			}
		}
		out, e = workflowExecutionSQL(ctx, tx, scope, id)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return
}
