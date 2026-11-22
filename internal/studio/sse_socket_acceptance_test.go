package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSESocketMemoryWriteDeadline(t *testing.T) {
	testSSESocketWriteDeadline(t, "")
}

func TestPostgresSSESocketWriteDeadlineAndReplay(t *testing.T) {
	testSSESocketWriteDeadline(t, sseSocketDatabase(t))
}

func testSSESocketWriteDeadline(t *testing.T, db string) {
	g := &sseSocketGateway{first: make(chan struct{}), flood: make(chan struct{}), finish: make(chan struct{})}
	up := httptest.NewServer(g)
	t.Cleanup(up.Close)
	first := sync.OnceFunc(func() { close(g.first) })
	flood := sync.OnceFunc(func() { close(g.flood) })
	finish := sync.OnceFunc(func() { close(g.finish) })
	cfg := Config{Mode: "live", BaseURL: up.URL + "/v1", ChatModel: "socket-fixture", APIKey: "local-fixture", Heartbeat: time.Second, DatabaseURL: db}
	if db != "" {
		cfg.AuthMode = "required"
		cfg.RunReplayReadConnections = 2
	}
	a, server := testApp(t, cfg)
	defer first()
	defer flood()
	defer finish()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	scope := sseSocketScope(t, server, 0, db != "")
	input := RunInput{ProjectID: scope.identity.Project.ID, ModelID: "studio-chat", Input: "socket replay fixture"}
	key := newID("socket")
	var run Run
	capacityRequest(t, ctx, scope, "POST", server.URL+"/api/v1/runs", key, input, &run, 201)
	path := "/api/v1/runs/" + run.ID + "/events?after=0"
	if db != "" {
		other := sseSocketScope(t, server, 1, true)
		capacityRequest(t, ctx, other, "GET", server.URL+path, "", nil, nil, 404)
		anonymous := capacityScope{client: server.Client()}
		capacityRequest(t, ctx, anonymous, "GET", server.URL+path, "", nil, nil, 401)
	}
	slow, actualTimeout := sseSocketPausedReader(t, a, scope, run.ID)
	fastResponse := sseSocketOpen(t, ctx, server, scope, path, "")
	defer fastResponse.Body.Close()
	fast := bufio.NewReader(fastResponse.Body)
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 2 })
	writesBefore, dropsBefore := a.obs.slowWrites.Load(), a.obs.slowConsumers.Load()
	first()
	cursor := sseSocketExpect(t, slow, run.ID, 1)
	sseSocketExpect(t, fast, run.ID, 1)
	// Never read from slow again, even after the server evicts it.
	start := time.Now()
	flood()
	for seq := 2; seq <= socketFloodDeltas+1; seq++ {
		sseSocketExpect(t, fast, run.ID, seq)
	}
	fastElapsed := time.Since(start)
	var blocked time.Duration
	select {
	case blocked = <-actualTimeout:
	case <-time.After(12 * time.Second):
		t.Fatal("no real TCP write deadline reached; kernel buffering is not evidence of eviction")
	}
	sseSocketWait(t, func() bool {
		return len(a.streamSlots) == 1 && a.scopedStreams(scope.identity.Workspace.ID) == 1 && a.obs.slowWrites.Load() == writesBefore+1
	})
	if a.obs.slowConsumers.Load() != dropsBefore {
		t.Fatal("socket case also filled an application mailbox")
	}
	replay := sseSocketOpen(t, ctx, server, scope, path, cursor)
	defer replay.Body.Close()
	recovered := bufio.NewReader(replay.Body)
	for seq := 2; seq <= socketFloodDeltas+1; seq++ {
		sseSocketExpect(t, recovered, run.ID, seq)
	}
	finish()
	for _, reader := range []*bufio.Reader{fast, recovered} {
		sseSocketExpect(t, reader, run.ID, socketFloodDeltas+2)
		tail, err := io.ReadAll(io.LimitReader(reader, 1024))
		if err != nil || len(tail) == 1024 || strings.TrimSpace(string(tail)) != "" {
			t.Fatal("extra event or incomplete terminal response")
		}
	}
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 0 })
	var snapshot Run
	code, raw, _ := authRequest(t, scope.client, server.URL, "GET", "/api/v1/runs/"+run.ID+"/snapshot", "", scope.identity.Project.ID, nil)
	if code != 200 || json.Unmarshal(raw, &snapshot) != nil {
		t.Fatal("could not load original Run snapshot")
	}
	var expected strings.Builder
	for seq := 1; seq <= socketFloodDeltas+1; seq++ {
		expected.WriteString(sseSocketDelta(seq))
	}
	if snapshot.ID != run.ID || snapshot.Status != "COMPLETED" || snapshot.Seq != socketFloodDeltas+2 || snapshot.Output != expected.String() {
		t.Fatal("original snapshot differs from committed events")
	}
	// The original idempotency receipt is large once this Run has finished.
	// Verify it through the regular endpoint and check the real model POSTs.
	code, raw, headers := authRequest(t, scope.client, server.URL, "POST", "/api/v1/runs", key, scope.identity.Project.ID, input)
	var receipt Run
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || json.Unmarshal(raw, &receipt) != nil || receipt.ID != run.ID || g.posts.Load() != 1 {
		t.Fatal("original receipt replay changed the resource or resubmitted to the model", code)
	}
	t.Logf("ordinary_readers=1 paused_readers=1 resumed_from_seq=1 deltas=%d terminal_seq=%d slow_socket_write_ms=%.2f fast_flood_ms=%.2f write_timeouts=1 buffer_full=0 model_posts=1", socketFloodDeltas+1, snapshot.Seq, float64(blocked.Microseconds())/1000, float64(fastElapsed.Microseconds())/1000)
}

