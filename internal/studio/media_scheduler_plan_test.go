package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type schedulerPlanEvidence struct {
	Queued, Terminal                             int
	Distribution, CacheMode, Selector, SQLSHA256 string
	PlanningMS, ExecutionMS                      float64
	Rows, LeafRows, Removed, LeafBuffers         float64
	PreparedCustomPlans, PreparedGenericPlans    int64
	Raw                                          json.RawMessage
}

// Count pgx query calls including BEGIN/COMMIT, never retaining query text,
// arguments, workspace identity, channel identity, or connection credentials.
// One call may use several protocol messages; this is not a TCP packet count.
type schedulerQueryCounter struct{ calls atomic.Uint64 }

func (c *schedulerQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.calls.Add(1)
	return ctx
}
func (*schedulerQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type schedulerPreselectionEvidence struct {
	Queued, Terminal, Tick         int
	Distribution                   string
	ElapsedMS                      float64
	Examined, QueryCalls           uint64
	CursorAdvanced, CandidateFound bool
}

// This is a SQL cost fixture, not an API admission benchmark. Root approved
// disabling only the new derived projection trigger inside one transaction.
// All original constraints remain enabled, and full anti-joins/counts validate
// the projection before the transaction can commit. No provider is invoked.
func schedulerBulkFixture(t *testing.T, conn *pgx.Conn, count int) {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	statements := []string{
		`INSERT INTO studio_workspaces(id,name) SELECT 'ws_bulk_'||lpad(n::text,3,'0'),'isolated SQL fixture' FROM generate_series(0,256)n`,
		`INSERT INTO studio_projects(id,workspace_id,name) SELECT 'prj_'||id,id,'isolated SQL fixture' FROM studio_workspaces WHERE id LIKE 'ws_bulk_%'`,
		`ALTER TABLE studio_tasks DISABLE TRIGGER studio_task_schedule_changed`,
		`INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
 SELECT 'task_bulk_'||lpad(n::text,10,'0'),'ws_bulk_'||lpad((n%257)::text,3,'0'),'prj_ws_bulk_'||lpad((n%257)::text,3,'0'),
 jsonb_build_object('id','task_bulk_'||lpad(n::text,10,'0'),'workspace_id','ws_bulk_'||lpad((n%257)::text,3,'0'),'project_id','prj_ws_bulk_'||lpad((n%257)::text,3,'0'),'provider_mode','demo','kind','image','status',CASE WHEN n<=$1 THEN 'RUNNING' ELSE 'SUCCEEDED' END,'version',1,'progress',0),
 '{}'::jsonb,jsonb_build_object('ID','fixture-handle-'||n,'ProviderIdentity',repeat('a',64),'Kind','image','Count',1),clock_timestamp()-interval '1 hour'
 FROM generate_series(1,$1::integer*2)n`,
		`INSERT INTO studio_workspace_schedule(lane,provider_mode,workspace_id) SELECT l,'demo',w.id FROM studio_workspaces w CROSS JOIN unnest(ARRAY['submit','query','delivery','review'])l WHERE w.id LIKE 'ws_bulk_%'`,
		`INSERT INTO studio_task_schedule(task_id,workspace_id,provider_mode,lane,channel_key,eligible_at,active)
 SELECT id,workspace_id,'demo','query',repeat('a',64),next_poll,(document->>'status')='RUNNING' FROM studio_tasks WHERE id LIKE 'task_bulk_%'`,
		`WITH counts AS (SELECT lane,provider_mode,workspace_id,count(*) n FROM studio_task_schedule WHERE active GROUP BY lane,provider_mode,workspace_id) UPDATE studio_workspace_schedule w SET active_count=c.n FROM counts c WHERE c.lane=w.lane AND c.provider_mode=w.provider_mode AND c.workspace_id=w.workspace_id`,
		`ALTER TABLE studio_tasks ENABLE TRIGGER studio_task_schedule_changed`,
	}
	for _, sql := range statements {
		var e error
		if strings.Contains(sql, "$1") {
			_, e = tx.Exec(ctx, sql, count)
		} else {
			_, e = tx.Exec(ctx, sql)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT tgenabled='O' FROM pg_trigger WHERE tgrelid='studio_tasks'::regclass AND tgname='studio_task_schedule_changed'`).Scan(&enabled); err != nil || !enabled {
		t.Fatal("fixture projection trigger not restored", err)
	}
	var invalid int
	if err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM studio_tasks t LEFT JOIN studio_task_schedule s ON s.task_id=t.id WHERE s.task_id IS NULL OR s.workspace_id<>t.workspace_id OR s.provider_mode<>t.document->>'provider_mode' OR s.active<>((t.document->>'status')='RUNNING'))+
 (SELECT count(*) FROM studio_task_schedule s LEFT JOIN studio_tasks t ON t.id=s.task_id WHERE t.id IS NULL)+
 (SELECT count(*) FROM studio_workspace_schedule w LEFT JOIN (SELECT lane,provider_mode,workspace_id,count(*) n FROM studio_task_schedule WHERE active GROUP BY lane,provider_mode,workspace_id) c USING(lane,provider_mode,workspace_id) WHERE w.active_count<>COALESCE(c.n,0))`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatal("bulk task/projection/workspace fixture mismatch", invalid, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if outboxCount(t, conn, `SELECT count(*) FROM studio_task_schedule WHERE active`) != count || outboxCount(t, conn, `SELECT count(*) FROM studio_task_schedule WHERE NOT active`) != count {
		t.Fatal("bulk fixture count differs")
	}
}

func schedulerBulkDistribution(t *testing.T, conn *pgx.Conn, count int, kind string) {
	t.Helper()
	ctx := context.Background()
	// Physical task history remains in place for every distribution. Only this
	// isolated fixture's derived projection/lease availability changes.
	threshold := count * 99 / 100
	for _, sql := range []string{`UPDATE studio_tasks SET lease_owner='',lease_until=NULL WHERE id LIKE 'task_bulk_%'`, `UPDATE studio_task_schedule SET lane='query',channel_key=repeat('a',64),eligible_at=clock_timestamp()-interval '1 hour'`, `DELETE FROM studio_media_channel_runtime`, `UPDATE studio_lane_schedule SET workspace_cursor=''`, `UPDATE studio_workspace_schedule SET task_cursor=''`} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	n := `right(task_id,10)::integer`
	switch kind {
	case "balanced_four_lanes":
		_, err := conn.Exec(ctx, `UPDATE studio_task_schedule SET lane=(ARRAY['submit','query','delivery','review'])[right(task_id,10)::integer%4+1] WHERE active`)
		if err != nil {
			t.Fatal(err)
		}
	case "future99":
		_, err := conn.Exec(ctx, `UPDATE studio_task_schedule SET eligible_at=clock_timestamp()+interval '1 day' WHERE active AND `+n+`<=$1`, threshold)
		if err != nil {
			t.Fatal(err)
		}
	case "other_lane99":
		_, err := conn.Exec(ctx, `UPDATE studio_task_schedule SET lane='submit' WHERE active AND `+n+`<=$1`, threshold)
		if err != nil {
			t.Fatal(err)
		}
	case "cooldown99":
		_, err := conn.Exec(ctx, `UPDATE studio_task_schedule SET channel_key=repeat('b',64) WHERE active AND `+n+`<=$1`, threshold)
		if err != nil {
			t.Fatal(err)
		}
	case "lease99":
		_, err := conn.Exec(ctx, `UPDATE studio_tasks SET lease_owner='isolated-plan-fixture',lease_until=clock_timestamp()+interval '1 day' WHERE right(id,10)::integer<=$1`, threshold)
		if err != nil {
			t.Fatal(err)
		}
	case "mixed99":
		_, err := conn.Exec(ctx, `UPDATE studio_task_schedule SET lane=CASE WHEN right(task_id,10)::integer%4=1 THEN 'submit' ELSE 'query' END,channel_key=CASE WHEN right(task_id,10)::integer%4=2 THEN repeat('b',64) ELSE repeat('a',64) END,eligible_at=CASE WHEN right(task_id,10)::integer%4=0 THEN clock_timestamp()+interval '1 day' ELSE clock_timestamp()-interval '1 hour' END WHERE active AND right(task_id,10)::integer<=$1`, threshold)
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Exec(ctx, `UPDATE studio_tasks SET lease_owner='isolated-plan-fixture',lease_until=clock_timestamp()+interval '1 day' WHERE right(id,10)::integer<=$1 AND right(id,10)::integer%4=3`, threshold)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown SQL distribution")
	}
	if _, err := conn.Exec(ctx, `INSERT INTO studio_media_channel_runtime(scope,lane,channel_key,cooldown_until,effective_limit) VALUES('gateway','query',repeat('b',64),clock_timestamp()+interval '1 day',1); UPDATE studio_workspace_schedule SET active_count=0; WITH counts AS (SELECT lane,provider_mode,workspace_id,count(*) n FROM studio_task_schedule WHERE active GROUP BY lane,provider_mode,workspace_id) UPDATE studio_workspace_schedule w SET active_count=c.n FROM counts c WHERE c.lane=w.lane AND c.provider_mode=w.provider_mode AND c.workspace_id=w.workspace_id`); err != nil {
		t.Fatal(err)
	}
	want := count / 100
	if kind == "balanced_four_lanes" {
		want = count / 4
	}
	var available int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM studio_task_schedule s JOIN studio_tasks t ON t.id=s.task_id WHERE s.active AND s.lane='query' AND s.eligible_at<=clock_timestamp() AND (t.lease_until IS NULL OR t.lease_until<=clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM studio_media_channel_runtime c WHERE c.scope='gateway' AND c.lane='query' AND c.channel_key=s.channel_key AND c.cooldown_until>clock_timestamp())`).Scan(&available); err != nil || available != want {
		t.Fatal("fixture eligible fraction differs", kind, available, want, err)
	}
	for _, table := range []string{"studio_tasks", "studio_task_schedule", "studio_workspace_schedule", "studio_media_channel_runtime"} {
		if _, err := conn.Exec(ctx, `ANALYZE `+pgx.Identifier{table}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
}

func schedulerPlanWalk(t *testing.T, node map[string]any, e *schedulerPlanEvidence) {
	t.Helper()
	number := func(name string) float64 { v, _ := node[name].(float64); return v }
	rows := number("Actual Rows") * number("Actual Loops")
	children, _ := node["Plans"].([]any)
	if len(children) == 0 {
		e.LeafRows += rows
		e.LeafBuffers += number("Shared Hit Blocks") + number("Shared Read Blocks")
	}
	e.Removed += (number("Rows Removed by Filter") + number("Rows Removed by Index Recheck")) * number("Actual Loops")
	relation, _ := node["Relation Name"].(string)
	kind, _ := node["Node Type"].(string)
	if relation == "studio_task_schedule" && (kind == "Seq Scan" || strings.HasPrefix(kind, "Bitmap")) {
		t.Errorf("large task selector performed %s", kind)
	}
	if strings.HasPrefix(e.Selector, "tasks_") && (kind == "Sort" || kind == "Incremental Sort") {
		t.Errorf("task keyset selector sorted its backlog")
	}
	for _, child := range children {
		schedulerPlanWalk(t, child.(map[string]any), e)
	}
}

func schedulerExplain(t *testing.T, conn *pgx.Conn, count int, distribution, mode, selector, sql string, args []any, limit int) schedulerPlanEvidence {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `SET plan_cache_mode=`+mode); err != nil {
		t.Fatal(err)
	}
	name := "media_plan_probe"
	_, _ = conn.Exec(ctx, `DEALLOCATE `+name)
	if _, err := conn.Exec(ctx, `PREPARE `+name+` AS `+sql); err != nil {
		t.Fatal(err)
	}
	// EXECUTE parameters must be SQL literals, so quote every test-only value.
	literals := make([]string, len(args))
	for i, v := range args {
		switch a := v.(type) {
		case string:
			literals[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
		case int:
			literals[i] = fmt.Sprint(a)
		default:
			t.Fatal("unexpected plan argument")
		}
	}
	execute := `EXECUTE ` + name + `(` + strings.Join(literals, ",") + `)`
	for i := 0; i < 6; i++ {
		rows, err := conn.Query(ctx, execute)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	var raw []byte
	if err := conn.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, SETTINGS, FORMAT JSON) `+execute).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded) != 1 {
		t.Fatal("invalid explain JSON", err)
	}
	sum := sha256.Sum256([]byte(sql))
	e := schedulerPlanEvidence{Queued: count, Terminal: count, Distribution: distribution, CacheMode: mode, Selector: selector, SQLSHA256: hex.EncodeToString(sum[:]), Raw: raw}
	e.PlanningMS, _ = decoded[0]["Planning Time"].(float64)
	e.ExecutionMS, _ = decoded[0]["Execution Time"].(float64)
	if err := conn.QueryRow(ctx, `SELECT custom_plans,generic_plans FROM pg_prepared_statements WHERE name=$1`, name).Scan(&e.PreparedCustomPlans, &e.PreparedGenericPlans); err != nil {
		t.Fatal(err)
	}
	node := decoded[0]["Plan"].(map[string]any)
	e.Rows, _ = node["Actual Rows"].(float64)
	schedulerPlanWalk(t, node, &e)
	if e.Rows > float64(limit) {
		t.Errorf("selector exceeded page: %.0f > %d", e.Rows, limit)
	}
	if strings.HasPrefix(selector, "tasks_") && e.LeafRows+e.Removed > float64(limit*2) {
		t.Errorf("task page scanned beyond keyset budget: %+v", e)
	}
	t.Logf("queued=%d history=%d distribution=%s mode=%s selector=%s rows=%.0f leaf_rows=%.0f removed=%.0f leaf_buffers=%.0f execution_ms=%.3f", count, count, distribution, mode, selector, e.Rows, e.LeafRows, e.Removed, e.LeafBuffers, e.ExecutionMS)
	return e
}

func TestPostgresMediaSchedulerLargePlan(t *testing.T) {
	if os.Getenv("MEDIA_SCHEDULER_LARGE_PLAN") != "1" {
		t.Skip("set MEDIA_SCHEDULER_LARGE_PLAN=1 for explicit 100k queued + 100k history SQL validation")
	}
	var evidence []schedulerPlanEvidence
	var preselections []schedulerPreselectionEvidence
	baseline := map[string]float64{}
	for _, count := range []int{10000, 100000} {
		t.Run(fmt.Sprintf("queued_%d_history_%d", count, count), func(t *testing.T) {
			database := testDatabase(t)
			store, err := openPostgres(database)
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			conn := store.conn
			traceConfig, err := pgx.ParseConfig(database)
			if err != nil {
				t.Fatal("invalid isolated plan connection configuration")
			}
			counter := &schedulerQueryCounter{}
			traceConfig.Tracer = counter
			selectionConn, err := pgx.ConnectConfig(context.Background(), traceConfig)
			if err != nil {
				t.Fatal("traced preselector connection unavailable")
			}
			defer selectionConn.Close(context.Background())
			var seqscan string
			if err = conn.QueryRow(context.Background(), `SHOW enable_seqscan`).Scan(&seqscan); err != nil || seqscan != "on" {
				t.Fatal("plan validation requires normal enabled sequential scans", err)
			}
			schedulerBulkFixture(t, conn, count)
			for _, distribution := range []string{"balanced_four_lanes", "future99", "other_lane99", "cooldown99", "lease99", "mixed99"} {
				schedulerBulkDistribution(t, conn, count, distribution)
				for _, mode := range []string{"force_custom_plan", "force_generic_plan", "auto"} {
					items := []schedulerPlanEvidence{schedulerExplain(t, conn, count, distribution, mode, "workspaces", mediaScheduleWorkspaceSelectSQL, []any{"query", "demo", "", mediaScheduleWorkspacePage}, mediaScheduleWorkspacePage)}
					for lane := mediaSubmitLane; lane <= mediaReviewLane; lane++ {
						items = append(items, schedulerExplain(t, conn, count, distribution, mode, "tasks_"+mediaLaneName(lane), mediaScheduleTaskSelectSQL[lane], []any{"demo", "ws_bulk_000", "", mediaScheduleTaskPage}, mediaScheduleTaskPage))
					}
					for _, item := range items {
						key := distribution + "/" + mode + "/" + item.Selector
						if count == 10000 {
							baseline[key] = item.LeafBuffers
						} else if item.LeafBuffers > baseline[key]*4+64 {
							t.Errorf("10x backlog expanded bounded selector buffers: %s baseline=%.0f large=%.0f", key, baseline[key], item.LeafBuffers)
						}
						evidence = append(evidence, item)
					}
				}
				cfg := Config{Mode: "demo"}
				cfg.defaults()
				cfg.MediaScheduling.normalize(cfg.TaskWorkers)
				a := &App{config: cfg, mediaScheduler: newMediaScheduler(cfg.MediaScheduling)}
				// Call the exact committed production preselector, not a rewritten
				// LIMIT query. Bulk rows never enter strict claim or provider I/O.
				for tick := 0; tick < 4; tick++ {
					var priorCursor string
					if err = conn.QueryRow(context.Background(), `SELECT workspace_cursor FROM studio_lane_schedule WHERE lane='query' AND provider_mode='demo'`).Scan(&priorCursor); err != nil {
						t.Fatal(err)
					}
					before := a.mediaScheduler.snapshot().Lanes[mediaQueryLane].ExaminedCandidates
					counter.calls.Store(0)
					started := time.Now()
					candidate, selectionErr := a.selectMediaScheduleCandidate(context.Background(), selectionConn, mediaQueryLane)
					elapsedMS := float64(time.Since(started)) / float64(time.Millisecond)
					queryCalls := counter.calls.Load()
					err = selectionErr
					if err != nil {
						t.Fatal(err)
					}
					after := a.mediaScheduler.snapshot().Lanes[mediaQueryLane].ExaminedCandidates
					if after-before > mediaScheduleWorkspacePage*mediaScheduleTaskPage {
						t.Fatalf("preselection tick exceeded128 candidates: %d", after-before)
					}
					var nextCursor string
					if err = conn.QueryRow(context.Background(), `SELECT workspace_cursor FROM studio_lane_schedule WHERE lane='query' AND provider_mode='demo'`).Scan(&nextCursor); err != nil || nextCursor == priorCursor {
						t.Fatal("preselector did not advance committed workspace cursor", err)
					}
					if queryCalls < 3 {
						t.Fatal("preselector trace did not include its transaction")
					}
					preselections = append(preselections, schedulerPreselectionEvidence{Queued: count, Terminal: count, Tick: tick, Distribution: distribution, ElapsedMS: elapsedMS, Examined: after - before, QueryCalls: queryCalls, CursorAdvanced: nextCursor != priorCursor, CandidateFound: candidate != nil})
					t.Logf("preselection queued=%d history=%d distribution=%s tick=%d elapsed_ms=%.3f examined=%d query_calls=%d cursor_advanced=%t", count, count, distribution, tick, elapsedMS, after-before, queryCalls, nextCursor != priorCursor)
				}
				var cursor string
				if err = conn.QueryRow(context.Background(), `SELECT workspace_cursor FROM studio_lane_schedule WHERE lane='query' AND provider_mode='demo'`).Scan(&cursor); err != nil || cursor == "" {
					t.Fatal("skipped page did not commit cursor progress", err)
				}
			}
		})
	}
	report := struct {
		CreatedUTC            string
		ProductionSQL         bool
		QueuedMax, HistoryMax int
		Plans                 []schedulerPlanEvidence
		Preselections         []schedulerPreselectionEvidence
	}{time.Now().UTC().Format(time.RFC3339), true, 100000, 100000, evidence, preselections}
	if path := os.Getenv("MEDIA_SCHEDULER_PLAN_REPORT"); path != "" {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("raw EXPLAIN evidence saved", path)
	}
}
