package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLargeQueryMaintenanceFormat040(t *testing.T) {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil || len(names) < 40 || names[39] != "migrations/040_media_query_capacity.sql" ||
		len(names) != maintenanceSchemaVersion || reconciliationSchemaVersion != maintenanceSchemaVersion {
		t.Fatal("online and offline tools have different migration histories", err, len(names))
	}
}

func TestLargeQueryLimits040(t *testing.T) {
	for _, c := range []MediaSchedulingConfig{
		{QueryWorkers: 33}, {QueryWorkers: 1537, QueryDBConnections: 1}, {QueryWorkers: 1536, QueryDBConnections: 33},
		{QueryWorkers: 32, QueryChannelConcurrency: 1537}, {QueryWorkspaceConcurrency: -1}, {QueryWorkers: 4, QueryDBConnections: 5},
	} {
		if c.normalize(4) == nil {
			t.Fatal("accepted invalid query configuration", c)
		}
	}
	c := MediaSchedulingConfig{QueryWorkers: 1536, QueryDBConnections: 4, ChannelConcurrency: 2, WorkspaceConcurrency: 1, QueryWorkspaceConcurrency: 40, QueryChannelConcurrency: 50}
	if err := c.normalize(4); err != nil {
		t.Fatal(err)
	}
	if a, err := newIsolatedApp(t, Config{MediaScheduling: c}); err == nil {
		a.Close()
		t.Fatal("allowed expansion without durable leases")
	}
	s := newMediaScheduler(c)
	if s.channelLimit(mediaSubmitLane) != 2 || s.workspaceLimit(mediaSubmitLane) != 1 || s.channelLimit(mediaDeliveryLane) != 2 || s.workspaceLimit(mediaReviewLane) != 1 {
		t.Fatal("query overrides changed another lane")
	}
	acquire := func(ws string) bool {
		release, ok := s.tryReserve(mediaScheduleCandidate{Lane: mediaQueryLane, WorkspaceID: ws, ChannelKey: "same-credential"})
		if ok {
			t.Cleanup(release)
		}
		return ok
	}
	for range 40 {
		if !acquire("a") {
			t.Fatal("query workspace closed early")
		}
	}
	if acquire("a") {
		t.Fatal("bypassed query workspace limit")
	}
	for range 10 {
		if !acquire("b") {
			t.Fatal("second workspace lost remaining shared channel capacity")
		}
	}
	if acquire("c") {
		t.Fatal("splitting workspaces bypassed the frozen channel limit")
	}
	original := newMediaScheduler(MediaSchedulingConfig{QueryWorkers: 16, WorkspaceConcurrency: 3, ChannelConcurrency: 9})
	if original.channelLimit(mediaQueryLane) != 9 || original.workspaceLimit(mediaQueryLane) != 3 {
		t.Fatal("missing overrides did not retain original scope budgets")
	}
}

func largeQueryConfig040(dsn, origin string, poll time.Duration) Config {
	return Config{Mode: "live", DatabaseURL: dsn, PollInterval: poll, TaskLeaseDuration: 60 * time.Second,
		AsyncImageBaseURL: origin, AsyncImageAPIKey: fixtureMediaKey, AsyncImageModel: "capacity-fixture", AsyncImageCredentialID: "capacity-fixture", MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()},
		MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: MaxMediaQueryWorkers, QueryDBConnections: 4, DeliveryWorkers: 1, ReviewWorkers: 1, WorkspaceConcurrency: 1, ChannelConcurrency: 2, QueryWorkspaceConcurrency: 280, QueryChannelConcurrency: 256}}
}

