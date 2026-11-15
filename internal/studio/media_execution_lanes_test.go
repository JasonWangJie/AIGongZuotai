package studio

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMediaExecutionConfigurationAndSeparateBudgets(t *testing.T) {
	for _, test := range []struct{ legacy, submit, query int }{{4, 2, 2}, {1, 1, 1}, {2, 1, 1}, {5, 3, 2}} {
		var c MediaSchedulingConfig
		if err := c.normalize(test.legacy); err != nil || c.SubmitWorkers != test.submit || c.QueryWorkers != test.query || c.DeliveryWorkers != 2 || c.ReviewWorkers != 2 {
			t.Fatal("legacy/default split changed", test, c, err)
		}
	}
	for _, c := range []MediaSchedulingConfig{{SubmitWorkers: -1}, {SubmitWorkers: 33}, {QueryWorkers: -1}, {QueryWorkers: 33}, {DeliveryWorkers: -1}, {DeliveryWorkers: 17}, {ReviewWorkers: -1}, {ReviewWorkers: 3}} {
		if c.normalize(4) == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	m := newMediaExecutionLanes(MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 2, DeliveryWorkers: 1, ReviewWorkers: 1})
	releaseSubmit, ok := m.reserve(mediaSubmitLane)
	if !ok {
		t.Fatal("submit budget unavailable")
	}
	if _, ok = m.reserve(mediaSubmitLane); ok {
		t.Fatal("submit exceeded capacity")
	}
	releaseQuery, ok := m.reserve(mediaQueryLane)
	if !ok {
		t.Fatal("submit exhausted query budget")
	}
	releaseDelivery, ok := m.reserve(mediaDeliveryLane)
	if !ok {
		t.Fatal("gateway lanes exhausted delivery budget")
	}
	if _, ok = m.reserve(mediaDeliveryLane); ok {
		t.Fatal("delivery exceeded capacity")
	}
	releaseReview, ok := m.reserve(mediaReviewLane)
	if !ok {
		t.Fatal("delivery exhausted review budget")
	}
	if _, ok = m.reserve(mediaReviewLane); ok {
		t.Fatal("review exceeded capacity")
	}
	if got := m.snapshot(); got.SubmitInFlight != 1 || got.QueryInFlight != 1 || got.SubmitLimit != 1 || got.QueryLimit != 2 || got.DeliveryInFlight != 1 || got.DeliveryLimit != 1 {
		t.Fatal("wrong reserved-budget snapshot", got)
	}
	releaseSubmit()
	releaseQuery()
	releaseDelivery()
	releaseReview()
	if got := m.snapshot(); got.SubmitInFlight+got.QueryInFlight+got.DeliveryInFlight+got.ReviewInFlight != 0 {
		t.Fatal("budget leaked", got)
	}
}

func waitMediaLaneSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("media lane did not make independent progress")
	}
}

func TestMediaExecutionMemoryIsolationCancelAndNoHelperBypass(t *testing.T) {
	queryStarted, queryRelease, queryDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	submitStarted, submitRelease, submitDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stop := make(chan struct{})
	provider := &leaseMedia{query: func(context.Context, MediaHandle) (MediaResult, error) {
		close(queryStarted)
		select {
		case <-queryRelease:
		case <-stop:
		}
		return MediaResult{Status: "SUCCEEDED"}, nil
	}, submit: func(_ context.Context, input TaskInput) (MediaHandle, error) {
		if input.Inputs.Prompt == "blocked submit" {
			close(submitStarted)
			select {
			case <-submitRelease:
			case <-stop:
			}
		}
		return MediaHandle{ID: newID("handle"), Kind: "image"}, nil
	}}
	a, _ := testApp(t, Config{PollInterval: time.Hour, mediaProvider: provider, MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 1}})
	t.Cleanup(func() { close(stop) })
	queryTask := leaseTask(t, a)
	a.executeTaskStep(queryTask.ID)
	advanceMediaScheduleDue(t, a, queryTask.ID)
	go func() { defer close(queryDone); a.executeTaskStep(queryTask.ID) }()
	waitMediaLaneSignal(t, queryStarted)
	input := taskInput("image")
	input.Inputs.Prompt = "blocked submit"
	submitTask, _, err := a.createTask(input, "memory-lane-submit")
	if err != nil {
		t.Fatal(err)
	}
	go func() { defer close(submitDone); a.executeTaskStep(submitTask.ID) }()
	waitMediaLaneSignal(t, submitStarted)
	if got := a.mediaExecution.snapshot(); got.SubmitInFlight != 1 || got.QueryInFlight != 1 {
		t.Fatal("lanes not independently occupied", got)
	}
	queued := leaseTask(t, a)
	a.executeTaskStep(queued.ID)
	if provider.submits.Load() != 2 {
		t.Fatal("explicit step bypassed full submit lane")
	}
	if _, err = a.cancelTask(queryTask.ID); err != nil {
		t.Fatal(err)
	}
	if got := a.mediaExecution.snapshot(); got.QueryInFlight != 1 {
		t.Fatal("cancel released still-running query")
	}
	close(queryRelease)
	waitMediaLaneSignal(t, queryDone)
	close(submitRelease)
	waitMediaLaneSignal(t, submitDone)
	a.mu.Lock()
	got := a.tasks[queryTask.ID].Task
	a.mu.Unlock()
	if got.Status != "CANCELLED" || len(got.Artifacts) != 0 {
		t.Fatal("late query revived cancellation")
	}
	if snapshot := a.mediaExecution.snapshot(); snapshot.SubmitInFlight+snapshot.QueryInFlight != 0 {
		t.Fatal("step budget leaked", snapshot)
	}
	a.executeTaskStep(queued.ID)
	if provider.submits.Load() != 3 {
		t.Fatal("queued task did not reuse released submit budget")
	}
}

