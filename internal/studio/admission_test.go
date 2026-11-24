package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func assertAdmissionError(t *testing.T, err error, code string) {
	t.Helper()
	var operation *operationError
	if !errors.As(err, &operation) || operation.status != 429 || operation.code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestAdmissionModelWorkspaceIdempotencyAndCancellation(t *testing.T) {
	a, s := testApp(t, Config{PollInterval: time.Hour, Admission: AdmissionLimits{WorkspaceTasks: 2, ModelTasks: 1}})
	code, body, _ := request(t, s, "POST", "/api/v1/tasks", "image-first-key", taskInput("image"))
	if code != 202 {
		t.Fatalf("first %d %s", code, body)
	}
	first := decode[Task](t, body)
	code, _, header := request(t, s, "POST", "/api/v1/tasks", "image-first-key", taskInput("image"))
	if code != 202 || header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("full model quota blocked replay")
	}
	changed := taskInput("image")
	changed.Inputs.Prompt = "different"
	code, _, _ = request(t, s, "POST", "/api/v1/tasks", "image-first-key", changed)
	if code != 409 {
		t.Fatal("conflict must precede quota")
	}
	code, body, header = request(t, s, "POST", "/api/v1/tasks", "image-second-key", taskInput("image"))
	if code != 429 || !strings.Contains(string(body), "ADMISSION_MODEL_LIMIT") {
		t.Fatalf("model limit %d %s", code, body)
	}
	code, body, _ = request(t, s, "POST", "/api/v1/tasks", "video-first-key", taskInput("video"))
	if code != 202 {
		t.Fatalf("other model blocked %d %s", code, body)
	}
	code, body, _ = request(t, s, "POST", "/api/v1/tasks", "image-second-key", taskInput("image"))
	if code != 429 || !strings.Contains(string(body), "ADMISSION_WORKSPACE_LIMIT") {
		t.Fatalf("workspace limit %d %s", code, body)
	}
	_, _, _ = request(t, s, "POST", "/api/v1/tasks/"+first.ID+"/cancel", "", nil)
	code, body, _ = request(t, s, "POST", "/api/v1/tasks", "image-second-key", taskInput("image"))
	if code != 202 {
		t.Fatalf("rejected key or cancelled slot leaked %d %s", code, body)
	}
	snapshot, err := a.admissionSnapshot("ws_demo")
	if err != nil || snapshot.Tasks.Active != 2 || snapshot.Tasks.Queued != 2 || snapshot.Tasks.OldestQueuedSeconds < 0 {
		t.Fatalf("snapshot %#v %v", snapshot, err)
	}
	if len(a.idem) != 3 {
		t.Fatalf("quota/conflict rejection wrote idempotency: %d", len(a.idem))
	}
}

func TestAdmissionConcurrentRunsAndCrossProjectScope(t *testing.T) {
	a, _ := testApp(t, Config{DemoTokenDelay: time.Hour, Admission: AdmissionLimits{WorkspaceRuns: 1, ModelRuns: 1}})
	var wg sync.WaitGroup
	accepted := make(chan Run, 12)
	rejected := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, _, err := a.createRun(runInput(), fmt.Sprintf("concurrent-key-%d", i))
			if err == nil {
				accepted <- run
			} else {
				rejected <- err
			}
		}()
	}
	wg.Wait()
	close(accepted)
	close(rejected)
	if len(accepted) != 1 || len(rejected) != 11 {
		t.Fatalf("accepted=%d rejected=%d", len(accepted), len(rejected))
	}
	for err := range rejected {
		assertAdmissionError(t, err, "ADMISSION_WORKSPACE_LIMIT")
	}
	first := <-accepted
	a.mu.Lock()
	a.projects["same-workspace-project"] = Project{ID: "same-workspace-project", WorkspaceID: "ws_demo"}
	a.projects["other-workspace-project"] = Project{ID: "other-workspace-project", WorkspaceID: "other-workspace"}
	a.mu.Unlock()
	input := runInput()
	input.ProjectID = "same-workspace-project"
	_, _, err := a.createRun(input, "same-workspace-run")
	assertAdmissionError(t, err, "ADMISSION_WORKSPACE_LIMIT")
	input.ProjectID = "other-workspace-project"
	if _, _, err = a.createRun(input, "other-workspace-run"); err != nil {
		t.Fatal("another workspace cannot use its slot", err)
	}
	if _, err = a.cancelRun(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.createRun(runInput(), "after-cancel-run"); err != nil {
		t.Fatal("run slot not released", err)
	}
	snapshot, err := a.admissionSnapshot("ws_demo")
	if err != nil || snapshot.Runs.Active != 1 {
		t.Fatalf("scope leaked %#v %v", snapshot, err)
	}
}

