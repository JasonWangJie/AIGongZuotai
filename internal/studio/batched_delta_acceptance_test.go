package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type batchTransactionShape struct {
	Runs         int `json:"distinct_runs_per_transaction"`
	Events       int `json:"delta_events_per_transaction"`
	Transactions int `json:"transactions"`
}

type batchDeltaEvidence struct {
	Events          int                     `json:"delta_rows"`
	Runs            int                     `json:"distinct_runs"`
	Commits         int                     `json:"distinct_delta_insert_xids"`
	Shapes          []batchTransactionShape `json:"transaction_shapes"`
	Digest          string                  `json:"ordered_fixture_sha256"`
	EventsPerSecond float64                 `json:"observed_events_per_wave_second"`
}

// Check both independent counts and their weighted histogram. The writer
// permits at most one outstanding delta per run and at most 32 per group.
func (e batchDeltaEvidence) validate(n int, enabled bool) error {
	if e.Events != n*activeStreamSteps || e.Runs != n || e.Commits <= 0 || e.Commits > e.Events {
		return errors.New("invalid row, run or physical transaction count")
	}
	events, commits := 0, 0
	shared := false
	for _, s := range e.Shapes {
		if s.Transactions <= 0 || s.Runs <= 0 || s.Runs > n || s.Runs > 32 || s.Runs != s.Events {
			return errors.New("invalid membership in a physical delta transaction")
		}
		shared = shared || s.Runs > 1
		events += s.Events * s.Transactions
		commits += s.Transactions
	}
	if events != e.Events || commits != e.Commits {
		return errors.New("transaction distribution disagrees with independent counts")
	}
	if enabled != shared || enabled != (e.Commits < e.Events) {
		return errors.New("expected transaction sharing not demonstrated")
	}
	return nil
}

func requiredBatchStreams(t *testing.T) int {
	t.Helper()
	mode := os.Getenv("STUDIO_DELTA_BATCH_STREAMS")
	if mode == "" {
		t.Skip("set STUDIO_DELTA_BATCH_STREAMS=8 or 64 for the opt-in comparison")
	}
	if !mixedLoopbackDatabase(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("isolated loopback PostgreSQL required")
	}
	switch mode {
	case "64":
		return 64
	case "8":
		return 8
	default:
		t.Fatal("choose 8 (pilot) or 64 (acceptance)")
		return 0
	}
}

func TestPostgresBatchedDeltaAcceptance(t *testing.T) {
	n := requiredBatchStreams(t)
	path := os.Getenv("STUDIO_DELTA_BATCH_REPORT")
	if _, err := os.Lstat(path); !filepath.IsAbs(path) || !os.IsNotExist(err) {
		t.Fatal("provide an unused absolute aggregate report path")
	}
	var evidence [2]batchDeltaEvidence
	cohorts := make(map[string]any)
	for i, name := range []string{"off", "on"} {
		if !t.Run(name, func(t *testing.T) {
			enabled := i == 1
			m := measureActiveStreamCapacity(t, n, enabled)
			e := observeBatchTransactions(t, m, n, enabled)
			stats := m.app.runDeltaBatch.snapshot()
			if stats.Pending != 0 || stats.PendingBytes != 0 || stats.StoreErrors != 0 || stats.QueueTimeouts != 0 || stats.Cancelled != 0 || stats.Duplicates != 0 {
				t.Fatal("unexpected rejection or retained coordinator work in completed comparison")
			}
			if enabled {
				if stats.WaitCount != uint64(e.Events) || stats.ConfirmedEvents != uint64(e.Events) || stats.CommittedBatches+stats.SoloAttempts != uint64(e.Commits) ||
					stats.AppendedEvents+stats.SoloAttempts != uint64(e.Events) {
					t.Fatal("confirmed coordinator outcomes disagree with independent delta transactions")
				}
			} else if stats.WaitCount != 0 || stats.ConfirmedEvents != 0 || stats.CommittedBatches != 0 || stats.SoloAttempts != 0 {
				t.Fatal("disabled cohort dispatched coordinator work")
			}
			evidence[i] = e
			cohorts[name] = map[string]any{"measurement": m.report, "postgres": e, "batching_enabled": enabled,
				"coordinator": map[string]any{"group_commits": stats.CommittedBatches, "group_delta_events": stats.AppendedEvents, "single_attempts": stats.SoloAttempts,
					"confirmed_events": stats.ConfirmedEvents, "queue_wait_count": stats.WaitCount, "pre_dispatch_wait_nsec_total": stats.WaitNanos,
					"dispatch_wait_bucket_le_nsec": runDeltaWaitBounds, "dispatch_wait_cumulative_counts": stats.WaitBuckets,
					"queue_timeouts": stats.QueueTimeouts, "cancelled": stats.Cancelled, "duplicate_items": stats.Duplicates, "store_errors": stats.StoreErrors,
					"pending_at_end": stats.Pending, "pending_bytes_at_end": stats.PendingBytes}}
		}) {
			t.FailNow()
		}
	}
	off, on := evidence[0], evidence[1]
	if off.Digest != on.Digest || off.Runs != on.Runs || off.Events != on.Events || on.Commits >= off.Commits {
		t.Fatal("cohorts differ in canonical output or do not demonstrate fewer physical delta transactions")
	}
	writeCapacityReport(t, path, map[string]any{"format": "studio.delta-batch-comparison.v1", "runs_per_cohort": n, "cohorts": cohorts,
		"event_fixture": "201 ordered HTTP gateway waves, fixed model and workspace assignment", "order": "off then on; separate freshly migrated schemas on the same database",
		"throughput_window": "first forced wave release through last ordinary live reader observing the final nonempty delta, including 100 ms wave spacing",
		"commit_scope":      "committed message.delta insert xids for the measured runs only", "delta_commits_saved": off.Commits - on.Commits,
		"delta_commit_reduction_percent": 100 * float64(off.Commits-on.Commits) / float64(off.Commits)})
	t.Logf("verified identical %d deltas, physical delta insert transactions off=%d on=%d", off.Events, off.Commits, on.Commits)
}

