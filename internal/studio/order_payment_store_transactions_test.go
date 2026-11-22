package studio

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresOrderPaymentCancellationSerializesWithCapture(t *testing.T) {
	f := newPaymentFixture(t)
	ctx := context.Background()
	controlPID := f.app.store.conn.PgConn().PID()
	cancel := func(e authenticatedOrderEvent, key string) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, _, err := f.app.cancelOrderIntent(f.owner, e.OrderID, 1, key)
			done <- err
		}()
		return done
	}
	first := f.order(t, true)
	tx, err := f.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	r, conflict, err := recordOrderPaymentSQL(ctx, tx, first)
	if err != nil || conflict || r.Projection.CreditState != "ISSUED" {
		t.Fatal(r, err)
	}
	cancelled := cancel(first, newID("cancel"))
	generationWaitWorkspaceBlock(t, tx, controlPID)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, <-cancelled, "ORDER_PAYMENT_RECORDED")
	second := f.order(t, true)
	_, err = f.admin.Exec(ctx, `CREATE FUNCTION hold_purchase_cancellation() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(911734,3502); RETURN NEW; END $$;
 CREATE TRIGGER hold_purchase_cancel BEFORE UPDATE ON studio_order_intents
 FOR EACH ROW EXECUTE FUNCTION hold_purchase_cancellation()`)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := f.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(911734,3502)"); err != nil {
		t.Fatal(err)
	}
	key := newID("cancel")
	cancelled = cancel(second, key)
	generationWaitWorkspaceBlock(t, barrier, controlPID)
	cash := deliveryAdmin(t, f.dsn)
	captured := make(chan error, 1)
	go func() {
		captured <- pgx.BeginFunc(ctx, cash, func(tx pgx.Tx) error {
			result, conflict, err := recordOrderPaymentSQL(ctx, tx, second)
			if err == nil && (conflict || result.Projection.PaymentState != "REVIEW" || result.Projection.GrantedCreditMicros != "0") {
				return errors.New("cancelled order issued CREDIT")
			}
			return err
		})
	}()
	// Verify that the independent capture is queued behind the real cancellation,
	// while that cancellation is still blocked in its own transaction.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waits bool
		if err = barrier.QueryRow(ctx, "SELECT $1::int=ANY(pg_blocking_pids($2))", int32(controlPID), int32(cash.PgConn().PID())).Scan(&waits); err != nil {
			t.Fatal(err)
		}
		if waits {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capture did not wait for the order lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-cancelled; err != nil {
		t.Fatal(err)
	}
	if err = <-captured; err != nil {
		t.Fatal(err)
	}
	again, replayed, err := f.app.cancelOrderIntent(f.owner, second.OrderID, 1, key)
	if err != nil || !replayed || again.State != "CANCELLED" {
		t.Fatal("late capture displaced original cancellation", err)
	}
	if got := f.current(t, second); got.PaidMinor != "1200" || got.CreditState != "REVIEW" {
		t.Fatal(got)
	}
}

func TestPostgresOrderPaymentCommitAckLossAndRestart(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	f.cfg.OrderCatalogFile = filepath.Join("..", "..", "config", "order-catalog.example.json")
	a, s := f.open(t)
	env := paymentFixtureForApp(t, a, s, direct)
	e := env.order(t, true)
	ctx := context.Background()
	_, err := env.admin.Exec(ctx, `CREATE FUNCTION hold_payment_notice() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(911734,3503); RETURN NEW; END $$;
 CREATE TRIGGER hold_payment_notice BEFORE INSERT ON studio_order_payment_events FOR EACH ROW EXECUTE FUNCTION hold_payment_notice()`)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := env.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(911734,3503)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		partial, err := a.store.recordAuthenticatedOrderEvent(e)
		var op *operationError
		if partial == (orderPaymentReceipt{}) && errors.As(err, &op) {
			done <- op.status
			return
		}
		done <- 0
	}()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, done)
	s.Close()
	a.Close()
	f.cfg.DatabaseURL = direct
	f.cfg.OrderCatalogFile = ""
	a, _ = f.open(t)
	original, err := a.store.orderPaymentEventReceipt(e.Scope, e.EventID)
	if err != nil || original.Projection.CreditState != "ISSUED" {
		t.Fatal(original, err)
	}
	replay, err := a.store.recordAuthenticatedOrderEvent(e)
	if err != nil || replay != original {
		t.Fatal("lost immutable acknowledgement", err)
	}
	if n := outboxCount(t, env.admin, "SELECT count(*) FROM studio_ledger_journals WHERE resource_id=$1", e.OrderID); n != 1 {
		t.Fatal("duplicate or missing purchase journal", n)
	}
}

