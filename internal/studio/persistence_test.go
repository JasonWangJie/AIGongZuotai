package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func testDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not configured; PostgreSQL integration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal("cannot connect to integration database")
	}
	schema := newID("studio_test")
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := conn.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		if err != nil {
			t.Error("test schema cleanup failed", err)
		}
		_ = conn.Close(ctx)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
func authClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}
func authRequest(t *testing.T, c *http.Client, base, method, path, key, project string, body any) (int, []byte, http.Header) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if project != "" {
		req.Header.Set("X-Project-ID", project)
	}
	response, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}

type testIdentity struct {
	User      User      `json:"user"`
	Workspace Workspace `json:"workspace"`
	Project   Project   `json:"project"`
	Projects  []Project `json:"projects"`
}

func registerFixture(t *testing.T, c *http.Client, base, email string) testIdentity {
	t.Helper()
	code, data, headers := authRequest(t, c, base, "POST", "/api/v1/auth/register", "", "", map[string]string{"email": email, "password": "fixture-password-123", "name": "Fixture Creator"})
	if code != 201 {
		t.Fatalf("register %d %s", code, data)
	}
	if cookie := headers.Get("Set-Cookie"); !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatal("session cookie protections missing")
	}
	return decode[testIdentity](t, data)
}
func awaitAuthRun(t *testing.T, c *http.Client, base, id string) Run {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		code, data, _ := authRequest(t, c, base, "GET", "/api/v1/runs/"+id+"/snapshot", "", "", nil)
		if code != 200 {
			t.Fatalf("snapshot %d %s", code, data)
		}
		run := decode[Run](t, data)
		if terminal(run.Status) {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return Run{}
}

type recoveryMedia struct {
	submits, queries atomic.Int32
	complete         atomic.Bool
	park             atomic.Bool
}

func (p *recoveryMedia) Submit(ctx context.Context, in TaskInput) (MediaHandle, error) {
	p.submits.Add(1)
	return MediaHandle{ID: "accepted-provider-task", Kind: in.Kind, Count: 1, SubmittedAt: time.Now()}, ctx.Err()
}
func (p *recoveryMedia) Query(ctx context.Context, h MediaHandle) (MediaResult, error) {
	p.queries.Add(1)
	if p.park.Load() {
		return MediaResult{}, &MediaProviderError{Code: "RESULT_RECONCILE_REQUIRED", Message: "fixture storage requires reconciliation", ReconcileRequired: true}
	}
	if p.complete.Load() {
		return MediaResult{Status: "SUCCEEDED", Progress: 100, Artifacts: []Artifact{{ID: "fixture-artifact", URL: "/demo-assets/image-1.svg", Kind: "image", Title: "fixture"}}}, nil
	}
	return MediaResult{Status: "RUNNING", Progress: 40}, ctx.Err()
}

func TestReconcileOnlyQueriesKnownHandleWithCooldown(t *testing.T) {
	provider := &recoveryMedia{}
	provider.park.Store(true)
	app, server := testApp(t, Config{mediaProvider: provider, PollInterval: 5 * time.Millisecond})
	_, data, _ := request(t, server, "POST", "/api/v1/tasks", "reconcile-known-key", taskInput("image"))
	task := decode[Task](t, data)
	awaitPark := func() Task {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			_, body, _ := request(t, server, "GET", "/api/v1/tasks/"+task.ID, "", nil)
			current := decode[Task](t, body)
			if current.Status == "RECONCILE_REQUIRED" {
				return current
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("task did not enter reconciliation")
		return Task{}
	}
	parked := awaitPark()
	if !parked.CanReconcile {
		t.Fatal("known handle did not expose safe reconciliation")
	}
	code, data, _ := request(t, server, "POST", "/api/v1/tasks/"+task.ID+"/reconcile", "", nil)
	if got := decode[Task](t, data); code != 202 || got.Version != parked.Version+1 || got.ReconcileAfter == nil {
		t.Fatalf("reconcile %d %s", code, data)
	}
	awaitPark()
	code, _, _ = request(t, server, "POST", "/api/v1/tasks/"+task.ID+"/reconcile", "", nil)
	if code != 429 {
		t.Fatal("reconciliation cooldown bypassed")
	}
	provider.park.Store(false)
	provider.complete.Store(true)
	app.mu.Lock()
	expired := time.Now().Add(-time.Second)
	app.tasks[task.ID].Task.ReconcileAfter = &expired
	app.mu.Unlock()
	code, _, _ = request(t, server, "POST", "/api/v1/tasks/"+task.ID+"/reconcile", "", nil)
	if code != 202 {
		t.Fatal("known handle did not resume querying")
	}
	if got := waitTask(t, server, task.ID); got.Status != "SUCCEEDED" || provider.submits.Load() != 1 {
		t.Fatal("reconciliation regenerated the media")
	}
	unknownID := newID("task")
	app.mu.Lock()
	app.tasks[unknownID] = &taskRecord{Task: Task{ID: unknownID, WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", Status: "SUBMIT_UNKNOWN", Artifacts: []Artifact{}}}
	app.mu.Unlock()
	code, _, _ = request(t, server, "POST", "/api/v1/tasks/"+unknownID+"/reconcile", "", nil)
	if code != 409 {
		t.Fatal("unknown handle was allowed to resubmit")
	}
	code, _, _ = request(t, server, "POST", "/api/v1/tasks/"+unknownID+"/reconcile", "", map[string]string{"upstream_id": "untrusted"})
	if code != 400 {
		t.Fatal("client supplied upstream handle accepted")
	}
}
func (p *recoveryMedia) Cancel(ctx context.Context, h MediaHandle) error { return ctx.Err() }

func TestPostgresPersistenceAuthIsolationAndRecovery(t *testing.T) {
	databaseURL := testDatabase(t)
	provider := &recoveryMedia{}
	config := Config{DatabaseURL: databaseURL, AuthMode: "required", AssetDir: t.TempDir(), DemoTokenDelay: time.Millisecond, PollInterval: 10 * time.Millisecond, Heartbeat: 20 * time.Millisecond, MaxRecords: 3, mediaProvider: provider}
	app, err := newIsolatedApp(t, config)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { app.Close(); server.Close() })
	anonymous, alice, bob := authClient(t), authClient(t), authClient(t)
	code, _, _ := authRequest(t, anonymous, server.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("bootstrap did not require authentication")
	}
	code, data, _ := authRequest(t, anonymous, server.URL, "GET", "/api/v1/auth/session", "", "", nil)
	if code != 200 || !bytes.Contains(data, []byte(`"user":null`)) {
		t.Fatal("anonymous session discovery failed")
	}
	a := registerFixture(t, alice, server.URL, "alice@example.test")
	b := registerFixture(t, bob, server.URL, "bob@example.test")
	if a.Workspace.ID == b.Workspace.ID || a.Project.ID == b.Project.ID {
		t.Fatal("registration reused tenant identity")
	}
	if second, e := newIsolatedApp(t, config); e == nil {
		second.Close()
		t.Fatal("second writer acquired database")
	}
	code, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/projects", "", "", map[string]string{"name": "Second project"})
	if code != 201 {
		t.Fatalf("project %d %s", code, data)
	}
	secondProject := decode[Project](t, data)
	input := runInput()
	input.ProjectID = a.Project.ID
	code, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "persistent-run-key", "", input)
	if code != 201 {
		t.Fatalf("create %d %s", code, data)
	}
	first := awaitAuthRun(t, alice, server.URL, decode[Run](t, data).ID)
	for _, path := range []string{"/api/v1/runs/" + first.ID + "/snapshot", "/api/v1/runs/" + first.ID + "/events", "/api/v1/workspaces/" + a.Workspace.ID + "/events"} {
		code, _, _ = authRequest(t, bob, server.URL, "GET", path, "", "", nil)
		if code != 404 {
			t.Fatalf("foreign resource leaked %s: %d", path, code)
		}
	}
	code, _, _ = authRequest(t, bob, server.URL, "POST", "/api/v1/runs/"+first.ID+"/cancel", "", "", nil)
	if code != 404 {
		t.Fatal("foreign cancel permitted")
	}
	code, _, _ = authRequest(t, bob, server.URL, "POST", "/api/v1/runs", "foreign-create-key", "", input)
	if code != 422 {
		t.Fatal("foreign project creation permitted")
	}
	code, _, _ = authRequest(t, bob, server.URL, "GET", "/api/v1/bootstrap", "", a.Project.ID, nil)
	if code != 404 {
		t.Fatal("foreign selected project leaked")
	}
	code, data, _ = authRequest(t, bob, server.URL, "GET", "/api/v1/runs", "", "", nil)
	if code != 200 || len(decode[struct {
		Items []Run `json:"items"`
	}](t, data).Items) != 0 {
		t.Fatal("cross-tenant list leak")
	}
	// Historical records must not consume the active cache limit permanently.
	for i := 0; i < 5; i++ {
		in := input
		in.Input = "history fixture"
		_, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "history-key-"+jsonNumber(uint64(i)), "", in)
		awaitAuthRun(t, alice, server.URL, decode[Run](t, data).ID)
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/runs?limit=2&offset=2", "", "", nil)
	if code != 200 || len(decode[struct {
		Items []Run `json:"items"`
	}](t, data).Items) != 2 {
		t.Fatal("database history pagination failed")
	}
	code, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "persistent-run-key", "", input)
	if code != 201 || decode[Run](t, data).ID != first.ID {
		t.Fatal("evicted idempotency resource could not be reloaded")
	}
	inSecond := input
	inSecond.ProjectID = secondProject.ID
	_, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "second-project-run", "", inSecond)
	secondRun := awaitAuthRun(t, alice, server.URL, decode[Run](t, data).ID)
	_, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/runs", "", secondProject.ID, nil)
	if list := decode[struct {
		Items []Run `json:"items"`
	}](t, data); len(list.Items) != 1 || list.Items[0].ID != secondRun.ID {
		t.Fatal("project scoped list incorrect")
	}
	// Passwords and bearer session values must not be stored as plaintext.
	u, _ := url.Parse(server.URL)
	cookies := alice.Jar.Cookies(u)
	if len(cookies) == 0 {
		t.Fatal("missing session")
	}
	rawToken := cookies[0].Value
	expectedHash := sha256.Sum256([]byte(rawToken))
	err = app.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var passwordHash, tokenHash []byte
		if err := conn.QueryRow(ctx, "SELECT u.password_hash,s.token_hash FROM studio_users u JOIN studio_sessions s ON s.user_id=u.id WHERE u.id=$1", a.User.ID).Scan(&passwordHash, &tokenHash); err != nil {
			return err
		}
		if !bytes.HasPrefix(passwordHash, []byte("$2")) || !bytes.Equal(tokenHash, expectedHash[:]) {
			t.Error("credential storage not hashed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/auth/logout", "", "", nil)
	if code != 200 {
		t.Fatal("logout failed")
	}
	alice.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: rawToken, Path: "/"}})
	code, _, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("revoked token still accepted")
	}
	login := map[string]string{"email": "alice@example.test", "password": "fixture-password-123"}
	code, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/auth/login", "", "", login)
	if code != 200 {
		t.Fatal("login failed")
	}
	_ = app.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "UPDATE studio_sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1", a.User.ID)
		return err
	})
	code, _, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("expired session accepted")
	}
	code, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/auth/login", "", "", login)
	if code != 200 {
		t.Fatal("login after expiry failed")
	}
	// Keep an accepted media task in progress; restart must only query its handle.
	mediaInput := taskInput("image")
	mediaInput.ProjectID = a.Project.ID
	_, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/tasks", "accepted-media-key", "", mediaInput)
	mediaTask := decode[Task](t, data)
	deadline := time.Now().Add(2 * time.Second)
	for provider.submits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if provider.submits.Load() != 1 {
		t.Fatal("media not submitted")
	}
	// Seed a pre-021 crash checkpoint without a stream identity. Its browser
	// may have observed volatile events beyond the stored sequence.
	now := time.Now().UTC()
	interrupted := Run{ID: newID("run"), ProviderMode: "demo", WorkspaceID: a.Workspace.ID, ProjectID: a.Project.ID, ModelID: "studio-chat", Prompt: "crash fixture", Status: "RUNNING", Output: "saved checkpoint", Seq: 7, CreatedAt: now, UpdatedAt: now}
	if err = ledgerTx(app.store, func(ctx context.Context, tx pgx.Tx) error {
		doc, err := json.Marshal(interrupted)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_runs(id,workspace_id,project_id,document,updated_at) VALUES($1,$2,$3,$4,$5)", interrupted.ID, interrupted.WorkspaceID, interrupted.ProjectID, doc, now); err != nil {
			return err
		}
		hash := payloadHash(input)
		if _, err = tx.Exec(ctx, "INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type) VALUES($1,$2,$3,'run')", a.Workspace.ID+":runs:crash-run-key", hash[:], interrupted.ID); err != nil {
			return err
		}
		return appendRunOutboxSQL(ctx, tx, interrupted)
	}); err != nil {
		t.Fatal(err)
	}
	unknown := &taskRecord{Task: Task{ID: newID("task"), ProviderMode: "demo", WorkspaceID: a.Workspace.ID, ProjectID: a.Project.ID, Kind: "image", ModelID: "studio-image", Prompt: "ambiguous", Status: "SUBMITTING", Version: 2, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{}}, Input: mediaInput, NextPoll: now, Submission: &MediaSubmission{Body: []byte(`{"model":"fixture","prompt":"ambiguous"}`), IdempotencyKey: "frozen-provider-key", CredentialID: "fixture-credential"}}
	if err = app.store.createTask(unknown, a.Workspace.ID+":tasks:ambiguous-key", payloadHash(mediaInput)); err != nil {
		t.Fatal(err)
	}
	// A parked task with a known handle keeps its cooldown across process restarts.
	cooldown := time.Now().UTC().Add(time.Minute)
	parkedRecord := &taskRecord{Task: Task{ID: newID("task"), ProviderMode: "demo", WorkspaceID: a.Workspace.ID, ProjectID: a.Project.ID, Kind: "image", ModelID: "studio-image", Status: "RECONCILE_REQUIRED", CanReconcile: true, ReconcileAfter: &cooldown, Version: 2, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{}}, Input: mediaInput, Handle: MediaHandle{ID: "original-accepted-id", Kind: "image"}, NextPoll: now}
	if err = app.store.createTask(parkedRecord, a.Workspace.ID+":tasks:cooldown-key", payloadHash(mediaInput)); err != nil {
		t.Fatal(err)
	}
	// Imported history stays downloadable even when the original live provider
	// is disabled; neither a gateway key nor live mode is required for local reads.
	assetID := "asset_" + strings.Repeat("a", 64)
	assetBytes := fixturePNG(t)
	assetHash := sha256.Sum256(assetBytes)
	meta, _ := json.Marshal(AssetInfo{ID: assetID, ContentType: "image/png", ByteSize: int64(len(assetBytes)), SHA256: hex.EncodeToString(assetHash[:]), CreatedAt: now})
	if err = os.WriteFile(filepath.Join(config.AssetDir, assetID+".bin"), assetBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(config.AssetDir, assetID+".json"), meta, 0600); err != nil {
		t.Fatal(err)
	}
	assetRecord := &taskRecord{Task: Task{ID: newID("task"), ProviderMode: "live", WorkspaceID: a.Workspace.ID, ProjectID: a.Project.ID, Kind: "image", ModelID: "studio-image", Status: "SUCCEEDED", Version: 4, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{{ID: assetID, URL: "/api/v1/assets/" + assetID, Kind: "image", Title: "persisted fixture"}}}, Input: mediaInput, NextPoll: now}
	if err = app.store.createTask(assetRecord, a.Workspace.ID+":tasks:asset-history-key", payloadHash(mediaInput)); err != nil {
		t.Fatal(err)
	}
	oldServer := server
	app.Close()
	oldServer.Close()
	provider.complete.Store(true)
	app, err = newIsolatedApp(t, config)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	// Cookies are host-scoped, not port-scoped; they remain valid after restart.
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/runs/"+interrupted.ID+"/snapshot", "", "", nil)
	if code != 200 {
		t.Fatalf("restored snapshot %d %s", code, data)
	}
	restored := decode[Run](t, data)
	if restored.Status != "INTERRUPTED" || restored.Output != "saved checkpoint" || restored.Seq != 7+recoverySequenceGap {
		t.Fatalf("incorrect interrupted recovery %+v", restored)
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/runs/"+interrupted.ID+"/events?after=0", "", "", nil)
	if code != 200 || !bytes.Contains(data, []byte("run.snapshot")) {
		t.Fatal("restored stream did not return snapshot")
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/runs/"+interrupted.ID+"/events?after=100", "", "", nil)
	if code != 200 || !bytes.Contains(data, []byte("run.snapshot")) {
		t.Fatal("browser cursor ahead of checkpoint could not accept recovery snapshot")
	}
	code, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "persistent-run-key", "", input)
	if code != 201 || decode[Run](t, data).ID != first.ID {
		t.Fatal("idempotency did not survive restart")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/tasks/"+mediaTask.ID, "", "", nil)
		if decode[Task](t, data).Status == "SUCCEEDED" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if decode[Task](t, data).Status != "SUCCEEDED" || provider.submits.Load() != 1 || provider.queries.Load() == 0 {
		t.Fatal("accepted media task was not resumed by query only")
	}
	_, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/tasks/"+unknown.Task.ID, "", "", nil)
	if decode[Task](t, data).Status != "SUBMIT_UNKNOWN" {
		t.Fatal("ambiguous submission was automatically retried")
	}
	if provider.submits.Load() != 1 {
		t.Fatal("unknown media was resubmitted")
	}
	code, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/tasks/"+parkedRecord.Task.ID+"/reconcile", "", "", nil)
	if code != 429 {
		t.Fatal("reconciliation cooldown did not survive restart")
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/assets/"+assetID, "", "", nil)
	if code != 200 || !bytes.Equal(data, assetBytes) {
		t.Fatalf("offline historical asset unavailable: %d", code)
	}
	code, _, _ = authRequest(t, bob, server.URL, "GET", "/api/v1/assets/"+assetID, "", "", nil)
	if code != 404 {
		t.Fatal("private historical asset leaked across accounts")
	}
	code, _, _ = authRequest(t, bob, server.URL, "POST", "/api/v1/tasks/"+parkedRecord.Task.ID+"/reconcile", "", "", nil)
	if code != 404 {
		t.Fatal("foreign reconciliation permitted")
	}
	app.Close()
	server.Close()
	app, err = newIsolatedApp(t, config)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/runs/"+interrupted.ID+"/snapshot", "", "", nil)
	if code != 200 || decode[Run](t, data).Seq != restored.Seq {
		t.Fatal("restarting an already interrupted run changed its sequence")
	}
	// Losing the exact lock connection must fail closed, not reconnect and write.
	app.store.mu.Lock()
	_ = app.store.conn.Close(context.Background())
	app.store.mu.Unlock()
	code, _, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 503 {
		t.Fatal("database loss did not fail closed")
	}
	code, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "after-db-failure", "", input)
	if code != 503 {
		t.Fatal("database loss still admitted generation")
	}
}

func TestRecoverySequenceExceedsMaximumOutputAndTerminalEvents(t *testing.T) {
	app, server := testApp(t, Config{})
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		if err := emit(""); err != nil {
			return err
		}
		for range maxRunOutputBytes {
			if err := emit("x"); err != nil {
				return err
			}
		}
		return nil
	})
	_, data, _ := request(t, server, "POST", "/api/v1/runs", "maximum-sequence-key", runInput())
	run := awaitMaxRun(t, server, decode[Run](t, data).ID)
	if run.Status != "COMPLETED" || run.Seq != maxRunOutputBytes+1 {
		t.Fatalf("output/terminal bound changed: status=%s seq=%d", run.Status, run.Seq)
	}
	if uint64(recoverySequenceGap) <= run.Seq {
		t.Fatal("recovery sequence does not strictly exceed all possible volatile events including terminal")
	}
}

