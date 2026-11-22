package studio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresRunStreamReplayAcrossLiveHandoff(t *testing.T) {
	testRunStreamReplayAcrossLiveHandoff(t, 0)
}

func TestPostgresRunReplayPoolAcrossLiveHandoff(t *testing.T) {
	testRunStreamReplayAcrossLiveHandoff(t, 2)
}

func testRunStreamReplayAcrossLiveHandoff(t *testing.T, replayConnections int) {
	t.Helper()
	const before, total, after = 70, 87, 3
	cfg := Config{DatabaseURL: testDatabase(t), AuthMode: "required", ReplaySize: 1, SubscriberBuffer: 1, RunReplayReadConnections: replayConnections}
	app, server := testApp(t, cfg)
	client, stranger := authClient(t), authClient(t)
	owner := registerFixture(t, client, server.URL, "run-stream-owner@example.test")
	other := registerFixture(t, stranger, server.URL, "run-stream-other@example.test")
	entered, resume := make(chan struct{}), make(chan struct{})
	doneProvider := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(doneProvider)
	delta := func(i int) string { return fmt.Sprintf("%03d·", i) }
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		for i := 1; i <= total; i++ {
			if i == before+1 {
				close(entered)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-resume:
				}
			}
			if err := emit(delta(i)); err != nil {
				return err
			}
		}
		return nil
	})
	run := executionRun(t, server, client, owner, "studio-chat", "bounded replay fixture", "run-stream-handoff")
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not reach the replay boundary")
	}
	path := "/api/v1/runs/" + run.ID + "/events"
	// Stall the HTTP writer only after the reader is attached and the first
	// page has been read. More than one page is pending, and the one-slot
	// subscriber will close while new deltas and the terminal commit arrive.
	req := httptest.NewRequest("GET", server.URL+path+"?after=999999", nil)
	for _, cookie := range client.Jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-Project-ID", owner.Project.ID)
	req.Header.Set("Last-Event-ID", strconv.Itoa(after))
	writer := &heldRunSSEWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	releaseHTTP := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(releaseHTTP)
	exited := make(chan struct{})
	go func() { defer close(exited); app.Handler().ServeHTTP(writer, req) }()
	select {
	case <-writer.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not reach the first write")
	}
	doneProvider()
	completed := awaitAuthRun(t, client, server.URL, run.ID)
	if completed.Status != "COMPLETED" || completed.Seq != total+1 {
		t.Fatal("producer did not commit each increment plus the terminal event")
	}
	releaseHTTP()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("SSE reader failed to catch up to terminal")
	}
	assertReplay := func(raw []byte, cursorAfter int) {
		t.Helper()
		reader := bufio.NewReader(bytes.NewReader(raw))
		if bytes.Count(raw, []byte("event: ")) != total-cursorAfter+1 {
			t.Fatal("replay duplicated or lost events")
		}
		for seq := cursorAfter + 1; seq <= total+1; seq++ {
			name, cursor, body := readSSEFixture(t, reader)
			if cursor != strconv.Itoa(seq) || string(body["run_id"]) != strconv.Quote(run.ID) || string(body["seq"]) != cursor {
				t.Fatal("replay lost resource identity or contiguous order", name, cursor)
			}
			if seq == total+1 {
				if name != "run.completed" {
					t.Fatal("replay lost the terminal transition")
				}
			} else if name != "message.delta" || string(body["delta"]) != strconv.Quote(delta(seq)) {
				t.Fatal("replay returned a different original increment", cursor)
			}
		}
	}
	if writer.Code != 200 {
		t.Fatal("Last-Event-ID did not take precedence over query cursor", writer.Code)
	}
	assertReplay(writer.Body.Bytes(), after)
	executionState(t, app, 0, 0)
	app.mu.Lock()
	delete(app.runs, run.ID)
	app.mu.Unlock()
	code, raw, _ := authRequest(t, client, server.URL, "GET", path+"?after=3", "", owner.Project.ID, nil)
	if code != http.StatusOK {
		t.Fatal("cold persisted replay rejected", code)
	}
	assertReplay(raw, after)
	code, raw, _ = authRequest(t, client, server.URL, "GET", path+"?after=0", "", owner.Project.ID, nil)
	if code != http.StatusOK {
		t.Fatal("explicit zero cursor rejected", code)
	}
	assertReplay(raw, 0)
	code, raw, _ = authRequest(t, client, server.URL, "GET", path, "", owner.Project.ID, nil)
	if code != http.StatusOK || bytes.Count(raw, []byte("event: ")) != 1 {
		t.Fatal("cold stream without a cursor did not return one event", code)
	}
	name, cursor, body := readSSEFixture(t, bufio.NewReader(bytes.NewReader(raw)))
	if name != "run.snapshot" || cursor != strconv.Itoa(total+1) || string(body["output"]) != strconv.Quote(completed.Output) {
		t.Fatal("cold stream without a cursor lost its final snapshot")
	}
	code, _, _ = authRequest(t, stranger, server.URL, "GET", path, "", other.Project.ID, nil)
	if code != http.StatusNotFound {
		t.Fatal("foreign reader could access the Run stream", code)
	}
	code, _, _ = authRequest(t, client, server.URL, "GET", path+"?after=999999", "", owner.Project.ID, nil)
	if code != http.StatusConflict {
		t.Fatal("ahead cursor did not produce conflict", code)
	}
}

