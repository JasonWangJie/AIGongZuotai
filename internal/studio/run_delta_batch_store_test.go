package studio

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRunDeltaBatchEnvelopeAndWatermarks(t *testing.T) {
	l := runLease{RunID: "r", Owner: "o", ExecutionID: "e", Generation: "g", Epoch: 1}
	item := runDeltaAppend{Lease: l, Delta: "界"}
	h := batchDeltaHead{runStreamHead: runStreamHead{status: "RUNNING", execution: "e", leaseRequired: true}, owner: "o", generation: "g", epoch: 1, live: true}
	if trim, solo, err := h.classify("g", item); err != nil || solo || trim != 0 {
		t.Fatal("initial append rejected")
	}
	h.head, h.snapshot, item.Expected = 257, 256, 257
	h.floor = 1
	if trim, solo, err := h.classify("g", item); err != nil || solo || trim != 2 {
		t.Fatal("covered incremental trim did not stay on batch path")
	}
	h.snapshot = 1
	if _, solo, err := h.classify("g", item); err != nil || !solo {
		t.Fatal("uncovered trim skipped the original checkpoint")
	}
	item.Expected = 200
	if _, solo, err := h.classify("g", item); err != nil || !solo {
		t.Fatal("retained replay did not require original comparison")
	}
	for _, delta := range []string{"", "a\x00b", string([]byte{255}), strings.Repeat("a", maxRunOutputBytes+1)} {
		bad := item
		bad.Delta = delta
		results := make([]error, 1)
		if len(batchDeltaCandidates([]runDeltaAppend{bad}, results)) != 0 {
			t.Fatal("invalid delta retained")
		}
		requireOperation(t, results[0], "INVALID_STREAM_DELTA")
	}
	results := make([]error, 2)
	if len(batchDeltaCandidates([]runDeltaAppend{item, item}, results)) != 0 {
		t.Fatal("two outstanding events on one Run")
	}
	for _, err := range results {
		requireOperation(t, err, "RUN_STREAM_CONFLICT")
	}
	over := make([]runDeltaAppend, maxRunDeltaBatchItems+1)
	for _, group := range [][]runDeltaAppend{over, {{Delta: strings.Repeat("x", maxRunDeltaBatchBytes)}, {Delta: "x"}}} {
		results = make([]error, len(group))
		if len(batchDeltaCandidates(group, results)) != 0 {
			t.Fatal("oversize group retained")
		}
		for _, err := range results {
			requireOperation(t, err, "RUN_DELTA_BATCH_LIMIT")
		}
	}
}

func fixtureBatchDelta(t *testing.T, s *postgresStore, id string, moderated bool) runDeltaAppend {
	t.Helper()
	r := outboxFixtureRun(id)
	r.Prompt, r.Output = "batch fixture", ""
	if moderated {
		engine, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
		if err != nil {
			t.Fatal(err)
		}
		r.ModerationPolicy = engine.Snapshot()
		r.Moderation = InitialModerationSummary(r.ModerationPolicy)
	}
	if err := s.createRun(r, newID("key"), payloadHash(r)); err != nil {
		t.Fatal(err)
	}
	return runDeltaAppend{Lease: claimFixtureRun(t, s, &r), Delta: "蓝", At: time.Now().UTC().Truncate(time.Microsecond)}
}

func assertBatchOutcome(t *testing.T, results []error, codes ...string) {
	t.Helper()
	if len(results) != len(codes) {
		t.Fatalf("result count %d/%d", len(results), len(codes))
	}
	for ix, code := range codes {
		if code != "" {
			requireOperation(t, results[ix], code)
		} else if results[ix] != nil {
			t.Fatalf("item %d failed: %v", ix, results[ix])
		}
	}
}

func assertBatchXIDs(t *testing.T, conn *pgx.Conn, rows, transactions int) {
	t.Helper()
	var n, xids int
	if err := conn.QueryRow(context.Background(), `SELECT count(*),count(DISTINCT xmin::text) FROM studio_run_stream_events WHERE name='message.delta'`).Scan(&n, &xids); err != nil {
		t.Fatal("event transaction count lookup failed")
	}
	if n != rows || xids != transactions {
		t.Fatalf("events=%d/%d physical event transactions=%d/%d", n, rows, xids, transactions)
	}
}

