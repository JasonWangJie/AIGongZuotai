package studio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newIsolatedApp(t *testing.T, c Config) (*App, error) {
	t.Helper()
	if c.AssetDir == "" {
		c.AssetDir = t.TempDir()
	}
	if c.ReferenceDir == "" {
		c.ReferenceDir = t.TempDir()
	}
	return New(c)
}

func testApp(t *testing.T, c Config) (*App, *httptest.Server) {
	t.Helper()
	if c.ReferenceDir == "" {
		c.ReferenceDir = t.TempDir()
	}
	if c.DemoTokenDelay == 0 {
		c.DemoTokenDelay = time.Millisecond
	}
	if c.DemoTaskDuration == 0 {
		c.DemoTaskDuration = 25 * time.Millisecond
	}
	if c.PollInterval == 0 {
		c.PollInterval = 5 * time.Millisecond
	}
	if c.Heartbeat == 0 {
		c.Heartbeat = 20 * time.Millisecond
	}
	a, err := newIsolatedApp(t, c)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(a.Handler())
	t.Cleanup(func() { a.Close(); s.Close() })
	return a, s
}

func request(t *testing.T, s *httptest.Server, method, path, key string, body any) (int, []byte, http.Header) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, s.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}

func runInput() RunInput {
	return RunInput{ProjectID: "prj_demo", ModelID: "studio-chat", Input: "测试创作想法"}
}
func taskInput(kind string) TaskInput {
	in := TaskInput{ProjectID: "prj_demo", Kind: kind, ModelID: "studio-" + kind}
	in.Inputs.Prompt = "本地演示的山川与月光"
	in.Parameters.AspectRatio = "16:9"
	in.Parameters.Count = 1
	return in
}
func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("JSON: %v (%s)", err, data)
	}
	return value
}
func waitRun(t *testing.T, s *httptest.Server, id string) Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, data, _ := request(t, s, "GET", "/api/v1/runs/"+id+"/snapshot", "", nil)
		if status != 200 {
			t.Fatalf("snapshot: %d %s", status, data)
		}
		r := decode[Run](t, data)
		if terminal(r.Status) {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("run did not terminate")
	return Run{}
}
func waitTask(t *testing.T, s *httptest.Server, id string) Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, data, _ := request(t, s, "GET", "/api/v1/tasks/"+id, "", nil)
		task := decode[Task](t, data)
		if terminal(task.Status) {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not terminate")
	return Task{}
}

