package studio

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// All PIDs must be waiting on real PostgreSQL locks before the test releases
// the barrier. A goroutine reaching the client-side call is insufficient.
func awaitRunLeaseWaiters(t *testing.T, inspect *pgx.Conn, pids ...int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := outboxCount(t, inspect, "SELECT count(*) FROM pg_stat_activity WHERE pid=ANY($1) AND wait_event_type='Lock'", pids); n == len(pids) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("contesting sessions did not both reach the PG lock")
}

func TestPostgresRunLeaseContentionExpiryAndLateWrites(t *testing.T) {
	s, dsn, barrierConn := outboxStore(t)
	ctx := context.Background()
	run := outboxFixtureRun(newID("run"))
	run.Output = ""
	if err := s.createRun(run, newID("key"), payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	run.Status = "RUNNING"
	// Independent sessions use the same controller generation and the exact
	// production claiming and saving methods, without opening another App.
	clients := [2]*postgresStore{}
	for i := range clients {
		clients[i] = &postgresStore{conn: deliveryAdmin(t, dsn), runGeneration: s.runGeneration}
	}
	barrier, err := barrierConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT id FROM studio_runs WHERE id=$1 FOR UPDATE", run.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		client *postgresStore
		lease  runLease
		err    error
	}
	results := make(chan result, 2)
	for _, client := range clients {
		go func(client *postgresStore, candidate Run) {
			lease, err := client.claimRunLease(&candidate, newID("worker"), 15*time.Second)
			results <- result{client, lease, err}
		}(client, copyRun(run))
	}
	awaitRunLeaseWaiters(t, s.conn, int32(clients[0].conn.PgConn().PID()), int32(clients[1].conn.PgConn().PID()))
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	winner, loser := <-results, <-results
	if winner.err != nil {
		winner, loser = loser, winner
	}
	if winner.err != nil {
		t.Fatal("both contenders lost", winner.err, loser.err)
	}
	requireOperation(t, loser.err, "RUN_LEASE_LOST")
	l := winner.lease
	if l.Owner == "" || l.Generation != s.runGeneration || l.ExecutionID == "" || l.Epoch == 0 || l.RunID != run.ID {
		t.Fatal("claim returned incomplete authority")
	}
	if n := outboxCount(t, s.conn, "SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1 AND status='RUNNING'", run.ID); n != 1 {
		t.Fatal("first claim did not record exactly one start", n)
	}
	renewed, err := winner.client.renewRunLease(l, 30*time.Second)
	if err != nil || !renewed.Until.After(l.Until) || renewed.Epoch != l.Epoch || renewed.ExecutionID != l.ExecutionID {
		t.Fatal("renewal lost identity or failed to extend deadline", err)
	}
	if n := outboxCount(t, s.conn, `SELECT count(*) FROM studio_run_streams WHERE run_id=$1 AND lease_until=$2
 AND lease_until BETWEEN clock_timestamp()+interval '25 seconds' AND clock_timestamp()+interval '31 seconds'`, run.ID, renewed.Until); n != 1 {
		t.Fatal("renewed deadline differs from the database clock")
	}
	l = renewed
	if err = winner.client.appendLeasedRunDelta(l, 0, "committed", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = winner.client.appendLeasedRunDelta(l, 0, "committed", time.Now()); err != nil {
		t.Fatal("valid duplicate rejected", err)
	}
	requireOperation(t, winner.client.appendLeasedRunDelta(l, 3, "gap", time.Now()), "RUN_STREAM_CONFLICT")
	prefix, err := s.loadRun(run.ID)
	if err != nil || prefix.Seq != 1 || prefix.Output != "committed" {
		t.Fatal("missing committed prefix", err)
	}
	complete := copyRun(prefix)
	complete.Status, complete.Seq = "COMPLETED", prefix.Seq+1
	before := outboxCount(t, s.conn, "SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1", run.ID)
	for _, field := range []string{"owner", "epoch", "generation", "execution"} {
		t.Run(field, func(t *testing.T) {
			forged := l
			switch field {
			case "owner":
				forged.Owner = newID("worker")
			case "epoch":
				forged.Epoch++
			case "generation":
				forged.Generation = newID("generation")
			case "execution":
				forged.ExecutionID = newID("execution")
			}
			_, err := loser.client.renewRunLease(forged, time.Minute)
			requireOperation(t, err, "RUN_LEASE_LOST")
			requireOperation(t, loser.client.appendLeasedRunDelta(forged, 0, "committed", time.Now()), "RUN_LEASE_LOST")
			requireOperation(t, loser.client.appendLeasedRunDelta(forged, 1, "stale", time.Now()), "RUN_LEASE_LOST")
			requireOperation(t, loser.client.saveLeasedRun(&prefix, forged, prefix.Seq), "RUN_LEASE_LOST")
			requireOperation(t, loser.client.saveLeasedRun(&complete, forged, prefix.Seq), "RUN_LEASE_LOST")
		})
	}
	// Shorten this fixture deadline, then let the real DB clock pass it
	// while the valid owner's append is blocked on the business row.
	var due time.Time
	if err = barrierConn.QueryRow(ctx, "UPDATE studio_run_streams SET lease_until=clock_timestamp()+interval '1 second' WHERE run_id=$1 RETURNING lease_until", run.ID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	lock, err := barrierConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, "SELECT id FROM studio_runs WHERE id=$1 FOR UPDATE", run.ID); err != nil {
		t.Fatal(err)
	}
	late := make(chan error, 1)
	go func() { late <- winner.client.appendLeasedRunDelta(l, 1, "late", time.Now()) }()
	awaitRunLeaseWaiters(t, s.conn, int32(winner.client.conn.PgConn().PID()))
	if n := outboxCount(t, s.conn, "SELECT count(*) FROM studio_run_streams WHERE run_id=$1 AND lease_until>clock_timestamp()", run.ID); n != 1 {
		t.Fatal("append did not enter PostgreSQL while its lease was still valid")
	}
	if _, err = s.conn.Exec(ctx, "SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp()))::double precision)+0.03)", due); err != nil {
		t.Fatal(err)
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, <-late, "RUN_LEASE_LOST")
	_, err = winner.client.renewRunLease(l, time.Minute)
	requireOperation(t, err, "RUN_LEASE_LOST")
	requireOperation(t, winner.client.appendLeasedRunDelta(l, 0, "committed", time.Now()), "RUN_LEASE_LOST")
	requireOperation(t, winner.client.saveLeasedRun(&prefix, l, prefix.Seq), "RUN_LEASE_LOST")
	requireOperation(t, winner.client.saveLeasedRun(&complete, l, prefix.Seq), "RUN_LEASE_LOST")
	_, err = loser.client.claimRunLease(&run, newID("worker"), time.Minute)
	requireOperation(t, err, "RUN_LEASE_LOST")
	if n := outboxCount(t, s.conn, "SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1", run.ID); n != before {
		t.Fatal("failed writes advanced outbox", before, n)
	}
	if n := outboxCount(t, s.conn, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", run.ID); n != 1 {
		t.Fatal("failed writes published extra events", n)
	}
	interrupted, err := s.interruptExpiredRun(l)
	if err != nil || interrupted == nil || interrupted.Status != "INTERRUPTED" || interrupted.Output != prefix.Output || interrupted.Seq != complete.Seq {
		t.Fatal("expired Run did not close on its original prefix", err)
	}
	requireOperation(t, winner.client.saveLeasedRun(&complete, l, prefix.Seq), "RUN_LEASE_LOST")
}

func TestPostgresRunLeaseExpiredModerationCannotPublishOrSettle(t *testing.T) {
	a, server, client, identity, db := newModeratedBillingFixture(t)
	admin := deliveryAdmin(t, db)
	const candidate = "private-lease-output-marker"
	entered, release := make(chan string, 1), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	var approvals atomic.Int32
	a.moderation = executionHTTPModerator(t, func(w http.ResponseWriter, _ *http.Request, in moderationHTTPRequest) {
		if in.Phase == "output" {
			entered <- in.Text
			<-release
			approvals.Add(1)
		}
		allowExecutionHTTP(w, in)
	})
	provider := &moderatedExecutionChat{text: candidate}
	a.chat = provider
	input := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "check a paused reviewer"}
	input.QuoteID = httpBillingQuote(t, client, server, identity.Project.ID, "run", input).ID
	const key = "expired-approval-fixture"
	code, raw, _ := authRequest(t, client, server.URL, "POST", "/api/v1/runs", key, identity.Project.ID, input)
	if code != 201 {
		t.Fatal("could not start funded run", code)
	}
	run := decode[Run](t, raw)
	select {
	case text := <-entered:
		if text != candidate {
			t.Fatal("reviewer did not receive the full candidate")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("output reviewer was not called")
	}
	if tag, err := admin.Exec(context.Background(), "UPDATE studio_run_streams SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1 AND lease_until>clock_timestamp()", run.ID); err != nil || tag.RowsAffected() != 1 {
		t.Fatal("failed to expire a live authority during output review", err)
	}
	executionState(t, a, 0, 1)
	finish()
	executionState(t, a, 0, 0)
	final := awaitAuthRun(t, client, server.URL, run.ID)
	if final.Status != "INTERRUPTED" || final.Output != "" || final.Moderation == nil || final.Moderation.State != "REVIEW" ||
		final.Billing == nil || final.Billing.State != "REVIEW" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != 0 {
		t.Fatal("late approval altered moderation, delivery or credit reservation", final.Status)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_moderation_decisions WHERE run_id=$1 AND state='ALLOWED'", run.ID); n != 0 {
		t.Fatal("late approval became an immutable allowed decision")
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals WHERE reservation_id=$1 AND kind IN ('CHARGE','RELEASE')", final.Billing.ReservationID); n != 0 {
		t.Fatal("late approval wrote a charge or release")
	}
	if n := outboxCount(t, admin, `SELECT count(*) FROM studio_outbox_events e WHERE resource_id=$1 AND (status='COMPLETED' OR strpos(to_jsonb(e)::text,$2)>0)`, run.ID, candidate); n != 0 {
		t.Fatal("late approval reached project outbox")
	}
	if n := outboxCount(t, admin, `SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND (name IN ('message.delta','run.completed') OR strpos(data::text,$2)>0)`, run.ID, candidate); n != 0 {
		t.Fatal("late approval reached durable stream")
	}
	for _, path := range []string{"/api/v1/runs/" + run.ID + "/events?after=0", "/api/v1/runs/" + run.ID + "/snapshot", "/api/v1/runs"} {
		code, raw, _ = authRequest(t, client, server.URL, "GET", path, "", identity.Project.ID, nil)
		if code != 200 || bytes.Contains(raw, []byte(candidate)) || !bytes.Contains(raw, []byte("INTERRUPTED")) {
			t.Fatal("late approval visible or interrupted state lost", path, code)
		}
	}
	code, raw, headers := authRequest(t, client, server.URL, "POST", "/api/v1/runs", key, identity.Project.ID, input)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || decode[Run](t, raw).ID != run.ID || provider.calls.Load() != 1 || approvals.Load() != 1 {
		t.Fatal("expired lease repeated model call or output review")
	}
}

func TestPostgresRunLeaseCancellationRevokesLiveWriter(t *testing.T) {
	s, dsn, admin := outboxStore(t)
	worker := &postgresStore{conn: deliveryAdmin(t, dsn), runGeneration: s.runGeneration}
	run := outboxFixtureRun(newID("run"))
	run.Output = ""
	if err := s.createRun(run, newID("key"), payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	run.Status = "RUNNING"
	l, err := worker.claimRunLease(&run, newID("worker"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.appendLeasedRunDelta(l, 0, "recorded", time.Now()); err != nil {
		t.Fatal(err)
	}
	prefix, err := s.loadRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.saveLeasedRun(&prefix, l, 1); err != nil {
		t.Fatal("live writer could not checkpoint", err)
	}
	cancelled := copyRun(prefix)
	cancelled.Status, cancelled.Seq = "CANCELLED", prefix.Seq+1
	if err = s.saveControlRun(&cancelled); err != nil {
		t.Fatal("control cancellation failed", err)
	}
	completed := copyRun(cancelled)
	completed.Status = "COMPLETED"
	requireOperation(t, worker.appendLeasedRunDelta(l, prefix.Seq, "ignored", time.Now()), "RUN_LEASE_LOST")
	requireOperation(t, worker.saveLeasedRun(&prefix, l, prefix.Seq), "RUN_LEASE_LOST")
	requireOperation(t, worker.saveLeasedRun(&completed, l, prefix.Seq), "RUN_LEASE_LOST")
	_, err = worker.renewRunLease(l, time.Minute)
	requireOperation(t, err, "RUN_LEASE_LOST")
	final, err := s.loadRun(run.ID)
	if err != nil || final.Status != "CANCELLED" || final.Seq != cancelled.Seq || final.Output != "recorded" {
		t.Fatal("revoked writer changed the cancelled prefix", err)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1 AND status='CANCELLED'", run.ID); n != 1 {
		t.Fatal("cancellation was not recorded exactly once", n)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", run.ID); n != 2 {
		t.Fatal("stale writer appended after cancellation", n)
	}
}
