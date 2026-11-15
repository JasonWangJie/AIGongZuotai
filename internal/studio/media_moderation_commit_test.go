package studio

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Isolate the control writer from unrelated background commits so the TCP
// proxy drops exactly the candidate/final transaction acknowledgement. The
// production persistTaskClaim still runs the real PostgreSQL lease fence.
func TestPostgresMediaModerationUnknownCommitRecoversOnlyCommittedDecision(t *testing.T) {
	for _, stage := range []string{"candidate", "ALLOWED", "BLOCKED"} {
		t.Run(stage, func(t *testing.T) {
			database := testDatabase(t)
			proxy, armed, dropped := commitAckDropProxy(t, database)
			s, err := openPostgres(proxy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.close)
			// The proxy accepts exactly the control writer. Real independent
			// lease sessions connect directly, as only the business COMMIT is
			// under fault injection (claims/renewals must not consume the fault).
			cfg := Config{DatabaseURL: database, Mode: "demo", TaskWorkers: 1, PollInterval: time.Hour, AssetDir: filepath.Join(t.TempDir(), "assets"), ReferenceDir: filepath.Join(t.TempDir(), "references")}
			cfg.defaults()
			a := &App{store: s, config: cfg, ctx: context.Background(), tasks: map[string]*taskRecord{}, workspaceSubscribers: map[chan event]subscriptionScope{}}
			if err = a.initTaskLeases(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.taskLeases.close)
			storage, err := NewAssetStorage(AssetStorageConfig{}, cfg.AssetDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			raw, _ := mediaBytesFixture(t, "allow.png")
			info, err := importMediaModerationFixture(context.Background(), storage, newID("fixture"), "image", raw)
			if err != nil {
				t.Fatal(err)
			}
			artifact := Artifact{ID: info.ID, URL: "/api/v1/assets/" + info.ID, Kind: "image", Title: "private fixture", StorageInfo: &info}
			text, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
			if err != nil {
				t.Fatal(err)
			}
			policy := mediaPolicyFixture(t).Snapshot()
			input, key := taskInput("image"), newID("key")
			now := time.Now().UTC()
			record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ModelID: input.ModelID, ModelVersion: "frozen-fixture", ProviderMode: "demo", Prompt: input.Inputs.Prompt, Status: "RUNNING", Version: 1, CreatedAt: now, UpdatedAt: now, Parameters: &input.Parameters, Artifacts: []Artifact{}, Moderation: InitialMediaModerationSummary(policy, "output")}, Input: input, Handle: MediaHandle{ID: "original-accepted-handle"}, NextPoll: now.Add(-time.Second)}
			record.MediaModeration = &MediaModerationRecord{Policy: policy, TextPolicy: text.Snapshot(), Summary: record.Task.Moderation, Candidates: []Artifact{}}
			if err = s.createTask(record, "ws_demo:tasks:"+key, payloadHash(input)); err != nil {
				t.Fatal(err)
			}
			conn, err := a.taskLeases.acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			claimed := claimTask(t, a, conn, record.Task.ID)
			a.taskLeases.release(conn)
			a.tasks[record.Task.ID] = cloneLeasedRecord(claimed)
			candidate := cloneLeasedRecord(claimed)
			candidate.Task.Status, candidate.Task.Progress = "MODERATING", 99
			candidate.Task.Version++
			candidate.MediaModeration.Candidates = []Artifact{artifact}
			if stage != "candidate" {
				if err = a.persistTaskClaim(candidate, false); err != nil {
					t.Fatal(err)
				}
				candidate = cloneLeasedRecord(candidate)
				candidate.Task.Moderation.State, candidate.Task.Moderation.Version = stage, 2
				candidate.Task.Status = "FAILED"
				candidate.Task.Moderation.ReasonCode = "policy_block"
				if stage == "ALLOWED" {
					candidate.Task.Status, candidate.Task.Progress = "SUCCEEDED", 100
					candidate.Task.Moderation.ReasonCode = "allowed"
					candidate.Task.Artifacts = []Artifact{artifact}
				}
				candidate.MediaModeration.Summary = candidate.Task.Moderation
				candidate.Task.Version++
			}
			before := copyTask(a.tasks[record.Task.ID].Task)
			events := make(chan event, 8)
			a.workspaceSubscribers[events] = subscriptionScope{WorkspaceID: "ws_demo", ProjectID: "prj_demo"}
			armed.Store(true)
			err = a.persistTaskClaim(candidate, stage != "candidate")
			if err == nil || !s.failed.Load() {
				t.Fatal("lost acknowledgement did not fail closed")
			}
			select {
			case <-dropped:
			case <-time.After(time.Second):
				t.Fatal("target commit acknowledgement was not dropped")
			}
			cached := a.tasks[record.Task.ID].Task
			if cached.Version != before.Version || cached.Status != before.Status || cached.Moderation.State != "PENDING" || len(cached.Artifacts) != 0 || len(events) != 0 {
				t.Fatal("unconfirmed candidate/decision escaped into cache or SSE")
			}
			a.taskLeases.close()
			s.close()
			_ = storage.Close()
			// Current review settings are disabled; recovery must use the original
			// committed decision, never current policy or another provider request.
			cfg.DatabaseURL = database
			restarted, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			provider := &countedModeratedMedia{MediaProvider: restarted.media}
			restarted.media = provider
			server := httptest.NewServer(restarted.Handler())
			defer server.Close()
			code, body, _ := authRequest(t, authClient(t), server.URL, "GET", "/api/v1/tasks/"+record.Task.ID, "", "", nil)
			if code != 200 {
				t.Fatal("recovered task unavailable", code)
			}
			recovered := decode[Task](t, body)
			want := stage
			if stage == "candidate" {
				want = "REVIEW"
			}
			if recovered.Moderation == nil || recovered.Moderation.State != want || recovered.Moderation.Version != 2 {
				t.Fatal("committed review/recovery state differs", recovered)
			}
			if stage == "ALLOWED" {
				if recovered.Status != "SUCCEEDED" || len(recovered.Artifacts) != 1 {
					t.Fatal("committed allow was lost")
				}
			} else if recovered.Status != "FAILED" || len(recovered.Artifacts) != 0 || recovered.CanReconcile {
				t.Fatal("quarantine was released on restart")
			}
			code, _, _ = authRequest(t, authClient(t), server.URL, "GET", artifact.URL, "", "", nil)
			if stage == "ALLOWED" && code != 200 || stage != "ALLOWED" && code != 404 {
				t.Fatal("restart download gate differs", code)
			}
			replayed, replay, err := restarted.createTask(input, key)
			if err != nil || !replay || replayed.ID != record.Task.ID || provider.submits.Load() != 0 || provider.queries.Load() != 0 {
				t.Fatal("original key regenerated or queried after recovery", err)
			}
		})
	}
}
