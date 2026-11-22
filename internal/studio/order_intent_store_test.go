package studio

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func orderStoreInput(a *App) OrderIntentCreate {
	p := a.orderCatalog.products[0]
	return OrderIntentCreate{ProductID: p.ID, CatalogDigest: p.CatalogDigest, ProductDigest: p.ProductDigest}
}
func TestPostgresOrderIntentPrivateSessionsAndStableHistory(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	f.cfg.OrderCatalogFile = filepath.Join("..", "..", "config", "order-catalog.example.json")
	a, s := f.open(t)
	buyer, second, outsider := authClient(t), authClient(t), authClient(t)
	one := registerFixture(t, buyer, s.URL, "order-scope-one@example.test")
	two := registerFixture(t, outsider, s.URL, "order-scope-two@example.test")
	presentationRequest(t, second, s.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": one.User.Email, "password": "fixture-password-123"}, 200)
	p, e := a.currentPrincipal(operationsRequest(t, buyer, s))
	if e != nil {
		t.Fatal(e)
	}
	sameOwner, e := a.currentPrincipal(operationsRequest(t, second, s))
	if e != nil {
		t.Fatal(e)
	}
	other, e := a.currentPrincipal(operationsRequest(t, outsider, s))
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	input := orderStoreInput(a)
	key := newID("create")
	first, replayed, e := a.createOrderIntent(p, input, key)
	if e != nil || replayed || first.State != "PENDING" || first.Version != 1 || first.Product != a.orderCatalog.products[0] || first.CancelledAt != nil {
		t.Fatal("new order snapshot", e)
	}
	for i := 0; i < 2; i++ {
		if _, _, e = a.createOrderIntent(p, input, newID("create")); e != nil {
			t.Fatal(e)
		}
	}
	page, e := a.listOrderIntents(p, "", 1)
	if e != nil || len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal("missing cursor", e)
	}
	cursor := *page.NextCursor
	// Put another valid account/session on the purchaser's workspace. Also
	// place a second, valid session belonging to the purchaser on another
	// workspace. Both predicates must be enforced independently.
	if _, e = admin.Exec(ctx, "UPDATE studio_sessions SET workspace_id=$1 WHERE id=$2", one.Workspace.ID, other.SessionID); e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, "UPDATE studio_sessions SET workspace_id=$1 WHERE id=$2", two.Workspace.ID, sameOwner.SessionID); e != nil {
		t.Fatal(e)
	}
	other, e = a.currentPrincipal(operationsRequest(t, outsider, s))
	if e != nil {
		t.Fatal(e)
	}
	sameOwner, e = a.currentPrincipal(operationsRequest(t, second, s))
	if e != nil {
		t.Fatal(e)
	}
	for _, actor := range []*principal{other, sameOwner} {
		_, e = a.orderIntentReceipt(actor, "create", key)
		requireOperation(t, e, "ORDER_INTENT_NOT_FOUND")
		_, e = a.orderIntentDetail(actor, first.ID)
		requireOperation(t, e, "ORDER_INTENT_NOT_FOUND")
		_, _, e = a.cancelOrderIntent(actor, first.ID, 1, newID("cancel"))
		requireOperation(t, e, "ORDER_INTENT_NOT_FOUND")
		if empty, e := a.listOrderIntents(actor, "", 1); e != nil || len(empty.Items) != 0 {
			t.Fatal("orders visible outside scope", e)
		}
		_, e = a.listOrderIntents(actor, cursor, 1)
		requireOperation(t, e, "INVALID_CURSOR")
		own, again, e := a.createOrderIntent(actor, input, key)
		if e != nil || again || own.ID == first.ID {
			t.Fatal("different scope original key collided", e)
		}
	}
	cancelKey := newID("cancel")
	cancelled, _, e := a.cancelOrderIntent(p, first.ID, 1, cancelKey)
	if e != nil || cancelled.State != "CANCELLED" || cancelled.Version != 2 || cancelled.CancelledAt == nil || cancelled.Product != first.Product {
		t.Fatal("cancel changed product", e)
	}
	for _, op := range []string{"create", "cancel"} {
		k := key
		want := first
		if op == "cancel" {
			k = cancelKey
			want = cancelled
		}
		got, e := a.orderIntentReceipt(p, op, k)
		if e != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("original snapshot was rewritten", op, e)
		}
	}
	if _, _, e = a.cancelOrderIntent(p, first.ID, 2, newID("cancel")); e == nil {
		t.Fatal("cancelled twice")
	}
	_, _, e = a.cancelOrderIntent(p, first.ID, 2, cancelKey)
	requireOperation(t, e, "IDEMPOTENCY_CONFLICT")
	data, e := a.orderIntentDetail(p, first.ID)
	if e != nil || data.Version != 2 {
		t.Fatal("latest detail", e)
	}
	seen := map[string]bool{}
	next := ""
	for {
		page, e := a.listOrderIntents(p, next, 1)
		if e != nil || len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatal("incorrect keyset page", e)
		}
		seen[page.Items[0].ID] = true
		if page.NextCursor == nil {
			break
		}
		next = *page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatal("wrong number of personal records", len(seen))
	}
	if _, e = admin.Exec(ctx, `UPDATE studio_order_intents SET product_name='changed' WHERE id=$1`, first.ID); e == nil {
		t.Fatal("product snapshot was mutable")
	}
	if _, e = admin.Exec(ctx, "DELETE FROM studio_order_intent_receipts WHERE order_id=$1", first.ID); e == nil {
		t.Fatal("receipt was mutable")
	}
	if _, e = admin.Exec(ctx, "UPDATE studio_order_intents SET state='PAID' WHERE id=$1", first.ID); e == nil {
		t.Fatal("order intent admitted a collected-payment state without an authorized financial event")
	}
}

