package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func canvasFixtureInput(revision uint64, n int) CanvasSaveInput {
	d := CanvasDocument{Title: "自由构图", Groups: []CanvasGroup{{ID: "g", Title: "第一组", Collapsed: true}}, Edges: []CanvasEdge{}, Nodes: []CanvasNode{}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("n%d", i)
		d.Nodes = append(d.Nodes, CanvasNode{ID: id, Title: "构图 " + id, GroupID: "g", X: 10 * i, Y: -i})
		if i > 0 {
			d.Edges = append(d.Edges, CanvasEdge{fmt.Sprintf("e%d", i), d.Nodes[i-1].ID, id})
		}
	}
	return CanvasSaveInput{ExpectedRevision: &revision, Document: d}
}
func TestCanvasGeometryAndInputBoundaries(t *testing.T) {
	for _, n := range []int{500, 2000} {
		in := canvasFixtureInput(0, n)
		for i := 0; i < n-2; i++ {
			in.Document.Edges = append(in.Document.Edges, CanvasEdge{fmt.Sprintf("skip%d", i), in.Document.Nodes[i].ID, in.Document.Nodes[i+2].ID})
		}
		for i := 0; i < 3; i++ {
			in.Document.Edges = append(in.Document.Edges, CanvasEdge{fmt.Sprintf("link%d", i), in.Document.Nodes[i].ID, in.Document.Nodes[i+3].ID})
		}
		if len(in.Document.Edges) != n*2 || validateCanvas(in) != nil {
			t.Fatal("complete reference graph rejected", n)
		}
	}
	for _, mutate := range []func(*CanvasDocument){
		func(d *CanvasDocument) { d.Edges[0].To = "missing" }, func(d *CanvasDocument) { d.Edges[0].To = d.Edges[0].From }, func(d *CanvasDocument) { e := d.Edges[0]; e.ID = "again"; d.Edges = append(d.Edges, e) },
		func(d *CanvasDocument) { d.Nodes[1].ID = d.Nodes[0].ID }, func(d *CanvasDocument) { d.Nodes[0].GroupID = "missing" }, func(d *CanvasDocument) { d.Nodes[0].X = 1000001 }, func(d *CanvasDocument) { d.Edges = nil },
	} {
		in := canvasFixtureInput(0, 3)
		mutate(&in.Document)
		if validateCanvas(in) == nil {
			t.Fatal("malformed graph accepted")
		}
	}
	for _, f := range []string{"url", "file_path", "status", "workspace_id", "content_version"} {
		in := canvasFixtureInput(0, 1)
		raw, _ := json.Marshal(in)
		raw = bytes.Replace(raw, []byte(`"nodes":[{`), []byte(`"nodes":[{"`+f+`":null,`), 1)
		r := httptest.NewRequest("PUT", "/", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		_, e := canvasDecode(httptest.NewRecorder(), r)
		requireOperation(t, e, "INVALID_JSON")
	}
}

func TestPostgresCanvasOwnerHistoryFrozenSources(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "canvas-author@example.test")
	other := authClient(t)
	guest := registerFixture(t, other, s.URL, "canvas-reader@example.test")
	prj := owner.Project.ID
	base := "/api/v1/projects/" + prj + "/canvas"
	key := newID("save")
	call := func(c *http.Client, p, m, path, k string, b any, want int) []byte {
		t.Helper()
		status, data, _ := authRequest(t, c, s.URL, m, path, k, p, b)
		if status != want {
			t.Fatalf("canvas request %s returned %d, wanted %d", m, status, want)
		}
		return data
	}
	shares := "/api/v1/projects/" + prj + "/collaboration"
	invite := decode[CollaborationReceipt](t, call(c, prj, "POST", shares+"/invitations", newID("invite"), map[string]any{"target_user_id": guest.User.ID, "role": "editor", "expected_version": 0}, 201))
	call(other, "", "POST", "/api/v1/collaboration/invitations/"+invite.InvitationID+"/accept", newID("accept"), map[string]any{"expected_version": invite.Version}, 200)
	status, data, _ := uploadReferenceFixture(t, c, s.URL, prj, newID("upload"), "画板.png", referencePNG(t, 36), false)
	if status != 201 {
		t.Fatal("approved source upload failed", status)
	}
	asset := libraryID("reference", decode[ReferenceAsset](t, data).ID, "")
	run := decode[Run](t, call(c, prj, "POST", "/api/v1/runs", newID("runkey"), RunInput{ProjectID: prj, ModelID: "studio-chat", Input: "仅本机运行的合成画布素材"}, 201))
	if got := awaitAuthRun(t, c, s.URL, run.ID); got.Status != "COMPLETED" {
		t.Fatal("fixture run did not complete")
	}
	in := canvasFixtureInput(0, 3)
	in.Document.Nodes[0].AssetID = asset
	in.Document.Nodes[0].AssetRevision = 1
	in.Document.Nodes[1].RunID = run.ID
	saved := decode[CanvasVersion](t, call(c, prj, "PUT", base, key, in, 200))
	if saved.Revision != 1 || len(saved.Sources) != 2 || saved.Sources["n1"].Status != "COMPLETED" || saved.Sources["n1"].Seq == nil || !mediaContentHash.MatchString(saved.Sources["n0"].ContentVersion) {
		t.Fatal("wrong real asset or run projection")
	}
	thumb := saved.Sources["n0"].ThumbnailURL
	original := call(c, prj, "GET", thumb, "", nil, 200)
	if _, err := png.Decode(bytes.NewReader(original)); err != nil {
		t.Fatal("thumbnail is not a decodable image")
	}
	for _, url := range []string{base, base + "/revisions", base + "/revisions/1", base + "/revisions/1/sources", base + "/receipt", thumb} {
		call(authClient(t), "", "GET", url, key, nil, 401)
		call(other, "", "GET", url, key, nil, 404)
	}
	call(other, "", "PUT", base, newID("foreign"), in, 404)
	n := uint64(1)
	moved := in
	moved.ExpectedRevision = &n
	moved.Document.Nodes = append([]CanvasNode(nil), in.Document.Nodes...)
	moved.Document.Nodes[2].X += 25
	moved.Document.Groups = []CanvasGroup{{ID: "g", Title: "第一组", Collapsed: false}}
	next := decode[CanvasVersion](t, call(c, prj, "PUT", base, newID("move"), moved, 200))
	if next.Revision != 2 || next.Document.Nodes[2].X == saved.Document.Nodes[2].X || next.Document.Groups[0].Collapsed || !reflect.DeepEqual(next.Document.Edges, saved.Document.Edges) {
		t.Fatal("move/reopen changed links or collapse state")
	}
	call(c, prj, "PUT", base, newID("stale"), moved, 409)
	call(c, prj, "PUT", base, key, moved, 409)
	lib := "/api/v1/library/" + asset
	call(c, prj, "PATCH", lib, newID("rename"), map[string]any{"expected_revision": 1, "name": "改名", "tags": []string{}}, 200)
	if !bytes.Equal(original, call(c, prj, "GET", thumb, "", nil, 200)) {
		t.Fatal("rename changed pinned original preview")
	}
	call(c, prj, "POST", lib+"/trash", newID("trash"), map[string]any{"expected_revision": 2}, 200)
	for _, path := range []string{base, base + "/revisions/1", base + "/receipt"} {
		v := decode[CanvasVersion](t, call(c, prj, "GET", path, key, nil, 200))
		if v.Sources["n0"].Available || v.Sources["n0"].ThumbnailURL != "" || v.Sources["n0"].ContentVersion != "" {
			t.Fatal("recycled source still projected")
		}
	}
	call(c, prj, "GET", thumb, "", nil, 404)
	replay := decode[CanvasVersion](t, call(c, prj, "PUT", base, key, in, 200))
	if replay.Revision != 1 || replay.Sources["n0"].Available {
		t.Fatal("replay disclosed recycled source")
	}
	another := decode[Project](t, call(c, prj, "POST", "/api/v1/projects", "", map[string]string{"name": "兄弟项目"}, 201))
	foreign := canvasFixtureInput(0, 1)
	foreign.Document.Nodes[0].RunID = run.ID
	call(c, another.ID, "PUT", "/api/v1/projects/"+another.ID+"/canvas", newID("cross"), foreign, 409)
	call(c, another.ID, "GET", base, "", nil, 404)
	hist := decode[CanvasHistory](t, call(c, prj, "GET", base+"/revisions?limit=1", "", nil, 200))
	if len(hist.Items) != 1 || hist.Items[0].Revision != 2 || hist.NextBeforeRevision != 2 {
		t.Fatal("unexpected history cursor")
	}
	hist = decode[CanvasHistory](t, call(c, prj, "GET", base+"/revisions?limit=1&before_revision=2", "", nil, 200))
	if len(hist.Items) != 1 || hist.Items[0].Revision != 1 || hist.NextBeforeRevision != 0 {
		t.Fatal("historical edit not found")
	}
	if got := f.chatCalls.Load(); got != 1 {
		t.Fatalf("saving/reopening caused %d generation calls", got)
	}
	p, err := a.currentPrincipal(operationsRequest(t, c, s))
	if err != nil {
		t.Fatal(err)
	}
	// Revoke the exact session in the database, then probe a stale principal
	// retained after authentication. Both state and receipts reauthorize in SQL.
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	if _, err = admin.Exec(context.Background(), "DELETE FROM studio_sessions WHERE id=$1", p.SessionID); err != nil {
		t.Fatal(err)
	}
	_, err = a.readCanvas(p, prj, 0, key)
	requireOperation(t, err, "AUTH_REQUIRED")
	_, _, err = a.canvasSources(p, prj, 1, "n0")
	requireOperation(t, err, "AUTH_REQUIRED")
}

