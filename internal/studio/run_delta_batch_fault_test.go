package studio

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresRunDeltaBatchSkipsContendedCancellation(t *testing.T) {
	s, dsn, inspector := outboxStore(t)
	ctx := context.Background()
	left := fixtureBatchDelta(t, s, newID("run"), false)
	right := fixtureBatchDelta(t, s, newID("run"), false)
	cancel := deliveryAdmin(t, dsn)
	gate, err := cancel.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(ctx)
	if err = s.lockRunWriterSQL(ctx, gate, left.Lease.RunID); err != nil {
		t.Fatal(err)
	}
	stopped := Run{ID: left.Lease.RunID, Status: "CANCELLED", UpdatedAt: time.Now().UTC()}
	if _, err = s.stoppedRunSQL(ctx, gate, &stopped, "cancelled", nil, nil); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		results []error
		stats   runDeltaBatchStats
	}
	done := make(chan outcome, 1)
	writerPID := int32(s.conn.PgConn().PID())
	go func() {
		results, stats := s.appendRunDeltaBatch([]runDeltaAppend{left, right})
		done <- outcome{results, stats}
	}()
	awaitRunLeaseWaiters(t, inspector, writerPID)
	// The healthy group committed before the original one-Run fallback began
	// waiting on the other transaction. The caller still awaits both results.
	assertBatchXIDs(t, inspector, 1, 1)
	if n := outboxCount(t, inspector, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND seq=1 AND name='message.delta'", right.Lease.RunID); n != 1 {
		t.Fatal("contended Run held its peer's physical commit")
	}
	if err = gate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		assertBatchOutcome(t, got.results, "RUN_LEASE_LOST", "")
		if got.stats != (runDeltaBatchStats{CommittedBatches: 1, AppendedEvents: 1, SoloAttempts: 1}) {
			t.Fatal("contended item entered grouped append", got.stats)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled append did not leave original path")
	}
	assertBatchXIDs(t, inspector, 1, 1)
	if n := outboxCount(t, inspector, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND name='run.cancelled' AND seq=1", left.Lease.RunID); n != 1 {
		t.Fatal("cancellation prefix changed")
	}
}

