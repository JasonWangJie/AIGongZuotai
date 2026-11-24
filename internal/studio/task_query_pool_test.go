package studio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPooledQueryLimitsAndWait(t *testing.T) {
	for _, cfg := range []MediaSchedulingConfig{{QueryWorkers: 4, QueryDBConnections: 5}, {QueryWorkers: 4, QueryDBConnections: -1}} {
		if cfg.normalize(2) == nil {
			t.Fatal("accepted an out-of-range database budget")
		}
	}
	cfg := MediaSchedulingConfig{QueryWorkers: 32, QueryDBConnections: 4}
	if err := cfg.normalize(2); err != nil {
		t.Fatal(err)
	}
	if a, err := newIsolatedApp(t, Config{MediaScheduling: cfg}); err == nil {
		a.Close()
		t.Fatal("enabled a PostgreSQL option in memory mode")
	}
	p := &taskLeasePool{duration: 150 * time.Millisecond}
	p.connections[mediaQueryLane] = make(chan *pgx.Conn, 1)
	token := TaskLease{Lane: mediaQueryLane, Version: 3, queryStatus: "RUNNING", queryHandleJSON: "{}"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(p.renewPooledQuery(ctx, token), context.Canceled) {
		t.Fatal("cancelled queue wait did not return")
	}
	requireOperation(t, p.renewPooledQuery(t.Context(), token), "MEDIA_QUERY_LEASE_BUSY")
}

func TestPooledQueryWireGuard(t *testing.T) {
	var sent atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer s.Close()
	p := mediaFixtureProvider(t, s)
	ctx := context.WithValue(t.Context(), queryWireGuardKey{}, func() bool { return false })
	if _, err := p.QueryStatus(ctx, mediaFixtureHandle(p, "image", 1)); !isLeaseLost(err) || sent.Load() != 0 {
		t.Fatal("expired lease reached native GET transport", err)
	}
}

func pooledQueryApp(t *testing.T, gateway *httptest.Server, poll, lease time.Duration) (*App, *pgx.Conn, string) {
	t.Helper()
	dsn := testDatabase(t)
	cfg := Config{Mode: "live", DatabaseURL: dsn, PollInterval: poll, TaskLeaseDuration: lease, MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()},
		AsyncImageBaseURL: gateway.URL, AsyncImageAPIKey: fixtureMediaKey, AsyncImageModel: "capacity-fixture", AsyncImageCredentialID: "capacity-fixture",
		MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 32, QueryDBConnections: 4, DeliveryWorkers: 1, ReviewWorkers: 1, WorkspaceConcurrency: 32, ChannelConcurrency: 32}}
	a, _ := testApp(t, cfg)
	return a, deliveryAdmin(t, dsn), dsn
}

