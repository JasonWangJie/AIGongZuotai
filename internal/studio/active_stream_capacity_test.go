package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type capacityScope struct {
	client   *http.Client
	identity testIdentity
}
type capacityReading struct {
	lane, step int
	at         time.Time
}
type capacityTrace struct {
	run           Run
	requested     time.Time
	receipt, open time.Duration
}

func capacityRequest(t *testing.T, ctx context.Context, s capacityScope, method, url, key string, in, out any, want int) {
	t.Helper()
	var data []byte
	if in != nil {
		var err error
		data, err = json.Marshal(in)
		if err != nil {
			t.Fatal("fixture input encoding failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal("invalid fixture request")
	}
	req.Header.Set("Content-Type", "application/json")
	if s.identity.Project.ID != "" {
		req.Header.Set("X-Project-ID", s.identity.Project.ID)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatal("fixture HTTP request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		t.Fatalf("fixture %s status=%d expected=%d", method, res.StatusCode, want)
	}
	if out != nil && json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(out) != nil {
		t.Fatal("fixture response decode failed")
	}
}

func capacityOpen(t *testing.T, ctx context.Context, s capacityScope, base, id string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/runs/"+id+"/events?after=0", nil)
	if err != nil {
		t.Fatal("invalid stream request")
	}
	req.Header.Set("X-Project-ID", s.identity.Project.ID)
	req.Header.Set("Accept", "text/event-stream")
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatal("stream connection failed")
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		res.Body.Close()
		t.Fatalf("stream status=%d or wrong media type", res.StatusCode)
	}
	return res
}

// The principal, HTTP adapter, scheduler, PostgreSQL lease/log and authenticated
// SSE handler all use the normal application path. Only the upstream is local.
func TestPostgresActiveStreamCapacity(t *testing.T) {
	mode := os.Getenv("STUDIO_ACTIVE_STREAMS")
	if mode == "" {
		t.Skip("set STUDIO_ACTIVE_STREAMS=8 or 64 for this opt-in measurement")
	}
	n, err := strconv.Atoi(mode)
	if err != nil || mode != strconv.Itoa(n) || (n != 8 && n != 64) {
		t.Fatal("choose the 8- or 64-stream fixture")
	}
	if !mixedLoopbackDatabase(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("isolated loopback PostgreSQL test database required")
	}
	outPath := os.Getenv("STUDIO_ACTIVE_STREAM_REPORT")
	if !filepath.IsAbs(outPath) {
		t.Fatal("provide a new absolute aggregate report path")
	}
	if _, err = os.Lstat(outPath); !os.IsNotExist(err) {
		t.Fatal("aggregate report path must not exist")
	}
	measurement := measureActiveStreamCapacity(t, n, false)
	writeCapacityReport(t, outPath, measurement.report)
}

type activeCapacityMeasurement struct {
	app    *App
	admin  *pgx.Conn
	traces []capacityTrace
	wave   time.Duration
	report map[string]any
}

func measureActiveStreamCapacity(t *testing.T, n int, batch bool) activeCapacityMeasurement {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	g := newCapacityGateway(ctx, n)
	upstream := httptest.NewServer(g)
	t.Cleanup(upstream.Close)
	catalog := customCatalogFixture()
	first := catalog.Models[0]
	second := first
	first.ID, first.UpstreamModel = "capacity-model-a", "synthetic-stream-0"
	second.ID, second.UpstreamModel = "capacity-model-b", "synthetic-stream-1"
	catalog.Models = []catalogConfigModel{first, second}
	db := testDatabase(t)
	cfg := Config{Mode: "live", BaseURL: upstream.URL + "/v1", APIKey: "local-stream-fixture", DatabaseURL: db, AuthMode: "required",
		ModelCatalogFile: writeCatalogFixture(t, catalog), Heartbeat: time.Second, RunDeltaBatching: batch,
		ChatScheduling: ChatSchedulingConfig{Workers: n, WorkspaceConcurrency: n / 4, ModelConcurrency: n / 2, QueueSize: 8, WorkspaceQueueSize: 2}}
	a, server := testApp(t, cfg)
	admin := deliveryAdmin(t, db)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost = n + 12
	transport.MaxIdleConnsPerHost = n + 12
	t.Cleanup(transport.CloseIdleConnections)
	var scopes [4]capacityScope
	for i := range scopes {
		c := authClient(t)
		c.Timeout = 0
		c.Transport = transport
		s := capacityScope{client: c}
		capacityRequest(t, ctx, s, "POST", server.URL+"/api/v1/auth/register", "", map[string]string{"email": fmt.Sprintf("capacity-%d@example.test", i), "password": "fixture-password-123", "name": "Fixture Creator"}, &s.identity, 201)
		if s.identity.Project.ID == "" || s.identity.Workspace.ID == "" {
			t.Fatal("registered scope incomplete")
		}
		scopes[i] = s
	}
	started := time.Now()
	traces := make([]capacityTrace, n)
	readings := make(chan capacityReading, n*(activeStreamSteps+1))
	finished := make(chan error, n)
	heldReader := make(chan struct{})
	delayedFinished := make(chan error, 1)
	delayedStarted := make(chan time.Time, 1)
	for i := range n {
		s := scopes[i%4]
		trace := &traces[i]
		trace.requested = time.Now()
		capacityRequest(t, ctx, s, "POST", server.URL+"/api/v1/runs", newID("capacity"), RunInput{ProjectID: s.identity.Project.ID, ModelID: catalog.Models[activeModel(i)].ID, Input: activePrompt(i)}, &trace.run, 201)
		trace.receipt = time.Since(trace.requested)
		if trace.run.ID == "" {
			t.Fatal("accepted run missing identity")
		}
		// Sequential admission lets each upstream reach its socket before
		// admitting the next. The one intentional queue probe is timed separately.
		select {
		case lane := <-g.entered:
			if lane != i {
				t.Fatal("provider entered for the wrong accepted run")
			}
		case <-time.After(12 * time.Second):
			t.Fatalf("provider admission timed out after %d streams", i)
		case <-ctx.Done():
			t.Fatal("measurement deadline")
		}
		if i == 0 {
			// A denied attempt releases its temporary stream slot before the
			// measured group is opened. It is not an extra authorized reader.
			capacityRequest(t, ctx, scopes[1], "GET", server.URL+"/api/v1/runs/"+trace.run.ID+"/events?after=0", "", nil, nil, 404)
		}
		open := time.Now()
		res := capacityOpen(t, ctx, s, server.URL, trace.run.ID)
		trace.open = time.Since(open)
		go func(lane int, runID string, body io.ReadCloser) {
			defer body.Close()
			finished <- readCapacityStream(body, runID, lane, func(step int, at time.Time) error {
				select {
				case readings <- capacityReading{lane, step, at}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}(i, trace.run.ID, res.Body)
	}
	// A second authorized consumer on just one stream pauses its body after
	// a complete first delta. The normal reader must continue independently.
	delayed := capacityOpen(t, ctx, scopes[0], server.URL, traces[0].run.ID)
	go func() {
		defer delayed.Body.Close()
		delayedFinished <- readCapacityStream(delayed.Body, traces[0].run.ID, 0, func(step int, at time.Time) error {
			if step == 0 {
				delayedStarted <- at
				select {
				case <-heldReader:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	checkActive := func(queued int) {
		t.Helper()
		a.mu.Lock()
		jobs := a.execution.snapshotLocked(time.Now())
		ws := make([]int, 0, 4)
		models := make([]int, 0, 2)
		for _, s := range scopes {
			ws = append(ws, a.execution.workspaceRunning[s.identity.Workspace.ID])
		}
		for _, count := range a.execution.modelRunning {
			models = append(models, count)
		}
		a.mu.Unlock()
		g.mu.Lock()
		active, posts, invalid := g.active, g.posts, g.invalid
		g.mu.Unlock()
		if jobs.Running != n || jobs.Queued != queued || active != n || posts != n || invalid != 0 || len(a.streamSlots) != n+1 {
			t.Fatalf("occupancy mismatch running=%d queued=%d provider=%d posts=%d readers=%d", jobs.Running, jobs.Queued, active, posts, len(a.streamSlots))
		}
		for _, v := range ws {
			if v != n/4 {
				t.Fatal("workspace budget distribution differs")
			}
		}
		if !slices.Equal(models, []int{n / 2, n / 2}) {
			t.Fatal("upstream model budget distribution differs")
		}
	}
	checkActive(0)
	var queued Run
	s := scopes[0]
	capacityRequest(t, ctx, s, "POST", server.URL+"/api/v1/runs", newID("queue"), RunInput{ProjectID: s.identity.Project.ID, ModelID: first.ID, Input: "queued-capacity-probe"}, &queued, 201)
	if queued.Status != "QUEUED" {
		t.Fatal("extra run did not remain queued")
	}
	checkActive(1)
	beforeSlow := a.obs.slowConsumers.Load()
	marks := make([][activeStreamSteps + 1]time.Time, n)
	count := 0
	consume := func() {
		t.Helper()
		select {
		case v := <-readings:
			if !marks[v.lane][v.step].IsZero() {
				t.Fatal("duplicate live event")
			}
			marks[v.lane][v.step] = v.at
			count++
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
			t.Fatal("reader closed before all model deltas")
		case <-ctx.Done():
			t.Fatalf("live event deadline after %d observations", count)
		}
	}
	releaseStart := time.Now()
	close(g.steps[0])
	for count < n {
		consume()
	}
	var delayedAt time.Time
	select {
	case delayedAt = <-delayedStarted:
	case <-ctx.Done():
		t.Fatal("delayed reader failed to receive the first delta")
	}
	checkActive(1)
	var cancelled Run
	capacityRequest(t, ctx, s, "POST", server.URL+"/api/v1/runs/"+queued.ID+"/cancel", "", nil, &cancelled, 200)
	if cancelled.Status != "CANCELLED" {
		t.Fatal("queued run was not cancelled")
	}
	checkActive(0)
	for j := 1; j < activeStreamSteps; j++ {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("load interval expired")
		}
		close(g.steps[j])
	}
	for count < n*activeStreamSteps {
		consume()
	}
	// All ordinary readers saw every nonempty increment while all n upstream
	// sockets and execution slots were still held; no terminal arrived early.
	checkActive(0)
	doneAt := time.Now()
	close(g.finish)
	for range n {
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("ordinary readers did not exit")
		}
	}
	for count < n*(activeStreamSteps+1) {
		select {
		case v := <-readings:
			if v.step != activeStreamSteps || !marks[v.lane][v.step].IsZero() {
				t.Fatal("invalid terminal observation")
			}
			marks[v.lane][v.step] = v.at
			count++
		case <-ctx.Done():
			t.Fatal("missing terminal observations")
		}
	}
	resumed := time.Now()
	close(heldReader)
	select {
	case err := <-delayedFinished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("delayed reader did not finish")
	}
	executionState(t, a, 0, 0)

	lat := map[string][]time.Duration{}
	add := func(phase string, d time.Duration) {
		t.Helper()
		if d < 0 {
			t.Fatalf("negative latency in %s", phase)
		}
		lat[phase] = append(lat[phase], d)
	}
	g.mu.Lock()
	gatewayStarts := slices.Clone(g.starts)
	sent := slices.Clone(g.sent)
	posts, peak, invalid := g.posts, g.peak, g.invalid
	g.mu.Unlock()
	if posts != n || peak != n || invalid != 0 {
		t.Fatal("additional or rejected upstream call")
	}
	for i, trace := range traces {
		add("admission_http_roundtrip", trace.receipt)
		add("create_start_to_provider_request", gatewayStarts[i].Sub(trace.requested))
		add("live_sse_open", trace.open)
		for j := range activeStreamSteps {
			if sent[i][j].IsZero() || marks[i][j].IsZero() {
				t.Fatal("missing release or observation timestamp")
			}
			d := marks[i][j].Sub(sent[i][j])
			add("all_delta_release_to_sse_observed", d)
			if j == 0 {
				add("first_delta_release_to_sse_observed", d)
			} else {
				add("later_delta_release_to_sse_observed", d)
			}
		}
		add("finish_barrier_to_terminal_sse_observed", marks[i][activeStreamSteps].Sub(doneAt))
	}
	checkCapacityDurability(t, ctx, admin, server.URL, scopes, traces, queued.ID, add)
	var dbCount int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM studio_runs").Scan(&dbCount); err != nil || dbCount != n+1 {
		t.Fatal("persisted accepted run count differs")
	}
	// Wait for stream cleanup including the last replay handler. This sample
	// represents this fixture's app only, not operating-system sockets.
	drain := time.Now().Add(5 * time.Second)
	for len(a.streamSlots) > 0 && time.Now().Before(drain) {
		time.Sleep(10 * time.Millisecond)
	}
	g.mu.Lock()
	posts, active := g.posts, g.active
	g.mu.Unlock()
	if len(a.streamSlots) != 0 || posts != n || active != 0 {
		t.Fatal("fixture readers, upstream calls or handlers did not drain")
	}
	phases := map[string]capacityDistribution{}
	for k, v := range lat {
		phases[k] = capacityDistributionOf(v)
	}
	report := map[string]any{"format": 1, "mode": strconv.Itoa(n), "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"started_at": started.UTC(), "finished_at": time.Now().UTC(), "elapsed_ms": time.Since(started).Milliseconds(), "active_wave_span_ms": doneAt.Sub(releaseStart).Milliseconds(),
		"delayed_reader_pause_ms": resumed.Sub(delayedAt).Milliseconds(), "later_wave_min_spacing_ms": 100,
		"http_run_creates": n + 1, "http_queued_run_cancels": 1, "http_normal_live_sse": n, "http_extra_live_sse": 1, "http_terminal_replay_sse": n, "http_cross_scope_sse_rejected": 1,
		"upstream": "owned loopback streaming HTTP", "platform": "one authenticated Go HTTP/1.1 process and one PostgreSQL database",
		"delta_measurement": "same-process monotonic clock immediately before model HTTP write until blank line of matching platform SSE was read on normal client",
		"workspaces":        4, "models": 2, "workspace_execution_limit": n / 4, "model_execution_limit": n / 2, "global_execution_limit": n,
		"peak_active_model_requests": peak, "confirmed_concurrent_execution_slots": n, "confirmed_concurrent_sse": n + 1,
		"completed_runs": n, "accepted_cancelled_while_queued": 1, "model_posts": posts, "deltas_per_run": activeStreamSteps,
		"observed_normal_deltas": n * activeStreamSteps, "persisted_replayed_deltas": n * activeStreamSteps, "delayed_readers": 1,
		"observed_slow_consumer_evictions": a.obs.slowConsumers.Load() - beforeSlow, "out_of_order": 0, "missing": 0, "extra_model_posts": 0, "final_stream_slots": len(a.streamSlots), "percentiles": phases}
	t.Logf("verified %d active streams, %d live and %d persisted deltas, %d model POSTs", n, n*activeStreamSteps, n*activeStreamSteps, posts)
	return activeCapacityMeasurement{app: a, admin: admin, traces: traces, wave: doneAt.Sub(releaseStart), report: report}
}

func writeCapacityReport(t *testing.T, outPath string, report any) {
	t.Helper()
	data := recoveryAggregateJSON(t, report)
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("cannot create new aggregate report")
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal("aggregate report write failed")
	}
}

func checkCapacityDurability(t *testing.T, ctx context.Context, admin *pgx.Conn, base string, scopes [4]capacityScope, traces []capacityTrace, queued string, add func(string, time.Duration)) {
	t.Helper()
	for lane, trace := range traces {
		s := scopes[lane%4]
		var status, output, project, workspace, deltaText string
		var high, floor, events, matching int
		var closed bool
		opened := time.Now()
		err := admin.QueryRow(ctx, `SELECT r.document->>'status',r.document->>'output',r.project_id,r.workspace_id,h.head_seq,h.floor_seq,h.closed,
  count(e.seq),string_agg(e.delta,'' ORDER BY e.seq),
  count(*) FILTER (WHERE (e.name='message.delta' AND e.seq BETWEEN 1 AND $2::bigint) OR (e.name='run.completed' AND e.seq=$2::bigint+1))
  FROM studio_runs r JOIN studio_run_streams h ON h.run_id=r.id JOIN studio_run_stream_events e ON e.run_id=r.id
  WHERE r.id=$1 GROUP BY r.id,h.run_id`, trace.run.ID, activeStreamSteps).Scan(&status, &output, &project, &workspace, &high, &floor, &closed, &events, &deltaText, &matching)
		add("independent_pg_terminal_and_log_read", time.Since(opened))
		if err != nil {
			capacityDBFailure(t, "terminal_log_projection", err)
		}
		var want strings.Builder
		for j := range activeStreamSteps {
			want.WriteString(activeDelta(lane, j))
		}
		if status != "COMPLETED" || project != s.identity.Project.ID || workspace != s.identity.Workspace.ID || !closed {
			t.Fatal("terminal_log_projection state/scope/closure mismatch")
		}
		if high != activeStreamSteps+1 || floor != 0 || events != high || matching != high {
			t.Fatalf("terminal_log_projection high=%d floor=%d events=%d matching=%d", high, floor, events, matching)
		}
		if output != want.String() || deltaText != want.String() {
			t.Fatalf("terminal_log_projection text mismatch snapshot_bytes=%d delta_bytes=%d expected_bytes=%d", len(output), len(deltaText), want.Len())
		}
		// Explicit zero always enters the PG journal replay path, including
		// for a retained hot record. This is a new connection after completion.
		opened = time.Now()
		response := capacityOpen(t, ctx, s, base, trace.run.ID)
		err = readCapacityStream(response.Body, trace.run.ID, lane, func(int, time.Time) error { return nil })
		response.Body.Close()
		add("terminal_sse_from_zero_full_response", time.Since(opened))
		if err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := admin.QueryRow(ctx, "SELECT document->>'status' FROM studio_runs WHERE id=$1", queued).Scan(&state); err != nil {
		capacityDBFailure(t, "queued_cancel_projection", err)
	}
	if state != "CANCELLED" {
		t.Fatal("queued cancellation was not committed")
	}
}

func capacityDBFailure(t *testing.T, stage string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr):
		t.Fatalf("independent database check stage=%s sqlstate=%s", stage, pgErr.Code)
	case errors.Is(err, pgx.ErrNoRows):
		t.Fatalf("independent database check stage=%s kind=no_rows", stage)
	default:
		t.Fatalf("independent database check stage=%s kind=scan_or_connection", stage)
	}
}
