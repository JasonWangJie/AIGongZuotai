package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type BillingCapabilities struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"`
	Unit           string `json:"unit"`
	Scale          int    `json:"scale"`
	PricingVersion string `json:"pricing_version,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func (a *App) billingCapabilities() BillingCapabilities {
	cap := BillingCapabilities{Mode: "disabled", Unit: LedgerUnit, Scale: LedgerScale, Reason: "billing_disabled"}
	if a.pricing != nil && a.store != nil {
		cap.Enabled = true
		cap.Mode = "credits"
		cap.PricingVersion = a.pricing.Version
		cap.Reason = ""
	}
	return cap
}

func (a *App) billingPrincipal(r *http.Request) (*principal, error) {
	p := principalFrom(r)
	if p == nil {
		return nil, failure(401, "AUTH_REQUIRED", "请先登录")
	}
	if a.pricing == nil || a.store == nil {
		return nil, billingDisabled()
	}
	if p.User == nil {
		return nil, failure(401, "AUTH_REQUIRED", "请先登录")
	}
	return p, nil
}

type billingQuoteInput struct {
	ResourceType      string          `json:"resource_type"`
	RunInput          json.RawMessage `json:"run_input,omitempty"`
	TaskInput         json.RawMessage `json:"task_input,omitempty"`
	ConversationID    string          `json:"conversation_id,omitempty"`
	ConversationInput json.RawMessage `json:"conversation_input,omitempty"`
}

type BillingQuote struct {
	LedgerPriceSnapshot
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	ProjectID      string    `json:"project_id"`
	ResourceType   string    `json:"resource_type"`
	ConversationID string    `json:"conversation_id,omitempty"`
	ModelID        string    `json:"model_id"`
	ModelVersion   string    `json:"model_version"`
	PriceVersion   string    `json:"price_version"`
	ProviderMode   string    `json:"provider_mode"`
	Unit           string    `json:"unit"`
	Scale          int       `json:"scale"`
	ReserveMicros  int64     `json:"reserve_micros,string"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
}

func invalidQuoteInput() error {
	return failure(422, "INVALID_QUOTE_INPUT", "报价需提供一种完整生成请求，且不能包含已有quote_id")
}
func decodeBillingInput(raw []byte, out any, forbidQuote bool) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || validateCatalogJSONKeys(raw) != nil {
		return invalidQuoteInput()
	}
	if forbidQuote {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return invalidQuoteInput()
		}
		for key := range fields {
			if strings.EqualFold(key, "quote_id") {
				return invalidQuoteInput()
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return invalidQuoteInput()
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return invalidQuoteInput()
	}
	return nil
}

func (a *App) postBillingQuoteHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.billingPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var raw json.RawMessage
	if err = decodeJSON(w, r, &raw); err != nil {
		writeError(w, err)
		return
	}
	var in billingQuoteInput
	if err = decodeBillingInput(raw, &in, false); err != nil {
		writeError(w, err)
		return
	}
	count := 0
	for _, v := range []json.RawMessage{in.RunInput, in.TaskInput, in.ConversationInput} {
		if len(v) > 0 {
			count++
		}
	}
	if count != 1 || (len(in.ConversationInput) == 0 && in.ConversationID != "") || (len(in.ConversationInput) > 0 && !validChatID(in.ConversationID, "conv")) {
		writeError(w, invalidQuoteInput())
		return
	}
	if in.ResourceType != "run" && in.ResourceType != "task" && in.ResourceType != "conversation_run" || in.ResourceType == "run" && len(in.RunInput) == 0 || in.ResourceType == "task" && len(in.TaskInput) == 0 || in.ResourceType == "conversation_run" && len(in.ConversationInput) == 0 {
		writeError(w, invalidQuoteInput())
		return
	}
	quote, err := a.createBillingQuote(r, p, in)
	if err != nil {
		if op, ok := err.(*operationError); ok && op.code == "QUOTE_LIMIT" {
			w.Header().Set("Retry-After", "60")
		}
		writeError(w, err)
		return
	}
	writeJSON(w, 201, quote)
}

