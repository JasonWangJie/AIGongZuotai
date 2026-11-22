package studio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunLeaseDurationBounds(t *testing.T) {
	c := Config{}
	c.defaults()
	if c.RunLeaseDuration != 30*time.Second {
		t.Fatal("unexpected default run lease duration")
	}
	for _, d := range []time.Duration{3*time.Second - time.Nanosecond, -time.Second, 5*time.Minute + time.Nanosecond} {
		app, err := newIsolatedApp(t, Config{RunLeaseDuration: d})
		if err == nil {
			app.Close()
			t.Fatalf("accepted invalid duration %s", d)
		}
	}
	for _, d := range []time.Duration{3 * time.Second, 5 * time.Minute} {
		app, err := newIsolatedApp(t, Config{RunLeaseDuration: d})
		if err != nil {
			t.Fatal(err)
		}
		app.Close()
	}
}

func TestPostgresRunLeaseHeartbeatAndInterruptBackpressure(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	a, server := testApp(t, Config{DatabaseURL: db, RunLeaseDuration: 3 * time.Second, PollInterval: time.Hour, RunWorkers: 1})
	admin := deliveryAdmin(t, db)
	emitted, modelCancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	late := make(chan error, 1)
	var calls atomic.Int32
	a.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		calls.Add(1)
		if err := emit("original prefix"); err != nil {
			return err
		}
		close(emitted)
		<-ctx.Done()
		close(modelCancelled)
		<-release // The provider is still executing after cancellation.
		err := emit("late result")
		late <- err
		return err
	})
	code, body, _ := request(t, server, "POST", "/api/v1/runs", "lease-watch-held-model", runInput())
	if code != 201 {
		t.Fatal("run rejected", code, string(body))
	}
	id := decode[Run](t, body).ID
	select {
	case <-emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("no committed prefix")
	}
	hot := make(chan event, 1)
	a.mu.Lock()
	record := a.runs[id]
	record.Subscribers[hot] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(record.Subscribers, hot)
		a.mu.Unlock()
	}()
	var initial time.Time
	if err := admin.QueryRow(ctx, "SELECT lease_until FROM studio_run_streams WHERE run_id=$1", id).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for outboxCount(t, admin, "SELECT count(*) FROM studio_run_streams WHERE run_id=$1 AND lease_until>$2 AND lease_until>clock_timestamp()", id, initial) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("blocked model did not renew lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Inject a rolled-back Outbox rejection only during interruption. The
	// sequence witnesses the attempts even though the transaction rolls back.
	if _, err := admin.Exec(ctx, `CREATE SEQUENCE run_loss_attempt;
CREATE FUNCTION hold_run_loss() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM nextval('run_loss_attempt'); UPDATE studio_outbox_limits SET pending=10000 WHERE singleton; RETURN NEW; END $$;
CREATE TRIGGER hold_run_loss BEFORE UPDATE OF document ON studio_runs FOR EACH ROW
 WHEN (NEW.document->>'status'='INTERRUPTED') EXECUTE FUNCTION hold_run_loss()`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "UPDATE studio_run_streams SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-modelCancelled:
	case <-time.After(4 * time.Second):
		t.Fatal("lost lease did not cancel blocked model")
	}
	deadline = time.Now().Add(4 * time.Second)
	for outboxCount(t, admin, "SELECT count(*) FROM run_loss_attempt WHERE is_called") != 1 {
		if time.Now().After(deadline) {
			t.Fatal("interruption was deferred until provider return")
		}
		time.Sleep(10 * time.Millisecond)
	}
	executionState(t, a, 0, 1)
	finish()
	select {
	case err := <-late:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("late model delta was accepted", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not exit")
	}
	executionState(t, a, 0, 1)
	if _, err := admin.Exec(ctx, `DROP TRIGGER hold_run_loss ON studio_runs`); err != nil {
		t.Fatal(err)
	}
	executionState(t, a, 0, 0)
	select {
	case ev := <-hot:
		if ev.Name != "run.interrupted" || ev.Seq != 2 || outboxCount(t, admin,
			"SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND seq=$2 AND name=$3 AND data=$4::jsonb", id, ev.Seq, ev.Name, string(ev.Data)) != 1 {
			t.Fatal("hot interruption disagrees with the persisted terminal event")
		}
	default:
		t.Fatal("active subscriber missed the interrupted run")
	}
	saved, err := a.store.loadRun(id)
	if err != nil || saved.Status != "INTERRUPTED" || saved.Output != "original prefix" || saved.Seq != 2 || calls.Load() != 1 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1 AND status='INTERRUPTED'", id) != 1 {
		t.Fatal("lost execution was not recovered exactly once", saved.Status, saved.Seq, err)
	}
}