func TestPostgresRunDeltaBatchPhysicalCommitAndIsolation(t *testing.T) {
	s, _, admin := outboxStore(t)
	a := fixtureBatchDelta(t, s, newID("run"), false)
	b := fixtureBatchDelta(t, s, newID("run"), false)
	m := fixtureBatchDelta(t, s, newID("run"), true)
	cancelled := fixtureBatchDelta(t, s, newID("run"), false)
	stopped := Run{ID: cancelled.Lease.RunID, Status: "CANCELLED", UpdatedAt: time.Now().UTC()}
	if err := s.saveControlRun(&stopped); err != nil {
		t.Fatal(err)
	}
	wrong := fixtureBatchDelta(t, s, newID("run"), false)
	wrong.Lease.Epoch++
	a.Delta = "one"
	b.Delta = "二"
	results, stats := s.appendRunDeltaBatch([]runDeltaAppend{b, m, wrong, cancelled, a})
	assertBatchOutcome(t, results, "", "RUN_STREAM_CONFLICT", "RUN_LEASE_LOST", "RUN_LEASE_LOST", "")
	if stats != (runDeltaBatchStats{CommittedBatches: 1, AppendedEvents: 2}) {
		t.Fatal("wrong fresh batch counters", stats)
	}
	assertBatchXIDs(t, admin, 2, 1)
	for _, item := range []runDeltaAppend{a, b} {
		r, err := s.loadRun(item.Lease.RunID)
		if err != nil || r.Output != item.Delta || r.Seq != 1 || !r.UpdatedAt.Equal(item.At) {
			t.Fatal("committed prefix differs from original item")
		}
	}
	results, stats = s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "", "")
	if stats != (runDeltaBatchStats{SoloAttempts: 2}) {
		t.Fatal("replay counted as new event", stats)
	}
	assertBatchXIDs(t, admin, 2, 1)
	a.Delta = "mismatch"
	b.Expected = 2
	results, _ = s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "RUN_STREAM_CONFLICT", "RUN_STREAM_CONFLICT")
	for _, item := range []runDeltaAppend{a, b} {
		item.Expected = 1
		if err := s.appendLeasedRunDelta(item.Lease, item.Expected, "x", item.At); err != nil {
			t.Fatal(err)
		}
	}
	assertBatchXIDs(t, admin, 4, 3)
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", m.Lease.RunID); n != 0 {
		t.Fatal("moderated candidate entered the stream")
	}
	// Rotate only this isolated test controller's persisted generation.
	if _, err := admin.Exec(context.Background(), "UPDATE studio_run_executor SET generation='new_fixture_generation' WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	results, _ = s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "RUN_LEASE_LOST", "RUN_LEASE_LOST")
	assertBatchXIDs(t, admin, 4, 3)
}

func TestPostgresRunDeltaBatchRetentionAndOutputLimit(t *testing.T) {
	s, _, admin := outboxStore(t)
	a := fixtureBatchDelta(t, s, newID("run"), false)
	b := fixtureBatchDelta(t, s, newID("run"), false)
	// Establish one intact unsnapshotted retained window and compare its
	// public prefix before and after the first materialization/covered trim.
	_, err := admin.Exec(context.Background(), `INSERT INTO studio_run_stream_events(run_id,seq,execution_id,name,delta,byte_size,emitted_at)
		SELECT $1,n,$2,'message.delta','x',1,clock_timestamp() FROM generate_series(1,256) n`, a.Lease.RunID, a.Lease.ExecutionID)
	if err != nil {
		t.Fatal("seed window")
	}
	_, err = admin.Exec(context.Background(), `UPDATE studio_run_streams SET head_seq=256,retained_bytes=256,output_bytes=256 WHERE run_id=$1`, a.Lease.RunID)
	if err != nil {
		t.Fatal("seed header")
	}
	a.Expected = 256
	results, stats := s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "", "")
	if stats != (runDeltaBatchStats{CommittedBatches: 1, AppendedEvents: 1, SoloAttempts: 1}) {
		t.Fatal("uncovered trim bypassed original checkpoint", stats)
	}
	a.Expected++
	b.Expected++
	results, stats = s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "", "")
	if stats != (runDeltaBatchStats{CommittedBatches: 1, AppendedEvents: 2}) {
		t.Fatal("covered trim unnecessarily left batch", stats)
	}
	r, err := s.loadRun(a.Lease.RunID)
	if err != nil || r.Seq != 258 || r.Output != strings.Repeat("x", 256)+a.Delta+a.Delta {
		t.Fatal("folded prefix lost or duplicated bytes")
	}
	if n := outboxCount(t, admin, `SELECT count(*) FROM studio_run_streams h WHERE h.run_id=$1 AND floor_seq=2 AND snapshot_seq=256 AND head_seq=258
		AND retained_bytes=output_bytes-2 AND retained_bytes=(SELECT sum(byte_size) FROM studio_run_stream_events WHERE run_id=h.run_id)
		AND 256=(SELECT count(*) FROM studio_run_stream_events WHERE run_id=h.run_id)`, a.Lease.RunID); n != 1 {
		t.Fatal("retained floor/byte totals changed")
	}
	a.Expected = 258
	a.Delta = strings.Repeat("x", maxRunOutputBytes)
	b.Expected++
	results, stats = s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "RUN_OUTPUT_LIMIT", "")
	if stats.AppendedEvents != 1 {
		t.Fatal("one full Run blocked its healthy peer")
	}
	a.Expected = 1
	a.Delta = "x"
	b.Expected++
	results, _ = s.appendRunDeltaBatch([]runDeltaAppend{a, b})
	assertBatchOutcome(t, results, "RUN_STREAM_CONFLICT", "")
}
