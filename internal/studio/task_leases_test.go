package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestTaskLeaseConfiguration(t *testing.T) {
	for _, d := range []time.Duration{-time.Second, time.Second, 301 * time.Second} {
		if app, err := newIsolatedApp(t, Config{TaskLeaseDuration: d}); err == nil {
			app.Close()
			t.Fatalf("invalid duration %s accepted", d)
		}
	}
	app, err := newIsolatedApp(t, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.config.TaskLeaseDuration != 30*time.Second || app.taskLeases != nil {
		t.Fatal("default/memory lease behavior changed")
	}
}

// Hold the App's worker connections so tests can inject an exact lease race;
// competing claims below use real independent PostgreSQL sessions.
func leaseFixture(t *testing.T, provider MediaProvider) (*App, *pgx.Conn, string) {
	t.Helper()
	dsn := testDatabase(t)
	app, err := newIsolatedApp(t, Config{DatabaseURL: dsn, PollInterval: time.Hour, TaskWorkers: 2, mediaProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	held := make([]*pgx.Conn, 0, 2)
	for range 2 {
		c, e := app.taskLeases.acquire(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, c)
	}
	t.Cleanup(func() {
		for _, c := range held {
			app.taskLeases.release(c)
		}
	})
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return app, conn, dsn
}
func leaseTask(t *testing.T, a *App) Task {
	t.Helper()
	task, _, err := a.createTask(taskInput("image"), newID("lease_key"))
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func claimTask(t *testing.T, a *App, conn *pgx.Conn, id string) *taskRecord {
	t.Helper()
	r, err := a.taskLeases.claim(context.Background(), conn, a.config.Mode, id)
	if err != nil || r == nil {
		t.Fatalf("claim missing: %v", err)
	}
	return r
}
func expireTaskLease(t *testing.T, conn *pgx.Conn, id string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), "UPDATE studio_tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
}
func readLeasedTask(t *testing.T, conn *pgx.Conn, id string) Task {
	t.Helper()
	var data []byte
	if err := conn.QueryRow(context.Background(), "SELECT document FROM studio_tasks WHERE id=$1", id).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestPostgresTaskLeaseIndependentCompetitionAndFence(t *testing.T) {
	app, admin, dsn := leaseFixture(t, nil)
	task := leaseTask(t, app)
	const competitors = 8
	conns := make([]*pgx.Conn, competitors)
	for i := range conns {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c
		defer c.Close(context.Background())
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	records := make(chan *taskRecord, competitors)
	errs := make(chan error, competitors)
	for _, conn := range conns {
		wg.Add(1)
		go func(c *pgx.Conn) {
			defer wg.Done()
			<-start
			r, e := app.taskLeases.claim(context.Background(), c, "demo", task.ID)
			if e != nil {
				errs <- e
			}
			if r != nil {
				records <- r
			}
		}(conn)
	}
	close(start)
	wg.Wait()
	close(records)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("one due task had %d concurrent owners", len(records))
	}
	first := <-records
	if first.Lease.Epoch < 1 || first.Lease.Until.Before(time.Now()) {
		t.Fatal("claim did not establish DB deadline")
	}
	// Valid renewal uses database time. Expired ownership can never be revived.
	if err := app.taskLeases.renew(context.Background(), admin, *first.Lease); err != nil {
		t.Fatal(err)
	}
	expireTaskLease(t, admin, task.ID)
	if err := app.taskLeases.renew(context.Background(), admin, *first.Lease); !isLeaseLost(err) {
		t.Fatalf("expired owner renewed: %v", err)
	}
	second := claimTask(t, app, admin, task.ID)
	if second.Lease.Epoch <= first.Lease.Epoch || second.Lease.Owner == first.Lease.Owner {
		t.Fatal("takeover did not monotonically fence owner")
	}
	first.Task.Status = "SUCCEEDED"
	first.Task.Version++
	first.Task.Artifacts = []Artifact{{ID: "stale-artifact", Kind: "image", URL: "/demo-assets/image-1.svg"}}
	first.LeaseComplete = true
	err := app.store.saveTask(first)
	if !isLeaseLost(err) || app.store.failed.Load() || app.ctx.Err() != nil {
		t.Fatalf("stale write did not fail normally: %v", err)
	}
	got := readLeasedTask(t, admin, task.ID)
	if got.Version != task.Version || got.Status != task.Status {
		t.Fatal("stale worker changed resource")
	}
	var assets, events int
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1 AND resource_version=$2", task.ID, first.Task.Version).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if assets != 0 || events != 0 {
		t.Fatal("rejected result leaked catalog/outbox side effects")
	}
	// A business version mismatch also rejects an otherwise current lease.
	second.Lease.Version++
	second.Task.Version++
	if err = app.store.saveTask(second); !isLeaseLost(err) {
		t.Fatal("version fence missing", err)
	}
}

func TestPostgresTaskLeaseControlRevokeAndGeneration(t *testing.T) {
	app, admin, _ := leaseFixture(t, nil)
	cancelled := leaseTask(t, app)
	old := claimTask(t, app, admin, cancelled.ID)
	if _, err := app.cancelTask(cancelled.ID); err != nil {
		t.Fatal(err)
	}
	old.Task.Status = "FAILED"
	old.Task.Version++
	if err := app.store.saveTask(old); !isLeaseLost(err) {
		t.Fatal("cancel did not fence old worker", err)
	}
	if got := readLeasedTask(t, admin, cancelled.ID); got.Status != "CANCELLED" {
		t.Fatal("cancel overwritten")
	}
	task := leaseTask(t, app)
	app.mu.Lock()
	r := app.tasks[task.ID]
	r.Handle = MediaHandle{ID: "known-original", Kind: "image"}
	r.Task.Status = "PROVIDER_STATE_UNKNOWN"
	r.Task.Version++
	err := app.store.saveTask(r)
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	old = claimTask(t, app, admin, task.ID)
	if _, err = app.reconcileTask(task.ID); err != nil {
		t.Fatal(err)
	}
	old.Task.Status = "FAILED"
	old.Task.Version++
	if err = app.store.saveTask(old); !isLeaseLost(err) {
		t.Fatal("reconcile did not revoke old claim", err)
	}
	next := claimTask(t, app, admin, task.ID)
	if next.Handle.ID != "known-original" || next.Lease.Epoch <= old.Lease.Epoch {
		t.Fatal("reconcile lost original handle/epoch")
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_task_executor SET generation=$1 WHERE singleton", newID("new_generation")); err != nil {
		t.Fatal(err)
	}
	if err = app.taskLeases.renew(context.Background(), admin, *next.Lease); !isLeaseLost(err) {
		t.Fatal("old generation renewed", err)
	}
	next.Task.Status = "FAILED"
	next.Task.Version++
	if err = app.store.saveTask(next); !isLeaseLost(err) {
		t.Fatal("old generation completed", err)
	}
	expireTaskLease(t, admin, task.ID)
	if got, err := app.taskLeases.claim(context.Background(), admin, "demo", task.ID); err != nil || got != nil {
		t.Fatal("old generation claimed again", err)
	}
}

type leaseMedia struct {
	submits atomic.Int32
	queries atomic.Int32
	query   func(context.Context, MediaHandle) (MediaResult, error)
	submit  func(context.Context, TaskInput) (MediaHandle, error)
}

func (p *leaseMedia) Submit(ctx context.Context, in TaskInput) (MediaHandle, error) {
	p.submits.Add(1)
	if p.submit != nil {
		return p.submit(ctx, in)
	}
	return MediaHandle{ID: "original-lease-handle", Kind: "image"}, nil
}
func (p *leaseMedia) Query(ctx context.Context, h MediaHandle) (MediaResult, error) {
	p.queries.Add(1)
	if p.query != nil {
		return p.query(ctx, h)
	}
	return MediaResult{Status: "SUCCEEDED", Progress: 100}, nil
}
func (p *leaseMedia) Cancel(context.Context, MediaHandle) error { return nil }

func TestPostgresTaskLeaseExpiredSubmittingNeverResubmits(t *testing.T) {
	provider := &leaseMedia{}
	app, admin, _ := leaseFixture(t, provider)
	task := leaseTask(t, app)
	first := claimTask(t, app, admin, task.ID)
	first.Task.Status = "SUBMITTING"
	first.Task.Version++
	app.mu.Lock()
	err := app.persistTaskClaim(first, false)
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	expireTaskLease(t, admin, task.ID)
	reclaimed := claimTask(t, app, admin, task.ID)
	app.executeTaskClaim(admin, reclaimed)
	if provider.submits.Load() != 0 || provider.queries.Load() != 0 {
		t.Fatal("ambiguous submission reached upstream again")
	}
	got := readLeasedTask(t, admin, task.ID)
	if got.Status != "SUBMIT_UNKNOWN" || got.CanReconcile {
		t.Fatal("expired no-handle submission was not parked")
	}
	if another, err := app.taskLeases.claim(context.Background(), admin, "demo", task.ID); err != nil || another != nil {
		t.Fatal("parked task remained claimable", err)
	}
}

func TestPostgresTaskLeaseHeartbeatLossDiscardsLateProvider(t *testing.T) {
	entered, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	provider := &leaseMedia{query: func(ctx context.Context, h MediaHandle) (MediaResult, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return MediaResult{Status: "SUCCEEDED", Progress: 100, Artifacts: []Artifact{{ID: "late-asset", Kind: "image", URL: "/demo-assets/image-1.svg"}}}, nil
	}}
	app, admin, dsn := leaseFixture(t, provider)
	task := leaseTask(t, app)
	app.mu.Lock()
	r := app.tasks[task.ID]
	r.Handle = MediaHandle{ID: "accepted", Kind: "image"}
	err := app.store.saveTask(r)
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close(context.Background())
	app.taskLeases.duration = 300 * time.Millisecond // Internal injection; public config remains >=5s.
	claimed := claimTask(t, app, worker, task.ID)
	done := make(chan struct{})
	go func() { defer close(done); app.executeTaskClaim(worker, claimed) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not entered")
	}
	expireTaskLease(t, admin, task.ID)
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("lost renewal did not cancel provider context")
	}
	successor := claimTask(t, app, admin, task.ID)
	close(release)
	<-done
	if got := readLeasedTask(t, admin, task.ID); got.Status == "SUCCEEDED" {
		t.Fatal("late provider result crossed fencing boundary")
	}
	if successor.Lease.Epoch <= claimed.Lease.Epoch || app.store.failed.Load() || app.ctx.Err() != nil {
		t.Fatal("lease loss either poisoned service or failed takeover")
	}
	app.mu.Lock()
	inflight := app.tasks[task.ID].InFlight
	app.mu.Unlock()
	if inflight {
		t.Fatal("stale in-memory active marker leaked")
	}
}

func TestPostgresTaskLeaseActualWorkersQueryUncachedRecord(t *testing.T) {
	provider := &leaseMedia{}
	app, server := testApp(t, Config{DatabaseURL: testDatabase(t), mediaProvider: provider, PollInterval: 10 * time.Millisecond, TaskWorkers: 4})
	task, _, err := app.createTask(taskInput("image"), "actual-persistent-worker")
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	delete(app.tasks, task.ID)
	app.mu.Unlock()
	got := waitTask(t, server, task.ID)
	if got.Status != "SUCCEEDED" || provider.submits.Load() != 1 || provider.queries.Load() != 1 {
		t.Fatalf("durable workers did not execute uncached row once: %s submits=%d query=%d", got.Status, provider.submits.Load(), provider.queries.Load())
	}
	if len(app.taskQueue) != 0 {
		t.Fatal("PostgreSQL work depended on memory scheduler queue")
	}
}

func TestPostgresTaskLeaseResultBackpressureKeepsAcceptedHandle(t *testing.T) {
	var admin *pgx.Conn
	submitted := make(chan struct{})
	provider := &leaseMedia{submit: func(context.Context, TaskInput) (MediaHandle, error) {
		_, err := admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=$1 WHERE singleton", outboxPendingLimit)
		close(submitted)
		return MediaHandle{ID: "accepted-before-backpressure", Kind: "image"}, err
	}}
	app, conn, _ := leaseFixture(t, provider)
	admin = conn
	task := leaseTask(t, app)
	claim := claimTask(t, app, admin, task.ID)
	// Only this test's worker may use this session while provider/save run.
	worker, err := pgx.Connect(context.Background(), app.config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); app.executeTaskClaim(worker, claim) }()
	select {
	case <-submitted:
	case <-time.After(3 * time.Second):
		t.Fatal("submission not started")
	}
	time.Sleep(100 * time.Millisecond)
	// Keep the injected count consistent with the real pending rows when clear.
	if _, err = admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("accepted result was not committed after backlog cleared")
	}
	var raw []byte
	if err = admin.QueryRow(context.Background(), "SELECT handle FROM studio_tasks WHERE id=$1", task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var handle MediaHandle
	if err = json.Unmarshal(raw, &handle); err != nil {
		t.Fatal(err)
	}
	if handle.ID != "accepted-before-backpressure" || provider.submits.Load() != 1 || provider.queries.Load() != 0 {
		t.Fatal("backpressure lost original accepted result or repeated network work")
	}
	if app.store.failed.Load() {
		t.Fatal("bounded backlog became database failure")
	}
}

func TestTaskLeaseProviderErrorClassification(t *testing.T) {
	cases := []struct {
		err        error
		handle     string
		mode, want string
	}{
		{&MediaProviderError{SubmissionUnknown: true}, "", "live", "SUBMIT_UNKNOWN"},
		{&MediaProviderError{Retryable: true}, "known", "live", "PROVIDER_STATE_UNKNOWN"},
		{&MediaProviderError{ReconcileRequired: true}, "known", "live", "RECONCILE_REQUIRED"},
		{errors.New("untyped network failure"), "", "live", "SUBMIT_UNKNOWN"},
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := &taskRecord{}
			applyLeasedMediaResult(r, MediaHandle{ID: c.handle}, MediaResult{}, c.err, c.mode)
			if r.Task.Status != c.want {
				t.Fatalf("got %s want %s", r.Task.Status, c.want)
			}
		})
	}
}