func TestPostgresOrderPaymentUpgrade034PreservesSupportIntentAndLedger(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	admin := deliveryAdmin(t, dsn)
	old := workflowOldMigrations(t, "034")
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing');
 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing');
 INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_previous','previous@unit.test','Owner',decode('00','hex'));
 INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_previous','usr_previous','previous');
 INSERT INTO studio_order_intents(id,workspace_id,owner_user_id,product_id,product_name,catalog_version,catalog_digest,
 product_digest,currency,currency_exponent,amount_minor,credit_micros,state,version)
 VALUES('ord_'||repeat('1',32),'ws_previous','usr_previous','old-sku','旧商品','old',repeat('a',64),repeat('b',64),'JPY',0,33,876543,'PENDING',1);
 INSERT INTO studio_order_intent_receipts(owner_user_id,workspace_id,operation,key_hash,request_hash,order_id,snapshot)
 SELECT owner_user_id,workspace_id,'create',decode(repeat('12',32),'hex'),decode(repeat('34',32),'hex'),id,to_jsonb(t)
 FROM studio_order_intents t;
 WITH origin AS (
 INSERT INTO studio_support_tickets(id,workspace_id,owner_user_id,category,subject,state,version,created_at,updated_at)
 VALUES('tkt_'||repeat('a',32),'ws_previous','usr_previous','billing','旧版咨询','OPEN',1,now(),now()) RETURNING *)
 INSERT INTO studio_support_messages(ticket_id,workspace_id,version,actor_user_id,operation,body,created_at)
 SELECT id,workspace_id,version,owner_user_id,'create','旧版私有服务消息',created_at FROM origin;
 INSERT INTO studio_support_receipts(workspace_id,actor_user_id,operation,key_hash,request_hash,ticket_id,version)
 SELECT workspace_id,actor_user_id,operation,decode(repeat('56',32),'hex'),decode(repeat('78',32),'hex'),ticket_id,version FROM studio_support_messages`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: "ws_previous", OperationKey: newID("oldgrant"), Reason: "fixture", EvidenceRef: "fixture:old", AmountMicros: 1234})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_order_intents", "studio_order_intent_receipts", "studio_support_tickets", "studio_support_messages", "studio_support_receipts", "studio_ledger_operations", "studio_ledger_journals", "studio_ledger_postings", "studio_ledger_accounts", "studio_ledger_wallets"}
	before := schedulerOriginalSnapshot(t, admin, tables)
	store, err := openPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, admin, tables)) {
		t.Fatal("035 altered an existing order, support message or ledger")
	}
	if n := outboxCount(t, admin, `SELECT (SELECT count(*) FROM studio_order_payment_environment)+(SELECT count(*) FROM studio_order_payment_bindings)
 +(SELECT count(*) FROM studio_order_payment_events)+(SELECT count(*) FROM studio_order_payment_facts)+(SELECT count(*) FROM studio_order_payment_credits)`); n != 0 {
		t.Fatal("upgrade invented merchant or financial evidence", n)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_order_pending_slots WHERE owner_user_id='usr_previous'"); n != 1 {
		t.Fatal("original pending limit was not preserved", n)
	}
	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("034 writer accepted 035", err)
	}
}
