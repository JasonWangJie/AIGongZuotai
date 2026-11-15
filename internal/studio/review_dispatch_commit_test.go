package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Only the business writer goes through the ACK-dropping TCP proxy. Lease
// connections remain real direct PG sessions and cannot consume the fault.
func TestPostgresMediaReviewDispatchUnknownCommitBoundaries(t *testing.T) {
	for _, stage := range []string{"READY", "STARTED", "ALLOWED"} {
		t.Run(stage, func(t *testing.T) {
			database := testDatabase(t)
			proxy, armed, dropped := commitAckDropProxy(t, database)
			s, err := openPostgres(proxy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.close)
			cfg := Config{DatabaseURL: database, AuthMode: "required", Mode: "demo", PollInterval: time.Hour, AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), Moderation: executionModerationConfig(t, nil), MediaModeration: MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}}
			cfg.defaults()
			storage, err := NewAssetStorage(AssetStorageConfig{}, cfg.AssetDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			raw, _ := mediaBytesFixture(t, "allow.png")
			info, err := importMediaModerationFixture(context.Background(), storage, newID("review_fixture"), "image", raw)
			if err != nil {
				t.Fatal(err)
			}
			artifact := Artifact{ID: info.ID, URL: "/api/v1/assets/" + info.ID, Kind: "image", Title: "original batch", StorageInfo: &info}
			text, err := newModerationEngine(cfg.Moderation, "demo")
			if err != nil {
				t.Fatal(err)
			}
			engine := mediaPolicyFixture(t)
			policy := engine.Snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := &App{store: s, assets: storage, config: cfg, ctx: ctx, cancel: cancel, mediaModeration: engine, tasks: map[string]*taskRecord{}, workspaceSubscribers: map[chan event]subscriptionScope{}}
			if err = a.initTaskLeases(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.taskLeases.close)
			input, key, now := taskInput("image"), newID("key"), time.Now().UTC()
			record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ModelID: input.ModelID, ModelVersion: "frozen-review-fixture", ProviderMode: "demo", Prompt: input.Inputs.Prompt, Status: "RUNNING", Version: 1, CreatedAt: now, UpdatedAt: now, Parameters: &input.Parameters, Artifacts: []Artifact{}, Moderation: InitialMediaModerationSummary(policy, "output")}, Input: input, Handle: MediaHandle{ID: "original-accepted-handle", Kind: "image", Count: 1}, NextPoll: now}
			record.MediaModeration = &MediaModerationRecord{Policy: policy, TextPolicy: text.Snapshot(), Summary: record.Task.Moderation, Candidates: []Artifact{}}
			if err = s.createTask(record, "ws_demo:tasks:"+key, payloadHash(input)); err != nil {
				t.Fatal(err)
			}
			claim := func() *taskRecord {
				t.Helper()
				conn, e := a.taskLeases.acquire(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				r := claimTask(t, a, conn, record.Task.ID)
				a.taskLeases.release(conn)
				return r
			}
			candidate := claim()
			a.tasks[record.Task.ID] = cloneLeasedRecord(candidate)
			candidate.MediaModeration.Candidates = []Artifact{artifact}
			candidate.Task.Status = "MODERATING"
			candidate.Task.Version++
			candidate.ReviewDispatch, err = newMediaReviewDispatch(candidate, "SUCCEEDED")
			if err != nil {
				t.Fatal(err)
			}
			if stage != "READY" {
				if err = a.persistTaskClaim(candidate, true); err != nil {
					t.Fatal(err)
				}
				candidate = claim()
				now := time.Now().UTC().Truncate(time.Microsecond)
				candidate.ReviewDispatch.State = "STARTED"
				candidate.ReviewDispatch.AttemptID = newID("review")
				candidate.ReviewDispatch.StartedAt = &now
				candidate.Task.Version++
				if stage == "ALLOWED" {
					if err = a.persistTaskClaim(candidate, false); err != nil {
						t.Fatal(err)
					}
					candidate.Task.Moderation.State = "ALLOWED"
					candidate.Task.Moderation.ReasonCode = "allowed"
					candidate.Task.Moderation.Version = 2
					candidate.MediaModeration.Summary = candidate.Task.Moderation
					candidate.Task.Status = "SUCCEEDED"
					candidate.Task.Artifacts = []Artifact{artifact}
					candidate.Task.Version++
				}
			}
			before := cloneLeasedRecord(a.tasks[record.Task.ID])
			events := make(chan event, 8)
			a.workspaceSubscribers[events] = subscriptionScope{WorkspaceID: "ws_demo", ProjectID: "prj_demo"}
			armed.Store(true)
			err = a.persistTaskClaim(candidate, stage != "STARTED")
			if err == nil || !s.failed.Load() {
				t.Fatal("lost COMMIT acknowledgement did not fail closed")
			}
			select {
			case <-dropped:
			case <-time.After(time.Second):
				t.Fatal("requested COMMIT ACK was not dropped")
			}
			cached := a.tasks[record.Task.ID]
			if cached.Task.Version != before.Task.Version || cached.Task.Status != before.Task.Status || len(cached.Task.Artifacts) != 0 || len(events) != 0 || (cached.ReviewDispatch == nil) != (before.ReviewDispatch == nil) {
				t.Fatal("unconfirmed review decision escaped cache/notifications")
			}
			if cached.ReviewDispatch != nil && !sameMediaReviewRecord(cached.ReviewDispatch, before.ReviewDispatch) {
				t.Fatal("unconfirmed private review marker escaped cache")
			}
			a.taskLeases.close()
			s.close()
			_ = storage.Close()
			restarted, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			provider := &countedModeratedMedia{MediaProvider: restarted.media}
			restarted.media = provider
			recovered, err := restarted.store.loadTask(record.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "READY":
				if recovered.ReviewDispatch.State != "READY" || recovered.Task.Moderation.State != "PENDING" {
					t.Fatal("committed not-started review was quarantined")
				}
				// No upstream provider is needed to review already private bytes.
				restarted.media = nil
				if !restarted.executePersistentTaskStep(record.Task.ID) {
					t.Fatal("first review could not run independently")
				}
				recovered, err = restarted.store.loadTask(record.Task.ID)
				if err != nil || recovered.Task.Moderation.State != "ALLOWED" || recovered.ReviewDispatch.State != "FINISHED" {
					t.Fatal("recovered READY failed its first review", err)
				}
			case "STARTED":
				if recovered.ReviewDispatch.State != "FINISHED" || recovered.Task.Moderation.State != "REVIEW" || recovered.Task.Status != "FAILED" || len(recovered.Task.Artifacts) != 0 {
					t.Fatal("committed uncertain STARTED repeated or released review")
				}
				if restarted.executePersistentTaskStep(record.Task.ID) {
					t.Fatal("recovered STARTED was executed again")
				}
			case "ALLOWED":
				if recovered.ReviewDispatch.State != "FINISHED" || recovered.Task.Moderation.State != "ALLOWED" || recovered.Task.Status != "SUCCEEDED" || len(recovered.Task.Artifacts) != 1 {
					t.Fatal("committed final decision was lost")
				}
			}
			restarted.mu.Lock()
			idem, exists, e := restarted.findIdempotencyLocked("ws_demo:tasks:" + key)
			restarted.mu.Unlock()
			if e != nil || !exists || idem.ID != record.Task.ID || provider.submits.Load() != 0 || provider.queries.Load() != 0 {
				t.Fatal("review recovery lost original acceptance or contacted generation provider", e)
			}
		})
	}
}

