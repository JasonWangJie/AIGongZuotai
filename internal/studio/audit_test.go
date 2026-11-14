package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMemoryAuditDeduplicatesAndRedactsSensitiveContent(t *testing.T) {
	_, server := testApp(t, Config{DemoTokenDelay: time.Second, DemoTaskDuration: time.Minute})
	input := runInput()
	input.Input = "PRIVATE_PROMPT_password=never-store-cookie-key"
	_, data, _ := request(t, server, "POST", "/api/v1/runs", "sensitive-idempotency-key", input)
	run := decode[Run](t, data)
	_, _, _ = request(t, server, "POST", "/api/v1/runs", "sensitive-idempotency-key", input)
	changed := input
	changed.Input = "different"
	code, _, _ := request(t, server, "POST", "/api/v1/runs", "sensitive-idempotency-key", changed)
	if code != 409 {
		t.Fatal("expected conflict")
	}
	for range 2 {
		code, _, _ = request(t, server, "POST", "/api/v1/runs/"+run.ID+"/cancel", "", nil)
		if code != 200 {
			t.Fatal("cancel failed")
		}
	}
	in := taskInput("image")
	in.Inputs.Prompt = "PRIVATE_MEDIA_PROMPT"
	_, data, _ = request(t, server, "POST", "/api/v1/tasks", "sensitive-media-key", in)
	task := decode[Task](t, data)
	_, _, _ = request(t, server, "POST", "/api/v1/tasks", "sensitive-media-key", in)
	for range 2 {
		_, _, _ = request(t, server, "POST", "/api/v1/tasks/"+task.ID+"/cancel", "", nil)
	}
	_, _, _ = request(t, server, "POST", "/api/v1/projects", "", map[string]string{"name": "PRIVATE_PROJECT_TITLE"})
	code, data, _ = request(t, server, "GET", "/api/v1/audit?scope=workspace", "", nil)
	if code != 200 {
		t.Fatalf("audit: %d %s", code, data)
	}
	page := decode[auditPage](t, data)
	if len(page.Items) != 5 || page.Storage != "memory" {
		t.Fatalf("unexpected events: %+v", page)
	}
	counts := map[string]int{}
	for _, event := range page.Items {
		counts[event.Action]++
		if event.ActorType != "local" || event.ActorID != "" {
			t.Fatal("local actor mislabeled")
		}
	}
	for _, action := range []string{"run.accepted", "run.cancelled", "task.accepted", "task.cancelled", "project.created"} {
		if counts[action] != 1 {
			t.Fatalf("duplicated or missing action %s: %d", action, counts[action])
		}
	}
	for _, secret := range []string{"PRIVATE_PROMPT", "PRIVATE_MEDIA_PROMPT", "PRIVATE_PROJECT_TITLE", "sensitive-idempotency-key", "sensitive-media-key", "password", "cookie"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("sensitive field leaked into audit: %s", secret)
		}
	}
	_, data, _ = request(t, server, "GET", "/api/v1/audit", "", nil)
	if len(decode[auditPage](t, data).Items) != 4 {
		t.Fatal("default audit did not filter current project")
	}
}

func TestMemoryAuditKeysetAndBoundedRetention(t *testing.T) {
	app, server := testApp(t, Config{})
	app.mu.Lock()
	for i := range memoryAuditLimit + 7 {
		event := newAuditEvent("ws_demo", "prj_demo", "", "run.accepted", "run", "run_"+jsonNumber(uint64(i)), AuditDetails{ToStatus: "QUEUED"})
		event.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, i*1000, time.UTC)
		app.appendMemoryAuditLocked(event)
	}
	if len(app.auditEvents) != memoryAuditLimit {
		t.Fatal("memory audit exceeded retention limit")
	}
	app.mu.Unlock()
	_, data, _ := request(t, server, "GET", "/api/v1/audit?limit=2", "", nil)
	first := decode[auditPage](t, data)
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatal("missing first-page cursor")
	}
	app.mu.Lock()
	app.appendMemoryAuditLocked(newAuditEvent("ws_demo", "prj_demo", "", "run.accepted", "run", "newer_run", AuditDetails{}))
	app.mu.Unlock()
	_, data, _ = request(t, server, "GET", "/api/v1/audit?limit=2&cursor="+url.QueryEscape(first.NextCursor), "", nil)
	second := decode[auditPage](t, data)
	for _, next := range second.Items {
		for _, previous := range first.Items {
			if next.ID == previous.ID {
				t.Fatal("keyset page duplicated a row after new insert")
			}
		}
	}
	for _, path := range []string{"/api/v1/audit?scope=workspace&cursor=" + url.QueryEscape(first.NextCursor), "/api/v1/audit?cursor=not-base64", "/api/v1/audit?limit=101", "/api/v1/audit?scope=foreign"} {
		code, _, _ := request(t, server, "GET", path, "", nil)
		if code != 400 {
			t.Fatalf("bad query accepted %s: %d", path, code)
		}
	}
	code, _, _ := request(t, server, "GET", "/api/v1/audit?cursor="+strings.Repeat("a", 1025), "", nil)
	if code != 400 {
		t.Fatal("unbounded cursor accepted")
	}
}

