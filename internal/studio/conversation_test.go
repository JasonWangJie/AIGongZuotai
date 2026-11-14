package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type conversationChatFixture struct {
	output string
	hold   bool
	nul    bool
	calls  chan ChatRequest
}

func (p *conversationChatFixture) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	return p.StreamMessages(ctx, "", ChatRequest{Messages: []ChatMessage{{Role: "user", Content: prompt}}}, emit)
}
func (p *conversationChatFixture) StreamMessages(ctx context.Context, _ string, request ChatRequest, emit func(string) error) error {
	if p.calls != nil {
		copy := request
		copy.Messages = append([]ChatMessage(nil), request.Messages...)
		p.calls <- copy
	}
	if p.output != "" {
		if err := emit(p.output); err != nil {
			return err
		}
	}
	if p.nul {
		return emit("bad\x00text")
	}
	if p.hold {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func conversationInput(version uint64, parent *string, text string) ConversationRunInput {
	return ConversationRunInput{ExpectedVersion: &version, ModelID: "studio-chat", Operation: "reply", ParentMessageID: parent, Input: text}
}
func memoryConversation(t *testing.T, a *App) Conversation {
	t.Helper()
	c, _, err := a.createConversation(ConversationInput{ProjectID: "prj_demo", Title: "Fixture conversation"}, newID("key"), "ws_demo", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func waitConversationRun(t *testing.T, a *App, id string) Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		r := a.runs[id]
		var result Run
		if r != nil {
			result = copyRun(r.Run)
		}
		a.mu.Unlock()
		if terminal(result.Status) {
			return result
		}
		if time.Now().After(deadline) {
			t.Fatal("conversation run did not terminate")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func requireOperation(t *testing.T, err error, code string) {
	t.Helper()
	var op *operationError
	if !errors.As(err, &op) || op.code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestConversationContextTrimsTurnsAndProtectsContinue(t *testing.T) {
	model := catalogEntry{UpstreamModel: "private-model", ChatOutputLimitField: "max_tokens", Model: Model{Capabilities: ModelCapabilities{ContextWindowTokens: 2048, MaxOutputTokens: 256}}}
	first, second := "run_1", "run_2"
	path := []chatNode{{MessageNode: MessageNode{ID: "a2", Role: "assistant", RunID: &second}}, {MessageNode: MessageNode{ID: "u2", Role: "user", Content: strings.Repeat("近", 30)}}, {MessageNode: MessageNode{ID: "a1", Role: "assistant", RunID: &first}}, {MessageNode: MessageNode{ID: "u1", Role: "user", Content: strings.Repeat("旧", 30)}}}
	records := map[string]Run{first: {Status: "COMPLETED", Output: strings.Repeat("x", 800)}, second: {Status: "CANCELLED", Output: strings.Repeat("y", 800)}}
	load := func(id string) (Run, error) { return records[id], nil }
	execution, err := assembleChatContext(path, "现在的问题", false, model, load)
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.Request.Messages) != 3 || execution.Summary.OmittedMessages != 2 || execution.Request.Messages[0].Content != path[1].Content {
		t.Fatal("context did not trim complete oldest turn", execution.Summary)
	}
	records[second] = Run{Status: "INTERRUPTED", Output: strings.Repeat("y", 1800)}
	_, err = assembleChatContext(path, conversationContinuePrompt, true, model, load)
	requireOperation(t, err, "CONTEXT_TOO_LARGE")
	records[second] = Run{Status: "CANCELLED", Output: ""}
	execution, err = assembleChatContext(path, "空答之后继续提问", false, model, load)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Summary.OmittedMessages != 2 || len(execution.Request.Messages) != 3 || execution.Request.Messages[0].Content != path[3].Content {
		t.Fatal("empty assistant turn was not omitted as a pair")
	}
}

func TestConversationMemoryBranchReplayAndCapacityPreserveHistory(t *testing.T) {
	app, server := testApp(t, Config{MaxRecords: 2})
	provider := &conversationChatFixture{output: "原始答复", calls: make(chan ChatRequest, 4)}
	app.chat = provider
	c := memoryConversation(t, app)
	input := conversationInput(1, nil, "原始问题")
	first, _, err := app.createConversationRun("ws_demo", "prj_demo", c.ID, "first-chat-key", "", input)
	if err != nil {
		t.Fatal(err)
	}
	waitConversationRun(t, app, first.ID)
	edited := conversationInput(2, nil, "编辑的问题")
	edited.EditedMessageID = &first.UserMessageID
	branch, _, err := app.createConversationRun("ws_demo", "prj_demo", c.ID, "edit-chat-key", "", edited)
	if err != nil {
		t.Fatal(err)
	}
	waitConversationRun(t, app, branch.ID)
	oldModel := app.catalog.Entries["studio-chat"]
	disabled := oldModel
	disabled.Model.Enabled = false
	app.catalog.Entries["studio-chat"] = disabled
	replay, yes, err := app.createConversationRun("ws_demo", "prj_demo", c.ID, "first-chat-key", "", input)
	if err != nil || !yes || replay.ID != first.ID {
		t.Fatal("original request did not replay before version/model checks", err)
	}
	app.catalog.Entries["studio-chat"] = oldModel
	_, _, err = app.createConversationRun("ws_demo", "prj_demo", c.ID, "third-chat-key", "", conversationInput(3, &branch.AssistantMessageID, "超过内存上限"))
	requireOperation(t, err, "RUN_QUEUE_FULL")
	code, data, _ := request(t, server, "GET", "/api/v1/conversations/"+c.ID+"/messages?leaf_id="+first.AssistantMessageID, "", nil)
	if code != 200 {
		t.Fatal("old branch disappeared", code, string(data))
	}
	page := decode[conversationMessagePage](t, data)
	if len(page.Items) != 2 || page.Items[0].Content != "原始问题" || page.Items[1].Content != "原始答复" {
		t.Fatal("editing mutated original tree")
	}
	code, data, _ = request(t, server, "GET", "/api/v1/conversations/"+c.ID+"/branches", "", nil)
	if code != 200 || !strings.Contains(string(data), first.AssistantMessageID) || !strings.Contains(string(data), branch.AssistantMessageID) {
		t.Fatal("branches missing", code, string(data))
	}
}

func TestConversationEmptyStopCanReplyButCannotContinue(t *testing.T) {
	app, _ := testApp(t, Config{})
	app.chat = &conversationChatFixture{hold: true, calls: make(chan ChatRequest, 4)}
	c := memoryConversation(t, app)
	first, _, err := app.createConversationRun("ws_demo", "prj_demo", c.ID, "empty-chat-key", "", conversationInput(1, nil, "空答测试"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.cancelRun(first.ID); err != nil {
		t.Fatal(err)
	}
	continued := conversationInput(2, &first.AssistantMessageID, "")
	continued.Operation = "continue"
	_, _, err = app.createConversationRun("ws_demo", "prj_demo", c.ID, "empty-continue-key", "", continued)
	requireOperation(t, err, "CHAT_CONTINUE_UNAVAILABLE")
	app.chat = &conversationChatFixture{output: "新的答复", calls: make(chan ChatRequest, 4)}
	next, _, err := app.createConversationRun("ws_demo", "prj_demo", c.ID, "empty-reply-key", "", conversationInput(2, &first.AssistantMessageID, "新问题"))
	if err != nil {
		t.Fatal(err)
	}
	if next.ContextSummary.OmittedMessages != 2 || next.ContextSummary.IncludedMessages != 1 {
		t.Fatal("empty stopped turn leaked into new request")
	}
	waitConversationRun(t, app, next.ID)
}

type blockingChatResponse struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingChatResponse) Header() http.Header { return w.header }
func (w *blockingChatResponse) WriteHeader(int)     {}
func (w *blockingChatResponse) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(data), nil
}
func TestConversationMessageWireBudgetAndSlowResponseDoesNotHoldStateLock(t *testing.T) {
	app, server := testApp(t, Config{})
	app.chat = &conversationChatFixture{output: strings.Repeat("\x01", maxRunOutputBytes)}
	c := memoryConversation(t, app)
	var parent *string
	version := uint64(1)
	for i := 0; i < 2; i++ {
		run, _, err := app.createConversationRun("ws_demo", "prj_demo", c.ID, newID("key"), "", conversationInput(version, parent, "预算测试"))
		if err != nil {
			t.Fatal(err)
		}
		waitConversationRun(t, app, run.ID)
		parent = &run.AssistantMessageID
		version++
	}
	path := "/api/v1/conversations/" + c.ID + "/messages?leaf_id=" + *parent + "&limit=40"
	code, data, _ := request(t, server, "GET", path, "", nil)
	if code != 200 || len(data) > 1<<20 {
		t.Fatal("escaped wire body exceeded budget", code, len(data))
	}
	page := decode[conversationMessagePage](t, data)
	if !page.HasMore || page.NextCursor == "" {
		t.Fatal("large messages did not paginate")
	}
	code, older, _ := request(t, server, "GET", path+"&before="+url.QueryEscape(page.NextCursor), "", nil)
	if code != 200 || len(older) > 1<<20 {
		t.Fatal("second page invalid", code)
	}
	writer := &blockingChatResponse{header: make(http.Header), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	req := httptest.NewRequest("GET", "http://127.0.0.1"+path, nil)
	go func() { app.Handler().ServeHTTP(writer, req); close(done) }()
	<-writer.entered
	unlocked := make(chan struct{})
	go func() { app.mu.Lock(); app.mu.Unlock(); close(unlocked) }()
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		close(writer.release)
		t.Fatal("slow client held global state lock")
	}
	close(writer.release)
	<-done
}

func conversationPGFixture(t *testing.T) (*App, *httptest.Server, *http.Client, testIdentity, string) {
	t.Helper()
	databaseURL := testDatabase(t)
	app, err := New(Config{DatabaseURL: databaseURL, AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	app.chat = &conversationChatFixture{output: "持久回复", calls: make(chan ChatRequest, 20)}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { server.Close(); app.Close() })
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "conversation-fixture@example.test")
	return app, server, client, identity, databaseURL
}
func createPGConversation(t *testing.T, client *http.Client, server *httptest.Server, p testIdentity) Conversation {
	t.Helper()
	code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations", newID("key"), p.Project.ID, ConversationInput{ProjectID: p.Project.ID, Title: "持久对话"})
	if code != 201 {
		t.Fatal("conversation create", code, string(data))
	}
	return decode[Conversation](t, data)
}

func TestPostgresConversationScopeFrozenContextAndImmutableTree(t *testing.T) {
	app, server, client, identity, databaseURL := conversationPGFixture(t)
	c := createPGConversation(t, client, server, identity)
	body := conversationInput(1, nil, "记住星舟")
	code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", "persistent-turn-key", identity.Project.ID, body)
	if code != 201 {
		t.Fatal(code, string(data))
	}
	first := decode[Run](t, data)
	waitConversationRun(t, app, first.ID)
	secondBody := conversationInput(2, &first.AssistantMessageID, "上一轮说了什么")
	code, data, _ = authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", "persistent-second-key", identity.Project.ID, secondBody)
	if code != 201 {
		t.Fatal(code, string(data))
	}
	second := decode[Run](t, data)
	waitConversationRun(t, app, second.ID)
	admin, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var snapshot []byte
	if err = admin.QueryRow(context.Background(), "SELECT snapshot FROM studio_chat_contexts WHERE run_id=$1", second.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var execution chatExecution
	if json.Unmarshal(snapshot, &execution) != nil || len(execution.Request.Messages) != 3 || execution.Request.Messages[0].Content != "记住星舟" || execution.Request.Messages[1].Content != "持久回复" {
		t.Fatal("frozen execution omitted prior turn")
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_chat_messages SET content='mutated' WHERE id=$1", first.UserMessageID); err == nil {
		t.Fatal("user message is mutable")
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_chat_contexts SET snapshot='{}' WHERE run_id=$1", second.ID); err == nil {
		t.Fatal("execution context is mutable")
	}
	foreign := authClient(t)
	other := registerFixture(t, foreign, server.URL, "conversation-other@example.test")
	code, _, _ = authRequest(t, foreign, server.URL, "GET", "/api/v1/conversations/"+c.ID, "", other.Project.ID, nil)
	if code != 404 {
		t.Fatal("conversation crossed account", code)
	}
	code, _, _ = authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", "persistent-second-key", identity.Project.ID, secondBody)
	if code != 201 {
		t.Fatal("old accepted input did not replay after CAS change", code)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_chat_messages WHERE conversation_id=$1", c.ID) != 4 {
		t.Fatal("replay duplicated nodes")
	}
	code, data, _ = authRequest(t, client, server.URL, "GET", "/api/v1/conversations/"+c.ID+"/messages?limit=1", "", identity.Project.ID, nil)
	if code != 200 {
		t.Fatal(code)
	}
	page := decode[conversationMessagePage](t, data)
	foreignConv := createPGConversation(t, foreign, server, other)
	code, _, _ = authRequest(t, foreign, server.URL, "GET", "/api/v1/conversations/"+foreignConv.ID+"/messages?before="+url.QueryEscape(page.NextCursor), "", other.Project.ID, nil)
	if code != 400 {
		t.Fatal("message cursor crossed scope", code)
	}
}

func TestPostgresConversationInvalidTextDoesNotPoisonDatabase(t *testing.T) {
	app, server, client, identity, _ := conversationPGFixture(t)
	c := createPGConversation(t, client, server, identity)
	empty := ""
	nulID := "msg_\x00"
	for i, input := range []ConversationRunInput{conversationInput(1, &empty, "valid"), conversationInput(1, &nulID, "valid"), conversationInput(1, nil, "bad\x00prompt")} {
		code, _, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", newID("key"), identity.Project.ID, input)
		if code != 422 || app.store.failed.Load() {
			t.Fatalf("invalid input %d reached/poisoned database: %d", i, code)
		}
	}
	for _, path := range []string{"/api/v1/conversations/conv_%00", "/api/v1/conversations/" + c.ID + "/messages?leaf_id=msg_%00"} {
		code, _, _ := authRequest(t, client, server.URL, "GET", path, "", identity.Project.ID, nil)
		if code != 404 || app.store.failed.Load() {
			t.Fatal("invalid URL reached database", code)
		}
	}
	scope := conversationCursor{Version: 1, User: identity.User.ID, Workspace: identity.Workspace.ID, Project: identity.Project.ID, Kind: "messages", Conversation: c.ID, BeforeID: "msg_\x00"}
	code, _, _ := authRequest(t, client, server.URL, "GET", "/api/v1/conversations/"+c.ID+"/messages?before="+encodeChatCursor(scope), "", identity.Project.ID, nil)
	if code != 400 || app.store.failed.Load() {
		t.Fatal("invalid cursor reached database", code)
	}
	app.chat = &conversationChatFixture{output: "保留此前有效文本", nul: true, calls: make(chan ChatRequest, 2)}
	code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", "nul-provider-fixture", identity.Project.ID, conversationInput(1, nil, "合法问题"))
	if code != 201 {
		t.Fatal(code, string(data))
	}
	run := waitConversationRun(t, app, decode[Run](t, data).ID)
	if run.Status != "FAILED" || run.Output != "保留此前有效文本" || app.store.failed.Load() {
		t.Fatal("NUL provider poisoned checkpoint or lost good prefix")
	}
}

func TestPostgresConversationCASAndTransactionalFailure(t *testing.T) {
	app, server, client, identity, databaseURL := conversationPGFixture(t)
	c := createPGConversation(t, client, server, identity)
	app.chat = &conversationChatFixture{hold: true, calls: make(chan ChatRequest, 2)}
	start := make(chan struct{})
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			code, _, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", newID("key"), identity.Project.ID, conversationInput(1, nil, "并发问题"))
			codes <- code
		}()
	}
	close(start)
	a, b := <-codes, <-codes
	if !(a == 201 && b == 409 || a == 409 && b == 201) {
		t.Fatal("CAS allowed competing turns", a, b)
	}
	admin, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	untouched := createPGConversation(t, client, server, identity)
	if _, err = admin.Exec(context.Background(), `CREATE FUNCTION fail_conversation_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='conversation.turn_created' THEN RAISE EXCEPTION 'fixture conversation event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_conversation_event BEFORE INSERT ON studio_outbox_events FOR EACH ROW EXECUTE FUNCTION fail_conversation_event()`); err != nil {
		t.Fatal(err)
	}
	code, _, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+untouched.ID+"/runs", "chat-atomic-rollback", identity.Project.ID, conversationInput(1, nil, "必须全部回滚"))
	if code != 503 {
		t.Fatal("fixture did not fail", code)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_chat_messages WHERE conversation_id=$1", untouched.ID) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_runs WHERE conversation_id=$1", untouched.ID) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_chat_contexts WHERE conversation_id=$1", untouched.ID) != 0 || outboxCount(t, admin, "SELECT version FROM studio_conversations WHERE id=$1", untouched.ID) != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency WHERE scope_key LIKE '%chat-atomic-rollback'") != 0 {
		t.Fatal("failed event left partial conversation acceptance")
	}
}

func TestPostgresConversationUnknownCommitReplaysWholeTurn(t *testing.T) {
	databaseURL := testDatabase(t)
	proxyURL, armed, dropped := commitAckDropProxy(t, databaseURL)
	store, err := openPostgres(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	assetDir, referenceDir := t.TempDir(), t.TempDir()
	app, err := New(Config{AssetDir: assetDir, ReferenceDir: referenceDir})
	if err != nil {
		store.close()
		t.Fatal(err)
	}
	// No background PG dispatcher/checkpointer is launched by this memory
	// fixture, so the next dropped COMMIT is exactly this acceptance transaction.
	app.store = store
	c := memoryConversation(t, app)
	input := conversationInput(1, nil, "仅创建一次的原始请求")
	armed.Store(true)
	_, _, err = app.createConversationRun("ws_demo", "prj_demo", c.ID, "conversation-unknown-key", "", input)
	if err == nil || !store.failed.Load() {
		t.Fatal("lost commit response did not fail closed", err)
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("commit was not acknowledged by database before disconnect")
	}
	app.Close()
	restarted, err := New(Config{DatabaseURL: databaseURL, AssetDir: assetDir, ReferenceDir: referenceDir})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, yes, err := restarted.createConversationRun("ws_demo", "prj_demo", c.ID, "conversation-unknown-key", "", input)
	if err != nil || !yes || replayed.Status != "INTERRUPTED" {
		t.Fatal("unknown acceptance did not recover via original key", err, replayed.Status)
	}
	admin, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if outboxCount(t, admin, "SELECT count(*) FROM studio_chat_messages WHERE conversation_id=$1", c.ID) != 2 || outboxCount(t, admin, "SELECT count(*) FROM studio_chat_contexts WHERE conversation_id=$1", c.ID) != 1 || outboxCount(t, admin, "SELECT version FROM studio_conversations WHERE id=$1", c.ID) != 2 {
		t.Fatal("unknown commit recovery duplicated or split accepted turn")
	}
}
