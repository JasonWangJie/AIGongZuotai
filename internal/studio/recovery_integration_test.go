package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The SQL fixture creates interrupted durable records, not API throughput.
// Production constraints and the 020 projection trigger stay enabled throughout.
func recoverySeedRows(t *testing.T, conn *pgx.Conn, runs, tasks, backlog int, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO studio_projects(id,workspace_id,name)
SELECT 'prj_recovery_'||lpad(i::text,2,'0'),'ws_demo','recovery fixture' FROM generate_series(0,31) i`, nil},
		{`INSERT INTO studio_runs(id,workspace_id,project_id,document)
SELECT 'run_recovery_'||lpad(i::text,6,'0'),'ws_demo','prj_recovery_'||lpad((i%32)::text,2,'0'),
jsonb_build_object('id','run_recovery_'||lpad(i::text,6,'0'),'workspace_id','ws_demo','project_id','prj_recovery_'||lpad((i%32)::text,2,'0'),
'model_id','studio-chat','provider_mode',$2::text,'status','RUNNING','seq',41,'prompt','private recovery input','output','durable checkpoint',
'created_at',clock_timestamp(),'updated_at',clock_timestamp()) FROM generate_series(1,$1::int) i`, []any{runs, mode}},
		{`INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll,lease_owner,lease_epoch,lease_until)
SELECT 'task_recovery_'||lpad(i::text,6,'0'),'ws_demo','prj_recovery_'||lpad((i%32)::text,2,'0'),
jsonb_build_object('id','task_recovery_'||lpad(i::text,6,'0'),'workspace_id','ws_demo','project_id','prj_recovery_'||lpad((i%32)::text,2,'0'),
'model_id','studio-image','kind','image','provider_mode',$2::text,'status','SUBMITTING','version',7,'prompt','private recovery prompt',
'artifacts','[]'::jsonb,'created_at',clock_timestamp(),'updated_at',clock_timestamp()),
jsonb_build_object('project_id','prj_recovery_'||lpad((i%32)::text,2,'0'),'model_id','studio-image','kind','image','inputs',jsonb_build_object('prompt','private recovery prompt')),
'{}'::jsonb,clock_timestamp()+interval '1 hour','old-recovery-owner',9,clock_timestamp()+interval '1 hour'
FROM generate_series(1,$1::int) i`, []any{tasks, mode}},
	}
	for _, statement := range statements {
		if _, err = tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal("recovery fixture load failed", err)
		}
	}
	if backlog > 0 {
		if tasks == 0 || backlog >= outboxPendingLimit {
			t.Fatal("invalid recovery backlog fixture")
		}
		_, err = tx.Exec(ctx, `INSERT INTO studio_outbox_events(event_id,event_key,workspace_id,project_id,project_seq,kind,resource_type,resource_id,resource_version,status)
