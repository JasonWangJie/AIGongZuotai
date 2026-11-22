package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Labels are fixed fixture operations. No account, project, request ID or
// user-supplied text is exported with the aggregate evidence.
type mixedTimings struct {
	mu     sync.Mutex
	starts map[string]time.Time
	waits  map[string]time.Duration
	calls  map[string]int
}

func (m *mixedTimings) submit(label string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts[label] = time.Now()
}

func (m *mixedTimings) enter(label string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[label]++
	if m.calls[label] == 1 {
		m.waits[label] = time.Since(m.starts[label])
	}
}

type mixedChat struct {
	*executionIntegrationProvider
	clock *mixedTimings
}

func (p *mixedChat) StreamMessages(ctx context.Context, model string, request ChatRequest, emit func(string) error) error {
	p.clock.enter(request.Messages[len(request.Messages)-1].Content)
	return p.executionIntegrationProvider.StreamMessages(ctx, model, request, emit)
}

type mixedMedia struct {
	*AsyncMediaProvider
	clock   *mixedTimings
	held    chan struct{}
	release chan struct{}
}

func (p *mixedMedia) SubmitPrepared(ctx context.Context, submission MediaSubmission) (MediaHandle, error) {
	var payload struct{ Prompt string }
	if err := json.Unmarshal(submission.Body, &payload); err != nil {
		return MediaHandle{}, err
	}
	p.clock.enter(payload.Prompt)
	handle, err := p.AsyncMediaProvider.SubmitPrepared(ctx, submission)
	if err == nil && payload.Prompt == "A-media-1" {
		// The gateway has accepted the request. Delay its return to the real
		// scheduler, retaining the workspace/channel slot and the PG connection.
		close(p.held)
		select {
		case <-p.release:
		case <-ctx.Done():
			return MediaHandle{}, ctx.Err()
		}
	}
	return handle, err
}

type mixedSample struct {
	Phase             string                     `json:"phase"`
	ElapsedMS         float64                    `json:"elapsed_ms"`
	HeapAlloc         uint64                     `json:"heap_alloc_bytes"`
	HeapInuse         uint64                     `json:"heap_inuse_bytes"`
	GoSys             uint64                     `json:"go_sys_bytes"`
	Goroutines        int                        `json:"goroutines"`
	ManagedPGSessions int                        `json:"managed_pg_sessions"`
	ActivePGSessions  int                        `json:"active_managed_pg_sessions"`
	PGActivity        mixedPGActivity            `json:"managed_pg_activity"`
	GC                mixedGCWindow              `json:"gc_since_previous_sample"`
	SlowBufferFull    uint64                     `json:"app_slow_buffer_full_total"`
	SlowWriteTimeout  uint64                     `json:"app_slow_write_timeout_total"`
	LeasedConnections [4]int                     `json:"checked_out_pg_connections_by_lane"`
	Chat              ExecutionSchedulerSnapshot `json:"chat"`
	Media             MediaExecutionSnapshot     `json:"media_lanes"`
	MediaScheduler    MediaSchedulerSnapshot     `json:"media_scheduler"`
}

func (ob *mixedObservation) sample(t *testing.T, a *App, admin *pgx.Conn, start time.Time, phase string) mixedSample {
	t.Helper()
	s := mixedSample{Phase: phase, ElapsedMS: float64(time.Since(start)) / float64(time.Millisecond)}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	s.HeapAlloc, s.HeapInuse, s.GoSys, s.Goroutines = memory.HeapAlloc, memory.HeapInuse, memory.Sys, runtime.NumGoroutine()
	s.GC = ob.gc.next(&memory)
	s.PGActivity = ob.managedActivity(t, a, admin)
	s.ManagedPGSessions, s.ActivePGSessions = s.PGActivity.Observed, s.PGActivity.States["active"]
	s.SlowBufferFull, s.SlowWriteTimeout = a.obs.slowConsumers.Load(), a.obs.slowWrites.Load()
	for lane, pool := range a.taskLeases.connections {
		s.LeasedConnections[lane] = cap(pool) - len(pool)
	}
	a.mu.Lock()
	s.Chat = a.execution.snapshotLocked(time.Now())
	a.mu.Unlock()
	s.Media, s.MediaScheduler = a.mediaExecution.snapshot(), a.mediaScheduler.snapshot()
	return s
}

