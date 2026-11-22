package studio

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type collaborationDraftFixture struct {
	a                                      *App
	s                                      *httptest.Server
	owner, member, secondSession           *http.Client
	host, guest                            testIdentity
	grant                                  CollaborationReceipt
	database, shared, ordinary, management string
}

func newCollaborationDraftFixture(t *testing.T) collaborationDraftFixture {
	t.Helper()
	db := testDatabase(t)
	return openCollaborationDraftFixture(t, Config{DatabaseURL: db, AuthMode: "required", BillingMode: "credits",
		ModelCatalogFile: filepath.Join("..", "..", "config", "models.example.json"), BillingPriceFile: filepath.Join("..", "..", "config", "prices.demo.example.json")})
}

func openCollaborationDraftFixture(t *testing.T, cfg Config) collaborationDraftFixture {
	t.Helper()
	f := collaborationDraftFixture{database: cfg.DatabaseURL, owner: authClient(t), member: authClient(t), secondSession: authClient(t)}
	f.a, f.s = testApp(t, cfg)
	f.host = registerFixture(t, f.owner, f.s.URL, "draft-host@example.test")
	f.guest = registerFixture(t, f.member, f.s.URL, "draft-editor@example.test")
	presentationRequest(t, f.secondSession, f.s.URL, "", "POST", "/api/v1/auth/login", "",
		map[string]string{"email": "draft-editor@example.test", "password": "fixture-password-123"}, 200)
	f.ordinary = "/api/v1/projects/" + f.host.Project.ID + "/draft"
	f.shared = "/api/v1/collaboration/projects/" + f.host.Project.ID + "/draft"
	f.management = "/api/v1/projects/" + f.host.Project.ID + "/collaboration"
	raw, _ := presentationRequest(t, f.owner, f.s.URL, "", "POST", f.management+"/invitations", "share-draft-editor",
		map[string]any{"target_user_id": f.guest.User.ID, "role": "editor", "expected_version": 0}, 201)
	stage := decode[CollaborationReceipt](t, raw)
	raw, _ = presentationRequest(t, f.member, f.s.URL, "", "POST", "/api/v1/collaboration/invitations/"+stage.InvitationID+"/accept", "accept-draft-editor", map[string]any{"expected_version": stage.Version}, 200)
	f.grant = decode[CollaborationReceipt](t, raw)
	if f.grant.State != "ACTIVE" {
		t.Fatal("invitation not active")
	}
	return f
}