SELECT 'evt_recovery_old_'||i,'recovery-prior:'||i,'ws_demo','prj_recovery_'||lpad(((((i-1)%$2::int)+1)%32)::text,2,'0'),
row_number() OVER (PARTITION BY (((i-1)%$2::int)+1)%32 ORDER BY i),'task.updated','task','task_recovery_'||lpad((((i-1)%$2::int)+1)::text,6,'0'),6,'QUEUED'
FROM generate_series(1,$1::int) i`, backlog, tasks)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO studio_event_heads(workspace_id,project_id,last_seq)
SELECT workspace_id,project_id,max(project_seq) FROM studio_outbox_events WHERE event_key LIKE 'recovery-prior:%' GROUP BY workspace_id,project_id`)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE studio_outbox_limits SET pending=pending+$1 WHERE singleton`, backlog)
		}
		if err != nil {
			t.Fatal("recovery backlog fixture load failed", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal("recovery fixture commit failed", err)
	}
}

func recoveryFixtureConfig(t *testing.T, database string) (Config, *reviewRestoreUnavailableMedia, *atomic.Int64) {
	t.Helper()
	chatCalls := &atomic.Int64{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chatCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(gateway.Close)
	media := &reviewRestoreUnavailableMedia{}
	return Config{DatabaseURL: database, Mode: "live", BaseURL: gateway.URL + "/v1", APIKey: "recovery-local-fixture-key", ChatModel: "recovery-fixture", mediaProvider: media,
		AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), PollInterval: time.Hour}, media, chatCalls
}

func recoveryAssertRows(t *testing.T, conn *pgx.Conn, runs, tasks int) {
	t.Helper()
	if got := outboxCount(t, conn, `SELECT count(*) FROM studio_runs WHERE id LIKE 'run_recovery_%' AND document->>'status'='INTERRUPTED' AND (document->>'seq')::bigint=$1 AND document->>'output'='durable checkpoint'`, uint64(41)+recoverySequenceGap); got != runs {
		t.Fatalf("run recovery count=%d want=%d; recovery must preserve checkpoint and advance once", got, runs)
	}
	if got := outboxCount(t, conn, `SELECT count(*) FROM studio_tasks WHERE id LIKE 'task_recovery_%' AND document->>'status'='SUBMIT_UNKNOWN' AND (document->>'version')::bigint=8 AND handle='{}'::jsonb AND lease_owner='' AND lease_until IS NULL AND lease_epoch>9`); got != tasks {
		t.Fatalf("unknown submission recovery count=%d want=%d", got, tasks)
	}
	if outboxCount(t, conn, `SELECT pending FROM studio_outbox_limits WHERE singleton`) != 0 || outboxCount(t, conn, `SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL`) != 0 {
		t.Fatal("New returned before recovery outbox was actually projected")
	}
	if outboxCount(t, conn, `SELECT count(*) FROM studio_event_heads h LEFT JOIN studio_event_consumers c ON c.consumer='project-feed-v1' AND c.workspace_id=h.workspace_id AND c.project_id=h.project_id WHERE c.last_seq IS DISTINCT FROM h.last_seq`) != 0 {
		t.Fatal("recovery consumer skipped a committed project event")
	}
}

func recoveryBusinessSnapshot(t *testing.T, conn *pgx.Conn) map[string]string {
	t.Helper()
	return schedulerOriginalSnapshot(t, conn, []string{"studio_runs", "studio_tasks", "studio_task_schedule", "studio_media_channel_runtime", "studio_idempotency", "studio_ledger_wallets", "studio_ledger_reservations", "studio_ledger_journals", "studio_ledger_postings", "studio_event_heads", "studio_event_consumers", "studio_outbox_events", "studio_project_event_feed"})
}

func TestPostgresRecoveryNewAcrossPagesPreservesBillingAndUnknown(t *testing.T) {
	database := testDatabase(t)
	cfg, media, chatCalls := recoveryFixtureConfig(t, database)
	seed, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ledgerGrant(t, seed.store, 10000)
	run := ledgerRun(t, seed.store, "live")
	run.Status = "RUNNING"
	if err = seed.store.saveRunCandidate(&run); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	in := taskInput("image")
	price := LedgerPriceSnapshot{ProviderMode: "live", Basis: "delivered_artifact", ArtifactMicros: 500, RequestedUnits: 1}
	quote := ledgerQuote(t, seed.store, "task", "studio-image", ledgerTaskRequestHash(in), 500, price)
	now := time.Now().UTC()
	task := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "live", Kind: "image", ModelID: "studio-image", ModelVersion: "model_v1", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, BillingQuoteID: quote.ID, BillingRequestHash: quote.RequestHash, Artifacts: []Artifact{}}, Input: in, NextPoll: now}
	const key = "ws_demo:tasks:recovery-billed-original"
	if err = seed.store.createTask(task, key, payloadHash(in)); err == nil {
		task.Task.Status = "SUBMITTING"
		task.Task.Version++
		err = seed.store.saveTask(task)
	}
	seed.Close()
	if err != nil {
		t.Fatal(err)
	}
	admin := deliveryAdmin(t, database)
	recoverySeedRows(t, admin, 257, 257, 0, "live")
	beforeJournal := schedulerOriginalSnapshot(t, admin, []string{"studio_ledger_journals", "studio_ledger_postings"})
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal("complete New failed to restore several pages", err)
	}
	defer reopened.Close()
	recoveryAssertRows(t, admin, 257, 257)
	loaded, err := reopened.store.loadTask(task.Task.ID)
	if err != nil || loaded.Task.Status != "SUBMIT_UNKNOWN" || loaded.Task.Version != task.Task.Version+1 || loaded.Task.Billing == nil || loaded.Task.Billing.State != "REVIEW" || loaded.Task.Billing.ChargedMicros != 0 || loaded.Task.Billing.ReleasedMicros != 0 {
		t.Fatal("recovery changed unknown media hold", err)
	}
	storedRun, err := reopened.store.loadRun(run.ID)
	if err != nil || storedRun.Status != "INTERRUPTED" || storedRun.Seq != run.Seq+recoverySequenceGap || storedRun.Billing == nil || storedRun.Billing.State != "REVIEW" {
		t.Fatal("recovery changed interrupted run or hold", err)
	}
	if !reflect.DeepEqual(beforeJournal, schedulerOriginalSnapshot(t, admin, []string{"studio_ledger_journals", "studio_ledger_postings"})) {
		t.Fatal("recovery invented journal or posting")
	}
	wallet := ledgerWallet(t, reopened.store)
	if wallet.ReservedMicros != 3500 || wallet.ReviewMicros != 3500 || wallet.AvailableMicros != 6500 {
		t.Fatal("recovery released or charged unknown execution")
	}
	ledgerAssertBalanced(t, reopened.store)
	if idem, found, e := reopened.store.idempotency(key); e != nil || !found || idem.ID != task.Task.ID {
		t.Fatal("recovery changed original idempotency", e)
	}
	if media.calls.Load() != 0 || chatCalls.Load() != 0 {
		t.Fatal("New automatically resubmitted interrupted execution")
	}
	stats := reopened.recoveryStats
	if stats.RunPages < 3 || stats.TaskPages < 3 || stats.MaxPageRecords > 128 || stats.MaxPageEncodedBytes > 16<<20 || stats.LeasesCleared != 257 {
		t.Fatalf("unexpected complete New recovery bounds: %+v", stats)
	}
	beforeRestart := recoveryBusinessSnapshot(t, admin)
	reopened.Close()
	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	recoveryAssertRows(t, admin, 257, 257)
	if !reflect.DeepEqual(beforeRestart, recoveryBusinessSnapshot(t, admin)) || media.calls.Load() != 0 || chatCalls.Load() != 0 {
		t.Fatal("second New repeated recovered version, event, billing or external work")
	}
	secondTask, err := second.store.loadTask(task.Task.ID)
	if err != nil || !reflect.DeepEqual(loaded.Task, secondTask.Task) {
		t.Fatal("second New repeated unknown media mutation", err)
	}
	t.Logf("complete New: runs=%d tasks=%d project_pages=%d run_pages=%d task_pages=%d max_page_records=%d max_encoded_bytes=%d leases_cleared=%d; second New changed no business evidence", 258, 258, stats.ProjectPages, stats.RunPages, stats.TaskPages, stats.MaxPageRecords, stats.MaxPageEncodedBytes, stats.LeasesCleared)
}

// Keep test measurement JSON limited to aggregate counts and timing. This
// helper intentionally does not format records or connection configuration.
func recoveryAggregateJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(fmt.Errorf("recovery aggregate serialization: %w", err))
	}
	return append(raw, '\n')
}