func TestPostgresRunDeltaBatchExpiryRollsBackBeforeHealthyAppend(t *testing.T) {
	s, _, admin := outboxStore(t)
	ctx := context.Background()
	expiring := fixtureBatchDelta(t, s, "run_batch_expiring", false)
	healthy := fixtureBatchDelta(t, s, "run_batch_healthy", false)
	// All test objects live in this test's random schema. The fixture sequence
	// retains the physical transaction ID even when all of its writes roll back.
	_, err := admin.Exec(ctx, `CREATE SEQUENCE batch_fixture_xid;
		CREATE FUNCTION batch_fixture_hold() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			PERFORM setval('batch_fixture_xid',pg_current_xact_id()::text::bigint);
			PERFORM pg_advisory_xact_lock(hashtext(current_schema()),209);
			RETURN NEW;
		END $body$;
		CREATE TRIGGER batch_fixture_hold AFTER UPDATE OF head_seq ON studio_run_streams
			FOR EACH ROW WHEN (NEW.run_id='run_batch_expiring' AND NEW.head_seq>OLD.head_seq)
			EXECUTE FUNCTION batch_fixture_hold()`)
	if err != nil {
		t.Fatal("could not install fixture latch")
	}
	gate, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(ctx)
	if _, err = gate.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),209)`); err != nil {
		t.Fatal("could not hold fixture latch")
	}
	inspect := deliveryAdmin(t, admin.Config().ConnString())
	if _, err = inspect.Exec(ctx, `UPDATE studio_run_streams SET lease_until=clock_timestamp()+interval '1.5 seconds' WHERE run_id=$1`, expiring.Lease.RunID); err != nil {
		t.Fatal("could not set fixture deadline")
	}
	type outcome struct {
		results []error
		stats   runDeltaBatchStats
	}
	done := make(chan outcome, 1)
	writerPID := int32(s.conn.PgConn().PID())
	go func() {
		results, stats := s.appendRunDeltaBatch([]runDeltaAppend{healthy, expiring})
		done <- outcome{results, stats}
	}()
	awaitRunLeaseWaiters(t, inspect, writerPID)
	assertBatchXIDs(t, inspect, 0, 0)
	if _, err = inspect.Exec(ctx, `SELECT pg_sleep(GREATEST(0.0,extract(epoch FROM (lease_until-clock_timestamp()))+0.02)::double precision)
		FROM studio_run_streams WHERE run_id=$1`, expiring.Lease.RunID); err != nil {
		t.Fatal("database deadline wait failed")
	}
	if err = gate.Commit(ctx); err != nil {
		t.Fatal("latch release failed")
	}
	select {
	case got := <-done:
		assertBatchOutcome(t, got.results, "", "RUN_LEASE_LOST")
		if got.stats != (runDeltaBatchStats{SoloAttempts: 1}) {
			t.Fatal("aborted group counted or lost lease retried", got.stats)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("writer did not finish")
	}
	assertBatchXIDs(t, inspect, 1, 1)
	if n := outboxCount(t, inspect, `SELECT count(*) FROM batch_fixture_xid WHERE is_called AND pg_xact_status(last_value::text::xid8)='aborted'`); n != 1 {
		t.Fatal("first physical transaction did not abort")
	}
	if n := outboxCount(t, inspect, `SELECT count(*) FROM studio_run_streams WHERE run_id=$1 AND head_seq=0 AND output_bytes=0`, expiring.Lease.RunID); n != 1 {
		t.Fatal("aborted watermark persisted")
	}
	r, err := s.loadRun(healthy.Lease.RunID)
	if err != nil || r.Seq != 1 || r.Output != healthy.Delta || s.failed.Load() {
		t.Fatal("healthy peer did not recover its single confirmed prefix")
	}
}

func TestPostgresRunDeltaBatchServerRollbackReusesOriginal(t *testing.T) {
	s, _, admin := outboxStore(t)
	ctx := context.Background()
	a := fixtureBatchDelta(t, s, newID("run"), false)
	b := fixtureBatchDelta(t, s, newID("run"), false)
	_, err := admin.Exec(ctx, `CREATE SEQUENCE batch_fixture_rejected_xid;
		CREATE FUNCTION batch_fixture_reject_group() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			IF (SELECT count(*) FROM batch_fixture_new)>1 THEN
				PERFORM setval('batch_fixture_rejected_xid',pg_current_xact_id()::text::bigint);
				RAISE EXCEPTION USING ERRCODE='40001',MESSAGE='fixture group serialization';
			END IF;
			RETURN NULL;
		END $body$;
		CREATE TRIGGER batch_fixture_reject_group AFTER INSERT ON studio_run_stream_events
			REFERENCING NEW TABLE AS batch_fixture_new FOR EACH STATEMENT EXECUTE FUNCTION batch_fixture_reject_group()`)
	if err != nil {
		t.Fatal("could not install fixture rejection")
	}
	results, stats := s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "", "")
	if stats != (runDeltaBatchStats{SoloAttempts: 2}) {
		t.Fatal("server-rolled-back transaction counted", stats)
	}
	assertBatchXIDs(t, admin, 2, 2)
	if n := outboxCount(t, admin, `SELECT count(*) FROM batch_fixture_rejected_xid WHERE is_called AND pg_xact_status(last_value::text::xid8)='aborted'`); n != 1 {
		t.Fatal("missing original aborted transaction")
	}
}

func TestPostgresRunDeltaBatchUnknownCommitNeverReappends(t *testing.T) {
	s, dsn, admin := outboxStore(t)
	ctx := context.Background()
	a := fixtureBatchDelta(t, s, newID("run"), false)
	b := fixtureBatchDelta(t, s, newID("run"), false)
	proxy, armed, dropped := commitAckDropProxy(t, dsn)
	conn, err := pgx.Connect(ctx, proxy)
	if err != nil {
		t.Fatal("proxy connection setup failed")
	}
	writer := &postgresStore{conn: conn, runGeneration: s.runGeneration}
	defer writer.close()
	armed.Store(true)
	results, stats := writer.appendRunDeltaBatch([]runDeltaAppend{b, a})
	assertBatchOutcome(t, results, "DATABASE_UNAVAILABLE", "DATABASE_UNAVAILABLE")
	if stats != (runDeltaBatchStats{}) || !writer.failed.Load() {
		t.Fatal("uncertain commit was acknowledged or retried", stats)
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("proxy did not observe acknowledged database commit")
	}
	assertBatchXIDs(t, admin, 2, 1)
	for _, item := range []runDeltaAppend{a, b} {
		r, err := s.loadRun(item.Lease.RunID)
		if err != nil || r.Seq != 1 || r.Output != item.Delta {
			t.Fatal("independent original-prefix read failed")
		}
	}
	a.Expected++
	b.Expected++
	results, stats = writer.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "DATABASE_UNAVAILABLE", "DATABASE_UNAVAILABLE")
	if stats != (runDeltaBatchStats{}) {
		t.Fatal("failed writer attempted another append", stats)
	}
	assertBatchXIDs(t, admin, 2, 1)
}