func TestMediaExecutionMemoryReconcileFencesLateQuery(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stop := make(chan struct{})
	provider := &leaseMedia{}
	provider.query = func(context.Context, MediaHandle) (MediaResult, error) {
		if provider.queries.Load() == 1 {
			close(started)
			select {
			case <-release:
			case <-stop:
			}
		}
		return MediaResult{Status: "SUCCEEDED", Progress: 100}, nil
	}
	a, _ := testApp(t, Config{PollInterval: time.Hour, mediaProvider: provider, MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 2}})
	t.Cleanup(func() { close(stop) })
	task := leaseTask(t, a)
	a.executeTaskStep(task.ID)
	advanceMediaScheduleDue(t, a, task.ID)
	a.mu.Lock()
	a.tasks[task.ID].Task.Status = "PROVIDER_STATE_UNKNOWN"
	a.mu.Unlock()
	go func() { defer close(done); a.executeTaskStep(task.ID) }()
	waitMediaLaneSignal(t, started)
	next, err := a.reconcileTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.executeTaskStep(task.ID)
	if provider.queries.Load() != 1 {
		t.Fatal("reconcile cleared another active attempt")
	}
	close(release)
	waitMediaLaneSignal(t, done)
	a.mu.Lock()
	got := a.tasks[task.ID].Task
	a.mu.Unlock()
	if got.Version != next.Version || got.Status != "QUEUED" {
		t.Fatal("late query overwrote reconciled version", got.Status, got.Version, next.Version)
	}
	a.executeTaskStep(task.ID)
	if provider.queries.Load() != 2 {
		t.Fatal("new original-handle query did not execute")
	}
}

func TestMediaExecutionMemoryUnknownSubmitNeverReposts(t *testing.T) {
	provider := &leaseMedia{}
	a, _ := testApp(t, Config{PollInterval: time.Hour, mediaProvider: provider})
	task := leaseTask(t, a)
	a.mu.Lock()
	a.tasks[task.ID].Task.Status = "SUBMITTING"
	a.mu.Unlock()
	a.executeTaskStep(task.ID)
	a.executeTaskStep(task.ID)
	a.mu.Lock()
	got := a.tasks[task.ID].Task
	a.mu.Unlock()
	if got.Status != "SUBMIT_UNKNOWN" || provider.submits.Load() != 0 {
		t.Fatal("unknown submit was repeated")
	}
}

func TestPostgresMediaExecutionLaneFiltersBudgetsAndConnections(t *testing.T) {
	provider := &leaseMedia{}
	a, _ := testApp(t, Config{DatabaseURL: testDatabase(t), PollInterval: time.Hour, mediaProvider: provider, MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 1}})
	accepted := leaseTask(t, a)
	if !a.executePersistentTaskStep(accepted.ID) {
		t.Fatal("initial submit missing")
	}
	advanceMediaScheduleDue(t, a, accepted.ID)
	waiting := leaseTask(t, a)
	admin, err := pgx.Connect(context.Background(), a.config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	for _, check := range []struct {
		lane mediaExecutionLane
		id   string
	}{{mediaQueryLane, waiting.ID}, {mediaSubmitLane, accepted.ID}} {
		if record, err := a.taskLeases.claim(context.Background(), admin, a.config.Mode, check.id, check.lane); err != nil || record != nil {
			t.Fatal("wrong lane claimed frozen handle", err)
		}
	}
	release, _ := a.mediaExecution.reserve(mediaSubmitLane)
	if a.executePersistentTaskStep(waiting.ID) {
		release()
		t.Fatal("full lane claimed new work")
	}
	release()
	var owner string
	if err = admin.QueryRow(context.Background(), "SELECT lease_owner FROM studio_tasks WHERE id=$1", waiting.ID).Scan(&owner); err != nil || owner != "" {
		t.Fatal("budget rejection changed durable lease", owner, err)
	}
	conn, err := a.taskLeases.acquireLane(context.Background(), mediaSubmitLane)
	if err != nil {
		t.Fatal(err)
	}
	defer a.taskLeases.release(conn)
	done := make(chan struct{})
	go func() { defer close(done); a.executePersistentTaskStep(accepted.ID) }()
	// This also checks that the compatibility step first probes a submit lane
	// without waiting for its unrelated exhausted connection pool.
	waitMediaLaneSignal(t, done)
	if provider.submits.Load() != 1 || provider.queries.Load() != 1 {
		t.Fatal("query did not progress while submit connections held")
	}
}
