package studio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fairPermitGrant struct {
	name   string
	permit *mediaModerationPermit
	err    error
}

func waitFairSnapshot(t *testing.T, e *mediaModerationEngine, in, out int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s := e.permitSnapshot()
		if s.InputWaiting == in && s.OutputWaiting == out {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("permit queue did not reach expected bound", e.permitSnapshot())
}
func enqueueFairPermit(e *mediaModerationEngine, ctx context.Context, ws, name string, out bool, ch chan<- fairPermitGrant) {
	go func() {
		var p *mediaModerationPermit
		var err error
		if out {
			p, err = e.acquireReviewPermit(ctx, ws)
		} else {
			p, err = e.acquireInputPermit(ctx, ws)
		}
		ch <- fairPermitGrant{name, p, err}
	}()
}
func TestMediaFairPermitAlternatesClassesAndInputWorkspaces(t *testing.T) {
	e := mediaPolicyFixture(t)
	held := mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grants := make(chan fairPermitGrant, 8)
	for i, item := range []struct{ ws, name string }{{"A", "A1"}, {"A", "A2"}, {"B", "B1"}} {
		enqueueFairPermit(e, ctx, item.ws, item.name, false, grants)
		waitFairSnapshot(t, e, i+1, 0)
	}
	enqueueFairPermit(e, ctx, "O", "O1", true, grants)
	waitFairSnapshot(t, e, 3, 1)
	enqueueFairPermit(e, ctx, "O", "O2", true, grants)
	waitFairSnapshot(t, e, 3, 2)
	if p, ok := e.tryAcquireReviewPermit(); ok || p != nil {
		t.Fatal("legacy caller jumped queued work")
	}
	held.Close()
	for _, want := range []string{"A1", "O1", "B1", "O2", "A2"} {
		got := reviewPermitWait(t, grants)
		if got.err != nil || got.name != want {
			t.Fatalf("fair order got %s want %s: %v", got.name, want, got.err)
		}
		got.permit.Close()
	}
	waitFairSnapshot(t, e, 0, 0)
}
func TestMediaFairPermitQueueBoundsAndCancellationCleanup(t *testing.T) {
	e := mediaPolicyFixture(t)
	_ = mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grants := make(chan fairPermitGrant, 20)
	for i := 0; i < 16; i++ {
		enqueueFairPermit(e, ctx, fmt.Sprintf("ws%d", i/2), fmt.Sprint(i), false, grants)
		waitFairSnapshot(t, e, i+1, 0)
	}
	for _, ws := range []string{"ws0", "new-workspace"} {
		p, err := e.acquireInputPermit(ctx, ws)
		var op *operationError
		if p != nil || !errors.As(err, &op) || op.code != "MEDIA_MODERATION_BUSY" {
			t.Fatal("input queue limit was not a business rejection", err)
		}
	}
	for i := 0; i < 2; i++ {
		enqueueFairPermit(e, ctx, "output", fmt.Sprint(i), true, grants)
		waitFairSnapshot(t, e, 16, i+1)
	}
	if p, err := e.acquireReviewPermit(ctx, "third-output"); p != nil || err == nil {
		t.Fatal("output queue exceeded two workers")
	}
	cancel()
	for range 18 {
		g := reviewPermitWait(t, grants)
		if g.permit != nil || !errors.Is(g.err, context.Canceled) {
			t.Fatal("cancelled waiter acquired a permit", g.err)
		}
	}
	waitFairSnapshot(t, e, 0, 0)
	if e.permitSnapshot().InFlight != 2 {
		t.Fatal("waiting cancellation released active work")
	}
}
func TestMediaFairPermitPerWorkspaceBoundAndTenSecondWait(t *testing.T) {
	e := mediaPolicyFixture(t)
	_ = mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grants := make(chan fairPermitGrant, 2)
	enqueueFairPermit(e, ctx, "A", "A1", false, grants)
	waitFairSnapshot(t, e, 1, 0)
	enqueueFairPermit(e, ctx, "A", "A2", false, grants)
	waitFairSnapshot(t, e, 2, 0)
	if p, err := e.acquireInputPermit(ctx, "A"); p != nil || err == nil {
		t.Fatal("one workspace queued more than two inputs")
	}
	start := time.Now()
	p, err := e.acquireInputPermit(context.Background(), "B")
	var op *operationError
	if p != nil || !errors.As(err, &op) || op.code != "MEDIA_MODERATION_BUSY" || time.Since(start) < 9*time.Second || time.Since(start) > 12*time.Second {
		t.Fatal("ten second queue expiry is not bounded business error", err, time.Since(start))
	}
	cancel()
	for range 2 {
		g := reviewPermitWait(t, grants)
		if g.permit != nil {
			g.permit.Close()
			t.Fatal("full queue unexpectedly granted")
		}
	}
	waitFairSnapshot(t, e, 0, 0)
}
func TestMediaFairPermitGrantCancellationRaceNeverLeaks(t *testing.T) {
	e := mediaPolicyFixture(t)
	for range 100 {
		a := mustReviewPermit(t, e)
		b := mustReviewPermit(t, e)
		ctx, cancel := context.WithCancel(context.Background())
		grants := make(chan fairPermitGrant, 1)
		enqueueFairPermit(e, ctx, "race", "input", false, grants)
		waitFairSnapshot(t, e, 1, 0)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); cancel() }()
		go func() { defer wg.Done(); a.Close() }()
		wg.Wait()
		g := reviewPermitWait(t, grants)
		if g.permit != nil {
			g.permit.Close()
		} else if !errors.Is(g.err, context.Canceled) {
			t.Fatal(g.err)
		}
		b.Close()
		s := e.permitSnapshot()
		if s.InFlight != 0 || s.InputWaiting != 0 || s.OutputWaiting != 0 {
			t.Fatal("grant/cancel leaked shared capacity", s)
		}
	}
}
func TestMediaFairPermitRealHTTPInputCannotStarveOutput(t *testing.T) {
	f := newReviewPermitHTTPFixture(t)
	e := f.engine
	raw, content := mediaBytesFixture(t, "allow.png")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan ModerationDecision, 4)
	startCheck := func(ws string, output bool) {
		go func() {
			var p *mediaModerationPermit
			var err error
			if output {
				p, err = e.acquireReviewPermit(ctx, ws)
			} else {
				p, err = e.acquireInputPermit(ctx, ws)
			}
			if err != nil {
				results <- moderationReview("check_busy")
				return
			}
			defer p.Close()
			phase := "input"
			if output {
				phase = "output"
			}
			results <- p.Check(ctx, e.Snapshot(), phase, content, bytes.NewReader(raw))
		}()
	}
	startCheck("A", false)
	first := reviewPermitWait(t, f.requests)
	startCheck("A", false)
	second := reviewPermitWait(t, f.requests)
	startCheck("output", true)
	waitFairSnapshot(t, e, 0, 1)
	startCheck("B", false)
	waitFairSnapshot(t, e, 1, 1)
	close(first.release)
	if got := reviewPermitWait(t, results); got.State != "ALLOWED" {
		t.Fatal(got)
	}
	third := reviewPermitWait(t, f.requests)
	if third.metadata.Phase != "output" {
		t.Fatal("continuous inputs passed a waiting output")
	}
	close(third.release)
	if got := reviewPermitWait(t, results); got.State != "ALLOWED" {
		t.Fatal(got)
	}
	fourth := reviewPermitWait(t, f.requests)
	if fourth.metadata.Phase != "input" {
		t.Fatal("output class starved input")
	}
	close(second.release)
	close(fourth.release)
	for range 2 {
		if got := reviewPermitWait(t, results); got.State != "ALLOWED" {
			t.Fatal(got)
		}
	}
	if f.peak.Load() != 2 || f.calls.Load() != 4 {
		t.Fatal("fairness changed physical HTTP budget")
	}
}
