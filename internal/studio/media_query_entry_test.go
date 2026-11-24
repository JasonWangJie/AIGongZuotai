package studio

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestLargeQueryEntryGuard249(t *testing.T) {
	a := &App{config: Config{MediaScheduling: MediaSchedulingConfig{QueryWorkers: 64}}}
	newClaim := func() *taskRecord {
		return &taskRecord{Lease: &TaskLease{Lane: mediaQueryLane, queryStatus: "RUNNING", queryHandleJSON: "{}"},
			Handle: MediaHandle{ID: "opaque-fixture"}, ScheduleClaim: &mediaScheduleCandidate{Lane: mediaQueryLane}}
	}
	if !a.canEnterQueryWithoutAppLock(nil, newClaim()) || a.canEnterQueryWithoutAppLock(&pgx.Conn{}, newClaim()) {
		t.Fatal("only the pooled original GET may enter independently")
	}
	a.config.MediaScheduling.QueryWorkers = 32
	if a.canEnterQueryWithoutAppLock(nil, newClaim()) {
		t.Fatal("the low configuration changed paths")
	}
	a.config.MediaScheduling.QueryWorkers = 33
	for name, mutate := range map[string]func(*taskRecord){
		"submit":     func(r *taskRecord) { r.Lease.Lane = mediaSubmitLane },
		"status":     func(r *taskRecord) { r.Lease.queryStatus = "" },
		"snapshot":   func(r *taskRecord) { r.Lease.queryHandleJSON = "" },
		"handle":     func(r *taskRecord) { r.Handle.ID = "" },
		"claim":      func(r *taskRecord) { r.ScheduleClaim = nil },
		"claim_lane": func(r *taskRecord) { r.ScheduleClaim.Lane = mediaReviewLane },
		"delivery":   func(r *taskRecord) { r.Delivery = &MediaDeliveryRecord{} },
		"review":     func(r *taskRecord) { r.ReviewDispatch = &MediaReviewDispatch{} },
		"candidates": func(r *taskRecord) { r.MediaModeration = &MediaModerationRecord{Candidates: []Artifact{{}}} },
	} {
		t.Run(name, func(t *testing.T) {
			r := newClaim()
			mutate(r)
			if a.canEnterQueryWithoutAppLock(nil, r) {
				t.Fatal("bypassed the original preparation")
			}
		})
	}
}

func TestLargeQueryPinsUnderAppLock249(t *testing.T) {
	a := &App{store: &postgresStore{}, config: Config{MaxRecords: 1}, tasks: map[string]*taskRecord{}}
	a.cacheIdleTaskLocked(&taskRecord{Task: Task{ID: "resident", Status: "RUNNING", Version: 1}})
	old := TaskLease{TaskID: "queried", Owner: "old", Generation: "g1", Epoch: 1}
	next := TaskLease{TaskID: "queried", Owner: "next", Generation: "g2", Epoch: 2}
	a.mu.Lock()
	pinned := make(chan func(), 1)
	go func() { pinned <- a.addTaskClaimPin(old) }()
	var releaseOld func()
	select {
	case releaseOld = <-pinned:
	case <-time.After(time.Second):
		a.mu.Unlock()
		t.Fatal("pin waited for an unrelated global write")
	}
	releaseNext := a.addTaskClaimPin(next)
	a.cacheIdleTaskLocked(&taskRecord{Task: Task{ID: old.TaskID, Status: "FAILED", Version: 4}, Lease: &next})
	a.evictTerminalsLocked()
	a.mu.Unlock()
	releaseOld()
	current := a.tasks[old.TaskID]
	if current == nil || current.Task.Version != 4 || current.Lease != &next || a.activeTaskClaims[old.TaskID] != 1 {
		t.Fatal("releasing a superseded attempt removed a newer cache version or lease")
	}
	releaseNext()
	if len(a.tasks) != 1 || a.tasks[old.TaskID] != nil || len(a.activeTaskClaims) != 0 {
		t.Fatal("last pin did not trim the over-budget record")
	}
}