func TestPostgresOrderIntentCapacityConcurrentCreateAndCancel(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	f.cfg.OrderCatalogFile = filepath.Join("..", "..", "config", "order-catalog.example.json")
	a, s := f.open(t)
	client := authClient(t)
	one := registerFixture(t, client, s.URL, "order-cap@example.test")
	p, e := a.currentPrincipal(operationsRequest(t, client, s))
	if e != nil {
		t.Fatal(e)
	}
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	ctx := context.Background()
	in := orderStoreInput(a)
	key := newID("create")
	first, _, e := a.createOrderIntent(p, in, key)
	if e != nil {
		t.Fatal(e)
	}
	// Seed archived history through the allowed create and cancel transitions
	// in one transaction. Only the one genuine pending order consumes capacity.
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO studio_order_intents(id,workspace_id,owner_user_id,product_id,product_name,catalog_version,catalog_digest,product_digest,currency,currency_exponent,amount_minor,credit_micros,state,version)
	  SELECT 'ord_'||md5('archived-order-'||i),o.workspace_id,o.owner_user_id,o.product_id,o.product_name,o.catalog_version,o.catalog_digest,o.product_digest,o.currency,o.currency_exponent,o.amount_minor,o.credit_micros,'PENDING',1
	  FROM studio_order_intents o CROSS JOIN generate_series(1,260) i WHERE o.id=$1`, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, "UPDATE studio_order_intents SET state='CANCELLED',version=2,cancelled_at=clock_timestamp() WHERE id<>$1 AND owner_user_id=$2", first.ID, one.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	for i := 1; i < 31; i++ {
		if _, _, e = a.createOrderIntent(p, in, newID("create")); e != nil {
			t.Fatal("archived history occupied pending capacity", i, e)
		}
	}
	type request struct {
		key string
		run func(*App, string) (OrderIntent, bool, error)
	}
	type result struct {
		key   string
		order OrderIntent
		err   error
	}
	race := func(reqs []request) []result {
		t.Helper()
		barrier, e := admin.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer barrier.Rollback(ctx)
		if _, e = barrier.Exec(ctx, "SELECT id FROM studio_users WHERE id=$1 FOR UPDATE", one.User.ID); e != nil {
			t.Fatal(e)
		}
		done := make(chan result, len(reqs))
		var pids []int32
		for _, req := range reqs {
			conn := deliveryAdmin(t, f.cfg.DatabaseURL)
			pids = append(pids, int32(conn.PgConn().PID()))
			writer := &App{config: Config{AuthMode: "required"}, store: &postgresStore{conn: conn}, orderCatalog: a.orderCatalog}
			go func() { o, _, e := req.run(writer, req.key); done <- result{req.key, o, e} }()
		}
		awaitRunLeaseWaiters(t, deliveryAdmin(t, f.cfg.DatabaseURL), pids...)
		if e = barrier.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		got := make([]result, 0, len(reqs))
		for range reqs {
			got = append(got, <-done)
		}
		return got
	}
	create := func(w *App, k string) (OrderIntent, bool, error) { return w.createOrderIntent(p, in, k) }
	loser := ""
	won := 0
	for _, r := range race([]request{{newID("create"), create}, {newID("create"), create}}) {
		if r.err == nil {
			won++
		} else {
			requireOperation(t, r.err, "ORDER_INTENT_PENDING_LIMIT")
			loser = r.key
		}
	}
	if won != 1 || loser == "" || outboxCount(t, admin, "SELECT count(*) FROM studio_order_intents WHERE state='PENDING'") != 32 {
		t.Fatal("last slot was not serialized")
	}
	// A second session for this purchaser on another workspace cannot evade
	// the account-wide limit and must not discover the first workspace's list.
	hostClient, secondClient := authClient(t), authClient(t)
	host := registerFixture(t, hostClient, s.URL, "order-other-space@example.test")
	presentationRequest(t, secondClient, s.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": one.User.Email, "password": "fixture-password-123"}, 200)
	second, e := a.currentPrincipal(operationsRequest(t, secondClient, s))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, "UPDATE studio_sessions SET workspace_id=$1 WHERE id=$2", host.Workspace.ID, second.SessionID); e != nil {
		t.Fatal(e)
	}
	second, e = a.currentPrincipal(operationsRequest(t, secondClient, s))
	if e != nil {
		t.Fatal(e)
	}
	if page, e := a.listOrderIntents(second, "", 1); e != nil || len(page.Items) != 0 {
		t.Fatal("cross-workspace records were disclosed", e)
	}
	_, _, e = a.createOrderIntent(second, in, newID("create"))
	requireOperation(t, e, "ORDER_INTENT_PENDING_LIMIT")
	_, e = a.orderIntentReceipt(p, "create", loser)
	requireOperation(t, e, "ORDER_INTENT_NOT_FOUND")
	if got, replayed, e := a.createOrderIntent(p, in, key); e != nil || !replayed || got.ID != first.ID {
		t.Fatal("capacity broke original key", e)
	}
	cancel := func(w *App, k string) (OrderIntent, bool, error) { return w.cancelOrderIntent(p, first.ID, 1, k) }
	won = 0
	for _, r := range race([]request{{newID("cancel"), cancel}, {newID("cancel"), cancel}}) {
		got, e := a.orderIntentReceipt(p, "cancel", r.key)
		if r.err == nil {
			won++
			if e != nil || got.State != "CANCELLED" || got.ID != first.ID {
				t.Fatal("winner lost original cancellation")
			}
		} else {
			requireOperation(t, r.err, "ORDER_INTENT_VERSION_CONFLICT")
			requireOperation(t, e, "ORDER_INTENT_NOT_FOUND")
		}
	}
	if won != 1 {
		t.Fatal("concurrent cancellation did not serialize")
	}
	if got, replayed, e := a.createOrderIntent(p, in, loser); e != nil || replayed || got.ID == first.ID {
		t.Fatal("cancellation did not free a live slot", e)
	}
	for _, table := range []string{"studio_ledger_journals", "studio_ledger_postings", "studio_ledger_reservations", "studio_runs", "studio_tasks"} {
		if n := outboxCount(t, admin, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatal(fmt.Sprintf("order intent changed %s", table))
		}
	}
}