func TestPostgresMediaReviewDispatchStartedRollbackAndLegacyCandidateGuard(t *testing.T) {
	a, s, client, identity, database := newMediaExecutionFixture(t, false)
	task, _ := acceptMediaExecution(t, a, s, client, identity, "image", "review rollback fixture", newID("key"), 1)
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("submit missing")
	}
	time.Sleep(5 * time.Millisecond)
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("READY handoff missing")
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	claim := claimTask(t, a, admin, task.ID)
	before := cloneLeasedRecord(claim)
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim.ReviewDispatch.State = "STARTED"
	claim.ReviewDispatch.AttemptID = newID("review")
	claim.ReviewDispatch.StartedAt = &now
	claim.Task.Version++
	deadline := time.Now().Add(5 * time.Second)
	for outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("pending admission events did not drain before barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err = admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=10000 WHERE singleton")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.persistTaskClaim(claim, false); !isEventBacklog(err) {
		t.Fatal("STARTED did not rollback with full Outbox", err)
	}
	durable, err := loadMediaReviewDispatchSQL(context.Background(), admin, task.ID)
	if err != nil || durable.State != "READY" || durable.Version != 1 || durable.AttemptID != "" || durable.StartedAt != nil {
		t.Fatal("rolled back STARTED became a consumed first attempt", err)
	}
	if claim.ReviewDispatch.Version != 1 || readLeasedTask(t, admin, task.ID).Version != before.Task.Version {
		t.Fatal("rollback advanced record versions")
	}
	_, err = admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton")
	if err != nil {
		t.Fatal(err)
	}
	expireTaskLease(t, admin, task.ID)
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("rolled back STARTED could not retry its first attempt")
	}
	final := readLeasedTask(t, admin, task.ID)
	if final.Moderation.State != "ALLOWED" {
		t.Fatal("first review after rollback failed")
	}
	// A pre-019 candidate must not acquire new READY evidence even if a caller
	// constructs an otherwise-valid header. The entire attempted task write rolls back.
	oldTask, _ := acceptMediaExecution(t, a, s, client, identity, "image", "legacy candidate fixture", newID("key"), 1)
	a.mu.Lock()
	old := cloneLeasedRecord(a.tasks[oldTask.ID])
	a.mu.Unlock()
	raw, _ := mediaBytesFixture(t, "allow.png")
	info, e := importMediaModerationFixture(context.Background(), a.assets, newID("legacy_candidate"), "image", raw)
	if e != nil {
		t.Fatal(e)
	}
	old.MediaModeration.Candidates = []Artifact{{ID: info.ID, Kind: "image", URL: "/api/v1/assets/" + info.ID, StorageInfo: &info}}
	old.Handle = MediaHandle{ID: "legacy-original-handle", Kind: "image", Count: 1}
	old.Task.Status = "MODERATING"
	old.Task.Version++
	if err = a.store.saveTask(old); err != nil {
		t.Fatal(err)
	}
	old.ReviewDispatch, err = newMediaReviewDispatch(old, "SUCCEEDED")
	if err != nil {
		t.Fatal(err)
	}
	old.Task.Version++
	if err = a.store.saveTask(old); err == nil {
		t.Fatal("legacy candidate was backfilled as a fresh first review")
	}
	if a.store.failed.Load() {
		t.Fatal("domain rejection poisoned database health")
	}
	var count int
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_media_review_dispatch WHERE task_id=$1", oldTask.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected legacy dispatch escaped rollback", err)
	}
}