func awaitMaxRun(t *testing.T, server *httptest.Server, id string) Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, data, _ := request(t, server, "GET", "/api/v1/runs/"+id+"/snapshot", "", nil)
		run := decode[Run](t, data)
		if terminal(run.Status) {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("maximal event fixture did not complete")
	return Run{}
}

func TestAuthDisabledAndLimiterBoundaries(t *testing.T) {
	_, server := testApp(t, Config{})
	code, data, _ := request(t, server, "GET", "/api/v1/auth/session", "", nil)
	if code != 200 || !bytes.Contains(data, []byte(`"auth_enabled":false`)) {
		t.Fatal("disabled auth discovery changed demo")
	}
	if app, err := newIsolatedApp(t, Config{AuthMode: "required"}); err == nil {
		app.Close()
		t.Fatal("required auth started without PostgreSQL")
	}
	limiter := newLoginLimiter()
	for range 10 {
		if !limiter.allow("fixture", 10) {
			t.Fatal("limiter rejected too early")
		}
	}
	if limiter.allow("fixture", 10) {
		t.Fatal("limiter did not bound attempts")
	}
}

func TestPrivateAssetsCannotOverlapPublicFrontend(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "dist")
	if err := validateAssetSeparation(web, filepath.Join(web, "private-assets")); err == nil {
		t.Fatal("private assets under web root accepted")
	}
	if err := validateAssetSeparation(web, web); err == nil {
		t.Fatal("shared public/private directory accepted")
	}
	if err := validateAssetSeparation(web, filepath.Join(root, "private-assets")); err != nil {
		t.Fatal(err)
	}
	if app, err := newIsolatedApp(t, Config{AsyncImageBaseURL: "https://fixture.invalid", AssetDir: "apps/workbench/public/private-media"}); err == nil {
		app.Close()
		t.Fatal("Vite public asset bypass accepted")
	}
	if err := os.Mkdir(web, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(web, link); err == nil {
		if err := validateAssetSeparation(web, filepath.Join(link, "private")); err == nil {
			t.Fatal("symlink bypass accepted")
		}
	}
}

