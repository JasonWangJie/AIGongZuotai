package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestBillingInputAndCursorBounds(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"input":"one","input":"two"}`, `{"input":"one","quote_id":""}`, `{"input":"one","QUOTE_ID":"hidden"}`, `{"input":"one","unexpected":true}`, `{"input":"one"} {}`} {
		var in RunInput
		if decodeBillingInput([]byte(raw), &in, true) == nil {
			t.Fatalf("invalid request accepted: %s", raw)
		}
	}
	var in RunInput
	if err := decodeBillingInput([]byte(`{"input":"safe","project_id":"prj_demo","model_id":"studio-chat"}`), &in, true); err != nil {
		t.Fatal(err)
	}
	scope := billingCursor{Version: 1, User: "usr_a", Workspace: "ws_a", Project: "prj_a", Page: "entries", Filter: "RESERVE"}
	c := scope
	c.BeforeID = newID("jrn")
	c.CreatedAt = time.Now().UTC()
	encoded := encodeBillingCursor(c)
	if decoded, err := parseBillingCursor(encoded, scope); err != nil || decoded.BeforeID != c.BeforeID {
		t.Fatal("valid cursor rejected", err)
	}
	for _, change := range []func(*billingCursor){func(c *billingCursor) { c.User = "usr_b" }, func(c *billingCursor) { c.Workspace = "ws_b" }, func(c *billingCursor) { c.Project = "" }, func(c *billingCursor) { c.Page = "reservations" }, func(c *billingCursor) { c.Filter = "" }, func(c *billingCursor) { c.BeforeID = "jrn_bad" }, func(c *billingCursor) { c.CreatedAt = time.Time{} }} {
		other := c
		change(&other)
		if _, err := parseBillingCursor(encodeBillingCursor(other), scope); err == nil {
			t.Fatal("cross-scope or malformed cursor accepted")
		}
	}
	for _, raw := range []string{strings.Repeat("x", 1025), base64.RawURLEncoding.EncodeToString([]byte(`{} {}`)), "not-base64!"} {
		if _, err := parseBillingCursor(raw, scope); err == nil {
			t.Fatal("malformed cursor accepted")
		}
	}
}

func TestBillingDisabledHTTP(t *testing.T) {
	_, server := testApp(t, Config{})
	code, data, _ := request(t, server, "GET", "/api/v1/bootstrap", "", nil)
	var bootstrap struct {
		Billing BillingCapabilities `json:"billing"`
	}
	if json.Unmarshal(data, &bootstrap) != nil || code != 200 || bootstrap.Billing.Enabled || bootstrap.Billing.Mode != "disabled" {
		t.Fatal("disabled capability missing")
	}
	for _, path := range []string{"wallet", "entries", "reservations", "quotes"} {
		method := "GET"
		if path == "quotes" {
			method = "POST"
		}
		code, data, _ = request(t, server, method, "/api/v1/billing/"+path, "", map[string]any{})
		if code != 409 || !strings.Contains(string(data), "BILLING_DISABLED") {
			t.Fatalf("disabled %s: %d %s", path, code, data)
		}
	}
}

func billingHTTPFixture(t *testing.T) (*App, *httptest.Server, *http.Client, testIdentity) {
	t.Helper()
	app, server := testApp(t, Config{DatabaseURL: testDatabase(t), AuthMode: "required", BillingMode: "credits", ModelCatalogFile: filepath.Join("..", "..", "config", "models.example.json"), BillingPriceFile: filepath.Join("..", "..", "config", "prices.demo.example.json")})
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "billing-http@example.test")
	return app, server, client, identity
}
func httpBillingQuote(t *testing.T, c *http.Client, s *httptest.Server, project, kind string, input any) BillingQuote {
	t.Helper()
	body := map[string]any{"resource_type": kind, kind + "_input": input}
	code, data, _ := authRequest(t, c, s.URL, "POST", "/api/v1/billing/quotes", "", project, body)
	if code != 201 {
		t.Fatalf("quote %d %s", code, data)
	}
	return decode[BillingQuote](t, data)
}
func httpCredit(t *testing.T, a *App, ws string, amount int64) {
	t.Helper()
	if err := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
		_, e := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: ws, OperationKey: newID("credit"), AmountMicros: amount, Reason: "http-fixture", EvidenceRef: "fixture:http"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresBillingHTTPQuoteSnapshotAndPreflight(t *testing.T) {
	app, server, client, identity := billingHTTPFixture(t)
	in := runInput()
	in.ProjectID = identity.Project.ID
	in.Input = "private quote body is never a response field"
	quote := httpBillingQuote(t, client, server, in.ProjectID, "run", in)
	if quote.ExpiresAt.Sub(quote.CreatedAt) != 10*time.Minute || quote.ReserveMicros <= 0 || quote.ProviderMode != "demo" || quote.Unit != "CREDIT" {
		t.Fatalf("quote envelope %+v", quote)
	}
	var hash []byte
	var business, wallets int
	if err := app.store.do(func(ctx context.Context, c *pgx.Conn) error {
		if e := c.QueryRow(ctx, "SELECT request_hash FROM studio_ledger_quotes WHERE id=$1", quote.ID).Scan(&hash); e != nil {
			return e
		}
		if e := c.QueryRow(ctx, "SELECT (SELECT count(*) FROM studio_runs)+(SELECT count(*) FROM studio_tasks)+(SELECT count(*) FROM studio_ledger_reservations)").Scan(&business); e != nil {
			return e
		}
		return c.QueryRow(ctx, "SELECT count(*) FROM studio_ledger_wallets").Scan(&wallets)
	}); err != nil {
		t.Fatal(err)
	}
	expected := ledgerRunRequestHash(in)
	if string(hash) != string(expected[:]) || business != 0 || wallets != 0 {
		t.Fatal("quote mutated business or request binding")
	}
	encoded, _ := json.Marshal(quote)
	for _, secret := range []string{in.Input, "request_hash", "upstream_model"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("quote leaked private input")
		}
	}
	for _, body := range []any{
		map[string]any{"run_input": in}, map[string]any{"resource_type": "task", "run_input": in}, map[string]any{"resource_type": "run", "run_input": in, "task_input": taskInput("image")},
		map[string]any{"resource_type": "run", "run_input": map[string]any{"project_id": in.ProjectID, "model_id": in.ModelID, "input": "x", "quote_id": ""}},
	} {
		code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", in.ProjectID, body)
		if code != 422 || !strings.Contains(string(data), "INVALID_QUOTE_INPUT") {
			t.Fatalf("invalid variant %d %s", code, data)
		}
	}
	media := taskInput("image")
	media.ProjectID = identity.Project.ID
	mediaQuote := httpBillingQuote(t, client, server, media.ProjectID, "task", media)
	if mediaQuote.Basis != "delivered_artifact" || mediaQuote.RequestedUnits != media.Parameters.Count {
		t.Fatalf("media price snapshot %+v", mediaQuote)
	}
	media.Operation = "edit"
	media.Inputs.ReferenceAssetIDs = []string{newID("ref")}
	code, _, _ := authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", media.ProjectID, map[string]any{"resource_type": "task", "task_input": media})
	if code != 404 {
		t.Fatalf("foreign/missing reference accepted: %d", code)
	}
	conversation := createPGConversation(t, client, server, identity)
	turn := conversationInput(conversation.Version, nil, "not sent to provider")
	body := map[string]any{"resource_type": "conversation_run", "conversation_id": conversation.ID, "conversation_input": turn}
	code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", identity.Project.ID, body)
	if code != 201 {
		t.Fatalf("conversation preflight %d %s", code, data)
	}
	convQuote := decode[BillingQuote](t, data)
	if convQuote.ConversationID != conversation.ID || convQuote.ResourceType != "run" {
		t.Fatal("conversation quote binding missing")
	}
	stale := uint64(2)
	turn.ExpectedVersion = &stale
	body["conversation_input"] = turn
	code, _, _ = authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", identity.Project.ID, body)
	if code != 409 {
		t.Fatalf("stale conversation quote %d", code)
	}
	code, data, _ = authRequest(t, client, server.URL, "GET", "/api/v1/conversations/"+conversation.ID, "", identity.Project.ID, nil)
	if code != 200 || decode[Conversation](t, data).Version != conversation.Version {
		t.Fatal("quote changed conversation")
	}
	other := authClient(t)
	foreign := registerFixture(t, other, server.URL, "billing-other@example.test")
	in.ProjectID = foreign.Project.ID
	code, _, _ = authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", "", map[string]any{"resource_type": "run", "run_input": in})
	if code != 404 {
		t.Fatalf("foreign project quote %d", code)
	}
}

func TestPostgresBillingHTTPWalletPaginationAndIsolation(t *testing.T) {
	app, server, client, identity := billingHTTPFixture(t)
	code, data, _ := authRequest(t, client, server.URL, "GET", "/api/v1/billing/wallet", "", "", nil)
	wallet := decode[LedgerWallet](t, data)
	if code != 200 || wallet.AvailableMicros != 0 || wallet.Version != 0 || wallet.UpdatedAt != nil {
		t.Fatal("empty wallet is not a zero read")
	}
	httpCredit(t, app, identity.Workspace.ID, 10_000_000)
	httpCredit(t, app, identity.Workspace.ID, 11)
	httpCredit(t, app, identity.Workspace.ID, 13)
	type entryPage struct {
		Items []LedgerEntry `json:"items"`
		Next  *string       `json:"next_cursor"`
	}
	code, data, _ = authRequest(t, client, server.URL, "GET", "/api/v1/billing/entries?limit=2&kind=CREDIT", "", identity.Project.ID, nil)
	first := decode[entryPage](t, data)
	if code != 200 || len(first.Items) != 2 || first.Next == nil {
		t.Fatalf("first page %d %s", code, data)
	}
	code, data, _ = authRequest(t, client, server.URL, "GET", "/api/v1/billing/entries?limit=2&kind=CREDIT&cursor="+url.QueryEscape(*first.Next), "", "", nil)
	second := decode[entryPage](t, data)
	if code != 200 || len(second.Items) != 1 || second.Next != nil || second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Fatal("keyset repeated or lost rows")
	}
	code, _, _ = authRequest(t, client, server.URL, "GET", "/api/v1/billing/entries?cursor="+url.QueryEscape(*first.Next), "", "", nil)
	if code != 400 {
		t.Fatal("cursor filter not bound")
	}
	other := authClient(t)
	registerFixture(t, other, server.URL, "billing-isolated@example.test")
	code, data, _ = authRequest(t, other, server.URL, "GET", "/api/v1/billing/entries", "", "", nil)
	if code != 200 || len(decode[entryPage](t, data).Items) != 0 {
		t.Fatal("other workspace saw journals")
	}
	code, _, _ = authRequest(t, other, server.URL, "GET", "/api/v1/billing/entries?kind=CREDIT&cursor="+url.QueryEscape(*first.Next), "", "", nil)
	if code != 400 {
		t.Fatal("foreign cursor accepted")
	}
	for _, query := range []string{"limit=0", "limit=51", "limit=1.5", "kind=credit", "project_id=" + newID("prj")} {
		code, _, _ = authRequest(t, client, server.URL, "GET", "/api/v1/billing/entries?"+query, "", "", nil)
		if code != 400 && code != 404 {
			t.Fatalf("invalid page %q: %d", query, code)
		}
	}
	input := runInput()
	input.ProjectID = identity.Project.ID
	q := httpBillingQuote(t, client, server, input.ProjectID, "run", input)
	if err := ledgerTx(app.store, func(ctx context.Context, tx pgx.Tx) error {
		_, e := reserveLedgerSQL(ctx, tx, LedgerReservationRequest{QuoteID: q.ID, WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, ResourceType: "run", ResourceID: newID("run"), ModelID: input.ModelID, ModelVersion: q.ModelVersion, ProviderMode: "demo", RequestHash: ledgerRunRequestHash(input)})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	code, data, _ = authRequest(t, client, server.URL, "GET", "/api/v1/billing/reservations?state=HELD&project_id="+identity.Project.ID, "", "", nil)
	var reservations struct {
		Items []LedgerReservation `json:"items"`
		Next  *string             `json:"next_cursor"`
	}
	if json.Unmarshal(data, &reservations) != nil || code != 200 || len(reservations.Items) != 1 || reservations.Items[0].State != "HELD" || reservations.Next != nil {
		t.Fatalf("reservations %d %s", code, data)
	}
	for _, private := range []string{"request_hash", "input_per_million", "usage", "upstream"} {
		if strings.Contains(string(data), private) {
			t.Fatal("reservation read leaked private evidence")
		}
	}
	code, data, _ = authRequest(t, other, server.URL, "GET", "/api/v1/billing/reservations", "", "", nil)
	if code != 200 || !strings.Contains(string(data), `"items":[]`) {
		t.Fatal("other workspace saw reservations")
	}
}
