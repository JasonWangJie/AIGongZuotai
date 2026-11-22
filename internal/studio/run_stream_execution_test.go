package studio

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresRunStreamAppendBeforeBroadcastAndConcurrentCancel(t *testing.T) {
	testStreamPendingCancel(t, false)
}

func testStreamPendingCancel(t *testing.T, batch bool) {
	t.Helper()
	db := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: db, PollInterval: time.Hour, RunDeltaBatching: batch})
	started, emitFirst := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	secondResult := make(chan error, 1)
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		calls.Add(1)
		close(started)
		select {
		case <-emitFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := emit("confirmed"); err != nil {
			return err
		}
		err := emit("after cancellation")
		secondResult <- err
		return err
	})
	code, raw, _ := request(t, server, "POST", "/api/v1/runs", "append-cancel-key", runInput())
	if code != 201 {
		t.Fatal("run not accepted", code, string(raw))
	}
	run := decode[Run](t, raw)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	conn := deliveryAdmin(t, db)
	ctx := context.Background()
	barrier, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	var authority string
	if err = barrier.QueryRow(ctx, "SELECT execution_id FROM studio_run_streams WHERE run_id=$1 FOR UPDATE", run.ID).Scan(&authority); err != nil || authority == "" {
		t.Fatal("model started without committed authorization", err)
	}
	close(emitFirst)
	awaitState := func(waitingCancel bool) {
		t.Helper()
		until := time.Now().Add(3 * time.Second)
		for time.Now().Before(until) {
			app.mu.Lock()
			r := app.runs[run.ID]
			waiting := r.streamWrite != nil && (!waitingCancel || r.streamCancel != nil)
			leaked := r.Run.Output != "" || r.Run.Seq != 0 || len(r.Events) != 0
			app.mu.Unlock()
			if leaked {
				t.Fatal("published delta while its durable header was locked")
			}
			if waiting {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatal("run did not reach blocked append/cancel")
	}
	awaitState(false)
	cancelled := make(chan error, 1)
	go func() { _, e := app.cancelRun(run.ID); cancelled <- e }()
	awaitState(true)
	if err = barrier.Rollback(ctx); err != nil && err != pgx.ErrTxClosed {
		t.Fatal(err)
	}
	select {
	case err := <-cancelled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not complete")
	}
	select {
	case err := <-secondResult:
		if err == nil {
			t.Fatal("second append passed waiting cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not finish")
	}
	persisted, err := app.store.loadRun(run.ID)
	if err != nil || persisted.Seq != 2 || persisted.Status != "CANCELLED" || persisted.Output != "confirmed" || calls.Load() != 1 {
		t.Fatal("cancel lost committed prefix or repeated model", err, persisted.Seq, persisted.Status)
	}
	code, raw, h := request(t, server, "POST", "/api/v1/runs", "append-cancel-key", runInput())
	if code != 201 || h.Get("Idempotency-Replayed") != "true" || decode[Run](t, raw).ID != run.ID || calls.Load() != 1 {
		t.Fatal("original key repeated execution")
	}
}

func TestPostgresRunStreamModerationKeepsCandidatesPrivate(t *testing.T) {
	testStreamPrivateCandidates(t, false)
}

func testStreamPrivateCandidates(t *testing.T, batch bool) {
	t.Helper()
	db := testDatabase(t)
	rules := []ModerationRule{
		{Phase: "output", Contains: "DENIED", Decision: "block"},
		{Phase: "output", Contains: "UNKNOWN", Decision: "review"},
	}
	a, s, client, identity := moderatedExecutionFixtureWithConfig(t, Config{DatabaseURL: db, RunDeltaBatching: batch}, rules)
	admin := deliveryAdmin(t, db)
	for _, decision := range []struct{ mark, want string }{{"ALLOWED", "ALLOWED"}, {"DENIED", "BLOCKED"}, {"UNKNOWN", "REVIEW"}, {"CANCEL", "REVIEW"}} {
		t.Run(decision.mark, func(t *testing.T) {
			marker := "private-candidate-" + decision.mark
			provider := &moderatedExecutionChat{text: marker, emitted: make(chan struct{}, 1), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(provider.release) })
			defer release()
			a.chat = provider
			input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "check output visibility"}
			code, raw, _ := authRequest(t, client, s.URL, "POST", "/api/v1/runs", "stream-moderated-"+decision.mark, identity.Project.ID, input)
			if code != 201 {
				t.Fatal("submission rejected", code, string(raw))
			}
			id := decode[Run](t, raw).ID
			select {
			case <-provider.emitted:
			case <-time.After(5 * time.Second):
				t.Fatal("provider did not emit")
			}
			prefix, err := a.store.loadRun(id)
			if err != nil || prefix.Output != "" || prefix.Seq != 0 ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", id) != 0 {
				t.Fatal("candidate reached durable stream before moderation", err)
			}
			if decision.mark == "CANCEL" {
				if _, err := a.cancelRun(id); err != nil {
					t.Fatal(err)
				}
			} else {
				release()
			}
			executionState(t, a, 0, 0)
			final, err := a.store.loadRun(id)
			if err != nil || final.Moderation == nil || provider.calls.Load() != 1 {
				t.Fatal("missing final moderation record", err)
			}
			if final.Moderation.State != decision.want {
				t.Fatal("unexpected decision", final.Moderation.State)
			}
			want := ""
			if decision.want == "ALLOWED" {
				want = marker
			}
			if final.Output != want || !terminal(final.Status) ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND name='message.delta'", id) != 0 ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND name='run.snapshot' AND COALESCE(data->>'output','')=$2", id, want) != 1 {
				t.Fatal("visible stream disagrees with final moderation", final.Status, final.Moderation.State)
			}
		})
	}
	if stats := a.runDeltaBatch.snapshot(); stats.WaitCount != 0 || stats.ConfirmedEvents != 0 {
		t.Fatal("private candidates entered public delta coordinator")
	}
}

