package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func reconciliationBusinessSignatures(t *testing.T, db *pgx.Conn) map[string]string {
	t.Helper()
	result := map[string]string{}
	ctx := context.Background()
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	err := pgx.BeginTxFunc(ctx, db, opts, func(tx pgx.Tx) error {
		for _, table := range []string{
			"studio_order_intents", "studio_order_intent_receipts", "studio_ledger_wallets",
			"studio_ledger_accounts", "studio_ledger_quotes", "studio_ledger_reservations",
			"studio_ledger_journals", "studio_ledger_postings", "studio_ledger_operations",
			"studio_order_payment_environment", "studio_order_payment_bindings", "studio_order_payment_identities",
			"studio_order_payment_facts", "studio_order_payment_events", "studio_order_payment_conflicts",
			"studio_order_payment_credits", "studio_order_payment_reviews", "studio_order_pending_slots",
		} {
			// Capture all rows and columns of each table at one MVCC snapshot.
			// Keep private evidence and row digests out of test diagnostics.
			q := "SELECT md5(coalesce(string_agg(v,'' ORDER BY v),'')) FROM (SELECT md5(to_jsonb(r)::text) AS v FROM " + pgx.Identifier{table}.Sanitize() + " AS r) x"
			var digest string
			if err := tx.QueryRow(ctx, q).Scan(&digest); err != nil {
				return err
			}
			result[table] = digest
		}
		return nil
	})
	if err != nil || len(result) != 18 {
		t.Fatal("could not snapshot all 18 business tables")
	}
	return result
}

