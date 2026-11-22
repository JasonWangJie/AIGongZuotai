package studio

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"
)

func (a *App) registerSupportTicketRoutes(mux *http.ServeMux) {
	const owner = "/api/v1/support/tickets"
	const staff = "/api/v1/operations/support/tickets"
	registerReads := func(base string, staffView bool) {
		read := func(w http.ResponseWriter, r *http.Request) { a.supportTicketReadHTTP(w, r, staffView, false) }
		mux.HandleFunc("GET "+base, read)
		mux.HandleFunc("GET "+base+"/{ticket}", read)
		mux.HandleFunc("GET "+base+"/receipt", func(w http.ResponseWriter, r *http.Request) {
			a.supportTicketReadHTTP(w, r, staffView, true)
		})
	}
	registerReads(owner, false)
	registerReads(staff, true)
	mux.HandleFunc("POST "+owner, a.supportTicketCreateHTTP)
	mux.HandleFunc("POST "+owner+"/{ticket}/followup", func(w http.ResponseWriter, r *http.Request) {
		a.supportTicketOwnerActionHTTP(w, r, "followup")
	})
	mux.HandleFunc("POST "+owner+"/{ticket}/close", func(w http.ResponseWriter, r *http.Request) {
		a.supportTicketOwnerActionHTTP(w, r, "close")
	})
	mux.HandleFunc("POST "+staff+"/{ticket}/reply", a.supportTicketReplyHTTP)
}

func (a *App) supportHTTPPrincipal(r *http.Request) (*principal, error) {
	if a.store == nil || a.config.AuthMode != "required" {
		return nil, failure(501, "SUPPORT_STORAGE_REQUIRED", "客服工单需要持久账户模式")
	}
	p := principalFrom(r)
	if p == nil || p.User == nil {
		return nil, failure(401, "AUTH_REQUIRED", "请先登录")
	}
	return p, nil
}

// The wire budget also accommodates clients that encode Unicode as JSON
// escapes. The store separately bounds the decoded subject and message.
func decodeSupportTicketInput(w http.ResponseWriter, r *http.Request, out any, fields ...string) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return failure(415, "JSON_REQUIRED", "请使用 application/json")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			return failure(413, "SUPPORT_BODY_TOO_LARGE", "客服工单请求最多 32 KiB")
		}
		return failure(400, "INVALID_JSON", "无法读取工单请求")
	}
	if !utf8.Valid(raw) || validateCatalogJSONKeys(raw) != nil {
		return failure(400, "INVALID_JSON", "工单请求必须是有效 UTF-8，字段不可重复")
	}
	if _, ok := orderFields(raw, fields...); !ok || json.Unmarshal(raw, out) != nil {
		return failure(400, "INVALID_JSON", "工单请求字段或类型无效")
	}
	return nil
}

func (a *App) supportTicketReadHTTP(w http.ResponseWriter, r *http.Request, staff, receipt bool) {
	p, err := a.supportHTTPPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	workspace := p.Workspace.ID
	q := r.URL.Query()
	if r.ContentLength != 0 {
		writeError(w, failure(400, "INVALID_SUPPORT_QUERY", "读取客服工单无需请求正文"))
		return
	}
	for field, values := range q {
		allowed := staff && field == "workspace_id" || receipt && field == "operation" ||
			!receipt && r.PathValue("ticket") == "" && field == "cursor"
		if !allowed || len(values) != 1 {
			writeError(w, failure(400, "INVALID_SUPPORT_QUERY", "工单查询字段无效"))
			return
		}
	}
	if staff {
		if !a.config.OperationsEnabled {
			writeError(w, failure(409, "OPERATIONS_DISABLED", "当前环境未启用客服操作"))
			return
		}
		if len(q["workspace_id"]) != 1 || !validOperationsID(q.Get("workspace_id"), "ws") {
			writeError(w, operationsDenied())
			return
		}
		workspace = q.Get("workspace_id")
	}
	var result any
	if receipt {
		op := q.Get("operation")
		if len(q["operation"]) != 1 || !validSupportOp(op) || (op == "reply") != staff {
			writeError(w, failure(400, "INVALID_SUPPORT_OPERATION", "请选择当前入口对应的工单操作"))
			return
		}
		key, e := idempotencyKey(r)
		if e != nil {
			writeError(w, e)
			return
		}
		result, err = a.supportTicketReceipt(p, workspace, op, key)
	} else if id := r.PathValue("ticket"); id != "" {
		result, err = a.supportTicket(p, workspace, id, staff)
	} else {
		result, err = a.supportTickets(p, workspace, staff, q.Get("cursor"))
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func writeSupportTicket(w http.ResponseWriter, result SupportTicketDetail, replay bool, err error, status int) {
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
		status = 200
	}
	writeJSON(w, status, result)
}

func (a *App) supportTicketCreateHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.supportHTTPPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in SupportTicketCreate
	if err = decodeSupportTicketInput(w, r, &in, "category", "subject", "body"); err != nil {
		writeError(w, err)
		return
	}
	result, replay, err := a.createSupportTicket(p, key, in)
	writeSupportTicket(w, result, replay, err, 201)
}

func (a *App) supportTicketOwnerActionHTTP(w http.ResponseWriter, r *http.Request, op string) {
	p, err := a.supportHTTPPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	fields := []string{"expected_version"}
	if op == "followup" {
		fields = append(fields, "body")
	}
	var in SupportTicketAction
	if err = decodeSupportTicketInput(w, r, &in, fields...); err != nil {
		writeError(w, err)
		return
	}
	result, replay, err := a.actOnSupportTicket(p, p.Workspace.ID, r.PathValue("ticket"), op, key, in)
	writeSupportTicket(w, result, replay, err, 200)
}

func (a *App) supportTicketReplyHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		Password    string `json:"password"`
		SupportTicketAction
	}
	if err = decodeSupportTicketInput(w, r, &in, "workspace_id", "expected_version", "body", "password"); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, "support_ticket")
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
	// The short-lived proof is checked with the current credential, session and
	// grant in the transaction that writes the reply or replays its receipt.
	result, replay, err := a.actOnSupportTicket(p, in.WorkspaceID, r.PathValue("ticket"), "reply", key, in.SupportTicketAction, proof)
	writeSupportTicket(w, result, replay, err, 200)
}
