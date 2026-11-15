package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	"time"

	"github.com/jackc/pgx/v5"
)

// The child calls the complete production New. The parent uses PostgreSQL row
// locks and the wire-level COMMIT proxy, never a production startup hook.
type recoveryProcessConfig struct {
	DatabaseURL, ReferenceDir, AssetDir, GatewayURL string
	ReadyFile, ErrorFile                            string
}

func TestRecoveryProcessChild(t *testing.T) {
	path := os.Getenv("STUDIO_RECOVERY_PROCESS_CONFIG")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private recovery fixture")
	}
	var fixture recoveryProcessConfig
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid private recovery fixture")
	}
	a, err := New(Config{DatabaseURL: fixture.DatabaseURL, AuthMode: "required", Mode: "live",
		ReferenceDir: fixture.ReferenceDir, AssetDir: fixture.AssetDir,
		AsyncImageBaseURL: fixture.GatewayURL, AsyncImageAPIKey: fixtureMediaKey,
		AsyncImageModel: "fixture-image-model", AsyncImageCredentialID: "fixture-credential",
		MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()},
		PollInterval:  20 * time.Millisecond, Heartbeat: 100 * time.Millisecond})
	if err != nil {
		// Never write the error text/DSN/prompt into the process rendezvous file.
		_ = os.WriteFile(fixture.ErrorFile, []byte("startup_rejected"), 0600)
		return
	}
	defer a.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot open local recovery fixture")
	}
	server := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: time.Second}
	defer server.Close()
	go server.Serve(listener)
	if os.WriteFile(fixture.ReadyFile, []byte("http://"+listener.Addr().String()), 0600) != nil {
		t.Fatal("cannot publish recovery fixture readiness")
	}
	<-time.After(90 * time.Second)
}

type recoveryProcess struct {
	*durableChild
	ready, rejected string
}

func startRecoveryProcess(t *testing.T, fixture recoveryProcessConfig) *recoveryProcess {
	t.Helper()
	dir := t.TempDir()
	fixture.ReadyFile, fixture.ErrorFile = filepath.Join(dir, "ready"), filepath.Join(dir, "rejected")
	raw, err := json.Marshal(fixture)
	if err != nil || os.WriteFile(filepath.Join(dir, "private.json"), raw, 0600) != nil {
		t.Fatal("cannot prepare private recovery process fixture")
	}
	child := &durableChild{command: exec.Command(os.Args[0], "-test.run=^TestRecoveryProcessChild$", "-test.timeout=100s"), done: make(chan struct{})}
	child.command.Env = append(os.Environ(), "STUDIO_RECOVERY_PROCESS_CONFIG="+filepath.Join(dir, "private.json"))
	child.command.Stdout, child.command.Stderr = &child.logs, &child.logs
	if child.command.Start() != nil {
		t.Fatal("cannot start recovery child")
	}
	go func() { _ = child.command.Wait(); close(child.done) }()
	t.Cleanup(func() { child.kill(t) })
	return &recoveryProcess{durableChild: child, ready: fixture.ReadyFile, rejected: fixture.ErrorFile}
}

