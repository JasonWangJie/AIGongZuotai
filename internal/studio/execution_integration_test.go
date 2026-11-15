package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Calls are held by the test, so assertions observe actual provider admission
// rather than relying on token delays or scheduler polling intervals.
type executionIntegrationCall struct {
	prompt, model string
	request       ChatRequest
	release       chan struct{}
	once          sync.Once
}

func (c *executionIntegrationCall) finish() { c.once.Do(func() { close(c.release) }) }

type executionIntegrationProvider struct {
	calls        chan *executionIntegrationCall
	ignoreCancel bool
	stop         chan struct{}
}

func (p *executionIntegrationProvider) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	return p.stream(ctx, "", ChatRequest{Messages: []ChatMessage{{Role: "user", Content: prompt}}}, emit)
}
func (p *executionIntegrationProvider) StreamModel(ctx context.Context, model, prompt string, emit func(string) error) error {
	return p.stream(ctx, model, ChatRequest{Messages: []ChatMessage{{Role: "user", Content: prompt}}}, emit)
}
func (p *executionIntegrationProvider) StreamMessages(ctx context.Context, model string, request ChatRequest, emit func(string) error) error {
	return p.stream(ctx, model, request, emit)
}
func (p *executionIntegrationProvider) StreamMessagesUsage(ctx context.Context, model string, request ChatRequest, emit func(string) error) (ChatUsage, error) {
	err := p.stream(ctx, model, request, emit)
	return ChatUsage{Status: "demo", Source: "demo", PromptTokens: 128, CompletionTokens: 64, TotalTokens: 192, Complete: err == nil}, err
}
func (p *executionIntegrationProvider) stream(ctx context.Context, model string, request ChatRequest, emit func(string) error) error {
	call := &executionIntegrationCall{prompt: request.Messages[len(request.Messages)-1].Content, model: model, request: request, release: make(chan struct{})}
	select {
	case p.calls <- call:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.ignoreCancel {
		select {
		case <-call.release:
		case <-p.stop:
			return context.Canceled
		}
	} else {
		select {
		case <-call.release:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.stop:
			return context.Canceled
		}
	}
	return emit("completed: " + call.prompt)
}

func executionIntegrationFixture(t *testing.T, c Config, ignoreCancel bool) (*App, *httptest.Server, *executionIntegrationProvider) {
	t.Helper()
	c.DatabaseURL = testDatabase(t)
	c.AuthMode = "required"
	c.PollInterval = time.Hour // Media workers are unrelated to these chat tests.
	a, server := testApp(t, c)
	provider := &executionIntegrationProvider{calls: make(chan *executionIntegrationCall, 32), ignoreCancel: ignoreCancel, stop: make(chan struct{})}
	a.chat = provider
	// Registered after testApp's cleanup, so even a failed assertion unblocks
	// an intentionally uncooperative provider before App.Close waits for it.
	t.Cleanup(func() { close(provider.stop) })
	return a, server, provider
}
func executionStarted(t *testing.T, p *executionIntegrationProvider, prompt string) *executionIntegrationCall {
	t.Helper()
	select {
	case call := <-p.calls:
		if call.prompt != prompt {
			t.Fatalf("provider started %q, expected %q", call.prompt, prompt)
		}
		return call
	case <-time.After(5 * time.Second):
		t.Fatalf("provider did not start %q", prompt)
		return nil
	}
}
func executionState(t *testing.T, a *App, queued, running int) ExecutionSchedulerSnapshot {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		a.mu.Lock()
		snapshot, wake := a.execution.snapshotLocked(time.Now()), a.execution.wake
		a.mu.Unlock()
		if snapshot.Queued == queued && snapshot.Running == running {
			return snapshot
		}
		select {
		case <-wake:
		case <-timeout.C:
			t.Fatalf("scheduler queued/running=%d/%d, expected %d/%d", snapshot.Queued, snapshot.Running, queued, running)
		}
	}
}
func executionRun(t *testing.T, server *httptest.Server, client *http.Client, p testIdentity, model, prompt, key string) Run {
	t.Helper()
	code, raw, _ := authRequest(t, client, server.URL, "POST", "/api/v1/runs", key, p.Project.ID, RunInput{ProjectID: p.Project.ID, ModelID: model, Input: prompt})
	if code != 201 {
		t.Fatalf("create run: %d %s", code, raw)
	}
	return decode[Run](t, raw)
}
func executionCancel(t *testing.T, server *httptest.Server, client *http.Client, p testIdentity, runID string) {
	t.Helper()
	code, raw, _ := authRequest(t, client, server.URL, "POST", "/api/v1/runs/"+runID+"/cancel", "", p.Project.ID, nil)
	if code != 200 || decode[Run](t, raw).Status != "CANCELLED" {
		t.Fatalf("cancel run: %d %s", code, raw)
	}
}