func TestPostgresOrderReconciliationReadOnlyWorkspace(t *testing.T) {
	cfg := Config{DatabaseURL: testDatabase(t), AuthMode: "required", OrderCatalogFile: filepath.Join("..", "..", "config", "order-catalog.example.json")}
	a, s := testApp(t, cfg)
	buyer, other := authClient(t), authClient(t)
	one := registerFixture(t, buyer, s.URL, "reconcile-private-owner@example.test")
	registerFixture(t, other, s.URL, "reconcile-other@example.test")
	httpCredit(t, a, one.Workspace.ID, 1234567)
	create := func(c *http.Client) OrderIntent {
		v, _ := presentationRequest(t, c, s.URL, "", "POST", "/api/v1/order-intents", newID("order"), orderStoreInput(a), 201)
		return decode[OrderIntent](t, v)
	}
	own, closed, foreign := create(buyer), create(buyer), create(other)
	create(buyer) // no material references this order
	v, _ := presentationRequest(t, buyer, s.URL, "", "POST", "/api/v1/order-intents/"+closed.ID+"/cancel", newID("cancel"), map[string]int{"expected_version": 1}, 200)
	closed = decode[OrderIntent](t, v)
	c := reconCfg()
	c.WorkspaceID = one.Workspace.ID
	c.DatabaseURL = cfg.DatabaseURL
	c.Statement = filepath.Join(t.TempDir(), "statement.json")
	c.From = own.CreatedAt.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	c.To = closed.CancelledAt.Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	scope := orderPaymentScope{Source: "fixture-reconciliation", Merchant: c.Merchant, Environment: c.Environment}
	if err := a.store.bindOrderPayment(one.Workspace.ID, one.User.ID, own.ID, scope); err != nil {
		t.Fatal("could not bind test purchase")
	}
	minor, err := strconv.ParseInt(own.Product.AmountMinor, 10, 64)
	if err != nil || minor <= 0 {
		t.Fatal("invalid frozen amount")
	}
	p := reconPayment(own.ID, "e-1", "charge-1")
	p.At = paymentFixtureTime(t, admin)
	p.Currency = own.Product.Currency
	p.Minor = own.Product.AmountMinor
	event := authenticatedOrderEvent{
		Scope: scope, WorkspaceID: one.Workspace.ID, OwnerID: one.User.ID, OrderID: own.ID,
		EventID: p.ID, Kind: "payment", TransactionID: p.Payment, OccurredAt: p.At, Currency: p.Currency, AmountMinor: minor,
	}
	payment, err := a.store.recordAuthenticatedOrderEvent(event)
	if err != nil || payment.Projection.PaymentState != "RECEIVED" || payment.Projection.RefundState != "NONE" ||
		payment.Projection.CreditState != "ISSUED" || payment.Projection.PaidMinor != p.Minor ||
		payment.Projection.GrantedCreditMicros != own.Product.CreditMicros {
		t.Fatal("payment did not commit a purchase credit for the frozen amount")
	}
	refunded := reconRefund(p, "e-2", "refund-1", p.Minor)
	refunded.At = paymentFixtureTime(t, admin)
	event.EventID, event.Kind, event.RefundID, event.OccurredAt = refunded.ID, "refund", refunded.Refund, refunded.At
	refund, err := a.store.recordAuthenticatedOrderEvent(event)
	if err != nil || refund.Projection.PaymentState != "RECEIVED" || refund.Projection.RefundState != "FULL" ||
		refund.Projection.CreditState != "REVIEW" || refund.Projection.RefundedMinor != p.Minor ||
		refund.Projection.GrantedCreditMicros != own.Product.CreditMicros || refund.Projection.ReversedCreditMicros != "0" {
		t.Fatal("full refund or original purchase credit missing from the committed receipt")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_order_payment_facts WHERE order_id=$1", own.ID) != 2 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_order_payment_events WHERE order_id=$1", own.ID) != 2 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_order_payment_credits WHERE order_id=$1", own.ID) != 1 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_order_pending_slots WHERE order_id=$1", own.ID) != 0 {
		t.Fatal("purchase facts, event receipts, journal link or pending slot inconsistent")
	}
	late := p
	late.ID = "e-3"
	late.Payment = "charge-2"
	late.Order = closed.ID
	late.At = closed.CancelledAt.Add(time.Second)
	outside := p
	outside.ID = "e-4"
	outside.Payment = "charge-3"
	outside.Order = foreign.ID
	if e := os.WriteFile(c.Statement, evidenceJSON(t, c, []evidenceEvent{p, refunded, late, outside}), 0600); e != nil {
		t.Fatal(e)
	}
	before := reconciliationBusinessSignatures(t, admin)
	ctx := context.Background()
	hold, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback(ctx)
	// Keep the app's control writer AND an actual row lock alive during
	// comparison. A read-only MVCC comparison must not acquire either lock.
	var held string
	if e = hold.QueryRow(ctx, "SELECT id FROM studio_order_intents WHERE id=$1 FOR UPDATE", own.ID).Scan(&held); e != nil {
		t.Fatal(e)
	}
	r, e := ReconcileOrderEvidence(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	again, e := ReconcileOrderEvidence(ctx, c)
	if e != nil || !reflect.DeepEqual(again, r) {
		t.Fatal("repeat under live writer changed result", e)
	}
	if binary := os.Getenv("ORDER_RECONCILE_TEST_BINARY"); binary != "" {
		if !filepath.IsAbs(binary) {
			t.Fatal("the reconciliation CLI path must be absolute")
		}
		commandCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		command := exec.CommandContext(commandCtx, binary, "-statement", c.Statement, "-merchant", c.Merchant,
			"-environment", c.Environment, "-workspace", c.WorkspaceID, "-from", c.From, "-to", c.To)
		command.Env = []string{"DATABASE_URL=" + cfg.DatabaseURL}
		for _, name := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
			if value := os.Getenv(name); value != "" {
				command.Env = append(command.Env, name+"="+value)
			}
		}
		output, err := command.Output()
		if err != nil {
			t.Fatal("the reconciliation executable failed against the scoped fixture")
		}
		var actual OrderReconciliationReport
		decoder := json.NewDecoder(strings.NewReader(string(output)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&actual) != nil || !json.Valid(output) || !reflect.DeepEqual(actual, r) {
			t.Fatal("executable output differed from the scoped aggregate report")
		}
		t.Log("compiled reconciliation CLI returned the same aggregate report while the writer and order lock remained active")
	}
	if r.Authenticated || r.Referenced != 3 || r.Matched != 1 || r.MatchedFullRefund != 1 || r.Missing != 1 || r.Contradictory != 1 || r.Findings["order_not_found_in_scope"] != 1 || r.Findings["event_at_or_after_cancellation"] != 1 || r.Findings["full_refund_in_material"] != 1 {
		t.Fatal("workspace or material classification changed")
	}
	raw, _ := json.Marshal(r)
	for _, private := range []string{foreign.ID, own.ID, closed.ID, one.User.ID, one.User.Email, one.Workspace.ID, scope.Source, scope.Merchant, p.Payment, refunded.Refund} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private identifier in aggregate")
		}
	}
	if e = hold.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	c.WorkspaceID = newID("ws")
	if e = os.WriteFile(c.Statement, evidenceJSON(t, c, []evidenceEvent{p}), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = ReconcileOrderEvidence(ctx, c); e == nil {
		t.Fatal("unknown workspace accepted")
	}
	if !reflect.DeepEqual(before, reconciliationBusinessSignatures(t, admin)) {
		t.Fatal("comparison changed order, payment, entitlement or pending slot rows")
	}
}
