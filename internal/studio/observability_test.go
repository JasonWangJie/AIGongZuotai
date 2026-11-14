package studio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const metricsFixtureToken = "fixture-operator-token-independent-0001"

type safeLogBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *safeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *safeLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

func observerFixture(t *testing.T, c Config) *App {
	t.Helper()
	app, err := newIsolatedApp(t, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}
func observedRequest(handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, nil)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestObservabilityConfigRequiresIndependentOperatorToken(t *testing.T) {
	for name, c := range map[string]Config{
		"too short":       {MetricsToken: "short"},
		"too long":        {MetricsToken: strings.Repeat("a", 257)},
		"whitespace":      {MetricsToken: strings.Repeat("a", 31) + " "},
		"LLM key reuse":   {MetricsToken: metricsFixtureToken, APIKey: metricsFixtureToken},
		"media key reuse": {MetricsToken: metricsFixtureToken, AsyncImageAPIKey: metricsFixtureToken},
	} {
		t.Run(name, func(t *testing.T) {
			if validateObservabilityConfig(c) == nil {
				t.Fatal("unsafe metrics configuration accepted")
			}
		})
	}
	for _, c := range []Config{{}, {MetricsToken: metricsFixtureToken}} {
		if err := validateObservabilityConfig(c); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHealthAndReadinessUseCachedDependencyState(t *testing.T) {
	app := observerFixture(t, Config{})
	handler := app.Handler()
	if got := observedRequest(handler, "GET", "/healthz", ""); got.Code != 200 || got.Body.String() != "{\"status\":\"alive\"}\n" {
		t.Fatalf("liveness %d %s", got.Code, got.Body.String())
	}
	if got := observedRequest(handler, "GET", "/readyz", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"ready":true`) {
		t.Fatal("ready process was not ready")
	}
	// This intentionally has no SQL connection: readiness and the rejected API
	// path must read the cached breaker instead of probing dependencies.
	store := &postgresStore{}
	store.failed.Store(true)
	app.store = store
	defer func() { app.store = nil }()
	for range 50 {
		if got := observedRequest(handler, "GET", "/healthz", ""); got.Code != 200 {
			t.Fatal("DB outage killed liveness")
		}
		if got := observedRequest(handler, "GET", "/readyz", ""); got.Code != 503 || !strings.Contains(got.Body.String(), "database_unavailable") {
			t.Fatal("readiness ignored database breaker")
		}
	}
	if got := observedRequest(handler, "GET", "/api/v1/bootstrap", ""); got.Code != 503 || !strings.Contains(got.Body.String(), "DATABASE_UNAVAILABLE") {
		t.Fatal("API bypassed failed-closed dependency")
	}
	app.store = nil
	app.cancel()
	if got := observedRequest(handler, "GET", "/healthz", ""); got.Code != 200 {
		t.Fatal("stopping process should remain live while serving")
	}
	if got := observedRequest(handler, "GET", "/readyz", ""); got.Code != 503 || !strings.Contains(got.Body.String(), "stopping") {
		t.Fatal("stopping process remained ready")
	}
}

func TestMetricsAuthenticationRequestIDAndBoundedSensitiveLabels(t *testing.T) {
	disabled := observerFixture(t, Config{})
	if got := observedRequest(disabled.Handler(), "GET", "/metrics", "Bearer "+metricsFixtureToken); got.Code != 404 {
		t.Fatal("metrics enabled without operator configuration")
	}
	app := observerFixture(t, Config{MetricsToken: metricsFixtureToken})
	var logs safeLogBuffer
	app.obs.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := app.Handler()
	for _, token := range []string{"", "Bearer incorrect-secret", "Basic " + metricsFixtureToken} {
		got := observedRequest(handler, "GET", "/metrics", token)
		if got.Code != 401 || got.Header().Get("WWW-Authenticate") == "" {
			t.Fatal("metrics accepted an absent/wrong operator credential")
		}
	}
	for i := range 120 {
		req := httptest.NewRequest(fmt.Sprintf("UNTRUSTED%d", i), fmt.Sprintf("http://127.0.0.1/api/v1/private-id-%d?token=secret-query", i), strings.NewReader("private prompt body"))
		req.Header.Set("X-Request-ID", "attacker-request-id")
		req.Header.Set("Cookie", "studio_session=private-session-cookie")
		req.Header.Set("Authorization", "Bearer secret-provider-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		id := w.Header().Get("X-Request-ID")
		if !regexp.MustCompile(`^req_[a-f0-9]{32}$`).MatchString(id) {
			t.Fatal("request ID trusted client input or had unbounded format")
		}
		var body struct {
			Error struct {
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.RequestID != id {
			t.Fatal("error request_id does not match response header")
		}
	}
	metrics := observedRequest(handler, "GET", "/metrics", "Bearer "+metricsFixtureToken)
	if metrics.Code != 200 || metrics.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatal("invalid Prometheus response format")
	}
	combined := logs.String() + metrics.Body.String()
	for _, forbidden := range []string{"attacker-request-id", "private-id-", "secret-query", "private prompt", "private-session-cookie", "secret-provider-token", "incorrect-secret", metricsFixtureToken, "UNTRUSTED"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("sensitive/unbounded label escaped: %s", forbidden)
		}
	}
	if !strings.Contains(metrics.Body.String(), `route="other",method="other",status="4xx"} 120`) {
		t.Fatal("untrusted routes/methods did not collapse to bounded enums")
	}
	if !strings.Contains(logs.String(), `"error_class":"not_found"`) || !strings.Contains(logs.String(), `"request_id":"req_`) {
		t.Fatal("safe diagnostic fields missing")
	}
	validatePrometheusFixture(t, metrics.Body.String())
	// Reset belongs to process construction, not tenant identity changes.
	fresh := observerFixture(t, Config{MetricsToken: metricsFixtureToken})
	if strings.Contains(fresh.prometheusText(), `route="other",method="other"`) {
		t.Fatal("request counters leaked across app restart")
	}
}

func validatePrometheusFixture(t *testing.T, text string) {
	t.Helper()
	if !strings.HasSuffix(text, "\n") {
		t.Fatal("Prometheus last line lacks LF")
	}
	types, samples := map[string]bool{}, map[string]bool{}
	buckets := map[string]float64{}
	counts, infinities := map[string]float64{}, map[string]float64{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			if len(fields) != 4 || types[fields[2]] {
				t.Fatal("duplicate/invalid TYPE")
			}
			types[fields[2]] = true
			continue
		}
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok || samples[key] {
			t.Fatal("missing value or duplicate metric sample")
		}
		samples[key] = true
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n < 0 {
			t.Fatalf("invalid numeric sample %q", line)
		}
		if strings.HasPrefix(key, "studio_http_request_duration_seconds_bucket{") {
			prefix, le, ok := strings.Cut(key, ",le=")
			if !ok {
				t.Fatal("histogram bucket lacks bound")
			}
			if n < buckets[prefix] {
				t.Fatal("histogram buckets are not cumulative")
			}
			buckets[prefix] = n
			if le == `"+Inf"}` {
				infinities[prefix] = n
			}
		}
		if strings.HasPrefix(key, "studio_http_request_duration_seconds_count{") {
			prefix := strings.Replace(strings.TrimSuffix(key, "}"), "_count{", "_bucket{", 1)
			counts[prefix] = n
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for key, count := range counts {
		if infinities[key] != count {
			t.Fatal("+Inf bucket must equal histogram count")
		}
	}
}

func TestRuntimeWorkspaceScopeDoesNotExposeOtherWorkspaceActivity(t *testing.T) {
	app := observerFixture(t, Config{MetricsToken: metricsFixtureToken})
	now := time.Now()
	app.mu.Lock()
	app.runs["own-run"] = &runRecord{Run: Run{ID: "own-run", WorkspaceID: "ws_demo", ProjectID: "prj_demo", ModelID: "studio-chat", Status: "QUEUED", CreatedAt: now.Add(-10 * time.Second)}, Subscribers: map[chan event]bool{}}
	app.runs["foreign-run"] = &runRecord{Run: Run{ID: "foreign-run", WorkspaceID: "foreign-secret-workspace", ProjectID: "foreign-project", ModelID: "studio-chat", Status: "QUEUED", Prompt: "foreign-private-prompt", CreatedAt: now.Add(-100 * time.Second)}, Subscribers: map[chan event]bool{}}
	app.tasks["own-task"] = &taskRecord{Task: Task{ID: "own-task", WorkspaceID: "ws_demo", ProjectID: "prj_demo", ModelID: "studio-image", Kind: "image", Status: "SUBMIT_UNKNOWN", CreatedAt: now}, InFlight: true}
	app.tasks["foreign-task"] = &taskRecord{Task: Task{ID: "foreign-task", WorkspaceID: "foreign-secret-workspace", ProjectID: "foreign-project", ModelID: "studio-video", Kind: "video", Status: "QUEUED", Prompt: "foreign-private-prompt", CreatedAt: now.Add(-100 * time.Second)}, InFlight: true}
	app.mu.Unlock()
	got := observedRequest(app.Handler(), "GET", "/api/v1/runtime?workspace_id=foreign-secret-workspace", "")
	var snapshot struct {
		Scope string            `json:"scope"`
		Ready bool              `json:"ready"`
		Runs  AdmissionResource `json:"runs"`
		Tasks AdmissionResource `json:"tasks"`
	}
	if json.Unmarshal(got.Body.Bytes(), &snapshot) != nil || got.Code != 200 {
		t.Fatalf("runtime %d %s", got.Code, got.Body.String())
	}
	if snapshot.Scope != "workspace" || !snapshot.Ready || snapshot.Runs.Active != 1 || snapshot.Runs.Queued != 1 || snapshot.Runs.OldestQueuedSeconds < 9 || snapshot.Runs.OldestQueuedSeconds > 30 || snapshot.Tasks.Active != 1 || snapshot.Tasks.Queued != 0 {
		t.Fatal("workspace counts included another tenant or lost unknown state")
	}
	for _, model := range snapshot.Tasks.Models {
		if model.ModelID == "studio-video" && model.Active != 0 {
			t.Fatal("foreign video occupancy leaked")
		}
	}
	if strings.Contains(got.Body.String(), "foreign") {
		t.Fatal("foreign tenant detail leaked")
	}
	metrics := observedRequest(app.Handler(), "GET", "/metrics", "Bearer "+metricsFixtureToken)
	if !strings.Contains(metrics.Body.String(), `studio_cached_active_resources{kind="chat",model="studio-chat"} 2`) || strings.Contains(metrics.Body.String(), "foreign") {
		t.Fatal("operator cached aggregate wrong or leaks identity")
	}
}

func TestSSEObservationPreservesStreamingAndWorkspaceAdmission(t *testing.T) {
	app := observerFixture(t, Config{Heartbeat: 10 * time.Millisecond, Admission: AdmissionLimits{WorkspaceStreams: 1}})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/workspaces/ws_demo/events", nil)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || response.Header.Get("X-Request-ID") == "" {
		t.Fatal("observer broke SSE headers")
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("SSE not flushed: %q %v", line, err)
	}
	// Another workspace remains independently admissible and must not appear
	// in the first workspace's runtime count.
	other := httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), principalKey{}, &principal{Workspace: Workspace{ID: "other"}}))
	if !app.acquireStream(httptest.NewRecorder(), other) {
		t.Fatal("workspace stream limit became global")
	}
	defer app.releaseObservedStream("other")
	got := observedRequest(app.Handler(), "GET", "/api/v1/runtime", "")
	var runtime struct {
		Streams struct{ Active, Limit int } `json:"streams"`
	}
	if json.Unmarshal(got.Body.Bytes(), &runtime) != nil || runtime.Streams.Active != 1 || runtime.Streams.Limit != 1 {
		t.Fatal("runtime stream occupancy leaked other workspace")
	}
	code, data, headers := request(t, server, "GET", "/api/v1/workspaces/ws_demo/events", "", nil)
	if code != 429 || headers.Get("Retry-After") != "3" || !bytes.Contains(data, []byte("ADMISSION_STREAM_LIMIT")) {
		t.Fatalf("SSE admission %d %s", code, data)
	}
	cancel()
	response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for app.scopedStreams("ws_demo") != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if app.scopedStreams("ws_demo") != 0 || len(app.streamSlots) != 1 {
		t.Fatal("disconnected stream did not release slot")
	}
}

type observedDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	flushes   int
	fail      bool
}

func (w *observedDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}
func (w *observedDeadlineWriter) FlushError() error {
	w.flushes++
	if w.fail {
		return fixtureWriteTimeout{}
	}
	w.ResponseRecorder.Flush()
	return nil
}

type fixtureWriteTimeout struct{}

func (fixtureWriteTimeout) Error() string   { return "fixture write timed out" }
func (fixtureWriteTimeout) Timeout() bool   { return true }
func (fixtureWriteTimeout) Temporary() bool { return true }

func TestObservedWriterPreservesResponseControllerAndSlowWriteCount(t *testing.T) {
	underlying := &observedDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	w := &observedWriter{ResponseWriter: underlying}
	if _, ok := any(w).(http.Flusher); !ok || w.Unwrap() != underlying {
		t.Fatal("wrapper lost Flush/Unwrap")
	}
	if !startSSE(w) || !writeSSE(w, event{Seq: 1, Name: "delta", Data: []byte(`{"text":"fixture"}`)}) {
		t.Fatal("wrapped SSE write failed")
	}
	if underlying.flushes != 2 || len(underlying.deadlines) != 4 || underlying.deadlines[0].IsZero() || !underlying.deadlines[3].IsZero() {
		t.Fatal("ResponseController did not reach/clear original write deadline")
	}
	app := observerFixture(t, Config{})
	var logs safeLogBuffer
	app.obs.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test-stream", func(w http.ResponseWriter, r *http.Request) { startSSE(w) })
	slow := &observedDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), fail: true}
	app.observeHTTP(mux, mux).ServeHTTP(slow, httptest.NewRequest("GET", "http://127.0.0.1/test-stream", nil))
	if app.obs.slowWrites.Load() != 1 {
		t.Fatal("timed-out slow writer not counted exactly once")
	}
}