func TestPostgresExecutionSchedulerFairAcrossWorkspaces(t *testing.T) {
	a, server, provider := executionIntegrationFixture(t, Config{ChatScheduling: ChatSchedulingConfig{Workers: 2, QueueSize: 8, WorkspaceConcurrency: 1, ModelConcurrency: 2, WorkspaceQueueSize: 4}}, false)
	clientA, clientB := authClient(t), authClient(t)
	ownerA := registerFixture(t, clientA, server.URL, "execution-a@example.test")
	ownerB := registerFixture(t, clientB, server.URL, "execution-b@example.test")
	a1 := executionRun(t, server, clientA, ownerA, "studio-chat", "a-first", "fair-a-first")
	executionStarted(t, provider, "a-first")
	a2 := executionRun(t, server, clientA, ownerA, "studio-chat", "a-second", "fair-a-second")
	a3 := executionRun(t, server, clientA, ownerA, "studio-chat", "a-cancel-queued", "fair-a-third")
	b1 := executionRun(t, server, clientB, ownerB, "studio-chat", "b-first", "fair-b-first")
	secondWorkspace := executionStarted(t, provider, "b-first")
	executionState(t, a, 2, 2)
	executionCancel(t, server, clientA, ownerA, a1.ID)
	secondA := executionStarted(t, provider, "a-second")
	executionCancel(t, server, clientA, ownerA, a3.ID)
	executionState(t, a, 0, 2)
	a4 := executionRun(t, server, clientA, ownerA, "studio-chat", "a-after-cancel", "fair-a-fourth")
	executionState(t, a, 1, 2)
	secondA.finish()
	lastA := executionStarted(t, provider, "a-after-cancel")
	lastA.finish()
	secondWorkspace.finish()
	executionState(t, a, 0, 0)
	for _, id := range []string{a2.ID, a4.ID, b1.ID} {
		if got := waitConversationRun(t, a, id); got.Status != "COMPLETED" {
			t.Fatal("accepted run did not complete", got.Status)
		}
	}
	if len(provider.calls) != 0 {
		t.Fatal("cancelled queued run reached provider")
	}
}

