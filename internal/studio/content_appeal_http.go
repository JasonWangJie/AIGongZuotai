package studio

import "net/http"

func (a *App) registerContentAppealRoutes(mux *http.ServeMux) {
	const owner = "/api/v1/content-appeals"
	const ops = "/api/v1/operations/content-appeals"
	mux.HandleFunc("POST "+owner, a.contentAppealCreateHTTP)
	mux.HandleFunc("GET "+owner, a.contentAppealsHTTP)
	mux.HandleFunc("GET "+owner+"/{id}", a.contentAppealsHTTP)
	mux.HandleFunc("GET "+owner+"/receipt", a.contentAppealReceiptHTTP)
	mux.HandleFunc("GET "+ops, a.operationsContentAppealsHTTP)
	mux.HandleFunc("GET "+ops+"/{id}", a.operationsContentAppealsHTTP)
	mux.HandleFunc("POST "+ops+"/{id}/plans", a.contentAppealPreviewHTTP)
	mux.HandleFunc("POST "+ops+"/{id}/execute", a.contentAppealExecuteHTTP)
	mux.HandleFunc("GET "+ops+"/{id}/plans/{plan}/receipt", a.contentAppealPlanReceiptHTTP)
	mux.HandleFunc("GET "+ops+"/finance", a.contentAppealFinanceQueueHTTP)
	mux.HandleFunc("POST "+ops+"/{id}/finance-case", a.contentAppealFinanceLinkHTTP)
	mux.HandleFunc("GET "+ops+"/{id}/finance-case", a.contentAppealFinanceReadHTTP)
	mux.HandleFunc("GET /api/v1/operations/cases/{case}/content-appeal", a.contentAppealFinanceReadHTTP)
}

func contentAppealResponse(w http.ResponseWriter, value any, status int, err error) {
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	writeJSON(w, status, value)
}

func (a *App) contentAppealCreateHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in ContentAppealInput
	if err = decodeGenerationJSON(w, r, &in, "project_id", "resource_type", "resource_id", "moderation_version", "reason_code", "statement"); err != nil {
		writeError(w, err)
		return
	}
	if err = contentAppealProject(r, in.ProjectID); err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, replayed, err := a.createContentAppeal(p, in, key)
	if replayed && err == nil {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	contentAppealResponse(w, result, 201, err)
}

// The explicitly selected project scopes an owner's reads and original-key receipts.
func contentAppealProject(r *http.Request, project string) error {
	header := r.Header.Get("X-Project-ID")
	if !validOperationsID(project, "prj") || (header != "" && header != project) {
		return failure(404, "NOT_FOUND", "项目或内容申诉不存在")
	}
	return nil
}

func (a *App) contentAppealReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	project := r.URL.Query().Get("project_id")
	if err = contentAppealProject(r, project); err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := a.contentAppealReceipt(p, project, key)
	contentAppealResponse(w, value, 200, err)
}

func (a *App) contentAppealsHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	project := r.URL.Query().Get("project_id")
	if err = contentAppealProject(r, project); err != nil {
		writeError(w, err)
		return
	}
	a.contentAppealQueryHTTP(w, r, p, p.Workspace.ID, project, false)
}

func (a *App) operationsContentAppealsHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "content_appeal_review")
	if err != nil {
		writeError(w, err)
		return
	}
	a.contentAppealQueryHTTP(w, r, p, workspace, r.URL.Query().Get("project_id"), true)
}

func (a *App) contentAppealQueryHTTP(w http.ResponseWriter, r *http.Request, p *principal, workspace, project string, operations bool) {
	if project != "" && !validOperationsID(project, "prj") {
		writeError(w, contentAppealMissing())
		return
	}
	if id := r.PathValue("id"); id != "" {
		if !validOperationsID(id, "capl") {
			writeError(w, contentAppealMissing())
			return
		}
		result, err := a.readContentAppeal(p, workspace, project, id, operations)
		contentAppealResponse(w, result, 200, err)
		return
	}
	limit, err := generationPageLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.listContentAppeals(p, workspace, project, r.URL.Query().Get("state"), r.URL.Query().Get("cursor"), limit, operations)
	contentAppealResponse(w, result, 200, err)
}

func (a *App) contentAppealPreviewHTTP(w http.ResponseWriter, r *http.Request) {
	var in ContentAppealPlanInput
	if err := decodeGenerationJSON(w, r, &in, "workspace_id", "expected_version", "action", "evidence_ref", "public_reply"); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, "content_appeal_review")
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.previewContentAppeal(p, r.PathValue("id"), in)
	contentAppealResponse(w, result, 201, err)
}

func (a *App) contentAppealExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		PlanID      string `json:"plan_id"`
		PlanDigest  string `json:"plan_digest"`
		Password    string `json:"password"`
	}
	if err := decodeGenerationJSON(w, r, &in, "workspace_id", "plan_id", "plan_digest", "password"); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, "content_appeal_review")
	if err != nil {
		writeError(w, err)
		return
	}
	proof, err := a.verifyOperationsPassword(r, in.Password)
	in.Password = ""
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	result, err := a.executeContentAppeal(p, in.WorkspaceID, r.PathValue("id"), in.PlanID, in.PlanDigest, proof)
	contentAppealResponse(w, result, 200, err)
}

func (a *App) contentAppealPlanReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "content_appeal_review")
	if err != nil {
		writeError(w, err)
		return
	}
	if !validOperationsID(r.PathValue("id"), "capl") || !validOperationsID(r.PathValue("plan"), "caplan") {
		writeError(w, contentAppealMissing())
		return
	}
	result, err := a.contentAppealPlanReceipt(p, workspace, r.PathValue("id"), r.PathValue("plan"))
	contentAppealResponse(w, result, 200, err)
}
