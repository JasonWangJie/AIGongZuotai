package studio

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestLedgerIntegerArithmeticAndHashCompatibility(t *testing.T) {
	p := LedgerPriceSnapshot{InputPerMillionMicros: 1, OutputPerMillionMicros: 1}
	if got, err := ledgerChatAmount(p, 1, 1); err != nil || got != 1 {
		t.Fatalf("single exact ceiling=%d %v", got, err)
	}
	p.InputPerMillionMicros = MaxLedgerMicros
	p.OutputPerMillionMicros = MaxLedgerMicros
	if _, err := ledgerChatAmount(p, 1_000_000_000, 1_000_000_000); err == nil {
		t.Fatal("overflow accepted")
	}
	p.InputPerMillionMicros = 9_007_199_254_740_993
	p.OutputPerMillionMicros = 0
	if _, err := ledgerChatAmount(p, 1, 0); err == nil {
		t.Fatal("rate above integer policy limit accepted")
	}
	p.InputPerMillionMicros = 8_999_999_999_999_999
	n, err := ledgerChatAmount(p, 1, 0)
	if err != nil || n != 9_000_000_000 {
		t.Fatalf("lost integer precision %d %v", n, err)
	}
	run := runInput()
	old := payloadHash(run)
	run.QuoteID = "qte_example"
	if ledgerRunRequestHash(run) != old || payloadHash(run) == old {
		t.Fatal("quote binding changed original request hash")
	}
	task := taskInput("image")
	old = payloadHash(task)
	task.QuoteID = "qte_example"
	if ledgerTaskRequestHash(task) != old {
		t.Fatal("task quote hash mismatch")
	}
	task.Parameters.Count = 3
	if ledgerTaskRequestHash(task) == old {
		t.Fatal("count omitted from binding")
	}
	c := conversationInput(1, nil, "hello")
	before := ledgerConversationRequestHash("conv_a", c)
	c.QuoteID = "qte_example"
	if ledgerConversationRequestHash("conv_a", c) != before || ledgerConversationRequestHash("conv_b", c) == before {
		t.Fatal("conversation binding mismatch")
	}
	raw, _ := json.Marshal(BillingSummary{ReservedMicros: MaxLedgerMicros, Unit: LedgerUnit, Scale: LedgerScale})
	if !strings.Contains(string(raw), `"reserved_micros":"9000000000000000"`) {
		t.Fatal("money JSON was not decimal string")
	}
}

