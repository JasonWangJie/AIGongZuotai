package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func newModeratedBillingFixture(t *testing.T, operations ...bool) (*App, *httptest.Server, *http.Client, testIdentity, string) {
	t.Helper()
	database := testDatabase(t)
	pricePath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(pricePath, []byte(`{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"2000000"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	policy := executionModerationConfig(t, []ModerationRule{{Phase: "input", Contains: "DENY-INPUT", Decision: "block"}, {Phase: "output", Contains: "BLOCK-OUTPUT", Decision: "block"}, {Phase: "output", Contains: "UNKNOWN-OUTPUT", Decision: "review"}})
	a, s := testApp(t, Config{DatabaseURL: database, AuthMode: "required", OperationsEnabled: len(operations) > 0 && operations[0], Moderation: policy, BillingMode: "credits", BillingPriceFile: pricePath})
	c := authClient(t)
	identity := registerFixture(t, c, s.URL, "moderation-billing@example.test")
	if err := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
		_, e := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: identity.Workspace.ID, OperationKey: newID("grant"), Reason: "fixture", EvidenceRef: "fixture:moderation", AmountMicros: 1_000_000})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return a, s, c, identity, database
}

func TestPostgresModeratedInputAtomicityAndOutputBilling(t *testing.T) {
	a, s, client, identity, database := newModeratedBillingFixture(t)
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	p := &moderatedExecutionChat{text: "allowed generated output"}
	a.chat = p
	input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "DENY-INPUT"}
	quote := httpBillingQuote(t, client, s, identity.Project.ID, "run", input)
	input.QuoteID = quote.ID
	code, _, _ := authRequest(t, client, s.URL, "POST", "/api/v1/runs", "blocked-input-original", identity.Project.ID, input)
	if code != 422 || p.calls.Load() != 0 {
		t.Fatal("input generated or was accepted", code)
	}
	for _, table := range []string{"studio_runs", "studio_run_moderation", "studio_moderation_decisions", "studio_ledger_reservations"} {
		if outboxCount(t, admin, "SELECT count(*) FROM "+table) != 0 {
			t.Fatal("rejection left transactional state", table)
		}
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", quote.ID) != 1 {
		t.Fatal("rejected input consumed quote")
	}
	c := createPGConversation(t, client, s, identity)
	conversation := conversationInput(c.Version, nil, "DENY-INPUT")
	// Billing validation runs before moderation but does not consume a quote.
	conversation.QuoteID = quote.ID
	code, _, _ = authRequest(t, client, s.URL, "POST", "/api/v1/conversations/"+c.ID+"/runs", "blocked-conversation-original", identity.Project.ID, conversation)
	if code != 422 || outboxCount(t, admin, "SELECT count(*) FROM studio_chat_messages") != 0 || outboxCount(t, admin, "SELECT version FROM studio_conversations WHERE id=$1", c.ID) != 1 {
		t.Fatal("denied conversation changed tree", code)
	}
	for _, tc := range []struct{ text, state, billing string }{{"allowed generated output", "ALLOWED", "SETTLED"}, {"BLOCK-OUTPUT private candidate", "BLOCKED", "REVIEW"}, {"UNKNOWN-OUTPUT private candidate", "REVIEW", "REVIEW"}} {
		t.Run(tc.state, func(t *testing.T) {
			provider := &moderatedExecutionChat{text: tc.text, emitted: make(chan struct{}, 1), release: make(chan struct{})}
			a.chat = provider
			input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "safe request " + tc.state}
			q := httpBillingQuote(t, client, s, identity.Project.ID, "run", input)
			input.QuoteID = q.ID
			code, body, _ := authRequest(t, client, s.URL, "POST", "/api/v1/runs", newID("key"), identity.Project.ID, input)
			if code != 201 {
				t.Fatalf("accept %d %s", code, body)
			}
			run := decode[Run](t, body)
			awaitModeratedEmission(t, provider)
			var doc, policy []byte
			if err := admin.QueryRow(context.Background(), "SELECT document FROM studio_runs WHERE id=$1", run.ID).Scan(&doc); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(doc), tc.text) {
				t.Fatal("unreviewed checkpoint leaked")
			}
			if err := admin.QueryRow(context.Background(), "SELECT policy FROM studio_run_moderation WHERE run_id=$1", run.ID).Scan(&policy); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(policy), input.Input) || strings.Contains(string(policy), tc.text) {
				t.Fatal("private policy captured user data")
			}
			close(provider.release)
			final := awaitAuthRun(t, client, s.URL, run.ID)
			if final.Moderation.State != tc.state || final.Billing.State != tc.billing {
				t.Fatalf("wrong review/ledger %+v", final)
			}
			if tc.state == "ALLOWED" {
				if final.Output != tc.text || final.Billing.ChargedMicros != 256 {
					t.Fatal("allowed settlement incorrect")
				}
			} else if final.Output != "" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != 0 {
				t.Fatal("review released text or money")
			}
			if outboxCount(t, admin, "SELECT count(*) FROM studio_moderation_decisions WHERE run_id=$1", run.ID) != 2 {
				t.Fatal("missing immutable decision history")
			}
		})
	}
}

func TestPostgresModeratedReleaseRollbackNeverPublishesCandidate(t *testing.T) {
	a, s, client, identity, database := newModeratedBillingFixture(t)
	p := &moderatedExecutionChat{text: "PRIVATE-ALLOW-CANDIDATE", emitted: make(chan struct{}, 1), release: make(chan struct{})}
	a.chat = p
	input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "safe request"}
	q := httpBillingQuote(t, client, s, identity.Project.ID, "run", input)
	input.QuoteID = q.ID
	code, body, _ := authRequest(t, client, s.URL, "POST", "/api/v1/runs", newID("key"), identity.Project.ID, input)
	if code != 201 {
		t.Fatal(code, string(body))
	}
	run := decode[Run](t, body)
	awaitModeratedEmission(t, p)
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err = admin.Exec(context.Background(), `CREATE FUNCTION moderation_fail_allow() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='ALLOWED' THEN RAISE EXCEPTION 'fixture allow audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER moderation_fail_allow BEFORE INSERT ON studio_moderation_decisions FOR EACH ROW EXECUTE FUNCTION moderation_fail_allow()`); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	deadline := time.Now().Add(4 * time.Second)
	for !a.store.failed.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !a.store.failed.Load() {
		t.Fatal("allow transaction was not rejected")
	}
	a.mu.Lock()
	cached := copyRun(a.runs[run.ID].Run)
	events := append([]event{}, a.runs[run.ID].Events...)
	a.mu.Unlock()
	if cached.Output != "" || cached.Moderation.State != "PENDING" {
		t.Fatal("uncommitted result entered cache")
	}
	for _, e := range events {
		if strings.Contains(string(e.Data), p.text) {
			t.Fatal("uncommitted result entered SSE")
		}
	}
	var doc []byte
	if err := admin.QueryRow(context.Background(), "SELECT document FROM studio_runs WHERE id=$1", run.ID).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	durable := decode[Run](t, doc)
	if durable.Output != "" || durable.Status != "RUNNING" || durable.Billing.State != "HELD" || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals WHERE kind='CHARGE'") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_moderation_decisions WHERE run_id=$1", run.ID) != 1 {
		t.Fatal("review/ledger/business transaction not atomic", string(doc))
	}
}

func TestPostgresModeratedRestartKeepsQuarantineAndOriginalReplay(t *testing.T) {
	database := testDatabase(t)
	a, s, client, identity := newModeratedExecutionFixture(t, database, nil)
	// Insert an accepted, started checkpoint without an in-process worker. This
	// deterministically models persisted state at an ungraceful stop; the parent
	// integration separately exercises actual OS process termination.
	input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "safe restart request"}
	policy, err := a.moderation.CheckInput(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: input.Input}}, MaxOutputTokens: 4096, OutputLimitField: "max_tokens"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := Run{ID: newID("run"), WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, ModelID: input.ModelID, ProviderMode: "demo", Prompt: input.Input, Status: "QUEUED", CreatedAt: now, UpdatedAt: now, ModerationPolicy: policy, Moderation: InitialModerationSummary(policy)}
	key := "moderation-restart-original"
	if err := a.store.createRunCandidate(&run, identity.Workspace.ID+":runs:"+key, payloadHash(input)); err != nil {
		t.Fatal(err)
	}
	claimFixtureRun(t, a.store, &run)
	cfg := a.config
	a.Close()
	cfg.Moderation = ModerationConfig{} // Turning off current checks never releases prior private candidates.
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := httptest.NewServer(reopened.Handler())
	defer second.Close()
	code, body, _ := authRequest(t, client, second.URL, "GET", "/api/v1/runs/"+run.ID+"/snapshot", "", identity.Project.ID, nil)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	recovered := decode[Run](t, body)
	if recovered.Output != "" || recovered.Status != "INTERRUPTED" || recovered.Moderation.State != "REVIEW" || recovered.Moderation.ReasonCode != "process_restarted" || recovered.Seq <= run.Seq {
		t.Fatal("restart promoted or lost review", recovered)
	}
	code, body, headers := authRequest(t, client, second.URL, "POST", "/api/v1/runs", key, identity.Project.ID, input)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || decode[Run](t, body).ID != run.ID {
		t.Fatal("restart replay resubmitted")
	}
	_ = s
	_ = database
}