func (a *App) createBillingQuote(r *http.Request, p *principal, in billingQuoteInput) (BillingQuote, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return BillingQuote{}, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	row := ledgerQuoteRow{ID: newID("quo"), WorkspaceID: p.Workspace.ID, PriceVersion: a.pricing.Version}
	var model catalogEntry
	var err error
	var conversation *ConversationRunInput
	var prompt string
	switch {
	case len(in.RunInput) > 0:
		var input RunInput
		if err = decodeBillingInput(in.RunInput, &input, true); err != nil {
			return BillingQuote{}, err
		}
		row.ProjectID, row.ResourceType, row.RequestHash = input.ProjectID, "run", ledgerRunRequestHash(input)
		model, err = a.resolveModel(input.ModelID, "chat", input.ModelVersion, input.Input)
		if err != nil {
			return BillingQuote{}, err
		}
		request := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: input.Input}}, MaxOutputTokens: model.Model.Capabilities.MaxOutputTokens, OutputLimitField: model.ChatOutputLimitField}
		if err = ValidateChatRequest(request, model.Model.Capabilities.ContextWindowTokens); err != nil {
			return BillingQuote{}, err
		}
		if _, ok := a.chat.(ChatUsageProvider); !ok {
			return BillingQuote{}, failure(503, "CHAT_USAGE_UNSUPPORTED", "当前适配器不支持用量报告")
		}
		row.Price, row.ReserveMicros, err = a.pricing.chatPrice(model.Model)
	case len(in.TaskInput) > 0:
		var input TaskInput
		if err = decodeBillingInput(in.TaskInput, &input, true); err != nil {
			return BillingQuote{}, err
		}
		row.ProjectID, row.ResourceType, row.RequestHash = input.ProjectID, "task", ledgerTaskRequestHash(input)
		operation := input.Operation
		if operation == "" {
			operation = "generate"
		}
		if input.Kind != "image" && input.Kind != "video" || operation != "generate" && operation != "edit" || operation == "edit" && input.Kind != "image" || operation == "generate" && len(input.Inputs.ReferenceAssetIDs) > 0 {
			return BillingQuote{}, failure(422, "INVALID_OPERATION", "任务类型、操作或参考素材不匹配")
		}
		model, err = a.resolveModel(input.ModelID, input.Kind, input.ModelVersion, input.Inputs.Prompt)
		if err != nil {
			return BillingQuote{}, err
		}
		if !a.authorizeProject(p, input.ProjectID) {
			return BillingQuote{}, failure(404, "PROJECT_NOT_FOUND", "项目不存在或不属于当前工作区")
		}
		if operation == "edit" {
			if !model.Model.Capabilities.ImageEditSupported || len(input.Inputs.ReferenceAssetIDs) > model.Model.Capabilities.MaxReferenceImages {
				return BillingQuote{}, failure(422, "IMAGE_EDIT_UNAVAILABLE", "当前模型不支持此参考图编辑")
			}
			if _, err = a.resolveReferencesLocked(p.Workspace.ID, input.ProjectID, input.Inputs.ReferenceAssetIDs); err != nil {
				return BillingQuote{}, err
			}
		}
		parameters, e := resolveTaskParameters(input.Parameters, model.Model)
		if e != nil {
			return BillingQuote{}, e
		}
		if a.media == nil {
			return BillingQuote{}, failure(501, "PROVIDER_NOT_CONFIGURED", "媒体接口尚未配置")
		}
		row.Price, row.ReserveMicros, err = a.pricing.taskPrice(model.Model, operation, parameters)
	default:
		var input ConversationRunInput
		if err = decodeBillingInput(in.ConversationInput, &input, true); err != nil {
			return BillingQuote{}, err
		}
		if input.ExpectedVersion == nil || *input.ExpectedVersion < 1 || *input.ExpectedVersion > 9007199254740991 || input.Operation != "reply" && input.Operation != "continue" || !utf8.ValidString(input.Input) || strings.ContainsRune(input.Input, 0) {
			return BillingQuote{}, failure(422, "INVALID_CHAT_INPUT", "需要有效操作、消息和当前会话版本")
		}
		for _, id := range []*string{input.ParentMessageID, input.EditedMessageID} {
			if id != nil && !validChatID(*id, "msg") {
				return BillingQuote{}, failure(422, "INVALID_CHAT_INPUT", "消息引用无效")
			}
		}
		prompt = input.Input
		if input.Operation == "continue" {
			if input.Input != "" || input.EditedMessageID != nil {
				return BillingQuote{}, failure(422, "INVALID_CHAT_INPUT", "继续不能替换服务端指令")
			}
			prompt = conversationContinuePrompt
		}
		model, err = a.resolveModel(input.ModelID, "chat", input.ModelVersion, prompt)
		if err != nil {
			return BillingQuote{}, err
		}
		if _, ok := a.chat.(ChatUsageProvider); !ok {
			return BillingQuote{}, failure(503, "CHAT_USAGE_UNSUPPORTED", "当前适配器不支持用量报告")
		}
		row.ProjectID, row.ResourceType, row.ConversationID, row.RequestHash = p.Project.ID, "run", in.ConversationID, ledgerConversationRequestHash(in.ConversationID, input)
		row.Price, row.ReserveMicros, err = a.pricing.chatPrice(model.Model)
		conversation = &input
	}
	if err != nil {
		return BillingQuote{}, err
	}
	if !a.authorizeProject(p, row.ProjectID) {
		return BillingQuote{}, failure(404, "PROJECT_NOT_FOUND", "项目不存在或不属于当前工作区")
	}
	if (r.Header.Get("X-Project-ID") != "" || r.URL.Query().Get("project_id") != "") && p.Project.ID != row.ProjectID {
		return BillingQuote{}, failure(400, "PROJECT_CONFLICT", "报价项目与请求作用域不一致")
	}
	row.ModelID, row.ModelVersion = model.Model.ID, model.Model.Version
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if conversation != nil {
			c, e := readConversationSQL(ctx, tx, p.Workspace.ID, row.ProjectID, in.ConversationID, false)
			if e != nil {
				return e
			}
			path, e := readChatPathSQL(ctx, tx, p.Workspace.ID, row.ProjectID, in.ConversationID, stringID(conversation.ParentMessageID))
			if e != nil {
				return e
			}
			runs, e := readChatRunMapSQL(ctx, tx, p.Workspace.ID, row.ProjectID, path)
			if e != nil {
				return e
			}
			load := func(id string) (Run, error) {
				run, ok := runs[id]
				if !ok {
					return Run{}, conversationNotFound()
				}
				return run, nil
			}
			edit := func(id string) (chatNode, error) {
				return readChatNodeSQL(ctx, tx, p.Workspace.ID, row.ProjectID, in.ConversationID, id)
			}
			if _, _, _, _, e = prepareConversationTurn(c, *conversation, prompt, model, path, load, edit); e != nil {
				return e
			}
		}
		if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&row.CreatedAt); e != nil {
			return e
		}
		row.ExpiresAt = row.CreatedAt.Add(10 * time.Minute)
		if e = insertLedgerQuoteSQL(ctx, tx, row); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return BillingQuote{}, err
	}
	return BillingQuote{LedgerPriceSnapshot: row.Price, ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, ResourceType: row.ResourceType, ConversationID: row.ConversationID, ModelID: row.ModelID, ModelVersion: row.ModelVersion, PriceVersion: row.PriceVersion, ProviderMode: a.config.Mode, Unit: LedgerUnit, Scale: LedgerScale, ReserveMicros: row.ReserveMicros, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt}, nil
}

