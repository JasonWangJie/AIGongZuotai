package studio

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresVideoTimelineUpgrade029PreservesVersionsAndQuote(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	conn := deliveryAdmin(t, dsn)
	old := workflowOldMigrations(t, "029")
	tx, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing workspace');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing project');
	 INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_before','before@timeline.test','owner',decode('00','hex'));
	 INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_before','usr_before','工作区');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_before','ws_before','剪辑之前');
	 INSERT INTO studio_presentations(id,workspace_id,project_id,version,title,slide_count)
	 VALUES('pres_'||repeat('a',32),'ws_before','prj_before',1,'演示归档',1);
	 INSERT INTO studio_presentation_versions(presentation_id,workspace_id,project_id,version,created_by,document)
	 VALUES('pres_'||repeat('a',32),'ws_before','prj_before',1,'usr_before','{"title":"演示归档","theme":"paper","slides":[{"id":"one","layout":"content","title":"原版","body":"逐字保留。","notes":"私有备注"}]}');
	 INSERT INTO studio_presentation_keys(workspace_id,project_id,actor_id,key_hash,request_hash,presentation_id,version)
	 VALUES('ws_before','prj_before','usr_before',decode(repeat('ae',32),'hex'),decode(repeat('ab',32),'hex'),'pres_'||repeat('a',32),1);
	 INSERT INTO studio_ledger_quotes(id,workspace_id,project_id,owner_user_id,resource_type,model_id,model_version,price_version,request_hash,price,reserve_micros,expires_at)
	 VALUES('quo_before','ws_before','prj_before','usr_before','task','studio-video','model-before','price-before',decode(repeat('cd',32),'hex'),'{"kind":"video","artifact_micros":"310000"}',310000,now()+interval '10 minutes')`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tables := []string{"studio_presentations", "studio_presentation_versions", "studio_presentation_assets", "studio_presentation_keys", "studio_users", "studio_workspaces", "studio_projects", "studio_ledger_quotes", "studio_library_entries", "studio_library_revisions", "studio_tasks", "studio_operations_grants"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	store, e := openPostgres(dsn)
	if e != nil {
		t.Fatal(e)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("030 changed an existing original or authorization")
	}
	if n := outboxCount(t, conn, `SELECT (SELECT count(*) FROM studio_video_timelines)+(SELECT count(*) FROM studio_video_timeline_versions)
	  +(SELECT count(*) FROM studio_video_timeline_clips)+(SELECT count(*) FROM studio_video_timeline_keys)`); n != 0 {
		t.Fatal("030 invented timelines, sources, or receipts")
	}
	tx, e = conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than this executable") {
		t.Fatal("029 writer accepted 030", e)
	}
}

func TestPostgresVideoTimelineConcurrentEditsAndCapacity(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "timeline-cas@example.test")
	project := owner.Project.ID
	p, e := a.currentPrincipal(operationsRequest(t, c, s))
	if e != nil {
		t.Fatal(e)
	}
	_, asset := timelineTestSource(t, a, s, c, owner)
	original := timelineTestInput(asset)
	key := newID("save")
	v, _, e := a.saveVideoTimeline(p, project, original, key)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM studio_users WHERE id=$1 FOR UPDATE", p.User.ID); e != nil {
		t.Fatal(e)
	}
	type result struct {
		key string
		err error
	}
	done := make(chan result, 2)
	var pids []int32
	for i := 0; i < 2; i++ {
		conn := deliveryAdmin(t, f.cfg.DatabaseURL)
		pids = append(pids, int32(conn.PgConn().PID()))
		writer := &App{config: Config{AuthMode: "required"}, store: &postgresStore{conn: conn}}
		edit := timelineTestInput(asset)
		edit.TimelineID = v.ID
		edit.ExpectedVersion = &v.Version
		edit.Document.Title = fmt.Sprintf("方案%d", i)
		editKey := newID("save")
		go func() { _, _, err := writer.saveVideoTimeline(p, project, edit, editKey); done <- result{editKey, err} }()
	}
	awaitRunLeaseWaiters(t, deliveryAdmin(t, f.cfg.DatabaseURL), pids...)
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	won := 0
	for i := 0; i < 2; i++ {
		r := <-done
		receipt, receiptErr := a.videoTimelineReceipt(p, project, r.key)
		if r.err == nil {
			won++
			if receiptErr != nil || receipt.Version != 2 {
				t.Fatal("winning receipt", receiptErr)
			}
		} else {
			requireOperation(t, r.err, "VIDEO_TIMELINE_VERSION_CONFLICT")
			requireOperation(t, receiptErr, "VIDEO_TIMELINE_NOT_FOUND")
		}
	}
	if won != 1 {
		t.Fatal("two independent edits did not serialize")
	}
	// Exercise the public store admission on every version. Reaching capacity
	// must leave original keys, history and private sources readable.
	for n := uint64(2); n < videoTimelineVersionLimit; n++ {
		edit := timelineTestInput(asset)
		edit.TimelineID = v.ID
		edit.ExpectedVersion = &n
		if _, _, e = a.saveVideoTimeline(p, project, edit, newID("save")); e != nil {
			t.Fatal("save version", n+1, e)
		}
	}
	n := uint64(videoTimelineVersionLimit)
	edit := timelineTestInput(asset)
	edit.TimelineID = v.ID
	edit.ExpectedVersion = &n
	failedKey := newID("save")
	_, _, e = a.saveVideoTimeline(p, project, edit, failedKey)
	requireOperation(t, e, "VIDEO_TIMELINE_VERSION_LIMIT")
	_, e = a.videoTimelineReceipt(p, project, failedKey)
	requireOperation(t, e, "VIDEO_TIMELINE_NOT_FOUND")
	got, replayed, e := a.saveVideoTimeline(p, project, original, key)
	if e != nil || !replayed || got.Version != 1 {
		t.Fatal("original key at capacity", got, e)
	}
	got, originals, e := a.videoTimelineSources(p, project, v.ID, 1)
	if e != nil || got.Version != 1 || len(originals) != 1 {
		t.Fatal("originals at capacity", e)
	}
	list, e := a.listVideoTimelines(p, project, "", 1)
	if e != nil || len(list.Items) != 1 || list.Items[0].Version != n {
		t.Fatal(list, e)
	}
}

func TestPostgresVideoTimelineSourceSizeAndDraftCapacity(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "timeline-limits@example.test")
	p, err := a.currentPrincipal(operationsRequest(t, c, s))
	if err != nil {
		t.Fatal(err)
	}
	_, asset := timelineTestSource(t, a, s, c, owner)
	project := owner.Project.ID
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	// Vary only reported file size in an isolated registered-source fixture.
	// Export tests independently check actual bytes and the saved source hash.
	putSize := func(size int64) {
		t.Helper()
		if _, err := admin.Exec(context.Background(), `UPDATE studio_library_entries SET byte_size=$2,storage_info=jsonb_set(storage_info,'{byte_size}',to_jsonb($2::bigint)) WHERE id=$1`, asset, size); err != nil {
			t.Fatal(err)
		}
	}
	putSize(videoTimelineSourceLimit + 1)
	original := timelineTestInput(asset, asset, asset, asset)
	key := newID("save")
	_, _, err = a.saveVideoTimeline(p, project, original, key)
	requireOperation(t, err, "VIDEO_TIMELINE_ASSET_UNAVAILABLE")
	putSize(videoTimelineSourceLimit)
	over := timelineTestInput(asset, asset, asset, asset, asset)
	_, _, err = a.saveVideoTimeline(p, project, over, key)
	requireOperation(t, err, "VIDEO_TIMELINE_SOURCE_LIMIT")
	_, err = a.videoTimelineReceipt(p, project, key)
	requireOperation(t, err, "VIDEO_TIMELINE_NOT_FOUND")
	first, _, err := a.saveVideoTimeline(p, project, original, key)
	if err != nil {
		t.Fatal("at cumulative limit", err)
	}
	one := timelineTestInput(asset)
	for i := 1; i < 64; i++ {
		if _, _, err = a.saveVideoTimeline(p, project, one, newID("save")); err != nil {
			t.Fatal("at project limit", i, err)
		}
	}
	_, _, err = a.saveVideoTimeline(p, project, one, newID("save"))
	requireOperation(t, err, "VIDEO_TIMELINE_LIMIT")
	got, replayed, err := a.saveVideoTimeline(p, project, original, key)
	if err != nil || !replayed || got.ID != first.ID {
		t.Fatal("replay at source and draft capacity", err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, e := a.listVideoTimelines(p, project, cursor, 50)
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate across pages")
			}
			seen[item.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 64 {
		t.Fatal("incomplete paged project", len(seen))
	}
}

func TestPostgresVideoTimelineLostSaveAcknowledgement(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "timeline-ack@example.test")
	_, asset := timelineTestSource(t, a, s, c, owner)
	project := owner.Project.ID
	api := "/api/v1/projects/" + project + "/video-timelines"
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `CREATE FUNCTION hold_video_timeline_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN PERFORM pg_advisory_xact_lock(912328,30); RETURN NEW; END $$;
	CREATE TRIGGER hold_video_timeline_receipt BEFORE INSERT ON studio_video_timeline_keys
	FOR EACH ROW EXECUTE FUNCTION hold_video_timeline_receipt()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912328,30)"); err != nil {
		t.Fatal(err)
	}
	key := newID("save")
	input := timelineTestInput(asset)
	result := make(chan int, 1)
	go func() { code, _, _ := authRequest(t, c, s.URL, "POST", api, key, project, input); result <- code }()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, result)
	s.Close()
	a.Close()
	f.cfg.DatabaseURL = direct
	_, again := f.open(t)
	code, data, _ := authRequest(t, c, again.URL, "GET", api+"/receipt", key, project, nil)
	if code != 200 {
		t.Fatal("read-only original receipt", code)
	}
	version := decode[VideoTimelineVersion](t, data)
	if version.Version != 1 || version.Document.Title != input.Document.Title || len(version.Document.Clips) != 1 ||
		version.Document.Clips[0].AssetID != asset || version.Document.Clips[0].ContentVersion != moderationSHA(f.media.video) {
		t.Fatal("recovered an unrelated original")
	}
	for _, table := range []string{"studio_video_timelines", "studio_video_timeline_versions", "studio_video_timeline_clips", "studio_video_timeline_keys"} {
		if outboxCount(t, admin, "SELECT count(*) FROM "+table) != 1 {
			t.Fatal("duplicate rows in", table)
		}
	}
}