func TestPostgresTaskLeaseHeartbeatKeepsLongQueryExclusive(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	provider := &leaseMedia{query: func(ctx context.Context, _ MediaHandle) (MediaResult, error) {
		close(entered)
		select {
		case <-release:
			return MediaResult{Status: "SUCCEEDED", Progress: 100}, nil
		case <-ctx.Done():
			return MediaResult{}, ctx.Err()
		}
	}}
	app, admin, dsn := leaseFixture(t, provider)
	task := leaseTask(t, app)
	app.mu.Lock()
	r := app.tasks[task.ID]
	r.Handle = MediaHandle{ID: "known", Kind: "image"}
	err := app.store.saveTask(r)
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close(context.Background())
	app.taskLeases.duration = 300 * time.Millisecond
	claimed := claimTask(t, app, worker, task.ID)
	done := make(chan struct{})
	go func() { defer close(done); app.executeTaskClaim(worker, claimed) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("query did not start")
	}
	time.Sleep(800 * time.Millisecond)
	if stolen, err := app.taskLeases.claim(context.Background(), admin, "demo", task.ID); err != nil || stolen != nil {
		t.Fatal("renewed long query lost exclusivity", err)
	}
	close(release)
	<-done
	if got := readLeasedTask(t, admin, task.ID); got.Status != "SUCCEEDED" || provider.queries.Load() != 1 {
		t.Fatal("long query failed to commit once")
	}
}