func TestPostgresCanvasCompetingWritersCapacityAndOutboxRollback(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	ctx := context.Background()
	c := authClient(t)
	id := registerFixture(t, c, s.URL, "canvas-cas@example.test")
	p, err := a.currentPrincipal(operationsRequest(t, c, s))
	if err != nil {
		t.Fatal(err)
	}
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	barrier, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT id FROM studio_users WHERE id=$1 FOR UPDATE", p.User.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		in  CanvasSaveInput
		key string
		err error
	}
	results := make(chan result, 2)
	var pids []int32
	for i := range 2 {
		conn := deliveryAdmin(t, f.cfg.DatabaseURL)
		pids = append(pids, int32(conn.PgConn().PID()))
		writer := &App{config: Config{AuthMode: "required"}, store: &postgresStore{conn: conn}}
		key := newID("save")
		in := canvasFixtureInput(0, 2)
		in.Document.Nodes[0].X = i
		go func() { _, _, err := writer.saveCanvas(p, id.Project.ID, key, in); results <- result{in, key, err} }()
	}
	awaitRunLeaseWaiters(t, deliveryAdmin(t, f.cfg.DatabaseURL), pids...)
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var winner result
	wins := 0
	for range 2 {
		r := <-results
		_, readErr := a.readCanvas(p, id.Project.ID, 0, r.key)
		if r.err == nil {
			wins++
			winner = r
			if readErr != nil {
				t.Fatal(readErr)
			}
		} else {
			requireOperation(t, r.err, "CANVAS_CONFLICT")
			requireOperation(t, readErr, "CANVAS_NOT_FOUND")
		}
	}
	if wins != 1 {
		t.Fatal("racing writes did not serialize")
	}
	_, err = admin.Exec(ctx, `CREATE FUNCTION hold_canvas_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN UPDATE studio_outbox_limits SET pending=10000 WHERE singleton; RETURN NEW; END $$;
 CREATE TRIGGER hold_canvas_outbox BEFORE INSERT ON studio_audit_events FOR EACH ROW WHEN(NEW.action='canvas.saved') EXECUTE FUNCTION hold_canvas_outbox()`)
	if err != nil {
		t.Fatal(err)
	}
	key := newID("backlog")
	_, _, err = a.saveCanvas(p, id.Project.ID, key, canvasFixtureInput(1, 3))
	requireOperation(t, err, "EVENT_BACKLOG_FULL")
	_, err = a.readCanvas(p, id.Project.ID, 0, key)
	requireOperation(t, err, "CANVAS_NOT_FOUND")
	if _, err = admin.Exec(ctx, "DROP TRIGGER hold_canvas_outbox ON studio_audit_events"); err != nil {
		t.Fatal(err)
	}
	for n := uint64(1); n < canvasMaxRevision; n++ {
		if n > 1 {
			key = newID("save")
		}
		if _, _, err = a.saveCanvas(p, id.Project.ID, key, canvasFixtureInput(n, 3)); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = a.saveCanvas(p, id.Project.ID, newID("extra"), canvasFixtureInput(canvasMaxRevision, 2))
	requireOperation(t, err, "CANVAS_REVISION_LIMIT")
	v, replayed, err := a.saveCanvas(p, id.Project.ID, winner.key, winner.in)
	if err != nil || !replayed || v.Revision != 1 {
		t.Fatal("original receipt lost at capacity", err)
	}
	for _, q := range []string{"SELECT count(*) FROM studio_canvas_versions", "SELECT count(*) FROM studio_audit_events WHERE action='canvas.saved'", "SELECT count(*) FROM studio_outbox_events WHERE action='canvas.saved'"} {
		if outboxCount(t, admin, q) != canvasMaxRevision {
			t.Fatal("business, audit and Outbox do not match")
		}
	}
}

