package studio

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPostgresOrderPaymentBrowserAndPairedRestore(t *testing.T) {
	if os.Getenv("ORDER_PAYMENT_BROWSER_TEST") != "1" || os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("requires a built workbench, local Chromium and PostgreSQL restore tools")
	}
	f := newWorkflowIntegrationFixture(t)
	catalog, err := filepath.Abs(filepath.Join("..", "..", "config", "order-catalog.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.OrderCatalogFile = catalog
	app, server := f.open(t)
	const buyerEmail = "payment-browser-buyer@example.test"
	const otherEmail = "payment-browser-outsider@example.test"
	buyer, other := authClient(t), authClient(t)
	one := registerFixture(t, buyer, server.URL, buyerEmail)
	registerFixture(t, other, server.URL, otherEmail)
	const base = "/api/v1/order-intents"
	scope := orderPaymentScope{Source: "fixture-verified-adapter", Merchant: "fixture-test-merchant", Environment: "test"}
	// The test calls the same internal transaction boundary a future authenticated
	// adapter will use. No user-facing HTTP route or statement file can call it.
	audit := deliveryAdmin(t, f.cfg.DatabaseURL)
	funded := func(tag string) (string, authenticatedOrderEvent) {
		t.Helper()
		raw, _ := presentationRequest(t, buyer, server.URL, "", "POST", base, "payment-browser-create-"+tag, orderStoreInput(app), 201)
		order := decode[OrderIntent](t, raw)
		if err := app.store.bindOrderPayment(one.Workspace.ID, one.User.ID, order.ID, scope); err != nil {
			t.Fatal(err)
		}
		capture := authenticatedOrderEvent{Scope: scope, WorkspaceID: one.Workspace.ID, OwnerID: one.User.ID, OrderID: order.ID, EventID: "browser-event-" + tag, Kind: "payment", TransactionID: "browser-transaction-" + tag, Currency: order.Product.Currency, AmountMinor: 1200, OccurredAt: paymentFixtureTime(t, audit)}
		if _, err := app.store.recordAuthenticatedOrderEvent(capture); err != nil {
			t.Fatal(err)
		}
		return order.ID, capture
	}
	paid, _ := funded("paid")
	refunded, notice := funded("refunded")
	notice.EventID = "browser-refund-notice"
	notice.RefundID = "browser-refund-id"
	notice.Kind = "refund"
	notice.AmountMinor = 300
	notice.OccurredAt = paymentFixtureTime(t, audit)
	if _, err := app.store.recordAuthenticatedOrderEvent(notice); err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("ORDER_PAYMENT_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("ORDER_PAYMENT_BROWSER_OUTPUT must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/order-payment-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "ORDER_PAYMENT_BROWSER_URL="+server.URL, "ORDER_PAYMENT_BROWSER_OUTPUT="+output,
		"ORDER_PAYMENT_BUYER_EMAIL="+buyerEmail, "ORDER_PAYMENT_BUYER_PASSWORD=fixture-password-123", "ORDER_PAYMENT_OTHER_EMAIL="+otherEmail, "ORDER_PAYMENT_OTHER_PASSWORD=fixture-password-123",
		"ORDER_PAYMENT_PAID_ORDER_ID="+paid, "ORDER_PAYMENT_REFUNDED_ORDER_ID="+refunded)
	raw, err := cmd.CombinedOutput()
	if bytes.Contains(raw, []byte("fixture-password-123")) {
		t.Fatal("browser diagnostics exposed a fixture credential")
	}
	if err != nil {
		t.Fatalf("order payment status browser: %v\n%s", err, raw)
	}
	t.Log(strings.TrimSpace(string(raw)))
	read := func(c *http.Client, url, id string) OrderPaymentProjection {
		t.Helper()
		raw, _ := presentationRequest(t, c, url, "", "GET", base+"/"+id+"/payment-status", "", nil, 200)
		return decode[OrderPaymentProjection](t, raw)
	}
	first, second := read(buyer, server.URL, paid), read(buyer, server.URL, refunded)
	if outboxCount(t, audit, "SELECT count(*) FROM studio_order_payment_events") != 3 || outboxCount(t, audit, "SELECT count(*) FROM studio_order_payment_credits") != 2 {
		t.Fatal("status checks changed payment events or purchase credits")
	}
	server.Close()
	app.Close()
	restoreWorkflowFixture(t, f, "studio_order_intents", "studio_order_intent_receipts", "studio_order_payment_environment", "studio_order_payment_bindings", "studio_order_payment_identities", "studio_order_payment_facts", "studio_order_payment_conflicts", "studio_order_pending_slots", "studio_order_payment_events", "studio_order_payment_credits", "studio_order_payment_reviews", "studio_ledger_wallets", "studio_ledger_journals", "studio_ledger_postings")
	f.cfg.OrderCatalogFile = ""
	app, server = f.open(t)
	buyer, other = authClient(t), authClient(t)
	for _, pair := range []struct {
		client *http.Client
		email  string
	}{{buyer, buyerEmail}, {other, otherEmail}} {
		presentationRequest(t, pair.client, server.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": pair.email, "password": "fixture-password-123"}, 200)
	}
	if !reflect.DeepEqual(first, read(buyer, server.URL, paid)) || !reflect.DeepEqual(second, read(buyer, server.URL, refunded)) {
		t.Fatal("restoring without a catalog changed original financial or entitlement history")
	}
	presentationRequest(t, other, server.URL, "", "GET", base+"/"+refunded+"/payment-status", "", nil, 404)
	before := reconciliationBusinessSignatures(t, deliveryAdmin(t, f.cfg.DatabaseURL))
	if _, err := app.store.recordAuthenticatedOrderEvent(notice); err != nil {
		t.Fatal(err)
	}
	after := reconciliationBusinessSignatures(t, deliveryAdmin(t, f.cfg.DatabaseURL))
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(second, read(buyer, server.URL, refunded)) {
		t.Fatal("replaying the original refund receipt after recovery changed the payment state or ledger")
	}
}
