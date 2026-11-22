package studio

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresComicUpgrade030RetainsTimelineAndOriginalReceipt(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	conn := deliveryAdmin(t, dsn)
	old := workflowOldMigrations(t, "030")
	tx, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing workspace');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing project');
	 INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_before','before@comic.test','owner',decode('00','hex'));
	 INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_before','usr_before','工作区');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_before','ws_before','原项目');
	 INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
	 VALUES('tsk_before','ws_before','prj_before','{"provider_mode":"demo","status":"SUCCEEDED","kind":"video","artifacts":[{"id":"ast_before","kind":"video"}]}','{}','{}',now());
	 INSERT INTO studio_library_entries(id,workspace_id,project_id,source,task_id,artifact_id,provider_mode,kind,name,created_at,updated_at,content_type,byte_size,storage_info)
	 VALUES('lib_'||encode(sha256(convert_to('generated:tsk_before:ast_before','UTF8')),'hex'),'ws_before','prj_before','generated','tsk_before','ast_before','demo','video','原镜头',now(),now(),'video/mp4',321,
	  jsonb_build_object('id','ast_before','content_type','video/mp4','byte_size',321,'sha256',repeat('f',64)));
	 INSERT INTO studio_library_revisions(asset_id,project_id,workspace_id,revision,name,tags,state,updated_at)
	 SELECT id,project_id,workspace_id,revision,name,tags,state,updated_at FROM studio_library_entries WHERE task_id='tsk_before';
	 INSERT INTO studio_video_timelines(id,workspace_id,project_id,version) VALUES('vtl_'||repeat('a',32),'ws_before','prj_before',1);
	 INSERT INTO studio_video_timeline_versions(timeline_id,version,workspace_id,project_id,created_by,title,clip_count)
	 VALUES('vtl_'||repeat('a',32),1,'ws_before','prj_before','usr_before','原始剪辑',1);
	 INSERT INTO studio_video_timeline_clips(timeline_id,version,project_id,workspace_id,position,clip_id,title,in_ms,out_ms,asset_id,asset_revision,content_version,asset_info)
	 SELECT 'vtl_'||repeat('a',32),1,project_id,workspace_id,0,'original','原始镜头',125,500,id,revision,storage_info->>'sha256',storage_info FROM studio_library_entries WHERE task_id='tsk_before';
	 INSERT INTO studio_video_timeline_keys(project_id,workspace_id,actor_id,key_hash,request_hash,timeline_id,version)
	 VALUES('prj_before','ws_before','usr_before',decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),'vtl_'||repeat('a',32),1);`)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tables := []string{"studio_video_timelines", "studio_video_timeline_versions", "studio_video_timeline_clips", "studio_video_timeline_keys", "studio_library_entries", "studio_library_revisions", "studio_presentations", "studio_users", "studio_workspaces", "studio_projects", "studio_tasks", "studio_ledger_quotes", "studio_operations_grants"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	store, e := openPostgres(dsn)
	if e != nil {
		t.Fatal(e)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("031 changed old document, private source, authorization, or original receipt")
	}
	if n := outboxCount(t, conn, `SELECT (SELECT count(*) FROM studio_comics)+(SELECT count(*) FROM studio_comic_versions)+(SELECT count(*) FROM studio_comic_receipts)+(SELECT count(*) FROM studio_comic_assets)`); n != 0 {
		t.Fatal("031 created fictitious storyboard data", n)
	}
	tx, e = conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than this executable") {
		t.Fatal("030 accepted 031 database", e)
	}
}

func TestPostgresComicConcurrentFirstEditAndBoundedHistory(t *testing.T) {
	dsn := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required"})
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "comic-cas@example.test")
	project := owner.Project.ID
	p, e := a.currentPrincipal(operationsRequest(t, c, s))
	if e != nil {
		t.Fatal(e)
	}
	admin := deliveryAdmin(t, dsn)
	ctx := context.Background()
	barrier, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer barrier.Rollback(ctx)
	if _, e = barrier.Exec(ctx, "SELECT id FROM studio_users WHERE id=$1 FOR UPDATE", p.User.ID); e != nil {
		t.Fatal(e)
	}
	type result struct {
		in  ComicSaveInput
		key string
		err error
	}
	done := make(chan result, 2)
	var pids []int32
	for n := 0; n < 2; n++ {
		conn := deliveryAdmin(t, dsn)
		pids = append(pids, int32(conn.PgConn().PID()))
		writer := &App{config: Config{AuthMode: "required"}, store: &postgresStore{conn: conn}}
		in := comicTestInput(0, "")
		in.Document.Title = fmt.Sprintf("并发版 %d", n)
		key := newID("save")
		go func() { _, _, e := writer.saveComic(p, project, in, key); done <- result{in, key, e} }()
	}
	awaitRunLeaseWaiters(t, deliveryAdmin(t, dsn), pids...)
	if e = barrier.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	win := []result{}
	for n := 0; n < 2; n++ {
		r := <-done
		receipt, e := a.comicReceipt(p, project, "save", r.key)
		if r.err == nil {
			win = append(win, r)
			if e != nil || receipt.Version != 1 || receipt.Document.Title != r.in.Document.Title {
				t.Fatal("winner lost receipt")
			}
		} else {
			requireOperation(t, r.err, "COMIC_VERSION_CONFLICT")
			requireOperation(t, e, "COMIC_NOT_FOUND")
		}
	}
	if len(win) != 1 {
		t.Fatal("first creation failed to serialize")
	}
	confirmKey := newID("confirm")
	if _, _, e = a.confirmComic(p, project, 1, confirmKey); e != nil {
		t.Fatal(e)
	}
	_, _, e = a.confirmComic(p, project, 1, newID("other-confirm"))
	requireOperation(t, e, "COMIC_ALREADY_CONFIRMED")
	for v := uint64(1); v < comicVersionLimit; v++ {
		edit := comicTestInput(v, "")
		if _, _, e = a.saveComic(p, project, edit, newID("save")); e != nil {
			t.Fatal("append version", v+1, e)
		}
	}
	_, _, e = a.saveComic(p, project, comicTestInput(comicVersionLimit, ""), newID("save"))
	requireOperation(t, e, "COMIC_VERSION_LIMIT")
	original, replayed, e := a.saveComic(p, project, win[0].in, win[0].key)
	if e != nil || !replayed || original.Version != 1 || !original.Confirmed {
		t.Fatal("old receipt at capacity", e)
	}
	original, replayed, e = a.confirmComic(p, project, 1, confirmKey)
	if e != nil || !replayed || !original.Confirmed {
		t.Fatal("old confirmation at capacity", e)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_comic_receipts"); n != comicVersionLimit+1 {
		t.Fatal("receipts grew without a version or confirmation", n)
	}
}

func TestPostgresComicLostAuthorConfirmationReceipt(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "comic-confirm-ack@example.test")
	project := owner.Project.ID
	p, e := a.currentPrincipal(operationsRequest(t, c, s))
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = a.saveComic(p, project, comicTestInput(0, ""), newID("save")); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	api := "/api/v1/projects/" + project + "/comic"
	key := newID("confirm")
	_, e = admin.Exec(ctx, `CREATE FUNCTION hold_comic_confirm() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN PERFORM pg_advisory_xact_lock(912329,31); RETURN NEW; END $$;
	CREATE TRIGGER hold_comic_confirm BEFORE INSERT ON studio_comic_receipts FOR EACH ROW WHEN (NEW.operation='confirm') EXECUTE FUNCTION hold_comic_confirm()`)
	if e != nil {
		t.Fatal(e)
	}
	barrier, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer barrier.Rollback(ctx)
	if _, e = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912329,31)"); e != nil {
		t.Fatal(e)
	}
	done := make(chan int, 1)
	go func() {
		code, _, _ := authRequest(t, c, s.URL, "POST", api+"/versions/1/confirm", key, project, nil)
		done <- code
	}()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, done)
	s.Close()
	a.Close()
	f.cfg.DatabaseURL = direct
	_, again := f.open(t)
	raw, _ := presentationRequest(t, c, again.URL, project, "GET", api+"/receipt?operation=confirm", key, nil, 200)
	type receipt struct {
		ComicVersion
		Operation string `json:"operation"`
	}
	got := decode[receipt](t, raw)
	if got.Operation != "confirm" || got.ProjectID != project || got.Version != 1 || !got.Confirmed ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_comic_receipts WHERE operation='confirm'") != 1 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_comic_versions") != 1 {
		t.Fatal("lost acknowledgement produced an unrelated or duplicate confirmation")
	}
}