func TestPostgresMediaReviewDispatchFinalBackpressureNeverRechecks(t *testing.T) {
	a, s, client, identity, database := newMediaExecutionFixture(t, true)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var checks atomic.Int32
	reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, _ := readMediaMultipartFixture(t, r)
		checks.Add(1)
		entered <- struct{}{}
		<-release
		writeMediaResponse(w, meta, "allow")
	}))
	defer reviewer.Close()
	a.mediaModeration = httpMediaEngine(t, reviewer.URL)
	task, _ := acceptMediaExecution(t, a, s, client, identity, "image", "decision save backpressure", newID("key"), 1)
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("submit missing")
	}
	time.Sleep(5 * time.Millisecond)
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("READY handoff missing")
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	// A sequence is deliberately only a test probe: nextval survives the
	// transaction rollback, so this observes a real final write attempt. It is
	// never used as a business event/replay cursor.
	_, err = admin.Exec(context.Background(), `CREATE SEQUENCE review_final_write_probe;
CREATE FUNCTION review_final_write_probe_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.document->'moderation'->>'state'='ALLOWED' THEN PERFORM nextval('review_final_write_probe'); END IF; RETURN NEW; END $$;
CREATE TRIGGER review_final_write_probe_tr BEFORE UPDATE ON studio_tasks FOR EACH ROW EXECUTE FUNCTION review_final_write_probe_fn();`)
	if err != nil {
		t.Fatal(err)
	}
	advanceMediaScheduleDue(t, a, task.ID)
	done := make(chan struct{})
	go func() { defer close(done); a.executePersistentTaskStep(task.ID) }()
	defer func() {
		unblock()
		a.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("review test worker did not drain")
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review HTTP was not entered")
	}
	deadline := time.Now().Add(5 * time.Second)
	for outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("STARTED event did not project")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=10000 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	unblock()
	for {
		var attempted bool
		if err = admin.QueryRow(context.Background(), "SELECT is_called FROM review_final_write_probe").Scan(&attempted); err != nil {
			t.Fatal(err)
		}
		if attempted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("final save was not attempted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Wait for that actual transaction to finish while the injected full
	// counter remains intact. The cached STARTED proves it rolled back.
	a.mu.Lock()
	cached := cloneLeasedRecord(a.tasks[task.ID])
	a.mu.Unlock()
	if cached.Task.Moderation.State != "PENDING" || cached.ReviewDispatch.State != "STARTED" || len(cached.Task.Artifacts) != 0 || cached.Task.Billing.ChargedMicros != 0 {
		t.Fatal("backpressured decision escaped transaction")
	}
	if a.mediaExecution.snapshot().ReviewInFlight != 1 || len(a.mediaModeration.slots) != 1 || checks.Load() != 1 || a.store.failed.Load() {
		t.Fatal("backpressure repeated review or released executing capacity")
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("original decision did not finish after backlog cleared")
	}
	final, err := a.store.loadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Task.Status != "SUCCEEDED" || final.Task.Moderation.State != "ALLOWED" || final.ReviewDispatch.State != "FINISHED" || final.Task.Billing.ChargedMicros != 100 || checks.Load() != 1 || a.mediaExecution.snapshot().ReviewInFlight != 0 || len(a.mediaModeration.slots) != 0 {
		t.Fatal("final transaction did not release exactly once")
	}
}
