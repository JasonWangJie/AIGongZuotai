package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type mediaLaneCall struct {
	prompt  string
	release chan struct{}
	once    sync.Once
}

func (c *mediaLaneCall) finish() { c.once.Do(func() { close(c.release) }) }

// Each actual provider operation has a test-controlled return barrier. Context
// cancellation intentionally does not release it: the worker must retain its
// execution budget until the adapter really returns, even if a task was revoked.
type mediaLaneProvider struct {
	submits, queries                                 chan *mediaLaneCall
	stop                                             chan struct{}
	mu                                               sync.Mutex
	activeSubmit, activeQuery, peakSubmit, peakQuery int
	countSubmit, countQuery                          int
}

func newMediaLaneProvider() *mediaLaneProvider {
	return &mediaLaneProvider{submits: make(chan *mediaLaneCall, 16), queries: make(chan *mediaLaneCall, 16), stop: make(chan struct{})}
}
func (p *mediaLaneProvider) enter(submit bool, prompt string) (*mediaLaneCall, func()) {
	p.mu.Lock()
	if submit {
		p.activeSubmit++
		p.countSubmit++
		p.peakSubmit = max(p.peakSubmit, p.activeSubmit)
	} else {
		p.activeQuery++
		p.countQuery++
		p.peakQuery = max(p.peakQuery, p.activeQuery)
	}
	p.mu.Unlock()
	call := &mediaLaneCall{prompt: prompt, release: make(chan struct{})}
	return call, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if submit {
			p.activeSubmit--
		} else {
			p.activeQuery--
		}
	}
}
func (p *mediaLaneProvider) Submit(_ context.Context, in TaskInput) (MediaHandle, error) {
	call, leave := p.enter(true, in.Inputs.Prompt)
	defer leave()
	p.submits <- call
	select {
	case <-call.release:
	case <-p.stop:
		return MediaHandle{}, context.Canceled
	}
	return MediaHandle{ID: call.prompt, Kind: in.Kind, Count: 1, SubmittedAt: time.Now()}, nil
}
func (p *mediaLaneProvider) Query(_ context.Context, h MediaHandle) (MediaResult, error) {
	call, leave := p.enter(false, h.ID)
	defer leave()
	p.queries <- call
	select {
	case <-call.release:
	case <-p.stop:
		return MediaResult{}, context.Canceled
	}
	return MediaResult{Status: "SUCCEEDED", Progress: 100, Artifacts: []Artifact{{ID: newID("lane_art"), Kind: "image", URL: "/demo-assets/image-1.svg", Title: "local lane fixture"}}}, nil
}
func (p *mediaLaneProvider) Cancel(context.Context, MediaHandle) error { return nil }

func laneCall(t *testing.T, ch <-chan *mediaLaneCall, prompt string) *mediaLaneCall {
	t.Helper()
	select {
	case call := <-ch:
		if call.prompt != prompt {
			t.Fatalf("provider entered %q, want %q", call.prompt, prompt)
		}
		return call
	case <-time.After(5 * time.Second):
		t.Fatalf("provider did not enter %q", prompt)
		return nil
	}
}
func laneTaskStatus(t *testing.T, a *App, id, want string) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		r, err := a.getTaskLocked(id)
		var task Task
		if err == nil {
			task = copyTask(r.Task)
		}
		a.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == want {
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task did not reach %s", want)
	return Task{}
}
func laneFixture(t *testing.T, persistent bool, submit, query int) (*App, *httptest.Server, *http.Client, testIdentity, *mediaLaneProvider) {
	t.Helper()
	provider := newMediaLaneProvider()
	// This fixture isolates lane saturation. Explicit scope ceilings let its
	// single workspace fill those lanes; A3.3 scoped-budget tests separately
	// enforce the new default of one active step per workspace and lane.
	config := Config{mediaProvider: provider, PollInterval: 5 * time.Millisecond, MediaScheduling: MediaSchedulingConfig{SubmitWorkers: submit, QueryWorkers: query, WorkspaceConcurrency: max(submit, query), ChannelConcurrency: max(submit, query)}}
	if persistent {
		config.DatabaseURL = testDatabase(t)
		config.AuthMode = "required"
	}
	a, s := testApp(t, config)
	t.Cleanup(func() { close(provider.stop) })
	c := s.Client()
	identity := testIdentity{Project: Project{ID: "prj_demo", WorkspaceID: "ws_demo"}}
	if persistent {
		c = authClient(t)
		identity = registerFixture(t, c, s.URL, "media-lanes@example.test")
	}
	return a, s, c, identity, provider
}
func laneAccept(t *testing.T, a *App, s *httptest.Server, c *http.Client, identity testIdentity, prompt string) (Task, TaskInput, string) {
	t.Helper()
	key := newID("lane_key")
	task, input := acceptMediaExecution(t, a, s, c, identity, "image", prompt, key, 1)
	return task, input, key
}
func laneCancel(t *testing.T, s *httptest.Server, c *http.Client, p testIdentity, id string) {
	t.Helper()
	code, raw, _ := authRequest(t, c, s.URL, "POST", "/api/v1/tasks/"+id+"/cancel", "", p.Project.ID, nil)
	if code != 200 || decode[Task](t, raw).Status != "CANCELLED" {
		t.Fatal("cancel failed", code, string(raw))
	}
}