func TestPostgresPooledQuerySlowSockets(t *testing.T) {
	probe := &queryCapacityProbe{samples: make(map[string]*queryCapacitySample), handlersDone: make(chan struct{})}
	arrived := make(chan struct{}, 8)
	var arrivals atomic.Int32
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrivals.Add(1) <= 8 {
			arrived <- struct{}{}
		}
		select {
		case <-unblock:
			probe.serve(w, r)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	defer release()
	a, db, _ := pooledQueryApp(t, server, queryCapacityPoll, 15*time.Second)
	provider := a.media.(*AsyncMediaProvider)
	provider.queryClient.Transport = &queryCapacityTransport{base: provider.queryClient.Transport, probe: probe}
	due := time.Now().UTC().Truncate(time.Microsecond)
	records := queryCapacityRecords(provider, due)
	ids, handles := []string{}, []string{}
	for _, r := range records {
		ids = append(ids, r.Task.ID)
		handles = append(handles, r.Handle.ID)
		probe.samples[r.Handle.ID] = &queryCapacitySample{due: r.NextPoll}
	}
	seedQueryCapacity(t, a, db, records)
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	for range 8 {
		select {
		case <-arrived:
		case <-timer.C:
			t.Fatal("eight original GETs could not coexist over four database slots")
		}
	}
	pool := a.taskLeases
	if cap(pool.connections[mediaQueryLane]) != 4 || a.mediaExecution.snapshot().QueryLimit != 32 {
		t.Fatal("configured HTTP and database widths differ")
	}
	func() {
		timeout, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		pids := map[uint32]bool{}
		for range 4 {
			c, err := pool.acquireLane(timeout, mediaQueryLane)
			if err != nil {
				t.Fatal("GET retained a database connection", err)
			}
			defer pool.release(c)
			pids[c.PgConn().PID()] = true
		}
		if len(pids) != 4 || pool.connectionLane[a.store.conn] == mediaQueryLane {
			t.Fatal("query connections were not four distinct lane sessions")
		}
	}()
	release()
	select {
	case <-probe.handlersDone:
	case <-time.After(45 * time.Second):
		t.Fatal("original GETs did not drain")
	}
	deadline := time.Now().Add(10 * time.Second)
	for !queryCapacityFinal(t, a, db, ids, handles) {
		if time.Now().After(deadline) {
			t.Fatal("not all frozen handles reached their original terminal states")
		}
		time.Sleep(10 * time.Millisecond)
	}
	probe.results(t, "postgres", a, due, time.Now())
}

func TestPostgresPooledQueryLeaseFences(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected wire request", 500)
	}))
	defer server.Close()
	a, admin, dsn := pooledQueryApp(t, server, time.Hour, 15*time.Second)
	pool := a.taskLeases
	records := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().UTC().Add(-time.Minute))[:8]
	seedQueryCapacity(t, a, admin, records)
	claim := func(t *testing.T, r *taskRecord) *taskRecord {
		t.Helper()
		c, err := pool.acquireLane(t.Context(), mediaQueryLane)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.release(c)
		selection, err := a.mediaScheduleCandidateByID(t.Context(), c, mediaQueryLane, r.Task.ID)
		if err != nil || selection == nil {
			t.Fatal("missing scheduled original query", err)
		}
		got, err := pool.claimScheduled(t.Context(), c, *selection)
		if err != nil || got == nil || got.Lease.Version != r.Task.Version || got.Handle.ID != r.Handle.ID || got.Lease.queryHandleJSON == "" {
			t.Fatal("missing committed claim for original handle", err)
		}
		return got
	}
	execSQL := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, tc := range []struct{ name, change string }{
		{"version", `UPDATE studio_tasks SET document=jsonb_set(document,'{version}',to_jsonb((document->>'version')::bigint+1)) WHERE id=$1`},
		{"handle", `UPDATE studio_tasks SET handle=jsonb_set(handle,'{CredentialID}',to_jsonb('changed-fixture'::text)) WHERE id=$1`},
		{"owner", `UPDATE studio_tasks SET lease_owner='replacement' WHERE id=$1`},
		{"epoch", `UPDATE studio_tasks SET lease_epoch=lease_epoch+1 WHERE id=$1`},
		{"unknown", `UPDATE studio_tasks SET document=jsonb_set(document,'{status}','"RECONCILE_REQUIRED"') WHERE id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := claim(t, records[i])
			execSQL(t, tc.change, r.Task.ID)
			if err := pool.renewPooledQuery(t.Context(), *r.Lease); !isLeaseLost(err) {
				t.Fatalf("invalid claim renewed: %v", err)
			}
			a.executeReservedTaskClaim(nil, r)
			a.mu.Lock()
			saveErr := a.persistTaskClaim(r, true)
			a.mu.Unlock()
			if !isLeaseLost(saveErr) {
				t.Fatal("an invalidated external result passed the final control transaction", saveErr)
			}
			if requests.Load() != 0 || a.ctx.Err() != nil {
				t.Fatal("invalid claim allowed external work or poisoned unrelated lanes")
			}
		})
	}

	t.Run("expiry during row wait", func(t *testing.T) {
		r := claim(t, records[5])
		execSQL(t, `UPDATE studio_tasks SET lease_until=clock_timestamp()+interval '600 milliseconds' WHERE id=$1`, r.Task.ID)
		locker := deliveryAdmin(t, dsn)
		tx, err := locker.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		if _, err = tx.Exec(t.Context(), "SELECT id FROM studio_tasks WHERE id=$1 FOR UPDATE", r.Task.ID); err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		saveErr := a.persistTaskClaim(r, true)
		a.mu.Unlock()
		if !isQueryLeaseBusy(saveErr) {
			t.Fatal("expected conservative contention before result save", saveErr)
		}
		done := make(chan struct{})
		go func() { defer close(done); a.executeReservedTaskClaim(nil, r) }()
		var pids []int32
		for c, lane := range pool.connectionLane {
			if lane == mediaQueryLane {
				pids = append(pids, int32(c.PgConn().PID()))
			}
		}
		waitReplayCount(t, admin, 1, `SELECT count(*) FROM pg_stat_activity WHERE pid=ANY($1::int[]) AND wait_event_type='Lock'`, pids)
		waitReplayCount(t, admin, 1, `SELECT count(*) FROM studio_tasks WHERE id=$1 AND lease_until<=clock_timestamp()`, r.Task.ID)
		if err = tx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("expired request remained in the execution lane")
		}
		if requests.Load() != 0 || a.ctx.Err() != nil {
			t.Fatal("expired original claim sent a query or stopped the controller")
		}
		a.mu.Lock()
		saveErr = a.persistTaskClaim(r, true)
		a.mu.Unlock()
		if !isLeaseLost(saveErr) {
			t.Fatal("expired original result was accepted after the lock was released", saveErr)
		}
	})
	t.Run("generation", func(t *testing.T) {
		r := claim(t, records[6])
		execSQL(t, `UPDATE studio_task_executor SET generation='new-generation' WHERE singleton`)
		if err := pool.renewPooledQuery(t.Context(), *r.Lease); !isLeaseLost(err) {
			t.Fatal("old generation renewed", err)
		}
		a.executeReservedTaskClaim(nil, r)
		if requests.Load() != 0 || a.ctx.Err() != nil {
			t.Fatal("replaced generation sent a request")
		}
	})
}

func TestPostgresPooledQueryUpstreamFailureKeepsHandle(t *testing.T) {
	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == mediaQueryPrefix+"asyncimg_capacity_000" {
			gets.Add(1)
		} else {
			posts.Add(1)
		}
		w.Header().Set("Retry-After", "30")
		http.Error(w, "busy", 503)
	}))
	defer server.Close()
	a, db, _ := pooledQueryApp(t, server, time.Hour, 15*time.Second)
	r := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().UTC().Add(-time.Minute))[0]
	seedQueryCapacity(t, a, db, []*taskRecord{r})
	if !a.executePersistentTaskLane(mediaQueryLane, r.Task.ID) {
		t.Fatal("due original GET was not claimed")
	}
	if gets.Load() != 1 || posts.Load() != 0 || a.executePersistentTaskStep(r.Task.ID) {
		t.Fatal("busy channel repeated external work")
	}
	if outboxCount(t, db, `SELECT count(*) FROM studio_tasks WHERE id=$1 AND handle->>'ID'=$2 AND document->>'status'='PROVIDER_STATE_UNKNOWN' AND lease_owner='' AND next_poll>clock_timestamp()`, r.Task.ID, r.Handle.ID) != 1 {
		t.Fatal("transient response lost the original handle, cooldown or conservative task state")
	}
}

func TestPostgresPooledQueryKnownResponseSaveRetry(t *testing.T) {
	arrived := make(chan struct{}, 1)
	reply := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(reply) }) }
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) != 1 || r.Method != "GET" || r.URL.Path != mediaQueryPrefix+"asyncimg_capacity_000" {
			http.Error(w, "unexpected request", 400)
			return
		}
		arrived <- struct{}{}
		select {
		case <-r.Context().Done():
			return
		case <-reply:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task_id":"asyncimg_capacity_000","media_type":"image","stage":"failed"}`))
		}
	}))
	defer server.Close()
	defer release()
	a, admin, dsn := pooledQueryApp(t, server, time.Hour, 15*time.Second)
	r := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().Add(-time.Minute))[0]
	seedQueryCapacity(t, a, admin, []*taskRecord{r})
	if _, err := admin.Exec(t.Context(), `UPDATE studio_tasks SET handle=jsonb_set(handle,'{fixture_optional}','"accepted"') WHERE id=$1`, r.Task.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- a.executePersistentTaskLane(mediaQueryLane, r.Task.ID) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("original GET did not arrive")
	}
	locker := deliveryAdmin(t, dsn)
	tx, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), "SELECT id FROM studio_tasks WHERE id=$1 FOR UPDATE", r.Task.ID); err != nil {
		t.Fatal(err)
	}
	release()
	var pids []int32
	for c, lane := range a.taskLeases.connectionLane {
		if lane == mediaQueryLane {
			pids = append(pids, int32(c.PgConn().PID()))
		}
	}
	waitReplayCount(t, admin, 1, `SELECT count(*) FROM pg_stat_activity WHERE pid=ANY($1::int[]) AND wait_event_type='Lock'`, pids)
	lockDeadline := time.Now().Add(time.Second)
	for !a.mu.TryLock() {
		if time.Now().After(lockDeadline) {
			t.Fatal("retrying a locked task row blocked the application state lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.Unlock()
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("worker lost its confirmed original GET")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("known response did not commit after releasing its task row")
	}
	if requests.Load() != 1 || !queryCapacityFinal(t, a, admin, []string{r.Task.ID}, []string{r.Handle.ID}) {
		t.Fatal("saving a known response repeated HTTP or lost its original terminal")
	}
	if outboxCount(t, admin, `SELECT count(*) FROM studio_tasks WHERE id=$1 AND handle->>'fixture_optional'='accepted'`, r.Task.ID) != 1 {
		t.Fatal("persisting the query rewrote an optional field on its original handle")
	}
}

func TestPostgresPooledQueryHeartbeatStarvation(t *testing.T) {
	arrived, stopped, escape := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	var gets, others atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != mediaQueryPrefix+"asyncimg_capacity_000" || gets.Add(1) != 1 {
			others.Add(1)
			w.WriteHeader(500)
			return
		}
		arrived <- struct{}{}
		select {
		case <-r.Context().Done():
			stopped <- struct{}{}
		case <-escape:
		}
	}))
	defer s.Close()
	defer close(escape)
	const lease = 5 * time.Second
	a, db, _ := pooledQueryApp(t, s, time.Hour, lease)
	r := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().Add(-time.Minute))[0]
	seedQueryCapacity(t, a, db, []*taskRecord{r})
	finished := make(chan bool, 1)
	go func() { finished <- a.executePersistentTaskLane(mediaQueryLane, r.Task.ID) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("original request never reached the socket")
	}
	if outboxCount(t, db, `SELECT count(*) FROM studio_tasks t CROSS JOIN studio_task_executor e WHERE t.id=$1 AND t.handle->>'ID'=$2 AND (t.document->>'version')::bigint=$3 AND t.lease_epoch>0 AND t.lease_owner<>'' AND t.lease_until>clock_timestamp() AND e.singleton AND e.generation=$4`, r.Task.ID, r.Handle.ID, int64(r.Task.Version), a.taskLeases.generation) != 1 {
		t.Fatal("actual GET did not have a committed live claim for its original handle and version")
	}
	pool := a.taskLeases
	borrow, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for range 4 {
		conn, err := pool.acquireLane(borrow, mediaQueryLane)
		if err != nil {
			t.Fatal("GET held a query SQL connection", err)
		}
		defer pool.release(conn)
	}
	select {
	case <-stopped:
	case <-time.After(lease + time.Second):
		t.Fatal("expired heartbeat kept the upstream HTTP request alive")
	}
	select {
	case ok := <-finished:
		if !ok {
			t.Fatal("expected an originally accepted GET claim")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled HTTP did not relinquish its worker without a database slot")
	}
	if gets.Load() != 1 || others.Load() != 0 || a.ctx.Err() != nil || a.mediaExecution.snapshot().QueryInFlight != 0 {
		t.Fatal("invalid HTTP accounting, controller state, or unreleased query worker")
	}
	if outboxCount(t, db, `SELECT count(*) FROM studio_tasks WHERE id=$1 AND handle->>'ID'=$2 AND document->>'status'='RUNNING' AND (document->>'version')::bigint=$3`, r.Task.ID, r.Handle.ID, int64(r.Task.Version)) != 1 {
		t.Fatal("late result changed the frozen handle or published a spurious failure")
	}
}