func ledgerTx(s *postgresStore, fn func(context.Context, pgx.Tx) error) error {
	var original error
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = fn(ctx, tx); err != nil {
			original = err
			return err
		}
		original = tx.Commit(ctx)
		return original
	})
	if original != nil {
		return original
	}
	return err
}
func ledgerGrant(t *testing.T, s *postgresStore, amount int64) {
	t.Helper()
	if err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		_, e := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: "ws_demo", OperationKey: newID("grant"), Reason: "fixture", EvidenceRef: "fixture:credit", AmountMicros: amount})
		return e
	}); err != nil {
		t.Fatal(err)
	}
}
func ledgerQuote(t *testing.T, s *postgresStore, kind, id string, hash [32]byte, reserve int64, price LedgerPriceSnapshot) ledgerQuoteRow {
	t.Helper()
	q := ledgerQuoteRow{ID: newID("qte"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ResourceType: kind, ModelID: id, ModelVersion: "model_v1", PriceVersion: "price_v1", RequestHash: hash, Price: price, ReserveMicros: reserve, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	if err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error { return insertLedgerQuoteSQL(ctx, tx, q) }); err != nil {
		t.Fatal(err)
	}
	return q
}
func ledgerRun(t *testing.T, s *postgresStore, mode string) Run {
	t.Helper()
	run := outboxFixtureRun(newID("run"))
	run.ProviderMode = mode
	run.ModelVersion = "model_v1"
	p := LedgerPriceSnapshot{ProviderMode: mode, Basis: "chat_tokens", InputPerMillionMicros: 1_000_000, OutputPerMillionMicros: 2_000_000, MaxInputTokens: 1000, MaxOutputTokens: 1000}
	q := ledgerQuote(t, s, "run", run.ModelID, payloadHash(run), 3000, p)
	run.BillingQuoteID = q.ID
	run.BillingRequestHash = q.RequestHash
	if err := s.createRunCandidate(&run, newID("key"), payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	return run
}
func ledgerWallet(t *testing.T, s *postgresStore) LedgerWallet {
	t.Helper()
	var result LedgerWallet
	err := s.do(func(ctx context.Context, c *pgx.Conn) error {
		var e error
		result, e = readLedgerWalletSQL(ctx, c, "ws_demo")
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func ledgerAssertBalanced(t *testing.T, s *postgresStore) {
	t.Helper()
	err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		r, e := reconcileLedgerSQL(ctx, tx, "ws_demo")
		if e == nil && (!r.Balanced || !r.AccountBalancesMatch || !r.HoldsMatch || !r.TotalsMatch) {
			t.Errorf("reconciliation mismatch %+v", r)
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLedgerSettlementRefundAndImmutableEvidence(t *testing.T) {
	s, _, admin := outboxStore(t)
	ledgerGrant(t, s, 10000)
	run := ledgerRun(t, s, "live")
	if w := ledgerWallet(t, s); w.AvailableMicros != 7000 || w.ReservedMicros != 3000 {
		t.Fatalf("reserve %+v", w)
	}
	l := claimFixtureRun(t, s, &run)
	run.Status = "COMPLETED"
	run.Seq = 1
	run.BillingUsage = &ChatUsage{Status: "reported", Source: "gateway", Complete: true, PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140}
	if err := s.saveLeasedRun(&run, l, 0); err != nil {
		t.Fatal(err)
	}
	if run.Billing.State != "SETTLED" || run.Billing.ChargedMicros != 180 || run.Billing.ReleasedMicros != 2820 {
		t.Fatalf("settlement %+v", run.Billing)
	}
	before := outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals")
	if err := s.saveRunCandidate(&run); err != nil {
		t.Fatal(err)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals") != before {
		t.Fatal("duplicate checkpoint charged twice")
	}
	in := LedgerRefundRequest{ReservationID: run.Billing.ReservationID, OperationKey: "refund-fixture-key", Reason: "support", EvidenceRef: "ticket:123"}
	var result LedgerMutation
	err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		result, e = refundLedgerSQL(ctx, tx, in)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reservation.State != "REFUNDED" || result.Wallet.AvailableMicros != 10000 || result.Wallet.RefundedMicros != 180 {
		t.Fatalf("refund %+v", result)
	}
	err = ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		result, e = refundLedgerSQL(ctx, tx, in)
		return e
	})
	if err != nil || !result.Replayed {
		t.Fatalf("refund replay %v %+v", err, result)
	}
	in.OperationKey = "refund-different-key"
	requireOperation(t, ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error { _, e := refundLedgerSQL(ctx, tx, in); return e }), "LEDGER_REFUND_NOT_ALLOWED")
	for _, sql := range []string{"DELETE FROM studio_ledger_postings", "UPDATE studio_ledger_journals SET amount_micros=1", "DELETE FROM studio_ledger_operations"} {
		if _, err = admin.Exec(context.Background(), sql); err == nil {
			t.Fatal("immutable evidence changed")
		}
	}
	// A bare journal cannot commit without balanced postings.
	tx, _ := admin.Begin(context.Background())
	_, err = tx.Exec(context.Background(), `INSERT INTO studio_ledger_journals(id,workspace_id,kind,amount_micros,available_delta_micros,reserved_delta_micros) VALUES('jrn_unbalanced','ws_demo','CREDIT',1,1,0)`)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Commit(context.Background()) == nil {
		t.Fatal("unbalanced journal committed")
	}
	ledgerAssertBalanced(t, s)
	var doc []byte
	if err = admin.QueryRow(context.Background(), "SELECT document FROM studio_runs WHERE id=$1", run.ID).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	var saved Run
	_ = json.Unmarshal(doc, &saved)
	if saved.Billing.State != "REFUNDED" || saved.Seq <= run.Seq {
		t.Fatal("refund did not publish a newer resource snapshot")
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_ledger_accounts SET balance_micros=balance_micros+1 WHERE id='available:ws_demo'"); err != nil {
		t.Fatal(err)
	}
	err = ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		v, e := reconcileLedgerSQL(ctx, tx, "ws_demo")
		if e == nil && v.AccountBalancesMatch {
			t.Fatal("projection drift not detected")
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLedgerReviewReleaseAndUsageBoundaries(t *testing.T) {
	s, _, _ := outboxStore(t)
	ledgerGrant(t, s, 100000)
	cases := []struct {
		name, status string
		started      bool
		usage        *ChatUsage
		want         string
	}{
		{"missing", "COMPLETED", true, &ChatUsage{Status: "missing", Source: "gateway", Complete: true}, "REVIEW"},
		{"invalid", "COMPLETED", true, &ChatUsage{Status: "invalid", Source: "gateway", Complete: true}, "REVIEW"},
		{"incomplete", "COMPLETED", true, &ChatUsage{Status: "reported", Source: "gateway", PromptTokens: 1, TotalTokens: 1}, "REVIEW"},
		{"over-input", "COMPLETED", true, &ChatUsage{Status: "reported", Source: "gateway", Complete: true, PromptTokens: 1001, TotalTokens: 1001}, "REVIEW"},
		{"false-demo", "COMPLETED", true, &ChatUsage{Status: "demo", Source: "demo", Complete: true, PromptTokens: 128, CompletionTokens: 64, TotalTokens: 192}, "REVIEW"},
		{"bad-source", "COMPLETED", true, &ChatUsage{Status: "reported", Source: "demo", Complete: true, PromptTokens: 1, TotalTokens: 1}, "REVIEW"},
		{"cancel-after-start", "CANCELLED", true, &ChatUsage{Status: "reported", Source: "gateway", Complete: true, PromptTokens: 1, TotalTokens: 1}, "REVIEW"},
		{"error-after-start", "FAILED", true, nil, "REVIEW"},
		{"queued-cancel", "CANCELLED", false, nil, "RELEASED"},
		{"queued-restart", "INTERRUPTED", false, nil, "RELEASED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := ledgerRun(t, s, "live")
			var l runLease
			if tc.started {
				l = claimFixtureRun(t, s, &run)
			}
			run.Status = tc.status
			run.BillingUsage = tc.usage
			run.Seq++
			var e error
			if tc.status == "CANCELLED" || tc.status == "INTERRUPTED" {
				e = s.saveControlRun(&run)
			} else {
				e = s.saveLeasedRun(&run, l, run.Seq-1)
			}
			if e != nil {
				t.Fatal(e)
			}
			if run.Billing.State != tc.want || run.Billing.ChargedMicros != 0 {
				t.Fatalf("unsafe outcome %+v", run.Billing)
			}
		})
	}
	if ledgerWallet(t, s).ReviewMicros != 24000 {
		t.Fatal("review subset lost held amounts")
	}
	ledgerAssertBalanced(t, s)
}

func TestPostgresLedgerConcurrentReserveCreditReplayAndRollback(t *testing.T) {
	s, url, admin := outboxStore(t)
	ledgerGrant(t, s, 1000)
	requests := make([]LedgerReservationRequest, 8)
	for i := range requests {
		q := ledgerQuote(t, s, "task", "studio-image", payloadHash(i), 600, LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 600, RequestedUnits: 1})
		requests[i] = LedgerReservationRequest{QuoteID: q.ID, WorkspaceID: q.WorkspaceID, ProjectID: q.ProjectID, ResourceType: q.ResourceType, ResourceID: newID("task"), ModelID: q.ModelID, ModelVersion: q.ModelVersion, ProviderMode: "demo", RequestHash: q.RequestHash}
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for _, request := range requests {
		wg.Add(1)
		go func(in LedgerReservationRequest) {
			defer wg.Done()
			c, e := pgx.Connect(context.Background(), url)
			if e != nil {
				results <- e
				return
			}
			defer c.Close(context.Background())
			tx, e := c.Begin(context.Background())
			if e == nil {
				defer tx.Rollback(context.Background())
				_, e = reserveLedgerSQL(context.Background(), tx, in)
				if e == nil {
					e = tx.Commit(context.Background())
				}
			}
			results <- e
		}(request)
	}
	wg.Wait()
	close(results)
	success, denied := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else {
			var op *operationError
			if errors.As(e, &op) && op.code == "INSUFFICIENT_CREDITS" {
				denied++
			} else {
				t.Fatal(e)
			}
		}
	}
	if success != 1 || denied != 7 {
		t.Fatalf("concurrency %d %d", success, denied)
	}
	if w := ledgerWallet(t, s); w.AvailableMicros != 400 || w.ReservedMicros != 600 {
		t.Fatalf("overdraft %+v", w)
	}
	// Forced transaction rollback after an otherwise valid reservation returns
	// both money and the quote to their prior state; no run/idempotency survives.
	ledgerGrant(t, s, 10000)
	run := outboxFixtureRun(newID("run"))
	run.ModelVersion = "model_v1"
	q := ledgerQuote(t, s, "run", run.ModelID, payloadHash(run), 3000, LedgerPriceSnapshot{ProviderMode: "demo", Basis: "chat_tokens", MaxInputTokens: 1000, MaxOutputTokens: 1000, InputPerMillionMicros: 1_000_000, OutputPerMillionMicros: 2_000_000})
	run.BillingQuoteID = q.ID
	run.BillingRequestHash = q.RequestHash
	walletBefore := ledgerWallet(t, s)
	err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		if e := insertBilledRunSQL(ctx, tx, &run, "rollback-run-key", payloadHash(run), nil, nil); e != nil {
			return e
		}
		return failure(409, "FIXTURE_ROLLBACK", "rollback")
	})
	requireOperation(t, err, "FIXTURE_ROLLBACK")
	walletAfter := ledgerWallet(t, s)
	if walletAfter.AvailableMicros != walletBefore.AvailableMicros || walletAfter.ReservedMicros != walletBefore.ReservedMicros {
		t.Fatal("rollback leaked money")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_runs WHERE id=$1", run.ID) != 0 {
		t.Fatal("rollback leaked accepted run")
	}
	var used bool
	if err = admin.QueryRow(context.Background(), "SELECT reservation_id IS NOT NULL FROM studio_ledger_quotes WHERE id=$1", q.ID).Scan(&used); err != nil || used {
		t.Fatal("rollback consumed quote")
	}
	ledgerAssertBalanced(t, s)
}

func TestPostgresLedgerMediaPartialAndUnknownRecovery(t *testing.T) {
	s, _, admin := outboxStore(t)
	ledgerGrant(t, s, 10000)
	input := taskInput("image")
	input.Parameters.Count = 4
	p := LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 500, RequestedUnits: 4}
	q := ledgerQuote(t, s, "task", "studio-image", ledgerTaskRequestHash(input), 2000, p)
	now := time.Now().UTC()
	task := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", Kind: "image", ModelID: "studio-image", ModelVersion: "model_v1", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, BillingQuoteID: q.ID, BillingRequestHash: q.RequestHash, Artifacts: []Artifact{}}, Input: input, NextPoll: now}
	if err := s.createTask(task, "media-billing-key", payloadHash(input)); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "SUBMITTING"
	task.Task.Version++
	if err := s.saveTask(task); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "PROVIDER_STATE_UNKNOWN"
	task.Task.Version++
	if err := s.saveTask(task); err != nil {
		t.Fatal(err)
	}
	if task.Task.Billing.State != "REVIEW" {
		t.Fatal("transient unknown state did not preserve reviewed hold")
	}
	requireOperation(t, ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		_, e := resolveLedgerSQL(ctx, tx, LedgerResolveRequest{ReservationID: task.Task.Billing.ReservationID, OperationKey: "no-active-resolution", Reason: "fixture", EvidenceRef: "case:pending", Decision: "release"})
		return e
	}), "LEDGER_EXECUTION_ACTIVE")
	task.Task.Status = "PARTIAL"
	task.Task.Version++
	task.Task.Artifacts = []Artifact{{ID: "one", Kind: "image", Title: "one", URL: "/demo-assets/image-1.svg"}, {ID: "two", Kind: "image", Title: "two", URL: "/demo-assets/image-1.svg"}}
	if err := s.saveTask(task); err != nil {
		t.Fatal(err)
	}
	if task.Task.Billing.State != "SETTLED" || task.Task.Billing.ChargedMicros != 1000 || task.Task.Billing.ReleasedMicros != 1000 {
		t.Fatalf("partial %+v", task.Task.Billing)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.Task.ID) != 2 {
		t.Fatal("charged without registered delivery")
	}
	// Startup turns a begun LLM into INTERRUPTED; funds are held for review.
	run := ledgerRun(t, s, "live")
	claimFixtureRun(t, s, &run)
	a := &App{config: Config{Mode: "demo"}, projects: map[string]Project{}, runs: map[string]*runRecord{}, tasks: map[string]*taskRecord{}}
	if err := s.restore(a); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.loadRun(run.ID)
	if err != nil || recovered.Billing == nil {
		t.Fatal("missing durable run or reservation after interrupted execution", err)
	}
	if recovered.Status != "INTERRUPTED" || recovered.Billing.State != "REVIEW" {
		t.Fatalf("restore released unknown charge %+v", recovered)
	}
	in := LedgerResolveRequest{ReservationID: recovered.Billing.ReservationID, OperationKey: "manual-resolve-key", Reason: "supplier-confirmed", EvidenceRef: "case:zero", Decision: "release"}
	if err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error { _, e := resolveLedgerSQL(ctx, tx, in); return e }); err != nil {
		t.Fatal(err)
	}
	ledgerAssertBalanced(t, s)
}