func TestPostgresMixedWorkspaceWorkload(t *testing.T) {
	if os.Getenv("MIXED_WORKLOAD_TEST") != "1" {
		t.Skip("set MIXED_WORKLOAD_TEST=1 for the mixed-workspace measurement")
	}
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" && !mixedLoopbackDatabase(dsn) {
		t.Fatal("mixed workload requires an isolated loopback PostgreSQL database")
	}
	fixture := newDeliveryHTTPFixture(t, -1)
	var arm429 atomic.Bool
	var delayedMu sync.Mutex
	var delayedHandle string
	var limitedAt, recoveredAt time.Time
	limited := make(chan string, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, mediaQueryPrefix) {
			handle := strings.TrimPrefix(r.URL.Path, mediaQueryPrefix)
			if arm429.CompareAndSwap(true, false) {
				delayedMu.Lock()
				delayedHandle, limitedAt = handle, time.Now()
				delayedMu.Unlock()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "fixture rate limit", http.StatusTooManyRequests)
				limited <- handle
				return
			}
			delayedMu.Lock()
			if handle == delayedHandle && recoveredAt.IsZero() {
				recoveredAt = time.Now()
			}
			delayedMu.Unlock()
		}
		fixture.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(gateway.Close)
	observed := &deliveryHTTPFixture{server: gateway}
	reviewer := newReviewDispatchHTTPFixture(t, "")
	cfg := reviewDispatchConfig(t, observed, reviewer, true, 1)
	cfg.PollInterval = 15 * time.Millisecond
	cfg.ChatScheduling = ChatSchedulingConfig{Workers: 2, QueueSize: 5, WorkspaceConcurrency: 1, ModelConcurrency: 2, WorkspaceQueueSize: 2}
	cfg.MediaScheduling = MediaSchedulingConfig{SubmitWorkers: 2, QueryWorkers: 1, DeliveryWorkers: 1, ReviewWorkers: 1, WorkspaceConcurrency: 1, ChannelConcurrency: 2}
	timing := &mixedTimings{starts: map[string]time.Time{}, waits: map[string]time.Duration{}, calls: map[string]int{}}
	chat := &mixedChat{executionIntegrationProvider: &executionIntegrationProvider{calls: make(chan *executionIntegrationCall, 16), stop: make(chan struct{})}, clock: timing}
	media := &mixedMedia{AsyncMediaProvider: cfg.mediaProvider.(*AsyncMediaProvider), clock: timing, held: make(chan struct{}), release: make(chan struct{})}
	cfg.mediaProvider = media
	releaseMedia := sync.OnceFunc(func() { close(media.release) })
	open := func() (*App, *httptest.Server, func()) {
		instanceCfg := cfg
		instanceCfg.DatabaseURL = mixedTaggedDSN(t, cfg.DatabaseURL, newID("mixed"))
		app, err := New(instanceCfg)
		if err != nil {
			t.Fatal(err)
		}
		app.chat = chat
		server := httptest.NewServer(app.Handler())
		stop := sync.OnceFunc(func() { app.Close(); server.Close() })
		t.Cleanup(stop)
		return app, server, stop
	}
	app, server, stop := open()
	// Release fixture barriers before Close even when an assertion fails.
	t.Cleanup(func() { releaseMedia(); reviewer.unblock(); close(chat.stop) })
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	clients := [2]*http.Client{authClient(t), authClient(t)}
	owners := [2]testIdentity{registerFixture(t, clients[0], server.URL, "mixed-a@example.test"), registerFixture(t, clients[1], server.URL, "mixed-b@example.test")}
	started := time.Now()
	var observation mixedObservation
	samples := []mixedSample{observation.sample(t, app, admin, started, "baseline")}
	runs := make(map[string]Run)
	tasks := make(map[string]Task)
	newChat := func(w int, label string) {
		timing.submit(label)
		runs[label] = executionRun(t, server, clients[w], owners[w], "studio-chat", label, "mixed-key-"+label)
	}
	newMedia := func(w int, label string) {
		timing.submit(label)
		tasks[label], _, _ = laneAccept(t, app, server, clients[w], owners[w], label)
	}
	newChat(0, "A-chat-healthy")
	executionStarted(t, chat.executionIntegrationProvider, "A-chat-healthy").finish()
	newMedia(0, "A-media-healthy")
	if healthy := awaitAuthRun(t, clients[0], server.URL, runs["A-chat-healthy"].ID); healthy.Status != "COMPLETED" {
		t.Fatal("healthy chat baseline did not complete")
	}
	laneTaskStatus(t, app, tasks["A-media-healthy"].ID, "SUCCEEDED")
	samples = append(samples, observation.sample(t, app, admin, started, "healthy_pair_completed"))
	reviewer.mu.Lock()
	reviewer.phase = "output"
	reviewer.mu.Unlock()
	newChat(0, "A-chat-0")
	a0 := executionStarted(t, chat.executionIntegrationProvider, "A-chat-0")
	newMedia(0, "A-media-0")
	deliveryWait(t, reviewer.entered)
	newMedia(0, "A-media-1")
	select {
	case <-media.held:
	case <-time.After(8 * time.Second):
		t.Fatal("fixture did not hold the second submit")
	}
	newChat(0, "A-chat-1")
	newChat(0, "A-chat-2")
	newMedia(0, "A-media-2")
	rejected := RunInput{ProjectID: owners[0].Project.ID, ModelID: "studio-chat", Input: "A-chat-3"}
	code, body, _ := authRequest(t, clients[0], server.URL, "POST", "/api/v1/runs", "mixed-key-A-chat-3", owners[0].Project.ID, rejected)
	if code != 429 || !bytes.Contains(body, []byte("EXECUTION_WORKSPACE_QUEUE_LIMIT")) {
		t.Fatal("saturated workspace was not rejected by its chat queue", code)
	}
	newChat(1, "B-chat-0")
	b0 := executionStarted(t, chat.executionIntegrationProvider, "B-chat-0")
	newMedia(1, "B-media-0")
	laneTaskStatus(t, app, tasks["B-media-0"].ID, "MODERATING")
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_media_review_dispatch WHERE task_id=$1 AND state='READY'", tasks["B-media-0"].ID); n != 1 {
		t.Fatal("other workspace did not reach review while the first was blocked")
	}
	timing.mu.Lock()
	early := timing.calls["A-media-2"]
	timing.mu.Unlock()
	if early != 0 {
		t.Fatal("same workspace bypassed the occupied submit scope")
	}
	peak := observation.sample(t, app, admin, started, "two_chats_and_submit_and_review_held")
	if peak.Chat.Running != 2 || peak.Chat.Queued != 2 || peak.MediaScheduler.Lanes[mediaSubmitLane].ScopeInFlight != 1 || peak.MediaScheduler.Lanes[mediaReviewLane].ScopeInFlight != 1 {
		t.Fatal("mixed contention did not overlap the expected scoped reservations", peak)
	}
	samples = append(samples, peak)
	b0.finish()
	if final := awaitAuthRun(t, clients[1], server.URL, runs["B-chat-0"].ID); final.Status != "COMPLETED" {
		t.Fatal("later workspace did not complete its chat under backlog")
	}
	releaseMedia()
	reviewer.unblock()
	a0.finish()
	for _, label := range []string{"A-chat-1", "A-chat-2"} {
		executionStarted(t, chat.executionIntegrationProvider, label).finish()
	}
	executionState(t, app, 0, 0)
	for _, task := range tasks {
		final := laneTaskStatus(t, app, task.ID, "SUCCEEDED")
		if len(final.Artifacts) != 1 || final.Moderation == nil || final.Moderation.State != "ALLOWED" {
			t.Fatal("media completed without approved delivery")
		}
	}
	samples = append(samples, observation.sample(t, app, admin, started, "backlog_drained"))
	// Resubmit the request that received a definite 429, using its original
	// key. Hold it at the provider, then stop and reopen the same PG/private dirs.
	newChat(0, "A-chat-3")
	executionStarted(t, chat.executionIntegrationProvider, "A-chat-3")
	arm429.Store(true)
	newMedia(1, "B-media-backoff")
	var originalHandle string
	select {
	case originalHandle = <-limited:
	case <-time.After(10 * time.Second):
		t.Fatal("gateway did not receive the original handle GET")
	}
	backoffTask := tasks["B-media-backoff"].ID
	var observedCooldown time.Duration
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		schedule, err := loadMediaScheduleSQL(context.Background(), admin, backoffTask)
		if err != nil {
			t.Fatal(err)
		}
		if schedule.FailureCount == 1 {
			channel, err := loadMediaChannelSQL(context.Background(), admin, "gateway", "query", schedule.ChannelKey, false)
			if err != nil || channel.FailureCount != 1 || !channel.ProbeRequired {
				t.Fatal("missing persisted channel cooldown", err)
			}
			delayedMu.Lock()
			observedCooldown = channel.CooldownUntil.Sub(limitedAt)
			delayedMu.Unlock()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if observedCooldown <= 0 {
		t.Fatal("rate limit did not install a future channel cooldown")
	}
	samples = append(samples, observation.sample(t, app, admin, started, "media_channel_cooling_with_chat_held"))
	laneTaskStatus(t, app, backoffTask, "SUCCEEDED")
	backoffRecord, err := app.store.loadTask(backoffTask)
	if err != nil || backoffRecord.Handle.ID != originalHandle {
		t.Fatal("rate-limited media changed the original handle", err)
	}
	delayedMu.Lock()
	retryWait := recoveredAt.Sub(limitedAt)
	delayedMu.Unlock()
	if retryWait < time.Second {
		t.Fatal("retry occurred before the gateway Retry-After", retryWait)
	}
	t0 := time.Now()
	stop()
	shutdownMS := float64(time.Since(t0)) / float64(time.Millisecond)
	media.AsyncMediaProvider = observed.provider(t, cfg.AssetDir)
	t0 = time.Now()
	recovered, next, _ := open()
	reopenMS := float64(time.Since(t0)) / float64(time.Millisecond)
	code, body, headers := authRequest(t, clients[0], next.URL, "POST", "/api/v1/runs", "mixed-key-A-chat-3", owners[0].Project.ID, rejected)
	last := decode[Run](t, body)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || last.ID != runs["A-chat-3"].ID || last.Status != "INTERRUPTED" || last.Output != "" {
		t.Fatal("reopened App did not preserve interrupted reviewed run and original key", code)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_run_streams WHERE closed AND execution_id<>''"); n != len(runs) {
		t.Fatal("reopen lost or duplicated accepted Run executions", n)
	}
	for _, task := range tasks {
		final, err := recovered.store.loadTask(task.ID)
		if err != nil || final.Task.Status != "SUCCEEDED" || len(final.Task.Artifacts) != 1 {
			t.Fatal("reopen lost approved media", err)
		}
	}
	samples = append(samples, observation.sample(t, recovered, admin, started, "reopened"))
	waits := map[string]float64{}
	timing.mu.Lock()
	for label, n := range timing.calls {
		if n != 1 {
			t.Errorf("fixture operation invoked %d times", n)
		}
		waits[label] = float64(timing.waits[label]) / float64(time.Millisecond)
	}
	timing.mu.Unlock()
	fixture.mu.Lock()
	submits, queries, downloads := fixture.count, 0, 0
	for _, n := range fixture.queries {
		queries += n
	}
	for _, n := range fixture.downloads {
		downloads += n
	}
	fixture.mu.Unlock()
	approvals := reviewer.count("output")
	if len(waits) != len(runs)+len(tasks) || submits != len(tasks) || queries != len(tasks) || downloads != len(tasks) || approvals != len(tasks) {
		t.Fatal("unexpected number of gateway or review calls", len(waits), submits, queries, downloads, approvals)
	}
	var pgVersion string
	if err := admin.QueryRow(context.Background(), "SHOW server_version").Scan(&pgVersion); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "logical_cpu": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "postgres_version": pgVersion,
		"scope":           "single local writer, two workspaces, six chat executions and six native asynchronous media tasks; samples include test HTTP servers; Go Sys excludes other processes and is not RSS",
		"wait_definition": "wall time from before submission via authenticated HTTP to provider adapter entry, includes admission and queue; fixed fixture barriers hold selected calls",
		"chat_limits":     cfg.ChatScheduling, "media_limits": cfg.MediaScheduling, "poll_interval_ms": cfg.PollInterval.Milliseconds(), "request_to_provider_ms": waits,
		"samples": samples, "workspace_queue_429": 1, "chat_provider_calls": len(runs), "media_submit_http": submits, "media_query_http": queries + 1, "private_download_http": downloads, "media_output_review_http": approvals,
		"media_query_http_429": 1, "retry_after_seconds": 1, "same_handle_retry_ms": float64(retryWait) / float64(time.Millisecond), "persisted_cooldown_from_http_ms": float64(observedCooldown) / float64(time.Millisecond),
		"shutdown_ms": shutdownMS, "complete_reopen_ms": reopenMS, "original_key_replayed": true, "interrupted_runs": 1, "restored_approved_media": len(tasks),
		"resource_sample_scope": "six phase snapshots; GC deltas from first runtime sample, nanoseconds; managed PG snapshots cover only this App writer and its media lane connections, state and wait types are counts not wait durations; slow counters are per App incarnation",
		"controlled_pg_lock":    mixedControlledLock(t, admin, cfg.DatabaseURL),
		"controlled_run_fanout": mixedControlledFanout(t, recovered),
	}
	raw := recoveryAggregateJSON(t, report)
	if path := os.Getenv("MIXED_WORKLOAD_REPORT"); path != "" {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(raw))
}
