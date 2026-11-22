package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type paymentFixture struct {
	app    *App
	server *httptest.Server
	owner  *principal
	admin  *pgx.Conn
	scope  orderPaymentScope
	dsn    string
}

func newPaymentFixture(t *testing.T) *paymentFixture {
	t.Helper()
	dsn := testDatabase(t)
	cfg := Config{DatabaseURL: dsn, AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), OrderCatalogFile: filepath.Join("..", "..", "config", "order-catalog.example.json")}
	a, s := testApp(t, cfg)
	return paymentFixtureForApp(t, a, s, dsn)
}

func paymentFixtureForApp(t *testing.T, a *App, s *httptest.Server, dsn string) *paymentFixture {
	t.Helper()
	c := authClient(t)
	registerFixture(t, c, s.URL, "test-purchase@example.test")
	p, err := a.currentPrincipal(operationsRequest(t, c, s))
	if err != nil {
		t.Fatal(err)
	}
	return &paymentFixture{app: a, server: s, owner: p, admin: deliveryAdmin(t, dsn), scope: orderPaymentScope{Source: "private-source", Merchant: "private-shop", Environment: "test"}, dsn: dsn}
}

func (f *paymentFixture) order(t *testing.T, bind bool) authenticatedOrderEvent {
	t.Helper()
	o, _, err := f.app.createOrderIntent(f.owner, orderStoreInput(f.app), newID("intent"))
	if err != nil {
		t.Fatal(err)
	}
	e := authenticatedOrderEvent{Scope: f.scope, OwnerID: f.owner.User.ID, WorkspaceID: f.owner.Workspace.ID, OrderID: o.ID,
		EventID: newID("event"), Kind: "payment", TransactionID: newID("economic"), Currency: o.Product.Currency, AmountMinor: 1200}
	if bind {
		if err = f.app.store.bindOrderPayment(e.WorkspaceID, e.OwnerID, e.OrderID, e.Scope); err != nil {
			t.Fatal(err)
		}
	}
	e.OccurredAt = paymentFixtureTime(t, f.admin)
	return e
}

// The fixture models a payment occurring after its binding and before its
// receipt. Both bounds use the database clock in the production transaction.
func paymentFixtureTime(t *testing.T, conn *pgx.Conn) time.Time {
	t.Helper()
	var now time.Time
	if err := conn.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC()
}