type observedReaderFromWriter struct {
	*httptest.ResponseRecorder
	calls int
	fail  bool
}

func (w *observedReaderFromWriter) ReadFrom(reader io.Reader) (int64, error) {
	w.calls++
	if w.fail {
		return 0, fixtureWriteTimeout{}
	}
	return io.Copy(w.ResponseRecorder, reader)
}
func TestObservedWriterPreservesLargeFileReaderFrom(t *testing.T) {
	payload := strings.Repeat("private-video-fixture", 8192)
	fast := &observedReaderFromWriter{ResponseRecorder: httptest.NewRecorder()}
	w := &observedWriter{ResponseWriter: fast}
	n, err := io.CopyN(w, strings.NewReader(payload), int64(len(payload)))
	if err != nil || n != int64(len(payload)) || fast.calls != 1 || fast.Body.String() != payload || w.status != 200 {
		t.Fatal("file transfer lost underlying ReaderFrom optimization or byte fidelity")
	}
	fallback := httptest.NewRecorder()
	fallbackWriter := &observedWriter{ResponseWriter: fallback}
	n, err = io.CopyN(fallbackWriter, strings.NewReader(payload), int64(len(payload)))
	if err != nil || n != int64(len(payload)) || fallback.Body.String() != payload {
		t.Fatal("ReaderFrom fallback recursed or lost data")
	}
	fast.fail = true
	if _, err := w.ReadFrom(strings.NewReader("test")); err == nil || !w.writeTimedOut {
		t.Fatal("ReaderFrom timeout was not observed")
	}
}

