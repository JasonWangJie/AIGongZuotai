package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamSessionProjectObserverRequiresScopedEvents(t *testing.T) {
	var owner testIdentity
	owner.User.ID, owner.Workspace.ID, owner.Project.ID = "user", "workspace", "project"
	cursor := durableCursor{Version: 1, StorageID: "store", UserID: "user", WorkspaceID: "workspace", ProjectID: "project"}
	first := encodeDurableCursor(cursor)
	initial := fmt.Sprintf(": connected\n\nid: %s\nevent: stream.reset\ndata: {\"reason\":\"initial\",\"cursor\":%q}\n\n", first, first)
	msg := DurableEvent{Seq: 1, Kind: "audit.created", ResourceType: "draft", ResourceID: "project", ResourceVersion: 1, Action: "draft.saved"}
	format := func(c durableCursor, e DurableEvent) string {
		raw, _ := json.Marshal(e)
		return fmt.Sprintf("id: %s\nevent: resource.changed\ndata: %s\n\n", encodeDurableCursor(c), raw)
	}
	cursor.Seq = 1
	good := format(cursor, msg)
	observer := &sessionSSEReader{}
	if err := observer.readProject(strings.NewReader(initial+good), owner, "store"); err != nil || observer.progress.Load() != 1 {
		t.Fatal("valid scoped event was not verified")
	}
	wrong := cursor
	wrong.UserID = "other"
	wrongResource := msg
	wrongResource.ResourceID = "other"
	for _, bad := range []string{good, initial + good + good, initial + format(wrong, msg), initial + format(cursor, wrongResource), initial + "event: broken\n"} {
		if (&sessionSSEReader{}).readProject(strings.NewReader(bad), owner, "store") == nil {
			t.Fatal("incorrect scoped or incomplete stream accepted")
		}
	}
}

func TestPostgresStreamSessionSlowSocketAndOriginalReplay(t *testing.T) {
	db := sseSocketDatabase(t)
	g := &sseSocketGateway{first: make(chan struct{}), flood: make(chan struct{}), finish: make(chan struct{})}
	up := httptest.NewServer(g)
	t.Cleanup(up.Close)
	a, server := testApp(t, Config{Mode: "live", BaseURL: up.URL + "/v1", ChatModel: "socket-fixture", APIKey: "local-fixture", Heartbeat: time.Second,
		DatabaseURL: db, AuthMode: "required", Admission: AdmissionLimits{GlobalStreams: 192, WorkspaceStreams: 96}, RunReplayReadConnections: 1})
	first := sync.OnceFunc(func() { close(g.first) })
	flood := sync.OnceFunc(func() { close(g.flood) })
	finish := sync.OnceFunc(func() { close(g.finish) })
	defer first()
	defer flood()
	defer finish()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	scope := sseSocketScope(t, server, 0, true)
	key := newID("sessionrun")
	input := RunInput{ProjectID: scope.identity.Project.ID, ModelID: "studio-chat", Input: "fixture socket progress"}
	var run Run
	capacityRequest(t, ctx, scope, "POST", server.URL+"/api/v1/runs", key, input, &run, 201)
	path := "/api/v1/runs/" + run.ID + "/events"
	slow, timeout := sseSocketPausedReader(t, a, scope, run.ID)
	live := sseSocketOpen(t, ctx, server, scope, path+"?after=0", "")
	defer live.Body.Close()
	fast := bufio.NewReader(live.Body)
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 2 })
	before, drops := a.obs.slowWrites.Load(), a.obs.slowConsumers.Load()
	first()
	cursor := sseSocketExpect(t, slow, run.ID, 1)
	sseSocketExpect(t, fast, run.ID, 1)
	flood()
	for seq := 2; seq <= socketFloodDeltas+1; seq++ {
		sseSocketExpect(t, fast, run.ID, seq)
	}
	var wait time.Duration
	select {
	case wait = <-timeout:
	case <-time.After(12 * time.Second):
		t.Fatal("missing physical write timeout")
	}
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 1 && a.obs.slowWrites.Load() == before+1 })
	if a.obs.slowConsumers.Load() != drops {
		t.Fatal("application mailbox filled before socket eviction")
	}
	resumed := sseSocketOpen(t, ctx, server, scope, path, cursor)
	defer resumed.Body.Close()
	replay := bufio.NewReader(resumed.Body)
	for seq := 2; seq <= socketFloodDeltas+1; seq++ {
		sseSocketExpect(t, replay, run.ID, seq)
	}
	_, _ = sessionRounds(t, a, 1)
	finish()
	sseSocketExpect(t, fast, run.ID, socketFloodDeltas+2)
	sseSocketExpect(t, replay, run.ID, socketFloodDeltas+2)
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 0 })
	got := awaitAuthRun(t, scope.client, server.URL, run.ID)
	var output strings.Builder
	for i := 1; i <= socketFloodDeltas+1; i++ {
		output.WriteString(sseSocketDelta(i))
	}
	if got.Seq != socketFloodDeltas+2 || got.Status != "COMPLETED" || got.Output != output.String() || g.posts.Load() != 1 {
		t.Fatal("slow reader changed original output, sequence or upstream call count")
	}
	t.Logf("paused_readers=1 normal_readers=1 actual_socket_timeout_ms=%.2f timeout_evictions=1 buffer_drops=0 resume_after=1 verified_resumed_deltas=%d completed_runs=1 upstream_posts=1", float64(wait.Microseconds())/1000, socketFloodDeltas)
}