func (p *recoveryProcess) waitReady(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(p.ready); err == nil && strings.HasPrefix(string(raw), "http://127.0.0.1:") {
			p.base = string(raw)
			return p.base
		}
		select {
		case <-p.done:
			t.Fatal("recovery child exited before readiness")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery child readiness deadline exceeded")
	return ""
}

func (p *recoveryProcess) assertNotReady(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(p.ready); !os.IsNotExist(err) {
		t.Fatal("recovery exposed HTTP before its complete startup committed")
	}
}

type recoveryFaultFixture struct {
	config recoveryProcessConfig
	conn   *pgx.Conn
	client *http.Client
	user   testIdentity
	input  TaskInput
	task   *taskRecord
	key    string
	calls  *atomic.Int64
}

// One real HTTP POST reaches the supplier, then the real process is killed
// before a handle response. Subsequent fixtures preserve that exact accepted
// request/idem record; recovery must not send it a second time.
func newRecoveryFaultFixture(t *testing.T) *recoveryFaultFixture {
	t.Helper()
	database := testDatabase(t)
	calls := &atomic.Int64{}
	accepted := make(chan struct{}, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != imageGenerationEndpoint {
			http.Error(w, "unexpected fixture operation", 404)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 128<<10))
		_ = r.Body.Close()
		select {
		case accepted <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(gateway.Close)
	cfg := recoveryProcessConfig{DatabaseURL: database, ReferenceDir: filepath.Join(t.TempDir(), "references"), AssetDir: filepath.Join(t.TempDir(), "assets"), GatewayURL: gateway.URL}
	first := startDurableChild(t, durableProcessConfig{DatabaseURL: database, ReferenceDir: cfg.ReferenceDir, AssetDir: cfg.AssetDir, GatewayURL: cfg.GatewayURL})
	client := authClient(t)
	identity := registerFixture(t, client, first.base, "recovery-process@example.test")
	input := mediaFixtureInput("image", 1)
	input.ProjectID = identity.Project.ID
	key := newID("recovery_key")
	code, body, _ := authRequest(t, client, first.base, "POST", "/api/v1/tasks", key, identity.Project.ID, input)
	if code != 202 {
		t.Fatal("real original media request was not accepted", code)
	}
	id := decode[Task](t, body).ID
	select {
	case <-accepted:
	case <-time.After(8 * time.Second):
		t.Fatal("original supplier request did not arrive")
	}
	conn, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal("cannot inspect isolated recovery database")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	record := recoveryFaultReadTask(t, conn, id)
	if record.Task.Status != "SUBMITTING" || record.Handle.ID != "" || record.Lease.Owner == "" || calls.Load() != 1 {
		t.Fatal("original POST did not have the expected durable fenced checkpoint")
	}
	first.kill(t)
	return &recoveryFaultFixture{config: cfg, conn: conn, client: client, user: identity, input: input, task: record, key: key, calls: calls}
}

func recoveryFaultReadTask(t *testing.T, conn *pgx.Conn, id string) *taskRecord {
	t.Helper()
	var document, input, handle, submission []byte
	record := &taskRecord{Lease: &TaskLease{TaskID: id}}
	var until *time.Time
	err := conn.QueryRow(context.Background(), `SELECT document,input,handle,submission,next_poll,lease_owner,lease_epoch,lease_until,(SELECT generation FROM studio_task_executor WHERE singleton) FROM studio_tasks WHERE id=$1`, id).Scan(&document, &input, &handle, &submission, &record.NextPoll, &record.Lease.Owner, &record.Lease.Epoch, &until, &record.Lease.Generation)
	if err != nil || json.Unmarshal(document, &record.Task) != nil || json.Unmarshal(input, &record.Input) != nil || json.Unmarshal(handle, &record.Handle) != nil || json.Unmarshal(submission, &record.Submission) != nil {
		t.Fatal("cannot decode original recovery task checkpoint")
	}
	if until != nil {
		record.Lease.Until = *until
	}
	record.Lease.Version, record.LeaseStatus = record.Task.Version, record.Task.Status
	return record
}

const recoveryFaultSeedSeq uint64 = 11

func seedRecoveryFaultRuns(t *testing.T, fixture *recoveryFaultFixture, count int) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := fixture.conn.Begin(ctx)
	if err != nil {
		t.Fatal("cannot seed recovery page")
	}
	defer tx.Rollback(ctx)
	ids := make([]string, count)
	now := time.Now().UTC()
	for i := range ids {
		ids[i] = fmt.Sprintf("run_%032x", i+1)
		run := Run{ID: ids[i], WorkspaceID: fixture.user.Workspace.ID, ProjectID: fixture.user.Project.ID, ProviderMode: "live", ModelID: "studio-chat", Prompt: "private recovery fixture", Output: "saved partial fixture", Status: "RUNNING", Seq: recoveryFaultSeedSeq, CreatedAt: now, UpdatedAt: now}
		raw, _ := json.Marshal(run)
		if _, err = tx.Exec(ctx, "INSERT INTO studio_runs(id,workspace_id,project_id,document) VALUES($1,$2,$3,$4)", run.ID, run.WorkspaceID, run.ProjectID, raw); err != nil {
			t.Fatal("cannot seed independent active run checkpoint")
		}
	}
	if tx.Commit(ctx) != nil {
		t.Fatal("recovery fixture seed commit failed")
	}
	return ids
}

type recoveryRowBarrier struct {
	conn *pgx.Conn
	tx   pgx.Tx
	pid  uint32
}

func lockRecoveryRow(t *testing.T, database, table, id string) *recoveryRowBarrier {
	t.Helper()
	if table != "studio_runs" && table != "studio_tasks" {
		t.Fatal("invalid fixture table")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, database)
	if err != nil {
		t.Fatal("cannot open recovery barrier connection")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		_ = conn.Close(ctx)
		t.Fatal("cannot begin recovery barrier")
	}
	b := &recoveryRowBarrier{conn: conn, tx: tx, pid: conn.PgConn().PID()}
	t.Cleanup(func() { b.release(); _ = conn.Close(context.Background()) })
	var locked string
	if tx.QueryRow(ctx, "SELECT id FROM "+table+" WHERE id=$1 FOR UPDATE", id).Scan(&locked) != nil || locked != id {
		t.Fatal("cannot lock target recovery boundary")
	}
	return b
}

func (b *recoveryRowBarrier) release() {
	if b.tx != nil {
		_ = b.tx.Rollback(context.Background())
		b.tx = nil
	}
}

func (b *recoveryRowBarrier) wait(t *testing.T, conn *pgx.Conn, child *recoveryProcess) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := conn.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid)))`, int32(b.pid)).Scan(&blocked)
		if err != nil {
			t.Fatal("cannot inspect recovery row-lock boundary")
		}
		if blocked {
			child.assertNotReady(t)
			return
		}
		select {
		case <-child.done:
			t.Fatal("recovery exited before reaching deterministic row boundary")
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("recovery did not reach the expected database row boundary")
}

func assertRecoveryFaultRuns(t *testing.T, f *recoveryFaultFixture, ids []string, recovered int) {
	t.Helper()
	var actual, wrong, events int
	err := f.conn.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE document->>'status'='INTERRUPTED'),count(*) FILTER(WHERE document->>'output'<>'saved partial fixture' OR CASE WHEN document->>'status'='INTERRUPTED' THEN (document->>'seq')::bigint<>$2 ELSE document->>'status'<>'RUNNING' OR (document->>'seq')::bigint<>$3 END) FROM studio_runs WHERE id=ANY($1)`, ids, recoveryFaultSeedSeq+recoverySequenceGap, recoveryFaultSeedSeq).Scan(&actual, &wrong)
	if err != nil || actual != recovered || wrong != 0 {
		t.Fatalf("recovery checkpoint boundary differs: recovered=%d want=%d invalid=%d error=%v", actual, recovered, wrong, err)
	}
	if f.conn.QueryRow(context.Background(), "SELECT count(*) FROM studio_outbox_events WHERE resource_type='run' AND resource_id=ANY($1) AND status='INTERRUPTED'", ids).Scan(&events) != nil || events != recovered {
		t.Fatal("recovered run and its single durable event did not commit together")
	}
}

