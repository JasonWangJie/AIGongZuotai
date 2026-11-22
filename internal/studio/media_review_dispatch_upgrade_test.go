package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func reviewDispatchSchema018(t *testing.T, conn *pgx.Conn) fstest.MapFS {
	t.Helper()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		prefix, _, ok := strings.Cut(strings.TrimPrefix(name, "migrations/"), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			t.Fatal("invalid embedded SQL filename")
		}
		if version > 18 {
			continue
		}
		body, err := fs.ReadFile(migrationFiles, name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: body}
	}
	if len(old) != 18 {
		t.Fatal("upgrade fixture does not carry original 001..018 bytes")
	}
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigrationsFrom(context.Background(), tx, old); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return old
}

func TestPostgresMediaReviewDispatchUpgrade018NeverBackfillsCandidates(t *testing.T) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "")
	cfg := reviewDispatchConfig(t, media, review, true, 1)
	ctx := context.Background()
	conn := deliveryAdmin(t, cfg.DatabaseURL)
	old := reviewDispatchSchema018(t, conn)
	var count, maximum int
	if err := conn.QueryRow(ctx, "SELECT count(*),max(version) FROM studio_schema_versions").Scan(&count, &maximum); err != nil || count != 18 || maximum != 18 {
		t.Fatal("fixture is not genuine 018", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','018 private workspace'); INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','018 private project')"); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAssetStorage(AssetStorageConfig{}, cfg.AssetDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	body, _ := mediaBytesFixture(t, "allow.png")
	info, err := importMediaModerationFixture(ctx, storage, "original-018-candidate", "image", body)
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: info.ID, Kind: "image", URL: "/api/v1/assets/" + info.ID, Title: "private pre-019 candidate", StorageInfo: &info}
	text, err := newModerationEngine(cfg.Moderation, "demo")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := newMediaModerationEngine(cfg.MediaModeration, "demo")
	if err != nil {
		t.Fatal(err)
	}
	policy := engine.Snapshot()
	records := make([]*taskRecord, 0, 2)
	keys := []string{"original-018-pending-key", "original-018-review-key"}
	for i, status := range []string{"MODERATING", "FAILED"} {
		input := taskInput("image")
		input.ProjectID = "prj_demo"
		input.Inputs.Prompt = "original immutable private request " + strconv.Itoa(i)
		now := time.Now().UTC()
		r := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ModelID: input.ModelID, ModelVersion: "original-018-model", ProviderMode: "demo", Prompt: input.Inputs.Prompt, Status: status, Version: 7, CreatedAt: now, UpdatedAt: now, Parameters: &input.Parameters, Artifacts: []Artifact{}}, Input: input, Handle: MediaHandle{ID: "original-018-handle-" + strconv.Itoa(i), Kind: "image", Count: 1}, NextPoll: now.Add(time.Hour)}
		r.Submission = &MediaSubmission{Body: []byte(`{"model":"original-018-upstream","prompt":"private immutable body"}`), IdempotencyKey: "original-upstream-key", CredentialID: "original-credential", ProviderIdentity: "original-provider", BaseURL: "https://fixture.invalid", Endpoint: imageGenerationEndpoint, Kind: "image", Count: 1, PreparedAt: now}
		r.Task.Moderation = InitialMediaModerationSummary(policy, "output")
		r.MediaModeration = &MediaModerationRecord{Policy: policy, TextPolicy: text.Snapshot(), Summary: r.Task.Moderation, Candidates: []Artifact{}}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		document, _ := json.Marshal(r.Task)
		request, _ := json.Marshal(r.Input)
		handle, _ := json.Marshal(r.Handle)
		submission, _ := json.Marshal(r.Submission)
		_, err = tx.Exec(ctx, `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,submission,next_poll) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, r.Task.ID, r.Task.WorkspaceID, r.Task.ProjectID, document, request, handle, submission, r.NextPoll)
		if err == nil {
			err = insertTaskMediaModerationSQL(ctx, tx, r.Task, r.MediaModeration)
		}
		r.MediaModeration.Candidates = []Artifact{artifact}
		if i == 1 {
			r.Task.Moderation.State, r.Task.Moderation.ReasonCode, r.Task.Moderation.Version = "REVIEW", "policy_review", 2
			r.MediaModeration.Summary = r.Task.Moderation
		}
		if err == nil {
			err = saveTaskMediaModerationSQL(ctx, tx, r.Task, r.MediaModeration)
		}
		document, _ = json.Marshal(r.Task)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE studio_tasks SET document=$2 WHERE id=$1", r.Task.ID, document)
		}
		hash := payloadHash(input)
		if err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type) VALUES($1,$2,$3,'task')", "ws_demo:tasks:"+keys[i], hash[:], r.Task.ID)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	_ = storage.Close()
	snapshot := func() [][]byte {
		t.Helper()
		result := [][]byte{}
		for _, query := range []string{
			"SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM studio_tasks t",
			"SELECT jsonb_agg(to_jsonb(m) ORDER BY task_id) FROM studio_task_media_moderation m",
			"SELECT jsonb_agg(to_jsonb(c) ORDER BY task_id) FROM studio_task_media_candidates c",
			"SELECT jsonb_agg(to_jsonb(d) ORDER BY task_id,version) FROM studio_media_moderation_decisions d",
			"SELECT jsonb_agg(to_jsonb(i) ORDER BY scope_key) FROM studio_idempotency i",
		} {
			var raw []byte
			if err := conn.QueryRow(ctx, query).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			result = append(result, raw)
		}
		return result
	}
	before := snapshot()
	upgraded, err := openPostgres(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.close()
	if err = conn.QueryRow(ctx, "SELECT count(*),max(version) FROM studio_schema_versions").Scan(&count, &maximum); err != nil || count != maintenanceSchemaVersion || maximum != maintenanceSchemaVersion {
		t.Fatal("loader did not apply all current migrations", err)
	}
	if !reflect.DeepEqual(before, snapshot()) || outboxCount(t, conn, "SELECT count(*) FROM studio_media_review_dispatch") != 0 {
		t.Fatal("019 migration changed old evidence or invented READY")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = applyMigrationsFrom(ctx, tx, old)
	_ = tx.Rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "newer than this executable") || !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("original 018 writer was not fenced without mutation", err)
	}
	actual, err := os.ReadFile(filepath.Join(cfg.AssetDir, info.ID+".bin"))
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatal("upgrade changed private bytes", err)
	}

	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	for i, original := range records {
		loaded, err := a.store.loadTask(original.Task.ID)
		if err != nil || loaded.ReviewDispatch != nil || loaded.MediaModeration == nil || loaded.Task.Moderation.State != "REVIEW" || len(loaded.Task.Artifacts) != 0 || len(loaded.MediaModeration.Candidates) != 1 || !reflect.DeepEqual(loaded.Submission, original.Submission) || loaded.Handle != original.Handle {
			t.Fatal("legacy candidate regained execution or changed frozen identity", err)
		}
		if a.executePersistentTaskLane(mediaReviewLane, original.Task.ID) || a.canReconcileTask(loaded) {
			t.Fatal("legacy candidate acquired a new first review or query")
		}
		replayed, replay, err := a.createTask(original.Input, keys[i])
		if err != nil || !replay || replayed.ID != original.Task.ID {
			t.Fatal("original 018 idempotency changed", err)
		}
	}
	if review.count("output") != 0 || outboxCount(t, conn, "SELECT count(*) FROM studio_media_review_dispatch") != 0 {
		t.Fatal("compatible engine automatically reviewed old candidates")
	}
	client, identity := deliveryIdentity(t, server, true)
	fresh, _, _ := laneAccept(t, a, server, client, identity, "new post-019 accepted task")
	prepareReviewDispatch(t, a, fresh.ID)
	reviewDispatchState(t, conn, fresh.ID, "READY")
	reviewDispatchStep(t, a, fresh.ID)
	if deliveryRead(t, a, fresh.ID).Moderation.State != "ALLOWED" || review.count("output") != 1 {
		t.Fatal("new accepted first review did not execute once")
	}
}