func TestPostgresLedgerQuoteLimitsMismatchAndLegacyIsolation(t *testing.T) {
	s, _, admin := outboxStore(t)
	ledgerGrant(t, s, 10000)
	p := LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 1, RequestedUnits: 1}
	q := ledgerQuote(t, s, "task", "studio-image", payloadHash("original"), 1, p)
	req := LedgerReservationRequest{QuoteID: q.ID, WorkspaceID: q.WorkspaceID, ProjectID: q.ProjectID, ResourceType: q.ResourceType, ResourceID: newID("task"), ModelID: q.ModelID, ModelVersion: q.ModelVersion, ProviderMode: "demo", RequestHash: payloadHash("changed")}
	requireOperation(t, ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error { _, e := reserveLedgerSQL(ctx, tx, req); return e }), "QUOTE_MISMATCH")
	if _, err := admin.Exec(context.Background(), "UPDATE studio_ledger_quotes SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", q.ID); err != nil {
		t.Fatal(err)
	}
	req.RequestHash = q.RequestHash
	requireOperation(t, ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error { _, e := reserveLedgerSQL(ctx, tx, req); return e }), "QUOTE_EXPIRED")
	for i := 0; i < 64; i++ {
		ledgerQuote(t, s, "task", "studio-image", payloadHash(i), 1, p)
	}
	q.ID = newID("qte")
	q.ExpiresAt = time.Now().Add(time.Minute)
	requireOperation(t, ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error { return insertLedgerQuoteSQL(ctx, tx, q) }), "QUOTE_LIMIT")
	legacy := outboxFixtureRun(newID("run"))
	if err := s.createRun(legacy, "legacy-free-run", payloadHash(legacy)); err != nil {
		t.Fatal(err)
	}
	l := claimFixtureRun(t, s, &legacy)
	legacy.Status = "COMPLETED"
	legacy.Seq++
	if err := s.saveLeasedRun(&legacy, l, legacy.Seq-1); err != nil {
		t.Fatal(err)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("legacy resource was retroactively charged")
	}
	if s.failed.Load() {
		t.Fatal("expected business refusals poisoned healthy DB")
	}
	// A newly initialized zero-balance account can be missing without a posting
	// FK referencing it. Reconciliation must still detect the broken projection.
	if _, err := admin.Exec(context.Background(), "DELETE FROM studio_ledger_accounts WHERE id='reserved:ws_demo'"); err != nil {
		t.Fatal(err)
	}
	if err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		v, e := reconcileLedgerSQL(ctx, tx, "ws_demo")
		if e == nil && v.AccountBalancesMatch {
			t.Fatal("missing reserved account not detected")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLedgerOutboxBackpressureAndLeaseFenceRollback(t *testing.T) {
	s, _, admin := outboxStore(t)
	ledgerGrant(t, s, 10000)
	run := ledgerRun(t, s, "live")
	l := claimFixtureRun(t, s, &run)
	before := outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals")
	if _, err := admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=10000 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	candidate := copyRun(run)
	candidate.Status = "COMPLETED"
	candidate.Seq++
	candidate.BillingUsage = &ChatUsage{Status: "reported", Source: "gateway", Complete: true, PromptTokens: 100, TotalTokens: 100}
	requireOperation(t, s.saveLeasedRun(&candidate, l, run.Seq), "EVENT_BACKLOG_FULL")
	if candidate.Billing.State != "HELD" || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals") != before {
		t.Fatal("failed state transaction exposed uncommitted settlement")
	}
	if s.failed.Load() {
		t.Fatal("backpressure poisoned ledger writer")
	}
	if _, err := admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	if err := s.saveLeasedRun(&candidate, l, run.Seq); err != nil {
		t.Fatal(err)
	}
	if candidate.Billing.State != "SETTLED" {
		t.Fatal("save-only retry failed to settle")
	}
	now := time.Now().UTC()
	input := taskInput("image")
	q := ledgerQuote(t, s, "task", "studio-image", payloadHash(input), 500, LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 500, RequestedUnits: 1})
	task := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", Kind: "image", ModelID: "studio-image", ModelVersion: "model_v1", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, BillingQuoteID: q.ID, BillingRequestHash: q.RequestHash}, Input: input, NextPoll: now}
	if err := s.createTask(task, "fenced-ledger-task", payloadHash(input)); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "SUCCEEDED"
	task.Task.Version++
	task.Task.Artifacts = []Artifact{{ID: "fenced", Kind: "image", Title: "fenced", URL: "/demo-assets/image-1.svg"}}
	task.Lease = &TaskLease{Owner: "wrong-owner", Generation: "wrong-generation", Epoch: 1, Version: 1}
	task.LeaseStatus = "QUEUED"
	before = outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals")
	requireOperation(t, s.saveTask(task), "TASK_LEASE_LOST")
	if task.Task.Billing.State != "HELD" || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.Task.ID) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals") != before {
		t.Fatal("stale lease registered or charged delivery")
	}
	ledgerAssertBalanced(t, s)
}

