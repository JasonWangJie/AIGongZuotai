package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func draftInput(revision uint64, note string) draftSaveInput {
	return draftSaveInput{ExpectedRevision: &revision, Nodes: []DraftNode{{ID: "node-1", Title: "创作草稿", Note: note}}}
}

func draftErrorCode(err error) string {
	var op *operationError
	if errors.As(err, &op) {
		return op.code
	}
	return ""
}

func TestDraftValidationAndBodyBounds(t *testing.T) {
	valid := draftInput(0, strings.Repeat("😀", 5000))
	valid.Nodes[0].Title = strings.Repeat("创", 80)
	if err := validateDraft(valid); err != nil {
		t.Fatal("Unicode limits should count characters", err)
	}
	zero := uint64(0)
	cases := map[string]draftSaveInput{
		"missing revision": {Nodes: []DraftNode{}},
		"missing nodes":    {ExpectedRevision: &zero},
		"duplicate ID":     {ExpectedRevision: &zero, Nodes: []DraftNode{{ID: "n"}, {ID: "n"}}},
		"invalid ID":       {ExpectedRevision: &zero, Nodes: []DraftNode{{ID: "_node"}}},
		"long ID":          {ExpectedRevision: &zero, Nodes: []DraftNode{{ID: strings.Repeat("n", 81)}}},
		"long title":       {ExpectedRevision: &zero, Nodes: []DraftNode{{ID: "n", Title: strings.Repeat("😀", 81)}}},
		"long note":        draftInput(0, strings.Repeat("文", 5001)),
		"postgres NUL":     draftInput(0, "must reject\x00before SQL"),
		"bad UTF8":         draftInput(0, string([]byte{0xff})),
		"revision bound":   draftInput(1001, ""),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if code := draftErrorCode(validateDraft(input)); code != "INVALID_DRAFT" {
				t.Fatalf("expected INVALID_DRAFT, got %s", code)
			}
		})
	}
	tooMany := draftInput(0, "")
	tooMany.Nodes = make([]DraftNode, 41)
	if validateDraft(tooMany) == nil {
		t.Fatal("node limit bypassed")
	}
	for name, fixture := range map[string]struct {
		body, contentType, code string
	}{
		"empty nodes":       {`{"expected_revision":0,"nodes":[]}`, "application/json", ""},
		"empty titles":      {`{"expected_revision":0,"nodes":[{"id":"a"}]}`, "application/json", ""},
		"caller identity":   {`{"expected_revision":0,"nodes":[],"user_id":"other"}`, "application/json", "INVALID_JSON"},
		"extra node field":  {`{"expected_revision":0,"nodes":[{"id":"a","url":"https://example.test"}]}`, "application/json", "INVALID_JSON"},
		"two documents":     {`{"expected_revision":0,"nodes":[]} {}`, "application/json", "INVALID_JSON"},
		"negative revision": {`{"expected_revision":-1,"nodes":[]}`, "application/json", "INVALID_JSON"},
		"null revision":     {`{"expected_revision":null,"nodes":[]}`, "application/json", "INVALID_DRAFT"},
		"raw invalid UTF8":  {"{\"nodes\":\"\xff\"}", "application/json", "INVALID_JSON"},
		"wrong type":        {`{}`, "text/plain", "UNSUPPORTED_MEDIA_TYPE"},
		"oversized body":    {strings.Repeat(" ", draftBodyLimit+1), "application/json", "DRAFT_BODY_TOO_LARGE"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/", strings.NewReader(fixture.body))
			req.Header.Set("Content-Type", fixture.contentType)
			_, err := decodeDraftJSON(httptest.NewRecorder(), req)
			if code := draftErrorCode(err); code != fixture.code {
				t.Fatalf("expected %q, got %q: %v", fixture.code, code, err)
			}
		})
	}
}

func TestDraftMemoryRequiresPostgres(t *testing.T) {
	_, server := testApp(t, Config{})
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/api/v1/projects/prj_demo/draft"},
		{"PUT", "/api/v1/projects/prj_demo/draft"},
		{"GET", "/api/v1/projects/prj_demo/draft/revisions"},
		{"GET", "/api/v1/projects/prj_demo/draft/revisions/1"},
	} {
		code, body, _ := request(t, server, endpoint.method, endpoint.path, "draft-memory-key", draftInput(0, ""))
		if code != 501 || !bytes.Contains(body, []byte("DRAFT_STORAGE_REQUIRED")) {
			t.Fatalf("memory %s: %d %s", endpoint.path, code, body)
		}
	}
}

