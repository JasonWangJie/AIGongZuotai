package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func writeCatalogFixture(t *testing.T, config catalogFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	rewriteCatalogFixture(t, path, config)
	return path
}
func rewriteCatalogFixture(t *testing.T, path string, config catalogFile) {
	t.Helper()
	blob, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, blob, 0600); err != nil {
		t.Fatal(err)
	}
}
func customCatalogFixture() catalogFile {
	c := defaultCatalogConfig(Config{Mode: "demo"})
	c.Models[0].ID = "custom-chat"
	c.Models[0].UpstreamModel = "private-chat-v1"
	c.Models[1].ID = "custom-image"
	c.Models[1].Name = "Original image"
	c.Models[1].UpstreamModel = "private-image-v1"
	c.Models[1].Capabilities.ParameterPresets = []ParameterPreset{{ID: "square", Name: "Square", AspectRatio: "1:1", Size: "1024x1024", Resolution: "2K"}, {ID: "landscape", Name: "Landscape", AspectRatio: "16:9", Size: "1536x864", Resolution: "1K"}}
	c.Models[2].ID = "custom-video"
	c.Models[2].UpstreamModel = "private-video-v1"
	return c
}

func TestCatalogStrictConfigurationAndStableVersion(t *testing.T) {
	base := customCatalogFixture()
	path := writeCatalogFixture(t, base)
	one, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: path})
	if err != nil {
		t.Fatal(err)
	}
	two, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: path})
	if err != nil || one.Public.Version != two.Public.Version || !bytes.Equal(one.JSON, two.JSON) {
		t.Fatal("identical catalog is not deterministic")
	}
	for _, secret := range []string{"upstream_model", "private-chat-v1", "private-image-v1"} {
		if bytes.Contains(one.JSON, []byte(secret)) {
			t.Fatal("private binding exposed", secret)
		}
	}
	base.Models[1].UpstreamModel = "private-image-v2"
	rewriteCatalogFixture(t, path, base)
	changed, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: path})
	if err != nil || changed.Public.Version == one.Public.Version || changed.Entries["custom-image"].Model.Version == one.Entries["custom-image"].Model.Version {
		t.Fatal("binding change must invalidate versions")
	}
	for name, mutate := range map[string]func(*catalogFile){
		"schema": func(c *catalogFile) { c.SchemaVersion = 2 }, "enabled missing": func(c *catalogFile) { c.Models[0].Enabled = nil },
		"duplicate id": func(c *catalogFile) { c.Models[1].ID = c.Models[0].ID }, "reserved kind": func(c *catalogFile) { c.Models[1].ID = "studio-chat" },
		"count": func(c *catalogFile) { c.Models[1].Capabilities.MaxCount = 5 }, "video count": func(c *catalogFile) { c.Models[2].Capabilities.MaxCount = 2 },
		"prompt": func(c *catalogFile) { c.Models[0].Capabilities.MaxPromptBytes = 16001 }, "image duration": func(c *catalogFile) { c.Models[1].Capabilities.ParameterPresets[0].Seconds = 5 },
		"edit on chat": func(c *catalogFile) {
			c.Models[0].Capabilities.ImageEditSupported = true
			c.Models[0].Capabilities.MaxReferenceImages = 1
		},
		"edit on video": func(c *catalogFile) {
			c.Models[2].Capabilities.ImageEditSupported = true
			c.Models[2].Capabilities.MaxReferenceImages = 1
		},
		"edit too many references":      func(c *catalogFile) { c.Models[1].Capabilities.MaxReferenceImages = 5 },
		"edit zero references":          func(c *catalogFile) { c.Models[1].Capabilities.MaxReferenceImages = 0 },
		"disabled edit with references": func(c *catalogFile) { c.Models[1].Capabilities.ImageEditSupported = false },
		"duplicate preset": func(c *catalogFile) {
			c.Models[1].Capabilities.ParameterPresets = append(c.Models[1].Capabilities.ParameterPresets, c.Models[1].Capabilities.ParameterPresets[0])
		},
		"too many models": func(c *catalogFile) {
			for len(c.Models) < 33 {
				c.Models = append(c.Models, c.Models[0])
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := customCatalogFixture()
			mutate(&c)
			if _, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: writeCatalogFixture(t, c)}); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	for name, raw := range map[string]string{"duplicate keys": `{"schema_version":1,"schema_version":2,"models":[]}`, "unknown field": `{"schema_version":1,"models":[],"api_key":"no"}`, "missing schema": `{"models":[]}`, "oversize": strings.Repeat(" ", 65537)} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: path}); err == nil {
				t.Fatal("invalid JSON accepted")
			}
		})
	}
}