func TestPostgresAuditTiedTimestampCursor(t *testing.T) {
	databaseURL := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: databaseURL})
	tied := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	insert := func(ids ...string) {
		t.Helper()
		err := app.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			for _, id := range ids {
				event := newAuditEvent("ws_demo", "prj_demo", "", "run.accepted", "run", "synthetic_fixture", AuditDetails{})
				event.ID = id
				event.CreatedAt = tied
				if err = insertAuditSQL(ctx, tx, event); err != nil {
					return err
				}
			}
			return tx.Commit(ctx)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("audit_a", "audit_b", "audit_c")
	_, data, _ := request(t, server, "GET", "/api/v1/audit?limit=2", "", nil)
	first := decode[auditPage](t, data)
	if len(first.Items) != 2 || first.Items[0].ID != "audit_c" || first.Items[1].ID != "audit_b" || first.NextCursor == "" {
		t.Fatalf("incorrect timestamp tie ordering: %+v", first)
	}
	insert("audit_d")
	_, data, _ = request(t, server, "GET", "/api/v1/audit?limit=2&cursor="+url.QueryEscape(first.NextCursor), "", nil)
	second := decode[auditPage](t, data)
	if len(second.Items) != 1 || second.Items[0].ID != "audit_a" || second.NextCursor != "" {
		t.Fatalf("tied cursor skipped or repeated events: %+v", second)
	}
	_, data, _ = request(t, server, "GET", "/api/v1/audit", "", nil)
	latest := decode[auditPage](t, data)
	if len(latest.Items) != 4 || latest.Items[0].ID != "audit_d" {
		t.Fatal("new tied event missing on refresh")
	}
}

func TestPostgresAuditTransactionIsolationAndRestart(t *testing.T) {
	databaseURL := testDatabase(t)
	app, err := newIsolatedApp(t, Config{DatabaseURL: databaseURL, AuthMode: "required", DemoTokenDelay: time.Second, PollInterval: time.Second, DemoTaskDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { app.Close(); server.Close() })
	alice, bob := authClient(t), authClient(t)
	a := registerFixture(t, alice, server.URL, "audit-alice@example.test")
	_ = registerFixture(t, bob, server.URL, "audit-bob@example.test")
	in := runInput()
	in.ProjectID = a.Project.ID
	in.Input = "DO_NOT_AUDIT_THIS_PROMPT"
	_, data, _ := authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "audit-persistent-key", "", in)
	run := decode[Run](t, data)
	_, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "audit-persistent-key", "", in)
	for range 2 {
		_, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs/"+run.ID+"/cancel", "", "", nil)
	}
	_, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/audit?scope=workspace", "", "", nil)
	page := decode[auditPage](t, data)
	if len(page.Items) != 3 {
		t.Fatalf("expected default project+accept+cancel, got %d", len(page.Items))
	}
	for _, event := range page.Items {
		if event.WorkspaceID != a.Workspace.ID || event.ActorID != a.User.ID || event.ActorType != "user" {
			t.Fatal("audit identity attribution incorrect")
		}
	}
	if bytes.Contains(data, []byte(in.Input)) || bytes.Contains(data, []byte("audit-alice@example.test")) {
		t.Fatal("audit exposed prompt or email")
	}
	_, foreign, _ := authRequest(t, bob, server.URL, "GET", "/api/v1/audit?scope=workspace", "", "", nil)
	if bytes.Contains(foreign, []byte(a.Workspace.ID)) || bytes.Contains(foreign, []byte(run.ID)) {
		t.Fatal("cross-account audit leak")
	}
	code, _, _ := authRequest(t, bob, server.URL, "GET", "/api/v1/audit", "", a.Project.ID, nil)
	if code != 404 {
		t.Fatal("foreign project audit allowed")
	}
	_, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/audit?limit=1", "", "", nil)
	cursor := decode[auditPage](t, data).NextCursor
	code, _, _ = authRequest(t, bob, server.URL, "GET", "/api/v1/audit?cursor="+url.QueryEscape(cursor), "", "", nil)
	if code != 400 {
		t.Fatal("cross-workspace cursor accepted")
	}
	app.Close()
	server.Close()
	app, err = newIsolatedApp(t, Config{DatabaseURL: databaseURL, AuthMode: "required"})
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	_, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/audit?scope=workspace", "", "", nil)
	if len(decode[auditPage](t, data).Items) != 3 {
		t.Fatal("audit missing or duplicated after restart")
	}
	var versions int
	if err = app.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT count(*) FROM studio_schema_versions WHERE version IN (1,2)").Scan(&versions)
	}); err != nil || versions != 2 {
		t.Fatal("audit migration not recorded")
	}
}

