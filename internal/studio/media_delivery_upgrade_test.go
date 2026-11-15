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

// Build the actual older schema from the same immutable embedded SQL bytes.
// This never deletes version registrations from a newer database.
func TestPostgresMediaDeliveryUpgradeSeventeenPreservesLegacyEvidence(t *testing.T) {
	database := testDatabase(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	old := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		prefix, _, ok := strings.Cut(strings.TrimPrefix(name, "migrations/"), "_")
		version, e := strconv.Atoi(prefix)
		if !ok || e != nil {
			t.Fatal("invalid embedded migration name")
		}
		if version > 17 {
			continue
		}
		data, e := fs.ReadFile(migrationFiles, name)
		if e != nil {
			t.Fatal(e)
		}
		old[name] = &fstest.MapFile{Data: data}
	}
	if len(old) != 17 {
		t.Fatal("upgrade fixture did not select exact versions 001..017")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var maximum, versions int
	if err = conn.QueryRow(ctx, "SELECT max(version),count(*) FROM studio_schema_versions").Scan(&maximum, &versions); err != nil || maximum != 17 || versions != 17 {
		t.Fatal("fixture was not actually schema 017", err)
	}
	if _, err = conn.Exec(ctx, "INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','old workspace'); INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','old project')"); err != nil {
		t.Fatal(err)
	}

	assetDir, referenceDir := t.TempDir(), t.TempDir()
	storage, err := NewAssetStorage(AssetStorageConfig{}, assetDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	raw, _ := mediaBytesFixture(t, "review.png")
	info, err := importMediaModerationFixture(ctx, storage, "017-quarantined-original", "image", raw)
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: info.ID, URL: "/api/v1/assets/" + info.ID, Kind: "image", Title: "original quarantined candidate", StorageInfo: &info}
	text, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
	if err != nil {
		t.Fatal(err)
	}
	policy := mediaPolicyFixture(t).Snapshot()
	now := time.Now().UTC()
	records := make([]*taskRecord, 0, 3)
	keys := []string{"upgrade017-handle", "upgrade017-isolated", "upgrade017-unknown"}
	for i, status := range []string{"RUNNING", "FAILED", "SUBMIT_UNKNOWN"} {
		input := taskInput("image")
		input.Inputs.Prompt = "original schema 017 task " + strconv.Itoa(i)
		record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ModelID: input.ModelID, ModelVersion: "original-017-model-version", ProviderMode: "demo", Prompt: input.Inputs.Prompt, Status: status, Version: 7, CreatedAt: now, UpdatedAt: now, Parameters: &input.Parameters, Artifacts: []Artifact{}}, Input: input, NextPoll: now.Add(time.Hour)}
		if i != 2 {
			record.Handle = MediaHandle{ID: "old-original-handle-" + strconv.Itoa(i), Kind: "image", Count: 1}
		}
		record.Submission = &MediaSubmission{Body: []byte(`{"model":"original-upstream","prompt":"original private request","count":1}`), IdempotencyKey: "original-upstream-key-" + strconv.Itoa(i), CredentialID: "original-credential-version", ProviderIdentity: "original-provider-identity", BaseURL: "https://fixture.invalid", Endpoint: imageGenerationEndpoint, Kind: "image", Count: 1, PreparedAt: now}
		if i == 1 {
			record.Task.Moderation = InitialMediaModerationSummary(policy, "output")
			record.MediaModeration = &MediaModerationRecord{Policy: policy, TextPolicy: text.Snapshot(), Summary: record.Task.Moderation, Candidates: []Artifact{}}
		}
		tx, e := conn.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		doc, _ := json.Marshal(record.Task)
		inputJSON, _ := json.Marshal(record.Input)
		handle, _ := json.Marshal(record.Handle)
		submission, _ := json.Marshal(record.Submission)
		_, e = tx.Exec(ctx, `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,submission,next_poll) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, record.Task.ID, record.Task.WorkspaceID, record.Task.ProjectID, doc, inputJSON, handle, submission, record.NextPoll)
		if e == nil && i == 1 {
			e = insertTaskMediaModerationSQL(ctx, tx, record.Task, record.MediaModeration)
			if e == nil {
				record.MediaModeration.Candidates = []Artifact{artifact}
				record.Task.Moderation.State = "REVIEW"
				record.Task.Moderation.Version = 2
				record.Task.Moderation.ReasonCode = "policy_review"
				record.MediaModeration.Summary = record.Task.Moderation
				e = saveTaskMediaModerationSQL(ctx, tx, record.Task, record.MediaModeration)
			}
			if e == nil {
				doc, _ = json.Marshal(record.Task)
				_, e = tx.Exec(ctx, "UPDATE studio_tasks SET document=$2 WHERE id=$1", record.Task.ID, doc)
			}
		}
		hash := payloadHash(input)
		if e == nil {
			_, e = tx.Exec(ctx, "INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type) VALUES($1,$2,$3,'task')", "ws_demo:tasks:"+keys[i], hash[:], record.Task.ID)
		}
		if e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		records = append(records, record)
	}
	_ = storage.Close()
	// JSONB snapshots compare PostgreSQL's own canonical bytes, including the
	// private immutable submission and moderation evidence, before and after.
	snapshot := func() [][]byte {
		t.Helper()
		var out [][]byte
		for _, r := range records {
			for _, column := range []string{"document", "input", "handle", "submission"} {
				var b []byte
				if e := conn.QueryRow(ctx, "SELECT "+column+" FROM studio_tasks WHERE id=$1", r.Task.ID).Scan(&b); e != nil {
					t.Fatal(e)
				}
				out = append(out, b)
			}
		}
		for _, query := range []string{"SELECT to_jsonb(m) FROM studio_task_media_moderation m WHERE task_id=$1", "SELECT candidates FROM studio_task_media_candidates WHERE task_id=$1", "SELECT jsonb_agg(to_jsonb(d) ORDER BY version) FROM studio_media_moderation_decisions d WHERE task_id=$1", "SELECT jsonb_agg(to_jsonb(i) ORDER BY scope_key) FROM studio_idempotency i"} {
			var b []byte
			var e error
			if strings.Contains(query, "$1") {
				e = conn.QueryRow(ctx, query, records[1].Task.ID).Scan(&b)
			} else {
				e = conn.QueryRow(ctx, query).Scan(&b)
			}
			if e != nil {
				t.Fatal(e)
			}
			out = append(out, b)
		}
		return out
	}
	before := snapshot()
	// The current startup loader performs the real additive migration.
	upgraded, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.close()
	var actualVersions []int
	if err = conn.QueryRow(ctx, "SELECT array_agg(version ORDER BY version) FROM studio_schema_versions").Scan(&actualVersions); err != nil {
		t.Fatal(err)
	}
	if len(actualVersions) != len(names) {
		t.Fatal("current loader omitted embedded migration versions")
	}
	for i, version := range actualVersions {
		if version != i+1 {
			t.Fatal("current loader did not apply the complete contiguous migration set")
		}
	}
	if len(actualVersions) < 18 {
		t.Fatal("current loader did not reach media delivery format")
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("018 migration rewrote legacy task/request/idempotency/review bytes")
	}
	for _, table := range []string{"studio_media_deliveries", "studio_media_delivery_items"} {
		var count int
		if err = conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("018 retroactively invented delivery evidence", err)
		}
	}
	body, err := os.ReadFile(filepath.Join(assetDir, info.ID+".bin"))
	if err != nil || !bytes.Equal(body, raw) {
		t.Fatal("upgrade changed quarantined original bytes", err)
	}
	// A writer carrying exactly the 017 embedded set rejects 018. No registry
	// deletion or manual downgrade is used to manufacture that assertion.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = applyMigrationsFrom(ctx, tx, old)
	_ = tx.Rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("old 017 writer accepted schema 018", err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("rejected old writer modified legacy evidence")
	}

	provider := &leaseMedia{query: func(_ context.Context, h MediaHandle) (MediaResult, error) {
		if h != records[0].Handle {
			t.Error("legacy execution changed original handle")
		}
		return MediaResult{Status: "RUNNING", Progress: 50}, nil
	}}
	a, err := New(Config{DatabaseURL: database, AssetDir: assetDir, ReferenceDir: referenceDir, PollInterval: time.Hour, mediaProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	code, _, _ := authRequest(t, authClient(t), server.URL, "GET", artifact.URL, "", "", nil)
	if code != 404 {
		t.Fatal("upgrade opened original quarantined candidate download", code)
	}
	for i, r := range records {
		loaded, e := a.store.loadTask(r.Task.ID)
		if e != nil || loaded.Delivery != nil || loaded.Task.Status != r.Task.Status || !reflect.DeepEqual(loaded.Submission, r.Submission) {
			t.Fatal("startup altered legacy identity/state or assigned delivery", e)
		}
		got, replay, e := a.createTask(r.Input, keys[i])
		if e != nil || !replay || got.ID != r.Task.ID {
			t.Fatal("legacy idempotency no longer replays", e)
		}
	}
	advanceMediaScheduleDue(t, a, records[0].Task.ID)
	if !a.executePersistentTaskStep(records[0].Task.ID) {
		t.Fatal("legacy original handle was not queried")
	}
	if a.executePersistentTaskStep(records[1].Task.ID) || a.executePersistentTaskStep(records[2].Task.ID) {
		t.Fatal("isolated or submit-unknown legacy task resumed")
	}
	if provider.submits.Load() != 0 || provider.queries.Load() != 1 {
		t.Fatal("upgrade repeated POST or queried isolated candidates")
	}
	legacy, e := a.store.loadTask(records[0].Task.ID)
	if e != nil || legacy.Delivery != nil || legacy.Handle != records[0].Handle || !reflect.DeepEqual(legacy.Submission, records[0].Submission) {
		t.Fatal("legacy query changed frozen request", e)
	}
	isolated, e := a.store.loadTask(records[1].Task.ID)
	if e != nil || isolated.Task.Moderation.State != "REVIEW" || isolated.MediaModeration == nil || len(isolated.MediaModeration.Candidates) != 1 || !sameMediaDeliveryReceipt(isolated.MediaModeration.Candidates[0].StorageInfo, &info) || len(isolated.Task.Artifacts) != 0 {
		t.Fatal("legacy B2 quarantine changed", e)
	}
	if len(isolated.MediaModeration.Candidates) > 0 {
		if isolated.Delivery != nil || a.canReconcileTask(isolated) {
			t.Fatal("legacy candidate became a delivery/requery path")
		}
	}
}