func TestPostgresLargeOriginalQuerySockets040(t *testing.T) {
	dsn := testDatabase(t)
	const total = 260
	const allowed = 256
	var mutex sync.Mutex
	seen := map[string]int{}
	var active, peak, unexpected int
	parked := make(chan struct{}, 1)
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, valid := strings.CutPrefix(r.URL.Path, mediaQueryPrefix)
		mutex.Lock()
		if !valid || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey || r.URL.RawQuery != "" || !strings.HasPrefix(id, "asyncimg_scale_") {
			unexpected++
			mutex.Unlock()
			http.Error(w, "unexpected query", 400)
			return
		}
		seen[id]++
		active++
		peak = max(peak, active)
		if active == allowed {
			select {
			case parked <- struct{}{}:
			default:
			}
		}
		mutex.Unlock()
		defer func() { mutex.Lock(); active--; mutex.Unlock() }()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-unblock:
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"task_id": id, "media_type": "image", "stage": "failed"})
	}))
	defer server.Close()
	defer release()
	const poll = 20 * time.Millisecond
	a, _ := testApp(t, largeQueryConfig040(dsn, server.URL, poll))
	db := deliveryAdmin(t, dsn)
	start := time.Now()
	baseline := a.mediaExecution.snapshot().QueryDispatchAttempts
	time.Sleep(5 * poll)
	idle := a.mediaExecution.snapshot().QueryDispatchAttempts - baseline
	if idle < 1 || idle > uint64(time.Since(start)/poll)+2 {
		t.Fatal("idle work scaled with configured GET slots", idle)
	}
	p := a.media.(*AsyncMediaProvider)
	if p.queryClient.Transport.(*http.Transport).MaxConnsPerHost != MaxMediaQueryWorkers {
		t.Fatal("native GET transport retained the legacy 32-connection ceiling")
	}
	base := queryCapacityRecords(p, time.Now().UTC().Add(-time.Minute))
	records := make([]*taskRecord, total)
	ids := make([]string, total)
	handles := make([]string, total)
	for i := range records {
		r := *base[i%len(base)]
		r.Task.ID = fmt.Sprintf("task_scale_%04d", i)
		r.Handle.ID = fmt.Sprintf("asyncimg_scale_%04d", i)
		records[i] = &r
		ids[i] = r.Task.ID
		handles[i] = r.Handle.ID
	}
	seedQueryCapacity(t, a, db, records)
	select {
	case <-parked:
	case <-time.After(24 * time.Second):
		mutex.Lock()
		n := peak
		mutex.Unlock()
		t.Fatal("large lane did not reach 256 simultaneous original GETs", n)
	}
	if a.mediaExecution.snapshot().QueryLimit != MaxMediaQueryWorkers || a.mediaScheduler.snapshot().Lanes[mediaQueryLane].ScopeInFlight != allowed {
		t.Fatal("actual occupancy exceeded or failed to reach configured scope limit")
	}
	func() {
		timeout, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		pids := map[uint32]bool{}
		for range 4 {
			c, err := a.taskLeases.acquireLane(timeout, mediaQueryLane)
			if err != nil {
				t.Fatal("slow GET retained query database connection", err)
			}
			defer a.taskLeases.release(c)
			pids[c.PgConn().PID()] = true
		}
		if len(pids) != 4 || cap(a.taskLeases.connections[mediaQueryLane]) != 4 {
			t.Fatal("query pool did not have exactly four physical backends")
		}
	}()
	if count := outboxCount(t, db, `SELECT count(*) FROM studio_tasks WHERE id=ANY($1::text[]) AND lease_owner<>'' AND lease_until>clock_timestamp()`, ids); count != allowed {
		t.Fatal("excess or missing persisted claims while sockets were blocked", count)
	}
	release()
	deadline := time.Now().Add(30 * time.Second)
	for !queryCapacityFinal(t, a, db, ids, handles) {
		if a.ctx.Err() != nil || time.Now().After(deadline) {
			t.Fatal("260 original GETs did not commit their own terminals")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mutex.Lock()
	defer mutex.Unlock()
	for _, h := range handles {
		if seen[h] != 1 {
			t.Fatal("missing or duplicated original GET")
		}
	}
	if len(seen) != total || active != 0 || unexpected != 0 || peak != allowed || a.mediaExecution.snapshot().SubmitSteps != 0 {
		t.Fatal("query accounting", len(seen), active, unexpected, peak)
	}
	t.Logf("MEDIA_QUERY_040 worker_limit=%d db_connections=4 workspace_limit=280 channel_limit=%d peak_http=%d claimed=%d unique_get=%d other_http=%d idle_dispatch_attempts=%d", MaxMediaQueryWorkers, allowed, peak, a.mediaExecution.snapshot().QuerySteps, len(seen), unexpected, idle)
}

func TestPostgresLargeQueryHandoffFences040(t *testing.T) {
	dsn := testDatabase(t)
	var sent atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent.Add(1); http.Error(w, "unexpected", 500) }))
	defer gateway.Close()
	a, _ := testApp(t, largeQueryConfig040(dsn, gateway.URL, time.Hour))
	db := deliveryAdmin(t, dsn)
	records := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().Add(-time.Minute))[:5]
	seedQueryCapacity(t, a, db, records)
	for i, tc := range []struct {
		name       string
		afterClaim bool
		sql        string
	}{
		{"cooldown after fair selection", false, `UPDATE studio_media_channel_runtime SET cooldown_until=clock_timestamp()+interval '1 hour',probe_required=true,version=version+1 WHERE lane='query' AND channel_key=$1`},
		{"reduced channel after claim", true, `UPDATE studio_media_channel_runtime SET effective_limit=5,version=version+1 WHERE lane='query' AND channel_key=$1`},
		{"new cooldown after claim", true, `UPDATE studio_media_channel_runtime SET cooldown_until=clock_timestamp()+interval '1 hour',probe_required=true,version=version+1 WHERE lane='query' AND channel_key=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Only the fixture establishes a healthy original row before this case.
			key := records[i].Handle.ProviderIdentity
			_, err := db.Exec(t.Context(), `INSERT INTO studio_media_channel_runtime(scope,lane,channel_key,effective_limit) VALUES('gateway','query',$1,256)
 ON CONFLICT(scope,lane,channel_key) DO UPDATE SET effective_limit=256,cooldown_until='epoch',probe_required=false,probe_id='',probe_until=NULL,version=studio_media_channel_runtime.version+1`, key)
			if err != nil {
				t.Fatal(err)
			}
			c, err := a.taskLeases.acquireLane(t.Context(), mediaQueryLane)
			if err != nil {
				t.Fatal(err)
			}
			defer a.taskLeases.release(c)
			candidate, err := a.mediaScheduleCandidateByID(t.Context(), c, mediaQueryLane, records[i].Task.ID)
			if err != nil || candidate == nil {
				t.Fatal("expected due original candidate", err)
			}
			change := func() {
				t.Helper()
				if _, err := db.Exec(t.Context(), tc.sql, key); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.afterClaim {
				change()
			}
			r, err := a.taskLeases.claimScheduled(t.Context(), c, *candidate)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.afterClaim {
				if r != nil {
					t.Fatal("queued candidate bypassed newer cooldown")
				}
				return
			}
			if r == nil {
				t.Fatal("expected committed original claim before contention")
			}
			change()
			a.executeReservedTaskClaim(nil, r)
			if sent.Load() != 0 || a.ctx.Err() != nil {
				t.Fatal("stale handoff sent HTTP or stopped other lanes")
			}
			if outboxCount(t, db, `SELECT count(*) FROM studio_tasks WHERE id=$1 AND handle->>'ID'=$2 AND document->>'status'='RUNNING' AND (document->>'version')::bigint=1`, r.Task.ID, r.Handle.ID) != 1 {
				t.Fatal("stale handoff changed original task")
			}
		})
	}
	t.Run("only original half-open probe may pass", func(t *testing.T) {
		key := records[3].Handle.ProviderIdentity
		if _, err := db.Exec(t.Context(), `UPDATE studio_media_channel_runtime SET cooldown_until='epoch', effective_limit=1, probe_required=true,probe_id='',probe_until=NULL,version=version+1 WHERE lane='query' AND channel_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		c, err := a.taskLeases.acquireLane(t.Context(), mediaQueryLane)
		if err != nil {
			t.Fatal(err)
		}
		defer a.taskLeases.release(c)
		one, err := a.mediaScheduleCandidateByID(t.Context(), c, mediaQueryLane, records[3].Task.ID)
		if err != nil || one == nil {
			t.Fatal("ready probe missing", err)
		}
		two, err := a.mediaScheduleCandidateByID(t.Context(), c, mediaQueryLane, records[4].Task.ID)
		if err != nil || two == nil {
			t.Fatal("competing candidate missing", err)
		}
		r, err := a.taskLeases.claimScheduled(t.Context(), c, *one)
		if err != nil || r == nil || r.Lease.ProbeID == "" {
			t.Fatal("could not claim the original half-open probe", err)
		}
		if next, err := a.taskLeases.claimScheduled(t.Context(), c, *two); err != nil || next != nil {
			t.Fatal("claimed a second half-open probe", err)
		}
		a.executeReservedTaskClaim(nil, r)
		if sent.Load() != 1 || a.ctx.Err() != nil {
			t.Fatal("the one current half-open original was not queried")
		}
	})
}