func TestPostgresCanvasLostSaveReceipt(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "canvas-ack@example.test")
	project := owner.Project.ID
	ctx := context.Background()
	_, err := admin.Exec(ctx, `CREATE FUNCTION hold_canvas_save() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(912329,37); RETURN NEW; END $$;
 CREATE TRIGGER hold_canvas_save BEFORE INSERT ON studio_canvas_versions FOR EACH ROW EXECUTE FUNCTION hold_canvas_save()`)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912329,37)"); err != nil {
		t.Fatal(err)
	}
	key := newID("save")
	base := "/api/v1/projects/" + project + "/canvas"
	in := canvasFixtureInput(0, 8)
	done := make(chan int, 1)
	go func() { code, _, _ := authRequest(t, c, s.URL, "PUT", base, key, project, in); done <- code }()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, done)
	s.Close()
	a.Close()
	f.cfg.DatabaseURL = direct
	_, again := f.open(t)
	body, _ := presentationRequest(t, c, again.URL, project, "GET", base+"/receipt", key, nil, 200)
	found := decode[CanvasVersion](t, body)
	if found.Revision != 1 || !reflect.DeepEqual(in.Document, found.Document) || outboxCount(t, admin, "SELECT count(*) FROM studio_canvas_versions") != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_audit_events WHERE action='canvas.saved'") != 1 {
		t.Fatal("unknown save did not leave exactly its original receipt and graph")
	}
}