func assertRecoveryFaultTaskReplay(t *testing.T, f *recoveryFaultFixture, base string) {
	t.Helper()
	code, raw, headers := authRequest(t, f.client, base, "POST", "/api/v1/tasks", f.key, f.user.Project.ID, f.input)
	got := decode[Task](t, raw)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || got.ID != f.task.Task.ID || got.Status != "SUBMIT_UNKNOWN" || got.Version != f.task.Task.Version+1 || len(got.Artifacts) != 0 || f.calls.Load() != 1 {
		t.Fatal("startup/original-key replay duplicated acceptance, recovery version or original POST")
	}
	var count int
	if f.conn.QueryRow(context.Background(), "SELECT count(*) FROM studio_outbox_events WHERE resource_type='task' AND resource_id=$1 AND status='SUBMIT_UNKNOWN'", got.ID).Scan(&count) != nil || count != 1 {
		t.Fatal("uncertain original submission has more than one recovery event")
	}
	persisted := recoveryFaultReadTask(t, f.conn, got.ID)
	if !reflect.DeepEqual(persisted.Input, f.task.Input) || !reflect.DeepEqual(persisted.Submission, f.task.Submission) || !reflect.DeepEqual(persisted.Handle, f.task.Handle) {
		t.Fatal("recovery changed the original input, immutable submission or unknown handle")
	}
}

