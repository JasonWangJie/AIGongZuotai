package studio

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func accountRestrictedError() error {
	return failure(403, "ACCOUNT_RESTRICTED", "账户已受限，请在账户复核页查看原限制或提出申诉")
}

func limitedAccountRoute(method, path string) bool {
	if method == "POST" {
		return path == "/api/v1/account/appeals"
	}
	if method != "GET" && method != "HEAD" {
		return false
	}
	switch path {
	case "/api/v1/account/access", "/api/v1/account/appeals", "/api/v1/account/appeals/receipt":
		return true
	}
	id, ok := strings.CutPrefix(path, "/api/v1/account/appeals/")
	return ok && validOperationsID(id, "aapl")
}

func accountRequestPrincipal(ctx context.Context) *principal {
	p, _ := ctx.Value(principalKey{}).(*principal)
	return p
}

// The user row and original session are locked until acceptance commits. A
// suspended and subsequently lifted account must use its newly issued session.
func checkOrdinaryAccountSQL(ctx context.Context, tx pgx.Tx, p *principal) error {
	if p == nil || p.User == nil {
		return nil
	}
	if _, err := operationsSessionSQL(ctx, tx, p); err != nil {
		return err
	}
	var restricted bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_account_controls WHERE user_id=$1 AND state<>'OPEN')", p.User.ID).Scan(&restricted); err != nil {
		return err
	}
	if restricted {
		return accountRestrictedError()
	}
	return nil
}

// Called only after admitting a bounded stream. The periodic session read
// stays independent of response writes, replay, and a long heartbeat setting.
func (a *App) accountStreamContext(r *http.Request) (*http.Request, func()) {
	p := principalFrom(r)
	if p == nil || p.User == nil || a.store == nil {
		return r, func() {}
	}
	if a.streamSessions != nil {
		ctx, release := a.streamSessions.subscribe(r.Context(), p)
		return r.WithContext(ctx), release
	}
	ctx, cancel := context.WithCancel(r.Context())
	done := make(chan struct{})
	interval := min(a.config.Heartbeat, 5*time.Second)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.ctx.Done():
				cancel()
				return
			case <-ticker.C:
				valid := false
				err := a.store.do(func(dbctx context.Context, c *pgx.Conn) error {
					return c.QueryRow(dbctx, `SELECT EXISTS(SELECT 1 FROM studio_sessions s JOIN studio_users u ON u.id=s.user_id
					LEFT JOIN studio_account_controls g ON g.user_id=u.id
					WHERE s.token_hash=$1 AND s.id=$2 AND s.credential_version=u.credential_version AND s.expires_at>clock_timestamp() AND COALESCE(g.state,'OPEN')='OPEN')`, p.SessionHash, p.SessionID).Scan(&valid)
				})
				if err != nil || !valid {
					cancel()
					return
				}
			}
		}
	}()
	return r.WithContext(ctx), func() { cancel(); <-done }
}

func (a *App) accountTargetsHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireOperationsAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	type item struct {
		TargetUserID string `json:"target_user_id"`
		Version      uint64 `json:"version"`
	}
	items := []item{}
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
				return e
			}
			rows, e := tx.Query(ctx, "SELECT target_user_id,version FROM studio_account_governance_grants WHERE operator_user_id=$1 AND active ORDER BY target_user_id LIMIT 65", p.User.ID)
			if e != nil {
				return e
			}
			rowsFound, e := pgx.CollectRows(rows, pgx.RowToStructByPos[item])
			if e != nil {
				return e
			}
			if len(rowsFound) > 64 {
				return errors.New("account governance grant capacity exceeded")
			}
			items = append(items, rowsFound...)
			return nil
		})
	})
	contentAppealResponse(w, map[string]any{"items": items}, 200, err)
}
func (a *App) registerAccountGovernanceRoutes(mux *http.ServeMux) {
	const own = "/api/v1/account"
	const ops = "/api/v1/operations/account-targets"
	mux.HandleFunc("GET "+own+"/access", a.accountControlReadHTTP)
	mux.HandleFunc("GET "+own+"/appeals", a.accountControlListHTTP)
	mux.HandleFunc("GET "+own+"/appeals/{id}", a.accountControlListHTTP)
	mux.HandleFunc("POST "+own+"/appeals", a.accountControlAppealHTTP)
	mux.HandleFunc("GET "+own+"/appeals/receipt", a.accountControlAppealReceiptHTTP)
	mux.HandleFunc("GET "+ops, a.accountTargetsHTTP)
	mux.HandleFunc("GET "+ops+"/{target}", a.accountControlReadHTTP)
	mux.HandleFunc("GET "+ops+"/{target}/appeals", a.accountControlListHTTP)
	mux.HandleFunc("GET "+ops+"/{target}/appeals/{id}", a.accountControlListHTTP)
	mux.HandleFunc("POST "+ops+"/{target}/plans", a.accountControlPlanHTTP)
	mux.HandleFunc("POST "+ops+"/{target}/execute", a.accountControlExecuteHTTP)
	mux.HandleFunc("GET "+ops+"/{target}/plans/{plan}/receipt", a.accountControlPlanReceiptHTTP)
}

