package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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

// Build the old schema from its original embedded files, never by erasing a
// migration registration from a database which already has newer objects.
func mediaSchedulerSchema019(t *testing.T, conn *pgx.Conn) fstest.MapFS {
	t.Helper()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		prefix, _, ok := strings.Cut(strings.TrimPrefix(name, "migrations/"), "_")
		version, parseErr := strconv.Atoi(prefix)
		if !ok || parseErr != nil {
			t.Fatal("invalid migration name")
		}
		if version > 19 {
			continue
		}
		body, readErr := fs.ReadFile(migrationFiles, name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		old[name] = &fstest.MapFile{Data: body}
	}
	if len(old) != 19 {
		t.Fatal("fixture does not contain original 001..019")
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

func schedulerOriginalTables(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename<>'studio_schema_versions' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	return names
}

func schedulerOriginalSnapshot(t *testing.T, conn *pgx.Conn, tables []string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range tables {
		var raw string
		query := `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + pgx.Identifier{name}.Sanitize() + ` t`
		if err := conn.QueryRow(context.Background(), query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		result[name] = raw
	}
	return result
}

func TestPostgresMediaSchedulerUpgrade019PreservesEvidenceAndMaintenance(t *testing.T) {
	database := testDatabase(t)
	ctx := context.Background()
	conn := deliveryAdmin(t, database)
	old := mediaSchedulerSchema019(t, conn)
	if _, err := conn.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','scheduler upgrade'); INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','scheduler upgrade')`); err != nil {
		t.Fatal(err)
	}
	referenceDir, assetDir := t.TempDir(), t.TempDir()
	storage, err := NewAssetStorage(AssetStorageConfig{}, assetDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	body, _ := mediaBytesFixture(t, "allow.png")
	info, err := importMediaModerationFixture(ctx, storage, "scheduler-upgrade-private", "image", body)
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{ID: info.ID, Kind: "image", URL: "/api/v1/assets/" + info.ID, Title: "private legacy candidate", StorageInfo: &info}
	textEngine, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
	if err != nil {
		t.Fatal(err)
	}
	mediaEngine, err := newMediaModerationEngine(MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	policy := mediaEngine.Snapshot()
	ids := map[string]string{}
	for _, phase := range []string{"queued", "submitting", "query", "legacy", "READY", "STARTED", "FINISHED"} {
		now := time.Now().UTC().Truncate(time.Microsecond)
		input := taskInput("image")
		input.ProjectID = "prj_demo"
		status := "MODERATING"
		switch phase {
		case "queued":
			status = "QUEUED"
		case "submitting":
			status = "SUBMITTING"
		case "query":
			status = "RUNNING"
		case "FINISHED":
			status = "FAILED"
		}
		task := Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ModelID: input.ModelID, ModelVersion: "legacy-019", ProviderMode: "demo", Prompt: "private original " + phase, Status: status, Version: 7, CreatedAt: now, UpdatedAt: now, Parameters: &input.Parameters, Artifacts: []Artifact{}}
		ids[phase] = task.ID
		handle := MediaHandle{}
		if phase != "queued" && phase != "submitting" {
			handle = MediaHandle{ID: "frozen-019-handle-" + phase, ProviderIdentity: strings.Repeat("a", 64), Kind: "image", Count: 1}
		}
		submission := &MediaSubmission{Body: []byte(`{"model":"frozen-019","prompt":"private original body"}`), IdempotencyKey: "original-019-key-" + phase, CredentialID: "original-019-credential", ProviderIdentity: strings.Repeat("a", 64), BaseURL: "https://fixture.invalid", Endpoint: imageGenerationEndpoint, Kind: "image", Count: 1, PreparedAt: now}
		hasCandidate := phase == "legacy" || phase == "READY" || phase == "STARTED" || phase == "FINISHED"
		var state *MediaModerationRecord
		if hasCandidate {
			task.Moderation = InitialMediaModerationSummary(policy, "output")
			state = &MediaModerationRecord{Policy: policy, TextPolicy: textEngine.Snapshot(), Summary: task.Moderation, Candidates: []Artifact{}}
		}
		document, _ := json.Marshal(task)
		request, _ := json.Marshal(input)
		handleRaw, _ := json.Marshal(handle)
		submissionRaw, _ := json.Marshal(submission)
		tx, beginErr := conn.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		_, err = tx.Exec(ctx, `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,submission,next_poll) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, task.ID, task.WorkspaceID, task.ProjectID, document, request, handleRaw, submissionRaw, now.Add(time.Hour))
		if err == nil && hasCandidate {
			err = insertTaskMediaModerationSQL(ctx, tx, task, state)
		}
		if err == nil && hasCandidate {
			state.Candidates = []Artifact{artifact}
			if phase == "FINISHED" {
				task.Moderation.State = "REVIEW"
				task.Moderation.ReasonCode = "policy_review"
				task.Moderation.Version = 2
				state.Summary = task.Moderation
			}
			err = saveTaskMediaModerationSQL(ctx, tx, task, state)
		}
		if err == nil && (phase == "READY" || phase == "STARTED" || phase == "FINISHED") {
			digest, digestErr := mediaReviewCandidateDigest(state.Candidates, "image")
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			version := 1
			attempt := ""
			var started, finished *time.Time
			if phase != "READY" {
				version = 2
				attempt = newID("review")
				started = &now
			}
			if phase == "FINISHED" {
				version = 3
				finished = &now
			}
			_, err = tx.Exec(ctx, `INSERT INTO studio_media_review_dispatch(task_id,workspace_id,project_id,candidate_digest,policy_digest,result_status,state,version,attempt_id,started_at,finished_at) VALUES($1,$2,$3,$4,$5,'SUCCEEDED',$6,$7,$8,$9,$10)`, task.ID, task.WorkspaceID, task.ProjectID, digest, policy.Digest, phase, version, attempt, started, finished)
		}
		if err == nil {
			document, _ = json.Marshal(task)
			_, err = tx.Exec(ctx, `UPDATE studio_tasks SET document=$2 WHERE id=$1`, task.ID, document)
		}
		if err == nil {
			hash := payloadHash(input)
			_, err = tx.Exec(ctx, `INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type) VALUES($1,$2,$3,'task')`, "ws_demo:tasks:original-019-"+phase, hash[:], task.ID)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_ = storage.Close()
	tables := schedulerOriginalTables(t, conn)
	before := schedulerOriginalSnapshot(t, conn, tables)
	if _, _, err = maintenanceReachable(ctx, conn); err == nil {
		t.Fatal("020 maintenance accepted old019")
	}
	store, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("020 migration changed original019 business/private/ledger/idempotency evidence")
	}
	var count, maximum int
	if err = conn.QueryRow(ctx, `SELECT count(*),max(version) FROM studio_schema_versions`).Scan(&count, &maximum); err != nil || count != 20 || maximum != 20 {
		t.Fatal("upgrade not exact020", err)
	}
	for phase, id := range ids {
		var lane string
		var active bool
		if err = conn.QueryRow(ctx, `SELECT lane,active FROM studio_task_schedule WHERE task_id=$1`, id).Scan(&lane, &active); err != nil {
			t.Fatal(err)
		}
		want := "query"
		if phase == "queued" || phase == "submitting" {
			want = "submit"
		}
		if phase == "READY" || phase == "STARTED" || phase == "FINISHED" {
			want = "review"
		}
		if lane != want || active != (phase != "FINISHED") {
			t.Fatalf("019 %s projection lane=%s active=%t", phase, lane, active)
		}
	}
	if outboxCount(t, conn, `SELECT count(*) FROM studio_media_review_dispatch`) != 3 {
		t.Fatal("020 invented first-review evidence for old candidate")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = applyMigrationsFrom(ctx, tx, old)
	_ = tx.Rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "newer than this executable") || !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("019 writer bypassed020 fence", err)
	}
	lease, err := AcquireStorageLease(referenceDir, assetDir)
	if err != nil {
		t.Fatal(err)
	}
	err = BindStorageOwnership(ctx, conn, referenceDir, assetDir)
	_ = lease.Close()
	if err != nil {
		t.Fatal(err)
	}
	maintenance := StorageMaintenanceConfig{DatabaseURL: database, ReferenceDir: referenceDir, AssetDir: assetDir}
	plan, err := MaintainPrivateStorage(ctx, maintenance)
	if err != nil || plan.SchemaVersion != 20 || plan.ProtectedFiles != 3 || len(plan.Candidates) != 0 {
		t.Fatal("020 upgrade lost candidate reachability", err, plan)
	}
	// Corrupt only the derived scheduling view in this isolated fixture. No
	// projection state, including a lost queue entry, authorizes file deletion.
	if _, err = conn.Exec(ctx, `UPDATE studio_task_schedule SET active=false,eligible_at=clock_timestamp()+interval '30 days'; UPDATE studio_workspace_schedule SET active_count=0,task_cursor='tail'; UPDATE studio_lane_schedule SET workspace_cursor='tail'`); err != nil {
		t.Fatal(err)
	}
	after, err := MaintainPrivateStorage(ctx, maintenance)
	if err != nil || after.PlanDigest != plan.PlanDigest || after.ProtectedFiles != 3 || len(after.Candidates) != 0 {
		t.Fatal("derived queue state changed GC authorization", err, after)
	}
	actual, err := os.ReadFile(filepath.Join(assetDir, info.ID+".bin"))
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatal("upgrade/maintenance changed private bytes", err)
	}
}

func TestPostgresMediaSchedulerLargeUpgrade019(t *testing.T) {
	if os.Getenv("MEDIA_SCHEDULER_LARGE_PLAN") != "1" {
		t.Skip("set MEDIA_SCHEDULER_LARGE_PLAN=1 for real 100k active + 100k historical 019 upgrade")
	}
	ctx := context.Background()
	database := testDatabase(t)
	conn := deliveryAdmin(t, database)
	mediaSchedulerSchema019(t, conn)
	for _, sql := range []string{
		// A real initialized 019 deployment already contains these defaults.
		// Preserve them in the before snapshot so current startup's idempotent
		// initialization is not mistaken for a migration rewriting evidence.
		`INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','本地开发工作区'); INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','未命名创意项目')`,
		`INSERT INTO studio_workspaces(id,name) SELECT 'ws_upgrade_'||lpad(n::text,3,'0'),'old019 bulk' FROM generate_series(0,256)n`,
		`INSERT INTO studio_projects(id,workspace_id,name) SELECT 'prj_'||id,id,'old019 bulk' FROM studio_workspaces WHERE id LIKE 'ws_upgrade_%'`,
		`INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
 SELECT 'task_upgrade_'||lpad(n::text,10,'0'),'ws_upgrade_'||lpad((n%257)::text,3,'0'),'prj_ws_upgrade_'||lpad((n%257)::text,3,'0'),
 jsonb_build_object('id','task_upgrade_'||lpad(n::text,10,'0'),'workspace_id','ws_upgrade_'||lpad((n%257)::text,3,'0'),'project_id','prj_ws_upgrade_'||lpad((n%257)::text,3,'0'),'provider_mode','demo','kind','image','status',CASE WHEN n<=100000 THEN 'RUNNING' ELSE 'SUCCEEDED' END,'version',7,'progress',0),
 jsonb_build_object('project_id','prj_ws_upgrade_'||lpad((n%257)::text,3,'0'),'kind','image','model_id','legacy-upstream','inputs',jsonb_build_object('prompt','private unchanged019 input')),
 jsonb_build_object('ID','frozen-019-handle-'||n,'ProviderIdentity',repeat('c',64),'CredentialID','frozen-019-credential','Kind','image','Count',1),clock_timestamp()+interval '1 day'
 FROM generate_series(1,200000)n`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	tables := schedulerOriginalTables(t, conn)
	hashes := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, table := range tables {
			var count int64
			var digest string
			// Each row is reduced before sorting, keeping the evidence query
			// independent of the size of a task's original frozen JSON body.
			query := `SELECT count(*),md5(COALESCE(string_agg(h,'' ORDER BY h),'')) FROM (SELECT md5(to_jsonb(t)::text) h FROM ` + pgx.Identifier{table}.Sanitize() + ` t) q`
			if err := conn.QueryRow(ctx, query).Scan(&count, &digest); err != nil {
				t.Fatal(err)
			}
			result[table] = fmt.Sprintf("%d:%s", count, digest)
		}
		return result
	}
	before := hashes()
	started := time.Now()
	store, err := openPostgres(database)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("production019→020 migration failed after %s with unchanged startup deadline: %v", elapsed, err)
	}
	store.close()
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("large019 migration changed original table evidence")
	}
	if outboxCount(t, conn, `SELECT count(*) FROM studio_task_schedule WHERE active`) != 100000 || outboxCount(t, conn, `SELECT count(*) FROM studio_task_schedule WHERE NOT active`) != 100000 {
		t.Fatal("large019 backfill lost active or historical projection")
	}
	if outboxCount(t, conn, `SELECT count(*) FROM studio_task_schedule s FULL JOIN studio_tasks t ON t.id=s.task_id WHERE t.id IS NULL OR s.task_id IS NULL OR s.workspace_id<>t.workspace_id OR s.eligible_at<>t.next_poll OR s.channel_key<>repeat('c',64) OR s.lane<>'query'`) != 0 {
		t.Fatal("large019 backfill changed frozen identities or due times")
	}
	if outboxCount(t, conn, `SELECT count(*) FROM studio_workspace_schedule w LEFT JOIN (SELECT lane,provider_mode,workspace_id,count(*) n FROM studio_task_schedule WHERE active GROUP BY lane,provider_mode,workspace_id) c USING(lane,provider_mode,workspace_id) WHERE w.active_count<>COALESCE(c.n,0)`) != 0 {
		t.Fatal("large019 backfill workspace count mismatch")
	}
	t.Logf("original019 active=100000 history=100000 workspaces=257 production_migration_elapsed=%s unchanged_tables=%d; this is migration cost, not full App capacity", elapsed, len(tables))
}