func (f *paymentFixture) record(t *testing.T, e authenticatedOrderEvent) orderPaymentReceipt {
	t.Helper()
	r, err := f.app.store.recordAuthenticatedOrderEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *paymentFixture) current(t *testing.T, e authenticatedOrderEvent) OrderPaymentProjection {
	t.Helper()
	var out OrderPaymentProjection
	err := pgx.BeginFunc(context.Background(), f.admin, func(tx pgx.Tx) error {
		var err error
		out, err = orderPaymentProjectionSQL(context.Background(), tx, e.WorkspaceID, e.OwnerID, e.OrderID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOrderPaymentFactValidation(t *testing.T) {
	e := authenticatedOrderEvent{Scope: orderPaymentScope{"source", "merchant", "test"}, WorkspaceID: newID("ws"), OwnerID: newID("usr"), OrderID: newID("ord"), EventID: "notice", Kind: "payment", TransactionID: "transaction", Currency: "CNY", AmountMinor: 1, OccurredAt: time.Now().UTC()}
	if !e.valid() {
		t.Fatal("valid payment rejected")
	}
	for _, mutate := range []func(*authenticatedOrderEvent){
		func(x *authenticatedOrderEvent) { x.OwnerID = x.WorkspaceID },
		func(x *authenticatedOrderEvent) { x.Scope.Merchant = "a/b" },
		func(x *authenticatedOrderEvent) { x.Scope.Environment = "sandbox" },
		func(x *authenticatedOrderEvent) { x.AmountMinor = 1_000_000_000_001 },
		func(x *authenticatedOrderEvent) { x.AmountMinor = 0 },
		func(x *authenticatedOrderEvent) { x.OccurredAt = time.Time{} },
		func(x *authenticatedOrderEvent) { x.Currency = "CREDIT" },
		func(x *authenticatedOrderEvent) { x.Kind = "refund" },
		func(x *authenticatedOrderEvent) { x.RefundID = "unexpected" },
	} {
		v := e
		mutate(&v)
		if v.valid() {
			t.Fatal("invalid payment accepted")
		}
	}
	e.Kind, e.RefundID = "refund", "refund"
	if !e.valid() || e.economicID() != e.RefundID {
		t.Fatal("valid refund rejected")
	}
	earlier := e
	earlier.OccurredAt = e.OccurredAt.Add(-time.Nanosecond)
	if payloadHash(e) == payloadHash(earlier) {
		t.Fatal("lost nanosecond precision")
	}
}

func TestPostgresOrderPaymentConcurrentEconomicIdentity(t *testing.T) {
	f := newPaymentFixture(t)
	e := f.order(t, true)
	const clients = 20
	gates := make(chan struct{})
	results := make(chan error, clients)
	ctx := context.Background()
	for i := 0; i < clients; i++ {
		conn := deliveryAdmin(t, f.dsn)
		notification := e
		if i >= 10 {
			notification.EventID = fmt.Sprintf("notice-%d", i)
		}
		go func(c *pgx.Conn, event authenticatedOrderEvent) {
			<-gates
			err := pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
				receipt, collision, err := recordOrderPaymentSQL(ctx, tx, event)
				if err == nil && (collision || receipt.Projection.CreditState != "ISSUED") {
					return errors.New("concurrent notification produced a conflicting outcome")
				}
				return err
			})
			results <- err
		}(conn, notification)
	}
	close(gates)
	for i := 0; i < clients; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_payment_events WHERE order_id=$1", e.OrderID); n != 11 {
		t.Fatal("notification count", n)
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_payment_facts WHERE order_id=$1", e.OrderID); n != 1 {
		t.Fatal("economic fact count", n)
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_payment_credits WHERE order_id=$1", e.OrderID); n != 1 {
		t.Fatal("credit count", n)
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_postings WHERE journal_id=(SELECT journal_id FROM studio_order_payment_credits WHERE order_id=$1)", e.OrderID); n != 2 {
		t.Fatal("unbalanced or duplicate postings", n)
	}
	first := f.current(t, e)
	if first.PaymentState != "RECEIVED" || first.PaidMinor != "1200" {
		t.Fatal(first)
	}
	e.EventID, e.TransactionID = newID("event"), newID("economic")
	multiple := f.record(t, e).Projection
	if multiple.PaymentState != "REVIEW" || multiple.PaidMinor != "2400" || multiple.GrantedCreditMicros != first.GrantedCreditMicros {
		t.Fatal(multiple)
	}
}

func TestPostgresOrderPaymentBindingCollisionAndThirdOrder(t *testing.T) {
	f := newPaymentFixture(t)
	e := f.order(t, false)
	_, err := f.app.store.recordAuthenticatedOrderEvent(e)
	requireOperation(t, err, "ORDER_PAYMENT_SCOPE_MISMATCH")
	if err = f.app.store.bindOrderPayment(e.WorkspaceID, e.OwnerID, e.OrderID, e.Scope); err != nil {
		t.Fatal(err)
	}
	if err = f.admin.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&e.OccurredAt); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*authenticatedOrderEvent){
		func(v *authenticatedOrderEvent) { v.Scope.Source = "other-source" },
		func(v *authenticatedOrderEvent) { v.Scope.Merchant = "other-shop" },
		func(v *authenticatedOrderEvent) { v.Scope.Environment = "live" },
	} {
		other := e
		change(&other)
		_, err = f.app.store.recordAuthenticatedOrderEvent(other)
		requireOperation(t, err, "ORDER_PAYMENT_SCOPE_MISMATCH")
	}
	foreign := e
	foreign.OwnerID = newID("usr")
	_, err = f.app.store.recordAuthenticatedOrderEvent(foreign)
	requireOperation(t, err, "ORDER_INTENT_NOT_FOUND")
	foreign = e
	foreign.WorkspaceID = newID("ws")
	_, err = f.app.store.recordAuthenticatedOrderEvent(foreign)
	requireOperation(t, err, "ORDER_INTENT_NOT_FOUND")
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_payment_events") != 0 {
		t.Fatal("unbound fact reached the inbox")
	}
	original := f.record(t, e)
	claimant := f.order(t, true)
	claimant.EventID = e.EventID
	for i := 0; i < 2; i++ {
		_, err = f.app.store.recordAuthenticatedOrderEvent(claimant)
		requireOperation(t, err, "ORDER_PAYMENT_EVENT_CONFLICT")
	}
	third := f.order(t, true)
	third.TransactionID = claimant.TransactionID
	blocked := f.record(t, third).Projection
	if blocked.PaymentState != "REVIEW" || blocked.PaidMinor != "0" || blocked.GrantedCreditMicros != "0" {
		t.Fatal("quarantined identifier escaped onto a third purchase", blocked)
	}
	if got := f.current(t, e); got.CreditState != "REVIEW" || got.GrantedCreditMicros != original.Projection.GrantedCreditMicros {
		t.Fatal("lost original purchase", got)
	}
	if again := f.record(t, e); again != original {
		t.Fatal("original delivery receipt changed")
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_payment_conflicts"); n != 1 {
		t.Fatal("duplicate conflict", n)
	}
	other := f.order(t, true)
	other.TransactionID = e.TransactionID
	if got := f.record(t, other).Projection; got.GrantedCreditMicros != "0" || got.PaymentState != "REVIEW" {
		t.Fatal("transaction reused across orders", got)
	}
	duplicate := f.order(t, true)
	f.record(t, duplicate)
	duplicate.EventID = newID("event")
	duplicate.AmountMinor++
	if got := f.record(t, duplicate); got.Disposition != "DISPUTED" || got.Projection.PaymentState != "REVIEW" {
		t.Fatal("amount changed for the same transaction", got)
	}
	unbound := f.order(t, false)
	live := f.scope
	live.Environment = "live"
	requireOperation(t, f.app.store.bindOrderPayment(unbound.WorkspaceID, unbound.OwnerID, unbound.OrderID, live), "ORDER_PAYMENT_SCOPE_MISMATCH")
}
