package studio

import (
	"net/http"
	"strconv"
)

func (a *App) registerWorkflowDefinitionRoutes(mux *http.ServeMux) {
	const definitions = "/api/v1/projects/{project}/workflow-definitions"
	mux.HandleFunc("GET "+definitions, a.listWorkflowDefinitionsHTTP)
	mux.HandleFunc("POST "+definitions, a.saveWorkflowDefinitionHTTP)
	mux.HandleFunc("GET "+definitions+"/receipt", a.workflowDefinitionReceiptHTTP)
	mux.HandleFunc("GET "+definitions+"/{definition}/versions/{version}", a.workflowDefinitionVersionHTTP)
}

func (a *App) workflowProjectScope(r *http.Request) (subscriptionScope, error) {
	var scope subscriptionScope
	if a.store == nil {
		return scope, failure(501, "WORKFLOW_STORAGE_REQUIRED", "项目工作流需要 PostgreSQL 持久存储")
	}
	if a.config.AuthMode != "required" {
		return scope, failure(501, "WORKFLOW_ACCOUNTS_REQUIRED", "项目工作流需要启用账户和项目鉴权")
	}
	p := principalFrom(r)
	if p == nil || p.User == nil {
		return scope, failure(401, "AUTH_REQUIRED", "请先登录")
	}
	project := r.PathValue("project")
	if !a.authorizeProject(p, project) || !resourceAllowed(r, p.Workspace.ID, project) {
		return scope, failure(404, "PROJECT_NOT_FOUND", "项目不存在或不属于当前工作区")
	}
	return subscriptionScope{WorkspaceID: p.Workspace.ID, ProjectID: project}, nil
}

func (a *App) listWorkflowDefinitionsHTTP(w http.ResponseWriter, r *http.Request) {
	scope, err := a.workflowProjectScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	items, err := a.store.workflowDefinitions(scope)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) saveWorkflowDefinitionHTTP(w http.ResponseWriter, r *http.Request) {
	scope, err := a.workflowProjectScope(r)
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
		DefinitionID    string        `json:"definition_id"`
		ExpectedVersion *uint64       `json:"expected_version"`
		Graph           WorkflowGraph `json:"graph"`
	}
	if err = decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if input.ExpectedVersion == nil {
		writeError(w, failure(422, "WORKFLOW_VERSION_REQUIRED", "新建时 expected_version 为 0，编辑时请提供当前版本"))
		return
	}
	definition, replayed, err := a.store.saveWorkflowDefinition(scope, input.DefinitionID, *input.ExpectedVersion, input.Graph, key, principalFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, definition)
}

func (a *App) workflowDefinitionReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	scope, err := a.workflowProjectScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	definition, err := a.store.workflowDefinitionByKey(scope, key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, definition)
}

func (a *App) workflowDefinitionVersionHTTP(w http.ResponseWriter, r *http.Request) {
	scope, err := a.workflowProjectScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	version, err := strconv.ParseUint(r.PathValue("version"), 10, 63)
	if err != nil || version == 0 {
		writeError(w, failure(400, "INVALID_WORKFLOW_VERSION", "流程版本须为正整数"))
		return
	}
	definition, err := a.store.workflowDefinition(scope, r.PathValue("definition"), version)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, definition)
}
