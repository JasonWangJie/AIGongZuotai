package studio

import (
	"errors"
	"net/http"
)

func (a *App) registerWorkflowExecutionRoutes(mux *http.ServeMux) {
	a.registerWorkflowCancelRoutes(mux)
	const collection = "/api/v1/projects/{project}/workflow-runs"
	const nodes = collection + "/{workflow}/nodes/{node}"
	mux.HandleFunc("POST "+collection, a.workflowStartHTTP)
	mux.HandleFunc("GET "+collection, a.workflowRunsHTTP)
	mux.HandleFunc("GET "+collection+"/receipt", a.workflowStartReceiptHTTP)
	mux.HandleFunc("GET "+collection+"/{workflow}", a.workflowDetailHTTP)
	mux.HandleFunc("GET "+nodes+"/input", a.workflowInputHTTP)
	mux.HandleFunc("GET "+nodes+"/receipt", a.workflowActionReceiptHTTP)
	mux.HandleFunc("POST "+nodes+"/execute", a.workflowExecuteHTTP)
	mux.HandleFunc("POST "+nodes+"/confirm", a.workflowConfirmHTTP)
	a.registerWorkflowRerunRoutes(mux)
}

func workflowHTTPStatus(err error) int {
	var op *operationError
	if errors.As(err, &op) {
		return op.status
	}
	return 0
}

type workflowStartInput struct {
	DefinitionID      string `json:"definition_id"`
	DefinitionVersion uint64 `json:"definition_version"`
	Input             string `json:"input"`
}

func (a *App) workflowStartHTTP(w http.ResponseWriter, r *http.Request) {
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
	var in workflowStartInput
	if err = decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.DefinitionVersion == 0 || !validatePrompt(in.Input) {
		writeError(w, failure(422, "INVALID_WORKFLOW_INPUT", "需要固定的定义版本及最多 16000 字节的非空输入"))
		return
	}
	_, err = a.store.workflowByStartKey(scope, key)
	models := make(map[string]Model)
	if err != nil {
		if workflowHTTPStatus(err) != 404 {
			writeError(w, err)
			return
		}
		def, err := a.store.workflowDefinition(scope, in.DefinitionID, in.DefinitionVersion)
		if err != nil {
			writeError(w, err)
			return
		}
		for _, node := range def.Graph.Nodes {
			if node.Kind == "approval" {
				continue
			}
			entry, found := a.catalog.Entries[node.ModelID]
			kind := node.Kind
			if kind == "text" {
				kind = "chat"
			}
			if !found || !entry.Model.Enabled || entry.Model.Kind != kind || entry.Model.Version != node.ModelVersion {
				writeError(w, failure(409, "WORKFLOW_MODEL_CHANGED", "定义引用的模型已变更，请保存新版本"))
				return
			}
			if kind == "chat" {
				if _, err := a.resolveModel(node.ModelID, kind, node.ModelVersion, workflowPrompt(node.Prompt, in.Input)); err != nil {
					writeError(w, err)
					return
				}
			}
			models[node.Kind] = entry.Model
		}
	}
	run, replayed, err := a.store.startWorkflow(scope, in.DefinitionID, in.DefinitionVersion, in.Input, key, models, a.config.Mode, principalFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	workflowResponse(w, http.StatusCreated, run, replayed)
}

func workflowResponse(w http.ResponseWriter, code int, v any, replay bool) {
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, code, v)
}

func (a *App) workflowStartReceiptHTTP(w http.ResponseWriter, r *http.Request) {
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
	run, err := a.store.workflowByStartKey(scope, key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, run)
}

func (a *App) workflowRunsHTTP(w http.ResponseWriter, r *http.Request) {
	scope, err := a.workflowProjectScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	runs, err := a.store.workflowExecutions(scope)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": runs})
}

func (a *App) workflowRequest(r *http.Request) (subscriptionScope, WorkflowExecution, error) {
	scope, err := a.workflowProjectScope(r)
	if err != nil {
		return scope, WorkflowExecution{}, err
	}
	run, err := a.store.workflowExecution(scope, r.PathValue("workflow"))
	return scope, run, err
}

func (a *App) workflowDetailHTTP(w http.ResponseWriter, r *http.Request) {
	scope, run, err := a.workflowRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := a.workflowDetail(scope, run)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (a *App) workflowInputHTTP(w http.ResponseWriter, r *http.Request) {
	scope, run, err := a.workflowRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	node, err := workflowNodeFor(run.Graph, r.PathValue("node"))
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := a.workflowDetail(scope, run)
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := a.workflowNodeInput(d, node)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, input)
}

func (a *App) workflowActionReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	scope, run, err := a.workflowRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	node, err := workflowNodeFor(run.Graph, r.PathValue("node"))
	if err != nil {
		writeError(w, err)
		return
	}
	receipt, err := a.store.workflowActionReceipt(scope, run.ID, node.Kind, key)
	if err != nil {
		writeError(w, err)
		return
	}
	d, err := a.workflowDetail(scope, receipt)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, d)
}

type workflowNodeMutation struct {
	Scope subscriptionScope
	Run   WorkflowExecution
	Node  WorkflowNode
	Key   string
	End   func()
}

func (a *App) beginWorkflowNodeMutation(r *http.Request) (workflowNodeMutation, error) {
	m := workflowNodeMutation{}
	var err error
	if m.Scope, err = a.workflowProjectScope(r); err != nil {
		return m, err
	}
	if m.Key, err = idempotencyKey(r); err != nil {
		return m, err
	}
	id := r.PathValue("workflow")
	if m.End, err = a.workflowActions.begin(id); err != nil {
		return m, err
	}
	if m.Run, err = a.store.workflowExecution(m.Scope, id); err == nil {
		m.Node, err = workflowNodeFor(m.Run.Graph, r.PathValue("node"))
	}
	if err != nil {
		m.End()
	}
	return m, err
}

func (a *App) workflowExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version uint64 `json:"expected_version"`
		QuoteID string `json:"quote_id,omitempty"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	m, err := a.beginWorkflowNodeMutation(r)
	if err != nil {
		writeError(w, err)
		return
	}
	defer m.End()
	d, replay, err := a.executeWorkflowNode(r.Context(), m.Scope, m.Run, m.Node, in.Version, in.QuoteID, m.Key, principalFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	workflowResponse(w, http.StatusOK, d, replay)
}

func (a *App) workflowConfirmHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version  uint64 `json:"expected_version"`
		AssetID  string `json:"asset_id"`
		Revision uint64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	m, err := a.beginWorkflowNodeMutation(r)
	if err != nil {
		writeError(w, err)
		return
	}
	defer m.End()
	d, replay, err := a.confirmWorkflowNode(m.Scope, m.Run, m.Node, in.Version, in.AssetID, in.Revision, m.Key, principalFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	workflowResponse(w, http.StatusOK, d, replay)
}
