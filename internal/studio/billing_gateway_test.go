package studio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Exercise the real HTTP adapter, original request/quote binding, DB commit and
// user-visible settlement together. Unit parsers alone cannot prove this chain.
func TestPostgresBillingGatewayUsageEndToEnd(t *testing.T) {
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages      []ChatMessage `json:"messages"`
			StreamOptions struct {
				Usage bool `json:"include_usage"`
			} `json:"stream_options"`
			MaxTokens int `json:"max_tokens"`
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&in) != nil || !in.StreamOptions.Usage || in.MaxTokens <= 0 || len(in.Messages) != 1 {
			http.Error(w, "bad request", 400)
			return
		}
		calls.Add(1)
		mode := in.Messages[0].Content
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"已输出\"}}]}\n\n")
		usage := `{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168}`
		if mode == "invalid" {
			usage = `{"prompt_tokens":1.5,"completion_tokens":45,"total_tokens":46.5}`
		}
		if mode == "over-budget" {
			usage = `{"prompt_tokens":999999,"completion_tokens":45,"total_tokens":1000044}`
		}
		if mode != "missing" {
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":"+usage+"}\n\n")
		}
		if mode != "incomplete" {
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	defer gateway.Close()
	pricePath := filepath.Join(t.TempDir(), "fixture-prices.json")
	if err := os.WriteFile(pricePath, []byte(`{"schema_version":1,"provider_mode":"live","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"4000000"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	a, server := testApp(t, Config{DatabaseURL: testDatabase(t), AuthMode: "required", Mode: "live", BaseURL: gateway.URL + "/v1", APIKey: "local-fixture-key", ChatModel: "fixture-chat", BillingMode: "credits", BillingPriceFile: pricePath})
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "metered-gateway@example.test")
	if err := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
		_, e := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: identity.Workspace.ID, OperationKey: "gateway-fixture-credit", AmountMicros: 1000000, Reason: "fixture", EvidenceRef: "fixture:gateway"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	var good Run
	var goodInput RunInput
	for _, tc := range []struct{ mode, state, status string }{{"valid", "SETTLED", "COMPLETED"}, {"missing", "REVIEW", "COMPLETED"}, {"invalid", "REVIEW", "COMPLETED"}, {"over-budget", "REVIEW", "COMPLETED"}, {"incomplete", "REVIEW", "FAILED"}} {
		t.Run(tc.mode, func(t *testing.T) {
			input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: tc.mode}
			quote := httpBillingQuote(t, client, server, identity.Project.ID, "run", input)
			input.QuoteID = quote.ID
			code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/runs", "gateway-"+tc.mode, identity.Project.ID, input)
			if code != 201 {
				t.Fatalf("accept %d %s", code, data)
			}
			run := awaitAuthRun(t, client, server.URL, decode[Run](t, data).ID)
			if run.Status != tc.status || run.Billing == nil || run.Billing.State != tc.state || run.Output != "已输出" {
				t.Fatalf("business/billing mismatch %+v", run)
			}
			if tc.mode == "valid" {
				if run.Billing.ChargedMicros != 303 || run.Billing.ReleasedMicros != quote.ReserveMicros-303 {
					t.Fatal("wrong actual-token fee", run.Billing)
				}
				good = run
				goodInput = input
			} else if run.Billing.ChargedMicros != 0 || run.Billing.ReleasedMicros != 0 {
				t.Fatal("untrusted usage moved credits", run.Billing)
			}
		})
	}
	// Accepted original replay wins even if the quote has since expired and
	// current pricing is unavailable. It must not reach the gateway again.
	if err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		_, e := c.Exec(ctx, "UPDATE studio_ledger_quotes SET expires_at=clock_timestamp()-interval '1 day' WHERE id=$1", goodInput.QuoteID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.pricing.Rates = map[string]LedgerPriceSnapshot{}
	a.pricing.Version = "reconfigured-price"
	a.mu.Unlock()
	code, data, headers := authRequest(t, client, server.URL, "POST", "/api/v1/runs", "gateway-valid", identity.Project.ID, goodInput)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || decode[Run](t, data).ID != good.ID || calls.Load() != 5 {
		t.Fatal("original replay re-executed or rejected")
	}
	goodInput.Input = "changed"
	code, data, _ = authRequest(t, client, server.URL, "POST", "/api/v1/runs", "gateway-valid", identity.Project.ID, goodInput)
	if code != 409 || !strings.Contains(string(data), "IDEMPOTENCY_CONFLICT") {
		t.Fatal("modified original key accepted")
	}
	code, data, _ = authRequest(t, client, server.URL, "GET", "/api/v1/billing/wallet", "", identity.Project.ID, nil)
	wallet := decode[LedgerWallet](t, data)
	if code != 200 || wallet.ChargedMicros != 303 || wallet.ReviewMicros != 4*45056 || wallet.ReservedMicros != wallet.ReviewMicros || wallet.AvailableMicros != 1000000-303-4*45056 {
		t.Fatal("end-to-end wallet mismatch", wallet)
	}
	if err := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
		r, e := reconcileLedgerSQL(ctx, tx, identity.Workspace.ID)
		if e == nil && (!r.Balanced || !r.AccountBalancesMatch || !r.HoldsMatch || !r.TotalsMatch) {
			t.Fatal("ledger did not reconcile", r)
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
}
