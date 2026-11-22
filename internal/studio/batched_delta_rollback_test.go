package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Force the actual group statement to roll back after its uncommitted inserts.
// A sequence counts attempted group statements, since ordinary rows roll back.
// Each successful fallback must still be the original append and HTTP run.
func testTwoRunBatchRollback(t *testing.T) {
	db := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: db, AuthMode: "required", RunDeltaBatching: true, PollInterval: time.Hour,
		ChatScheduling: ChatSchedulingConfig{Workers: 2, WorkspaceConcurrency: 2, ModelConcurrency: 2}})
	// Widen just this fault fixture's window so both gated model callbacks
	// reach the same write. The performance samples use the product setting.
	app.runDeltaBatch.mu.Lock()
	app.runDeltaBatch.groupDelay = 250 * time.Millisecond
	app.runDeltaBatch.mu.Unlock()
	admin := deliveryAdmin(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	_, err := admin.Exec(ctx, `CREATE SEQUENCE fixture_group_conflicts;
		CREATE FUNCTION fixture_reject_group() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF (SELECT count(*) FROM inserted WHERE name='message.delta') > 1 THEN
				PERFORM nextval('fixture_group_conflicts');
				PERFORM pg_advisory_xact_lock(hashtext(current_schema()::text),211);
				RAISE EXCEPTION USING ERRCODE='40001', MESSAGE='fixture group rolled back';
			END IF;
			RETURN NULL;
		END $$;
		CREATE TRIGGER fixture_batch_rollback AFTER INSERT ON studio_run_stream_events
		REFERENCING NEW TABLE AS inserted FOR EACH STATEMENT EXECUTE FUNCTION fixture_reject_group()`)
	if err != nil {
		capacityDBFailure(t, "rollback_trigger_setup", err)
	}
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock(hashtext(current_schema()::text),211)"); err != nil {
		capacityDBFailure(t, "rollback_barrier_setup", err)
	}
	unlock := sync.OnceFunc(func() {
		var released bool
		if err := admin.QueryRow(ctx, "SELECT pg_advisory_unlock(hashtext(current_schema()::text),211)").Scan(&released); err != nil || !released {
			t.Error("failed to release fixture rollback barrier")
		}
	})
	defer unlock()

	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	gate := make(chan struct{})
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-gate:
			return emit("fixture confirmed")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	client := authClient(t)
	client.Timeout = 0
	user := registerFixture(t, client, server.URL, "group-rollback@example.test")
	scope := capacityScope{client: client, identity: user}
	var ids []string
	var readers []*bufio.Reader
	for i := range 2 {
		id := executionRun(t, server, client, user, "studio-chat", "rollback test", fmt.Sprintf("batch-rollback-%d", i)).ID
		ids = append(ids, id)
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("two providers did not enter")
		}
		resp := capacityOpen(t, ctx, scope, server.URL, id)
		defer resp.Body.Close()
		readers = append(readers, bufio.NewReader(resp.Body))
	}
	close(gate)
	for {
		var blocked bool
		err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pg_backend_pid()=ANY(pg_blocking_pids(pid)))`).Scan(&blocked)
		if err != nil {
			capacityDBFailure(t, "group_rollback_barrier", err)
		}
		if blocked {
			break
		}
		select {
		case <-time.After(2 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("multi-run delta statement never reached rollback barrier")
		}
	}
	if outboxCount(t, admin, `SELECT count(*) FROM studio_run_stream_events WHERE run_id=ANY($1::text[]) AND name='message.delta'`, ids) != 0 {
		t.Fatal("uncommitted group was visible on independent connection")
	}
	app.mu.Lock()
	private := true
	for _, id := range ids {
		r := app.runs[id]
		private = private && r != nil && r.streamWrite != nil && r.Run.Seq == 0 && r.Run.Output == "" && len(r.Events) == 0
	}
	app.mu.Unlock()
	if !private {
		t.Fatal("pending group leaked to the app stream before rollback")
	}
	unlock()
	for i, r := range readers {
		name, seq, event := readSSEFixture(t, r)
		if name != "message.delta" || seq != "1" || string(event["delta"]) != `"fixture confirmed"` {
			t.Fatal("original live reader did not receive its confirmed single delta")
		}
		name, seq, _ = readSSEFixture(t, r)
		if name != "run.completed" || seq != "2" {
			t.Fatal("original live reader did not receive its terminal immediately after the delta")
		}
		code, raw, header := authRequest(t, client, server.URL, http.MethodPost, "/api/v1/runs", fmt.Sprintf("batch-rollback-%d", i), user.Project.ID,
			RunInput{ProjectID: user.Project.ID, ModelID: "studio-chat", Input: "rollback test"})
		var run Run
		if code != 201 || header.Get("Idempotency-Replayed") != "true" || json.Unmarshal(raw, &run) != nil || run.ID != ids[i] || run.Output != "fixture confirmed" || run.Seq != 2 || run.Status != "COMPLETED" {
			t.Fatal("original receipt no longer identifies the completed original run")
		}
	}
	var attempts, rows, transactions int
	if err := admin.QueryRow(ctx, `SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM fixture_group_conflicts`).Scan(&attempts); err != nil {
		capacityDBFailure(t, "group_rollback_attempts", err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*),count(DISTINCT xmin::text) FROM studio_run_stream_events
		WHERE run_id=ANY($1::text[]) AND name='message.delta' AND seq=1`, ids).Scan(&rows, &transactions); err != nil {
		capacityDBFailure(t, "group_rollback_rows", err)
	}
	stats := app.runDeltaBatch.snapshot()
	if attempts != 1 || rows != 2 || transactions != 2 || calls.Load() != 2 || stats.CommittedBatches != 0 || stats.SoloAttempts != 2 || stats.ConfirmedEvents != 2 {
		t.Fatalf("group rollbacks=%d delta rows=%d xids=%d provider calls=%d solo attempts=%d confirms=%d", attempts, rows, transactions, calls.Load(), stats.SoloAttempts, stats.ConfirmedEvents)
	}
}