func (a *App) accountGovernanceRequest(r *http.Request) (*principal, string, bool, error) {
	target := r.PathValue("target")
	if target != "" {
		if !validOperationsID(target, "usr") {
			return nil, "", true, operationsDenied()
		}
		p, e := a.requireOperationsAccount(r)
		return p, target, true, e
	}
	p, e := a.requireAccount(r)
	if e != nil {
		return nil, "", false, e
	}
	return p, p.User.ID, false, nil
}
func (a *App) accountControlReadHTTP(w http.ResponseWriter, r *http.Request) {
	p, target, ops, e := a.accountGovernanceRequest(r)
	if e != nil {
		writeError(w, e)
		return
	}
	result, e := a.accountGovernanceAccess(p, target, ops)
	contentAppealResponse(w, result, 200, e)
}
func (a *App) accountControlListHTTP(w http.ResponseWriter, r *http.Request) {
	p, target, ops, e := a.accountGovernanceRequest(r)
	if e != nil {
		writeError(w, e)
		return
	}
	if id := r.PathValue("id"); id != "" {
		if !validOperationsID(id, "aapl") {
			writeError(w, operationsNotFound())
			return
		}
		v, e := a.readAccountAppeal(p, target, id, ops)
		contentAppealResponse(w, v, 200, e)
		return
	}
	limit, e := generationPageLimit(r)
	if e != nil {
		writeError(w, e)
		return
	}
	v, e := a.listAccountAppeals(p, target, r.URL.Query().Get("state"), r.URL.Query().Get("cursor"), limit, ops)
	contentAppealResponse(w, v, 200, e)
}
func (a *App) accountControlAppealHTTP(w http.ResponseWriter, r *http.Request) {
	p, e := a.requireAccount(r)
	if e != nil {
		writeError(w, e)
		return
	}
	var in struct {
		RestrictionID string `json:"restriction_id"`
		GenerationAppealInput
	}
	if e = decodeGenerationJSON(w, r, &in, "restriction_id", "reason_code", "statement"); e != nil {
		writeError(w, e)
		return
	}
	key, e := idempotencyKey(r)
	if e != nil {
		writeError(w, e)
		return
	}
	value, replay, e := a.createAccountAppeal(p, in.RestrictionID, in.GenerationAppealInput, key)
	if e == nil && replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	contentAppealResponse(w, value, 201, e)
}
func (a *App) accountControlAppealReceiptHTTP(w http.ResponseWriter, r *http.Request) {
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
	v, e := a.accountAppealReceipt(p, key)
	contentAppealResponse(w, v, 200, e)
}
func (a *App) accountControlPlanHTTP(w http.ResponseWriter, r *http.Request) {
	p, target, _, e := a.accountGovernanceRequest(r)
	if e != nil {
		writeError(w, e)
		return
	}
	var in AccountPlanInput
	if e = decodeGenerationJSON(w, r, &in, "action", "expected_version", "restriction_id", "appeal_id", "expected_appeal_version", "reason_code", "evidence_ref", "public_reply"); e != nil {
		writeError(w, e)
		return
	}
	v, e := a.previewAccountControl(p, target, in)
	contentAppealResponse(w, v, 201, e)
}
func (a *App) accountControlExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	p, target, _, e := a.accountGovernanceRequest(r)
	if e != nil {
		writeError(w, e)
		return
	}
	var in struct {
		PlanID     string `json:"plan_id"`
		PlanDigest string `json:"plan_digest"`
		Password   string `json:"password"`
	}
	if e = decodeGenerationJSON(w, r, &in, "plan_id", "plan_digest", "password"); e != nil {
		writeError(w, e)
		return
	}
	// Fail closed on the exact target grant before performing expensive bcrypt.
	if _, e = a.accountGovernanceAccess(p, target, true); e != nil {
		writeError(w, e)
		return
	}
	proof, e := a.verifyOperationsPassword(r, in.Password)
	in.Password = ""
	if e != nil {
		operationsPlanError(w, e)
		return
	}
	v, e := a.executeAccountControl(p, target, in.PlanID, in.PlanDigest, proof)
	contentAppealResponse(w, v, 200, e)
}
func (a *App) accountControlPlanReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	p, target, _, e := a.accountGovernanceRequest(r)
	if e != nil {
		writeError(w, e)
		return
	}
	plan := r.PathValue("plan")
	if !validOperationsID(plan, "aplan") {
		writeError(w, operationsNotFound())
		return
	}
	v, e := a.accountControlReceipt(p, target, plan)
	contentAppealResponse(w, v, 200, e)
}