// A known cancellation rollback leaves the same model connection usable.
func TestPostgresRunStreamCancelBackpressureKeepsOriginalExecution(t *testing.T) {
	testStreamCancelRollback(t, false)
}

func testStreamCancelRollback(t *testing.T, batch bool) {
	t.Helper()
	db := testDatabase(t)
	a, server := testApp(t, Config{DatabaseURL: db, PollInterval: time.Hour, RunDeltaBatching: batch})
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	// The injected counter belongs to the cancellation transaction itself and
	// rolls back with it. The real outbox count remains unchanged.
	_, err := admin.Exec(ctx, `CREATE FUNCTION force_cancel_pressure() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN UPDATE studio_outbox_limits SET pending=10000 WHERE singleton; RETURN NEW; END $$;
        CREATE TRIGGER force_cancel_pressure BEFORE UPDATE OF document ON studio_runs
        FOR EACH ROW WHEN (NEW.document->>'status'='CANCELLED') EXECUTE FUNCTION force_cancel_pressure()`)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	resume := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	var calls atomic.Int32
	a.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		calls.Add(1)
		if err := emit("first"); err != nil {
			return err
		}
		close(first)
		select {
		case <-resume:
		case <-ctx.Done():
			return ctx.Err()
		}
		return emit(" second")
	})
	code, raw, _ := request(t, server, "POST", "/api/v1/runs", "stream-rolled-back-cancel", runInput())
	if code != 201 {
		t.Fatal("submission refused", code)
	}
	id := decode[Run](t, raw).ID
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk missing")
	}
	if _, err = a.cancelRun(id); !isEventBacklog(err) {
		t.Fatal("injected cancellation did not roll back", err)
	}
	running, err := a.store.loadRun(id)
	if err != nil || running.Status != "RUNNING" || running.Output != "first" || running.Seq != 1 {
		t.Fatal("rollback mutated committed run", err)
	}
	release()
	executionState(t, a, 0, 0)
	final, err := a.store.loadRun(id)
	if err != nil || final.Output != "first second" || final.Status != "COMPLETED" || final.Seq != 3 || calls.Load() != 1 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND name='run.cancelled'", id) != 0 {
		t.Fatal("recoverable cancel interrupted the original execution", err, final.Status)
	}
}