func TestPostgresLedgerConcurrentAdminReplayAndUnknownCommit(t *testing.T) {
	databaseURL := testDatabase(t)
	proxyURL, armed, dropped := commitAckDropProxy(t, databaseURL)
	s, err := openPostgres(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	input := LedgerCreditRequest{WorkspaceID: "ws_demo", OperationKey: "same-credit-operation", Reason: "fixture", EvidenceRef: "case:credit", AmountMicros: 10000}
	var wg sync.WaitGroup
	results := make(chan LedgerMutation, 4)
	failures := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := pgx.Connect(context.Background(), databaseURL)
			if e != nil {
				failures <- e
				return
			}
			defer c.Close(context.Background())
			tx, e := c.Begin(context.Background())
			if e != nil {
				failures <- e
				return
			}
			defer tx.Rollback(context.Background())
			v, e := creditLedgerSQL(context.Background(), tx, input)
			if e == nil {
				e = tx.Commit(context.Background())
			}
			if e != nil {
				failures <- e
			} else {
				results <- v
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	replays := 0
	for v := range results {
		if v.Replayed {
			replays++
		}
	}
	if replays != 3 || ledgerWallet(t, s).AvailableMicros != 10000 {
		t.Fatal("concurrent admin operation duplicated credit")
	}
	run := outboxFixtureRun(newID("run"))
	run.ModelVersion = "model_v1"
	q := ledgerQuote(t, s, "run", run.ModelID, payloadHash(run), 3000, LedgerPriceSnapshot{ProviderMode: "demo", Basis: "chat_tokens", MaxInputTokens: 1000, MaxOutputTokens: 1000, InputPerMillionMicros: 1_000_000, OutputPerMillionMicros: 2_000_000})
	run.BillingQuoteID = q.ID
	run.BillingRequestHash = q.RequestHash
	armed.Store(true)
	err = s.createRunCandidate(&run, "unknown-ledger-key", payloadHash(run))
	if err == nil || !s.failed.Load() {
		t.Fatal("unknown commit did not fail closed")
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("did not drop committed ACK")
	}
	if run.Billing != nil {
		t.Fatal("unknown commit leaked speculative billing snapshot")
	}
	s.close()
	reopened, err := openPostgres(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	old, found, err := reopened.idempotency("unknown-ledger-key")
	if err != nil || !found || old.ID != run.ID {
		t.Fatal("committed request identity lost")
	}
	if w := ledgerWallet(t, reopened); w.AvailableMicros != 7000 || w.ReservedMicros != 3000 {
		t.Fatalf("lost atomic reserve %+v", w)
	}
	a := &App{config: Config{Mode: "demo"}, projects: map[string]Project{}, runs: map[string]*runRecord{}, tasks: map[string]*taskRecord{}}
	if err = reopened.restore(a); err != nil {
		t.Fatal(err)
	}
	// This request never reached RUNNING; queued interrupted work is proven not
	// submitted and can release. Begun execution recovery is tested separately.
	recovered, err := reopened.loadRun(run.ID)
	if err != nil || recovered.Billing == nil || recovered.Billing.State != "RELEASED" {
		t.Fatal("unstarted interrupted reservation was not released")
	}
	ledgerAssertBalanced(t, reopened)
}

func ledgerMediaWithAmount(t *testing.T, s *postgresStore, amount int64) (*taskRecord, string) {
	t.Helper()
	input := taskInput("image")
	p := LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: amount, RequestedUnits: 1}
	q := ledgerQuote(t, s, "task", "studio-image", payloadHash(input), amount, p)
	now := time.Now().UTC()
	key := newID("key")
	return &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", Kind: "image", ModelID: "studio-image", ModelVersion: "model_v1", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, BillingQuoteID: q.ID, BillingRequestHash: q.RequestHash, Artifacts: []Artifact{}}, Input: input, NextPoll: now}, key
}
func ledgerCompleteMedia(t *testing.T, s *postgresStore, task *taskRecord) {
	t.Helper()
	task.Task.Status = "SUBMITTING"
	task.Task.Version++
	if err := s.saveTask(task); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "SUCCEEDED"
	task.Task.Version++
	task.Task.Artifacts = []Artifact{{ID: "one", Kind: "image", Title: "one", URL: "/demo-assets/image-1.svg"}}
	if err := s.saveTask(task); err != nil {
		t.Fatal(err)
	}
	if task.Task.Billing.State != "SETTLED" {
		t.Fatalf("accepted hold failed final settlement: %+v", task.Task.Billing)
	}
}
func ledgerRefundMedia(t *testing.T, s *postgresStore, task *taskRecord) {
	t.Helper()
	if err := ledgerTx(s, func(ctx context.Context, tx pgx.Tx) error {
		_, e := refundLedgerSQL(ctx, tx, LedgerRefundRequest{ReservationID: task.Task.Billing.ReservationID, OperationKey: newID("refund"), Reason: "fixture", EvidenceRef: "case:headroom"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
}
func TestPostgresLedgerCumulativeCapacityReservedBeforeAcceptance(t *testing.T) {
	t.Run("full-refund-does-not-reset-cumulative-capacity", func(t *testing.T) {
		s, _, admin := outboxStore(t)
		ledgerGrant(t, s, MaxLedgerMicros)
		task, key := ledgerMediaWithAmount(t, s, MaxLedgerMicros)
		if err := s.createTask(task, key, payloadHash(task.Input)); err != nil {
			t.Fatal(err)
		}
		ledgerCompleteMedia(t, s, task)
		ledgerRefundMedia(t, s, task)
		w := ledgerWallet(t, s)
		if w.AvailableMicros != MaxLedgerMicros || w.ChargedMicros != MaxLedgerMicros || w.RefundedMicros != MaxLedgerMicros {
			t.Fatalf("合法边界路径未成立: %+v", w)
		}
		rejected, rejectedKey := ledgerMediaWithAmount(t, s, 1)
		before := outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals")
		requireOperation(t, s.createTask(rejected, rejectedKey, payloadHash(rejected.Input)), "LEDGER_CAPACITY")
		if rejected.Task.Billing != nil || outboxCount(t, admin, "SELECT count(*) FROM studio_tasks WHERE id=$1", rejected.Task.ID) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency WHERE scope_key=$1", rejectedKey) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals") != before {
			t.Fatal("capacity rejection partially accepted work")
		}
		var consumed bool
		if err := admin.QueryRow(context.Background(), "SELECT reservation_id IS NOT NULL FROM studio_ledger_quotes WHERE id=$1", rejected.Task.BillingQuoteID).Scan(&consumed); err != nil || consumed {
			t.Fatal("capacity rejection consumed quote")
		}
		if s.failed.Load() {
			t.Fatal("capacity refusal poisoned healthy database")
		}
		ledgerAssertBalanced(t, s)
	})
	t.Run("all-open-holds-reserve-cumulative-headroom", func(t *testing.T) {
		s, _, _ := outboxStore(t)
		ledgerGrant(t, s, MaxLedgerMicros)
		prior, key := ledgerMediaWithAmount(t, s, MaxLedgerMicros-10)
		if err := s.createTask(prior, key, payloadHash(prior.Input)); err != nil {
			t.Fatal(err)
		}
		ledgerCompleteMedia(t, s, prior)
		ledgerRefundMedia(t, s, prior)
		first, key := ledgerMediaWithAmount(t, s, 6)
		if err := s.createTask(first, key, payloadHash(first.Input)); err != nil {
			t.Fatal(err)
		}
		second, key := ledgerMediaWithAmount(t, s, 4)
		if err := s.createTask(second, key, payloadHash(second.Input)); err != nil {
			t.Fatal(err)
		}
		// REVIEW is an open hold, not returned capacity.
		second.Task.Status = "SUBMITTING"
		second.Task.Version++
		if err := s.saveTask(second); err != nil {
			t.Fatal(err)
		}
		second.Task.Status = "RECONCILE_REQUIRED"
		second.Task.Version++
		if err := s.saveTask(second); err != nil {
			t.Fatal(err)
		}
		extra, key := ledgerMediaWithAmount(t, s, 1)
		requireOperation(t, s.createTask(extra, key, payloadHash(extra.Input)), "LEDGER_CAPACITY")
		first.Task.Status = "CANCELLED"
		first.Task.Version++
		if err := s.saveTask(first); err != nil {
			t.Fatal(err)
		}
		if first.Task.Billing.State != "RELEASED" {
			t.Fatal("unstarted cancellation did not release capacity")
		}
		replacement, key := ledgerMediaWithAmount(t, s, 6)
		if err := s.createTask(replacement, key, payloadHash(replacement.Input)); err != nil {
			t.Fatal(err)
		}
		ledgerCompleteMedia(t, s, second)
		ledgerCompleteMedia(t, s, replacement)
		if w := ledgerWallet(t, s); w.ChargedMicros != MaxLedgerMicros || w.ReservedMicros != 0 {
			t.Fatalf("accepted holds lost settlement capacity: %+v", w)
		}
		ledgerAssertBalanced(t, s)
	})
}
