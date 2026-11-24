package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPanelVideoPromptAndQuoteDomains(t *testing.T) {
	d := comicTestInput(0, "").Document
	d.Characters = append(d.Characters, ComicCard{ID: "unused", Name: "unused private character", Description: "private"})
	d.Panels[0].AssetID = "private-image-never-sent"
	got, e := panelVideoPrompt(d, "dawn")
	if e != nil || strings.Contains(got, "private") || !strings.Contains(got, "角色：林：旧外套") || !strings.Contains(got, "动作：走向码头。") {
		t.Fatal("selected text was not frozen independently of other people, panels or images", e)
	}
	d.Story = strings.Repeat("字", 16000)
	_, e = panelVideoPrompt(d, "dawn")
	requireOperation(t, e, "PANEL_VIDEO_PROMPT_LIMIT")
	var none *panelVideoGuard
	in := TaskInput{ProjectID: "project", Kind: "video", ModelID: "model"}
	if none.quoteHash(in) != ledgerTaskRequestHash(in) {
		t.Fatal("ordinary quote digest changed")
	}
	p := &principal{User: &User{ID: "actor"}, Workspace: Workspace{ID: "ws"}}
	g := newPanelVideoGuard(p, panelVideoSource{"project", 1, "dawn"}, "key-original", panelVideoRequest{})
	g.attempt = 1
	h := g.quoteHash(in)
	if h == none.quoteHash(in) {
		t.Fatal("ordinary and scoped quotes collide")
	}
	for _, mutate := range []func(*panelVideoGuard){func(v *panelVideoGuard) { v.actor = "other" }, func(v *panelVideoGuard) { v.source.PanelID = "night" }, func(v *panelVideoGuard) { v.source.ComicVersion++ }, func(v *panelVideoGuard) { v.attempt++ }, func(v *panelVideoGuard) { v.keyHash[0] ^= 1 }} {
		v := *g
		mutate(&v)
		if v.quoteHash(in) == h {
			t.Fatal("price evidence lost source or approval identity")
		}
	}
}

type panelHTTPFixture struct {
	f             *workflowIntegrationFixture
	a             *App
	s             *httptest.Server
	c             *http.Client
	identity      testIdentity
	project, path string
	options       panelVideoOptions
}

func openPanelHTTPFixture(t *testing.T, f *workflowIntegrationFixture) panelHTTPFixture {
	t.Helper()
	a, s := f.open(t)
	c := authClient(t)
	id := registerFixture(t, c, s.URL, "panel-owner@example.test")
	m := a.catalog.Entries["studio-video"].Model
	return panelHTTPFixture{f: f, a: a, s: s, c: c, identity: id, project: id.Project.ID, path: "/api/v1/projects/" + id.Project.ID + "/comic/versions/1/panels/dawn/videos",
		options: panelVideoOptions{ModelID: m.ID, ModelVersion: m.Version, Parameters: TaskParameters{AspectRatio: "16:9", Seconds: 5, Count: 1}}}
}

func (f panelHTTPFixture) confirm(t *testing.T) {
	t.Helper()
	p, e := f.a.currentPrincipal(operationsRequest(t, f.c, f.s))
	if e != nil {
		t.Fatal(e)
	}
	d := comicTestInput(0, "")
	d.Document.Panels[1] = d.Document.Panels[0]
	d.Document.Panels[1].ID = "meeting" // Two sources intentionally have identical model text.
	if _, _, e = f.a.saveComic(p, f.project, d, newID("save")); e != nil {
		t.Fatal(e)
	}
	f.call(t, "POST", f.path+"/quote", newID("key"), f.options, 409)
	if _, _, e = f.a.confirmComic(p, f.project, 1, newID("confirm")); e != nil {
		t.Fatal(e)
	}
}
func (f panelHTTPFixture) call(t *testing.T, method, path, key string, body any, status int) []byte {
	t.Helper()
	code, raw, _ := authRequest(t, f.c, f.s.URL, method, path, key, f.project, body)
	if code != status {
		t.Fatalf("panel %s got %d want %d: %s", method, code, status, raw)
	}
	return raw
}

