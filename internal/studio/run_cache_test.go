package studio

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Hold the first write after runEvents registers, so a real subscription stays
// open across final persistence, a full channel and provider exit.
type heldRunSSEWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	first            sync.Once
}

func (w *heldRunSSEWriter) Write(data []byte) (int, error) {
	w.first.Do(func() { close(w.entered); <-w.release })
	return w.ResponseRecorder.Write(data)
}

func holdRunSSE(t *testing.T, a *App, client *http.Client, base string, identity testIdentity, id string) func() {
	t.Helper()
	req := httptest.NewRequest("GET", base+"/api/v1/runs/"+id+"/events", nil)
	for _, cookie := range client.Jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-Project-ID", identity.Project.ID)
	writer := &heldRunSSEWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); a.Handler().ServeHTTP(writer, req) }()
	var once sync.Once
	release := func() { once.Do(func() { close(writer.release) }) }
	t.Cleanup(release)
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE handler did not reach its first write")
	}
	return func() {
		release()
		select {
		case <-done:
			if writer.Code != 200 || writer.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
				t.Fatal("SSE stream was rejected", writer.Code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("SSE handler did not exit after unblocking")
		}
	}
}

func TestPostgresRunCachePinsExecutionAndSubscribers(t *testing.T) {
	cfg := Config{MaxRecords: 1, SubscriberBuffer: 1, ChatScheduling: ChatSchedulingConfig{Workers: 1, QueueSize: 5}}
	a, server, provider := executionIntegrationFixture(t, cfg, true)
	alice, bob := authClient(t), authClient(t)
	first := registerFixture(t, alice, server.URL, "cache-run-one@example.test")
	second := registerFixture(t, bob, server.URL, "cache-run-two@example.test")
	active := executionRun(t, server, alice, first, "studio-chat", "active", "cache-running-key")
	pendingIO := executionStarted(t, provider, "active")

	c := createPGConversation(t, bob, server, second)
	input := conversationInput(c.Version, nil, "queued conversation")
	path := "/api/v1/conversations/" + c.ID + "/runs"
	code, raw, _ := authRequest(t, bob, server.URL, "POST", path, "cache-turn-key", second.Project.ID, input)
	if code != 201 {
		t.Fatal("conversation refused behind foreign run", code, string(raw))
	}
	turn := decode[Run](t, raw)
	cold := executionRun(t, server, bob, second, "studio-chat", "cancel queued", "cache-cold-key")
	executionCancel(t, server, bob, second, cold.ID)
	getCached := func(id string) *runRecord { a.mu.Lock(); defer a.mu.Unlock(); return a.runs[id] }
	activeRecord, turnRecord := getCached(active.ID), getCached(turn.ID)
	if activeRecord == nil || turnRecord == nil || getCached(cold.ID) != nil {
		t.Fatal("queued cancellation did not release only its own pin")
	}
	executionCancel(t, server, alice, first, active.ID)
	executionState(t, a, 1, 1)
	code, raw, _ = authRequest(t, bob, server.URL, "GET", "/api/v1/runs/"+cold.ID+"/snapshot", "", second.Project.ID, nil)
	if code != 200 || decode[Run](t, raw).Status != "CANCELLED" {
		t.Fatal("cold cancelled run lost")
	}
	code, raw, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/runs/"+cold.ID+"/snapshot", "", first.Project.ID, nil)
	if code != 404 {
		t.Fatal("cold foreign run leaked")
	}
	if getCached(active.ID) != activeRecord || getCached(turn.ID) != turnRecord || getCached(cold.ID) != nil {
		t.Fatal("cold reads displaced queued or still-executing run")
	}

	// Two readers share one record. One disconnect must not release the other.
	endFirst := holdRunSSE(t, a, bob, server.URL, second, turn.ID)
	endSecond := holdRunSSE(t, a, bob, server.URL, second, turn.ID)
	pendingIO.finish()
	turnIO := executionStarted(t, provider, "queued conversation")
	if getCached(active.ID) != nil {
		t.Fatal("worker exit did not release excess cancelled record")
	}
	remaining := executionRun(t, server, bob, second, "studio-chat", "next", "cache-next-key")
	turnIO.finish()
	nextIO := executionStarted(t, provider, "next")
	assertReaders := func(n int) {
		t.Helper()
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.runs[turn.ID] != turnRecord || turnRecord.streamReaders != n || len(turnRecord.Subscribers) != 0 || turnRecord.Run.Status != "COMPLETED" {
			t.Fatal("open stream lost identity or pin after terminal/slow subscriber")
		}
	}
	assertReaders(2)
	endFirst()
	assertReaders(1)
	endSecond()
	if getCached(turn.ID) != nil || getCached(remaining.ID) == nil {
		t.Fatal("last subscriber released wrong record")
	}
	code, raw, headers := authRequest(t, bob, server.URL, "POST", path, "cache-turn-key", second.Project.ID, input)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || decode[Run](t, raw).ID != turn.ID {
		t.Fatal("cold conversation replay failed", code)
	}
	code, raw, _ = authRequest(t, bob, server.URL, "GET", "/api/v1/runs/"+turn.ID+"/events", "", second.Project.ID, nil)
	if code != 200 || !bytes.Contains(raw, []byte("event: run.snapshot")) || !bytes.Contains(raw, []byte("completed: queued conversation")) {
		t.Fatal("terminal SSE lost final persisted snapshot", code)
	}
	if getCached(turn.ID) != nil {
		t.Fatal("terminal replay grew idle cache")
	}
	nextIO.finish()
	executionState(t, a, 0, 0)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.runs) > cfg.MaxRecords {
		t.Fatal("excess idle cache retained", len(a.runs))
	}
}

