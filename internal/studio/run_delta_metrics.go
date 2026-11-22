package studio

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var runDeltaWaitBounds = [...]time.Duration{
	time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
	25 * time.Millisecond, 100 * time.Millisecond, time.Second, 5 * time.Second,
}

type runDeltaCoordinatorStats struct {
	CommittedBatches, AppendedEvents, SoloAttempts                     uint64
	ConfirmedEvents, StoreErrors, QueueTimeouts, Cancelled, Duplicates uint64
	WaitCount, WaitNanos                                               uint64
	WaitBuckets                                                        [len(runDeltaWaitBounds)]uint64
	Pending, PendingBytes                                              int
}

func (s *runDeltaCoordinatorStats) observeWait(d time.Duration) {
	s.WaitCount++
	s.WaitNanos += uint64(d)
	for i, b := range runDeltaWaitBounds {
		if d <= b {
			s.WaitBuckets[i]++
		}
	}
}
func (c *runDeltaCoordinator) snapshot() runDeltaCoordinatorStats {
	if c == nil {
		return runDeltaCoordinatorStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Pending, s.PendingBytes = len(c.active), c.bytes
	return s
}
func (c *runDeltaCoordinator) writeMetrics(out *strings.Builder) {
	s := c.snapshot()
	for _, metric := range []struct {
		name, help string
		value      uint64
	}{
		{"studio_run_delta_batch_commits_total", "Group SQL transactions confirmed to have appended new events.", s.CommittedBatches},
		{"studio_run_delta_batch_appended_events_total", "New events in confirmed group SQL transactions.", s.AppendedEvents},
		{"studio_run_delta_batch_solo_attempts_total", "Items actually attempted through individual SQL appends by the coordinator.", s.SoloAttempts},
		{"studio_run_delta_batch_confirmed_events_total", "Successful per-item storage responses, including individual and idempotent confirmations.", s.ConfirmedEvents},
	} {
		metricHeader(out, metric.name, metric.help, "counter")
		fmt.Fprintf(out, "%s %d\n", metric.name, metric.value)
	}
	const rejected = "studio_run_delta_batch_rejections_total"
	metricHeader(out, rejected, "Items without a positive confirmation, including unconfirmed storage outcomes.", "counter")
	for _, reason := range []struct {
		label string
		value uint64
	}{
		{"store", s.StoreErrors}, {"queue_timeout", s.QueueTimeouts}, {"cancelled", s.Cancelled}, {"duplicate", s.Duplicates},
	} {
		fmt.Fprintf(out, "%s{reason=%q} %d\n", rejected, reason.label, reason.value)
	}
	const pending = "studio_run_delta_batch_pending"
	metricHeader(out, pending, "Currently admitted delta items, including storage in progress.", "gauge")
	fmt.Fprintf(out, "%s %d\n", pending, s.Pending)
	const latency = "studio_run_delta_batch_wait_seconds"
	metricHeader(out, latency, "Per-item time from requesting admission to dispatch to storage, excluding that storage operation.", "histogram")
	for i, b := range runDeltaWaitBounds {
		fmt.Fprintf(out, "%s_bucket{le=%q} %d\n", latency, strconv.FormatFloat(b.Seconds(), 'g', -1, 64), s.WaitBuckets[i])
	}
	fmt.Fprintf(out, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %s\n%s_count %d\n", latency, s.WaitCount, latency,
		strconv.FormatFloat(float64(s.WaitNanos)/1e9, 'g', -1, 64), latency, s.WaitCount)
}