func TestPostgresRecoveryProcessKillAtPageBoundary(t *testing.T) {
	f := newRecoveryFaultFixture(t)
	ids := seedRecoveryFaultRuns(t, f, recoveryMediaPageRecords+2)
	barrier := lockRecoveryRow(t, f.config.DatabaseURL, "studio_runs", ids[recoveryMediaPageRecords])
	first := startRecoveryProcess(t, f.config)
	barrier.wait(t, f.conn, first)
	assertRecoveryFaultRuns(t, f, ids, recoveryMediaPageRecords)
	if f.calls.Load() != 1 {
		t.Fatal("workers sent supplier traffic while startup was incomplete")
	}
	first.kill(t)
	barrier.release()
	assertRecoveryFaultRuns(t, f, ids, recoveryMediaPageRecords)
	second := startRecoveryProcess(t, f.config)
	base := second.waitReady(t)
	assertRecoveryFaultRuns(t, f, ids, len(ids))
	assertRecoveryFaultTaskReplay(t, f, base)
	second.kill(t)
	third := startRecoveryProcess(t, f.config)
	base = third.waitReady(t)
	assertRecoveryFaultRuns(t, f, ids, len(ids))
	assertRecoveryFaultTaskReplay(t, f, base)
	t.Log("Complete New was killed after one committed page; restart resumed the remainder, with stable checkpoint sequences/events and one original supplier POST.")
}

// New creates auxiliary lease connections as well as its control writer. Route
// only its first connection through the existing single-connection ACK proxy;
// the rest reach the same real isolated database directly.
func recoveryControlAckProxy(t *testing.T, database string) (string, *atomic.Bool, <-chan struct{}) {
	t.Helper()
	controlURL, armed, dropped := commitAckDropProxy(t, database)
	actual, err := url.Parse(database)
	if err != nil {
		t.Fatal("invalid private fixture database URL")
	}
	control, err := url.Parse(controlURL)
	if err != nil {
		t.Fatal("invalid private fixture control URL")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot start local recovery proxy")
	}
	var mu sync.Mutex
	var sockets []net.Conn
	var wg sync.WaitGroup
	closed := false
	addSocket := func(c net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			_ = c.Close()
			return false
		}
		sockets = append(sockets, c)
		return true
	}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		first := true
		for {
			client, e := listener.Accept()
			if e != nil {
				return
			}
			if !addSocket(client) {
				return
			}
			target := actual.Host
			if first {
				target, first = control.Host, false
			}
			wg.Add(1)
			go func(client net.Conn, target string) {
				defer wg.Done()
				defer client.Close()
				backend, e := net.DialTimeout("tcp", target, 3*time.Second)
				if e != nil || !addSocket(backend) {
					return
				}
				defer backend.Close()
				copied := make(chan struct{})
				go func() { _, _ = io.Copy(backend, client); _ = backend.Close(); close(copied) }()
				_, _ = io.Copy(client, backend)
				_ = client.Close()
				<-copied
			}(client, target)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		closed = true
		for _, socket := range sockets {
			_ = socket.Close()
		}
		mu.Unlock()
		<-acceptDone
		wg.Wait()
	})
	control.Host = listener.Addr().String()
	return control.String(), armed, dropped
}

