package studio

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func claimFixtureRun(t *testing.T, s *postgresStore, r *Run) runLease {
	t.Helper()
	r.Status = "RUNNING"
	l, err := s.claimRunLease(r, newID("worker"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestPostgresRunStreamStoreSequenceAndCheckpoint(t *testing.T) {
	s, _, admin := outboxStore(t)
	ctx := context.Background()
	r := outboxFixtureRun(newID("run"))
	r.Output = ""
	if err := s.createRun(r, newID("key"), payloadHash(r)); err != nil {
		t.Fatal(err)
	}
	l := claimFixtureRun(t, s, &r)
	execution := l.Owner
	_, conflict := s.claimRunLease(&r, "replacement", time.Minute)
	requireOperation(t, conflict, "RUN_LEASE_LOST")
	appendDelta := func(owner string, expected uint64, delta string) error {
		caller := l
		caller.Owner = owner
		return s.appendLeasedRunDelta(caller, expected, delta, time.Now().UTC())
	}
	if err := appendDelta(execution, 0, "A"); err != nil {
		t.Fatal(err)
	}
	if err := appendDelta(execution, 0, "A"); err != nil {
		t.Fatal("identical replay rejected", err)
	}
	requireOperation(t, appendDelta("replacement", 0, "A"), "RUN_LEASE_LOST")
	requireOperation(t, appendDelta(execution, 0, "B"), "RUN_STREAM_CONFLICT")
	requireOperation(t, appendDelta(execution, 2, "future"), "RUN_STREAM_CONFLICT")
	if got := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", r.ID); got != 1 {
		t.Fatal("duplicate affected stream length", got)
	}
	// A replay reader has a real transaction snapshot from before the next
	// append and the explicit checkpoint. It must not splice that newer suffix.
	reader, err := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback(ctx)
	old, err := readRunDocumentSQL(ctx, reader, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Seq != 0 || old.Output != "" {
		t.Fatal("first chunk unexpectedly rewrote business document")
	}
	if err = appendDelta(execution, 1, "界"); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.loadRun(r.ID)
	if err != nil || fresh.Output != "A界" || fresh.Seq != 2 {
		t.Fatal("latest prefix not readable", err)
	}
	if err = s.saveLeasedRun(&fresh, l, fresh.Seq); err != nil {
		t.Fatal("checkpoint did not cover committed chunks", err)
	}
	if ok, err := loadRunStreamPrefixSQL(ctx, reader, &old); !ok || err != nil || old.Output != "A" || old.Seq != 1 {
		t.Fatal("reader merged different database snapshots", err)
	}
	if err = reader.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, s.saveLeasedRun(&old, l, old.Seq), "RUN_STREAM_CONFLICT")
	tampered := fresh
	tampered.Output = "B界"
	requireOperation(t, s.saveLeasedRun(&tampered, l, fresh.Seq), "RUN_STREAM_CONFLICT")
	tampered.Seq++
	tampered.Status = "COMPLETED"
	requireOperation(t, s.saveLeasedRun(&tampered, l, fresh.Seq), "RUN_STREAM_CONFLICT")
	remaining := maxRunOutputBytes - len(fresh.Output)
	requireOperation(t, appendDelta(execution, 2, strings.Repeat("a", remaining+1)), "RUN_OUTPUT_LIMIT")
	suffix := strings.Repeat("a", remaining)
	if err = appendDelta(execution, 2, suffix); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, appendDelta(execution, 3, "b"), "RUN_OUTPUT_LIMIT")
	last, err := s.loadRun(r.ID)
	if err != nil || last.Seq != 3 || last.Output != fresh.Output+suffix {
		t.Fatal("incorrect byte-boundary prefix", err)
	}
	last.Status = "COMPLETED"
	last.Seq++
	if err = s.saveLeasedRun(&last, l, last.Seq-1); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, appendDelta(execution, 4, "late"), "RUN_LEASE_LOST")
	if got := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", r.ID); got != 4 {
		t.Fatal("write conflict altered durable stream", got)
	}
	// Revisions of a terminal resource carry full public snapshots. Force the
	// byte bound well before the row-count bound, as repeated ledger updates can.
	for i := 0; i < 10; i++ {
		last.Seq++
		if err = s.saveRunCandidate(&last); err != nil {
			t.Fatal(err)
		}
	}
	var floor uint64
	var retained, measured int
	if err = admin.QueryRow(ctx, `SELECT floor_seq,retained_bytes,(SELECT COALESCE(sum(byte_size),0) FROM studio_run_stream_events WHERE run_id=$1) FROM studio_run_streams WHERE run_id=$1`, r.ID).Scan(&floor, &retained, &measured); err != nil || floor == 0 || retained != measured || retained > runStreamByteLimit {
		t.Fatal("byte pruning did not retain an accurate bounded window", err, floor, retained, measured)
	}
	public, err := s.loadRun(r.ID)
	if err != nil || public.Output != last.Output || public.Seq != last.Seq {
		t.Fatal("byte pruning lost the latest covered snapshot", err)
	}
}