func observeBatchTransactions(t *testing.T, m activeCapacityMeasurement, n int, enabled bool) batchDeltaEvidence {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ids := make([]string, 0, len(m.traces))
	for _, trace := range m.traces {
		ids = append(ids, trace.run.ID)
	}
	e := batchDeltaEvidence{}
	const scope = " FROM studio_run_stream_events WHERE name='message.delta' AND run_id=ANY($1::text[])"
	if err := m.admin.QueryRow(ctx, "SELECT count(*),count(DISTINCT run_id),count(DISTINCT xmin::text)"+scope, ids).Scan(&e.Events, &e.Runs, &e.Commits); err != nil {
		capacityDBFailure(t, "delta_xmin_counts", err)
	}
	rows, err := m.admin.Query(ctx, `SELECT runs,events,count(*) FROM (
		SELECT xmin::text AS xid,count(DISTINCT run_id) AS runs,count(*) AS events`+scope+`
		GROUP BY xmin::text
	) grouped GROUP BY runs,events ORDER BY runs,events`, ids)
	if err != nil {
		capacityDBFailure(t, "delta_xmin_shape", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s batchTransactionShape
		if err := rows.Scan(&s.Runs, &s.Events, &s.Transactions); err != nil {
			capacityDBFailure(t, "delta_xmin_shape", err)
		}
		e.Shapes = append(e.Shapes, s)
	}
	if err := rows.Err(); err != nil {
		capacityDBFailure(t, "delta_xmin_shape", err)
	}
	if err := e.validate(n, enabled); err != nil {
		t.Fatal(err)
	}

	rows, err = m.admin.Query(ctx, `SELECT lane.ordinality-1,e.seq,e.delta
		FROM unnest($1::text[]) WITH ORDINALITY lane(id,ordinality)
		JOIN studio_run_stream_events e ON e.run_id=lane.id AND e.name='message.delta'
		ORDER BY lane.ordinality,e.seq`, ids)
	if err != nil {
		capacityDBFailure(t, "delta_fixture_digest", err)
	}
	defer rows.Close()
	hash := sha256.New()
	count := 0
	for rows.Next() {
		var lane, seq int
		var delta string
		if err := rows.Scan(&lane, &seq, &delta); err != nil {
			capacityDBFailure(t, "delta_fixture_digest", err)
		}
		if lane != count/activeStreamSteps || seq != count%activeStreamSteps+1 || delta != activeDelta(lane, seq-1) {
			t.Fatal("ordered stored fixture differs from released waves")
		}
		fmt.Fprintf(hash, "%d/%d/%d/%s\n", lane, seq, len(delta), delta)
		count++
	}
	if err := rows.Err(); err != nil {
		capacityDBFailure(t, "delta_fixture_digest", err)
	}
	if count != e.Events || m.wave <= 0 {
		t.Fatal("incomplete fixture or elapsed time")
	}
	e.Digest = hex.EncodeToString(hash.Sum(nil))
	e.EventsPerSecond = float64(e.Events) / m.wave.Seconds()
	return e
}

func TestPostgresBatchedDeltaBusinessBoundaries(t *testing.T) {
	requiredBatchStreams(t)
	t.Run("confirmed-two-run-group-rollback", testTwoRunBatchRollback)
	t.Run("inflight-cancel", func(t *testing.T) { testStreamPendingCancel(t, true) })
	t.Run("known-cancel-rollback", func(t *testing.T) { testStreamCancelRollback(t, true) })
	t.Run("private-candidates", func(t *testing.T) { testStreamPrivateCandidates(t, true) })
	t.Run("process-commit-boundary", func(t *testing.T) { testStreamProcessCommitBoundary(t, true) })
}

func TestBatchedDeltaTransactionEvidence(t *testing.T) {
	const n = 8
	cases := []struct {
		name    string
		on      bool
		commits int
		shape   []batchTransactionShape
		accept  bool
	}{
		{"separate", false, 96, []batchTransactionShape{{1, 1, 96}}, true},
		{"mixed", true, 32, []batchTransactionShape{{1, 1, 16}, {5, 5, 16}}, true},
		{"no-sharing", true, 96, []batchTransactionShape{{1, 1, 96}}, false},
		{"same-run", true, 12, []batchTransactionShape{{1, 8, 12}}, false},
		{"extra-event", true, 12, []batchTransactionShape{{8, 8, 11}}, false},
		{"wrong-xid-count", true, 13, []batchTransactionShape{{8, 8, 12}}, false},
		{"off-shared", false, 12, []batchTransactionShape{{8, 8, 12}}, false},
		{"too-many-runs", true, 6, []batchTransactionShape{{16, 16, 6}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := batchDeltaEvidence{Runs: n, Events: n * activeStreamSteps, Commits: tc.commits, Shapes: tc.shape}
			if (e.validate(n, tc.on) == nil) != tc.accept {
				t.Fatal("unexpected proof validation result")
			}
		})
	}
}