func TestRunIdempotencyLifecycleAndReplay(t *testing.T) {
	_, s := testApp(t, Config{})
	status, data, _ := request(t, s, "POST", "/api/v1/runs", "run-key-0001", runInput())
	if status != 201 {
		t.Fatalf("create: %d %s", status, data)
	}
	created := decode[Run](t, data)
	status, data, headers := request(t, s, "POST", "/api/v1/runs", "run-key-0001", runInput())
	if status != 201 || decode[Run](t, data).ID != created.ID || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("same payload did not replay original run")
	}
	changed := runInput()
	changed.Input = "different"
	status, _, _ = request(t, s, "POST", "/api/v1/runs", "run-key-0001", changed)
	if status != 409 {
		t.Fatalf("payload conflict = %d", status)
	}
	completed := waitRun(t, s, created.ID)
	if completed.Status != "COMPLETED" || completed.Output == "" || completed.Seq < 2 {
		t.Fatalf("bad result: %+v", completed)
	}
	_, data, _ = request(t, s, "GET", "/api/v1/runs/"+created.ID+"/events?after=0", "", nil)
	if !bytes.Contains(data, []byte("event: message.delta")) || !bytes.Contains(data, []byte("event: run.completed")) {
		t.Fatalf("missing SSE events: %s", data)
	}
	var output strings.Builder
	var last uint64
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Seq   uint64 `json:"seq"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(line[6:]), &event); err != nil {
			t.Fatal(err)
		}
		if event.Seq <= last {
			t.Fatalf("non-increasing event sequence %d <= %d", event.Seq, last)
		}
		last = event.Seq
		output.WriteString(event.Delta)
	}
	if output.String() != completed.Output {
		t.Fatal("event replay does not reconstruct snapshot")
	}
	_, data, _ = request(t, s, "POST", "/api/v1/runs/"+created.ID+"/cancel", "", nil)
	if got := decode[Run](t, data); got.Status != "COMPLETED" || got.Seq != completed.Seq {
		t.Fatal("cancel changed completed run")
	}
	_, data, _ = request(t, s, "GET", "/api/v1/runs", "", nil)
	if got := decode[struct {
		Items []Run `json:"items"`
	}](t, data); len(got.Items) != 1 {
		t.Fatal("refresh list missing run")
	}
}

func TestReplayWindowSnapshotAndLastEventIDPrecedence(t *testing.T) {
	_, s := testApp(t, Config{ReplaySize: 3})
	_, data, _ := request(t, s, "POST", "/api/v1/runs", "replay-key-0001", runInput())
	run := waitRun(t, s, decode[Run](t, data).ID)
	_, data, _ = request(t, s, "GET", "/api/v1/runs/"+run.ID+"/events?after=0", "", nil)
	if !bytes.Contains(data, []byte("event: run.snapshot")) || !bytes.Contains(data, []byte(`"status":"COMPLETED"`)) {
		t.Fatalf("expired window must return complete snapshot: %s", data)
	}
	req, _ := http.NewRequest("GET", s.URL+"/api/v1/runs/"+run.ID+"/events?after=0", nil)
	req.Header.Set("Last-Event-ID", jsonNumber(run.Seq-1))
	response, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ = io.ReadAll(response.Body)
	if !bytes.Contains(data, []byte("run.completed")) || bytes.Contains(data, []byte("run.snapshot")) || bytes.Contains(data, []byte("message.delta")) {
		t.Fatalf("Last-Event-ID did not override stale query cursor: %s", data)
	}
}
func jsonNumber(value uint64) string { b, _ := json.Marshal(value); return string(b) }

func TestDisconnectDoesNotCancelAndExplicitCancelIsTerminal(t *testing.T) {
	_, s := testApp(t, Config{DemoTokenDelay: 3 * time.Millisecond})
	_, data, _ := request(t, s, "POST", "/api/v1/runs", "disconnect-key-01", runInput())
	run := decode[Run](t, data)
	response, err := s.Client().Get(s.URL + "/api/v1/runs/" + run.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "event: message.delta") {
			break
		}
	}
	_ = response.Body.Close()
	result := waitRun(t, s, run.ID)
	if result.Status != "COMPLETED" {
		t.Fatal("browser disconnect cancelled generation")
	}
	_, data, _ = request(t, s, "POST", "/api/v1/runs", "cancel-key-0001", runInput())
	run = decode[Run](t, data)
	_, data, _ = request(t, s, "POST", "/api/v1/runs/"+run.ID+"/cancel", "", nil)
	cancelled := decode[Run](t, data)
	if cancelled.Status != "CANCELLED" {
		t.Fatal("explicit cancellation failed")
	}
	time.Sleep(25 * time.Millisecond)
	_, data, _ = request(t, s, "GET", "/api/v1/runs/"+run.ID+"/snapshot", "", nil)
	got := decode[Run](t, data)
	if got.Status != "CANCELLED" || got.Seq != cancelled.Seq || got.Output != cancelled.Output {
		t.Fatal("cancelled run was resurrected")
	}
}

func TestConcurrentIdempotencyCreatesExactlyOneRun(t *testing.T) {
	a, _ := testApp(t, Config{})
	var wg sync.WaitGroup
	ids := make(chan string, 24)
	errors := make(chan error, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, e := a.createRun(runInput(), "concurrent-key")
			if e != nil {
				errors <- e
			} else {
				ids <- r.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate run IDs")
		}
	}
	if len(a.listRuns()) != 1 {
		t.Fatal("duplicate run records")
	}
}

func TestTaskLifecycleArtifactsAndTerminalCancel(t *testing.T) {
	_, s := testApp(t, Config{})
	input := taskInput("image")
	input.Parameters.Count = 2
	status, data, _ := request(t, s, "POST", "/api/v1/tasks", "task-key-0001", input)
	if status != 202 {
		t.Fatalf("create: %d %s", status, data)
	}
	created := decode[Task](t, data)
	_, data, headers := request(t, s, "POST", "/api/v1/tasks", "task-key-0001", input)
	if decode[Task](t, data).ID != created.ID || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("task not idempotent")
	}
	input.Inputs.Prompt = "changed"
	status, _, _ = request(t, s, "POST", "/api/v1/tasks", "task-key-0001", input)
	if status != 409 {
		t.Fatal("task mismatch not rejected")
	}
	result := waitTask(t, s, created.ID)
	if result.Status != "SUCCEEDED" || result.Progress != 100 || len(result.Artifacts) != 2 || result.Version < 3 {
		t.Fatalf("bad result: %+v", result)
	}
	status, data, headers = request(t, s, "GET", result.Artifacts[0].URL, "", nil)
	if status != 200 || headers.Get("Content-Type") != "image/svg+xml" || !bytes.Contains(data, []byte("LOCAL DEMO")) {
		t.Fatal("local SVG unavailable")
	}
	_, data, _ = request(t, s, "POST", "/api/v1/tasks/"+result.ID+"/cancel", "", nil)
	if got := decode[Task](t, data); got.Status != "SUCCEEDED" || got.Version != result.Version {
		t.Fatal("cancel changed terminal task")
	}
	_, data, _ = request(t, s, "POST", "/api/v1/tasks", "video-key-0001", taskInput("video"))
	video := waitTask(t, s, decode[Task](t, data).ID)
	if len(video.Artifacts) != 1 || video.Artifacts[0].Kind != "poster" || !strings.Contains(video.Artifacts[0].Title, "非真实视频") {
		t.Fatal("video result must honestly be a poster")
	}
}

func TestWorkspaceEventsAndCancelDuringTask(t *testing.T) {
	_, s := testApp(t, Config{DemoTaskDuration: 200 * time.Millisecond})
	_, data, _ := request(t, s, "POST", "/api/v1/tasks", "task-cancel-key1", taskInput("video"))
	task := decode[Task](t, data)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/api/v1/workspaces/ws_demo/events", nil)
	response, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), task.ID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("workspace initial event snapshot missing")
	}
	_, data, _ = request(t, s, "POST", "/api/v1/tasks/"+task.ID+"/cancel", "", nil)
	cancelled := decode[Task](t, data)
	time.Sleep(40 * time.Millisecond)
	_, data, _ = request(t, s, "GET", "/api/v1/tasks/"+task.ID, "", nil)
	got := decode[Task](t, data)
	if got.Status != "CANCELLED" || got.Version != cancelled.Version || len(got.Artifacts) != 0 {
		t.Fatal("late provider update overwrote cancelled task")
	}
}

func TestOriginHostBodyAndInputGuards(t *testing.T) {
	_, s := testApp(t, Config{})
	for _, tc := range []struct {
		name, origin, host string
		want               int
	}{
		{"same origin", s.URL, "", 200}, {"vite", "http://127.0.0.1:5173", "", 200}, {"malicious", "https://evil.example", "", 403}, {"null", "null", "", 403}, {"rebinding", "", "evil.example", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", s.URL+"/api/v1/bootstrap", nil)
			req.Header.Set("Origin", tc.origin)
			if tc.host != "" {
				req.Host = tc.host
			}
			res, err := s.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("got %d want %d", res.StatusCode, tc.want)
			}
		})
	}
	status, _, _ := request(t, s, "POST", "/api/v1/runs", "", runInput())
	if status != 400 {
		t.Fatal("missing idempotency key accepted")
	}
	bad := runInput()
	bad.ProjectID = "another-workspace"
	status, _, _ = request(t, s, "POST", "/api/v1/runs", "bad-project-key", bad)
	if status != 422 {
		t.Fatal("foreign project accepted")
	}
	bad = runInput()
	bad.Input = strings.Repeat("x", maxRequestBody)
	status, _, _ = request(t, s, "POST", "/api/v1/runs", "oversized-body-key", bad)
	if status != 413 {
		t.Fatalf("oversized body status %d", status)
	}
	status, _, _ = request(t, s, "GET", "/api/v1/does-not-exist", "", nil)
	if status != 404 {
		t.Fatal("unknown API not 404")
	}
}

func TestBoundedAdmissionAndSlowSubscriber(t *testing.T) {
	a, _ := testApp(t, Config{RunWorkers: 1, QueueSize: 1, DemoTokenDelay: time.Second, SubscriberBuffer: 1, ReplaySize: 2})
	first, _, err := a.createRun(runInput(), "queue-first")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		running := a.runs[first.ID].Run.Status == "RUNNING"
		a.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, err := a.createRun(runInput(), "queue-second"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.createRun(runInput(), "queue-third"); err == nil {
		t.Fatal("unbounded admission")
	}
	if len(a.listRuns()) != 2 {
		t.Fatal("rejected run retained a phantom record")
	}
	a.mu.Lock()
	r := a.runs[first.ID]
	ch := make(chan event, 1)
	r.Subscribers[ch] = true
	for range 4 {
		r.Run.Seq++
		a.publishRunLocked(r, "message.delta", map[string]string{"delta": "test"})
	}
	_, present := r.Subscribers[ch]
	if len(r.Events) != 2 {
		t.Error("replay ring exceeded capacity")
	}
	a.mu.Unlock()
	if present {
		t.Fatal("slow subscriber was not disconnected")
	}
	<-ch
	if _, open := <-ch; open {
		t.Fatal("overflow channel not closed")
	}
}

type chatFunc func(context.Context, string, func(string) error) error

func (f chatFunc) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	return f(ctx, prompt, emit)
}

func TestLargeOutputRetainsPartialFailure(t *testing.T) {
	a, s := testApp(t, Config{})
	a.chat = chatFunc(func(_ context.Context, _ string, emit func(string) error) error {
		for range 129 {
			if err := emit(strings.Repeat("x", 1024)); err != nil {
				return err
			}
		}
		return nil
	})
	_, data, _ := request(t, s, "POST", "/api/v1/runs", "large-output-key", runInput())
	run := waitRun(t, s, decode[Run](t, data).ID)
	if run.Status != "FAILED" || len(run.Output) != maxRunOutputBytes || run.Error == nil {
		t.Fatalf("unexpected capped output: status=%s size=%d error=%v", run.Status, len(run.Output), run.Error)
	}
}

func TestFrontendFallbackDoesNotServeSecretsOrUnknownAPI(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	if err := os.Mkdir(dist, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>workbench fixture</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, ".env"), []byte("secret fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	_, s := testApp(t, Config{WebDir: dist})
	status, data, _ := request(t, s, "GET", "/project/demo", "", nil)
	if status != 200 || !bytes.Contains(data, []byte("workbench fixture")) {
		t.Fatal("SPA fallback unavailable")
	}
	for _, path := range []string{"/.env", "/api/v1/unknown", "/missing.js"} {
		status, data, _ = request(t, s, "GET", path, "", nil)
		if status != 404 || bytes.Contains(data, []byte("secret fixture")) || bytes.Contains(data, []byte("workbench fixture")) {
			t.Fatalf("unsafe fallback %s: %d %s", path, status, data)
		}
	}
}
