package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func workflowOldMigrations(t *testing.T, version string) fstest.MapFS {
	t.Helper()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if filepath.Base(name)[:3] > version {
			continue
		}
		data, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: data}
	}
	if fmt.Sprintf("%03d", len(old)) != version {
		t.Fatalf("incomplete %s migration fixture", version)
	}
	return old
}

func TestPostgresWorkflowUpgrade022(t *testing.T) {
	ctx := context.Background()
	db := testDatabase(t)
	admin := deliveryAdmin(t, db)
	old := workflowOldMigrations(t, "022")
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing');
		INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing');
		INSERT INTO studio_project_drafts(project_id,workspace_id,revision,updated_at) VALUES('prj_demo','ws_demo',1,now());
		INSERT INTO studio_draft_revisions(project_id,workspace_id,revision,nodes,node_count,created_at)
		VALUES('prj_demo','ws_demo',1,'[{"id":"card","title":"original","note":"unmapped draft"}]',1,now());
		INSERT INTO studio_draft_idempotency(project_id,workspace_id,idempotency_key,payload_hash,revision,created_at)
		VALUES('prj_demo','ws_demo','original-draft-key',decode(repeat('01',32),'hex'),1,now())`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_projects", "studio_draft_revisions", "studio_draft_idempotency", "studio_project_drafts"}
	before := schedulerOriginalSnapshot(t, admin, tables)
	store, err := openPostgres(db)
	if err != nil {
		t.Fatal(err)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, admin, tables)) {
		t.Fatal("023 rewrote an existing draft, revision or replay receipt")
	}
	if n := outboxCount(t, admin, "SELECT (SELECT count(*) FROM studio_workflow_definitions)+(SELECT count(*) FROM studio_workflow_executions)"); n != 0 {
		t.Fatal("migration turned an old draft into an executable workflow")
	}
	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("022 binary accepted the workflow schema")
	}
}

func TestPostgresWorkflowVideoUpgrade023(t *testing.T)   { verifyWorkflowUpgrade(t, "023") }
func TestPostgresWorkflowAttemptUpgrade024(t *testing.T) { verifyWorkflowUpgrade(t, "024") }

func verifyWorkflowUpgrade(t *testing.T, version string) {
	t.Helper()
	ctx := context.Background()
	db := testDatabase(t)
	conn := deliveryAdmin(t, db)
	old := workflowOldMigrations(t, version)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing');
	 INSERT INTO studio_workflow_definitions(id,workspace_id,project_id,version) VALUES('wfd_old','ws_demo','prj_demo',1)`); err != nil {
		t.Fatal(err)
	}
	graph, models := workflowStoreGraph(t)
	selectedStatus := "COMPLETED"
	expectedVideoIntents := 0
	if version == "024" {
		catalog := defaultCatalogConfig(Config{Mode: "demo"})
		for i := range catalog.Models {
			if catalog.Models[i].Kind == "video" {
				catalog.Models[i].Capabilities.VideoFirstFrameSupported = true
			}
		}
		c, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: writeCatalogFixture(t, catalog)})
		if err != nil {
			t.Fatal(err)
		}
		graph = workflowVideoGraph(&App{catalog: c})
		models["video"] = c.Entries["studio-video"].Model
		selectedStatus = "VIDEO_READY"
		expectedVideoIntents = 2
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_workflow_versions(definition_id,version,workspace_id,project_id,graph)
	 VALUES('wfd_old',1,'ws_demo','prj_demo',$1)`, graph); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, status string
		selected   any
	}{
		{"wf_pending", "WAITING_APPROVAL", nil},
		{"wf_completed", selectedStatus, WorkflowCandidate{AssetID: "old-asset", ArtifactID: "old-artifact", TaskID: "old-task", Revision: 2, ContentVersion: "original"}},
	} {
		if _, err = tx.Exec(ctx, `INSERT INTO studio_workflow_executions(id,workspace_id,project_id,definition_id,definition_version,input,models,provider_mode,status,selected)
		 VALUES($1,'ws_demo','prj_demo','wfd_old',1,'original input',$2,'demo',$3,$4)`, row.id, models, row.status, row.selected); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO studio_workflow_intents(execution_id,kind,child_key) VALUES($1,'text',$1 || '_text'),($1,'image',$1 || '_image')`, row.id); err != nil {
			t.Fatal(err)
		}
	}
	if version == "024" {
		if _, err = tx.Exec(ctx, `INSERT INTO studio_workflow_intents(execution_id,kind,child_key) SELECT id,'video',id||'_video' FROM studio_workflow_executions`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_workflow_receipts(workspace_id,project_id,operation,operation_key,request_hash,resource_id,version)
	VALUES('ws_demo','prj_demo','confirm','previous-action',decode(repeat('01',32),'hex'),'wf_completed',4)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_workflow_definitions", "studio_workflow_versions", "studio_workflow_intents", "studio_workflow_receipts"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	// Preserve every 023 field, including timestamps and the original choice.
	originalRows := func() string {
		t.Helper()
		var rows string
		if err := conn.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(e)-ARRAY['video_task_id','video_artifacts','source_workflow_id','source_version','from_node','attempt','graph_override','inherited_text_run_id','inherited_image_task_id'] ORDER BY id)::text FROM studio_workflow_executions e`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := originalRows()
	store, err := openPostgres(db)
	if err != nil {
		t.Fatal(err)
	}
	store.close()
	if rows != originalRows() || !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatalf("upgrade from %s changed workflow metadata, selection or original receipt", version)
	}
	if outboxCount(t, conn, "SELECT count(*) FROM studio_workflow_executions WHERE video_task_id IS NOT NULL OR video_artifacts <> '[]'") != 0 || outboxCount(t, conn, "SELECT count(*) FROM studio_workflow_intents WHERE kind='video'") != expectedVideoIntents {
		t.Fatal("migration retroactively scheduled a video")
	}
	if n := outboxCount(t, conn, "SELECT count(*) FROM studio_workflow_executions WHERE attempt<>1 OR source_workflow_id IS NOT NULL OR from_node IS NOT NULL OR inherited_text_run_id IS NOT NULL OR inherited_image_task_id IS NOT NULL"); n != 0 {
		t.Fatal("025 backfilled execution or inheritance into old workflows")
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatalf("old %s writer accepted the new format", version)
	}
}

const workflowTestText = "a lit lighthouse above the frozen sea"

type workflowIntegrationFixture struct {
	cfg       Config
	chat      *openAIChat
	chatCalls atomic.Int32
	media     *deliveryHTTPFixture
	gateway   *httptest.Server
	mu        sync.Mutex
	prompts   []string
}

func newWorkflowIntegrationFixture(t *testing.T) *workflowIntegrationFixture {
	t.Helper()
	f := &workflowIntegrationFixture{media: newDeliveryHTTPFixture(t, -1)}
	reviewer := newReviewDispatchHTTPFixture(t, "")
	f.cfg = Config{
		DatabaseURL: testDatabase(t), AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(),
		WebDir: filepath.Join("..", "..", "apps", "workbench", "dist"), PollInterval: 15 * time.Millisecond,
		Moderation: executionModerationConfig(t, nil), MediaModeration: reviewer.config(t),
		MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()},
	}
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == imageGenerationEndpoint {
			raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
			_ = r.Body.Close()
			var body struct{ Prompt string }
			if err != nil || len(raw) > maxRequestBody || json.Unmarshal(raw, &body) != nil || body.Prompt == "" {
				http.Error(w, "invalid fixture image request", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.prompts = append(f.prompts, body.Prompt)
			f.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		f.media.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.gateway.Close)
	chatHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		f.chatCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\ndata: [DONE]\n\n", workflowTestText)
	}))
	t.Cleanup(chatHTTP.Close)
	var err error
	f.chat, err = newOpenAIChat(chatHTTP.URL+"/v1", "workflow-local-fixture", "fixture-chat")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *workflowIntegrationFixture) open(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	cfg := f.cfg
	cfg.mediaProvider = deliveryTestProvider(t, f.gateway, cfg.AssetDir, cfg.AssetStorage, 1)
	a, server := testApp(t, cfg)
	a.chat = f.chat
	return a, server
}

func workflowFixtureGraph(a *App) WorkflowGraph {
	textModel := a.catalog.Entries["studio-chat"].Model
	imageModel := a.catalog.Entries["studio-image"].Model
	return WorkflowGraph{Name: "Illustrated scene", TemplateID: "text-image-approval", TemplateVersion: "1",
		Nodes: []WorkflowNode{
			{ID: "script", Kind: "text", Title: "Scene", Prompt: "Describe the scene", ModelID: textModel.ID, ModelVersion: textModel.Version},
			{ID: "picture", Kind: "image", Title: "Illustration", Prompt: "Light the scene", ModelID: imageModel.ID, ModelVersion: imageModel.Version, Parameters: &TaskParameters{AspectRatio: "1:1", Count: 2}},
			{ID: "pick", Kind: "approval", Title: "Select"},
		},
		Edges: []WorkflowEdge{{From: "script", FromPort: "text", To: "picture", ToPort: "prompt"}, {From: "picture", FromPort: "images", To: "pick", ToPort: "images"}}}
}

// Restore a single isolated schema and both owned directories at the same
// stopped backup point. The restored service must use the original delivery key.
func restoreWorkflowFixture(t *testing.T, f *workflowIntegrationFixture, extraTables ...string) {
	t.Helper()
	ctx := context.Background()
	bin := os.Getenv("TEST_POSTGRES_BIN_DIR")
	if bin == "" {
		t.Skip("TEST_POSTGRES_BIN_DIR required for real workflow pg_dump/restore")
	}
	u, err := url.Parse(f.cfg.DatabaseURL)
	if err != nil || !strings.HasPrefix(u.Query().Get("search_path"), "studio_test_") {
		t.Fatal("expected a private test schema")
	}
	source := deliveryAdmin(t, f.cfg.DatabaseURL)
	tables := []string{"studio_workflow_definitions", "studio_workflow_versions", "studio_workflow_executions", "studio_workflow_intents", "studio_workflow_receipts", "studio_library_entries", "studio_storage_identity"}
	tables = append(tables, extraTables...)
	before := schedulerOriginalSnapshot(t, source, tables)
	dir := t.TempDir()
	archive := filepath.Join(dir, "workflow.dump")
	deliveryPostgresTool(t, bin, "pg_dump", f.cfg.DatabaseURL, "--no-password", "--no-owner", "--no-acl", "--format=custom", "--schema="+u.Query().Get("search_path"), "--file="+archive)
	newAssets, newRefs := t.TempDir(), t.TempDir()
	deliveryCopyPrivateTree(t, f.cfg.AssetDir, newAssets)
	deliveryCopyPrivateTree(t, f.cfg.ReferenceDir, newRefs)
	keyFile := filepath.Join(dir, "private-delivery.key")
	if err := os.WriteFile(keyFile, []byte(f.cfg.MediaDelivery.EncryptionKey), 0600); err != nil {
		t.Fatal(err)
	}
	name := newID("studio_workflow_restore")
	if _, err = source.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := source.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("could not clean up the isolated restore database", err)
		}
	})
	u.Path = "/" + name
	f.cfg.DatabaseURL = u.String()
	deliveryPostgresTool(t, bin, "pg_restore", f.cfg.DatabaseURL, "--no-password", "--no-owner", "--no-acl", "--exit-on-error", "--dbname="+name, archive)
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, deliveryAdmin(t, f.cfg.DatabaseURL), tables)) {
		t.Fatal("restore changed workflow versions, child identities, receipts or asset ownership")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.AssetDir, f.cfg.ReferenceDir, f.cfg.MediaDelivery.EncryptionKey = newAssets, newRefs, string(key)
}

func TestPostgresWorkflowBrowser(t *testing.T) {
	if os.Getenv("WORKFLOW_BROWSER_TEST") != "1" {
		t.Skip("set WORKFLOW_BROWSER_TEST=1 with built web assets and local Chromium")
	}
	f := newWorkflowIntegrationFixture(t)
	_, s := f.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/workflow-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "WORKFLOW_BROWSER_URL="+s.URL)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("local browser workflow failed: %v\n%s", err, raw)
	}
	t.Log(strings.TrimSpace(string(raw)))
	f.media.mu.Lock()
	defer f.media.mu.Unlock()
	if f.chatCalls.Load() != 1 || f.media.count != 1 {
		t.Fatal("browser did not complete exactly one text and one media generation")
	}
}

func TestPostgresWorkflowApprovalAfterBackupRestore(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, server := f.open(t)
	client := authClient(t)
	const email = "workflow-owner@example.test"
	owner := registerFixture(t, client, server.URL, email)
	project := owner.Project.ID
	prefix := "/api/v1/projects/" + project
	defs, runs := prefix+"/workflow-definitions", prefix+"/workflow-runs"
	base := server.URL
	call := func(method, path, key string, body any, want int) []byte {
		t.Helper()
		code, raw, _ := authRequest(t, client, base, method, path, key, project, body)
		if code != want {
			t.Fatalf("%s %s: HTTP %d, wanted %d: %s", method, path, code, want, raw)
		}
		return raw
	}
	graph := workflowFixtureGraph(a)
	def := decode[WorkflowDefinition](t, call("POST", defs, "workflow-definition-1", map[string]any{"expected_version": 0, "graph": graph}, 201))
	start := map[string]any{"definition_id": def.ID, "definition_version": def.Version, "input": "a winter night"}
	run := decode[WorkflowExecution](t, call("POST", runs, "workflow-start-1", start, 201))
	path := runs + "/" + run.ID
	waitFor := func(status string) workflowDetail {
		t.Helper()
		var d workflowDetail
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			d = decode[workflowDetail](t, call("GET", path, "", nil, 200))
			if d.Status == status {
				return d
			}
			if d.Status == "FAILED" || d.Status == "REVIEW" {
				t.Fatalf("workflow stopped at %s: %s", d.Status, d.ErrorCode)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("workflow stopped at %s, wanted %s", d.Status, status)
		return d
	}
	graph.Nodes[1].Prompt = "future version instruction"
	later := decode[WorkflowDefinition](t, call("POST", defs, "workflow-definition-2", map[string]any{"expected_version": def.Version, "definition_id": def.ID, "graph": graph}, 201))
	old := decode[WorkflowDefinition](t, call("GET", defs+"/receipt", "workflow-definition-1", nil, 200))
	if later.Version != 2 || old.Version != 1 || old.Graph.Nodes[1].Prompt != "Light the scene" || run.DefinitionVersion != old.Version {
		t.Fatal("later edits changed a frozen definition or its original receipt")
	}
	if d := waitFor("TEXT_READY"); d.TextRunID != "" || f.chatCalls.Load() != 0 {
		t.Fatal("starting or reading workflow invoked generation")
	}
	textBody := map[string]any{"expected_version": run.Version}
	call("POST", path+"/nodes/script/execute", "workflow-text-step", textBody, 200)
	d := waitFor("IMAGE_READY")
	if d.TextRun == nil || d.TextRun.Output != workflowTestText || d.TextRun.Moderation == nil || d.TextRun.Moderation.State != "ALLOWED" {
		t.Fatal("image became runnable without approved child text")
	}
	quotedInput := decode[workflowInput](t, call("GET", path+"/nodes/picture/input", "", nil, 200))
	if quotedInput.Task == nil || quotedInput.Task.Inputs.Prompt != "Light the scene\n\n"+workflowTestText {
		t.Fatal("image input did not use the approved text and original revision")
	}
	call("POST", path+"/nodes/picture/execute", "workflow-image-step", map[string]any{"expected_version": d.Version}, 200)
	d = waitFor("WAITING_APPROVAL")
	if len(d.Candidates) != 2 || d.ImageTask == nil || d.ImageTask.Moderation == nil || d.ImageTask.Moderation.State != "ALLOWED" {
		t.Fatal("approved private candidates missing")
	}
	f.mu.Lock()
	sent := append([]string(nil), f.prompts...)
	f.mu.Unlock()
	if len(sent) != 1 || sent[0] != quotedInput.Task.Inputs.Prompt || f.chatCalls.Load() != 1 {
		t.Fatal("actual media POST did not use exactly the frozen approved input")
	}
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	for until := time.Now().Add(5 * time.Second); outboxCount(t, admin, "SELECT count(*) FROM studio_workflow_executions WHERE id=$1 AND status='WAITING_APPROVAL' AND jsonb_array_length(candidates)=2", run.ID) != 1; {
		if time.Now().After(until) {
			t.Fatal("waiting status and original candidate set were not durable before confirmation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	firstDetail := d
	chosen := d.Candidates[1]
	download := "/api/v1/library/" + chosen.AssetID + "/content"
	if got := call("GET", download, "", nil, 200); !bytes.Equal(got, f.media.image) {
		t.Fatal("selected asset differs from the source PNG")
	}
	outsider := authClient(t)
	registerFixture(t, outsider, base, "workflow-other@example.test")
	for _, resource := range []string{path, defs + "/" + def.ID + "/versions/1", path + "/nodes/picture/receipt", download} {
		if code, _, _ := authRequest(t, outsider, base, "GET", resource, "workflow-image-step", project, nil); code != 404 {
			t.Fatalf("another workspace was allowed to read %s: %d", resource, code)
		}
	}
	a.Close()
	server.Close()
	restoreWorkflowFixture(t, f)
	_, restored := f.open(t)
	base = restored.URL
	client = authClient(t)
	call("POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	d = waitFor("WAITING_APPROVAL")
	if d.TextRunID != firstDetail.TextRunID || d.ImageTaskID != firstDetail.ImageTaskID || !reflect.DeepEqual(d.Candidates, firstDetail.Candidates) || !bytes.Equal(call("GET", download, "", nil, 200), f.media.image) {
		t.Fatal("restored project lost an original child, candidate, version or private file")
	}
	body := map[string]any{"expected_version": d.Version, "asset_id": chosen.AssetID, "revision": chosen.Revision}
	confirmed := decode[workflowDetail](t, call("POST", path+"/nodes/pick/confirm", "workflow-confirm", body, 200))
	replay := decode[workflowDetail](t, call("GET", path+"/nodes/pick/receipt", "workflow-confirm", nil, 200))
	if confirmed.Status != "COMPLETED" || replay.Selected == nil || *replay.Selected != chosen || chosen.ContentVersion == "" {
		t.Fatal("confirmation receipt differs from the selected restored asset")
	}
	code, _, headers := authRequest(t, client, base, "POST", path+"/nodes/pick/confirm", "workflow-confirm", project, body)
	if code != 200 || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("original confirmation did not replay", code)
	}
	f.media.mu.Lock()
	defer f.media.mu.Unlock()
	if f.chatCalls.Load() != 1 || f.media.count != 1 {
		t.Fatal("restore, reread or confirmation repeated a model request")
	}
}

func TestPostgresWorkflowChildAcceptanceLostAck(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	ctx := context.Background()
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	app, s := f.open(t)
	client := authClient(t)
	owner := registerFixture(t, client, s.URL, "workflow-uncertain@example.test")
	project := owner.Project.ID
	base := "/api/v1/projects/" + project
	code, raw, _ := authRequest(t, client, s.URL, "POST", base+"/workflow-definitions", newID("save"), project,
		map[string]any{"expected_version": 0, "graph": workflowFixtureGraph(app)})
	if code != 201 {
		t.Fatal("definition not accepted", code)
	}
	def := decode[WorkflowDefinition](t, raw)
	code, raw, _ = authRequest(t, client, s.URL, "POST", base+"/workflow-runs", newID("start"), project,
		map[string]any{"definition_id": def.ID, "definition_version": 1, "input": "one attempt"})
	if code != 201 {
		t.Fatal("workflow not accepted", code)
	}
	flow := decode[WorkflowExecution](t, raw)
	path := base + "/workflow-runs/" + flow.ID + "/nodes/script"
	// Block the exact transaction that links the child to the workflow. A
	// receipt for intent preparation alone would not prove child acceptance.
	if _, err := admin.Exec(ctx, `CREATE FUNCTION hold_workflow_child() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(912327,23); RETURN NEW; END $$;
		CREATE TRIGGER hold_workflow_child BEFORE UPDATE OF text_run_id ON studio_workflow_executions
		FOR EACH ROW WHEN (OLD.text_run_id IS NULL AND NEW.text_run_id IS NOT NULL) EXECUTE FUNCTION hold_workflow_child()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912327,23)"); err != nil {
		t.Fatal(err)
	}
	key := newID("workflow-action")
	body := map[string]any{"expected_version": flow.Version}
	result := make(chan int, 1)
	go func() {
		status, _, _ := authRequest(t, client, s.URL, "POST", path+"/execute", key, project, body)
		result <- status
	}()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, result)
	var child string
	if err := admin.QueryRow(ctx, "SELECT text_run_id FROM studio_workflow_executions WHERE id=$1 AND status='TEXT_ACTIVE'", flow.ID).Scan(&child); err != nil || child == "" {
		t.Fatal("child and parent link did not commit together", err)
	}
	app.Close()
	s.Close()
	f.cfg.DatabaseURL = direct
	_, restarted := f.open(t)
	code, raw, _ = authRequest(t, client, restarted.URL, "GET", path+"/receipt", key, project, nil)
	receipt := decode[workflowDetail](t, raw)
	if code != 200 || receipt.TextRunID != child || receipt.TextRun == nil || receipt.TextRun.Status != "INTERRUPTED" || receipt.ImageTaskID != "" {
		t.Fatal("original action receipt did not identify the interrupted child", code)
	}
	code, _, headers := authRequest(t, client, restarted.URL, "POST", path+"/execute", key, project, body)
	if code != 200 || headers.Get("Idempotency-Replayed") != "true" || f.chatCalls.Load() != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_runs WHERE project_id=$1", project) != 1 {
		t.Fatal("original action repeated an uncertain child admission", code)
	}
}

func verifyWorkflowLostAck(t *testing.T, ctx context.Context, barrier pgx.Tx, armed *atomic.Uint32, dropped <-chan struct{}, result <-chan int) {
	t.Helper()
	var err error
	var pid uint32
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until) && pid == 0; {
		if err = barrier.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE pg_backend_pid()=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if pid == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("target workflow transaction never waited at the test barrier")
	}
	armed.Store(pid)
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("targeted COMMIT acknowledgement was not lost")
	}
	select {
	case code := <-result:
		if code != 503 {
			t.Fatal("uncertain acceptance was reported as a definite result", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("uncertain request did not return")
	}
}