func (a *App) billingWalletHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.billingPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var wallet LedgerWallet
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var e error
		wallet, e = readLedgerWalletSQL(ctx, conn, p.Workspace.ID)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, wallet)
}

type billingCursor struct {
	Version                                          int `json:"v"`
	User, Workspace, Project, Page, Filter, BeforeID string
	CreatedAt                                        time.Time
}

func encodeBillingCursor(c billingCursor) string {
	blob, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(blob)
}
func parseBillingCursor(raw string, scope billingCursor) (billingCursor, error) {
	bad := func() (billingCursor, error) {
		return scope, failure(400, "INVALID_BILLING_CURSOR", "账务游标无效或不属于当前账户、作用域和筛选")
	}
	if raw == "" {
		return scope, nil
	}
	if len(raw) > 1024 {
		return bad()
	}
	blob, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return bad()
	}
	var c billingCursor
	d := json.NewDecoder(bytes.NewReader(blob))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return bad()
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return bad()
	}
	if c.Version != 1 || c.User != scope.User || c.Workspace != scope.Workspace || c.Project != scope.Project || c.Page != scope.Page || c.Filter != scope.Filter || c.CreatedAt.IsZero() {
		return bad()
	}
	prefix := "jrn_"
	if c.Page == "reservations" {
		prefix = "res_"
	}
	if !strings.HasPrefix(c.BeforeID, prefix) || len(c.BeforeID) != len(prefix)+32 {
		return bad()
	}
	for _, ch := range c.BeforeID[len(prefix):] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return bad()
		}
	}
	return c, nil
}