func TestPostgresRunStreamExpiredAndLegacySnapshots(t *testing.T) {
	testRunStreamExpiredAndLegacySnapshots(t, 0)
}

func TestPostgresRunReplayPoolExpiredAndLegacySnapshots(t *testing.T) {
	testRunStreamExpiredAndLegacySnapshots(t, 1)
}

func testRunStreamExpiredAndLegacySnapshots(t *testing.T, replayConnections int) {
	t.Helper()
	const count = 280
	cfg := Config{DatabaseURL: testDatabase(t), RunReplayReadConnections: replayConnections}
	app, server := testApp(t, cfg)
	generated := make(chan struct{})
	app.chat = chatFunc(func(_ context.Context, _ string, emit func(string) error) error {
		defer close(generated)
		for i := 0; i < count; i++ {
			if err := emit("x"); err != nil {
				return err
			}
		}
		return nil
	})
	code, raw, _ := request(t, server, "POST", "/api/v1/runs", "stream-retention-fixture", runInput())
	if code != http.StatusCreated {
		t.Fatal("run creation failed", code)
	}
	id := decode[Run](t, raw).ID
	select {
	case <-generated:
	case <-time.After(time.Minute):
		t.Fatal("provider failed to finish the full retention window")
	}
	run := awaitAuthRun(t, server.Client(), server.URL, id)
	if run.Seq != count+1 || run.Output != strings.Repeat("x", count) {
		t.Fatal("durable stream did not finish as expected")
	}
	conn := deliveryAdmin(t, cfg.DatabaseURL)
	if outboxCount(t, conn, "SELECT floor_seq FROM studio_run_streams WHERE run_id=$1", id) == 0 {
		t.Fatal("fixture did not exceed the persisted event window")
	}
	legacy := outboxFixtureRun(newID("run"))
	legacy.Seq, legacy.Output, legacy.Status = 12345, "old checkpoint", "INTERRUPTED"
	document, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(context.Background(), "INSERT INTO studio_runs(id,workspace_id,project_id,document) VALUES($1,$2,$3,$4)", legacy.ID, legacy.WorkspaceID, legacy.ProjectID, document)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []Run{run, legacy} {
		path := "/api/v1/runs/" + expected.ID + "/events?after=1"
		code, raw, _ = request(t, server, "GET", path, "", nil)
		if code != http.StatusOK || bytes.Count(raw, []byte("event: ")) != 1 {
			t.Fatal("missing/expired history did not yield one reset snapshot", code)
		}
		name, cursor, body := readSSEFixture(t, bufio.NewReader(bytes.NewReader(raw)))
		if name != "run.snapshot" || cursor != strconv.FormatUint(expected.Seq, 10) || string(body["output"]) != strconv.Quote(expected.Output) {
			t.Fatal("reset did not cover the persisted visible output", name, cursor)
		}
	}
}