func TestCatalogLegacyRequestHashMatchesHistoricalJSONBytes(t *testing.T) {
	// Literal pre-V06 JSON, not marshaled with today's structs. Adding optional
	// fields must not alter the old order/omission rules of persisted hashes.
	run := RunInput{ProjectID: "prj_demo", ModelID: "studio-chat", Input: "legacy prompt"}
	wantRun := sha256.Sum256([]byte(`{"project_id":"prj_demo","model_id":"studio-chat","input":"legacy prompt"}`))
	if payloadHash(run) != wantRun {
		t.Fatal("legacy run request hash changed")
	}
	in := TaskInput{ProjectID: "prj_demo", Kind: "image", ModelID: "studio-image"}
	in.Inputs.Prompt = "legacy prompt"
	wantTask := sha256.Sum256([]byte(`{"project_id":"prj_demo","kind":"image","model_id":"studio-image","inputs":{"prompt":"legacy prompt"},"parameters":{"aspect_ratio":"16:9","count":1}}`))
	if payloadHash(legacyTaskInput(in)) != wantTask {
		t.Fatal("legacy normalized task request hash changed")
	}
}

func TestCatalogETagBootstrapAndRestartOnlyRefresh(t *testing.T) {
	config := customCatalogFixture()
	path := writeCatalogFixture(t, config)
	a, s := testApp(t, Config{ModelCatalogFile: path})
	code, body, headers := request(t, s, "GET", "/api/v1/models", "", nil)
	if code != 200 {
		t.Fatal(code)
	}
	catalog := decode[Catalog](t, body)
	etag := headers.Get("ETag")
	if etag != `"`+catalog.Version+`"` || headers.Get("Cache-Control") != "private, no-cache" {
		t.Fatal("bad catalog validators")
	}
	req, _ := http.NewRequest("GET", s.URL+"/api/v1/models", nil)
	req.Header.Set("If-None-Match", etag)
	response, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 304 {
		t.Fatal("matching validator not honored")
	}
	_, body, _ = request(t, s, "GET", "/api/v1/bootstrap", "", nil)
	var bootstrap struct {
		CatalogVersion string  `json:"catalog_version"`
		Models         []Model `json:"models"`
	}
	_ = json.Unmarshal(body, &bootstrap)
	if bootstrap.CatalogVersion != catalog.Version || len(bootstrap.Models) != len(catalog.Models) {
		t.Fatal("bootstrap catalog mismatch")
	}
	config.Models[0].Name = "Changed"
	rewriteCatalogFixture(t, path, config)
	if a.catalog.Public.Version != catalog.Version {
		t.Fatal("file rewrite hot reloaded")
	}
	b, err := newIsolatedApp(t, Config{ModelCatalogFile: path})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.catalog.Public.Version == catalog.Version {
		t.Fatal("restart did not load changed config")
	}
}