func TestPostgresExecutionSchedulerSharedModelAndConversation(t *testing.T) {
	catalog := defaultCatalogConfig(Config{Mode: "demo"})
	base := catalog.Models[0]
	base.ID, base.UpstreamModel = "shared-chat-one", "fixture-shared-upstream"
	alias := base
	alias.ID = "shared-chat-two"
	other := base
	other.ID, other.UpstreamModel = "other-chat", "fixture-other-upstream"
	catalog.Models = []catalogConfigModel{base, alias, other}
	a, server, provider := executionIntegrationFixture(t, Config{ModelCatalogFile: writeCatalogFixture(t, catalog), ChatScheduling: ChatSchedulingConfig{Workers: 2, QueueSize: 8, WorkspaceConcurrency: 2, ModelConcurrency: 1, WorkspaceQueueSize: 4}}, false)
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "execution-model@example.test")
	first := executionRun(t, server, client, identity, base.ID, "shared-running", "shared-model-first")
	firstCall := executionStarted(t, provider, "shared-running")
	conversation := createPGConversation(t, client, server, identity)
	turn := conversationInput(conversation.Version, nil, "shared-conversation-waiting")
	turn.ModelID = alias.ID
	code, raw, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+conversation.ID+"/runs", "shared-model-conversation", identity.Project.ID, turn)
	if code != 201 {
		t.Fatal("conversation acceptance", code, string(raw))
	}
	conversationRun := decode[Run](t, raw)
	third := executionRun(t, server, client, identity, other.ID, "different-model-bypasses", "other-model-third")
	otherCall := executionStarted(t, provider, "different-model-bypasses")
	if firstCall.model != base.UpstreamModel || otherCall.model != other.UpstreamModel {
		t.Fatal("dispatch used public IDs instead of frozen upstream bindings")
	}
	executionState(t, a, 1, 2)
	// All three requests are already accepted. Changing only the current
	// catalog's private alias must not reroute the queued execution snapshot.
	a.mu.Lock()
	changed := a.catalog.Entries[alias.ID]
	changed.UpstreamModel = "changed-after-acceptance"
	a.catalog.Entries[alias.ID] = changed
	a.mu.Unlock()
	otherCall.finish()
	executionState(t, a, 1, 1)
	firstCall.finish()
	aliasCall := executionStarted(t, provider, "shared-conversation-waiting")
	if aliasCall.model != base.UpstreamModel || len(aliasCall.request.Messages) != 1 || aliasCall.request.Messages[0].Role != "user" {
		t.Fatal("conversation execution lost frozen context or binding")
	}
	aliasCall.finish()
	executionState(t, a, 0, 0)
	for _, id := range []string{first.ID, third.ID, conversationRun.ID} {
		if got := waitConversationRun(t, a, id); got.Status != "COMPLETED" {
			t.Fatal("model-limited run did not complete", got.Status)
		}
	}
}

func TestPostgresExecutionSchedulerCapacityReplayAndCancel(t *testing.T) {
	for _, test := range []struct {
		name   string
		queue  int
		status int
		code   string
	}{
		{name: "workspace", queue: 4, status: 429, code: "EXECUTION_WORKSPACE_QUEUE_LIMIT"},
		{name: "global", queue: 1, status: 503, code: "RUN_QUEUE_FULL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, server, provider := executionIntegrationFixture(t, Config{ChatScheduling: ChatSchedulingConfig{Workers: 1, QueueSize: test.queue, WorkspaceConcurrency: 1, ModelConcurrency: 1, WorkspaceQueueSize: 1}}, true)
			client := authClient(t)
			identity := registerFixture(t, client, server.URL, "execution-capacity@example.test")
			running := executionRun(t, server, client, identity, "studio-chat", "capacity-running", "capacity-running-key")
			runningCall := executionStarted(t, provider, "capacity-running")
			queued := executionRun(t, server, client, identity, "studio-chat", "capacity-queued", "capacity-queued-key")
			executionState(t, a, 1, 1)
			body := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "capacity-queued"}
			code, raw, header := authRequest(t, client, server.URL, "POST", "/api/v1/runs", "capacity-queued-key", identity.Project.ID, body)
			if code != 201 || header.Get("Idempotency-Replayed") != "true" || decode[Run](t, raw).ID != queued.ID {
				t.Fatal("full queue blocked original replay", code, string(raw))
			}
			body.Input = "different body"
			code, raw, _ = authRequest(t, client, server.URL, "POST", "/api/v1/runs", "capacity-queued-key", identity.Project.ID, body)
			if code != 409 || decode[struct {
				Error APIError `json:"error"`
			}](t, raw).Error.Code != "IDEMPOTENCY_CONFLICT" {
				t.Fatal("full queue hid idempotency conflict", code, string(raw))
			}
			body.Input = "capacity-next"
			code, raw, header = authRequest(t, client, server.URL, "POST", "/api/v1/runs", "capacity-new-key", identity.Project.ID, body)
			if code != test.status || header.Get("Retry-After") != "3" || decode[struct {
				Error APIError `json:"error"`
			}](t, raw).Error.Code != test.code {
				t.Fatal("wrong queue rejection", code, string(raw))
			}
			if err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
				if outboxCount(t, conn, "SELECT count(*) FROM studio_runs WHERE workspace_id=$1", identity.Workspace.ID) != 2 || outboxCount(t, conn, "SELECT count(*) FROM studio_idempotency WHERE scope_key LIKE '%capacity-new-key'") != 0 {
					t.Fatal("queue rejection consumed persistence or idempotency")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			executionCancel(t, server, client, identity, queued.ID)
			executionState(t, a, 0, 1)
			replacement := executionRun(t, server, client, identity, "studio-chat", body.Input, "capacity-new-key")
			executionCancel(t, server, client, identity, running.ID)
			// Cancellation does not free an occupied provider slot while that call
			// has not returned; otherwise a slow adapter could exceed concurrency.
			executionState(t, a, 1, 1)
			runningCall.finish()
			next := executionStarted(t, provider, "capacity-next")
			next.finish()
			executionState(t, a, 0, 0)
			if got := waitConversationRun(t, a, replacement.ID); got.Status != "COMPLETED" {
				t.Fatal("released queue capacity was not reusable", got.Status)
			}
			if got := waitConversationRun(t, a, running.ID); got.Status != "CANCELLED" || got.Output != "" {
				t.Fatal("late provider response revived cancelled run", got.Status)
			}
		})
	}
}

