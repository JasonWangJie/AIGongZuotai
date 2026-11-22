package studio

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Seed real 020 rows in committed chunks. Every schedule trigger and constraint
// stays enabled; the seed cost is recorded separately from complete App startup.
func seedStartupCapacity(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	_, err := conn.Exec(ctx, `INSERT INTO studio_workspaces(id,name)
SELECT 'ws_cap_'||n,'capacity fixture' FROM generate_series(0,255)n;
INSERT INTO studio_projects(id,workspace_id,name)
SELECT 'prj_'||id,id,'capacity fixture' FROM studio_workspaces WHERE id LIKE 'ws_cap_%'`)
	if err != nil {
		t.Fatal(err)
	}
	const runSQL = `INSERT INTO studio_runs(id,workspace_id,project_id,document)
SELECT id,ws,'prj_'||ws,jsonb_build_object('id',id,'workspace_id',ws,'project_id','prj_'||ws,'provider_mode','live',
'model_id','studio-chat','status',CASE WHEN n<=1024 THEN 'RUNNING' ELSE 'COMPLETED' END,'seq',41,
'output','saved checkpoint','created_at',now(),'updated_at',now())
FROM (SELECT n,'run_cap_'||lpad(n::text,6,'0') AS id,'ws_cap_'||(n%256) AS ws FROM generate_series($1::int,$2::int)n) seed`
	const taskSQL = `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
SELECT id,ws,'prj_'||ws,jsonb_build_object('id',id,'workspace_id',ws,'project_id','prj_'||ws,'provider_mode','live',
'model_id','studio-image','kind','image','status',CASE WHEN n<=128 THEN 'SUBMITTING' WHEN n<=98976 THEN 'RUNNING' ELSE 'FAILED' END,
'version',7,'created_at',now(),'updated_at',now()),
jsonb_build_object('kind','image','project_id','prj_'||ws,'model_id','studio-image','inputs',jsonb_build_object('prompt','fixture')),
CASE WHEN n<=128 THEN '{}'::jsonb ELSE jsonb_build_object('ID','original-'||id,'Kind','image','Count',1) END,
now()+interval '1 day'
FROM (SELECT n,'task_cap_'||lpad(n::text,6,'0') AS id,'ws_cap_'||(n%256) AS ws FROM generate_series($1::int,$2::int)n) seed`
	for _, group := range []struct {
		query string
		count int
	}{{runSQL, 51024}, {taskSQL, 148976}} {
		for first := 1; first <= group.count; first += 1000 {
			if _, err := conn.Exec(ctx, group.query, first, min(first+999, group.count)); err != nil {
				t.Fatal("capacity seed failed", err)
			}
		}
	}
	if outboxCount(t, conn, "SELECT count(*) FROM studio_runs WHERE document->>'status'='RUNNING'") != 1024 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_task_schedule WHERE active") != 98976 ||
		outboxCount(t, conn, "SELECT sum(active_count) FROM studio_workspace_schedule") != 98976 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_runs WHERE document->>'status'='COMPLETED'") != 50000 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_tasks WHERE document->>'status'='FAILED'") != 50000 {
		t.Fatal("database did not contain the expected active and terminal populations")
	}
}

// Cache payload is the JSON encoding of resident business records after startup.
// It excludes Go map/object overhead and is distinct from SQL page transfer size.
func startupCachePayload(t *testing.T, a *App) int {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	bytes := 0
	count := func(value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		bytes += len(raw)
	}
	for _, r := range a.runs {
		count(r.Run)
	}
	for _, record := range a.tasks {
		count(record)
	}
	return bytes
}

func TestPostgresBoundedCacheCompleteStartup(t *testing.T) {
	if os.Getenv("RECOVERY_CAPACITY_TEST") != "1" {
		t.Skip("set RECOVERY_CAPACITY_TEST=1 for complete New with 100000 active and 100000 terminal records")
	}
	database := testDatabase(t)
	cfg, media, chat := recoveryFixtureConfig(t, database)
	seed, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()
	conn := deliveryAdmin(t, database)
	loadStart := time.Now()
	seedStartupCapacity(t, conn)
	loadTime := time.Since(loadStart)
	started := time.Now()
	a, err := New(cfg)
	startup := time.Since(started)
	if err != nil {
		t.Fatal("complete startup failed", err)
	}
	defer a.Close()
	s := a.recoveryStats
	if s.RunRows != 1024 || s.TaskRows != 98976 || s.RunPages != 8 || s.TaskPages != 774 ||
		s.CachedRunsPeak != a.config.MaxRecords || s.CachedTasksPeak != a.config.MaxRecords || s.CachedProjectsPeak != 0 ||
		s.MaxPageRecords != 128 || s.MaxPageEncodedBytes > recoveryMediaPageBytes {
		t.Fatalf("unexpected full startup bounds: %+v", s)
	}
	if outboxCount(t, conn, "SELECT count(*) FROM studio_runs WHERE document->>'status'='INTERRUPTED' AND (document->>'seq')::bigint=$1", uint64(41)+recoverySequenceGap) != 1024 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_tasks WHERE document->>'status'='SUBMIT_UNKNOWN' AND document->>'version'='8'") != 128 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_tasks WHERE document->>'status'='RUNNING' AND handle->>'ID'='original-'||id AND document->>'version'='7'") != 98848 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_project_event_feed") != 1152 ||
		outboxCount(t, conn, "SELECT pending FROM studio_outbox_limits WHERE singleton") != 0 ||
		media.calls.Load() != 0 || chat.Load() != 0 {
		t.Fatal("recovery changed the wrong business population or produced external traffic")
	}
	payload := startupCachePayload(t, a)
	// Exercise a cold active record well beyond the seeded cache prefix.
	a.mu.Lock()
	row, err := a.getTaskLocked("task_cap_098976")
	a.mu.Unlock()
	if err != nil || row.Handle.ID != "original-task_cap_098976" || row.Task.Version != 7 {
		t.Fatal("cold active task did not retain its original handle", err)
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	report := map[string]any{
		"go_version": runtime.Version(), "active_before": 100000, "terminal_before": 100000,
		"active_runs_before": 1024, "active_tasks_before": 98976, "terminal_runs_before": 50000, "terminal_tasks_before": 50000,
		"fixture_load_ms": ms(loadTime), "complete_new_ms": ms(startup),
		"run_pages": s.RunPages, "task_pages": s.TaskPages, "max_page_records": s.MaxPageRecords,
		"max_encoded_page_bytes": s.MaxPageEncodedBytes, "total_encoded_page_bytes": s.ReadBytes,
		"page_read_mean_ms": ms(s.PageReadTotal) / float64(s.RunPages+s.TaskPages), "page_read_max_ms": ms(s.MaxPageRead),
		"startup_cache_run_records_peak": s.CachedRunsPeak, "startup_cache_task_records_peak": s.CachedTasksPeak,
		"startup_cache_project_records_peak": s.CachedProjectsPeak, "startup_resident_payload_json_bytes": payload,
		"interrupted_runs": 1024, "unknown_submissions": 128, "projected_events": 1152, "external_calls": 0,
		"scope": "single local PostgreSQL writer, intact schedule triggers, current migrations, original handles due in the future; page reads exclude recovery writes; cache payload excludes heap overhead",
	}
	raw := recoveryAggregateJSON(t, report)
	if path := os.Getenv("RECOVERY_CAPACITY_REPORT"); path != "" {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(raw))
}