func TestPostgresRecoveryProcessCommitAckUnknown(t *testing.T) {
	f := newRecoveryFaultFixture(t)
	ids := seedRecoveryFaultRuns(t, f, recoveryMediaPageRecords+2)
	barrier := lockRecoveryRow(t, f.config.DatabaseURL, "studio_runs", ids[recoveryMediaPageRecords])
	proxy, armed, dropped := recoveryControlAckProxy(t, f.config.DatabaseURL)
	cfg := f.config
	cfg.DatabaseURL = proxy
	first := startRecoveryProcess(t, cfg)
	barrier.wait(t, f.conn, first)
	assertRecoveryFaultRuns(t, f, ids, recoveryMediaPageRecords)
	// The writer is already inside the specific run-save transaction. Previous
	// read-only page and outbox commits cannot consume this acknowledgement fault.
	armed.Store(true)
	barrier.release()
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("specific recovery save COMMIT acknowledgement was not dropped")
	}
	select {
	case <-first.done:
	case <-time.After(8 * time.Second):
		t.Fatal("unknown startup commit did not fail closed")
	}
	first.assertNotReady(t)
	if raw, err := os.ReadFile(first.rejected); err != nil || string(raw) != "startup_rejected" {
		t.Fatal("complete New did not reject unknown recovery commit")
	}
	assertRecoveryFaultRuns(t, f, ids, recoveryMediaPageRecords+1)
	if f.calls.Load() != 1 {
		t.Fatal("unknown startup commit allowed worker traffic")
	}
	second := startRecoveryProcess(t, f.config)
	base := second.waitReady(t)
	assertRecoveryFaultRuns(t, f, ids, len(ids))
	assertRecoveryFaultTaskReplay(t, f, base)
	t.Log("Wire-level ACK loss followed an actual recovery save COMMIT: no HTTP readiness/worker traffic; next complete New retained exactly one sequence/event transition.")
}

func TestPostgresRecoveryGenerationFencesBeforeLeaseCleanup(t *testing.T) {
	f := newRecoveryFaultFixture(t)
	ctx := context.Background()
	// A second independent old owner remains unblocked while the first cleanup
	// row is held. It models a late worker from the crashed executor generation.
	otherID := "task_ffffffffffffffffffffffffffffffff"
	_, err := f.conn.Exec(ctx, `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,submission,next_poll,updated_at,lease_owner,lease_epoch,lease_until) SELECT $2,workspace_id,project_id,document||jsonb_build_object('id',$2::text),input,handle,submission,next_poll,updated_at,lease_owner,lease_epoch,clock_timestamp()+interval '5 minutes' FROM studio_tasks WHERE id=$1`, f.task.Task.ID, otherID)
	if err != nil {
		t.Fatal("cannot seed independent old leased checkpoint")
	}
	old := recoveryFaultReadTask(t, f.conn, otherID)
	barrier := lockRecoveryRow(t, f.config.DatabaseURL, "studio_tasks", f.task.Task.ID)
	child := startRecoveryProcess(t, f.config)
	barrier.wait(t, f.conn, child)
	var generation, owner string
	var epoch int64
	if f.conn.QueryRow(ctx, "SELECT generation FROM studio_task_executor WHERE singleton").Scan(&generation) != nil || generation == old.Lease.Generation {
		t.Fatal("generation was not committed before old-lease cleanup waited")
	}
	if f.conn.QueryRow(ctx, "SELECT lease_owner,lease_epoch FROM studio_tasks WHERE id=$1", otherID).Scan(&owner, &epoch) != nil || owner != old.Lease.Owner || epoch != old.Lease.Epoch {
		t.Fatal("old-lease fixture disappeared before the independent generation fence was exercised")
	}
	stale := cloneLeasedRecord(old)
	stale.Task.Version++
	stale.Task.Progress++
	tx, err := f.conn.Begin(ctx)
	if err != nil {
		t.Fatal("cannot begin independent old-worker result transaction")
	}
	writeCtx, cancel := context.WithTimeout(ctx, time.Second)
	err = writeTaskWithLeaseSQL(writeCtx, tx, stale)
	cancel()
	_ = tx.Rollback(ctx)
	if !isLeaseLost(err) {
		t.Fatal("old generation wrote or waited on cleanup instead of failing the generation fence", err)
	}
	barrier.release()
	base := child.waitReady(t)
	assertRecoveryFaultTaskReplay(t, f, base)
	got := recoveryFaultReadTask(t, f.conn, otherID)
	if got.Task.Status != "SUBMIT_UNKNOWN" || got.Task.Version != old.Task.Version+1 || got.Task.Progress != old.Task.Progress || got.Lease.Owner != "" || got.Lease.Epoch <= old.Lease.Epoch || f.calls.Load() != 1 {
		t.Fatal("cleanup/recovery accepted the old result or restarted its generation request")
	}
	t.Log("Complete New committed executor generation before bounded cleanup; a still-unexpired old lease could not write while another cleanup row remained locked.")
}