type streamLockProbeWriter struct {
	*httptest.ResponseRecorder
	mutex       *sync.Mutex
	lockWasFree bool
}

func (w *streamLockProbeWriter) WriteHeader(status int) {
	w.lockWasFree = w.mutex.TryLock()
	if w.lockWasFree {
		w.mutex.Unlock()
	}
	w.ResponseRecorder.WriteHeader(status)
}
func TestSSEAdmissionDoesNotHoldGlobalLockAcrossErrorWrite(t *testing.T) {
	app := observerFixture(t, Config{Admission: AdmissionLimits{WorkspaceStreams: 1}})
	req := httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), principalKey{}, &principal{Workspace: Workspace{ID: "fixture"}}))
	if !app.acquireStream(httptest.NewRecorder(), req) {
		t.Fatal("first stream rejected")
	}
	limited := &streamLockProbeWriter{ResponseRecorder: httptest.NewRecorder(), mutex: &app.obs.streamMu}
	if app.acquireStream(limited, req) || limited.Code != 429 || !limited.lockWasFree {
		t.Fatal("workspace rejection writes under global stream lock")
	}
	app.releaseObservedStream("fixture")
	for range cap(app.streamSlots) {
		app.streamSlots <- struct{}{}
	}
	global := &streamLockProbeWriter{ResponseRecorder: httptest.NewRecorder(), mutex: &app.obs.streamMu}
	if app.acquireStream(global, req) || global.Code != 503 || !global.lockWasFree {
		t.Fatal("global rejection writes under global stream lock")
	}
	for range cap(app.streamSlots) {
		<-app.streamSlots
	}
}

