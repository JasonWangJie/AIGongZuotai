package studio

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func enqueueTestDelta(ctx context.Context, c *runDeltaCoordinator, id, text string) <-chan error {
	out := make(chan error, 1)
	go func() { out <- c.append(ctx, runDeltaAppend{Lease: runLease{RunID: id}, Delta: text, At: time.Now()}) }()
	return out
}

func TestRunDeltaCoordinatorBatchBoundsAndIndividualReceipts(t *testing.T) {
	for _, tc := range []struct {
		name               string
		count, size, first int
	}{
		{"item-limit", 35, 3, 32}, {"byte-limit", 5, maxRunOutputBytes, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, stop := context.WithCancel(t.Context())
				defer stop()
				conflict := runStreamConflict()
				var sizes []int
				c := newRunDeltaCoordinator(ctx, func(items []runDeltaAppend) ([]error, runDeltaBatchStats) {
					sizes = append(sizes, len(items))
					result := make([]error, len(items))
					used := 0
					for i, v := range items {
						used += len(v.Delta)
						if v.Lease.RunID == "private-id-0" {
							result[i] = conflict
						}
					}
					if len(items) > maxRunDeltaBatchItems || used > maxRunDeltaBatchBytes {
						t.Error("storage batch exceeds its limits")
					}
					return result, runDeltaBatchStats{}
				})
				var results []<-chan error
				for i := range tc.count {
					results = append(results, enqueueTestDelta(ctx, c, "private-id-"+strconv.Itoa(i), strings.Repeat("x", tc.size)))
				}
				synctest.Wait()
				if snap := c.snapshot(); snap.Pending != tc.count || snap.PendingBytes != tc.count*tc.size {
					t.Fatal("missing queued work", snap)
				}
				go c.run()
				for i, r := range results {
					err := <-r
					if i == 0 && err != conflict || i != 0 && err != nil {
						t.Fatal("result delivered to wrong Run", i, err)
					}
				}
				snap := c.snapshot()
				if len(sizes) < 2 || sizes[0] != tc.first || snap.Pending != 0 || snap.PendingBytes != 0 || snap.WaitCount != uint64(tc.count) || snap.ConfirmedEvents != uint64(tc.count-1) || snap.StoreErrors != 1 {
					t.Fatal("inaccurate storage or queue accounting", sizes, snap)
				}
				if snap.WaitBuckets[len(snap.WaitBuckets)-1] != snap.WaitCount {
					t.Fatal("invalid cumulative wait histogram")
				}
			})
		})
	}
}

func TestRunDeltaCoordinatorWithdrawalAndShutdownRespectSentFacts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, shutdown := context.WithCancel(t.Context())
		defer shutdown()
		caller, abort := context.WithCancel(t.Context())
		defer abort()
		entered, finish := make(chan struct{}), make(chan struct{})
		unknown := errors.New("storage confirmation unavailable")
		saves := 0
		c := newRunDeltaCoordinator(ctx, func(items []runDeltaAppend) ([]error, runDeltaBatchStats) {
			saves++
			close(entered)
			<-finish
			results := make([]error, len(items))
			for i, v := range items {
				if v.Lease.RunID == "unconfirmed" {
					results[i] = unknown
				}
			}
			return results, runDeltaBatchStats{CommittedBatches: 1, AppendedEvents: 1, SoloAttempts: 1}
		})
		known := enqueueTestDelta(caller, c, "confirmed", "private")
		absent := enqueueTestDelta(caller, c, "unconfirmed", "private")
		synctest.Wait()
		if err := <-enqueueTestDelta(ctx, c, "confirmed", "duplicate"); err == nil {
			t.Fatal("admitted two deltas for one Run")
		}
		go c.run()
		<-entered
		unsent := enqueueTestDelta(ctx, c, "never-sent", "private")
		synctest.Wait()
		abort()
		shutdown()
		synctest.Wait()
		if !errors.Is(<-unsent, context.Canceled) {
			t.Fatal("queued work was not withdrawn")
		}
		if len(known) != 0 || len(absent) != 0 || c.snapshot().Pending != 2 {
			t.Fatal("supplied a result before a sent operation was resolved")
		}
		close(finish)
		if <-known != nil || <-absent != unknown {
			t.Fatal("local cancellation replaced a storage receipt")
		}
		synctest.Wait()
		s := c.snapshot()
		if saves != 1 || s.CommittedBatches != 1 || s.AppendedEvents != 1 || s.SoloAttempts != 1 || s.ConfirmedEvents != 1 || s.StoreErrors != 1 || s.Duplicates != 1 || s.Cancelled != 1 || s.Pending != 0 {
			t.Fatal("repeated work or wrong storage accounting", s)
		}
		var metrics strings.Builder
		c.writeMetrics(&metrics)
		for _, line := range []string{"studio_run_delta_batch_commits_total 1\n", "studio_run_delta_batch_appended_events_total 1\n", "studio_run_delta_batch_solo_attempts_total 1\n", "studio_run_delta_batch_rejections_total{reason=\"store\"} 1\n", "studio_run_delta_batch_wait_seconds_count 2\n"} {
			if !strings.Contains(metrics.String(), line) {
				t.Fatal("missing fixed aggregate", line)
			}
		}
		if strings.Contains(metrics.String(), "private") || strings.Contains(metrics.String(), unknown.Error()) {
			t.Fatal("sensitive input reached metrics")
		}
	})
}