func TestPostgresRunStreamStoreModerationSnapshot(t *testing.T) {
	s, _, admin := outboxStore(t)
	engine, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
	if err != nil {
		t.Fatal(err)
	}
	r := outboxFixtureRun(newID("run"))
	r.Output = ""
	r.ModerationPolicy = engine.Snapshot()
	r.Moderation = InitialModerationSummary(r.ModerationPolicy)
	if err = s.createRunCandidate(&r, newID("key"), payloadHash(r)); err != nil {
		t.Fatal(err)
	}
	l := claimFixtureRun(t, s, &r)
	requireOperation(t, s.appendLeasedRunDelta(l, 0, "private candidate", time.Now()), "RUN_STREAM_CONFLICT")
	decision := copyRun(r)
	decision.Moderation.State = "ALLOWED"
	decision.Moderation.ReasonCode = "allowed"
	decision.Moderation.Version++
	decision.Output = "approved fixture"
	requireOperation(t, s.saveLeasedRun(&decision, l, r.Seq), "RUN_STREAM_CONFLICT")
	decision.Status = "COMPLETED"
	requireOperation(t, s.saveLeasedRun(&decision, l, r.Seq), "RUN_STREAM_CONFLICT")
	if got := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", r.ID); got != 0 {
		t.Fatal("uncommitted decision was replayable")
	}
	decision.Seq++
	if err = s.saveLeasedRun(&decision, l, r.Seq); err != nil {
		t.Fatal(err)
	}
	tampered := copyRun(decision)
	tampered.Output = "unapproved replacement"
	requireOperation(t, s.saveRunCandidate(&tampered), "RUN_STREAM_CONFLICT")
	tampered.Seq++
	requireOperation(t, s.saveRunCandidate(&tampered), "RUN_STREAM_CONFLICT")
	if count := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND name='run.snapshot' AND data->>'output'=$2", r.ID, decision.Output); count != 1 {
		t.Fatal("missing unique approved snapshot")
	}
	if count := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", r.ID); count != 1 {
		t.Fatal("rejected candidate advanced stream")
	}
}

func TestPostgresRunLeaseDeadlineDuringTerminalOutboxWait(t *testing.T) {
	s, dsn, barrier := outboxStore(t)
	ctx := context.Background()
	inspect := deliveryAdmin(t, dsn)
	ledgerGrant(t, s, 5000)
	engine, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
	if err != nil {
		t.Fatal(err)
	}
	r := outboxFixtureRun(newID("run"))
	r.ModelVersion, r.Output = "model_v1", ""
	r.ModerationPolicy = engine.Snapshot()
	r.Moderation = InitialModerationSummary(r.ModerationPolicy)
	price := LedgerPriceSnapshot{ProviderMode: "demo", Basis: "chat_tokens", InputPerMillionMicros: 1_000_000,
		OutputPerMillionMicros: 2_000_000, MaxInputTokens: 1000, MaxOutputTokens: 1000}
	q := ledgerQuote(t, s, "run", r.ModelID, payloadHash(r), 3000, price)
	r.BillingQuoteID, r.BillingRequestHash = q.ID, q.RequestHash
	if err = s.createRunCandidate(&r, newID("key"), payloadHash(r)); err != nil {
		t.Fatal(err)
	}
	l := claimFixtureRun(t, s, &r)
	next := copyRun(r)
	next.Status, next.Output, next.Seq = "COMPLETED", "approved private fixture", r.Seq+1
	next.Moderation.State, next.Moderation.ReasonCode = "ALLOWED", "allowed"
	next.Moderation.Version++
	next.BillingUsage = &ChatUsage{Status: "demo", Source: "demo", Complete: true, PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}
	var due time.Time
	if err = barrier.QueryRow(ctx, "UPDATE studio_run_streams SET lease_until=clock_timestamp()+interval '1.5 seconds' WHERE run_id=$1 RETURNING lease_until", r.ID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	tx, err := barrier.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pending FROM studio_outbox_limits WHERE singleton FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	pid := int32(s.conn.PgConn().PID())
	done := make(chan error, 1)
	go func() { done <- s.saveLeasedRun(&next, l, r.Seq) }()
	awaitRunLeaseWaiters(t, inspect, pid)
	if n := outboxCount(t, inspect, `SELECT count(*) FROM pg_stat_activity WHERE pid=$1
		AND query LIKE '%FROM studio_outbox_limits%' AND clock_timestamp()<$2::timestamptz`, pid, due); n != 1 {
		t.Fatal("terminal did not reach the final Outbox wait while authorized")
	}
	if _, err = inspect.Exec(ctx, "SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp()))::double precision)+0.03)", due); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, <-done, "RUN_LEASE_LOST")
	durable, err := s.loadRun(r.ID)
	if err != nil || durable.Status != "RUNNING" || durable.Seq != r.Seq || durable.Output != "" ||
		durable.Moderation.State != "PENDING" || durable.Billing.State != "HELD" || next.Billing.State != "HELD" {
		t.Fatal("an expired terminal changed the public snapshot or committed billing", err)
	}
	if n := outboxCount(t, inspect, `SELECT
		(SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1) +
		(SELECT count(*) FROM studio_moderation_decisions WHERE run_id=$1 AND state='ALLOWED') +
		(SELECT count(*) FROM studio_ledger_journals WHERE reservation_id=$2 AND kind IN ('CHARGE','RELEASE')) +
		(SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1 AND status='COMPLETED')`,
		r.ID, r.Billing.ReservationID); n != 0 {
		t.Fatal("expired terminal left committed evidence", n)
	}
}

