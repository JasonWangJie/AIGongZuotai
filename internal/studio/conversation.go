package studio

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const conversationNodeLimit = 256
const conversationPathLimit = 128
const conversationContinuePrompt = "请从上一条回复结束处继续，不要重复已有内容。"

var chatIDPattern = regexp.MustCompile(`^(conv|msg)_[a-f0-9]{32}$`)

func validChatID(id, prefix string) bool {
	return strings.HasPrefix(id, prefix+"_") && chatIDPattern.MatchString(id)
}

type Conversation struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"-"`
	ProjectID     string    `json:"project_id"`
	Title         string    `json:"title"`
	Version       uint64    `json:"version"`
	HeadMessageID *string   `json:"head_message_id"`
	ActiveRunID   *string   `json:"active_run_id"`
	NodeCount     int       `json:"node_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type MessageNode struct {
	Moderation     *ModerationSummary `json:"moderation,omitempty"`
	ID             string             `json:"id"`
	ConversationID string             `json:"conversation_id"`
	ParentID       *string            `json:"parent_id"`
	Role           string             `json:"role"`
	Content        string             `json:"content"`
	RunID          *string            `json:"run_id"`
	Status         string             `json:"status"`
	Seq            uint64             `json:"seq"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	ModelID        string             `json:"model_id,omitempty"`
	ModelName      string             `json:"model_name,omitempty"`
	Error          *APIError          `json:"error,omitempty"`
}
type chatNode struct {
	MessageNode
	WorkspaceID, ProjectID string
	EditedFromID           *string
}
type ChatContextSummary struct {
	InputTokensEstimate  int    `json:"input_tokens_estimate"`
	OutputTokensReserved int    `json:"output_tokens_reserved"`
	ContextWindowTokens  int    `json:"context_window_tokens"`
	IncludedMessages     int    `json:"included_messages"`
	OmittedMessages      int    `json:"omitted_messages"`
	Estimator            string `json:"estimator"`
}
type chatExecution struct {
	UpstreamModel    string             `json:"upstream_model"`
	Request          ChatRequest        `json:"request"`
	Summary          ChatContextSummary `json:"summary"`
	SourceMessageIDs []string           `json:"source_message_ids"`
}
type ConversationInput struct {
	ProjectID string `json:"project_id"`
	Title     string `json:"title,omitempty"`
}
type ConversationRunInput struct {
	QuoteID         string  `json:"quote_id,omitempty"`
	ExpectedVersion *uint64 `json:"expected_version"`
	ModelID         string  `json:"model_id"`
	ModelVersion    string  `json:"model_version,omitempty"`
	Operation       string  `json:"operation"`
	ParentMessageID *string `json:"parent_message_id"`
	Input           string  `json:"input,omitempty"`
	EditedMessageID *string `json:"edited_message_id"`
}

func stringID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}
func copyChatID(id *string) *string {
	if id == nil {
		return nil
	}
	value := *id
	return &value
}
func conversationNotFound() error { return failure(404, "NOT_FOUND", "对话或消息不存在") }

func (a *App) createConversation(input ConversationInput, key, workspaceID, actor string) (Conversation, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	index := workspaceID + ":conversations:" + key
	hash := payloadHash(input)
	old, found, err := a.findIdempotencyLocked(index)
	if err != nil {
		return Conversation{}, false, err
	}
	if found {
		if old.Hash != hash {
			return Conversation{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "相同幂等键已经用于其他请求")
		}
		c, e := a.conversationLocked(workspaceID, input.ProjectID, old.ID)
		return c, true, e
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "新对话"
	}
	if !validName(title) || strings.ContainsRune(title, 0) {
		return Conversation{}, false, failure(422, "INVALID_CONVERSATION_TITLE", "对话名称需要1至80字符")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := Conversation{ID: newID("conv"), WorkspaceID: workspaceID, ProjectID: input.ProjectID, Title: title, Version: 1, CreatedAt: now, UpdatedAt: now}
	audit := newAuditEvent(workspaceID, input.ProjectID, actor, "conversation.created", "conversation", c.ID, AuditDetails{Revision: 1})
	if a.store != nil {
		err = a.store.saveWithAudit(func(ctx context.Context, tx pgx.Tx) error {
			var count int
			if e := tx.QueryRow(ctx, "SELECT count(*) FROM studio_conversations WHERE workspace_id=$1", workspaceID).Scan(&count); e != nil {
				return e
			}
			if count >= 1000 {
				return failure(429, "CONVERSATION_LIMIT", "当前工作区对话数量已达上限")
			}
			if _, e := tx.Exec(ctx, "INSERT INTO studio_conversations(id,workspace_id,project_id,title,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5)", c.ID, c.WorkspaceID, c.ProjectID, c.Title, now); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, "INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type) VALUES($1,$2,$3,'conversation')", index, hash[:], c.ID)
			return e
		}, []AuditEvent{audit})
		if err != nil {
			return Conversation{}, false, err
		}
	} else {
		if len(a.conversations) >= 64 {
			return Conversation{}, false, failure(429, "CONVERSATION_LIMIT", "内存演示最多保存64个对话")
		}
		a.conversations[c.ID] = c
		a.idem[index] = idemRecord{Hash: hash, ID: c.ID}
		a.appendMemoryAuditLocked(audit)
	}
	return c, false, nil
}

func (a *App) conversationLocked(workspaceID, projectID, id string) (Conversation, error) {
	if !validChatID(id, "conv") {
		return Conversation{}, conversationNotFound()
	}
	if a.store != nil {
		var c Conversation
		err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var e error
			c, e = readConversationSQL(ctx, conn, workspaceID, projectID, id, false)
			return e
		})
		return c, err
	}
	c, ok := a.conversations[id]
	if !ok || c.WorkspaceID != workspaceID || c.ProjectID != projectID {
		return Conversation{}, conversationNotFound()
	}
	c.ActiveRunID = nil
	c.HeadMessageID = copyChatID(c.HeadMessageID)
	for id, r := range a.runs {
		if r.Run.ConversationID == c.ID && !terminal(r.Run.Status) {
			copy := id
			c.ActiveRunID = &copy
			break
		}
	}
	return c, nil
}

func (a *App) createConversationRun(workspaceID, projectID, id, key, actor string, input ConversationRunInput) (Run, bool, error) {
	return a.createConversationRunContext(a.ctx, workspaceID, projectID, id, key, actor, input)
}

func (a *App) createConversationRunContext(ctx context.Context, workspaceID, projectID, id, key, actor string, input ConversationRunInput) (Run, bool, error) {
	approval := &runModerationAdmission{}
	run, replayed, err := a.createConversationRunApproved(workspaceID, projectID, id, key, actor, input, approval)
	if !isModerationPreparation(err) {
		return run, replayed, err
	}
	approval.check(ctx, a.moderation)
	return a.createConversationRunApproved(workspaceID, projectID, id, key, actor, input, approval)
}

func (a *App) createConversationRunApproved(workspaceID, projectID, id, key, actor string, input ConversationRunInput, approval *runModerationAdmission) (Run, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return Run{}, false, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	index := workspaceID + ":conversation-runs:" + key
	hash := payloadHash(struct {
		ConversationID string
		Input          ConversationRunInput
	}{id, input})
	old, found, err := a.findIdempotencyLocked(index)
	if err != nil {
		return Run{}, false, err
	}
	if found {
		if old.Hash != hash {
			return Run{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "相同幂等键已经用于其他请求")
		}
		r, e := a.getRunLocked(old.ID)
		if e != nil {
			return Run{}, false, e
		}
		if r.Run.WorkspaceID != workspaceID || r.Run.ProjectID != projectID {
			return Run{}, false, conversationNotFound()
		}
		return copyRun(r.Run), true, nil
	}
	if input.ExpectedVersion == nil || *input.ExpectedVersion < 1 || *input.ExpectedVersion > 9007199254740991 || input.Operation != "reply" && input.Operation != "continue" {
		return Run{}, false, failure(422, "INVALID_CHAT_INPUT", "需要明确操作和当前对话版本")
	}
	if !validChatID(id, "conv") {
		return Run{}, false, conversationNotFound()
	}
	if strings.ContainsRune(input.Input, 0) || !utf8.ValidString(input.Input) {
		return Run{}, false, failure(422, "INVALID_CHAT_INPUT", "消息必须是有效UTF-8且不能包含NUL")
	}
	for _, value := range []*string{input.ParentMessageID, input.EditedMessageID} {
		if value != nil && !validChatID(*value, "msg") {
			return Run{}, false, failure(422, "INVALID_CHAT_INPUT", "消息引用必须是有效非空标识或null")
		}
	}
	prompt := input.Input
	if input.Operation == "continue" {
		if input.Input != "" || input.EditedMessageID != nil {
			return Run{}, false, failure(422, "INVALID_CHAT_INPUT", "继续操作不能替换服务端继续指令")
		}
		prompt = conversationContinuePrompt
	}
	model, err := a.resolveModel(input.ModelID, "chat", input.ModelVersion, prompt)
	if err != nil {
		return Run{}, false, err
	}
	if err := a.validateBillingAdmission(input.QuoteID, true); err != nil {
		return Run{}, false, err
	}
	if _, ok := a.chat.(interface {
		StreamMessages(context.Context, string, ChatRequest, func(string) error) error
	}); !ok {
		return Run{}, false, failure(503, "CHAT_CONTEXT_UNSUPPORTED", "当前对话适配器不支持完整上下文，未创建运行")
	}
	if err = a.checkAdmissionLocked(workspaceID, input.ModelID, "chat"); err != nil {
		return Run{}, false, err
	}
	a.evictTerminalsLocked()
	if len(a.runs) >= a.config.MaxRecords || len(a.runQueue) >= cap(a.runQueue) {
		return Run{}, false, failure(503, "RUN_QUEUE_FULL", "生成队列或本地缓存已满，请稍后重试原请求")
	}
	var run Run
	var execution *chatExecution
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			tx, e := conn.Begin(ctx)
			if e != nil {
				return e
			}
			defer tx.Rollback(ctx)
			c, e := readConversationSQL(ctx, tx, workspaceID, projectID, id, true)
			if e != nil {
				return e
			}
			path, e := readChatPathSQL(ctx, tx, workspaceID, projectID, id, stringID(input.ParentMessageID))
			if e != nil {
				return e
			}
			records, e := readChatRunMapSQL(ctx, tx, workspaceID, projectID, path)
			if e != nil {
				return e
			}
			load := func(runID string) (Run, error) {
				r, ok := records[runID]
				if !ok {
					return Run{}, conversationNotFound()
				}
				return r, nil
			}
			edit := func(nodeID string) (chatNode, error) {
				return readChatNodeSQL(ctx, tx, workspaceID, projectID, id, nodeID)
			}
			next, user, assistant, ex, e := prepareConversationTurn(c, input, prompt, model, path, load, edit)
			if e != nil {
				return e
			}
			run = next
			execution = ex
			if a.moderation != nil {
				if e = approval.prepare(ex.Request); e != nil {
					return e
				}
				approval.attach(&run)
			}
			run.ProviderMode = a.config.Mode
			run.BillingQuoteID, run.BillingRequestHash = input.QuoteID, ledgerConversationRequestHash(id, input)
			audit := newAuditEvent(workspaceID, projectID, actor, "run.accepted", "run", run.ID, AuditDetails{ModelID: run.ModelID, ToStatus: run.Status})
			if e = insertBilledRunSQL(ctx, tx, &run, index, hash, []AuditEvent{audit}); e != nil {
				return e
			}
			if e = insertChatNodeSQL(ctx, tx, user); e != nil {
				return e
			}
			if e = insertChatNodeSQL(ctx, tx, assistant); e != nil {
				return e
			}
			blob, _ := json.Marshal(execution)
			if _, e = tx.Exec(ctx, "INSERT INTO studio_chat_contexts(run_id,conversation_id,workspace_id,project_id,snapshot) VALUES($1,$2,$3,$4,$5)", run.ID, id, workspaceID, projectID, blob); e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, "UPDATE studio_conversations SET version=version+1,node_count=node_count+2,head_message_id=$4,updated_at=$5 WHERE id=$1 AND workspace_id=$2 AND version=$3", id, workspaceID, *input.ExpectedVersion, assistant.ID, run.CreatedAt)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return failure(409, "CONVERSATION_VERSION_CONFLICT", "对话已有新版本，请保留输入并重新选择分支")
			}
			if e = insertAuditSQL(ctx, tx, newAuditEvent(workspaceID, projectID, actor, "conversation.turn_created", "conversation", id, AuditDetails{Revision: run.ConversationVersion, NodeCount: c.NodeCount + 2})); e != nil {
				return e
			}
			return tx.Commit(ctx)
		})
	} else {
		c, e := a.conversationLocked(workspaceID, projectID, id)
		if e != nil {
			return Run{}, false, e
		}
		path, e := a.chatPathMemory(c, stringID(input.ParentMessageID))
		if e != nil {
			return Run{}, false, e
		}
		load := func(runID string) (Run, error) {
			r, e := a.getRunLocked(runID)
			if e != nil {
				return Run{}, e
			}
			return r.Run, nil
		}
		edit := func(nodeID string) (chatNode, error) {
			n, ok := a.chatMessages[nodeID]
			if !ok || n.ConversationID != id {
				return chatNode{}, conversationNotFound()
			}
			return n, nil
		}
		next, user, assistant, ex, e := prepareConversationTurn(c, input, prompt, model, path, load, edit)
		if e != nil {
			return Run{}, false, e
		}
		if a.moderation != nil {
			if e = approval.prepare(ex.Request); e != nil {
				return Run{}, false, e
			}
			approval.attach(&next)
		}
		if len(a.chatMessages)+2 > 2048 {
			return Run{}, false, failure(429, "CONVERSATION_LIMIT", "内存消息总量已达上限")
		}
		run, execution = next, ex
		c.Version++
		c.NodeCount += 2
		c.HeadMessageID = &assistant.ID
		c.UpdatedAt = run.CreatedAt
		a.conversations[id] = c
		a.chatMessages[user.ID] = user
		a.chatMessages[assistant.ID] = assistant
		a.idem[index] = idemRecord{Hash: hash, ID: run.ID}
		run.ProviderMode = a.config.Mode
		a.appendMemoryAuditLocked(newAuditEvent(workspaceID, projectID, actor, "run.accepted", "run", run.ID, AuditDetails{ModelID: run.ModelID, ToStatus: run.Status}), newAuditEvent(workspaceID, projectID, actor, "conversation.turn_created", "conversation", id, AuditDetails{Revision: c.Version, NodeCount: c.NodeCount}))
	}
	if err != nil {
		return Run{}, false, err
	}
	a.runs[run.ID] = &runRecord{Run: run, UpstreamModel: execution.UpstreamModel, ChatExecution: execution, Subscribers: make(map[chan event]bool)}
	a.runQueue <- run.ID
	return copyRun(run), false, nil
}

func prepareConversationTurn(c Conversation, input ConversationRunInput, prompt string, model catalogEntry, path []chatNode, load func(string) (Run, error), edit func(string) (chatNode, error)) (Run, chatNode, chatNode, *chatExecution, error) {
	fail := func(err error) (Run, chatNode, chatNode, *chatExecution, error) {
		return Run{}, chatNode{}, chatNode{}, nil, err
	}
	if c.Version != *input.ExpectedVersion {
		return fail(failure(409, "CONVERSATION_VERSION_CONFLICT", "对话已有新版本，请保留输入并重新选择分支"))
	}
	if c.ActiveRunID != nil {
		return fail(failure(409, "CONVERSATION_BUSY", "当前对话已有运行，请等待完成或主动停止"))
	}
	if c.NodeCount+2 > conversationNodeLimit || len(path)+2 > conversationPathLimit {
		return fail(failure(429, "CONVERSATION_LIMIT", "会话节点或当前分支深度已达上限"))
	}
	if stringID(input.ParentMessageID) != "" && (len(path) == 0 || path[0].Role != "assistant") {
		return fail(failure(422, "INVALID_CHAT_PARENT", "父节点必须是本对话已结束的助手回复"))
	}
	if len(path) > 0 {
		parent, err := load(stringID(path[0].RunID))
		if err != nil {
			return fail(err)
		}
		if !terminal(parent.Status) {
			return fail(failure(422, "INVALID_CHAT_PARENT", "父回复仍在生成"))
		}
		if input.Operation == "continue" && (!runOutputReleased(parent) || strings.TrimSpace(parent.Output) == "") {
			return fail(failure(422, "CHAT_CONTINUE_UNAVAILABLE", "没有可继续的已保存回复内容"))
		}
	}
	if input.Operation == "continue" && len(path) == 0 {
		return fail(failure(422, "CHAT_CONTINUE_UNAVAILABLE", "继续操作需要选择一条已结束回复"))
	}
	if input.EditedMessageID != nil {
		node, err := edit(*input.EditedMessageID)
		if err != nil {
			return fail(err)
		}
		if node.Role != "user" || stringID(node.ParentID) != stringID(input.ParentMessageID) {
			return fail(failure(422, "INVALID_CHAT_EDIT", "编辑必须保留原用户节点的父关系并创建兄弟分支"))
		}
	}
	execution, err := assembleChatContext(path, prompt, input.Operation == "continue", model, load)
	if err != nil {
		return fail(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	run := Run{ID: newID("run"), WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, ConversationID: c.ID, ConversationVersion: c.Version + 1, ModelID: model.Model.ID, ModelName: model.Model.Name, ModelVersion: model.Model.Version, ModelCapabilities: copyCapabilities(&model.Model.Capabilities), Prompt: prompt, Status: "QUEUED", CreatedAt: now, UpdatedAt: now, ContextSummary: &execution.Summary}
	user := chatNode{WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, EditedFromID: copyChatID(input.EditedMessageID), MessageNode: MessageNode{ID: newID("msg"), ConversationID: c.ID, ParentID: copyChatID(input.ParentMessageID), Role: "user", Content: prompt, Status: "COMPLETED", CreatedAt: now, UpdatedAt: now}}
	assistant := chatNode{WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, MessageNode: MessageNode{ID: newID("msg"), ConversationID: c.ID, ParentID: &user.ID, Role: "assistant", RunID: &run.ID, Status: "QUEUED", CreatedAt: now, UpdatedAt: now}}
	run.UserMessageID, run.AssistantMessageID = user.ID, assistant.ID
	return run, user, assistant, execution, nil
}

func assembleChatContext(path []chatNode, prompt string, continuing bool, model catalogEntry, load func(string) (Run, error)) (*chatExecution, error) {
	request := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: prompt}}, MaxOutputTokens: model.Model.Capabilities.MaxOutputTokens, OutputLimitField: model.ChatOutputLimitField}
	window := model.Model.Capabilities.ContextWindowTokens
	if err := ValidateChatRequest(request, window); err != nil {
		return nil, err
	}
	included := 0
	ids := []string{}
	for i := 0; i < len(path); i += 2 {
		if i+1 >= len(path) || path[i].Role != "assistant" || path[i+1].Role != "user" {
			return nil, errors.New("invalid persisted conversation ancestry")
		}
		previous, err := load(stringID(path[i].RunID))
		if err != nil {
			return nil, err
		}
		if !terminal(previous.Status) {
			return nil, failure(422, "INVALID_CHAT_PARENT", "祖先回复尚未结束")
		}
		if !runOutputReleased(previous) || previous.Output == "" {
			if continuing && i == 0 {
				return nil, failure(422, "CHAT_CONTINUE_UNAVAILABLE", "没有可继续的回复")
			}
			continue
		}
		trial := request
		trial.Messages = append([]ChatMessage{{Role: "user", Content: path[i+1].Content}, {Role: "assistant", Content: previous.Output}}, request.Messages...)
		if err = ValidateChatRequest(trial, window); err != nil {
			var op *operationError
			if errors.As(err, &op) && (op.code == "CONTEXT_TOO_LARGE" || len(trial.Messages) > MaxChatMessages) {
				if continuing && i == 0 {
					return nil, failure(422, "CONTEXT_TOO_LARGE", "必须保留的继续来源超过模型窗口，请改为新问题或选择更大窗口")
				}
				break
			}
			return nil, err
		}
		request = trial
		included += 2
		ids = append([]string{path[i+1].ID, path[i].ID}, ids...)
	}
	estimate, err := EstimateChatContext(request.Messages)
	if err != nil {
		return nil, err
	}
	return &chatExecution{UpstreamModel: model.UpstreamModel, Request: request, SourceMessageIDs: ids, Summary: ChatContextSummary{InputTokensEstimate: estimate, OutputTokensReserved: request.MaxOutputTokens, ContextWindowTokens: window, IncludedMessages: len(request.Messages), OmittedMessages: len(path) - included, Estimator: "utf8_bytes_v1"}}, nil
}

func (a *App) chatPathMemory(c Conversation, leaf string) ([]chatNode, error) {
	path := []chatNode{}
	for leaf != "" {
		n, ok := a.chatMessages[leaf]
		if !ok || n.ConversationID != c.ID || n.WorkspaceID != c.WorkspaceID || n.ProjectID != c.ProjectID {
			return nil, conversationNotFound()
		}
		path = append(path, n)
		if len(path) > conversationPathLimit {
			return nil, failure(422, "INVALID_CHAT_PARENT", "消息路径超过上限")
		}
		leaf = stringID(n.ParentID)
	}
	return path, nil
}

func hydrateChatNode(n chatNode, load func(string) (Run, error)) (MessageNode, error) {
	m := n.MessageNode
	m.ParentID, m.RunID = copyChatID(m.ParentID), copyChatID(m.RunID)
	if m.Role == "user" {
		return m, nil
	}
	run, err := load(stringID(m.RunID))
	if err != nil {
		return MessageNode{}, err
	}
	run = copyRun(run)
	m.Moderation = copyRunModeration(run.Moderation)
	m.Content, m.Status, m.Seq, m.UpdatedAt, m.ModelID, m.ModelName, m.Error = run.Output, run.Status, run.Seq, run.UpdatedAt, run.ModelID, run.ModelName, run.Error
	return m, nil
}
func previewText(text string) string {
	if utf8.RuneCountInString(text) > 160 {
		return string([]rune(text)[:160])
	}
	return text
}