type leaseChat func(context.Context, string, func(string) error) error

func (f leaseChat) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	return f(ctx, prompt, emit)
}

func TestPostgresTaskLeaseRunBackpressureDoesNotRestartStream(t *testing.T) {
	app, admin, _ := leaseFixture(t, nil)
	var calls atomic.Int32
	completed := make(chan struct{})
	app.chat = leaseChat(func(ctx context.Context, _ string, emit func(string) error) error {
		calls.Add(1)
		if err := emit("retained result"); err != nil {
			return err
		}
		_, err := admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=$1 WHERE singleton", outboxPendingLimit)
		close(completed)
		return err
	})
	run := outboxFixtureRun(newID("run"))
	if err := app.store.createRun(run, newID("run_key"), payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.runs[run.ID] = &runRecord{Run: run, Subscribers: make(map[chan event]bool)}
	app.mu.Unlock()
	if _, err := admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=$1 WHERE singleton", outboxPendingLimit); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); app.executeRun(run.ID) }()
	time.Sleep(100 * time.Millisecond)
	app.mu.Lock()
	status := app.runs[run.ID].Run.Status
	app.mu.Unlock()
	if calls.Load() != 0 || status != "QUEUED" {
		t.Fatal("rejected start mutated cache or invoked model")
	}
	clearBacklog := func() {
		t.Helper()
		if _, err := admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton"); err != nil {
			t.Fatal(err)
		}
	}
	clearBacklog()
	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never began after backlog clear")
	}
	time.Sleep(100 * time.Millisecond)
	clearBacklog()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal transaction did not retry")
	}
	app.mu.Lock()
	got := app.runs[run.ID].Run
	app.mu.Unlock()
	if got.Status != "COMPLETED" || got.Output != "retained result" || calls.Load() != 1 {
		t.Fatalf("result lost/restarted: %s calls=%d", got.Status, calls.Load())
	}
}