// Return one original terminal through the same media HTTP adapter as the curve.
func queryEntryGateway249(t *testing.T) (*httptest.Server, <-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	arrived := make(chan struct{}, 1)
	reply := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(reply) }) }
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) != 1 || r.Method != http.MethodGet || r.URL.Path != mediaQueryPrefix+"asyncimg_capacity_000" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			http.Error(w, "unexpected query", 400)
			return
		}
		arrived <- struct{}{}
		select {
		case <-r.Context().Done():
			return
		case <-reply:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"asyncimg_capacity_000","media_type":"image","stage":"failed"}`))
	}))
	t.Cleanup(func() { release(); s.Close() })
	return s, arrived, release, &requests
}

func readEntryTask249(t *testing.T, api *httptest.Server, id, status string, version uint64) {
	t.Helper()
	code, raw, _ := request(t, api, http.MethodGet, "/api/v1/tasks/"+id, "", nil)
	if code != 200 {
		t.Fatalf("task read returned HTTP %d", code)
	}
	got := decode[Task](t, raw)
	if got.ID != id || got.Status != status || got.Version != version {
		t.Fatalf("task read returned %s v%d; expected %s v%d", got.Status, got.Version, status, version)
	}
}

func TestPostgresLargeQueryContendedEntryAndSaveRetry249(t *testing.T) {
	dsn := testDatabase(t)
	up, arrived, reply, requests := queryEntryGateway249(t)
	a, api := testApp(t, largeQueryConfig040(dsn, up.URL, time.Hour))
	admin := deliveryAdmin(t, dsn)
	r := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().Add(-time.Minute))[0]
	id := r.Task.ID
	seedQueryCapacity(t, a, admin, []*taskRecord{r})
	readEntryTask249(t, api, id, "RUNNING", 1)
	events := make(chan event, 4)
	a.mu.Lock()
	a.workspaceSubscribers[events] = subscriptionScope{WorkspaceID: r.Task.WorkspaceID, ProjectID: r.Task.ProjectID}
	var once sync.Once
	unlock := func() { once.Do(a.mu.Unlock) }
	defer unlock()
	defer reply()
	done := make(chan bool, 1)
	go func() { done <- a.executePersistentTaskLane(mediaQueryLane, id) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("holding App.mu prevented an already claimed original GET from reaching the server")
	}

	// The initial renewal has already succeeded. Holding this row now causes the
	// first save to roll back via NOWAIT and the retry to wait on its own lease.
	locker := deliveryAdmin(t, dsn)
	tx, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), "SELECT id FROM studio_tasks WHERE id=$1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	reply()
	unlock()
	var pids []int32
	for c, lane := range a.taskLeases.connectionLane {
		if lane == mediaQueryLane {
			pids = append(pids, int32(c.PgConn().PID()))
		}
	}
	waitReplayCount(t, admin, 1, `SELECT count(*) FROM pg_stat_activity WHERE pid=ANY($1::int[]) AND wait_event_type='Lock'`, pids)
	readEntryTask249(t, api, id, "RUNNING", 1)
	// This live provider has no confirmed cancellation endpoint.
	code, _, _ := request(t, api, http.MethodPost, "/api/v1/tasks/"+id+"/cancel", "", nil)
	if code != http.StatusConflict {
		t.Fatalf("concurrent live cancellation returned %d", code)
	}
	if len(events) != 0 || requests.Load() != 1 || outboxCount(t, admin, `SELECT count(*) FROM studio_tasks WHERE id=$1 AND document->>'status'='RUNNING' AND document->>'version'='1'`, id) != 1 {
		t.Fatal("uncommitted result became visible or sent another GET")
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("did not execute original claim")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("known result did not finish after rollback")
	}
	readEntryTask249(t, api, id, "FAILED", 2)
	if len(events) != 1 {
		t.Fatalf("got %d committed notifications", len(events))
	}
	e := <-events
	published := decode[map[string]Task](t, e.Data)["task"]
	if e.Name != "task.updated" || published.ID != id || published.Status != "FAILED" || published.Version != 2 || !queryCapacityFinal(t, a, admin, []string{id}, []string{r.Handle.ID}) || a.executePersistentTaskStep(id) || requests.Load() != 1 {
		t.Fatal("terminal read, database, event or original request count differs")
	}
}

func TestPostgresLargeQueryFinalCommitUnknown249(t *testing.T) {
	dsn := testDatabase(t)
	up, _, reply, requests := queryEntryGateway249(t)
	reply()
	proxy, armed, dropped := recoveryControlAckProxy(t, dsn)
	cfg := largeQueryConfig040(proxy, up.URL, time.Hour)
	cfg.AssetDir, cfg.ReferenceDir = t.TempDir(), t.TempDir()
	var target atomic.Uint32
	cfg.queryTiming = &mediaQueryTiming{record: func(p queryTimingPhase, _, _ time.Time) {
		if p == queryControlWait {
			if pid := target.Swap(0); pid != 0 {
				armed.Store(pid)
			}
		}
	}}
	a, err := newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(a.Handler())
	var closed sync.Once
	closeFirst := func() { closed.Do(func() { api.Close(); a.Close() }) }
	t.Cleanup(closeFirst)
	admin := deliveryAdmin(t, dsn)
	r := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().Add(-time.Minute))[0]
	id := r.Task.ID
	seedQueryCapacity(t, a, admin, []*taskRecord{r})
	readEntryTask249(t, api, id, "RUNNING", 1)
	events := make(chan event, 4)
	a.mu.Lock()
	a.workspaceSubscribers[events] = subscriptionScope{WorkspaceID: r.Task.WorkspaceID, ProjectID: r.Task.ProjectID}
	a.mu.Unlock()
	target.Store(a.store.conn.PgConn().PID())
	if !a.executePersistentTaskLane(mediaQueryLane, id) {
		t.Fatal("did not reach the original query")
	}
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("did not drop the known result COMMIT reply")
	}
	a.mu.Lock()
	cached := a.tasks[id]
	stillOriginal := cached != nil && cached.Task.Status == "RUNNING" && cached.Task.Version == 1
	a.taskPinsMu.Lock()
	released := len(a.activeTaskClaims) == 0
	a.taskPinsMu.Unlock()
	a.mu.Unlock()
	if !a.store.failed.Load() || !stillOriginal || !released || len(events) != 0 || !queryCapacityFinal(t, a, admin, []string{id}, []string{r.Handle.ID}) || requests.Load() != 1 {
		t.Fatal("a lost commit acknowledgement replaced the cache, published an event, kept a pin or repeated HTTP")
	}
	if code, _, _ := request(t, api, http.MethodGet, "/api/v1/tasks/"+id, "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("unconfirmed controller answered task read with HTTP %d", code)
	}
	closeFirst()
	cfg.DatabaseURL = dsn
	cfg.queryTiming = nil
	recovered, api := testApp(t, cfg)
	readEntryTask249(t, api, id, "FAILED", 2)
	if recovered.executePersistentTaskStep(id) || requests.Load() != 1 {
		t.Fatal("restoring and reading a committed original terminal repeated external work")
	}
}
