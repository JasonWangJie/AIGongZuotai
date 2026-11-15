package studio

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Only test-controlled ordinary poll time can advance. This helper refuses to
// shorten a server deadline, cooldown, probe or an existing execution lease.
func advanceMediaScheduleDue(t *testing.T, a *App, id string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	if a.store == nil {
		r := a.tasks[id]
		if r == nil {
			t.Fatal("missing scheduled task")
		}
		q := memoryMediaSchedule(r)
		if q == nil {
			t.Fatal("invalid schedule")
		}
		a.mediaScheduler.mu.Lock()
		c := a.mediaScheduler.memoryChannels[mediaLaneForTask(r)][q.ChannelKey]
		a.mediaScheduler.mu.Unlock()
		if r.InFlight || a.mediaExecution.memoryAttempts[id] != "" || now.Before(q.ServerNotBefore) || c.Parked || now.Before(c.CooldownUntil) || c.ProbeID != "" {
			t.Fatal("test cannot bypass server delay, cooldown or active work")
		}
		r.NextPoll = now.Add(-time.Millisecond)
		q.EligibleAt = r.NextPoll
		return
	}
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		var held bool
		if err = tx.QueryRow(ctx, `SELECT lease_until IS NOT NULL AND lease_until>clock_timestamp() FROM studio_tasks WHERE id=$1 FOR UPDATE`, id).Scan(&held); err != nil {
			return err
		}
		q, err := loadMediaScheduleSQL(ctx, tx, id)
		if err != nil {
			return err
		}
		if q == nil {
			return fmt.Errorf("schedule missing")
		}
		c, err := loadMediaChannelSQL(ctx, tx, mediaScheduleScope(mediaLaneFromName(q.Lane)), q.Lane, q.ChannelKey, false)
		if err != nil {
			return err
		}
		if held || now.Before(q.ServerNotBefore) || c.Parked || now.Before(c.CooldownUntil) || c.ProbeID != "" {
			return failure(409, "TEST_CLOCK_REFUSED", "test cannot bypass delay or lease")
		}
		if _, err = tx.Exec(ctx, `UPDATE studio_tasks SET next_poll=clock_timestamp()-interval '1 millisecond' WHERE id=$1`, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := a.tasks[id]; r != nil {
		r.NextPoll = now.Add(-time.Millisecond)
	}
}

func TestMediaSchedulerScopedBudgetAndConfiguration(t *testing.T) {
	for _, c := range []MediaSchedulingConfig{{WorkspaceConcurrency: -1}, {WorkspaceConcurrency: 33}, {ChannelConcurrency: -1}, {ChannelConcurrency: 33}} {
		if c.normalize(4) == nil {
			t.Fatal("invalid scope limits")
		}
	}
	c := MediaSchedulingConfig{SubmitWorkers: 4, QueryWorkers: 4}
	if err := c.normalize(4); err != nil {
		t.Fatal(err)
	}
	if c.WorkspaceConcurrency != 1 || c.ChannelConcurrency != 2 {
		t.Fatal(c)
	}
	s := newMediaScheduler(c)
	key := strings.Repeat("a", 64)
	a := mediaScheduleCandidate{WorkspaceID: "a", ChannelKey: key, Lane: mediaSubmitLane}
	releaseA, ok := s.tryReserve(a)
	if !ok {
		t.Fatal("first scope missing")
	}
	if _, ok = s.tryReserve(a); ok {
		t.Fatal("workspace bypass")
	}
	b := a
	b.WorkspaceID = "b"
	releaseB, ok := s.tryReserve(b)
	if !ok {
		t.Fatal("second workspace blocked")
	}
	third := b
	third.WorkspaceID = "c"
	if _, ok = s.tryReserve(third); ok {
		t.Fatal("channel alias bypass")
	}
	q := third
	q.Lane = mediaQueryLane
	releaseQ, ok := s.tryReserve(q)
	if !ok {
		t.Fatal("submit exhausted query scope")
	}
	releaseA()
	releaseA()
	releaseB()
	releaseQ()
	for _, v := range s.snapshot().Lanes {
		if v.ScopeInFlight != 0 {
			t.Fatal("scope leak")
		}
	}
}

func TestMediaSchedulerDelayStableAndLateSuccess(t *testing.T) {
	now := time.Now().UTC()
	o := &mediaScheduleOutcome{ID: "stable", At: now, Lane: mediaQueryLane, Temporary: true, BaseInterval: 3 * time.Second, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "rate_limit", HasServerDelay: true, RetryNotBefore: now.Add(time.Minute)}}
	old := mediaTaskSchedule{FailureCount: 2}
	due, f, u := mediaScheduleDelay(o, old)
	due2, f2, u2 := mediaScheduleDelay(o, old)
	if !due.Equal(due2) || f != f2 || u != u2 || due.Before(o.Observation.RetryNotBefore) || f != 3 {
		t.Fatal("unstable or early backoff")
	}
	r := mediaChannelRuntime{Version: 4, EffectiveLimit: 4}
	r = applyMediaChannelObservation(r, o, &mediaScheduleCandidate{ChannelVersion: 4}, 4)
	if r.EffectiveLimit != 2 || !r.ProbeRequired || r.CooldownUntil.Before(o.Observation.RetryNotBefore) {
		t.Fatal(r)
	}
	success := &mediaScheduleOutcome{At: now.Add(time.Second), Lane: mediaQueryLane, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "success"}}
	late := applyMediaChannelObservation(r, success, &mediaScheduleCandidate{ChannelVersion: 4}, 4)
	if late != r {
		t.Fatal("old success reset newer cooldown")
	}
	r.ProbeID = "probe"
	until := now.Add(time.Minute)
	r.ProbeUntil = &until
	healthy := applyMediaChannelObservation(r, success, &mediaScheduleCandidate{ProbeID: "probe", ChannelVersion: r.Version}, 4)
	if healthy.ProbeRequired || healthy.ProbeID != "" {
		t.Fatal("matching GET did not recover")
	}
	for _, scope := range []string{"source", "storage", "review"} {
		other := *o
		fact := *o.Observation
		fact.Scope = scope
		other.Observation = &fact
		if got := applyMediaChannelObservation(r, &other, nil, 4); got != r {
			t.Fatal("other service poisoned gateway", scope)
		}
	}
	cancelled := *o
	cancelled.Observation = &MediaScheduleObservation{Scope: "gateway", Class: "cancelled"}
	if got := applyMediaChannelObservation(r, &cancelled, nil, 4); got != r {
		t.Fatal("cancel poisoned gateway")
	}
}