func TestPostgresDraftLifecycleIsolationAndRestart(t *testing.T) {
	databaseURL := testDatabase(t)
	config := Config{DatabaseURL: databaseURL, AuthMode: "required", AssetDir: t.TempDir()}
	app, err := newIsolatedApp(t, config)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { server.Close(); app.Close() })
	alice, bob, anonymous := authClient(t), authClient(t), authClient(t)
	identity := registerFixture(t, alice, server.URL, "draft-alice@example.test")
	registerFixture(t, bob, server.URL, "draft-bob@example.test")
	path := "/api/v1/projects/" + identity.Project.ID + "/draft"
	for _, method := range []string{"GET", "PUT"} {
		code, _, _ := authRequest(t, anonymous, server.URL, method, path, "anonymous-draft-key", "", draftInput(0, ""))
		if code != 401 {
			t.Fatalf("anonymous %s got %d", method, code)
		}
	}
	code, data, _ := authRequest(t, alice, server.URL, "GET", path, "", "", nil)
	empty := decode[ProjectDraft](t, data)
	if code != 200 || empty.Revision != 0 || empty.UpdatedAt != nil || empty.Nodes == nil || len(empty.Nodes) != 0 {
		t.Fatalf("empty draft %d %s", code, data)
	}
	firstInput := draftInput(0, "private first draft text")
	code, data, _ = authRequest(t, alice, server.URL, "PUT", path, "draft-original-key", "", firstInput)
	first := decode[ProjectDraft](t, data)
	if code != 200 || first.Revision != 1 || first.UpdatedAt == nil || first.ProjectID != identity.Project.ID {
		t.Fatalf("first draft %d %s", code, data)
	}
	code, data, _ = authRequest(t, alice, server.URL, "PUT", path, "draft-second-key", "", draftInput(1, "second draft"))
	if code != 200 || decode[ProjectDraft](t, data).Revision != 2 {
		t.Fatalf("second draft %d %s", code, data)
	}
	code, data, headers := authRequest(t, alice, server.URL, "PUT", path, "draft-original-key", "", firstInput)
	replayed := decode[ProjectDraft](t, data)
	if code != 200 || replayed.Revision != 1 || !replayed.UpdatedAt.Equal(*first.UpdatedAt) || headers.Get("Idempotency-Replayed") != "true" || replayed.Nodes[0].Note != first.Nodes[0].Note {
		t.Fatal("replay did not return original immutable version")
	}
	for _, fixture := range []struct {
		key, want string
		input     draftSaveInput
	}{
		{"draft-original-key", "IDEMPOTENCY_CONFLICT", draftInput(0, "changed content")},
		{"draft-original-key", "IDEMPOTENCY_CONFLICT", draftInput(2, first.Nodes[0].Note)},
		{"draft-stale-key", "DRAFT_CONFLICT", draftInput(1, "stale writer")},
	} {
		code, data, _ := authRequest(t, alice, server.URL, "PUT", path, fixture.key, "", fixture.input)
		if code != 409 || !bytes.Contains(data, []byte(fixture.want)) {
			t.Fatalf("conflict: %d %s", code, data)
		}
	}
	for _, endpoint := range []struct{ method, suffix string }{
		{"GET", ""}, {"PUT", ""}, {"GET", "/revisions"}, {"GET", "/revisions/1"},
	} {
		code, _, _ = authRequest(t, bob, server.URL, endpoint.method, path+endpoint.suffix, "draft-foreign-key", "", draftInput(2, "foreign"))
		if code != 404 {
			t.Fatalf("foreign %s %s got %d", endpoint.method, endpoint.suffix, code)
		}
	}
	code, data, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/projects", "", "", map[string]string{"name": "Separate draft"})
	if code != 201 {
		t.Fatalf("second project %d %s", code, data)
	}
	otherProject := decode[Project](t, data)
	for _, endpoint := range []struct{ method, suffix string }{
		{"GET", ""}, {"PUT", ""}, {"GET", "/revisions"}, {"GET", "/revisions/1"},
	} {
		code, _, _ = authRequest(t, alice, server.URL, endpoint.method, path+endpoint.suffix, "draft-scoped-key", otherProject.ID, draftInput(2, "wrong selection"))
		if code != 404 {
			t.Fatalf("project scope mismatch %s got %d", endpoint.suffix, code)
		}
	}
	otherPath := "/api/v1/projects/" + otherProject.ID + "/draft"
	code, data, _ = authRequest(t, alice, server.URL, "PUT", otherPath, "draft-original-key", otherProject.ID, draftInput(0, "other project"))
	if code != 200 || decode[ProjectDraft](t, data).Revision != 1 {
		t.Fatal("idempotency incorrectly crossed project boundaries")
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", path+"/revisions/1", "", "", nil)
	old := decode[ProjectDraft](t, data)
	if code != 200 || old.Nodes[0].Note != first.Nodes[0].Note {
		t.Fatal("historical revision mutated")
	}
	restore := draftInput(2, "")
	restore.Nodes = old.Nodes
	code, data, _ = authRequest(t, alice, server.URL, "PUT", path, "draft-restore-key", "", restore)
	if code != 200 || decode[ProjectDraft](t, data).Revision != 3 {
		t.Fatalf("restore should create version3: %d %s", code, data)
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", path+"/revisions?limit=2", "", "", nil)
	page := decode[draftRevisionPage](t, data)
	if code != 200 || len(page.Items) != 2 || page.Items[0].Revision != 3 || page.Items[1].Revision != 2 || !page.HasMore || page.NextBeforeRevision == nil || *page.NextBeforeRevision != 2 || bytes.Contains(data, []byte("private first")) {
		t.Fatalf("bounded history %d %s", code, data)
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", path+"/revisions?limit=2&before_revision=2", "", "", nil)
	page = decode[draftRevisionPage](t, data)
	if code != 200 || len(page.Items) != 1 || page.Items[0].Revision != 1 || page.HasMore || page.NextBeforeRevision != nil {
		t.Fatalf("history cursor %d %s", code, data)
	}
	for _, suffix := range []string{"/revisions?limit=0", "/revisions?limit=51", "/revisions?before_revision=0", "/revisions?before_revision=-1", "/revisions/0", "/revisions/nope"} {
		code, _, _ = authRequest(t, alice, server.URL, "GET", path+suffix, "", "", nil)
		if code != 400 {
			t.Fatalf("invalid history query %s got %d", suffix, code)
		}
	}
	code, _, _ = authRequest(t, alice, server.URL, "GET", path+"/revisions/999", "", "", nil)
	if code != 404 {
		t.Fatal("missing historical version must be404")
	}
	// The largest valid node set remains below the explicit body cap, including
	// 4-byte Unicode. This also verifies the per-draft limit exceeds generic API64KiB.
	large := draftSaveInput{ExpectedRevision: new(uint64), Nodes: make([]DraftNode, 40)}
	*large.ExpectedRevision = 3
	for i := range large.Nodes {
		large.Nodes[i] = DraftNode{ID: fmt.Sprintf("node-%d", i), Title: strings.Repeat("😀", 80), Note: strings.Repeat("😀", 5000)}
	}
	code, data, _ = authRequest(t, alice, server.URL, "PUT", path, "draft-large-body-key", "", large)
	if code != 200 || decode[ProjectDraft](t, data).Revision != 4 {
		t.Fatalf("bounded large draft %d", code)
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/audit?project_id="+identity.Project.ID, "", "", nil)
	if code != 200 || bytes.Contains(data, []byte(first.Nodes[0].Note)) {
		t.Fatal("draft text leaked into audit")
	}
	var audit struct {
		Items []AuditEvent `json:"items"`
	}
	if json.Unmarshal(data, &audit) != nil {
		t.Fatal("audit decode failed")
	}
	saves := 0
	for _, event := range audit.Items {
		if event.Action == "draft.saved" {
			saves++
			if event.ActorID != identity.User.ID || event.ProjectID != identity.Project.ID || event.Details.Revision < 1 {
				t.Fatal("draft audit identity or revision incorrect")
			}
		}
	}
	if saves != 4 {
		t.Fatalf("replays/conflicts created extra audit: %d", saves)
	}
	// Reuse the existing session cookie after a process restart. Drafts and
	// original idempotency responses must survive without memory reconstruction.
	server.Close()
	app.Close()
	app, err = newIsolatedApp(t, config)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	code, data, _ = authRequest(t, alice, server.URL, "GET", path, "", "", nil)
	if code != 200 || decode[ProjectDraft](t, data).Revision != 4 {
		t.Fatalf("restart lost draft %d", code)
	}
	code, data, headers = authRequest(t, alice, server.URL, "PUT", path, "draft-original-key", "", firstInput)
	if code != 200 || decode[ProjectDraft](t, data).Revision != 1 || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("restart lost original idempotency response")
	}
}

func draftSQLFixture(t *testing.T) (string, *App, *pgx.Conn) {
	t.Helper()
	databaseURL := testDatabase(t)
	app, err := newIsolatedApp(t, Config{DatabaseURL: databaseURL, AssetDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal("cannot open second draft test connection")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return databaseURL, app, conn
}

func TestPostgresDraftConcurrentCASAndIdempotency(t *testing.T) {
	databaseURL, _, conn1 := draftSQLFixture(t)
	conn2, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal("cannot open concurrent draft connection")
	}
	t.Cleanup(func() { _ = conn2.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		draft  ProjectDraft
		replay bool
		err    error
	}
	runPair := func(keys [2]string, input draftSaveInput) []result {
		t.Helper()
		start := make(chan struct{})
		results := make(chan result, 2)
		var wg sync.WaitGroup
		for i, conn := range []*pgx.Conn{conn1, conn2} {
			wg.Add(1)
			go func(conn *pgx.Conn, key string) {
				defer wg.Done()
				<-start
				draft, replay, err := saveDraftSQL(ctx, conn, "ws_demo", "prj_demo", "", key, input)
				results <- result{draft, replay, err}
			}(conn, keys[i])
		}
		close(start)
		wg.Wait()
		close(results)
		items := []result{}
		for r := range results {
			items = append(items, r)
		}
		return items
	}
	first := runPair([2]string{"parallel-draft-a", "parallel-draft-b"}, draftInput(0, "two connections"))
	success, conflict := 0, 0
	for _, result := range first {
		if result.err == nil && result.draft.Revision == 1 && !result.replay {
			success++
		} else if draftErrorCode(result.err) == "DRAFT_CONFLICT" {
			conflict++
		} else {
			t.Fatalf("unexpected concurrent result: %+v", result)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("CAS did not select exactly one writer")
	}
	second := runPair([2]string{"parallel-same-key", "parallel-same-key"}, draftInput(1, "single logical save"))
	replays := 0
	for _, result := range second {
		if result.err != nil || result.draft.Revision != 2 {
			t.Fatalf("same-key concurrent save: %v", result.err)
		}
		if result.replay {
			replays++
		}
	}
	if replays != 1 {
		t.Fatal("same-key concurrent save must have exactly one replay")
	}
	for _, table := range []string{"studio_draft_revisions", "studio_draft_idempotency", "studio_audit_events"} {
		var count int
		query := "SELECT count(*) FROM " + table
		if table == "studio_audit_events" {
			query += " WHERE action='draft.saved'"
		}
		if err := conn1.QueryRow(ctx, query).Scan(&count); err != nil || count != 2 {
			t.Fatalf("duplicate %s: count %d, err %v", table, count, err)
		}
	}
}

func TestPostgresDraftVersionCapAndImmutability(t *testing.T) {
	_, _, conn := draftSQLFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := draftInput(0, "original")
	if _, _, err := saveDraftSQL(ctx, conn, "ws_demo", "prj_demo", "", "draft-cap-original", input); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE studio_draft_revisions SET nodes='[]',node_count=0 WHERE project_id='prj_demo' AND revision=1`,
		`DELETE FROM studio_draft_revisions WHERE project_id='prj_demo' AND revision=1`,
	} {
		if _, err := conn.Exec(ctx, query); err == nil {
			t.Fatal("immutable revision allowed mutation")
		}
	}
	_, err := conn.Exec(ctx, `INSERT INTO studio_draft_revisions(project_id,workspace_id,revision,nodes,node_count,created_at) SELECT 'prj_demo','ws_demo',n,'[]'::jsonb,0,clock_timestamp() FROM generate_series(2,1000) AS n; UPDATE studio_project_drafts SET revision=1000,updated_at=clock_timestamp() WHERE project_id='prj_demo'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = saveDraftSQL(ctx, conn, "ws_demo", "prj_demo", "", "draft-over-limit", draftInput(1000, "cannot erase history")); draftErrorCode(err) != "DRAFT_REVISION_LIMIT" {
		t.Fatalf("limit returned %v", err)
	}
	if original, replay, err := saveDraftSQL(ctx, conn, "ws_demo", "prj_demo", "", "draft-cap-original", input); err != nil || !replay || original.Revision != 1 {
		t.Fatalf("cap blocked safe replay: %v", err)
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM studio_draft_revisions`).Scan(&count); err != nil || count != 1000 {
		t.Fatal("cap pruned or added history")
	}
}

func TestPostgresDraftTransactionRollsBackWithAuditFailure(t *testing.T) {
	_, _, conn := draftSQLFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := conn.Exec(ctx, `CREATE FUNCTION reject_fixture_draft_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='draft.saved' THEN RAISE EXCEPTION 'fixture audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER fixture_draft_audit BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_fixture_draft_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = saveDraftSQL(ctx, conn, "ws_demo", "prj_demo", "", "draft-audit-atomic", draftInput(0, "must rollback")); err == nil {
		t.Fatal("audit failure was ignored")
	}
	for _, table := range []string{"studio_project_drafts", "studio_draft_revisions", "studio_draft_idempotency"} {
		var count int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial commit in %s: %d %v", table, count, err)
		}
	}
}