func TestPostgresTaskLeaseGenerationGateSerializesWithResultCommit(t *testing.T) {
	app, admin, dsn := leaseFixture(t, nil)
	task := leaseTask(t, app)
	claim := claimTask(t, app, admin, task.ID)
	claim.Task.Status = "RUNNING"
	claim.Task.Version++
	tx, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = writeTaskWithLeaseSQL(context.Background(), tx, claim); err != nil {
		t.Fatal(err)
	}
	other, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(entered)
		_, e := other.Exec(ctx, "UPDATE studio_task_executor SET generation=$1 WHERE singleton", newID("replacement"))
		done <- e
	}()
	<-entered
	select {
	case e := <-done:
		t.Fatalf("generation crossed uncommitted result: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if got := readLeasedTask(t, admin, task.ID); got.Status != "RUNNING" || got.Version != task.Version+1 {
		t.Fatal("ordered transaction did not persist")
	}
	claim.Lease.Version = claim.Task.Version
	claim.LeaseStatus = claim.Task.Status
	claim.Task.Version++
	if err = app.store.saveTask(claim); !isLeaseLost(err) {
		t.Fatal("old generation wrote again after activation", err)
	}
}

func TestPostgresTaskLeaseClaimThenControlChangeCannotReplaceCache(t *testing.T) {
	for _, action := range []string{"cancel", "reconcile"} {
		t.Run(action, func(t *testing.T) {
			provider := &leaseMedia{}
			app, admin, _ := leaseFixture(t, provider)
			task := leaseTask(t, app)
			app.mu.Lock()
			record := app.tasks[task.ID]
			record.Handle = MediaHandle{ID: "known-before-control", Kind: "image"}
			record.Task.Status = "PROVIDER_STATE_UNKNOWN"
			record.Task.Version++
			err := app.store.saveTask(record)
			app.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			claimed := claimTask(t, app, admin, task.ID)
			var newer Task
			if action == "cancel" {
				newer, err = app.cancelTask(task.ID)
			} else {
				newer, err = app.reconcileTask(task.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// This is the exact interleaving: the durable claim is older than the
			// committed control action, but has not entered provider work yet.
			app.executeTaskClaim(admin, claimed)
			app.mu.Lock()
			cached := copyTask(app.tasks[task.ID].Task)
			app.mu.Unlock()
			durable := readLeasedTask(t, admin, task.ID)
			if cached.Status != newer.Status || cached.Version != newer.Version || durable.Status != newer.Status || durable.Version != newer.Version {
				t.Fatalf("old claim replaced committed control snapshot: cache %s/%d database %s/%d expected %s/%d", cached.Status, cached.Version, durable.Status, durable.Version, newer.Status, newer.Version)
			}
			if provider.submits.Load() != 0 || provider.queries.Load() != 0 || app.store.failed.Load() || app.ctx.Err() != nil {
				t.Fatal("revoked claim reached provider or poisoned service")
			}
		})
	}
}

func TestPostgresTaskLeaseStepDeadlineDoesNotPoisonDatabase(t *testing.T) {
	app, admin, _ := leaseFixture(t, nil)
	task := leaseTask(t, app)
	claimed := claimTask(t, app, admin, task.ID)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if app.renewTaskClaimForStep(ctx, admin, *claimed.Lease) {
		t.Fatal("expired step renewed")
	}
	if app.store.failed.Load() || app.ctx.Err() != nil {
		t.Fatal("step deadline incorrectly became global database failure")
	}
	// The PG connection and unexpired lease remain usable by a live context.
	if !app.renewTaskClaimForStep(context.Background(), admin, *claimed.Lease) {
		t.Fatal("step deadline damaged otherwise healthy lease connection")
	}
}