type admissionFailureMedia struct{ demoMedia }

func (p admissionFailureMedia) Submit(context.Context, TaskInput) (MediaHandle, error) {
	return MediaHandle{}, errors.New("fixture provider failure")
}

func TestAdmissionFailureCompletionAndParkedReconcile(t *testing.T) {
	t.Run("provider failure releases", func(t *testing.T) {
		a, s := testApp(t, Config{mediaProvider: admissionFailureMedia{}, Admission: AdmissionLimits{WorkspaceTasks: 1, ModelTasks: 1}})
		_, body, _ := request(t, s, "POST", "/api/v1/tasks", "failed-first-key", taskInput("image"))
		if got := waitTask(t, s, decode[Task](t, body).ID); got.Status != "FAILED" {
			t.Fatal(got.Status)
		}
		if snapshot, err := a.admissionSnapshot("ws_demo"); err != nil || snapshot.Tasks.Active != 0 {
			t.Fatalf("failed slot retained %#v %v", snapshot, err)
		}
		if _, _, err := a.createTask(taskInput("image"), "failed-second-key"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("parked reconcile consumes one slot", func(t *testing.T) {
		p := &recoveryMedia{}
		p.park.Store(true)
		a, s := testApp(t, Config{mediaProvider: p, Admission: AdmissionLimits{WorkspaceTasks: 1, ModelTasks: 1}})
		_, body, _ := request(t, s, "POST", "/api/v1/tasks", "parked-first-key", taskInput("image"))
		task := decode[Task](t, body)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			a.mu.Lock()
			status := a.tasks[task.ID].Task.Status
			a.mu.Unlock()
			if status == "RECONCILE_REQUIRED" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		_, _, err := a.createTask(taskInput("image"), "parked-second-key")
		assertAdmissionError(t, err, "ADMISSION_WORKSPACE_LIMIT")
		p.park.Store(false)
		p.complete.Store(true)
		if _, err = a.reconcileTask(task.ID); err != nil {
			t.Fatal(err)
		}
		if got := waitTask(t, s, task.ID); got.Status != "SUCCEEDED" {
			t.Fatal(got.Status)
		}
		if p.submits.Load() != 1 {
			t.Fatal("reconcile resubmitted")
		}
		if _, _, err = a.createTask(taskInput("image"), "parked-second-key"); err != nil {
			t.Fatal("completed slot not released", err)
		}
	})
}

func TestAdmissionConfigRejectsInvalidBounds(t *testing.T) {
	for _, limits := range []AdmissionLimits{{WorkspaceRuns: -1}, {ModelRuns: 4097}, {WorkspaceTasks: -1}, {ModelTasks: 4097}, {WorkspaceStreams: 129}} {
		if app, err := newIsolatedApp(t, Config{Admission: limits}); err == nil {
			app.Close()
			t.Fatal("invalid limit accepted", limits)
		}
	}
}

func TestPostgresAdmissionParkedEvictionRestartAndRecovery(t *testing.T) {
	db := testDatabase(t)
	cfg := Config{DatabaseURL: db, PollInterval: time.Hour, DemoTokenDelay: time.Hour, MaxRecords: 2, Admission: AdmissionLimits{WorkspaceTasks: 2, ModelTasks: 2, WorkspaceRuns: 1, ModelRuns: 1}}
	a, err := newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks := []Task{}
	for i, status := range []string{"SUBMIT_UNKNOWN", "RECONCILE_REQUIRED"} {
		task, _, err := a.createTask(taskInput("image"), fmt.Sprintf("parked-pg-key-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		record := a.tasks[task.ID]
		record.Task.Status = status
		if status == "RECONCILE_REQUIRED" {
			record.Handle = MediaHandle{ID: "original-provider-id", Kind: "image", Count: 1}
		}
		err = a.store.saveTask(record)
		a.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	a.mu.Lock()
	a.evictTerminalsLocked()
	cached := len(a.tasks)
	a.mu.Unlock()
	if cached >= 2 {
		t.Fatal("fixture failed to evict parked task")
	}
	_, _, err = a.createTask(taskInput("image"), "blocked-before-restart")
	assertAdmissionError(t, err, "ADMISSION_WORKSPACE_LIMIT")
	// Persist a real authorized 022 execution outside the worker cache. Closing
	// this fixture leaves the same open stream a killed process would leave.
	run := outboxFixtureRun(newID("run"))
	run.Output = ""
	if err := a.store.createRun(run, "ws_demo:runs:interrupted-pg-run", payloadHash(runInput())); err != nil {
		t.Fatal(err)
	}
	claimFixtureRun(t, a.store, &run)
	a.Close()
	a, err = newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.tasks) != 0 {
		t.Fatal("parked records unexpectedly loaded; test must cover uncached durable rows")
	}
	snapshot, err := a.admissionSnapshot("ws_demo")
	if err != nil || snapshot.Tasks.Active != 2 || snapshot.Runs.Active != 0 {
		t.Fatalf("restart count %#v %v", snapshot, err)
	}
	if a.runs[run.ID] == nil || a.runs[run.ID].Run.Status != "INTERRUPTED" {
		t.Fatal("persisted running checkpoint was not recovered")
	}
	_, _, err = a.createTask(taskInput("image"), "blocked-after-restart")
	assertAdmissionError(t, err, "ADMISSION_WORKSPACE_LIMIT")
	replayed, replay, err := a.createTask(taskInput("image"), "parked-pg-key-0")
	if err != nil || !replay || replayed.ID != tasks[0].ID {
		t.Fatal("parked full quota blocked persistent replay", err)
	}
	if _, _, err = a.createRun(runInput(), "after-interrupted-pg-run"); err != nil {
		t.Fatal("interrupted run retained slot", run.ID, err)
	}
	if _, err = a.reconcileTask(tasks[1].ID); err != nil {
		t.Fatal("reconcile must not acquire another slot", err)
	}
	snapshot, err = a.admissionSnapshot("ws_demo")
	if err != nil || snapshot.Tasks.Active != 2 {
		t.Fatal("reconcile changed occupancy", snapshot, err)
	}
	if _, err = a.cancelTask(tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.createTask(taskInput("image"), "blocked-after-restart"); err != nil {
		t.Fatal("cancel did not free durable slot", err)
	}
}

func TestPostgresAdmissionTenantIsolationAndAtomicFailure(t *testing.T) {
	db := testDatabase(t)
	a, err := newIsolatedApp(t, Config{DatabaseURL: db, AuthMode: "required", PollInterval: time.Hour, Admission: AdmissionLimits{WorkspaceTasks: 1, ModelTasks: 1}})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(a.Handler())
	defer s.Close()
	first, second := authClient(t), authClient(t)
	u1 := registerFixture(t, first, s.URL, "quota-one@example.test")
	u2 := registerFixture(t, second, s.URL, "quota-two@example.test")
	input := taskInput("image")
	input.ProjectID = u1.Project.ID
	code, body, _ := authRequest(t, first, s.URL, "POST", "/api/v1/tasks", "tenant-one-first", u1.Project.ID, input)
	if code != 202 {
		t.Fatalf("first %d %s", code, body)
	}
	code, _, _ = authRequest(t, first, s.URL, "POST", "/api/v1/tasks", "tenant-one-second", u1.Project.ID, input)
	if code != 429 {
		t.Fatal("workspace quota not applied")
	}
	input.ProjectID = u2.Project.ID
	code, _, _ = authRequest(t, first, s.URL, "POST", "/api/v1/tasks", "tenant-forged-project", u1.Project.ID, input)
	if code != 422 {
		t.Fatal("foreign project accepted")
	}
	code, body, _ = authRequest(t, second, s.URL, "POST", "/api/v1/tasks", "tenant-two-first", u2.Project.ID, input)
	if code != 202 {
		t.Fatalf("second tenant quota leaked %d %s", code, body)
	}
	a.Close()
	// Audit failure must not leave either a resource or an admission reservation.
	admin, err := pgx.Connect(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	_, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_admission_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture admission audit failure'; END $$; CREATE TRIGGER reject_admission_fixture BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_admission_fixture()`)
	if err != nil {
		t.Fatal(err)
	}
	a, err = newIsolatedApp(t, Config{DatabaseURL: db, PollInterval: time.Hour, Admission: AdmissionLimits{WorkspaceTasks: 1, ModelTasks: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.createTask(taskInput("image"), "atomic-failure-task")
	if err == nil {
		t.Fatal("fault injection not triggered")
	}
	a.Close()
	_, err = admin.Exec(context.Background(), "DROP TRIGGER reject_admission_fixture ON studio_audit_events")
	if err != nil {
		t.Fatal(err)
	}
	a, err = newIsolatedApp(t, Config{DatabaseURL: db, PollInterval: time.Hour, Admission: AdmissionLimits{WorkspaceTasks: 1, ModelTasks: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	snapshot, err := a.admissionSnapshot("ws_demo")
	if err != nil || snapshot.Tasks.Active != 0 {
		t.Fatal("failed admission left occupancy", snapshot, err)
	}
	if _, replay, err := a.createTask(taskInput("image"), "atomic-failure-task"); err != nil || replay {
		t.Fatal("rolled back request cannot be admitted", err, replay)
	}
}

func TestPostgresAdmissionTerminalPredicateAndIndexes(t *testing.T) {
	a, err := newIsolatedApp(t, Config{DatabaseURL: testDatabase(t), PollInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	statuses := []string{"QUEUED", "SUBMITTING", "RUNNING", "INGESTING", "SUBMIT_UNKNOWN", "PROVIDER_STATE_UNKNOWN", "RECONCILE_REQUIRED", "SUCCEEDED", "COMPLETED", "FAILED", "CANCELLED", "INTERRUPTED", "PARTIAL"}
	wantActive := 0
	for _, status := range statuses {
		now := time.Now().UTC().Add(-time.Minute)
		record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: a.config.Mode, ModelID: "studio-image", Kind: "image", Status: status, CreatedAt: now, UpdatedAt: now}, Input: taskInput("image"), NextPoll: time.Now().Add(time.Hour)}
		if err := a.store.createTask(record, "terminal-fixture-"+status, payloadHash(status)); err != nil {
			t.Fatal(err)
		}
		if !terminal(status) {
			wantActive++
		}
	}
	snapshot, err := a.admissionSnapshot("ws_demo")
	if err != nil || snapshot.Tasks.Active != wantActive || snapshot.Tasks.Queued != 1 || snapshot.Tasks.OldestQueuedSeconds < 59 {
		t.Fatalf("terminal predicate diverged: %#v %v", snapshot, err)
	}
	for _, probe := range []struct {
		workspace, model, kind     string
		workspaceLimit, modelLimit int
		workspaceFull, modelFull   bool
	}{
		{"ws_demo", "studio-image", "image", wantActive, wantActive, true, true},
		{"ws_demo", "studio-image", "image", wantActive + 1, wantActive, false, true},
		{"ws_demo", "studio-video", "video", wantActive, 1, true, false},
		{"ws_demo", "studio-image", "image", wantActive + 1, wantActive + 1, false, false},
		{"ws_demo", "studio-image", "chat", 1, 1, false, false},
		{"empty_workspace", "studio-image", "image", 1, 1, false, false},
	} {
		a.mu.Lock()
		ws, model, e := a.admissionAtLimitLocked(probe.workspace, probe.model, probe.kind, probe.workspaceLimit, probe.modelLimit)
		a.mu.Unlock()
		if e != nil || ws != probe.workspaceFull || model != probe.modelFull {
			t.Fatalf("admission threshold %+v gave workspace=%v model=%v err=%v", probe, ws, model, e)
		}
	}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var count int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname IN ('studio_runs_admission_active','studio_tasks_admission_active') AND indexdef LIKE '%workspace_id%' AND indexdef LIKE '%model_id%' AND indexdef LIKE '%WHERE%'").Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			return fmt.Errorf("expected two active-only workspace/model indexes, found %d", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