func TestSlowSubscriberBuffersDisconnectAndRemainBounded(t *testing.T) {
	app := observerFixture(t, Config{ReplaySize: 2, SubscriberBuffer: 1})
	runChannel, workspaceChannel := make(chan event, 1), make(chan event, 1)
	record := &runRecord{Subscribers: map[chan event]bool{runChannel: true}}
	app.mu.Lock()
	app.workspaceSubscribers[workspaceChannel] = subscriptionScope{WorkspaceID: "ws_demo", ProjectID: "prj_demo"}
	for range 4 {
		app.publishRunLocked(record, "delta", map[string]string{"text": "private-not-in-metrics"})
		app.publishTaskLocked(Task{WorkspaceID: "ws_demo", ProjectID: "prj_demo", ID: "private-task-id"})
	}
	remainingRun, remainingWorkspace := len(record.Subscribers), len(app.workspaceSubscribers)
	app.mu.Unlock()
	if remainingRun != 0 || remainingWorkspace != 0 || app.obs.slowConsumers.Load() != 2 || len(record.Events) != 2 {
		t.Fatal("slow subscribers were retained or counter grew per later event")
	}
	for _, ch := range []chan event{runChannel, workspaceChannel} {
		<-ch
		if _, open := <-ch; open {
			t.Fatal("full subscriber channel did not close")
		}
	}
	if strings.Contains(app.prometheusText(), "private-") {
		t.Fatal("subscriber event data escaped into metrics")
	}
}

func TestDefaultHTTPLogLevelAvoidsSuccessAccessLogIO(t *testing.T) {
	app := observerFixture(t, Config{})
	var logs safeLogBuffer
	app.obs.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	observedRequest(app.Handler(), "GET", "/healthz", "")
	if logs.String() != "" {
		t.Fatal("successful hot-path requests produced default access log IO")
	}
	app.cancel()
	observedRequest(app.Handler(), "GET", "/readyz", "")
	if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"route":"ready"`) {
		t.Fatal("safe 5xx diagnostic missing")
	}
}

func TestPostgresObservabilityReadinessAndPrivateRuntime(t *testing.T) {
	databaseURL := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: databaseURL, AuthMode: "required", AssetDir: t.TempDir(), MetricsToken: metricsFixtureToken})
	app.chat = chatFunc(func(ctx context.Context, _ string, _ func(string) error) error { <-ctx.Done(); return ctx.Err() })
	alice, bob := authClient(t), authClient(t)
	a := registerFixture(t, alice, server.URL, "runtime-alice@example.test")
	b := registerFixture(t, bob, server.URL, "runtime-bob@example.test")
	for i, fixture := range []struct {
		client  *http.Client
		project string
	}{{alice, a.Project.ID}, {bob, b.Project.ID}, {bob, b.Project.ID}} {
		input := runInput()
		input.ProjectID = fixture.project
		code, body, _ := authRequest(t, fixture.client, server.URL, "POST", "/api/v1/runs", fmt.Sprintf("runtime-private-%d", i), "", input)
		if code != 201 {
			t.Fatalf("runtime fixture %d %s", code, body)
		}
	}
	for _, fixture := range []struct {
		client *http.Client
		want   int
	}{{alice, 1}, {bob, 2}} {
		code, data, _ := authRequest(t, fixture.client, server.URL, "GET", "/api/v1/runtime?workspace_id="+b.Workspace.ID, "", "", nil)
		var snapshot struct {
			Runs AdmissionResource `json:"runs"`
		}
		if json.Unmarshal(data, &snapshot) != nil || code != 200 || snapshot.Runs.Active != fixture.want {
			t.Fatalf("authenticated runtime leaked/corrupted counts: %d %s", code, data)
		}
	}
	code, _, _ := authRequest(t, alice, server.URL, "GET", "/metrics", "", "", nil)
	if code != 401 {
		t.Fatal("ordinary login grants global operator metrics")
	}
	// Kill the actual writer session. The independent watchdog, not a health
	// request, must trip the breaker and stop active provider work.
	app.store.mu.Lock()
	_ = app.store.conn.Close(context.Background())
	app.store.mu.Unlock()
	select {
	case <-app.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("actual writer loss was not detected by watchdog")
	}
	code, data, _ := request(t, server, "GET", "/healthz", "", nil)
	if code != 200 || !bytes.Contains(data, []byte("alive")) {
		t.Fatal("writer loss incorrectly failed process liveness")
	}
	code, data, _ = request(t, server, "GET", "/readyz", "", nil)
	if code != 503 || !bytes.Contains(data, []byte("database_unavailable")) {
		t.Fatal("writer loss did not fail readiness")
	}
	code, data, headers := authRequest(t, alice, server.URL, "GET", "/api/v1/runtime", "", "", nil)
	if code != 503 || headers.Get("X-Request-ID") == "" || !bytes.Contains(data, []byte("request_id")) {
		t.Fatal("failed dependency runtime did not fail closed with traceable error")
	}
}
