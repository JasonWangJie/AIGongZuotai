package studio

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

const workflowProjectDefinitions = 64
const workflowProjectExecutions = 128

func workflowAuthorizeSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, actors ...*principal) error {
	if len(actors) == 0 && scope.WorkspaceID != "ws_demo" {
		return failure(401, "AUTH_REQUIRED", "请先登录")
	}
	if len(actors) > 0 {
		p := actors[0]
		if p == nil || p.Workspace.ID != scope.WorkspaceID || (p.User == nil && scope.WorkspaceID != "ws_demo") {
			return workflowMissing()
		}
		if p.User != nil {
			if _, err := operationsSessionSQL(ctx, tx, p); err != nil {
				return err
			}
		}
	}
	var id string
	err := tx.QueryRow(ctx, "SELECT id FROM studio_projects WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE", scope.WorkspaceID, scope.ProjectID).Scan(&id)
	return workflowReadError(err)
}

func workflowReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return workflowMissing()
	}
	return err
}

func (s *postgresStore) workflowWrite(scope subscriptionScope, actors []*principal, fn func(context.Context, pgx.Tx) error) error {
	return s.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = workflowAuthorizeSQL(ctx, tx, scope, actors...); err != nil {
			return err
		}
		if err = fn(ctx, tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

type workflowReceipt struct {
	id      string
	version uint64
	hash    []byte
}

func readWorkflowReceipt(ctx context.Context, q sqlQueryRower, scope subscriptionScope, operation, key string) (workflowReceipt, error) {
	var r workflowReceipt
	err := q.QueryRow(ctx, `SELECT resource_id,version,request_hash FROM studio_workflow_receipts WHERE workspace_id=$1 AND project_id=$2 AND operation=$3 AND operation_key=$4`, scope.WorkspaceID, scope.ProjectID, operation, key).Scan(&r.id, &r.version, &r.hash)
	return r, err
}
func replayWorkflowReceipt(ctx context.Context, q sqlQueryRower, scope subscriptionScope, operation, key string, hash [32]byte) (workflowReceipt, bool, error) {
	if !workflowKey.MatchString(key) {
		return workflowReceipt{}, false, failure(400, "INVALID_IDEMPOTENCY_KEY", "操作键必须为 8 至 128 个有效字符")
	}
	r, err := readWorkflowReceipt(ctx, q, scope, operation, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if !bytes.Equal(r.hash, hash[:]) {
		return r, false, failure(409, "IDEMPOTENCY_CONFLICT", "该操作键已用于不同的流程请求")
	}
	return r, true, nil
}
func insertWorkflowReceipt(ctx context.Context, tx pgx.Tx, scope subscriptionScope, operation, key string, hash [32]byte, id string, version uint64) error {
	_, err := tx.Exec(ctx, `INSERT INTO studio_workflow_receipts(workspace_id,project_id,operation,operation_key,request_hash,resource_id,version) VALUES($1,$2,$3,$4,$5,$6,$7)`, scope.WorkspaceID, scope.ProjectID, operation, key, hash[:], id, version)
	return err
}

const workflowDefinitionSelect = `SELECT d.id,d.project_id,v.version,v.graph,v.created_at FROM studio_workflow_definitions d
 JOIN studio_workflow_versions v ON v.definition_id=d.id WHERE d.workspace_id=$1 AND d.project_id=$2`

func scanWorkflowDefinition(row libraryScanner) (WorkflowDefinition, error) {
	var d WorkflowDefinition
	err := row.Scan(&d.ID, &d.ProjectID, &d.Version, &d.Graph, &d.CreatedAt)
	return d, workflowReadError(err)
}
func workflowDefinitionSQL(ctx context.Context, q sqlQueryRower, scope subscriptionScope, id string, version uint64) (WorkflowDefinition, error) {
	if version > 64 {
		return WorkflowDefinition{}, workflowMissing()
	}
	return scanWorkflowDefinition(q.QueryRow(ctx, workflowDefinitionSelect+" AND d.id=$3 AND v.version=COALESCE(NULLIF($4::integer,0),d.version)", scope.WorkspaceID, scope.ProjectID, id, version))
}

func (s *postgresStore) saveWorkflowDefinition(scope subscriptionScope, id string, expected uint64, g WorkflowGraph, key string, actors ...*principal) (WorkflowDefinition, bool, error) {
	var d WorkflowDefinition
	var replay bool
	hash := payloadHash(struct {
		ID       string
		Expected uint64
		Graph    WorkflowGraph
	}{id, expected, g})
	err := s.workflowWrite(scope, actors, func(ctx context.Context, tx pgx.Tx) error {
		r, found, err := replayWorkflowReceipt(ctx, tx, scope, "save", key, hash)
		if err != nil {
			return err
		}
		if found {
			replay = true
			d, err = workflowDefinitionSQL(ctx, tx, scope, r.id, r.version)
			return err
		}
		if err = validateWorkflowGraph(g); err != nil {
			return err
		}
		if id == "" {
			if expected != 0 {
				return workflowConflict()
			}
			var n int
			if err = tx.QueryRow(ctx, "SELECT count(*) FROM studio_workflow_definitions WHERE workspace_id=$1 AND project_id=$2", scope.WorkspaceID, scope.ProjectID).Scan(&n); err != nil {
				return err
			}
			if n >= workflowProjectDefinitions {
				return failure(429, "WORKFLOW_LIMIT", "项目流程定义已达上限")
			}
			id = newID("wf")
			_, err = tx.Exec(ctx, "INSERT INTO studio_workflow_definitions(id,workspace_id,project_id,version) VALUES($1,$2,$3,1)", id, scope.WorkspaceID, scope.ProjectID)
		} else {
			current, e := workflowDefinitionSQL(ctx, tx, scope, id, 0)
			if e != nil {
				return e
			}
			if current.Version != expected {
				return workflowConflict()
			}
			if expected >= 64 {
				return failure(429, "WORKFLOW_VERSION_LIMIT", "流程版本已达上限")
			}
			_, err = tx.Exec(ctx, "UPDATE studio_workflow_definitions SET version=version+1 WHERE id=$1", id)
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO studio_workflow_versions(definition_id,version,workspace_id,project_id,graph) VALUES($1,$2,$3,$4,$5)`, id, expected+1, scope.WorkspaceID, scope.ProjectID, g); err != nil {
			return err
		}
		d, err = workflowDefinitionSQL(ctx, tx, scope, id, expected+1)
		if err != nil {
			return err
		}
		return insertWorkflowReceipt(ctx, tx, scope, "save", key, hash, id, d.Version)
	})
	if err != nil {
		return WorkflowDefinition{}, false, err
	}
	return d, replay, nil
}

func (s *postgresStore) workflowDefinition(scope subscriptionScope, id string, version uint64) (d WorkflowDefinition, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		var e error
		d, e = workflowDefinitionSQL(ctx, c, scope, id, version)
		return e
	})
	return
}
func (s *postgresStore) workflowDefinitionByKey(scope subscriptionScope, key string) (d WorkflowDefinition, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		r, e := readWorkflowReceipt(ctx, c, scope, "save", key)
		if e != nil {
			return workflowReadError(e)
		}
		d, e = workflowDefinitionSQL(ctx, c, scope, r.id, r.version)
		return e
	})
	return
}
func (s *postgresStore) workflowDefinitions(scope subscriptionScope) ([]WorkflowDefinition, error) {
	list := []WorkflowDefinition{}
	err := s.do(func(ctx context.Context, c *pgx.Conn) error {
		rows, err := c.Query(ctx, workflowDefinitionSelect+" AND v.version=d.version ORDER BY v.created_at DESC,d.id LIMIT $3", scope.WorkspaceID, scope.ProjectID, workflowProjectDefinitions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanWorkflowDefinition(rows)
			if err != nil {
				return err
			}
			list = append(list, d)
		}
		return rows.Err()
	})
	return list, err
}

const workflowExecutionSelect = `SELECT x.id,x.project_id,x.definition_id,x.definition_version,COALESCE(x.graph_override,v.graph),x.input,x.models,x.provider_mode,
 x.version,x.status,COALESCE(x.text_run_id,x.inherited_text_run_id,''),COALESCE(x.image_task_id,x.inherited_image_task_id,''),COALESCE(x.video_task_id,''),x.video_artifacts,x.candidates,x.selected,x.error_code,x.created_at,x.updated_at,COALESCE(x.source_workflow_id,''),COALESCE(x.source_version,0),COALESCE(x.from_node,''),x.attempt
 FROM studio_workflow_executions x JOIN studio_workflow_versions v ON v.definition_id=x.definition_id AND v.version=x.definition_version
 WHERE x.workspace_id=$1 AND x.project_id=$2`

func scanWorkflowExecution(row libraryScanner) (WorkflowExecution, error) {
	var x WorkflowExecution
	err := row.Scan(&x.ID, &x.ProjectID, &x.DefinitionID, &x.DefinitionVersion, &x.Graph, &x.Input, &x.FrozenModels, &x.ProviderMode, &x.Version, &x.Status, &x.TextRunID, &x.ImageTaskID, &x.VideoTaskID, &x.VideoArtifacts, &x.Candidates, &x.Selected, &x.ErrorCode, &x.CreatedAt, &x.UpdatedAt, &x.SourceWorkflowID, &x.SourceVersion, &x.FromNode, &x.Attempt)
	return x, workflowReadError(err)
}
func workflowExecutionSQL(ctx context.Context, q sqlQueryRower, scope subscriptionScope, id string) (WorkflowExecution, error) {
	return scanWorkflowExecution(q.QueryRow(ctx, workflowExecutionSelect+" AND x.id=$3", scope.WorkspaceID, scope.ProjectID, id))
}
func (s *postgresStore) workflowExecution(scope subscriptionScope, id string) (x WorkflowExecution, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		var e error
		x, e = workflowExecutionSQL(ctx, c, scope, id)
		return e
	})
	return
}
func (s *postgresStore) workflowExecutions(scope subscriptionScope) ([]WorkflowExecution, error) {
	result := []WorkflowExecution{}
	err := s.do(func(ctx context.Context, c *pgx.Conn) error {
		rows, err := c.Query(ctx, workflowExecutionSelect+" ORDER BY x.created_at DESC,x.id LIMIT $3", scope.WorkspaceID, scope.ProjectID, workflowProjectExecutions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scanWorkflowExecution(rows)
			if err != nil {
				return err
			}
			result = append(result, x)
		}
		return rows.Err()
	})
	return result, err
}

func (s *postgresStore) workflowByStartKey(scope subscriptionScope, key string) (x WorkflowExecution, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		r, e := readWorkflowReceipt(ctx, c, scope, "start", key)
		if e != nil {
			return workflowReadError(e)
		}
		x, e = workflowExecutionSQL(ctx, c, scope, r.id)
		return e
	})
	return
}
func (s *postgresStore) startWorkflow(scope subscriptionScope, id string, version uint64, input, key string, models map[string]Model, providerMode string, actors ...*principal) (WorkflowExecution, bool, error) {
	var x WorkflowExecution
	var replay bool
	hash := payloadHash(struct {
		ID      string
		Version uint64
		Input   string
	}{id, version, input})
	err := s.workflowWrite(scope, actors, func(ctx context.Context, tx pgx.Tx) error {
		r, found, err := replayWorkflowReceipt(ctx, tx, scope, "start", key, hash)
		if err != nil {
			return err
		}
		if found {
			replay = true
			x, err = workflowExecutionSQL(ctx, tx, scope, r.id)
			return err
		}
		if version == 0 || !validatePrompt(input) {
			return failure(422, "INVALID_WORKFLOW_INPUT", "须提供固定版本和有效的运行输入")
		}
		d, err := workflowDefinitionSQL(ctx, tx, scope, id, version)
		if err != nil {
			return err
		}
		if err = validateWorkflowGraph(d.Graph); err != nil {
			return err
		}
		if len(models) != len(d.Graph.Nodes)-1 || (providerMode != "demo" && providerMode != "live") {
			return workflowConflict()
		}
		for _, n := range d.Graph.Nodes {
			if n.Kind == "approval" {
				continue
			}
			m := models[n.Kind]
			kind := n.Kind
			if kind == "text" {
				kind = "chat"
			}
			if !m.Enabled || m.ID != n.ModelID || m.Version != n.ModelVersion || m.Kind != kind {
				return failure(409, "WORKFLOW_MODEL_CHANGED", "流程的冻结模型版本不可用于新的运行")
			}
			if n.Kind == "video" && !m.Capabilities.VideoFirstFrameSupported {
				return failure(422, "WORKFLOW_VIDEO_UNSUPPORTED", "视频模型不支持已选首帧输入")
			}
			if n.Kind == "image" || n.Kind == "video" {
				if _, err := resolveTaskParameters(*n.Parameters, m); err != nil {
					return err
				}
			}
		}
		if err = checkWorkspaceGenerationSQL(ctx, tx, scope.WorkspaceID); err != nil {
			return err
		}
		var n int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM studio_workflow_executions WHERE workspace_id=$1 AND project_id=$2", scope.WorkspaceID, scope.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n >= workflowProjectExecutions {
			return failure(429, "WORKFLOW_EXECUTION_LIMIT", "项目工作流运行已达上限")
		}
		execID := newID("wfr")
		if _, err = tx.Exec(ctx, `INSERT INTO studio_workflow_executions(id,workspace_id,project_id,definition_id,definition_version,input,models,provider_mode,status)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,'TEXT_READY')`, execID, scope.WorkspaceID, scope.ProjectID, id, version, input, models, providerMode); err != nil {
			return err
		}
		for _, n := range d.Graph.Nodes {
			if n.Kind == "approval" {
				continue
			}
			if _, err = tx.Exec(ctx, "INSERT INTO studio_workflow_intents(execution_id,kind,child_key) VALUES($1,$2,$3)", execID, n.Kind, newID("wfstep")); err != nil {
				return err
			}
		}
		x, err = workflowExecutionSQL(ctx, tx, scope, execID)
		if err != nil {
			return err
		}
		if err = appendWorkflowOutboxSQL(ctx, tx, scope, execID, x.Version, x.Status); err != nil {
			return err
		}
		return insertWorkflowReceipt(ctx, tx, scope, "start", key, hash, execID, x.Version)
	})
	if err != nil {
		return WorkflowExecution{}, false, err
	}
	return x, replay, nil
}

func workflowKindState(kind, suffix string) string { return strings.ToUpper(kind) + "_" + suffix }