func TestCatalogExactPresetVersionAndImmutableSnapshots(t *testing.T) {
	a, s := testApp(t, Config{ModelCatalogFile: writeCatalogFixture(t, customCatalogFixture()), PollInterval: time.Hour})
	input := taskInput("image")
	input.ModelID = "custom-image"
	input.ModelVersion = a.catalog.Entries[input.ModelID].Model.Version
	input.Parameters = TaskParameters{Count: 1, AspectRatio: "1:1", Size: "1536x864", Resolution: "2K"}
	code, body, _ := request(t, s, "POST", "/api/v1/tasks", "invalid-preset-key", input)
	if code != 422 || !bytes.Contains(body, []byte("INVALID_MODEL_PARAMETERS")) {
		t.Fatalf("mixed preset accepted %d %s", code, body)
	}
	input.Parameters = TaskParameters{Count: 1, AspectRatio: "1:1", Size: "1024x1024", Resolution: "2K"}
	input.ModelVersion = "old-version"
	code, body, _ = request(t, s, "POST", "/api/v1/tasks", "outdated-version-key", input)
	if code != 409 || !bytes.Contains(body, []byte("MODEL_VERSION_CHANGED")) {
		t.Fatal("stale model version accepted")
	}
	input.ModelVersion = a.catalog.Entries[input.ModelID].Model.Version
	task, replay, err := a.createTask(input, "snapshot-immutable-key")
	if err != nil || replay {
		t.Fatal(err)
	}
	if task.ModelName != "Original image" || task.Parameters.Size != "1024x1024" || task.ModelCapabilities == nil {
		t.Fatal("effective snapshot missing")
	}
	task.Parameters.Size = "mutated"
	task.ModelCapabilities.ParameterPresets[0].Size = "mutated"
	again, _, err := a.createTask(input, "snapshot-immutable-key")
	if err != nil || again.Parameters.Size != "1024x1024" || again.ModelCapabilities.ParameterPresets[0].Size != "1024x1024" {
		t.Fatal("caller mutated stored task snapshot")
	}
	runIn := runInput()
	runIn.ModelID = "custom-chat"
	run, _, err := a.createRun(runIn, "run-snapshot-immutable")
	if err != nil {
		t.Fatal(err)
	}
	run.ModelCapabilities.MaxPromptBytes = 1
	run, _, err = a.createRun(runIn, "run-snapshot-immutable")
	if err != nil || run.ModelCapabilities.MaxPromptBytes != 16000 {
		t.Fatal("caller mutated stored run capabilities")
	}
	snap, err := a.admissionSnapshot("ws_demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range append(snap.Runs.Models, snap.Tasks.Models...) {
		if strings.HasPrefix(m.ModelID, "studio-") {
			t.Fatal("nonexistent legacy model leaked into custom runtime")
		}
	}
}

type catalogMediaFixture struct {
	mu                  sync.Mutex
	prepared, submitted []MediaSubmission
	queries             int
}

func (p *catalogMediaFixture) PrepareModel(in TaskInput, model string) (MediaSubmission, error) {
	body, _ := json.Marshal(struct {
		Model      string
		Parameters TaskParameters
	}{model, in.Parameters})
	submission := MediaSubmission{Body: body, Kind: in.Kind, Count: in.Parameters.Count, IdempotencyKey: newID("prepared"), ProviderIdentity: "fixture", PreparedAt: time.Now()}
	p.mu.Lock()
	p.prepared = append(p.prepared, submission)
	p.mu.Unlock()
	return submission, nil
}
func (p *catalogMediaFixture) SubmitPrepared(ctx context.Context, s MediaSubmission) (MediaHandle, error) {
	p.mu.Lock()
	p.submitted = append(p.submitted, s)
	p.mu.Unlock()
	return MediaHandle{ID: "fixture-known-task", Kind: s.Kind, Count: s.Count}, ctx.Err()
}
func (p *catalogMediaFixture) Submit(context.Context, TaskInput) (MediaHandle, error) {
	return MediaHandle{}, errors.New("must use frozen submission")
}
func (p *catalogMediaFixture) Query(context.Context, MediaHandle) (MediaResult, error) {
	p.mu.Lock()
	p.queries++
	p.mu.Unlock()
	return MediaResult{Status: "SUCCEEDED", Progress: 100}, nil
}
func (p *catalogMediaFixture) Cancel(context.Context, MediaHandle) error { return nil }

