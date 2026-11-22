package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func decodePublicPaymentStatus(t *testing.T, raw []byte) OrderPaymentProjection {
	t.Helper()
	if _, ok := orderFields(raw, "order_id", "currency", "currency_exponent", "environment", "payment_state", "refund_state",
		"credit_state", "paid_minor", "refunded_minor", "granted_credit_micros", "reversed_credit_micros"); !ok {
		t.Fatal("payment status contains missing, private or unexpected fields")
	}
	return decode[OrderPaymentProjection](t, raw)
}

func TestOrderPaymentStatusPublicFields(t *testing.T) {
	unbound := OrderPaymentProjection{OrderID: newID("ord"), Currency: "JPY", PaymentState: "UNBOUND", RefundState: "NONE", CreditState: "NOT_ISSUED",
		PaidMinor: "0", RefundedMinor: "0", GrantedCreditMicros: "0", ReversedCreditMicros: "0"}
	raw, err := json.Marshal(unbound)
	if err != nil || decodePublicPaymentStatus(t, raw) != unbound {
		t.Fatal("unbound public response lost its zero scale, empty environment or economic amounts")
	}
}

func TestOrderPaymentStatusNeedsPersistentAccounts(t *testing.T) {
	_, s := testApp(t, Config{})
	code, raw, _ := request(t, s, "GET", "/api/v1/order-intents/"+newID("ord")+"/payment-status", "", nil)
	if code != 501 || !bytes.Contains(raw, []byte("ORDER_INTENT_STORAGE_REQUIRED")) {
		t.Fatal("demo exposed a payment projection", code)
	}
}