func TestPostgresSSESocketSharedAdmissionAndRelease(t *testing.T) {
	db := sseSocketDatabase(t)
	finish := make(chan struct{})
	releaseModels := sync.OnceFunc(func() { close(finish) })
	var posts atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(up.Close)
	a, server := testApp(t, Config{Mode: "live", DatabaseURL: db, AuthMode: "required", ChatModel: "socket-fixture", BaseURL: up.URL + "/v1", APIKey: "local-fixture", RunReplayReadConnections: 2, Heartbeat: time.Second, ChatScheduling: ChatSchedulingConfig{Workers: 4, ModelConcurrency: 4}})
	// Release only this fixture's own four original calls after all checks.
	defer releaseModels()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	const perScope, global = 32, 128
	if a.config.Admission.WorkspaceStreams != perScope || cap(a.streamSlots) != global {
		t.Fatal("global or default workspace stream limit changed; re-evaluate acceptance")
	}
	var scopes [5]capacityScope
	var projects [5]string
	var runs [4]string
	var runIDs [4]string
	for i := range scopes {
		scopes[i] = sseSocketScope(t, server, i, true)
		projects[i] = "/api/v1/workspaces/" + scopes[i].identity.Workspace.ID + "/events"
		if i < len(runs) {
			var run Run
			input := RunInput{ProjectID: scopes[i].identity.Project.ID, ModelID: "studio-chat", Input: fmt.Sprintf("owned admission fixture %d", i)}
			capacityRequest(t, ctx, scopes[i], "POST", server.URL+"/api/v1/runs", newID("slot"), input, &run, 201)
			runs[i] = "/api/v1/runs/" + run.ID + "/events?after=0"
			runIDs[i] = run.ID
		}
	}
	sseSocketWait(t, func() bool { return posts.Load() == 4 })
	bodies := make([]io.ReadCloser, 0, global+3)
	hold := func(i int, path string) {
		bodies = append(bodies, sseSocketOpen(t, ctx, server, scopes[i], path, "").Body)
	}
	release := func() {
		for _, b := range bodies {
			b.Close()
		}
		bodies = bodies[:0]
	}
	defer release()
	denied := func(i int, path string, want int, wantCode string) {
		t.Helper()
		var response struct{ Error APIError }
		capacityRequest(t, ctx, scopes[i], "GET", server.URL+path, "", nil, &response, want)
		if response.Error.Code != wantCode {
			t.Fatalf("admission returned incorrect public error at HTTP %d", want)
		}
	}
	for i := range runs {
		for j := range perScope {
			if i == 0 && j == 0 {
				hold(i, projects[i])
			} else {
				hold(i, runs[i])
			}
		}
		if a.scopedStreams(scopes[i].identity.Workspace.ID) != perScope {
			t.Fatal("workspace did not hold its actual admitted connections")
		}
		denied(i, projects[i], 429, "ADMISSION_STREAM_LIMIT")
	}
	if len(a.streamSlots) != global {
		t.Fatal("admitted clients did not fill the shared global limit")
	}
	denied(4, projects[4], 503, "STREAM_LIMIT")
	bodies[1].Close() // one active Run slot returns to both limits
	sseSocketWait(t, func() bool {
		return len(a.streamSlots) == global-1 && a.scopedStreams(scopes[0].identity.Workspace.ID) == perScope-1
	})
	hold(4, projects[4]) // a different workspace can spend the global slot
	denied(0, runs[0], 503, "STREAM_LIMIT")
	release()
	sseSocketWait(t, func() bool {
		if len(a.streamSlots) != 0 {
			return false
		}
		for _, s := range scopes {
			if a.scopedStreams(s.identity.Workspace.ID) != 0 {
				return false
			}
		}
		return true
	})
	hold(0, runs[0])
	hold(4, projects[4])
	if len(a.streamSlots) != 2 || posts.Load() != 4 {
		t.Fatal("ordinary routes did not recover without extra model submissions")
	}
	release()
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 0 })
	releaseModels()
	for i, id := range runIDs {
		run := awaitAuthRun(t, scopes[i].client, server.URL, id)
		if run.Status != "COMPLETED" || run.Output != "done" || posts.Load() != 4 {
			t.Fatal("subscription admission affected the original model execution")
		}
	}
	t.Log("workspaces=5 concurrent=128 run_readers_at_peak=127 project_readers_at_peak=1 workspace_rejections=4 global_rejections=2 end_active=0 upstream_posts=4")
}