func TestPostgresPeriodicAndTerminalCheckpoints(t *testing.T) {
	databaseURL := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: databaseURL})
	first := make(chan struct{})
	second := make(chan struct{})
	continueOutput := make(chan struct{})
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		if err := emit("first checkpoint"); err != nil {
			return err
		}
		close(first)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-continueOutput:
		}
		if err := emit(" + final delta"); err != nil {
			return err
		}
		close(second)
		<-ctx.Done()
		return ctx.Err()
	})
	_, data, _ := request(t, server, "POST", "/api/v1/runs", "checkpoint-timer-key", runInput())
	run := decode[Run](t, data)
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("provider did not emit")
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	var saved Run
	for time.Now().Before(deadline) {
		var err error
		saved, err = app.store.loadRun(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Output == "first checkpoint" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if saved.Output != "first checkpoint" || saved.Seq != 1 {
		t.Fatal("idle stream delta was not saved by timer")
	}
	close(continueOutput)
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("second delta missing")
	}
	code, _, _ := request(t, server, "POST", "/api/v1/runs/"+run.ID+"/cancel", "", nil)
	if code != 200 {
		t.Fatal("cancel failed")
	}
	saved, err := app.store.loadRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "CANCELLED" || saved.Output != "first checkpoint + final delta" || saved.Seq != 3 {
		t.Fatalf("terminal checkpoint not forced: %+v", saved)
	}
}

func TestPostgresWriterLossCancelsIdleProviderWithoutHTTPActivity(t *testing.T) {
	databaseURL := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: databaseURL})
	started := make(chan struct{})
	stopped := make(chan struct{})
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	_, _, _ = request(t, server, "POST", "/api/v1/runs", "idle-lock-loss-key", runInput())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	// This closes the exact PostgreSQL session holding the advisory lock. No
	// subsequent HTTP request or delta should be necessary to detect its loss.
	app.store.mu.Lock()
	_ = app.store.conn.Close(context.Background())
	app.store.mu.Unlock()
	select {
	case <-stopped:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("idle provider kept executing after losing writer connection")
	}
}