func TestCatalogAdmissionFreezesSubmissionWithoutNetwork(t *testing.T) {
	p := &catalogMediaFixture{}
	a, _ := testApp(t, Config{ModelCatalogFile: writeCatalogFixture(t, customCatalogFixture()), PollInterval: time.Hour, mediaProvider: p})
	in := taskInput("image")
	in.ModelID = "custom-image"
	in.Parameters = TaskParameters{} // Original hash remains independent of the first preset.
	task, _, err := a.createTask(in, "freeze-original-key")
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	prepared, submitted := len(p.prepared), len(p.submitted)
	p.mu.Unlock()
	if prepared != 1 || submitted != 0 {
		t.Fatal("admission sent upstream request or did not prepare")
	}
	if task.Parameters.AspectRatio != "1:1" || task.Parameters.Size != "1024x1024" {
		t.Fatal("default preset not frozen")
	}
	if _, replay, err := a.createTask(in, "freeze-original-key"); err != nil || !replay {
		t.Fatal("original omitted parameters no longer replay", err)
	}
	a.executeTaskStep(task.ID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.submitted) != 1 || !bytes.Equal(p.submitted[0].Body, p.prepared[0].Body) || !bytes.Contains(p.submitted[0].Body, []byte("private-image-v1")) {
		t.Fatal("worker rebuilt a different execution request")
	}
}

func TestPostgresCatalogReplaySnapshotsRecoveryAndLegacyHash(t *testing.T) {
	db := testDatabase(t)
	config := customCatalogFixture()
	path := writeCatalogFixture(t, config)
	p := &catalogMediaFixture{}
	cfg := Config{DatabaseURL: db, ModelCatalogFile: path, PollInterval: time.Hour, DemoTokenDelay: time.Hour, mediaProvider: p}
	a, err := newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := taskInput("image")
	in.ModelID = "custom-image"
	in.Parameters = TaskParameters{}
	in.ModelVersion = a.catalog.Entries[in.ModelID].Model.Version
	task, _, err := a.createTask(in, "persisted-original-key")
	if err != nil {
		t.Fatal(err)
	}
	legacyIn := taskInput("image")
	legacyIn.Parameters = TaskParameters{}
	legacy := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ModelID: "studio-image", Kind: "image", ProviderMode: "demo", Status: "SUCCEEDED", CreatedAt: time.Now(), UpdatedAt: time.Now()}, Input: legacyTaskInput(legacyIn), NextPoll: time.Now()}
	if err := a.store.createTask(legacy, "ws_demo:tasks:legacy-original-key", payloadHash(legacyTaskInput(legacyIn))); err != nil {
		t.Fatal(err)
	}
	a.Close()
	off := false
	config.Models[1].Enabled = &off
	config.Models[1].Name = "Disabled image"
	config.Models[1].UpstreamModel = "private-image-v2"
	config.Models[1].Capabilities.MaxPromptBytes = 1
	config.Models[1].Capabilities.ParameterPresets = []ParameterPreset{{ID: "new", Name: "New", AspectRatio: "9:16"}}
	rewriteCatalogFixture(t, path, config)
	a, err = newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	replay, replayed, err := a.createTask(in, "persisted-original-key")
	if err != nil || !replayed || replay.ID != task.ID || replay.ModelVersion != task.ModelVersion || replay.ModelName != "Original image" || replay.Parameters.Size != "1024x1024" {
		t.Fatalf("disabled catalog invalidated replay/history %+v %v", replay, err)
	}
	changed := in
	changed.Inputs.Prompt = "changed"
	_, _, err = a.createTask(changed, "persisted-original-key")
	var operation *operationError
	if !errors.As(err, &operation) || operation.code != "IDEMPOTENCY_CONFLICT" {
		t.Fatal("disabled check ran before idempotency conflict", err)
	}
	newRequest := in
	newRequest.ModelVersion = a.catalog.Entries[in.ModelID].Model.Version
	_, _, err = a.createTask(newRequest, "disabled-new-key")
	if !errors.As(err, &operation) || operation.code != "MODEL_DISABLED" || operation.status != 422 {
		t.Fatal("new disabled request admitted", err)
	}
	if _, replayed, err = a.createTask(legacyIn, "legacy-original-key"); err != nil || !replayed {
		t.Fatal("legacy normalized hash failed after model removed", err)
	}
	a.executeTaskStep(task.ID)
	advanceMediaScheduleDue(t, a, task.ID)
	a.executeTaskStep(task.ID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.prepared) != 1 || len(p.submitted) != 1 || p.queries != 1 || !bytes.Contains(p.submitted[0].Body, []byte("private-image-v1")) {
		t.Fatal("disabled recovery used current binding or resubmitted", p.queries)
	}
}