func TestPostgresCollaborationDraftSharedRevisionAndPersonalIsolation(t *testing.T) {
	f := newCollaborationDraftFixture(t)
	scoped := func(c *http.Client, method, p, key string, b any, status int) ([]byte, http.Header) {
		return presentationRequest(t, c, f.s.URL, "", method, p, key, b, status)
	}
	const key = "same-original-save-key"
	hostBody, editorBody := draftInput(0, "host original"), draftInput(1, "member original")
	scoped(f.owner, "PUT", f.ordinary, key, hostBody, 200)
	data, _ := scoped(f.member, "GET", f.shared, "", nil, 200)
	if decode[ProjectDraft](t, data).Nodes[0].Note != "host original" {
		t.Fatal("member did not read original draft")
	}
	scoped(f.secondSession, "GET", f.shared+"/revisions/1", "", nil, 200)
	scoped(authClient(t), "GET", f.shared, "", nil, 401)
	scoped(f.member, "GET", f.shared+"/receipt", key, nil, 404)
	scoped(f.member, "PUT", f.shared, key, editorBody, 200)
	scoped(f.secondSession, "GET", f.shared+"/receipt", key, nil, 200)
	for _, entry := range []struct {
		c        *http.Client
		expected uint64
	}{{f.owner, 1}, {f.member, 2}} {
		raw, _ := scoped(entry.c, "GET", f.shared+"/receipt", key, nil, 200)
		if decode[ProjectDraft](t, raw).Revision != entry.expected {
			t.Fatal("same-key receipts crossed actors")
		}
	}
	_, h := scoped(f.owner, "PUT", f.shared, key, hostBody, 200)
	if h.Get("Idempotency-Replayed") != "true" {
		t.Fatal("original 003 owner receipt not preserved")
	}
	_, h = scoped(f.member, "PUT", f.shared, key, editorBody, 200)
	if h.Get("Idempotency-Replayed") != "true" {
		t.Fatal("member original receipt lost")
	}
	scoped(f.member, "PUT", f.shared, key, draftInput(2, "different content"), 409)
	raw, _ := scoped(f.owner, "GET", f.ordinary+"/revisions/2", "", nil, 200)
	if decode[ProjectDraft](t, raw).Nodes[0].Note != "member original" {
		t.Fatal("owner did not read member's committed revision")
	}
	// Simultaneous HTTP saves against the same revision may publish exactly one.
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, p := range []struct {
		c                   *http.Client
		endpoint, key, note string
	}{{f.owner, f.ordinary, "owner-contest-key", "owner-first"}, {f.member, f.shared, "member-contest-key", "member-first"}} {
		go func() {
			<-start
			code, _, _ := authRequest(t, p.c, f.s.URL, "PUT", p.endpoint, p.key, "", draftInput(2, p.note))
			results <- code
		}()
	}
	close(start)
	one, two := <-results, <-results
	if !((one == 200 && two == 409) || (one == 409 && two == 200)) {
		t.Fatal("concurrent draft CAS", one, two)
	}
	raw, _ = scoped(f.member, "GET", f.shared+"/revisions?limit=1&before_revision=3", "", nil, 200)
	page := decode[draftRevisionPage](t, raw)
	if len(page.Items) != 1 || page.Items[0].Revision != 2 || !page.HasMore {
		t.Fatal("member history crossed revision limit")
	}
	raw, _ = scoped(f.owner, "POST", "/api/v1/projects", "", map[string]string{"name": "Untouched sibling"}, 201)
	sibling := decode[Project](t, raw)
	for _, path := range []string{f.ordinary, "/api/v1/collaboration/projects/" + sibling.ID + "/draft", "/api/v1/collaboration/projects/" + sibling.ID + "/draft/revisions"} {
		scoped(f.member, "GET", path, "", nil, 404)
	}
	for _, path := range []string{f.shared, "/api/v1/billing/wallet", "/api/v1/billing/entries", "/api/v1/tasks", "/api/v1/library"} {
		presentationRequest(t, f.member, f.s.URL, f.host.Project.ID, "GET", path, "", nil, 404)
	}
	raw, _ = scoped(f.member, "GET", "/api/v1/billing/wallet", "", nil, 200)
	if decode[LedgerWallet](t, raw).WorkspaceID != f.guest.Workspace.ID {
		t.Fatal("wallet crossed into host workspace")
	}
	raw, _ = scoped(f.member, "GET", "/api/v1/projects", "", nil, 200)
	if bytes.Contains(raw, []byte(f.host.Project.ID)) || bytes.Contains(raw, []byte(sibling.ID)) {
		t.Fatal("personal project list leaked host scope")
	}
	input := RunInput{ProjectID: f.host.Project.ID, ModelID: "studio-chat", Input: "must not execute"}
	scoped(f.member, "POST", "/api/v1/runs", "member-cannot-run-host", input, 422)
	scoped(f.member, "GET", "/api/v1/workspaces/"+f.host.Workspace.ID+"/events", "", nil, 404)
	admin := deliveryAdmin(t, f.database)
	var revisions, hostKeys, memberKeys, audits, events, runs int
	err := admin.QueryRow(t.Context(), `SELECT
	(SELECT count(*) FROM studio_draft_revisions WHERE project_id=$1),
	(SELECT count(*) FROM studio_draft_idempotency WHERE project_id=$1),
	(SELECT count(*) FROM studio_collaboration_draft_keys WHERE project_id=$1),
	(SELECT count(*) FROM studio_audit_events WHERE project_id=$1 AND action='draft.saved'),
	(SELECT count(*) FROM studio_outbox_events WHERE project_id=$1 AND action='draft.saved'),
	(SELECT count(*) FROM studio_runs WHERE project_id=$1)`, f.host.Project.ID).Scan(&revisions, &hostKeys, &memberKeys, &audits, &events, &runs)
	if err != nil || revisions != 3 || hostKeys+memberKeys != 3 || audits != 3 || events != 3 || runs != 0 {
		t.Fatal("unexpected durable draft or resource counts", revisions, hostKeys, memberKeys, audits, events, runs, err)
	}
}