func TestPostgresExecutionSchedulerFailedAcceptanceNeverDispatches(t *testing.T) {
	a, server, provider := executionIntegrationFixture(t, Config{BillingMode: "credits", ModelCatalogFile: filepath.Join("..", "..", "config", "models.example.json"), BillingPriceFile: filepath.Join("..", "..", "config", "prices.demo.example.json"), Moderation: executionModerationConfig(t, nil), ChatScheduling: ChatSchedulingConfig{Workers: 2, QueueSize: 4}}, false)
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "execution-rollback@example.test")
	httpCredit(t, a, identity.Workspace.ID, 1_000_000)
	input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "must never reach provider"}
	quote := httpBillingQuote(t, client, server, identity.Project.ID, "run", input)
	input.QuoteID = quote.ID
	if err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `CREATE FUNCTION reject_execution_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='run.accepted' THEN RAISE EXCEPTION 'fixture audit write failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_execution_acceptance BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_execution_acceptance()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	code, _, _ := authRequest(t, client, server.URL, "POST", "/api/v1/runs", "failed-execution-key", identity.Project.ID, input)
	if code != 503 || !a.store.failed.Load() {
		t.Fatal("transaction fault did not fail closed", code)
	}
	state := executionState(t, a, 0, 0)
	if state.Dispatched != 0 || len(provider.calls) != 0 {
		t.Fatal("rolled-back acceptance reached scheduler or provider")
	}
	admin, err := pgx.Connect(context.Background(), a.config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if outboxCount(t, admin, "SELECT count(*) FROM studio_runs WHERE workspace_id=$1", identity.Workspace.ID) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency WHERE scope_key LIKE '%failed-execution-key'") != 0 {
		t.Fatal("failed transaction left accepted resources")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_run_moderation") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_moderation_decisions") != 0 {
		t.Fatal("failed transaction persisted moderation state")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", quote.ID) != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations WHERE workspace_id=$1", identity.Workspace.ID) != 0 || outboxCount(t, admin, "SELECT balance_micros FROM studio_ledger_accounts WHERE workspace_id=$1 AND kind='available'", identity.Workspace.ID) != 1_000_000 {
		t.Fatal("failed acceptance consumed quote or changed available credit")
	}
}

func TestPostgresExecutionSchedulerFullQueueDoesNotConsumeQuoteOrModeration(t *testing.T) {
	a, server, provider := executionIntegrationFixture(t, Config{BillingMode: "credits", ModelCatalogFile: filepath.Join("..", "..", "config", "models.example.json"), BillingPriceFile: filepath.Join("..", "..", "config", "prices.demo.example.json"), Moderation: executionModerationConfig(t, nil), ChatScheduling: ChatSchedulingConfig{Workers: 1, QueueSize: 1, WorkspaceConcurrency: 1, ModelConcurrency: 1, WorkspaceQueueSize: 1}}, false)
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "execution-quote-capacity@example.test")
	httpCredit(t, a, identity.Workspace.ID, 1_000_000)
	inputs := []RunInput{}
	for _, prompt := range []string{"billed-first", "billed-queued", "billed-rejected"} {
		in := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: prompt}
		in.QuoteID = httpBillingQuote(t, client, server, identity.Project.ID, "run", in).ID
		inputs = append(inputs, in)
	}
	accepted := []Run{}
	var firstCall *executionIntegrationCall
	for i := 0; i < 2; i++ {
		code, raw, _ := authRequest(t, client, server.URL, "POST", "/api/v1/runs", "billed-key-"+inputs[i].Input, identity.Project.ID, inputs[i])
		if code != 201 {
			t.Fatal("billed acceptance", code, string(raw))
		}
		accepted = append(accepted, decode[Run](t, raw))
		if i == 0 {
			firstCall = executionStarted(t, provider, inputs[i].Input)
		}
	}
	executionState(t, a, 1, 1)
	readProjection := func() [6]int {
		t.Helper()
		var state [6]int
		if err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			state = [6]int{
				outboxCount(t, conn, "SELECT count(*) FROM studio_runs"),
				outboxCount(t, conn, "SELECT count(*) FROM studio_ledger_reservations"),
				outboxCount(t, conn, "SELECT balance_micros FROM studio_ledger_accounts WHERE workspace_id=$1 AND kind='available'", identity.Workspace.ID),
				outboxCount(t, conn, "SELECT balance_micros FROM studio_ledger_accounts WHERE workspace_id=$1 AND kind='reserved'", identity.Workspace.ID),
				outboxCount(t, conn, "SELECT count(*) FROM studio_run_moderation"),
				outboxCount(t, conn, "SELECT count(*) FROM studio_moderation_decisions"),
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := readProjection()
	code, raw, headers := authRequest(t, client, server.URL, "POST", "/api/v1/runs", "billed-rejected-key", identity.Project.ID, inputs[2])
	if code != 503 || headers.Get("Retry-After") != "3" || decode[struct {
		Error APIError `json:"error"`
	}](t, raw).Error.Code != "RUN_QUEUE_FULL" {
		t.Fatal("wrong billed queue rejection", code, string(raw))
	}
	if after := readProjection(); after != before || after[0] != 2 || after[1] != 2 || after[4] != 2 || after[5] != 2 {
		t.Fatal("queue refusal changed resources, wallet or moderation", before, after)
	}
	if err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		if outboxCount(t, conn, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", inputs[2].QuoteID) != 1 || outboxCount(t, conn, "SELECT count(*) FROM studio_idempotency WHERE scope_key LIKE '%billed-rejected-key'") != 0 {
			t.Fatal("queue refusal consumed quote or idempotency key")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	firstCall.finish()
	secondCall := executionStarted(t, provider, inputs[1].Input)
	secondCall.finish()
	executionState(t, a, 0, 0)
	for _, run := range accepted {
		got := waitConversationRun(t, a, run.ID)
		if got.Status != "COMPLETED" || got.Moderation == nil || got.Moderation.State != "ALLOWED" || got.Billing == nil || got.Billing.State != "SETTLED" {
			t.Fatal("queue rejection disrupted existing accepted execution", got.Status, got.Moderation, got.Billing)
		}
	}
}
