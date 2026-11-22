package studio

import (
	"context"
	"testing"
	"time"
)

func TestPostgresMediaTaskCachePinSurvivesCancelAndLateIO(t *testing.T) {
	provider := newMediaLaneProvider()
	app, server := testApp(t, Config{DatabaseURL: testDatabase(t), PollInterval: time.Hour, MaxRecords: 1, mediaProvider: provider})
	t.Cleanup(func() { close(provider.stop) })
	for _, key := range []string{"resident-task", "executing-cold-task"} {
		in := taskInput("image")
		in.Inputs.Prompt = key
		if code, _, _ := request(t, server, "POST", "/api/v1/tasks", key, in); code != 202 {
			t.Fatal("fixture acceptance failed", code)
		}
	}
	in := taskInput("image")
	in.Inputs.Prompt = "executing-cold-task"
	code, raw, hdr := request(t, server, "POST", "/api/v1/tasks", in.Inputs.Prompt, in)
	if code != 202 || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatal("uncached task could not be replayed", code)
	}
	id := decode[Task](t, raw).ID
	if len(app.tasks) != 1 || app.tasks[id] != nil {
		t.Fatal("fixture must execute from a full foreign cache")
	}
	advanceMediaScheduleDue(t, app, id)
	done := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if app.executePersistentTaskLane(mediaSubmitLane, id) {
				done <- true
				return
			}
			if !waitTaskPoll(ctx, 5*time.Millisecond) {
				break
			}
		}
		done <- false
	}()
	call := laneCall(t, provider.submits, in.Inputs.Prompt)
	defer call.finish()
	path := "/api/v1/tasks/" + id
	code, raw, _ = request(t, server, "POST", path+"/cancel", "", nil)
	if code != 200 {
		t.Fatal("could not cancel while provider held its return", code)
	}
	cancelled := decode[Task](t, raw)
	app.mu.Lock()
	app.evictTerminalsLocked()
	record, pins, count := app.tasks[id], app.activeTaskClaims[id], len(app.tasks)
	app.mu.Unlock()
	if record == nil || record.Lease != nil || pins != 1 || count != 2 || cancelled.Status != "CANCELLED" {
		t.Fatal("lease revocation released the executing task pin")
	}
	call.finish()
	select {
	case worked := <-done:
		if !worked {
			t.Fatal("claimed work did not return")
		}
	case <-ctx.Done():
		t.Fatal("late provider call did not finish")
	}
	app.mu.Lock()
	record, pins, count = app.tasks[id], len(app.activeTaskClaims), len(app.tasks)
	app.mu.Unlock()
	if record != nil || pins != 0 || count != 1 {
		t.Fatal("worker did not release its excess cache entry after actual return")
	}
	code, raw, _ = request(t, server, "GET", path, "", nil)
	final := decode[Task](t, raw)
	if code != 200 || final.Status != "CANCELLED" || final.Version != cancelled.Version || len(final.Artifacts) != 0 {
		t.Fatal("late response changed the committed cancellation")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.countSubmit != 1 || provider.countQuery != 0 {
		t.Fatal("cache release or cold read repeated external work")
	}
}

func TestMediaTaskCacheOverlappingLeasePins(t *testing.T) {
	app := &App{store: &postgresStore{}, config: Config{MaxRecords: 1}, tasks: map[string]*taskRecord{}}
	app.cacheIdleTaskLocked(&taskRecord{Task: Task{ID: "resident", Version: 1}})
	first := TaskLease{TaskID: "working", Owner: "old", Epoch: 1, Generation: "gen"}
	second := TaskLease{TaskID: first.TaskID, Owner: "new", Epoch: 2, Generation: "gen"}
	releaseOld := app.pinTaskClaim(first)
	releaseNew := app.pinTaskClaim(second)
	app.cacheIdleTaskLocked(&taskRecord{Task: Task{ID: first.TaskID, Status: "SUBMITTING", Version: 4}, InFlight: true, Lease: &second})
	app.cacheIdleTaskLocked(&taskRecord{Task: Task{ID: first.TaskID, Status: "QUEUED", Version: 3}})
	releaseOld()
	current := app.tasks[first.TaskID]
	if current == nil || current.Task.Version != 4 || current.Lease != &second || !current.InFlight || app.activeTaskClaims[first.TaskID] != 1 {
		t.Fatal("late state or cleanup replaced the current lease")
	}
	// An operations decision revokes the lease as soon as its transaction
	// commits. The caller already in external I/O retains its independent pin.
	decision := cloneLeasedRecord(current)
	decision.Task.Status = "RECONCILE_REQUIRED"
	decision.Task.Version++
	app.cacheOperationsResourceLocked(operationsResource{Task: decision})
	app.evictTerminalsLocked()
	current = app.tasks[first.TaskID]
	if current == nil || current.Lease != nil || current.Task.Version != 5 || app.activeTaskClaims[first.TaskID] != 1 {
		t.Fatal("operations revocation lost the committed decision or released a running attempt")
	}
	releaseNew()
	if len(app.tasks) != 1 || app.tasks["resident"] == nil || len(app.activeTaskClaims) != 0 {
		t.Fatal("last returning worker did not restore the idle cache bound")
	}
}
