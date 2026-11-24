package studio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type openLoopSampler259 struct {
	watch openLoopWatch259
	pop   openLoopPGPopulation259
}

func runOpenLoop259(t *testing.T, parent context.Context, p openLoopProfile259, start time.Time, output openLoopReportFile259, app *App, api string, g *openLoopGateway259, accounts []admissionCapacityAccount, admin, db *pgx.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	records := make([]openLoopAdmission259, p.slots())
	tracker := newOpenLoopDueTracker259()
	sampler := openLoopSampler259{}
	stop := make(chan struct{})
	observed := make(chan error, 1)
	population := make(chan openLoopPopulation259, 1)
	nextReport := start.Add(p.Window)
	// Publication through observed joins the observer before its records or the
	// report destination are used by the main driver again.
	go func() {
		err := sampler.watch.loop(ctx, start, p, func(ctx context.Context) error {
			if err := sampler.pop.sample(ctx, db); err != nil {
				return err
			}
			if err := tracker.sample(ctx, db); err != nil {
				return err
			}
			sample := sampler.pop.samples[len(sampler.pop.samples)-1]
			select {
			case <-population:
			default:
			}
			select {
			case population <- sample:
			default:
			}
			if !sample.When.Before(nextReport) {
				if err := output.append(map[string]any{"type": "progress", "at_ms": sample.When.Sub(start).Milliseconds(), "persisted_active": sample.Active, "uncertain": sample.Unknown, "persisted_terminal": sample.Terminal, "pg_samples": len(sampler.pop.samples)}); err != nil {
					return err
				}
				for !nextReport.After(sample.When) {
					nextReport = nextReport.Add(p.Window)
				}
			}
			return nil
		}, stop)
		observed <- err
		if err != nil {
			cancel()
		}
	}()
	loadErr := openLoopSlots259(ctx, p, start, accounts, api, records)
	accepted := 0
	for _, r := range records {
		if r.outcome == "accepted" {
			accepted++
		}
	}
	drainDeadline := start.Add(p.Warm + p.Steady + p.DrainLimit)
	deadline := time.NewTimer(max(time.Nanosecond, time.Until(drainDeadline)))
	defer deadline.Stop()
	drained := false
	if loadErr == nil {
	waitDrain:
		for {
			select {
			case sample := <-population:
				if !sample.When.Before(start.Add(p.Warm+p.Steady)) && sample.Active == 0 && sample.Terminal == accepted && sample.Unknown == 0 {
					drained = true
					break waitDrain
				}
				if sample.Unknown != 0 {
					loadErr = errors.New("a confirmed task entered an uncertain state")
					break waitDrain
				}
			case <-ctx.Done():
				loadErr = errors.New("observation interrupted before drain finished")
				break waitDrain
			case <-deadline.C:
				loadErr = errors.New("drain did not finish before the fixed time limit")
				break waitDrain
			}
		}
	}
	close(stop)
	observerErr := <-observed
	end := time.Now()
	if observerErr != nil || loadErr != nil || !drained {
		output.write(t, map[string]any{"type": "summary", "complete": false, "drained": drained, "planned_slots": p.slots(), "acknowledged": accepted, "arrival_outcomes": openLoopAdmissionOutcomes259(records), "observer_samples": sampler.watch.ticks, "observer_skipped_slots": sampler.watch.skips})
		t.Fatal("fixed-rate load or independent durable observation failed", loadErr, observerErr)
	}
	wire := g.snapshot()
	rows, verifyErr := openLoopVerifyRows259(ctx, admin, accounts, records, wire.original)
	series := newOpenLoopSeries259(p, start, end)
	errorsFound := []error{verifyErr, series.admissions(records), series.populations(sampler.pop.samples), series.terminalObservations(records, rows, sampler.pop.firstTerminal)}
	series.finish()
	errorsFound = append(errorsFound, series.verify(p.slots()))
	reportWire, wireWindows, wireErr := wire.report(p, start, end)
	reportDue, dueWindows, dueErr := tracker.report(start, end, p.Window, wire.writtenSnapshot())
	errorsFound = append(errorsFound, wireErr, dueErr)
	for _, err := range errorsFound {
		if err != nil {
			t.Error(err)
		}
	}
	if len(series.w) != len(wireWindows) || len(series.w) != len(dueWindows) {
		t.Fatal("observers have incompatible fixed window coverage")
	}
	for i, w := range series.w {
		output.write(t, map[string]any{"type": "window", "index": i, "workload": w, "wire": wireWindows[i], "pg_due": dueWindows[i]})
	}
	replays := make([]admissionCapacityRecord, 0, accepted)
	for _, r := range records {
		if r.outcome == "accepted" {
			replays = append(replays, r.admissionCapacityRecord)
		}
	}
	admissionCapacityReplay(t, ctx, api, accounts, replays)
	if !g.unchangedAfterReplay(wire) {
		t.Error("receipt reads produced more supplier activity")
	}
	output.write(t, map[string]any{"type": "summary", "complete": !t.Failed(), "profile": p.Name, "steady": series.steady(), "planned_slots": p.slots(), "new_202": accepted, "arrival_outcomes": openLoopAdmissionOutcomes259(records), "matched_terminal_rows": len(rows), "original_receipts": len(replays), "drained": drained, "observed_seconds": end.Sub(start).Seconds(), "observer_samples": sampler.watch.ticks, "observer_skipped_slots": sampler.watch.skips, "observer_max_start_delay_ms": sampler.watch.maxDelay.Milliseconds(), "wire": reportWire, "sampled_due": reportDue, "query_dispatch_attempts": app.mediaExecution.snapshot().QueryDispatchAttempts})
}