func TestMediaSchedulerMemoryRingAndFuturePages(t *testing.T) {
	a, _ := testApp(t, Config{PollInterval: time.Hour})
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for w := 0; w < 21; w++ {
		for n := 0; n < 12; n++ {
			id := fmt.Sprintf("task_%02d_%02d", w, n)
			next := now.Add(time.Hour)
			if w >= 19 && n == 11 {
				next = now.Add(-time.Second)
			}
			a.tasks[id] = &taskRecord{Task: Task{ID: id, WorkspaceID: fmt.Sprintf("ws_%02d", w), ProviderMode: "demo", Status: "QUEUED"}, NextPoll: next}
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		r, _ := a.selectMemoryScheduleLocked(mediaSubmitLane, "")
		if r != nil {
			seen[r.Task.WorkspaceID] = true
			r.NextPoll = now.Add(time.Hour)
		}
	}
	if !seen["ws_19"] || !seen["ws_20"] {
		t.Fatal("future pages starved eligible workspaces", seen)
	}
}

func TestMediaSchedulerExplicitIDHonorsDue(t *testing.T) {
	p := &leaseMedia{}
	a, _ := testApp(t, Config{PollInterval: time.Hour, mediaProvider: p})
	task := leaseTask(t, a)
	a.executeTaskStep(task.ID)
	a.executeTaskStep(task.ID)
	if p.submits.Load() != 1 || p.queries.Load() != 0 {
		t.Fatal("explicit id bypassed due")
	}
	advanceMediaScheduleDue(t, a, task.ID)
	a.executeTaskStep(task.ID)
	if p.submits.Load() != 1 || p.queries.Load() != 1 {
		t.Fatal("due query failed")
	}
}