func TestRunDeltaCoordinatorBoundedQueueDoesNotSubmitTimedOutWork(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count, size int
	}{
		{"bytes", runDeltaPendingBytes / maxRunOutputBytes, maxRunOutputBytes},
		{"items", runDeltaPendingItems, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, shutdown := context.WithCancel(t.Context())
				defer shutdown()
				saves := 0
				c := newRunDeltaCoordinator(ctx, func(items []runDeltaAppend) ([]error, runDeltaBatchStats) {
					saves++
					return make([]error, len(items)), runDeltaBatchStats{}
				})
				// Fill the queue before its worker starts. Each blocked caller is a
				// distinct Run. Releasing one item must actually admit the waiter.
				text := strings.Repeat("a", tc.size)
				firstCtx, cancelFirst := context.WithCancel(ctx)
				first := enqueueTestDelta(firstCtx, c, "first", text)
				var others []<-chan error
				for i := 1; i < tc.count; i++ {
					others = append(others, enqueueTestDelta(ctx, c, strconv.Itoa(i), text))
				}
				synctest.Wait()
				extra := enqueueTestDelta(ctx, c, "overflow", text)
				synctest.Wait()
				if s := c.snapshot(); s.Pending != tc.count || s.PendingBytes != tc.count*tc.size {
					t.Fatal("pending capacity exceeded", s)
				}
				cancelFirst()
				if !errors.Is(<-first, context.Canceled) {
					t.Fatal("cancelled waiting item remained admitted")
				}
				synctest.Wait()
				if c.snapshot().Pending != tc.count {
					t.Fatal("freed capacity did not wake waiter")
				}
				time.Sleep(c.queueTimeout + time.Nanosecond)
				for _, r := range append(others, extra) {
					if <-r != errRunDeltaQueueTimeout {
						t.Fatal("missing truthful pre-dispatch timeout")
					}
				}
				go c.run()
				synctest.Wait()
				if s := c.snapshot(); saves != 0 || s.Pending != 0 || s.PendingBytes != 0 || s.QueueTimeouts != uint64(tc.count) || s.Cancelled != 1 {
					t.Fatal("late storage work after confirmed withdrawal", s)
				}
			})
		})
	}
}

func TestRunDeltaCoordinatorRequiresPostgresAndDefaultsOff(t *testing.T) {
	if (Config{}).RunDeltaBatching {
		t.Fatal("batching defaulted on")
	}
	if _, err := New(Config{RunDeltaBatching: true}); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatal("memory configuration accepted", err)
	}
	var disabled *runDeltaCoordinator
	if got := disabled.snapshot(); got != (runDeltaCoordinatorStats{}) {
		t.Fatal("disabled counters not zero")
	}
}
