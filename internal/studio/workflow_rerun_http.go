package studio

import "net/http"

func (a *App) registerWorkflowRerunRoutes(mux *http.ServeMux) {
	const path = "/api/v1/projects/{project}/workflow-runs/{workflow}/rerun"
	mux.HandleFunc("GET "+path+"/preview", a.workflowRerunPreviewHTTP)
	mux.HandleFunc("GET "+path+"/receipt", a.workflowRerunReceiptHTTP)
	mux.HandleFunc("POST "+path, a.workflowRerunHTTP)
}

func (a *App) workflowRerunPreviewHTTP(w http.ResponseWriter, r *http.Request) {
	scope, err := a.workflowProjectScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := a.store.workflowRerunPreview(scope, r.PathValue("workflow"), r.URL.Query().Get("from_node"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *App) workflowRerunReceiptHTTP(w http.ResponseWriter, r *http.Request) {
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
	x, err := a.store.workflowRerunReceipt(scope, r.PathValue("workflow"), key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, x)
}

func (a *App) workflowRerunHTTP(w http.ResponseWriter, r *http.Request) {
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
	var in workflowRerunInput
	if err = decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.ExpectedVersion == 0 || !workflowNodeID.MatchString(in.FromNode) {
		writeError(w, failure(422, "INVALID_WORKFLOW_RERUN", "须提供原运行版本与有效的起点节点"))
		return
	}
	result, replay, err := a.store.rerunWorkflow(scope, r.PathValue("workflow"), key, in, principalFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	workflowResponse(w, 201, result, replay)
}