func TestPostgresAuditInsertFailureRollsBackAcceptedRunAndIdempotency(t *testing.T) {
	databaseURL := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: databaseURL})
	admin, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	_, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture audit failure'; END $$; CREATE TRIGGER reject_audit_fixture BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_audit_fixture()`)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := request(t, server, "POST", "/api/v1/runs", "audit-rollback-key", runInput())
	if code != 503 {
		t.Fatalf("audit failure must fail closed, got %d", code)
	}
	var runs, keys, events int
	if err = admin.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM studio_runs),(SELECT count(*) FROM studio_idempotency),(SELECT count(*) FROM studio_audit_events)").Scan(&runs, &keys, &events); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || keys != 0 || events != 0 || len(app.listRuns()) != 0 {
		t.Fatalf("failed audit left partial business state: runs=%d keys=%d events=%d", runs, keys, events)
	}
}

func TestPostgresAuditFailureRollsBackCancellationState(t *testing.T) {
	databaseURL := testDatabase(t)
	_, server := testApp(t, Config{DatabaseURL: databaseURL, DemoTokenDelay: time.Minute})
	_, data, _ := request(t, server, "POST", "/api/v1/runs", "cancel-audit-atomic-key", runInput())
	run := decode[Run](t, data)
	admin, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	_, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_cancel_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='run.cancelled' THEN RAISE EXCEPTION 'fixture cancellation audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cancel_audit_fixture BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_cancel_audit_fixture()`)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := request(t, server, "POST", "/api/v1/runs/"+run.ID+"/cancel", "", nil)
	if code != 503 {
		t.Fatal("cancellation audit failure did not fail closed")
	}
	var status string
	var cancels int
	if err = admin.QueryRow(context.Background(), "SELECT document->>'status' FROM studio_runs WHERE id=$1", run.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_audit_events WHERE action='run.cancelled'").Scan(&cancels); err != nil {
		t.Fatal(err)
	}
	if status == "CANCELLED" || cancels != 0 {
		t.Fatal("business cancellation committed without its audit")
	}
}

func TestPostgresAuditFailureRollsBackProjectAndRegistration(t *testing.T) {
	for _, operation := range []string{"project", "registration"} {
		t.Run(operation, func(t *testing.T) {
			databaseURL := testDatabase(t)
			mode := "disabled"
			if operation == "registration" {
				mode = "required"
			}
			_, server := testApp(t, Config{DatabaseURL: databaseURL, AuthMode: mode})
			admin, err := pgx.Connect(context.Background(), databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(context.Background())
			_, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_project_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture project audit failure'; END $$; CREATE TRIGGER reject_project_audit_fixture BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_project_audit_fixture()`)
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/projects"
			body := any(map[string]string{"name": "must rollback"})
			if operation == "registration" {
				path = "/api/v1/auth/register"
				body = map[string]string{"email": "rollback@example.test", "password": "fixture-password-123", "name": "Rollback fixture"}
			}
			code, _, _ := request(t, server, "POST", path, "", body)
			if code != 503 {
				t.Fatalf("expected audit failure503, got %d", code)
			}
			var projects, users, sessions int
			if err = admin.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM studio_projects),(SELECT count(*) FROM studio_users),(SELECT count(*) FROM studio_sessions)").Scan(&projects, &users, &sessions); err != nil {
				t.Fatal(err)
			}
			if projects != 1 || users != 0 || sessions != 0 {
				t.Fatal("project/account/session partially committed without project audit")
			}
		})
	}
}