func (a *App) billingPageScope(r *http.Request, page string) (*principal, int, billingCursor, error) {
	p, err := a.billingPrincipal(r)
	if err != nil {
		return nil, 0, billingCursor{}, err
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 50 {
			return nil, 0, billingCursor{}, failure(400, "INVALID_LIMIT", "分页数量必须为1至50")
		}
		limit = v
	}
	project := r.URL.Query().Get("project_id")
	if project != "" && !a.authorizeProject(p, project) {
		return nil, 0, billingCursor{}, failure(404, "PROJECT_NOT_FOUND", "项目不存在或不属于当前工作区")
	}
	key, allowed := "kind", []string{"CREDIT", "RESERVE", "CHARGE", "RELEASE", "REFUND", "REFUND_RELEASE"}
	if page == "reservations" {
		key, allowed = "state", []string{"HELD", "REVIEW", "SETTLED", "RELEASED", "REFUNDED"}
	}
	filter := r.URL.Query().Get(key)
	if filter != "" {
		found := false
		for _, v := range allowed {
			found = found || filter == v
		}
		if !found {
			return nil, 0, billingCursor{}, failure(400, "INVALID_BILLING_FILTER", "账务筛选值无效")
		}
	}
	scope := billingCursor{Version: 1, User: p.User.ID, Workspace: p.Workspace.ID, Project: project, Page: page, Filter: filter}
	cursor, err := parseBillingCursor(r.URL.Query().Get("cursor"), scope)
	return p, limit, cursor, err
}

func (a *App) billingEntriesHTTP(w http.ResponseWriter, r *http.Request) {
	p, limit, cursor, err := a.billingPageScope(r, "entries")
	if err != nil {
		writeError(w, err)
		return
	}
	var items []LedgerEntry
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var e error
		items, e = listLedgerEntriesSQL(ctx, conn, p.Workspace.ID, cursor.Project, cursor.Filter, limit+1, cursor.CreatedAt, cursor.BeforeID)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.BeforeID, cursor.CreatedAt = last.ID, last.CreatedAt
		encoded := encodeBillingCursor(cursor)
		next = &encoded
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (a *App) billingReservationsHTTP(w http.ResponseWriter, r *http.Request) {
	p, limit, cursor, err := a.billingPageScope(r, "reservations")
	if err != nil {
		writeError(w, err)
		return
	}
	var items []LedgerReservation
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var e error
		items, e = listLedgerReservationsSQL(ctx, conn, p.Workspace.ID, cursor.Project, cursor.Filter, limit+1, cursor.CreatedAt, cursor.BeforeID)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.BeforeID, cursor.CreatedAt = last.ReservationID, last.CreatedAt
		encoded := encodeBillingCursor(cursor)
		next = &encoded
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