func TestPostgresPanelVideoBoundQuotesAndSingleAdmission(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	f.media.blockSlot = 0
	defer f.media.unblock()
	observed := make(chan string, 4)
	originalHandler := f.gateway.Config.Handler
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == videoGenerationEndpoint {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			r.Body.Close()
			var model struct{ Prompt string }
			if err != nil || json.Unmarshal(raw, &model) != nil {
				t.Error("invalid model request")
				w.WriteHeader(400)
				return
			}
			select {
			case observed <- model.Prompt:
			default:
				t.Error("unexpected model POST")
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		originalHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.gateway.Close)
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "price.json")
	if e := os.WriteFile(f.cfg.BillingPriceFile, []byte(`{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-video","kind":"video","operation":"generate","preset_id":"preset-1","artifact_micros":"500000"}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	w := openPanelHTTPFixture(t, f)
	w.confirm(t)
	httpCredit(t, w.a, w.identity.Workspace.ID, 5_000_000)
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	key := newID("accept")
	preview := func(path, key string) panelVideoPreview {
		return decode[panelVideoPreview](t, w.call(t, "POST", path+"/quote", key, w.options, 200))
	}
	q := preview(w.path, key)
	if q.Attempt != 1 || !q.BillingEnabled || q.Quote == nil || q.Quote.ReserveMicros != 500000 || preview(w.path, key).Quote.ID != q.Quote.ID {
		t.Fatal("preview did not recover the bound original paid quote")
	}
	otherPath := strings.Replace(w.path, "/dawn/", "/meeting/", 1)
	other := preview(otherPath, newID("other"))
	if q.Prompt != other.Prompt || q.Quote.ID == other.Quote.ID {
		t.Fatal("fixture needs equal generated text for separate panel quotes")
	}
	in := panelVideoRequest{panelVideoOptions: w.options, ExpectedAttempt: 1, QuoteID: other.Quote.ID}
	w.call(t, "POST", w.path, key, in, 409)
	in.QuoteID = q.Quote.ID
	direct := TaskInput{ProjectID: w.project, Kind: "video", Operation: "generate", ModelID: w.options.ModelID, ModelVersion: w.options.ModelVersion, Parameters: q.Parameters, QuoteID: q.Quote.ID}
	direct.Inputs.Prompt = q.Prompt
	w.call(t, "POST", "/api/v1/tasks", newID("direct"), direct, 409)
	if n := outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations"); n != 0 {
		t.Fatal("rejected requests consumed a quote")
	}
	// Occupy the actual outbox inside this acceptance transaction, after the
	// reservation was prepared; rollback restores the queue and all other rows.
	_, err := db.Exec(t.Context(), `CREATE FUNCTION panel_video_backpressure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 UPDATE studio_outbox_limits SET pending=10000 WHERE singleton; RETURN NEW; END $$;
	 CREATE TRIGGER panel_video_backpressure AFTER INSERT ON studio_tasks FOR EACH ROW EXECUTE FUNCTION panel_video_backpressure()`)
	if err != nil {
		t.Fatal(err)
	}
	w.call(t, "POST", w.path, key, in, 503)
	w.call(t, "GET", w.path+"/receipt", key, nil, 404)
	if outboxCount(t, db, "SELECT (SELECT count(*) FROM studio_tasks)+(SELECT count(*) FROM studio_comic_panel_videos)+(SELECT count(*) FROM studio_ledger_reservations)") != 0 ||
		outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", q.Quote.ID) != 1 {
		t.Fatal("rolled-back binding, child or charge leaked")
	}
	if _, err = db.Exec(t.Context(), "DROP TRIGGER panel_video_backpressure ON studio_tasks"); err != nil {
		t.Fatal(err)
	}
	counts := make(chan int, 4)
	for range 4 {
		go func() { code, _, _ := authRequest(t, w.c, w.s.URL, "POST", w.path, key, w.project, in); counts <- code }()
	}
	created := 0
	for range 4 {
		switch code := <-counts; code {
		case 201:
			created++
		case 200:
		default:
			t.Fatalf("duplicate same request got %d", code)
		}
	}
	if created != 1 {
		t.Fatalf("first acceptances=%d", created)
	}
	original := decode[panelVideoResult](t, w.call(t, "GET", w.path+"/receipt", key, nil, 200))
	if original.Attempt != 1 || original.TaskID == "" || original.QuoteID != q.Quote.ID || original.ComicVersion != 1 {
		t.Fatal("receipt lost source, task or quote")
	}
	w.call(t, "GET", otherPath+"/receipt", key, nil, 404)
	w.call(t, "POST", otherPath, key, in, 409)
	select {
	case <-f.media.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("original media task did not reach file delivery")
	}
	if msg := w.call(t, "POST", w.path+"/quote", newID("premature"), w.options, 409); !bytes.Contains(msg, []byte("PANEL_VIDEO_UNRESOLVED")) {
		t.Fatal("unfinished known handle permitted a second attempt")
	}
	f.media.unblock()
	final := laneTaskStatus(t, w.a, original.TaskID, "SUCCEEDED")
	if final.Prompt != q.Prompt || final.Billing == nil || final.Billing.State != "SETTLED" || len(final.Artifacts) != 1 {
		t.Fatal("original text, billing or delivery missing")
	}
	nextKey := newID("redo")
	second := preview(w.path, nextKey)
	if second.Attempt != 2 || second.Quote.ID == q.Quote.ID {
		t.Fatal("explicit redo reused former quote")
	}
	in.ExpectedAttempt = 2
	w.call(t, "POST", w.path, nextKey, in, 409)
	in.QuoteID = second.Quote.ID
	replacement := decode[panelVideoResult](t, w.call(t, "POST", w.path, nextKey, in, 201))
	if replacement.TaskID == original.TaskID {
		t.Fatal("new attempt reused old task")
	}
	laneTaskStatus(t, w.a, replacement.TaskID, "SUCCEEDED")
	page := decode[panelVideoPage](t, w.call(t, "GET", w.path+"?limit=1", "", nil, 200))
	if len(page.Items) != 1 || page.Items[0].TaskID != replacement.TaskID || page.Next == nil || *page.Next != 2 {
		t.Fatal("two attempts not separately pageable")
	}
	page = decode[panelVideoPage](t, w.call(t, "GET", w.path+"?limit=1&before_attempt=2", "", nil, 200))
	if len(page.Items) != 1 || page.Items[0].TaskID != original.TaskID || page.Next != nil {
		t.Fatal("lost original paged attempt")
	}
	if outboxCount(t, db, "SELECT count(*) FROM studio_comic_panel_videos") != 2 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", other.Quote.ID) != 1 {
		t.Fatal("unused quote consumed or binding duplicated")
	}
	f.media.mu.Lock()
	posts := f.media.count
	f.media.mu.Unlock()
	if posts != 2 {
		t.Fatalf("real gateway POST count=%d expected two approved attempts", posts)
	}
	for range 2 {
		select {
		case text := <-observed:
			if text != q.Prompt {
				t.Fatal("gateway received a different frozen prompt")
			}
		default:
			t.Fatal("missing original gateway POST")
		}
	}
}

func TestPostgresPanelVideoLostCommitReceipt(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	w := openPanelHTTPFixture(t, f)
	w.confirm(t)
	key := newID("unknown")
	pv := decode[panelVideoPreview](t, w.call(t, "POST", w.path+"/quote", key, w.options, 200))
	if pv.BillingEnabled || pv.Quote != nil {
		t.Fatal("disabled billing fabricated a quote")
	}
	ctx := context.Background()
	_, e := admin.Exec(ctx, `CREATE FUNCTION hold_panel_video() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(912329,38); RETURN NEW; END $$;
	 CREATE TRIGGER hold_panel_video BEFORE INSERT ON studio_comic_panel_videos FOR EACH ROW EXECUTE FUNCTION hold_panel_video()`)
	if e != nil {
		t.Fatal(e)
	}
	barrier, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer barrier.Rollback(ctx)
	if _, e = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912329,38)"); e != nil {
		t.Fatal(e)
	}
	in := panelVideoRequest{panelVideoOptions: w.options, ExpectedAttempt: pv.Attempt}
	ch := make(chan int, 1)
	go func() { code, _, _ := authRequest(t, w.c, w.s.URL, "POST", w.path, key, w.project, in); ch <- code }()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, ch)
	w.s.Close()
	w.a.Close()
	f.cfg.DatabaseURL = direct
	// Change the video directory's public version while preserving accepted task evidence.
	catalog := defaultCatalogConfig(Config{Mode: "demo"})
	for i := range catalog.Models {
		if catalog.Models[i].Kind == "video" {
			off := false
			catalog.Models[i].Enabled = &off
		}
	}
	f.cfg.ModelCatalogFile = writeCatalogFixture(t, catalog)
	w.a, w.s = f.open(t)
	one := decode[panelVideoResult](t, w.call(t, "GET", w.path+"/receipt", key, nil, 200))
	two := decode[panelVideoResult](t, w.call(t, "POST", w.path, key, in, 200))
	if one.TaskID == "" || two.TaskID != one.TaskID || outboxCount(t, admin, "SELECT count(*) FROM studio_comic_panel_videos") != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_tasks") != 1 {
		t.Fatal("unknown COMMIT was not recovered by original receipt")
	}
}

func TestPostgresPanelVideoUpgrade037PreservesExistingCanvasAndTasks(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	admin := deliveryAdmin(t, db)
	old := workflowOldMigrations(t, "037")
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_before','canvas-before@example.test','原作者',decode('01','hex'));
	 INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','原工作区');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','旧项目');
	 INSERT INTO studio_canvas_versions(project_id,workspace_id,revision,created_by,key_hash,request_hash,title,node_count,edge_count,document)
	 VALUES('prj_demo','ws_demo',1,'usr_before',decode(repeat('01',32),'hex'),decode(repeat('02',32),'hex'),'旧画布',1,0,
	 '{"title":"旧画布","nodes":[{"id":"n1","title":"原镜头","x":0,"y":0}],"edges":[],"groups":[]}');
	 INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
	 VALUES('task_before','ws_demo','prj_demo',
	 '{"id":"task_before","workspace_id":"ws_demo","project_id":"prj_demo","provider_mode":"demo","kind":"video","status":"SUCCEEDED","version":3,"progress":100}',
	 '{"project_id":"prj_demo","kind":"video","model_id":"original-video"}','null',clock_timestamp())`)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tables := []string{"studio_canvas_versions", "studio_tasks", "studio_task_schedule", "studio_projects", "studio_comic_versions", "studio_ledger_quotes", "studio_ledger_reservations"}
	before := schedulerOriginalSnapshot(t, admin, tables)
	store, e := openPostgres(db)
	if e != nil {
		t.Fatal(e)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, admin, tables)) || outboxCount(t, admin, "SELECT count(*) FROM studio_comic_panel_videos") != 0 {
		t.Fatal("038 changed existing business data or inferred a storyboard source")
	}
	tx, e = admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than") {
		t.Fatal("037 writer accepted the newer format")
	}
}