func TestPostgresCatalogETagNeverBypassesAuthentication(t *testing.T) {
	a, s := testApp(t, Config{DatabaseURL: testDatabase(t), AuthMode: "required"})
	req := httptest.NewRequest("GET", "http://127.0.0.1/api/v1/models", nil)
	req.Header.Set("If-None-Match", `"`+a.catalog.Public.Version+`"`)
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatal("catalog ETag bypassed authentication", out.Code)
	}
	client := authClient(t)
	registerFixture(t, client, s.URL, "catalog-auth@example.test")
	request, _ := http.NewRequest("GET", s.URL+"/api/v1/models", nil)
	request.Header.Set("If-None-Match", `"`+a.catalog.Public.Version+`"`)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 304 {
		t.Fatal("authenticated conditional request failed", response.StatusCode)
	}
}

func TestPostgresCatalogLegacyLiveRecoveryNeverGuessesMissingSnapshot(t *testing.T) {
	db := testDatabase(t)
	p := &catalogMediaFixture{}
	cfg := Config{DatabaseURL: db, Mode: "live", BaseURL: "http://127.0.0.1:1/v1", APIKey: "fixture-unused-private-key", ChatModel: "fixture", mediaProvider: p, PollInterval: time.Hour}
	a, err := newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := range 2 {
		now := time.Now().UTC()
		record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ModelID: "studio-image", Kind: "image", ProviderMode: "live", Status: "QUEUED", CreatedAt: now, UpdatedAt: now}, Input: taskInput("image"), NextPoll: now}
		if i == 1 {
			record.Handle = MediaHandle{ID: "old-known-handle", Kind: "image", Count: 1}
			record.Task.Status = "RUNNING"
		}
		if err := a.store.createTask(record, "legacy-live-"+record.Task.ID, payloadHash(record.Task.ID)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.Task.ID)
	}
	a.Close()
	a, err = newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.tasks[ids[0]].Task.Status != "RECONCILE_REQUIRED" || a.tasks[ids[0]].Task.Error.Code != "EXECUTION_SNAPSHOT_MISSING" {
		t.Fatal("missing snapshot was guessed on restore")
	}
	if _, err := a.reconcileTask(ids[0]); err == nil {
		t.Fatal("unknown legacy submission was retried")
	}
	a.executeTaskStep(ids[1])
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queries != 1 || len(p.prepared) != 0 || len(p.submitted) != 0 {
		t.Fatal("known disabled model handle did not exclusively query")
	}
}

func TestPostgresCatalogFormatFenceUpgradesAndRejectsOldWriter(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	oldFiles := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		// Simulate the actual v0.4 executable: only migrations 001..004.
		if name >= "migrations/005_" {
			continue
		}
		blob, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		oldFiles[name] = &fstest.MapFile{Data: blob}
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigrationsFrom(ctx, tx, oldFiles); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','Legacy workspace'); INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','Legacy project'); INSERT INTO studio_runs(id,workspace_id,project_id,document) VALUES('legacy-format-run','ws_demo','prj_demo','{"id":"legacy-format-run","workspace_id":"ws_demo","project_id":"prj_demo","model_id":"studio-chat","status":"COMPLETED","prompt":"preserved old prompt","output":"preserved old output"}')`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newIsolatedApp(t, Config{DatabaseURL: db})
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	var versions int
	if err = admin.QueryRow(ctx, "SELECT count(*) FROM studio_schema_versions WHERE version BETWEEN 1 AND 6").Scan(&versions); err != nil || versions != 6 {
		t.Fatal("format fence not recorded", versions, err)
	}
	var output string
	if err = admin.QueryRow(ctx, "SELECT document->>'output' FROM studio_runs WHERE id='legacy-format-run'").Scan(&output); err != nil || output != "preserved old output" {
		t.Fatal("upgrade rewrote legacy data", err)
	}
	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, oldFiles); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("old writer migration loader accepted snapshot format", err)
	}
}
