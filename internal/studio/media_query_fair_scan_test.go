package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresLargeQueryScopeFairScan040(t *testing.T) {
	dsn := testDatabase(t)
	const poll = 4 * time.Second
	var mu sync.Mutex
	seen := map[string]int{}
	arrivals := make(chan string, 8)
	unblock := make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle, ok := strings.CutPrefix(r.URL.Path, mediaQueryPrefix)
		if !ok || r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			t.Error("unexpected supplier request during fair scan")
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		seen[handle]++
		mu.Unlock()
		select {
		case arrivals <- handle:
		case <-r.Context().Done():
			return
		}
		select {
		case <-unblock:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"task_id": handle, "media_type": "image", "stage": "failed"})
	}))
	defer gateway.Close()
	defer release()
	cfg := largeQueryConfig040(dsn, gateway.URL, poll)
	cfg.MediaScheduling.QueryWorkers = 33
	cfg.MediaScheduling.QueryWorkspaceConcurrency = 1
	cfg.MediaScheduling.QueryChannelConcurrency = 4
	a, _ := testApp(t, cfg)
	db := deliveryAdmin(t, dsn)
	gate, err := deliveryAdmin(t, dsn).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(t.Context())
	// Hold only the normal query selector cursor during setup. Explicitly
	// prime A's first original GET using its normal lease and HTTP client.
	if _, err = gate.Exec(t.Context(), "UPDATE studio_lane_schedule SET workspace_cursor='' WHERE lane='query' AND provider_mode='live'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), `INSERT INTO studio_workspaces(id,name) VALUES ('ws_fair_b','B');
		INSERT INTO studio_projects(id,workspace_id,name) VALUES ('prj_fair_b','ws_fair_b','B')`); err != nil {
		t.Fatal(err)
	}
	records := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().Add(-time.Minute))[:4]
	var ids, handles []string
	for i, r := range records {
		if i >= 2 {
			r.Task.WorkspaceID = "ws_fair_b"
			r.Task.ProjectID, r.Input.ProjectID = "prj_fair_b", "prj_fair_b"
		}
		ids = append(ids, r.Task.ID)
		handles = append(handles, r.Handle.ID)
	}
	seedQueryCapacity(t, a, db, records)
	first := schedulerStepAsync(a, mediaQueryLane, ids[0])
	waitGET := func(want string, limit time.Duration) {
		t.Helper()
		select {
		case got := <-arrivals:
			if got != want {
				t.Fatal("supplier GET arrived out of order")
			}
		case <-time.After(limit):
			t.Fatal("expected original GET did not reach the supplier before the deadline")
		}
	}
	waitGET(handles[0], 3*time.Second)
	baseline := a.mediaScheduler.snapshot().Lanes[mediaQueryLane].CapacityDeferrals
	if err = gate.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * poll)
	for a.mediaScheduler.snapshot().Lanes[mediaQueryLane].CapacityDeferrals == baseline {
		if time.Now().After(deadline) {
			t.Fatal("did not select the second due A task while its workspace was full")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// With the previous dispatcher this takes a full four seconds: the
	// denial for A is interpreted as idle and neither slow GET has ended.
	waitGET(handles[2], poll/3)

	start := time.Now()
	attempts := a.mediaExecution.snapshot().QueryDispatchAttempts
	time.Sleep(poll + poll/4)
	spent := a.mediaExecution.snapshot().QueryDispatchAttempts - attempts
	windows := uint64(time.Since(start)/poll) + 2
	if spent == 0 || spent > windows*8 ||
		a.mediaScheduler.snapshot().Lanes[mediaQueryLane].ScopeInFlight != 2 {
		t.Fatal("fully occupied workspaces spun or lost a held original GET slot", spent)
	}
	mu.Lock()
	blockedOK := len(seen) == 2 && seen[handles[0]] == 1 && seen[handles[2]] == 1
	mu.Unlock()
	if !blockedOK {
		t.Fatal("workspace budget admitted another supplier GET before release")
	}
	release()
	schedulerStepDone(t, first, true)
	deadline = time.Now().Add(3 * poll)
	for !queryCapacityFinal(t, a, db, ids, handles) {
		if time.Now().After(deadline) {
			t.Fatal("not all four original GETs completed after release")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start, attempts = time.Now(), a.mediaExecution.snapshot().QueryDispatchAttempts
	time.Sleep(poll + poll/4)
	idle := a.mediaExecution.snapshot().QueryDispatchAttempts - attempts
	if idle == 0 || idle > uint64(time.Since(start)/poll)+3 {
		t.Fatal("empty queue used the extra scope scan budget", idle)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, handle := range handles {
		if seen[handle] != 1 {
			t.Fatal("original GET was omitted or duplicated")
		}
	}
	t.Logf("scope-blocked scan attempts=%d, empty attempts=%d, original GETs=%d, peak occupied workspaces=2", spent, idle, len(seen))
}
