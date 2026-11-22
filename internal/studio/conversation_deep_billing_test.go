package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresDeepConversationQuoteModerationAndFrozenRequest036(t *testing.T) {
	if os.Getenv("DEEP_CONVERSATION_TEST") == "" {
		t.Skip("opt-in persistent deep conversation")
	}
	if !libraryLoadLocalDB(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("disposable loopback database required")
	}
	const turns = 65
	ctx := context.Background()
	dsn := testDatabase(t)
	cfg := Config{DatabaseURL: dsn, AuthMode: "required", LongConversationHistory: true, AssetDir: t.TempDir(), ReferenceDir: t.TempDir()}
	var calls atomic.Int32
	cfg.Mode, cfg.ChatModel, cfg.APIKey = "live", "deep-local", newID("fixture")
	gateway := deepHistoryGateway(turns, &calls, cfg.APIKey)
	defer gateway.Close()
	cfg.BaseURL = gateway.URL + "/v1"
	a, server := testApp(t, cfg)
	client := authClient(t)
	owner := registerFixture(t, client, server.URL, "deep-billing@example.test")
	c := createPGConversation(t, client, server, owner)
	var parent *string
	var lastIDs []string
	for n := 1; n <= turns; n++ {
		code, raw, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs",
			newID("key"), owner.Project.ID, conversationInput(uint64(n), parent, deepPrompt(n)))
		if code != 201 {
			t.Fatalf("failed to build legitimate branch at turn %d: %d", n, code)
		}
		got := waitConversationRun(t, a, decode[Run](t, raw).ID)
		if got.Status != "COMPLETED" || got.Output != deepAnswer(n) {
			t.Fatalf("incorrect history at turn %d", n)
		}
		lastIDs = append(lastIDs, got.UserMessageID, got.AssistantMessageID)
		parent = &got.AssistantMessageID
	}
	server.Close()
	a.Close()
	// Use the real offline preview and exact apply while the control writer
	// is stopped. No fixture SQL creates balances, histories or reservations.
	credit := LedgerAdminConfig{DatabaseURL: dsn, Action: "grant", Credit: LedgerCreditRequest{WorkspaceID: owner.Workspace.ID,
		OperationKey: "deep-fixture-local-credit", AmountMicros: 1_000_000, Reason: "local-fixture", EvidenceRef: "fixture:deep-history"}}
	plan, err := AdministerLedger(ctx, credit)
	if err != nil {
		t.Fatal(err)
	}
	credit.Apply, credit.PlanDigest = true, plan.PlanDigest
	if _, err := AdministerLedger(ctx, credit); err != nil {
		t.Fatal(err)
	}
	checked := make(chan []ChatMessage, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var outputs atomic.Int32
	moderator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in moderationHTTPRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if in.Phase == "input" {
			select {
			case checked <- in.Messages:
			case <-r.Context().Done():
				return
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		} else if in.Phase == "output" {
			outputs.Add(1)
		}
		allowExecutionHTTP(w, in)
	}))
	defer moderator.Close()
	defer unblock()
	cfg.Mode, cfg.BaseURL, cfg.APIKey, cfg.ChatModel = "demo", "", "", ""
	cfg.BillingMode = "credits"
	cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	const price = `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"2000000"}]}`
	if err := os.WriteFile(cfg.BillingPriceFile, []byte(price), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Moderation = ModerationConfig{Mode: "http", Endpoint: moderator.URL, CredentialID: "fixture", APIKey: newID("fixture"),
		PolicyFile: writeFixtureModerationPolicy(t, moderationPolicyFile{SchemaVersion: 1, PolicyID: "deep", PolicyVersion: "v1",
			MaxInputBytes: MaxChatContentBytes, MaxOutputBytes: maxRunOutputBytes, Rules: []ModerationRule{}})}
	reopened, serve := testApp(t, cfg)
	in := conversationInput(turns+1, parent, "")
	in.Operation = "continue"
	code, raw, _ := authRequest(t, client, serve.URL, "POST", "/api/v1/billing/quotes", "", owner.Project.ID, map[string]any{
		"resource_type": "conversation_run", "conversation_id": c.ID, "conversation_input": in})
	if code != 201 {
		t.Fatalf("long-branch quote rejected: %d", code)
	}
	q := decode[BillingQuote](t, raw)
	if q.ConversationID != c.ID || q.ProjectID != owner.Project.ID || q.ProviderMode != "demo" {
		t.Fatal("quote lost the chosen conversation or execution mode")
	}
	in.QuoteID = q.ID
	type reply struct {
		code int
		data []byte
	}
	result := make(chan reply, 1)
	go func() {
		code, data, _ := authRequest(t, client, serve.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", "deep-quoted-continue", owner.Project.ID, in)
		result <- reply{code, data}
	}()
	select {
	case checkedMessages := <-checked:
		if !slices.Equal(checkedMessages, deepExpectedMessages(turns+1, turns)) {
			t.Fatal("input review received the wrong deep ancestry")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("input review was not reached")
	}
	unlocked := make(chan struct{})
	go func() { reopened.mu.Lock(); reopened.mu.Unlock(); close(unlocked) }()
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		t.Fatal("waiting for review held the application state lock")
	}
	db := deliveryAdmin(t, dsn)
	if outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", q.ID) != 1 ||
		outboxCount(t, db, "SELECT count(*) FROM studio_runs WHERE conversation_id=$1", c.ID) != turns {
		t.Fatal("the unconfirmed input review advanced the quote or branch")
	}
	unblock()
	var accepted reply
	select {
	case accepted = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("approved long turn did not return")
	}
	if accepted.code != 201 {
		t.Fatalf("approved quote was not admitted: %d", accepted.code)
	}
	end := waitConversationRun(t, reopened, decode[Run](t, accepted.data).ID)
	if end.Status != "COMPLETED" || end.Moderation == nil || end.Moderation.State != "ALLOWED" ||
		end.Billing == nil || end.Billing.State != "SETTLED" || outputs.Load() != 1 || int(calls.Load()) != turns {
		t.Fatal("review or fixture-credit settlement failed")
	}
	var held, snapshot []byte
	requestHash := ledgerConversationRequestHash(c.ID, in)
	err = db.QueryRow(ctx, `SELECT q.request_hash, cc.snapshot FROM studio_ledger_quotes q
 JOIN studio_ledger_reservations r ON r.id=q.reservation_id AND r.resource_id=$2 AND r.workspace_id=q.workspace_id AND r.project_id=q.project_id
 JOIN studio_chat_contexts cc ON cc.run_id=r.resource_id AND cc.conversation_id=q.conversation_id WHERE q.id=$1`, q.ID, end.ID).Scan(&held, &snapshot)
	var frozen chatExecution
	if err != nil || !slices.Equal(held, requestHash[:]) || json.Unmarshal(snapshot, &frozen) != nil ||
		!slices.Equal(frozen.Request.Messages, deepExpectedMessages(turns+1, turns)) || !slices.Equal(frozen.SourceMessageIDs, lastIDs[2:]) {
		t.Fatal("quote, reservation, reviewed input and committed historical IDs differ")
	}
	code, raw, headers := authRequest(t, client, serve.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", "deep-quoted-continue", owner.Project.ID, in)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || decode[Run](t, raw).ID != end.ID ||
		outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations WHERE workspace_id=$1", owner.Workspace.ID) != 1 || outputs.Load() != 1 {
		t.Fatal("replay repeated review, generation or reservation")
	}
}
