package studio

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func TestPostgresRunLeaseUpgrade021(t *testing.T) {
	ctx := context.Background()
	database := testDatabase(t)
	conn := deliveryAdmin(t, database)
	old := mediaSchedulerSchema019(t, conn)
	for _, name := range []string{"migrations/020_media_schedule.sql", "migrations/021_run_stream.sql"} {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: body}
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	// The active stream has a committed delta newer than its checkpoint.
	// The completed stream has already compacted to its terminal snapshot.
	if _, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','legacy');
		INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','legacy');
		INSERT INTO studio_runs(id,workspace_id,project_id,document) VALUES
		('run_old_active','ws_demo','prj_demo','{"id":"run_old_active","workspace_id":"ws_demo","project_id":"prj_demo","status":"RUNNING","output":"first","seq":1}'),
		('run_old_done','ws_demo','prj_demo','{"id":"run_old_done","workspace_id":"ws_demo","project_id":"prj_demo","status":"COMPLETED","output":"done","seq":3}');
		INSERT INTO studio_run_streams(run_id,execution_id,head_seq,floor_seq,snapshot_seq,retained_bytes,output_bytes,closed,moderated) VALUES
		('run_old_active','exec_active',2,1,1,6,11,false,false),('run_old_done','exec_done',3,3,3,0,4,true,false);
		INSERT INTO studio_run_stream_events(run_id,seq,execution_id,name,delta,byte_size,emitted_at)
		VALUES('run_old_active',2,'exec_active','message.delta','second',6,now())`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := schedulerOriginalSnapshot(t, conn, []string{"studio_run_stream_events", "studio_runs"})
	store, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	store.close()
	after := schedulerOriginalSnapshot(t, conn, []string{"studio_run_stream_events", "studio_runs"})
	for table, contents := range before {
		if contents != after[table] {
			t.Fatalf("022 migration rewrote existing evidence in %s", table)
		}
	}
	if n := outboxCount(t, conn, `SELECT count(*) FROM studio_run_streams WHERE NOT lease_required AND lease_owner='' AND lease_epoch=0 AND lease_until IS NULL`); n != 2 {
		t.Fatal("upgrade assigned authority to an existing 021 stream")
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = applyMigrationsFrom(ctx, tx, old)
	_ = tx.Rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("021 writer accepted the 022 database")
	}
	cfg, _, calls := recoveryFixtureConfig(t, database)
	for i := 0; i < 2; i++ {
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		a.Close()
	}
	for _, want := range []struct{ id, status, output, execution string }{
		{"run_old_active", "INTERRUPTED", "firstsecond", "exec_active"},
		{"run_old_done", "COMPLETED", "done", "exec_done"},
	} {
		if n := outboxCount(t, conn, `SELECT count(*) FROM studio_runs r JOIN studio_run_streams h ON h.run_id=r.id WHERE r.id=$1 AND document->>'status'=$2 AND document->>'output'=$3 AND (document->>'seq')::bigint=3 AND h.head_seq=3 AND h.closed AND h.execution_id=$4`, want.id, want.status, want.output, want.execution); n != 1 {
			t.Fatalf("021 recovery changed committed evidence for %s", want.id)
		}
	}
	if calls.Load() != 0 || outboxCount(t, conn, `SELECT count(*) FROM studio_outbox_events WHERE resource_id='run_old_active' AND status='INTERRUPTED'`) != 1 {
		t.Fatal("021 recovery repeated execution or its interruption event")
	}
}

func TestPostgresRunLeaseProcessGenerationSwitch(t *testing.T) {
	database := testDatabase(t)
	conn := deliveryAdmin(t, database)
	var upstream atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		upstream.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"prefix\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(gateway.Close)
	cfg := durableProcessConfig{DatabaseURL: database, AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), GatewayURL: gateway.URL, ChatGatewayURL: gateway.URL + "/v1"}
	first := startDurableChild(t, cfg)
	client := authClient(t)
	user := registerFixture(t, client, first.base, "lease-generation@example.test")
	input := RunInput{ProjectID: user.Project.ID, ModelID: "studio-chat", Input: "generation fixture"}
	key := newID("key")
	code, raw, _ := authRequest(t, client, first.base, "POST", "/api/v1/runs", key, user.Project.ID, input)
	if code != 201 {
		t.Fatal("original run acceptance failed", code)
	}
	run := decode[Run](t, raw)
	path := "/api/v1/runs/" + run.ID + "/events?after="
	req, err := http.NewRequest("GET", first.base+path+"0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Project-ID", user.Project.ID)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	kind, cursor, data := readSSEFixture(t, bufio.NewReader(resp.Body))
	firstSeq, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil || kind != "message.delta" || string(data["delta"]) != `"prefix"` {
		t.Fatal("first committed delta missing")
	}
	resp.Body.Close()
	ctx := context.Background()
	var original, switching string
	if err = conn.QueryRow(ctx, `SELECT generation FROM studio_run_executor e JOIN studio_run_streams h ON h.lease_generation=e.generation WHERE h.run_id=$1 AND h.lease_until>clock_timestamp() AND h.lease_owner<>'' AND h.lease_epoch>0`, run.ID).Scan(&original); err != nil {
		t.Fatal("first delta has no current lease", err)
	}
	first.kill(t)
	barrier := lockRecoveryRow(t, database, "studio_runs", run.ID)
	next := startRecoveryProcess(t, recoveryProcessConfig{DatabaseURL: database, ReferenceDir: cfg.ReferenceDir, AssetDir: cfg.AssetDir, GatewayURL: gateway.URL})
	barrier.wait(t, conn, next)
	if err = conn.QueryRow(ctx, "SELECT generation FROM studio_run_executor WHERE singleton").Scan(&switching); err != nil || switching == original {
		t.Fatal("recovery reached the run before fencing the previous generation", err)
	}
	next.kill(t)
	barrier.release()
	last := startDurableChild(t, cfg)
	code, raw, headers := authRequest(t, client, last.base, "POST", "/api/v1/runs", key, user.Project.ID, input)
	got := decode[Run](t, raw)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || got.ID != run.ID || got.Output != "prefix" || got.Status != "INTERRUPTED" || got.Seq != firstSeq+1 || upstream.Load() != 1 {
		t.Fatalf("restart changed the committed prefix or repeated execution: status=%s seq=%d calls=%d", got.Status, got.Seq, upstream.Load())
	}
	code, raw, _ = authRequest(t, client, last.base, "GET", path+cursor, "", user.Project.ID, nil)
	if code != 200 || bytes.Count(raw, []byte("event: run.interrupted")) != 1 || bytes.Contains(raw, []byte("event: message.delta")) {
		t.Fatal("replay after prior generation did not deliver exactly the interruption")
	}
	if n := outboxCount(t, conn, `SELECT count(*) FROM studio_run_streams h CROSS JOIN studio_run_executor e WHERE h.run_id=$1 AND h.closed AND h.lease_until IS NULL AND e.generation<>$2 AND e.generation<>$3`, run.ID, original, switching); n != 1 {
		t.Fatal("a restored run still has a lease or the generation was reused")
	}
	last.kill(t)
}

// Lose only the COMMIT response for the first lease. The start is durable
// but the worker cannot know that it committed and must never call the model.
func TestPostgresRunLeaseClaimCommitAckLost(t *testing.T) {
	ctx := context.Background()
	database := testDatabase(t)
	admin := deliveryAdmin(t, database)
	proxy, armed, dropped := recoveryControlAckProxy(t, database)
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(gateway.Close)
	cfg := durableProcessConfig{DatabaseURL: proxy, GatewayURL: gateway.URL, ChatGatewayURL: gateway.URL + "/v1", AssetDir: t.TempDir(), ReferenceDir: t.TempDir()}
	first := startDurableChild(t, cfg)
	client := authClient(t)
	user := registerFixture(t, client, first.base, "lease-claim-ack@example.test")
	if _, err := admin.Exec(ctx, `CREATE FUNCTION hold_first_run_lease() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(912327,22); RETURN NEW; END $$;
		CREATE TRIGGER hold_first_run_lease BEFORE UPDATE OF execution_id ON studio_run_streams
		FOR EACH ROW WHEN(OLD.execution_id='' AND NEW.execution_id<>'') EXECUTE FUNCTION hold_first_run_lease()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912327,22)"); err != nil {
		t.Fatal(err)
	}
	key := newID("key")
	input := RunInput{ProjectID: user.Project.ID, ModelID: "studio-chat", Input: "claim acknowledgement"}
	code, data, _ := authRequest(t, client, first.base, "POST", "/api/v1/runs", key, user.Project.ID, input)
	if code != 201 {
		t.Fatal("failed to accept original request", code)
	}
	run := decode[Run](t, data)
	var pid uint32
	for deadline := time.Now().Add(4 * time.Second); pid == 0 && time.Now().Before(deadline); {
		if err = barrier.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE pg_backend_pid()=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if pid == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if pid == 0 || calls.Load() != 0 {
		t.Fatal("model called or lease transaction failed to reach first-claim barrier")
	}
	armed.Store(pid)
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("first lease COMMIT response was not lost")
	}
	// Let the live child observe the lost acknowledgement before killing it.
	// Otherwise the kill itself could mask a worker that proceeded to send.
	for deadline := time.Now().Add(5 * time.Second); ; {
		code, _, _ := authRequest(t, client, first.base, "GET", "/readyz", "", "", nil)
		if code == http.StatusServiceUnavailable {
			break
		}
		if code != http.StatusOK || time.Now().After(deadline) {
			t.Fatal("live child did not fail closed after the claim acknowledgement was lost", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 0 {
		t.Fatal("model called after the first lease acknowledgement was lost")
	}
	first.kill(t)
	if n := outboxCount(t, admin, `SELECT count(*) FROM studio_run_streams h JOIN studio_runs r ON r.id=h.run_id WHERE r.id=$1 AND r.document->>'status'='RUNNING' AND h.execution_id<>'' AND h.lease_epoch=1 AND h.head_seq=0`, run.ID); n != 1 {
		t.Fatal("targeted transaction did not commit its authorization")
	}
	cfg.DatabaseURL = database
	last := startDurableChild(t, cfg)
	code, data, headers := authRequest(t, client, last.base, "POST", "/api/v1/runs", key, user.Project.ID, input)
	got := decode[Run](t, data)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || got.ID != run.ID || got.Status != "INTERRUPTED" || got.Output != "" || got.Seq != 1 || calls.Load() != 0 {
		t.Fatalf("uncertain claim was replayed or produced output: status=%s seq=%d calls=%d", got.Status, got.Seq, calls.Load())
	}
	last.kill(t)
}