func TestPostgresCanvasUpgrade036PreservesExistingData(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	conn := deliveryAdmin(t, db)
	old := workflowOldMigrations(t, "036")
	tx, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	// The previous standard service already created the demo workspace and project.
	// Keep their original names and rows so startup seeding is an actual no-op.
	_, e = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','原工作区');
 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','项目');
 INSERT INTO studio_project_drafts(project_id,workspace_id,revision,updated_at) VALUES('prj_demo','ws_demo',1,now());
 INSERT INTO studio_draft_revisions(project_id,workspace_id,revision,nodes,node_count,created_at)
 VALUES('prj_demo','ws_demo',1,'[{"id":"card","title":"source","note":"original note"}]',1,now());
 INSERT INTO studio_draft_idempotency(project_id,workspace_id,idempotency_key,payload_hash,revision,created_at)
 VALUES('prj_demo','ws_demo','original-save-key',decode(repeat('01',32),'hex'),1,now());`)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tables := []string{"studio_projects", "studio_project_drafts", "studio_draft_revisions", "studio_draft_idempotency", "studio_chat_ancestry", "studio_workflow_definitions", "studio_library_entries", "studio_audit_events", "studio_outbox_events"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	store, e := openPostgres(db)
	if e != nil {
		t.Fatal(e)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) || outboxCount(t, conn, "SELECT count(*) FROM studio_canvas_versions") != 0 {
		t.Fatal("037 rewrote an older document or generated an invented graph")
	}
	tx, e = conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than") {
		t.Fatal("old writer accepted the new format")
	}
}