func TestMediaLaneIsolationMemory(t *testing.T)   { verifyMediaLaneIsolation(t, false) }
func TestPostgresMediaLaneIsolation(t *testing.T) { verifyMediaLaneIsolation(t, true) }
func verifyMediaLaneIsolation(t *testing.T, persistent bool) {
	a, s, c, identity, p := laneFixture(t, persistent, 2, 1)
	first, _, _ := laneAccept(t, a, s, c, identity, "lane-first")
	laneCall(t, p.submits, "lane-first").finish()
	firstQuery := laneCall(t, p.queries, "lane-first")
	second, _, _ := laneAccept(t, a, s, c, identity, "lane-second")
	laneCall(t, p.submits, "lane-second").finish()
	laneTaskStatus(t, a, second.ID, "QUEUED")
	slow1, body, key := laneAccept(t, a, s, c, identity, "lane-slow-one")
	call1 := laneCall(t, p.submits, "lane-slow-one")
	slow2, _, _ := laneAccept(t, a, s, c, identity, "lane-slow-two")
	call2 := laneCall(t, p.submits, "lane-slow-two")
	// Both submit workers are occupied; releasing a query must admit the next
	// original handle without waiting for either POST to return.
	firstQuery.finish()
	secondQuery := laneCall(t, p.queries, "lane-second")
	if snapshot := a.mediaExecution.snapshot(); snapshot.SubmitInFlight != 2 || snapshot.QueryInFlight != 1 {
		t.Fatal("independent lane reservations missing", snapshot)
	}
	code, raw, headers := authRequest(t, c, s.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, body)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != slow1.ID {
		t.Fatal("occupied lanes blocked original idempotency", code)
	}
	third, _, _ := laneAccept(t, a, s, c, identity, "lane-third")
	laneCancel(t, s, c, identity, slow1.ID)
	if a.mediaExecution.snapshot().SubmitInFlight != 2 {
		t.Fatal("cancel released a slot before provider return")
	}
	// The cancelled call really exits now. A fresh submit can then enter while
	// the query/delivery lane remains fully occupied.
	call1.finish()
	thirdCall := laneCall(t, p.submits, "lane-third")
	laneCancel(t, s, c, identity, slow2.ID)
	laneCancel(t, s, c, identity, third.ID)
	call2.finish()
	thirdCall.finish()
	secondQuery.finish()
	for _, id := range []string{first.ID, second.ID} {
		laneTaskStatus(t, a, id, "SUCCEEDED")
	}
	for _, id := range []string{slow1.ID, slow2.ID, third.ID} {
		task := laneTaskStatus(t, a, id, "CANCELLED")
		if len(task.Artifacts) != 0 {
			t.Fatal("late cancelled result was delivered")
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.peakSubmit != 2 || p.peakQuery != 1 || p.countSubmit != 5 || p.countQuery != 2 {
		t.Fatalf("unexpected real provider concurrency/calls: %d/%d peaks, %d/%d calls", p.peakSubmit, p.peakQuery, p.countSubmit, p.countQuery)
	}
}

func TestPostgresMediaLaneExpiredSubmittingNeverPosts(t *testing.T) {
	a, s, c, identity, p := laneFixture(t, true, 1, 1)
	blocker, _, _ := laneAccept(t, a, s, c, identity, "hold-submit-slot")
	blockedCall := laneCall(t, p.submits, "hold-submit-slot")
	victim, body, key := laneAccept(t, a, s, c, identity, "crashed-submitting")
	admin, err := pgx.Connect(context.Background(), a.config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	// Fault injection represents the durable SUBMITTING point followed by a
	// lost process/lease with no handle. The resource was accepted via HTTP.
	_, err = admin.Exec(context.Background(), `UPDATE studio_tasks SET document=jsonb_set(jsonb_set(document,'{status}','"SUBMITTING"'),'{version}',to_jsonb((document->>'version')::bigint+1)),lease_owner='expired-fixture',lease_until=clock_timestamp()-interval '1 second',next_poll=clock_timestamp() WHERE id=$1`, victim.ID)
	if err != nil {
		t.Fatal(err)
	}
	laneCancel(t, s, c, identity, blocker.ID)
	blockedCall.finish()
	final := laneTaskStatus(t, a, victim.ID, "SUBMIT_UNKNOWN")
	if final.CanReconcile || len(final.Artifacts) != 0 {
		t.Fatal("unknown submission became deliverable")
	}
	code, raw, headers := authRequest(t, c, s.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, body)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != victim.ID {
		t.Fatal("unknown original replay changed identity")
	}
	p.mu.Lock()
	submits, queries := p.countSubmit, p.countQuery
	p.mu.Unlock()
	if submits != 1 || queries != 0 {
		t.Fatal("expired handleless claim repeated provider work")
	}
	// Lane settings only apply at startup. Reopen the same durable state with
	// different budgets and verify the unknown request retains its original key.
	config := a.config
	a.Close()
	s.Close()
	config.MediaScheduling = MediaSchedulingConfig{SubmitWorkers: 2, QueryWorkers: 3}
	reopened, restarted := testApp(t, config)
	if snapshot := reopened.mediaExecution.snapshot(); snapshot.SubmitLimit != 2 || snapshot.QueryLimit != 3 {
		t.Fatal("restart did not apply the new lane budgets")
	}
	code, raw, headers = authRequest(t, c, restarted.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, body)
	replayed := decode[Task](t, raw)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || replayed.ID != victim.ID || replayed.Status != "SUBMIT_UNKNOWN" {
		t.Fatal("lane restart changed the original unknown request")
	}
	select {
	case <-p.submits:
		t.Fatal("restarted lane entered a new submit for an unknown request")
	case <-p.queries:
		t.Fatal("restarted lane queried a request with no handle")
	case <-time.After(100 * time.Millisecond):
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.countSubmit != 1 || p.countQuery != 0 {
		t.Fatal("lane restart repeated the unknown provider submission")
	}
}

func TestPostgresMediaLaneBackpressureKeepsSubmitSlotUntilCommit(t *testing.T) {
	a, s, c, identity, p := laneFixture(t, true, 1, 1)
	first, _, _ := laneAccept(t, a, s, c, identity, "accepted-before-lane-backpressure")
	firstCall := laneCall(t, p.submits, "accepted-before-lane-backpressure")
	second, _, _ := laneAccept(t, a, s, c, identity, "waiting-during-lane-backpressure")
	admin, err := pgx.Connect(context.Background(), a.config.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	// Drain real admission events first, so the normal projector cannot lower
	// the injected full counter and accidentally unblock this fault barrier.
	deadline := time.Now().Add(5 * time.Second)
	for outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("admission events did not project")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=$1 WHERE singleton", outboxPendingLimit); err != nil {
		t.Fatal(err)
	}
	firstCall.finish()
	deadline = time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		returned := p.activeSubmit == 0
		p.mu.Unlock()
		if returned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider did not return")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The provider has returned, but its durable handle cannot commit yet.
	// The next queued task must not borrow this still-owned submit capacity.
	select {
	case <-p.submits:
		t.Fatal("backpressure prematurely released submit slot")
	case <-time.After(100 * time.Millisecond):
	}
	if a.mediaExecution.snapshot().SubmitInFlight != 1 || readLeasedTask(t, admin, first.ID).Status != "SUBMITTING" {
		t.Fatal("backpressured accepted result did not retain its lane and durable state")
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	secondCall := laneCall(t, p.submits, "waiting-during-lane-backpressure")
	firstQuery := laneCall(t, p.queries, "accepted-before-lane-backpressure")
	laneCancel(t, s, c, identity, first.ID)
	laneCancel(t, s, c, identity, second.ID)
	secondCall.finish()
	firstQuery.finish()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.countSubmit != 2 || p.countQuery != 1 || p.peakSubmit != 1 || p.peakQuery != 1 || a.store.failed.Load() {
		t.Fatal("backpressure repeated provider work, exceeded a lane, or became database failure")
	}
}

func TestPostgresMediaLaneReviewHoldsOwnLaneUntilCancelledResultReturns(t *testing.T) {
	database := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: database, AuthMode: "required", Moderation: executionModerationConfig(t, nil), MediaModeration: MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}, PollInterval: 5 * time.Millisecond, DemoTaskDuration: time.Millisecond, MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 1, ReviewWorkers: 1}})
	c := authClient(t)
	identity := registerFixture(t, c, s.URL, "media-lane-review@example.test")
	entered, release := gatedMediaChecker(t, a)
	// Model an HTTP transport with delayed cleanup even after cancellation.
	// The original real HTTP call still runs; only its return is held so the
	// assertion measures actual local execution, not a remote handler lifetime.
	transport := a.mediaModeration.http.client.Transport
	a.mediaModeration.http.client.Transport = mediaRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		<-release
		return response, err
	})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	first, _, _ := laneAccept(t, a, s, c, identity, "first-held-review")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first review not entered")
	}
	second, _, _ := laneAccept(t, a, s, c, identity, "second-accepted-during-review")
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	// Real Submit must finish and its handle must commit while the other task
	// holds its review capacity across a slow full-byte HTTP review.
	deadline := time.Now().Add(5 * time.Second)
	handle := ""
	for time.Now().Before(deadline) {
		err = admin.QueryRow(context.Background(), "SELECT COALESCE(handle->>'ID','') FROM studio_tasks WHERE id=$1", second.ID).Scan(&handle)
		if err != nil {
			t.Fatal(err)
		}
		if handle != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if handle == "" {
		t.Fatal("slow review blocked fresh media submit")
	}
	for time.Now().Before(deadline) {
		var state string
		err = admin.QueryRow(context.Background(), "SELECT COALESCE((SELECT state FROM studio_media_review_dispatch WHERE task_id=$1),'')", second.ID).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		if state == "READY" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state, err := loadMediaReviewDispatchSQL(context.Background(), admin, second.ID); err != nil || state == nil || state.State != "READY" {
		t.Fatal("slow review blocked independent query/READY handoff", err)
	}
	if a.mediaExecution.snapshot().ReviewInFlight != 1 {
		t.Fatal("review lost its own slot")
	}
	private, err := loadTaskMediaModerationSQL(context.Background(), admin, first.ID)
	if err != nil || len(private.Candidates) != 1 {
		t.Fatal("missing private frozen review candidate", err)
	}
	laneCancel(t, s, c, identity, first.ID)
	// Cancel the waiting task before returning the first decision, so it must
	// never be reviewed; the old ALLOW must not resurrect the cancelled task.
	laneCancel(t, s, c, identity, second.ID)
	if a.mediaExecution.snapshot().ReviewInFlight != 1 {
		t.Fatal("cancel released slow review execution slot early")
	}
	unblock()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && a.mediaExecution.snapshot().ReviewInFlight != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if a.mediaExecution.snapshot().ReviewInFlight != 0 {
		t.Fatal("review slot did not drain")
	}
	final := readLeasedTask(t, admin, first.ID)
	if final.Status != "CANCELLED" || final.Moderation.State != "REVIEW" || len(final.Artifacts) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", first.ID) != 0 {
		t.Fatal("late ALLOW published cancelled candidate")
	}
	code, _, _ := authRequest(t, c, s.URL, "GET", private.Candidates[0].URL, "", identity.Project.ID, nil)
	if code != 404 {
		t.Fatal("cancelled candidate download opened", code)
	}
	if len(entered) != 0 {
		t.Fatal("cancelled waiting media was automatically reviewed")
	}
}