// Stop exactly between scheduler pick and executeRun. Filling the real outbox
// also makes the first shutdown save roll back after the normal consumer exits.
func TestPostgresRunCacheShutdownRetainsPickedRunOnBackpressure(t *testing.T) {
	store, _, admin := outboxStore(t)
	recoverySeedRows(t, admin, 0, 1, outboxPendingLimit-1, "demo")
	run := outboxFixtureRun(newID("run"))
	if err := store.createRun(run, newID("key"), payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	if pending := outboxCount(t, admin, "SELECT pending FROM studio_outbox_limits WHERE singleton"); pending != outboxPendingLimit {
		t.Fatal("expected real full outbox", pending)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	record := &runRecord{Run: run}
	app := &App{ctx: ctx, cancel: cancel, store: store,
		config:    Config{MaxRecords: 1, ReplaySize: 8},
		runs:      map[string]*runRecord{run.ID: record, "idle": {Run: Run{ID: "idle", Status: "COMPLETED"}}},
		execution: newExecutionScheduler(ChatSchedulingConfig{Workers: 1, WorkspaceConcurrency: 1, ModelConcurrency: 1}),
	}
	app.execution.enqueueLocked(executionJob{ID: run.ID, WorkspaceID: run.WorkspaceID, ModelKey: run.ModelID, EnqueuedAt: time.Now()})
	picked, ok := app.execution.pickLocked(time.Now())
	if !ok || picked.ID != run.ID {
		t.Fatal("run not picked")
	}
	cancel()
	app.executePickedRun(picked, record)
	if app.runs[run.ID] != record || len(app.execution.running) != 0 {
		t.Fatal("picked job lost its pending shutdown checkpoint")
	}
	app.Close()
	if outboxCount(t, admin, `SELECT count(*) FROM studio_runs WHERE id=$1 AND document->>'status'='INTERRUPTED' AND (document->>'seq')::bigint=1`, run.ID) != 1 ||
		outboxCount(t, admin, `SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1 AND status='INTERRUPTED'`, run.ID) != 1 ||
		outboxCount(t, admin, `SELECT count(*) FROM studio_outbox_events WHERE event_key LIKE 'recovery-prior:%' AND projected_at IS NOT NULL`) == 0 {
		t.Fatal("shutdown did not confirm exactly one interrupted checkpoint after projecting existing events")
	}
}