func TestPostgresOrderPaymentStatusPrivateReadOnlyHTTP(t *testing.T) {
	cfg := Config{DatabaseURL: testDatabase(t), AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(),
		OrderCatalogFile: filepath.Join("..", "..", "config", "order-catalog.example.json")}
	a, s := testApp(t, cfg)
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	buyer, outsider, otherSession := authClient(t), authClient(t), authClient(t)
	one := registerFixture(t, buyer, s.URL, "payment-owner@example.test")
	two := registerFixture(t, outsider, s.URL, "payment-foreign@example.test")
	const base = "/api/v1/order-intents"
	call := func(client *http.Client, method, route, key string, body any, want int) ([]byte, http.Header) {
		t.Helper()
		// Do not echo the response if a private projection ever fails a gate.
		code, raw, h := authRequest(t, client, s.URL, method, route, key, "", body)
		if code != want {
			t.Fatalf("order-status HTTP got %d, want %d", code, want)
		}
		return raw, h
	}
	createKey := newID("intent")
	raw, _ := call(buyer, "POST", base, createKey, orderStoreInput(a), 201)
	order := decode[OrderIntent](t, raw)
	path := base + "/" + order.ID + "/payment-status"
	scope := orderPaymentScope{Source: "private-fixture-adapter", Merchant: "private-fixture-shop", Environment: "test"}
	event := authenticatedOrderEvent{Scope: scope, WorkspaceID: one.Workspace.ID, OwnerID: one.User.ID, OrderID: order.ID,
		EventID: "private-capture-notice", Kind: "payment", TransactionID: "private-economic-id", Currency: order.Product.Currency,
		AmountMinor: 1200}
	refundID := "private-refund-id"
	get := func(wantPayment, wantRefund, wantCredit, paid, refunded, granted, environment string) {
		t.Helper()
		raw, h := call(buyer, "GET", path, "", nil, 200)
		if h.Get("Cache-Control") != "no-store" {
			t.Fatal("private payment status was cacheable")
		}
		status := decodePublicPaymentStatus(t, raw)
		for _, private := range []string{one.User.ID, one.Workspace.ID, one.User.Email, scope.Source, scope.Merchant, event.EventID, event.TransactionID, refundID} {
			if bytes.Contains(raw, []byte(private)) {
				t.Fatal("private merchant or account identifier in response")
			}
		}
		if status.OrderID != order.ID || status.Currency != order.Product.Currency ||
			status.CurrencyExponent != order.Product.CurrencyExponent || status.Environment != environment || status.PaymentState != wantPayment ||
			status.RefundState != wantRefund || status.CreditState != wantCredit || status.PaidMinor != paid || status.RefundedMinor != refunded ||
			status.GrantedCreditMicros != granted || status.ReversedCreditMicros != "0" {
			t.Fatal("private projection disagrees with accepted economic or CREDIT evidence")
		}
	}
	get("UNBOUND", "NONE", "NOT_ISSUED", "0", "0", "0", "")
	another, _ := call(buyer, "POST", base, newID("intent"), orderStoreInput(a), 201)
	cancelledPath := base + "/" + decode[OrderIntent](t, another).ID
	call(buyer, "POST", cancelledPath+"/cancel", newID("cancel"), map[string]int{"expected_version": 1}, 200)
	cancelled, _ := call(buyer, "GET", cancelledPath+"/payment-status", "", nil, 200)
	noEvidence := decode[OrderPaymentProjection](t, cancelled)
	if noEvidence.PaymentState != "UNBOUND" || noEvidence.RefundState != "NONE" || noEvidence.RefundedMinor != "0" || noEvidence.CreditState != "NOT_ISSUED" {
		t.Fatal("intent cancellation was presented as a payment or refund")
	}
	call(authClient(t), "GET", path, "", nil, 401)
	foreign, _ := call(outsider, "GET", path, "", nil, 404)
	absent, _ := call(buyer, "GET", base+"/"+newID("ord")+"/payment-status", "", nil, 404)
	type errorEnvelope struct {
		Error struct{ Code, Message string }
	}
	foreignError, absentError := decode[errorEnvelope](t, foreign), decode[errorEnvelope](t, absent)
	if foreignError != absentError || foreignError.Error.Code != "ORDER_INTENT_NOT_FOUND" {
		t.Fatal("foreign order distinguishable from unknown order")
	}
	call(otherSession, "POST", "/api/v1/auth/login", "", map[string]string{"email": one.User.Email, "password": "fixture-password-123"}, 200)
	// Independently cover the owner and workspace predicates with valid
	// accounts and session tokens, just as the 033 intent tests do.
	for _, tc := range []struct {
		client    *http.Client
		workspace string
	}{{outsider, one.Workspace.ID}, {otherSession, two.Workspace.ID}} {
		p, err := a.currentPrincipal(operationsRequest(t, tc.client, s))
		if err != nil || p == nil {
			t.Fatal("could not read fixture session")
		}
		if _, err = admin.Exec(context.Background(), "UPDATE studio_sessions SET workspace_id=$1 WHERE id=$2", tc.workspace, p.SessionID); err != nil {
			t.Fatal("could not prepare independent scope check")
		}
		call(tc.client, "GET", path, "", nil, 404)
	}
	if err := a.store.bindOrderPayment(one.Workspace.ID, one.User.ID, order.ID, scope); err != nil {
		t.Fatal("fixture could not bind test payment scope", err)
	}
	get("AWAITING", "NONE", "NOT_ISSUED", "0", "0", "0", "test")
	for _, route := range []string{path, base + "/" + order.ID + "/pay", base + "/" + order.ID + "/refund", "/api/v1/payments/callback"} {
		call(buyer, "POST", route, "", map[string]string{"payment_id": event.TransactionID, "amount_minor": "1200"}, 404)
	}
	get("AWAITING", "NONE", "NOT_ISSUED", "0", "0", "0", "test")
	event.OccurredAt = paymentFixtureTime(t, admin)
	if outboxCount(t, admin, "SELECT count(*) FROM studio_order_payment_events WHERE order_id=$1", order.ID) != 0 {
		t.Fatal("ordinary HTTP installed a payment event")
	}
	if _, err := a.store.recordAuthenticatedOrderEvent(event); err != nil {
		t.Fatal("fixture payment not accepted", err)
	}
	get("RECEIVED", "NONE", "ISSUED", "1200", "0", order.Product.CreditMicros, "test")
	call(outsider, "GET", path, "", nil, 404)
	replay, _ := call(buyer, "GET", base+"/receipt?operation=create", createKey, nil, 200)
	if old := decode[OrderIntent](t, replay); old != order {
		t.Fatal("payment altered original intent receipt")
	}
	denied, _ := call(buyer, "POST", base+"/"+order.ID+"/cancel", newID("cancel"), map[string]int{"expected_version": 1}, 409)
	if !bytes.Contains(denied, []byte("ORDER_PAYMENT_RECORDED")) {
		t.Fatal("new cancellation did not recognize the recorded payment")
	}
	event.EventID, event.Kind, event.RefundID, event.AmountMinor = "private-refund-notice", "refund", refundID, 300
	event.OccurredAt = paymentFixtureTime(t, admin)
	if _, err := a.store.recordAuthenticatedOrderEvent(event); err != nil {
		t.Fatal("fixture refund not accepted", err)
	}
	get("RECEIVED", "PARTIAL", "REVIEW", "1200", "300", order.Product.CreditMicros, "test")
	get("RECEIVED", "PARTIAL", "REVIEW", "1200", "300", order.Product.CreditMicros, "test")
	if outboxCount(t, admin, "SELECT count(*) FROM studio_order_payment_events WHERE order_id=$1", order.ID) != 2 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_order_payment_credits WHERE order_id=$1", order.ID) != 1 {
		t.Fatal("reading status duplicated economic evidence or the original grant")
	}
	beforeCapture, _ := call(buyer, "POST", base, newID("intent"), orderStoreInput(a), 201)
	latePayment := decode[OrderIntent](t, beforeCapture)
	if err := a.store.bindOrderPayment(one.Workspace.ID, one.User.ID, latePayment.ID, scope); err != nil {
		t.Fatal("could not bind second test order", err)
	}
	orphan := event
	orphan.OrderID, orphan.EventID, orphan.TransactionID, orphan.RefundID = latePayment.ID, "private-early-notice", "private-late-original", "private-early-refund"
	orphan.OccurredAt = paymentFixtureTime(t, admin)
	if _, err := a.store.recordAuthenticatedOrderEvent(orphan); err != nil {
		t.Fatal("fixture could not record refund before original payment", err)
	}
	beforeCapture, _ = call(buyer, "GET", base+"/"+latePayment.ID+"/payment-status", "", nil, 200)
	waiting := decodePublicPaymentStatus(t, beforeCapture)
	if waiting.PaymentState != "AWAITING" || waiting.RefundState != "PENDING_MATCH" || waiting.CreditState != "REVIEW" ||
		waiting.PaidMinor != "0" || waiting.RefundedMinor != "300" || waiting.GrantedCreditMicros != "0" || waiting.ReversedCreditMicros != "0" {
		var outsideFirstObservation bool
		if err := admin.QueryRow(context.Background(), `SELECT f.occurred_at < b.bound_at OR (f.occurred_at, f.nano_remainder) > (f.first_received_at, 0)
		   FROM studio_order_payment_facts f JOIN studio_order_payment_bindings b USING(order_id) WHERE f.order_id=$1 AND f.kind='refund'`, latePayment.ID).Scan(&outsideFirstObservation); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("refund before original capture: payment=%s refund=%s credit=%s paid=%s refunded=%s issued=%s reversed=%s outside observation window=%v", waiting.PaymentState, waiting.RefundState, waiting.CreditState, waiting.PaidMinor, waiting.RefundedMinor, waiting.GrantedCreditMicros, waiting.ReversedCreditMicros, outsideFirstObservation)
	}
	s.Close()
	a.Close()
	cfg.OrderCatalogFile = ""
	a, s = testApp(t, cfg)
	get("RECEIVED", "PARTIAL", "REVIEW", "1200", "300", order.Product.CreditMicros, "test")
	// Simulate the account control state; authenticate a fresh limited
	// session and verify this ordinary route remains unavailable.
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `WITH origin AS (
 INSERT INTO studio_account_restrictions(id,target_user_id,reason_code,public_notice,evidence_ref,created_by)
 VALUES($1,$2,'manual_review','notice','private-control-evidence',$3) RETURNING id,target_user_id)
 INSERT INTO studio_account_controls(user_id,state,version,active_restriction_id) SELECT target_user_id,'SUSPENDED',2,id FROM origin`,
		newID("arst"), one.User.ID, two.User.ID); err != nil {
		t.Fatal("could not prepare account restriction")
	}
	limited := authClient(t)
	call(limited, "POST", "/api/v1/auth/login", "", map[string]string{"email": one.User.Email, "password": "fixture-password-123"}, 200)
	restricted, _ := call(limited, "GET", path, "", nil, 403)
	if !strings.Contains(string(restricted), "ACCOUNT_RESTRICTED") || bytes.Contains(restricted, []byte(event.TransactionID)) {
		t.Fatal("limited account exposed ordinary payment status")
	}
}
