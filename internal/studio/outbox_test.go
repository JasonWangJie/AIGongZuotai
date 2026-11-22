package studio

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestDurableCursorRejectsForeignScopeAndUnboundedInput(t *testing.T) {
	expected := durableCursor{Version: 1, StorageID: "database", UserID: "alice", WorkspaceID: "ws_a", ProjectID: "p_a", Seq: 7}
	if got, err := parseDurableCursor(encodeDurableCursor(expected), expected); err != nil || got != expected {
		t.Fatal("valid cursor rejected", err)
	}
	for _, field := range []string{"user", "project", "workspace", "database", "version", "sequence"} {
		t.Run(field, func(t *testing.T) {
			bad := expected
			switch field {
			case "user":
				bad.UserID = "bob"
			case "project":
				bad.ProjectID = "p_b"
			case "workspace":
				bad.WorkspaceID = "ws_b"
			case "database":
				bad.StorageID = "other"
			case "version":
				bad.Version = 2
			case "sequence":
				bad.Seq = 9007199254740992
			}
			if _, err := parseDurableCursor(encodeDurableCursor(bad), expected); err == nil {
				t.Fatal("foreign cursor accepted")
			}
		})
	}
	for _, raw := range []string{"7", strings.Repeat("x", 1025), "", encodeDurableCursor(durableCursor{})} {
		if _, err := parseDurableCursor(raw, expected); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
}

func outboxFixtureRun(id string) Run {
	now := time.Now().UTC()
	return Run{ID: id, WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", ModelID: "studio-chat", Prompt: "PRIVATE_PROMPT_DO_NOT_LOG", Output: "PRIVATE_OUTPUT_DO_NOT_STORE_IN_OUTBOX", Status: "QUEUED", CreatedAt: now, UpdatedAt: now}
}
func outboxStore(t *testing.T) (*postgresStore, string, *pgx.Conn) {
	t.Helper()
	databaseURL := testDatabase(t)
	s, err := openPostgres(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return s, databaseURL, conn
}
func outboxCount(t *testing.T, conn *pgx.Conn, sql string, args ...any) int {
	t.Helper()
	var count int
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresOutboxLifecycleDedupAndRestart(t *testing.T) {
	s, databaseURL, admin := outboxStore(t)
	ctx := context.Background()
	run := outboxFixtureRun("run_outbox")
	audit := newAuditEvent(run.WorkspaceID, run.ProjectID, "", "run.accepted", "run", run.ID, AuditDetails{ToStatus: run.Status})
	if err := s.createRun(run, "outbox-create-key", payloadHash(run), audit); err != nil {
		t.Fatal(err)
	}
	l := claimFixtureRun(t, s, &run)
	if err := s.appendLeasedRunDelta(l, run.Seq, "private", time.Now()); err != nil {
		t.Fatal(err)
	}
	run.Output += "private"
	run.Seq++
	if err := s.saveLeasedRun(&run, l, run.Seq); err != nil {
		t.Fatal(err)
	}
	if err := s.saveLeasedRun(&run, l, run.Seq); err != nil {
		t.Fatal(err)
	}
	if count := outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events"); count != 4 {
		t.Fatalf("expected audit, accepted, running and checkpoint, got %d", count)
	}
	var raw string
	if err := admin.QueryRow(ctx, "SELECT json_agg(e)::text FROM studio_outbox_events e").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "PRIVATE_") || strings.Contains(raw, "message.delta") {
		t.Fatal("sensitive token stream entered metadata outbox")
	}
	s.close()
	reopened, err := openPostgres(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	if err = reopened.prepareOutbox(); err != nil {
		t.Fatal(err)
	}
	page, err := reopened.readEventPage(subscriptionScope{"ws_demo", "prj_demo"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 || page.High != 4 {
		t.Fatalf("lost committed events on restart %+v", page)
	}
	// Simulate redelivery of an already committed effect. Cursor and event_id
	// uniqueness suppress a second projection, even after a dispatcher restart.
	if _, err = admin.Exec(ctx, "UPDATE studio_outbox_events SET projected_at=NULL WHERE project_seq=2; UPDATE studio_outbox_limits SET pending=1 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	if err = reopened.prepareOutbox(); err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_project_event_feed"); n != 4 {
		t.Fatal("redelivery duplicated durable effect", n)
	}
	if n := outboxCount(t, admin, "SELECT pending FROM studio_outbox_limits"); n != 0 {
		t.Fatal("pending did not drain", n)
	}
}

func TestPostgresOutboxFailuresRollbackBusinessAndProjection(t *testing.T) {
	for _, stage := range []string{"producer", "consumer"} {
		t.Run(stage, func(t *testing.T) {
			s, _, admin := outboxStore(t)
			ctx := context.Background()
			run := outboxFixtureRun("run_atomic")
			if stage == "consumer" {
				if err := s.createRun(run, "atomic-key", payloadHash(run)); err != nil {
					t.Fatal(err)
				}
			}
			table := "studio_outbox_events"
			if stage == "consumer" {
				table = "studio_project_event_feed"
			}
			_, err := admin.Exec(ctx, `CREATE FUNCTION fail_event_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture event transaction failure'; END $$; CREATE TRIGGER fail_event BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_event_insert()`)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "producer" {
				audit := newAuditEvent("ws_demo", "prj_demo", "", "run.accepted", "run", run.ID, AuditDetails{})
				if err = s.createRun(run, "atomic-key", payloadHash(run), audit); err == nil {
					t.Fatal("expected transaction failure")
				}
				for _, table := range []string{"studio_runs", "studio_idempotency", "studio_audit_events", "studio_outbox_events"} {
					if n := outboxCount(t, admin, "SELECT count(*) FROM "+table); n != 0 {
						t.Fatal("failed transaction leaked rows", table, n)
					}
				}
			} else {
				if _, err = s.projectOutboxBatch(); err == nil {
					t.Fatal("expected projection failure")
				}
				if outboxCount(t, admin, "SELECT count(*) FROM studio_project_event_feed") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_event_consumers") != 0 || outboxCount(t, admin, "SELECT pending FROM studio_outbox_limits") != 1 {
					t.Fatal("projection effect/checkpoint/pending did not roll back together")
				}
			}
		})
	}
}

func TestPostgresOutboxSequenceFollowsCommitAndRollback(t *testing.T) {
	_, databaseURL, first := outboxStore(t)
	second, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx1, err := first.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	event := DurableEvent{WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "run.updated", ResourceType: "run", ResourceID: "run_one", ResourceVersion: 1, Status: "RUNNING"}
	if err = appendOutboxSQL(ctx, tx1, "first-aborted", event); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		tx2, e := second.Begin(ctx)
		if e != nil {
			finished <- e
			return
		}
		defer tx2.Rollback(ctx)
		close(started)
		e = appendOutboxSQL(ctx, tx2, "second-committed", event)
		if e == nil {
			e = tx2.Commit(ctx)
		}
		finished <- e
	}()
	<-started
	select {
	case err = <-finished:
		t.Fatalf("second transaction passed uncommitted project sequence: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	if err = tx1.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if seq := outboxCount(t, first, "SELECT project_seq FROM studio_outbox_events WHERE event_key='second-committed'"); seq != 1 {
		t.Fatal("rolled back sequence made a gap", seq)
	}
}

func TestPostgresOutboxBoundedBacklogRecoversAndRetentionResets(t *testing.T) {
	s, _, admin := outboxStore(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `INSERT INTO studio_event_heads(workspace_id,project_id,last_seq) VALUES('ws_demo','prj_demo',10000);
 INSERT INTO studio_outbox_events(event_id,event_key,workspace_id,project_id,project_seq,kind,resource_type,resource_id,resource_version) SELECT 'evt_seed_'||n,'seed_'||n,'ws_demo','prj_demo',n,'run.updated','run','run_seed',n FROM generate_series(1,10000) n;
 UPDATE studio_outbox_limits SET pending=10000 WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	run := outboxFixtureRun("run_backpressure")
	err := s.createRun(run, "backpressure-key", payloadHash(run))
	var op *operationError
	if !errors.As(err, &op) || op.code != "EVENT_BACKLOG_FULL" || s.failed.Load() {
		t.Fatalf("bounded backlog should reject recoverably: %v", err)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_runs") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency") != 0 {
		t.Fatal("backpressure partially accepted mutation")
	}
	if _, err = s.projectOutboxBatch(); err != nil {
		t.Fatal(err)
	}
	if err = s.createRun(run, "backpressure-key", payloadHash(run)); err != nil {
		t.Fatal("same request did not recover after draining", err)
	}
	if err = s.prepareOutbox(); err != nil {
		t.Fatal(err)
	}
	page, err := s.readEventPage(subscriptionScope{"ws_demo", "prj_demo"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.High != 10001 || page.Floor != 10001-eventProjectRetention || len(page.Items) != outboxBatchSize {
		t.Fatalf("retention bounds or read limit invalid: high=%d floor=%d items=%d", page.High, page.Floor, len(page.Items))
	}
	if count := outboxCount(t, admin, "SELECT retained FROM studio_outbox_limits"); count != eventProjectRetention {
		t.Fatal("retained counter does not match project retention", count)
	}
}

// The proxy observes PostgreSQL's COMMIT command-complete, then closes before
// forwarding that acknowledgement. The client gets a real unknown commit result.
func commitAckDropProxy(t *testing.T, databaseURL string) (string, *atomic.Bool, <-chan struct{}) {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	target := u.Host
	u.Host = listener.Addr().String()
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	armed := &atomic.Bool{}
	dropped := make(chan struct{})
	go func() {
		client, e := listener.Accept()
		if e != nil {
			return
		}
		defer client.Close()
		backend, e := net.DialTimeout("tcp", target, 3*time.Second)
		if e != nil {
			return
		}
		defer backend.Close()
		go func() { _, _ = io.Copy(backend, client); _ = backend.Close() }()
		if relayUntilCommitAck(client, backend, func(uint32) bool { return armed.Load() }) {
			close(dropped)
		}
	}()
	return u.String(), armed, dropped
}

// Only pass the backend PID to the test's selector. BackendKeyData also
// contains a private cancellation key which must never be exposed.
func relayUntilCommitAck(client io.Writer, backend io.Reader, drop func(uint32) bool) bool {
	reader := bufio.NewReader(backend)
	var pid uint32
	for {
		var header [5]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return false
		}
		size := int(binary.BigEndian.Uint32(header[1:]))
		if size < 4 || size > 16<<20 {
			return false
		}
		body := make([]byte, size-4)
		if _, err := io.ReadFull(reader, body); err != nil {
			return false
		}
		if header[0] == 'K' && len(body) >= 4 {
			pid = binary.BigEndian.Uint32(body[:4])
		}
		if header[0] == 'C' && string(body) == "COMMIT\x00" && drop(pid) {
			return true
		}
		if _, err := client.Write(append(header[:], body...)); err != nil {
			return false
		}
	}
}

func TestPostgresOutboxUnknownCommitRecoversCommittedEvent(t *testing.T) {
	databaseURL := testDatabase(t)
	proxyURL, armed, dropped := commitAckDropProxy(t, databaseURL)
	s, err := openPostgres(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	run := outboxFixtureRun("run_unknown_commit")
	armed.Store(true)
	err = s.createRun(run, "unknown-commit-key", payloadHash(run))
	if err == nil || !s.failed.Load() {
		t.Fatal("lost commit acknowledgement did not fail closed", err)
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("proxy did not observe committed transaction")
	}
	s.close()
	reopened, err := openPostgres(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	record, found, err := reopened.idempotency("unknown-commit-key")
	if err != nil || !found || record.ID != run.ID {
		t.Fatal("committed idempotency was lost", err)
	}
	if err = reopened.prepareOutbox(); err != nil {
		t.Fatal(err)
	}
	page, err := reopened.readEventPage(subscriptionScope{"ws_demo", "prj_demo"}, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ResourceID != run.ID {
		t.Fatal("committed event was not recovered", err)
	}
}

func TestPostgresOutboxConsumerUnknownCommitDoesNotDuplicate(t *testing.T) {
	databaseURL := testDatabase(t)
	proxyURL, armed, dropped := commitAckDropProxy(t, databaseURL)
	s, err := openPostgres(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	run := outboxFixtureRun("run_consumer_unknown_commit")
	if err = s.createRun(run, "consumer-unknown-key", payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	if _, err = s.projectOutboxBatch(); err == nil {
		t.Fatal("projection commit acknowledgement was not lost")
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("proxy never observed committed projection")
	}
	s.close()
	reopened, err := openPostgres(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	if err = reopened.prepareOutbox(); err != nil {
		t.Fatal(err)
	}
	page, err := reopened.readEventPage(subscriptionScope{"ws_demo", "prj_demo"}, 0)
	if err != nil || len(page.Items) != 1 || page.High != 1 {
		t.Fatal("consumer unknown commit duplicated or lost effect", err)
	}
}

func TestPostgresOutboxInitialResetDoesNotSkipPending(t *testing.T) {
	s, _, _ := outboxStore(t)
	// Attach PG to a memory fixture only after its goroutines start. There is
	// deliberately no automatic dispatcher, so this interleaving is deterministic.
	app, err := New(Config{AssetDir: t.TempDir(), ReferenceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	app.store = s
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	run := outboxFixtureRun("run_before_initial_reset")
	if err = s.createRun(run, "pending-before-reset", payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL + "/api/v1/workspaces/ws_demo/events?project_id=prj_demo")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	name, token, _ := readSSEFixture(t, reader)
	if name != "stream.reset" {
		t.Fatal("initial reset missing")
	}
	data, _ := base64.RawURLEncoding.DecodeString(token)
	var cursor durableCursor
	if json.Unmarshal(data, &cursor) != nil || cursor.Seq != 0 {
		t.Fatal("initial cursor skipped committed but pending event")
	}
	if _, err = s.projectOutboxBatch(); err != nil {
		t.Fatal(err)
	}
	name, _, event := readSSEFixture(t, reader)
	if name != "resource.changed" || string(event["resource_id"]) != `"run_before_initial_reset"` {
		t.Fatal("pending event was lost after reset", name)
	}
}

func readSSEFixture(t *testing.T, reader *bufio.Reader) (string, string, map[string]json.RawMessage) {
	t.Helper()
	var name, id string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			var data map[string]json.RawMessage
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatal(err)
			}
			return name, id, data
		}
	}
}

func TestPostgresOutboxHTTPInitialPendingReplayAndScope(t *testing.T) {
	databaseURL := testDatabase(t)
	app, err := New(Config{DatabaseURL: databaseURL, AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), DemoTokenDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	alice, bob := authClient(t), authClient(t)
	identity := registerFixture(t, alice, server.URL, "events-alice@example.test")
	other := registerFixture(t, bob, server.URL, "events-bob@example.test")
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/workspaces/"+identity.Workspace.ID+"/events?project_id="+identity.Project.ID, nil)
	response, err := alice.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	name, token, _ := readSSEFixture(t, reader)
	if name != "stream.reset" || token == "" {
		t.Fatal("initial stream did not provide reset cursor")
	}
	_ = response.Body.Close()
	code, _, _ := authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "events-run-key", identity.Project.ID, map[string]any{"project_id": identity.Project.ID, "model_id": "studio-chat", "input": "event fixture"})
	if code != 201 {
		t.Fatal("create run failed", code)
	}
	req, _ = http.NewRequest("GET", server.URL+"/api/v1/workspaces/"+identity.Workspace.ID+"/events?project_id="+identity.Project.ID+"&after=invalid-stale-query", nil)
	req.Header.Set("Last-Event-ID", token)
	response, err = alice.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	name, _, _ = readSSEFixture(t, bufio.NewReader(response.Body))
	if name != "resource.changed" {
		t.Fatal("pending/reconnected event was not delivered", name)
	}
	req, _ = http.NewRequest("GET", server.URL+"/api/v1/workspaces/"+other.Workspace.ID+"/events?project_id="+other.Project.ID, nil)
	req.Header.Set("Last-Event-ID", token)
	foreign, err := bob.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = foreign.Body.Close()
	if foreign.StatusCode != 400 {
		t.Fatal("cross-account cursor was accepted", foreign.StatusCode)
	}
	code, data, _ := authRequest(t, alice, server.URL, "POST", "/api/v1/projects", "", "", map[string]string{"name": "another scope"})
	if code != 201 {
		t.Fatal("project create failed")
	}
	project := decode[Project](t, data)
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/workspaces/%s/events?project_id=%s", server.URL, identity.Workspace.ID, project.ID), nil)
	req.Header.Set("Last-Event-ID", token)
	foreign, err = alice.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = foreign.Body.Close()
	if foreign.StatusCode != 400 {
		t.Fatal("cross-project cursor was accepted", foreign.StatusCode)
	}
}
