package studio

import (
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// This measures complete New on an already-current database. Fixture loading
// and a separate warm migration-only openPostgres measurement are excluded.
// It deliberately retains the existing cache policy; this is not A4.2.
func TestPostgresRecoveryNewLargeBacklog(t *testing.T) {
	if os.Getenv("RECOVERY_LARGE_TEST") != "1" {
		t.Skip("set RECOVERY_LARGE_TEST=1 for complete New with 12002 interrupted records and 9999 pending events")
	}
	const runs, tasks, backlog = 6001, 6001, 9999
	database := testDatabase(t)
	cfg, media, chatCalls := recoveryFixtureConfig(t, database)
	seed, err := New(cfg)
	if err != nil {
		t.Fatal("initial isolated schema and directory setup failed", err)
	}
	seed.Close()
	admin := deliveryAdmin(t, database)
	loadStarted := time.Now()
	recoverySeedRows(t, admin, runs, tasks, backlog, "live")
	loadElapsed := time.Since(loadStarted)
	if outboxCount(t, admin, "SELECT pending FROM studio_outbox_limits WHERE singleton") != backlog || outboxCount(t, admin, "SELECT count(*) FROM studio_project_event_feed") != 0 {
		t.Fatal("fixture did not begin with real unconsumed outbox")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_task_schedule WHERE active") != tasks || outboxCount(t, admin, "SELECT sum(active_count) FROM studio_workspace_schedule") != tasks {
		t.Fatal("fixture bypassed enabled schedule projection")
	}
	migrationStarted := time.Now()
	migrationOnly, err := openPostgres(database)
	if err != nil {
		t.Fatal("current-format migration-only baseline failed", err)
	}
	migrationOnly.close()
	migrationElapsed := time.Since(migrationStarted)
	started := time.Now()
	a, err := New(cfg)
	newElapsed := time.Since(started)
	if err != nil {
		t.Fatalf("single complete New did not recover %d records plus %d pending events after %s: %v", runs+tasks, backlog, newElapsed, err)
	}
	defer a.Close()
	recoveryAssertRows(t, admin, runs, tasks)
	const expectedEvents = runs + tasks + backlog
	if outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events") != expectedEvents || outboxCount(t, admin, "SELECT count(*) FROM studio_project_event_feed") != expectedEvents {
		t.Fatal("startup lost or duplicated a committed event; 32 scopes keep every event inside retention")
	}
	if outboxCount(t, admin, `SELECT count(*) FROM studio_outbox_events WHERE event_key LIKE 'recovery-prior:%' AND projected_at IS NOT NULL`) != backlog || outboxCount(t, admin, "SELECT retained FROM studio_outbox_limits WHERE singleton") != expectedEvents {
		t.Fatal("original backlog was discarded instead of consumed")
	}
	if outboxCount(t, admin, `SELECT count(*) FROM studio_outbox_events WHERE resource_type='task' AND resource_version=8 AND status='SUBMIT_UNKNOWN'`) != tasks || outboxCount(t, admin, `SELECT count(*) FROM studio_outbox_events WHERE resource_type='run' AND resource_version=$1 AND status='INTERRUPTED'`, uint64(41)+recoverySequenceGap) != runs {
		t.Fatal("recovery events do not match exactly one committed business version")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries") != 0 || media.calls.Load() != 0 || chatCalls.Load() != 0 {
		t.Fatal("startup invented money, public assets or external generation")
	}
	stats := a.recoveryStats
	if stats.RunPages < (runs+127)/128 || stats.TaskPages < (tasks+127)/128 || stats.MaxPageRecords < 1 || stats.MaxPageRecords > 128 || stats.MaxPageEncodedBytes < 1 || stats.MaxPageEncodedBytes > 16<<20 || stats.LeasesCleared != tasks || stats.CachedRunsPeak < runs || stats.CachedTasksPeak < tasks {
		t.Fatalf("complete New page or aggregate cache evidence invalid: %+v", stats)
	}
	first := recoveryBusinessSnapshot(t, admin)
	a.Close()
	restartStarted := time.Now()
	second, err := New(cfg)
	restartElapsed := time.Since(restartStarted)
	if err != nil {
		t.Fatal("second complete New failed", err)
	}
	defer second.Close()
	recoveryAssertRows(t, admin, runs, tasks)
	if !reflect.DeepEqual(first, recoveryBusinessSnapshot(t, admin)) || media.calls.Load() != 0 || chatCalls.Load() != 0 || second.recoveryStats.LeasesCleared != 0 {
		t.Fatal("second New repeated recovery, released original evidence or performed external I/O")
	}
	report := struct {
		SchemaVersion                int     `json:"schema_version"`
		GoVersion                    string  `json:"go_version"`
		Runs                         int     `json:"runs"`
		Tasks                        int     `json:"tasks"`
		InitialPending               int     `json:"initial_pending"`
		ProjectedEvents              int     `json:"projected_events"`
		FixtureLoadMS                float64 `json:"fixture_load_ms"`
		CurrentFormatMigrationOnlyMS float64 `json:"current_format_migration_only_ms"`
		CompleteNewMS                float64 `json:"complete_new_ms"`
		SecondNewMS                  float64 `json:"second_new_ms"`
		ProjectPages                 int     `json:"project_pages"`
		RunPages                     int     `json:"run_pages"`
		TaskPages                    int     `json:"task_pages"`
		MaxPageRecords               int     `json:"max_page_records"`
		MaxPageEncodedBytes          int64   `json:"max_page_encoded_bytes"`
		CachedProjectsPeak           int     `json:"cached_projects_peak"`
		CachedRunsPeak               int     `json:"cached_runs_peak"`
		CachedTasksPeak              int     `json:"cached_tasks_peak"`
		LeasesCleared                int64   `json:"leases_cleared"`
		SecondNewBusinessUnchanged   bool    `json:"second_new_business_unchanged"`
		ExternalCalls                int64   `json:"external_calls"`
		Scope                        string  `json:"scope"`
	}{1, runtime.Version(), runs, tasks, backlog, expectedEvents, float64(loadElapsed) / float64(time.Millisecond), float64(migrationElapsed) / float64(time.Millisecond), float64(newElapsed) / float64(time.Millisecond), float64(restartElapsed) / float64(time.Millisecond), stats.ProjectPages, stats.RunPages, stats.TaskPages, stats.MaxPageRecords, stats.MaxPageEncodedBytes, stats.CachedProjectsPeak, stats.CachedRunsPeak, stats.CachedTasksPeak, stats.LeasesCleared, true, int64(media.calls.Load()) + chatCalls.Load(), "single local complete New; unchanged cache policy; no production capacity claim"}
	if path := os.Getenv("RECOVERY_LARGE_REPORT"); path != "" {
		if err = os.WriteFile(path, recoveryAggregateJSON(t, report), 0600); err != nil {
			t.Fatal("could not write aggregate recovery report", err)
		}
	}
	t.Logf("complete New=%s warm migration-only=%s second New=%s; interrupted=%d initial backlog=%d projected=%d pages=%d/%d/%d max loaded page=%d records/%d encoded bytes; cached run/task peaks=%d/%d (not bounded memory)", newElapsed, migrationElapsed, restartElapsed, runs+tasks, backlog, expectedEvents, stats.ProjectPages, stats.RunPages, stats.TaskPages, stats.MaxPageRecords, stats.MaxPageEncodedBytes, stats.CachedRunsPeak, stats.CachedTasksPeak)
}
