package studio

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func riskReservationFixture(q ledgerQuoteRow, actor string) LedgerReservationRequest {
	return LedgerReservationRequest{QuoteID: q.ID, WorkspaceID: q.WorkspaceID, ProjectID: q.ProjectID, ResourceType: q.ResourceType, ResourceID: newID(q.ResourceType), ConversationID: q.ConversationID, ModelID: q.ModelID, ModelVersion: q.ModelVersion, ProviderMode: q.Price.ProviderMode, RequestHash: q.RequestHash, ActorUserID: actor}
}
func TestPostgresCreditRiskConcurrentWindowAndReviewBalance(t *testing.T) {
	s, dsn, admin := outboxStore(t)
	ctx := context.Background()
	ledgerGrant(t, s, 100000)
	// All new charged admissions share the wallet lock; a genuine unsuccessful
	// started execution keeps its outstanding balance in the reserved account.
	run := ledgerRun(t, s, "live")
	lease := claimFixtureRun(t, s, &run)
	run.Status = "FAILED"
	run.Seq++
	if err := s.saveLeasedRun(&run, lease, run.Seq-1); err != nil {
		t.Fatal(err)
	}
	if run.Billing.State != "REVIEW" || run.Billing.ReservedMicros != 3000 {
		t.Fatal(run.Billing)
	}
	if _, e := admin.Exec(ctx, `INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES('ws_demo',1,true,3100,1000)`); e != nil {
		t.Fatal(e)
	}
	price := LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 100, RequestedUnits: 1}
	makeQuote := func(amount int64) ledgerQuoteRow {
		return ledgerQuote(t, s, "task", "fixed-model", payloadHash(newID("body")), amount, price)
	}
	q := makeQuote(101)
	var err = ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		_, e := reserveLedgerSQL(ctx, tx, riskReservationFixture(q, ""))
		return e
	})
	requireOperation(t, err, "CREDIT_RISK_HELD_LIMIT")
	if ledgerWallet(t, s).ReservedMicros != 3000 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", q.ID) != 1 {
		t.Fatal("decline changed original hold or consumed quote")
	}
	boundary := makeQuote(100)
	if err = ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		_, e := reserveLedgerSQL(ctx, tx, riskReservationFixture(boundary, ""))
		return e
	}); err != nil {
		t.Fatal("exact boundary rejected", err)
	}
	// Pin the historical window through the evaluator argument. Other committed
	// reservations remain immutable and settlement never erases admission counts.
	var last time.Time
	if err = admin.QueryRow(ctx, "SELECT created_at FROM studio_ledger_reservations WHERE quote_id=$1", boundary.ID).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES('ws_demo',2,true,100000,1)`); err != nil {
		t.Fatal(err)
	}
	err = ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		_, reasons, e := evaluateCreditRiskSQL(ctx, tx, "ws_demo", 3100, 1, last)
		if e != nil {
			return e
		}
		if !slices.Equal(reasons, []string{"RATE_LIMIT"}) {
			t.Fatalf("expected committed admission at %v, got %v", last, reasons)
		}
		next := last.UTC().Truncate(time.Minute).Add(time.Minute)
		_, reasons, e = evaluateCreditRiskSQL(ctx, tx, "ws_demo", 3100, 1, next)
		if len(reasons) > 0 {
			t.Fatal("adjacent UTC minute counted previous rows")
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	// Place this concurrency group safely inside the current natural minute.
	// Seed a threshold from the actual pre-existing count so only one slot remains.
	var dbNow time.Time
	if err = admin.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	untilNext := dbNow.UTC().Truncate(time.Minute).Add(time.Minute).Sub(dbNow)
	if untilNext < 8*time.Second {
		time.Sleep(untilNext + 20*time.Millisecond)
	}
	var accepted int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM studio_ledger_reservations WHERE workspace_id='ws_demo' AND created_at>=date_trunc('minute',clock_timestamp(),'UTC')`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES('ws_demo',3,true,100000,$1)`, accepted+1); err != nil {
		t.Fatal(err)
	}
	const n = 5
	reqs := make([]LedgerReservationRequest, n)
	for i := range reqs {
		reqs[i] = riskReservationFixture(makeQuote(1), "")
	}
	start := make(chan struct{})
	results := make(chan error, n)
	var wg sync.WaitGroup
	for _, req := range reqs {
		c, e := pgx.Connect(ctx, dsn)
		if e != nil {
			t.Fatal(e)
		}
		wg.Add(1)
		go func(conn *pgx.Conn, in LedgerReservationRequest) {
			defer wg.Done()
			defer conn.Close(ctx)
			<-start
			results <- pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { _, e := reserveLedgerSQL(ctx, tx, in); return e })
		}(c, req)
	}
	close(start)
	wg.Wait()
	close(results)
	count := 0
	for e := range results {
		if e == nil {
			count++
		} else {
			requireOperation(t, e, "CREDIT_RISK_RATE_LIMIT")
		}
	}
	if count != 1 || ledgerWallet(t, s).ReservedMicros != 3101 {
		t.Fatal("concurrent risk oversubscription", count)
	}
	ledgerAssertBalanced(t, s)
}

func TestPostgresCreditRiskPermitMatchesExactOwnerQuoteAndRollsBack(t *testing.T) {
	a, server, owner, identity, dsn := newModeratedBillingFixture(t, true)
	admin := deliveryAdmin(t, dsn)
	ctx := context.Background()
	incoming := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "credit risk fixture"}
	quote := httpBillingQuote(t, owner, server, incoming.ProjectID, "run", incoming)
	replacement := httpBillingQuote(t, owner, server, incoming.ProjectID, "run", incoming)
	if _, e := admin.Exec(ctx, `INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES($1,1,true,1,1000)`, identity.Workspace.ID); e != nil {
		t.Fatal(e)
	}
	opClient := authClient(t)
	op := registerFixture(t, opClient, server.URL, "credit-risk-operator@example.test")
	operationsGrantFixture(t, a, op.User.ID, identity.Workspace.ID, "risk_reviewer")
	p, e := a.currentPrincipal(operationsRequest(t, owner, server))
	if e != nil {
		t.Fatal(e)
	}
	reviewer, e := a.currentPrincipal(operationsRequest(t, opClient, server))
	if e != nil {
		t.Fatal(e)
	}
	proof, e := a.verifyOperationsPassword(operationsRequest(t, opClient, server), "fixture-password-123")
	if e != nil {
		t.Fatal(e)
	}
	approvable := func(id string) string {
		review, replay, e := a.createRiskAppeal(p, RiskAppealInput{ProjectID: identity.Project.ID, QuoteID: id, ReasonCode: "limit_error", Statement: "本人申请"}, newID("key"))
		if e != nil || replay {
			t.Fatal("create real review", e)
		}
		plan, e := a.previewRiskReview(reviewer, review.ID, ContentAppealPlanInput{WorkspaceID: identity.Workspace.ID, ExpectedVersion: 1, Action: "approve", EvidenceRef: "test-exact-quote", PublicReply: "本次报价可单次受理"})
		if e != nil {
			t.Fatal("preview", e)
		}
		if _, e = a.executeRiskReview(reviewer, identity.Workspace.ID, review.ID, plan.ID, plan.PlanDigest, proof); e != nil {
			t.Fatal("approval", e)
		}
		return review.ID
	}
	rid := approvable(quote.ID)
	readQ := func(id string) ledgerQuoteRow {
		q, e := readLedgerQuoteSQL(ctx, admin, id, false)
		if e != nil {
			t.Fatal(e)
		}
		return q
	}
	old := readQ(quote.ID)
	if old.OwnerUserID != identity.User.ID {
		t.Fatal("quote owner not captured")
	}
	req := riskReservationFixture(old, op.User.ID)
	try := func(r LedgerReservationRequest) error {
		return ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error { _, e := reserveLedgerSQL(ctx, tx, r); return e })
	}
	requireOperation(t, try(req), "CREDIT_RISK_HELD_LIMIT")
	requireOperation(t, try(riskReservationFixture(readQ(replacement.ID), identity.User.ID)), "CREDIT_RISK_HELD_LIMIT")
	req.ActorUserID = identity.User.ID
	deliberate := errors.New("rollback fixture after writing reservation and permit")
	// Inject rollback on an independent connection. On the application control
	// writer, an unclassified driver error deliberately takes the service offline.
	e = pgx.BeginFunc(ctx, admin, func(tx pgx.Tx) error {
		if _, e := reserveLedgerSQL(ctx, tx, req); e != nil {
			return e
		}
		var attached bool
		if e := tx.QueryRow(ctx, "SELECT reservation_id IS NOT NULL FROM studio_credit_risk_permits WHERE review_id=$1", rid).Scan(&attached); e != nil || !attached {
			t.Fatal("permit missing inside admitted transaction", e)
		}
		return deliberate
	})
	if !errors.Is(e, deliberate) || outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_permits WHERE review_id=$1 AND reservation_id IS NULL", rid) != 1 || readQ(quote.ID).ReservationID != "" {
		t.Fatal("rollback consumed grant or quote", e)
	}
	if e = try(req); e != nil {
		t.Fatal("verified owner could not use permit", e)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_permits p JOIN studio_ledger_reservations r ON r.id=p.reservation_id AND r.quote_id=p.quote_id WHERE p.review_id=$1", rid) != 1 {
		t.Fatal("no receipt for real hold")
	}
	requireOperation(t, try(req), "QUOTE_USED")
	rid = approvable(replacement.ID)
	if _, e = admin.Exec(ctx, `INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES($1,2,true,2,1000)`, identity.Workspace.ID); e != nil {
		t.Fatal(e)
	}
	requireOperation(t, try(riskReservationFixture(readQ(replacement.ID), identity.User.ID)), "CREDIT_RISK_HELD_LIMIT")
	if outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_permits WHERE review_id=$1 AND reservation_id IS NULL", rid) != 1 {
		t.Fatal("old policy permit was spent")
	}
}

func TestPostgresCreditRiskQuoteExpiresWaitingForWallet(t *testing.T) {
	s, dsn, admin := outboxStore(t)
	ctx := context.Background()
	ledgerGrant(t, s, 10000)
	q := ledgerQuote(t, s, "task", "m", payloadHash("expiry"), 1, LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 1, RequestedUnits: 1})
	b, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Rollback(ctx)
	if _, e = b.Exec(ctx, "SELECT workspace_id FROM studio_ledger_wallets WHERE workspace_id='ws_demo' FOR UPDATE"); e != nil {
		t.Fatal(e)
	}
	observer := deliveryAdmin(t, dsn)
	if e = observer.QueryRow(ctx, "UPDATE studio_ledger_quotes SET expires_at=clock_timestamp()+interval '1.5 seconds' WHERE id=$1 RETURNING expires_at", q.ID).Scan(&q.ExpiresAt); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		done <- ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
			_, e := reserveLedgerSQL(ctx, tx, riskReservationFixture(q, ""))
			return e
		})
	}()
	awaitRunLeaseWaiters(t, observer, int32(s.conn.PgConn().PID()))
	var expired bool
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline) && !expired; {
		if e = observer.QueryRow(ctx, "SELECT clock_timestamp()>$1::timestamptz", q.ExpiresAt).Scan(&expired); e != nil {
			t.Fatal(e)
		}
		if !expired {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !expired {
		t.Fatal("fixture clock did not reach expiry")
	}
	if e = b.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	requireOperation(t, <-done, "QUOTE_EXPIRED")
	if outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("expired quote created a reservation")
	}
}