func TestCollaborationDraftMemoryRequiresAccountStorage(t *testing.T) {
	_, s := testApp(t, Config{})
	const base = "/api/v1/collaboration/projects/prj_demo/draft"
	for _, route := range []struct{ method, path string }{
		{"GET", base}, {"PUT", base}, {"GET", base + "/receipt"}, {"GET", base + "/revisions"}, {"GET", base + "/revisions/1"},
	} {
		code, body, _ := request(t, s, route.method, route.path, "original-draft-key", draftInput(0, ""))
		if code != 501 || !bytes.Contains(body, []byte("COLLABORATION_STORAGE_REQUIRED")) {
			t.Fatalf("memory %s %s: %d %s", route.method, route.path, code, body)
		}
	}
}

func TestPostgresCollaborationDraftReceiptAfterLostCommit(t *testing.T) {
	direct := testDatabase(t)
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	cfg := Config{DatabaseURL: proxy, AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir()}
	f := openCollaborationDraftFixture(t, cfg)
	ctx := t.Context()
	_, err := admin.Exec(ctx, `CREATE FUNCTION hold_shared_draft_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN PERFORM pg_advisory_xact_lock(912330,32); RETURN NEW; END $$;
	CREATE TRIGGER hold_shared_draft_receipt BEFORE INSERT ON studio_collaboration_draft_keys FOR EACH ROW EXECUTE FUNCTION hold_shared_draft_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912330,32)"); err != nil {
		t.Fatal(err)
	}
	const key = "unknown-member-draft-save"
	result := make(chan int, 1)
	go func() {
		code, _, _ := authRequest(t, f.member, f.s.URL, "PUT", f.shared, key, "", draftInput(0, "committed before disconnect"))
		result <- code
	}()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, result)
	f.s.Close()
	f.a.Close()
	cfg.DatabaseURL = direct
	_, restarted := testApp(t, cfg)
	raw, _ := presentationRequest(t, f.member, restarted.URL, "", "GET", f.shared+"/receipt", key, nil, 200)
	got := decode[ProjectDraft](t, raw)
	if got.Revision != 1 || len(got.Nodes) != 1 || got.Nodes[0].Note != "committed before disconnect" {
		t.Fatal("read-only lookup did not identify the original member revision")
	}
	presentationRequest(t, f.owner, restarted.URL, "", "GET", f.shared+"/receipt", key, nil, 404)
	if outboxCount(t, admin, "SELECT count(*) FROM studio_draft_revisions") != 1 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_collaboration_draft_keys") != 1 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events WHERE action='draft.saved'") != 1 {
		t.Fatal("read-only recovery duplicated the revision, receipt, or event")
	}
}

func TestPostgresCollaborationDraftDowngradeAndRevocationFence(t *testing.T) {
	f := newCollaborationDraftFixture(t)
	const key = "earlier-member-save"
	presentationRequest(t, f.member, f.s.URL, "", "PUT", f.shared, key, draftInput(0, "private project node"), 200)
	member, err := f.a.currentPrincipal(operationsRequest(t, f.member, f.s))
	if err != nil {
		t.Fatal(err)
	}
	host, err := f.a.currentPrincipal(operationsRequest(t, f.owner, f.s))
	if err != nil {
		t.Fatal(err)
	}
	admin := deliveryAdmin(t, f.database)
	writer := deliveryAdmin(t, f.database)
	manager := deliveryAdmin(t, f.database)
	remote := &App{config: Config{AuthMode: "required"}, store: &postgresStore{conn: manager}}
	_, err = admin.Exec(t.Context(), `CREATE FUNCTION collab_draft_change_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 PERFORM pg_advisory_xact_lock(82154903); RETURN NEW; END $$;
	 CREATE TRIGGER collab_draft_change_barrier BEFORE UPDATE ON studio_collaboration_members FOR EACH ROW EXECUTE FUNCTION collab_draft_change_barrier()`)
	if err != nil {
		t.Fatal(err)
	}
	waitLock := func(pid uint32) {
		t.Helper()
		end := time.Now().Add(2 * time.Second)
		for time.Now().Before(end) {
			var waiting bool
			if e := admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM pg_locks WHERE pid=$1 AND NOT granted)`, int32(pid)).Scan(&waiting); e != nil {
				t.Fatal(e)
			}
			if waiting {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("independent operation did not actually block at database fence")
	}
	grant := f.grant
	for _, action := range []string{"downgrade", "revoke"} {
		_, err = admin.Exec(t.Context(), "SELECT pg_advisory_lock(82154903)")
		if err != nil {
			t.Fatal(err)
		}
		type managed struct {
			r   CollaborationReceipt
			err error
		}
		next := make(chan managed, 1)
		go func() {
			v, _, e := remote.mutateCollaboration(host, action, "draft-fence-"+action, collaborationMutation{
				Project: f.host.Project.ID, Target: f.guest.User.ID, Invitation: grant.InvitationID, ExpectedVersion: grant.Version})
			next <- managed{v, e}
		}()
		waitLock(manager.PgConn().PID())
		attempt := make(chan error, 1)
		edit := action == "downgrade"
		go func() {
			attempt <- pgx.BeginFunc(t.Context(), writer, func(tx pgx.Tx) error {
				workspace, _, _, e := collaborationProjectAccessSQL(t.Context(), tx, member, f.host.Project.ID, edit)
				if e != nil {
					return e
				}
				if edit {
					_, _, e = saveLockedDraftSQL(t.Context(), tx, workspace, f.host.Project.ID, member.User.ID, "racing-member-write", draftInput(1, "must not be committed"), true)
				} else {
					_, e = currentDraftSQL(t.Context(), tx, workspace, f.host.Project.ID)
				}
				return e
			})
		}()
		waitLock(writer.PgConn().PID())
		_, err = admin.Exec(t.Context(), "SELECT pg_advisory_unlock(82154903)")
		if err != nil {
			t.Fatal(err)
		}
		out := <-next
		if out.err != nil {
			t.Fatal(out.err)
		}
		grant = out.r
		var denial *operationError
		expected := 404
		if edit {
			expected = 403
		}
		if e := <-attempt; !errors.As(e, &denial) || denial.status != expected {
			t.Fatalf("cached session bypassed committed %s: %v", action, e)
		}
		if edit {
			presentationRequest(t, f.member, f.s.URL, "", "PUT", f.shared, key, draftInput(0, "private project node"), 403)
			presentationRequest(t, f.secondSession, f.s.URL, "", "PUT", f.shared, key, draftInput(0, "private project node"), 403)
			presentationRequest(t, f.member, f.s.URL, "", "GET", f.shared+"/receipt", key, nil, 200)
		}
	}
	for _, p := range []string{f.shared, f.shared + "/revisions", f.shared + "/revisions/1", f.shared + "/receipt"} {
		presentationRequest(t, f.member, f.s.URL, "", "GET", p, key, nil, 404)
		presentationRequest(t, f.secondSession, f.s.URL, "", "GET", p, key, nil, 404)
	}
	presentationRequest(t, f.member, f.s.URL, "", "PUT", f.shared, key, draftInput(0, "private project node"), 404)
	raw, _ := presentationRequest(t, f.owner, f.s.URL, "", "GET", f.ordinary, "", nil, 200)
	if decode[ProjectDraft](t, raw).Revision != 1 {
		t.Fatal("late draft committed after withdrawal")
	}
}