func TestPostgresAuditFailureRollsBackReconcileAndTaskAdmission(t *testing.T) {
	for _, operation := range []string{"accept", "cancel", "reconcile"} {
		t.Run(operation, func(t *testing.T) {
			databaseURL := testDatabase(t)
			app, server := testApp(t, Config{DatabaseURL: databaseURL, PollInterval: time.Minute})
			var task *taskRecord
			if operation != "accept" {
				now := time.Now().UTC()
				task = &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", ModelID: "studio-image", Kind: "image", Status: "RECONCILE_REQUIRED", CanReconcile: true, Version: 9, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{}}, Input: taskInput("image"), Handle: MediaHandle{ID: "original-provider-handle"}, NextPoll: now}
				if err := app.store.createTask(task, "ws_demo:tasks:fixture-parked", payloadHash(task.Input)); err != nil {
					t.Fatal(err)
				}
			}
			events := make(chan event, 4)
			app.mu.Lock()
			app.workspaceSubscribers[events] = subscriptionScope{WorkspaceID: "ws_demo", ProjectID: "prj_demo"}
			app.mu.Unlock()
			admin, err := pgx.Connect(context.Background(), databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(context.Background())
			_, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_task_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture task audit failure'; END $$; CREATE TRIGGER reject_task_audit_fixture BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_task_audit_fixture()`)
			if err != nil {
				t.Fatal(err)
			}
			path, key, body := "/api/v1/tasks", "audit-task-rollback-key", any(taskInput("image"))
			if operation != "accept" {
				path = "/api/v1/tasks/" + task.Task.ID + "/" + operation
				key = ""
				body = nil
			}
			code, _, _ := request(t, server, "POST", path, key, body)
			if code != 503 {
				t.Fatalf("expected503, got%d", code)
			}
			if len(events) != 0 {
				t.Fatal("uncommitted task transition was broadcast")
			}
			var count int
			if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_audit_events").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed action left an audit row")
			}
			if operation == "accept" {
				var tasks, keys int
				if err = admin.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM studio_tasks),(SELECT count(*) FROM studio_idempotency)").Scan(&tasks, &keys); err != nil {
					t.Fatal(err)
				}
				if tasks != 0 || keys != 0 {
					t.Fatal("task admission partially committed")
				}
			} else {
				var doc []byte
				if err = admin.QueryRow(context.Background(), "SELECT document FROM studio_tasks WHERE id=$1", task.Task.ID).Scan(&doc); err != nil {
					t.Fatal(err)
				}
				saved := decode[Task](t, doc)
				if saved.Status != "RECONCILE_REQUIRED" || saved.Version != 9 || saved.ReconcileAfter != nil {
					t.Fatal("reconcile state/cooldown committed without audit")
				}
				app.mu.Lock()
				cached := app.tasks[task.Task.ID].Task
				app.mu.Unlock()
				if cached.Status != "RECONCILE_REQUIRED" || cached.Version != 9 || cached.ReconcileAfter != nil {
					t.Fatal("failed audit exposed uncommitted in-memory task state")
				}
			}
		})
	}
}

func TestAuditDetailsHasNoOpenEndedPayloadField(t *testing.T) {
	var event AuditEvent
	_ = json.Unmarshal([]byte(`{"details":{"prompt":"SECRET_PROMPT","output":"SECRET_OUTPUT","password":"SECRET_PASSWORD","key":"SECRET_KEY","cookie":"SECRET_COOKIE","from_status":"QUEUED"}}`), &event)
	data, _ := json.Marshal(event)
	if strings.Contains(string(data), "SECRET_") {
		t.Fatal("unknown audit payload fields were reserialized")
	}
	if !bytes.Contains(data, []byte("QUEUED")) {
		t.Fatal("safe details lost")
	}
}