func TestPostgresRunLeaseOperationsAndLateSaveLockOrder(t *testing.T) {
	s, dsn, barrier := outboxStore(t)
	ctx := context.Background()
	ledgerGrant(t, s, 5000)
	r := ledgerRun(t, s, "live")
	l := claimFixtureRun(t, s, &r)
	r.Status = "FAILED"
	r.Seq++
	if err := s.saveLeasedRun(&r, l, r.Seq-1); err != nil || r.Billing.State != "REVIEW" {
		t.Fatal("fixture did not revoke the original lease and retain the hold", err)
	}
	opConn := deliveryAdmin(t, dsn)
	old := &postgresStore{conn: deliveryAdmin(t, dsn), runGeneration: s.runGeneration}
	gate, err := barrier.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(ctx)
	if _, err = gate.Exec(ctx, "SELECT id FROM studio_ledger_reservations WHERE id=$1 FOR UPDATE", r.Billing.ReservationID); err != nil {
		t.Fatal(err)
	}
	opDone, oldDone := make(chan error, 1), make(chan error, 1)
	go func() {
		tx, err := opConn.Begin(ctx)
		if err != nil {
			opDone <- err
			return
		}
		defer tx.Rollback(ctx)
		a := &App{}
		resource, err := a.readOperationsResourceSQL(ctx, tx, r.WorkspaceID, "run", r.ID, true)
		if err == nil {
			c := OperationsCase{WorkspaceID: r.WorkspaceID, ResourceType: "run", ResourceID: r.ID}
			in := OperationsPlanInput{Action: "release", Reason: "supplier-confirmed", EvidenceRef: "case:zero"}
			_, err = a.applyOperationsActionSQL(ctx, tx, nil, c, newID("oplan"), in, resource)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		opDone <- err
	}()
	next := copyRun(r)
	next.Status, next.Output, next.Seq = "COMPLETED", "late candidate", r.Seq+1
	go func() { oldDone <- old.saveLeasedRun(&next, l, r.Seq) }()
	awaitRunLeaseWaiters(t, s.conn, int32(opConn.PgConn().PID()), int32(old.conn.PgConn().PID()))
	// Both actual producers are blocked on the same reservation. Neither may
	// already own the Run row while waiting for it; detect this without taking
	// a second blocking lock or relying on PostgreSQL's deadlock timer.
	probe, err := s.conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = probe.Exec(ctx, "SELECT id FROM studio_runs WHERE id=$1 FOR UPDATE NOWAIT", r.ID)
	_ = probe.Rollback(ctx)
	if err != nil {
		t.Fatal("producer held the Run while waiting on its reservation", err)
	}
	if err = gate.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, <-oldDone, "RUN_LEASE_LOST")
	if err = <-opDone; err != nil {
		t.Fatal("operations finance failed after the reservation became available", err)
	}
	final, err := s.loadRun(r.ID)
	if err != nil || final.Status != r.Status || final.Output != r.Output || final.Seq != r.Seq+1 ||
		final.Billing.State != "RELEASED" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != 3000 {
		t.Fatal("late save changed the original terminal or its operations revision", err)
	}
	if n := outboxCount(t, barrier, "SELECT count(*) FROM studio_ledger_journals WHERE reservation_id=$1 AND kind IN ('CHARGE','RELEASE')", r.Billing.ReservationID); n != 1 {
		t.Fatal("competing terminal altered the financial evidence", n)
	}
}
