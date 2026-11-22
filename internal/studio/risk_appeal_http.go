package studio

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (a *App) registerRiskAppealRoutes(mux *http.ServeMux) {
	const user = "/api/v1/risk"
	const ops = "/api/v1/operations/risk-appeals"
	mux.HandleFunc("GET "+user+"/eligibility", a.riskEligibilityHTTP)
	mux.HandleFunc("POST "+user+"/appeals", a.riskAppealCreateHTTP)
	mux.HandleFunc("GET "+user+"/appeals", a.riskOwnerReadHTTP)
	mux.HandleFunc("GET "+user+"/appeals/{id}", a.riskOwnerReadHTTP)
	mux.HandleFunc("GET "+user+"/appeals/receipt", a.riskOriginalAppealHTTP)
	mux.HandleFunc("GET "+ops, a.riskReviewerReadHTTP)
	mux.HandleFunc("GET "+ops+"/{id}", a.riskReviewerReadHTTP)
	mux.HandleFunc("POST "+ops+"/{id}/plans", a.riskPlanHTTP)
	mux.HandleFunc("POST "+ops+"/{id}/execute", a.riskExecuteHTTP)
	mux.HandleFunc("GET "+ops+"/{id}/plans/{plan}/receipt", a.riskPlanReceiptHTTP)
}
func (a *App) riskEligibilityHTTP(w http.ResponseWriter, r *http.Request) {
	p, e := a.requireAccount(r)
	if e != nil {
		writeError(w, e)
		return
	}
	q := r.URL.Query()
	v, e := a.riskEligibility(p, q.Get("project_id"), q.Get("quote_id"))
	contentAppealResponse(w, v, 200, e)
}
func (a *App) riskAppealCreateHTTP(w http.ResponseWriter, r *http.Request) {
	p, e := a.requireAccount(r)
	if e != nil {
		writeError(w, e)
		return
	}
	var in RiskAppealInput
	if e = decodeGenerationJSON(w, r, &in, "project_id", "quote_id", "reason_code", "statement"); e != nil {
		writeError(w, e)
		return
	}
	if e = contentAppealProject(r, in.ProjectID); e != nil {
		writeError(w, e)
		return
	}
	key, e := idempotencyKey(r)
	if e != nil {
		writeError(w, e)
		return
	}
	v, replay, e := a.createRiskAppeal(p, in, key)
	if replay && e == nil {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	contentAppealResponse(w, v, 201, e)
}
func (a *App) riskOriginalAppealHTTP(w http.ResponseWriter, r *http.Request) {
	p, e := a.requireAccount(r)
	if e != nil {
		writeError(w, e)
		return
	}
	key, e := idempotencyKey(r)
	if e != nil {
		writeError(w, e)
		return
	}
	v, e := a.readRiskAppeal(p, p.Workspace.ID, r.URL.Query().Get("project_id"), "", key, false)
	contentAppealResponse(w, v, 200, e)
}
func (a *App) riskOwnerReadHTTP(w http.ResponseWriter, r *http.Request) {
	p, e := a.requireAccount(r)
	if e != nil {
		writeError(w, e)
		return
	}
	a.riskReadHTTP(w, r, p, p.Workspace.ID, r.URL.Query().Get("project_id"), false)
}
func (a *App) riskReviewerReadHTTP(w http.ResponseWriter, r *http.Request) {
	ws := r.URL.Query().Get("workspace_id")
	p, e := a.requireOperations(r, ws, "risk_review")
	if e != nil {
		writeError(w, e)
		return
	}
	a.riskReadHTTP(w, r, p, ws, "", true)
}
func (a *App) riskReadHTTP(w http.ResponseWriter, r *http.Request, p *principal, ws, project string, ops bool) {
	if id := r.PathValue("id"); id != "" {
		v, e := a.readRiskAppeal(p, ws, project, id, "", ops)
		contentAppealResponse(w, v, 200, e)
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "all"
	}
	if state != "all" && state != "OPEN" && state != "APPROVED" && state != "DENIED" {
		writeError(w, failure(400, "INVALID_FILTER", "风控复核状态无效"))
		return
	}
	route := "risk-owner"
	if ops {
		route = "risk-operations"
	}
	cur, limit, e := parseOperationsPage(r, p, ws, route, project+"/"+state)
	if e != nil {
		writeError(w, e)
		return
	}
	result := struct {
		Items      []RiskAppeal `json:"items"`
		NextCursor *string      `json:"next_cursor"`
	}{Items: []RiskAppeal{}}
	e = a.contentAppealTx(p, ops, func(ctx context.Context, tx pgx.Tx) error {
		owner := p.User.ID
		if ops {
			if _, err := authorizeOperationsSQL(ctx, tx, p, ws, "risk_review"); err != nil {
				return err
			}
			owner = ""
		} else if err := riskOwnerSQL(ctx, tx, p, project); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+riskReviewFields+",'',''"+riskReviewTables+`WHERE r.workspace_id=$1 AND ($2='' OR r.project_id=$2) AND ($3='' OR r.applicant_id=$3) AND ($4='all' OR r.state=$4) AND ($5='' OR (r.created_at,r.id)<($6,$5)) ORDER BY r.created_at DESC,r.id DESC LIMIT $7`, ws, project, owner, state, cur.ID, cur.Before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanRiskAppeal(rows)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, v)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(result.Items) > limit {
			result.Items = result.Items[:limit]
			last := result.Items[limit-1]
			cur.ID = last.ID
			cur.Before = last.CreatedAt
			result.NextCursor = nextOperationsCursor(cur)
		}
		return nil
	})
	contentAppealResponse(w, result, 200, e)
}
func (a *App) riskPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var in ContentAppealPlanInput
	if e := decodeGenerationJSON(w, r, &in, "workspace_id", "expected_version", "action", "public_reply", "evidence_ref"); e != nil {
		writeError(w, e)
		return
	}
	p, e := a.requireOperations(r, in.WorkspaceID, "risk_review")
	if e != nil {
		writeError(w, e)
		return
	}
	v, e := a.previewRiskReview(p, r.PathValue("id"), in)
	contentAppealResponse(w, v, 201, e)
}
func (a *App) riskExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		PlanID      string `json:"plan_id"`
		PlanDigest  string `json:"plan_digest"`
		Password    string `json:"password"`
	}
	if e := decodeGenerationJSON(w, r, &in, "workspace_id", "plan_id", "plan_digest", "password"); e != nil {
		writeError(w, e)
		return
	}
	p, e := a.requireOperations(r, in.WorkspaceID, "risk_review")
	if e != nil {
		writeError(w, e)
		return
	}
	proof, e := a.verifyOperationsPassword(r, in.Password)
	in.Password = ""
	if e != nil {
		operationsPlanError(w, e)
		return
	}
	v, e := a.executeRiskReview(p, in.WorkspaceID, r.PathValue("id"), in.PlanID, in.PlanDigest, proof)
	contentAppealResponse(w, v, 200, e)
}
func (a *App) riskPlanReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	ws := r.URL.Query().Get("workspace_id")
	p, e := a.requireOperations(r, ws, "risk_review")
	if e != nil {
		writeError(w, e)
		return
	}
	v, e := a.riskPlanReceipt(p, ws, r.PathValue("id"), r.PathValue("plan"))
	contentAppealResponse(w, v, 200, e)
}
