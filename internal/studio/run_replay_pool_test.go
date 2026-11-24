package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRunReplayPoolConfigAndErrorBoundary(t *testing.T) {
	for _, cfg := range []Config{{RunReplayReadConnections: 1}, {RunReplayReadConnections: -1}, {RunReplayReadConnections: 5}} {
		if _, err := New(cfg); err == nil {
			t.Fatal("invalid replay configuration accepted")
		}
	}
	for _, tc := range []struct {
		code      string
		transient bool
	}{
		{"08006", true}, {"53300", true}, {"57P01", true}, {"57014", true}, {"55P03", true},
		{"XX001", false}, {"42P01", false}, {"23514", false},
	} {
		if transientReplayError(&pgconn.PgError{Code: tc.code}) != tc.transient {
			t.Fatal("incorrect replay failure boundary", tc.code)
		}
	}
	if transientReplayError(errors.New("invalid run stream suffix")) {
		t.Fatal("corrupt output downgraded")
	}
}

func TestRunReplayPoolQueuedCancellation(t *testing.T) {
	// No available token. An HTTP cancellation must finish without even
	// constructing a database connection, let alone touching the controller.
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	s := &postgresStore{replay: &runReplayPool{ctx: parent, slots: make(chan *pgx.Conn, 1)}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	entered := false
	err := s.withRunReplaySnapshot(ctx, func(context.Context, pgx.Tx) error { entered = true; return nil })
	requireOperation(t, err, "RUN_REPLAY_UNAVAILABLE")
	if entered || s.failed.Load() {
		t.Fatal("a queued reader touched the controller")
	}
}

// Hold actual repeatable-read transactions on the two physical sessions. The
// reader callbacks also verify their isolation settings and stable snapshot.
func TestPostgresRunReplayPoolSaturationAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, _, admin := outboxStore(t)
	if err := s.openRunReplayPool(ctx, 2); err != nil {
		t.Fatal(err)
	}
	r := outboxFixtureRun(newID("run"))
	r.Output = ""
	if err := s.createRun(r, newID("key"), payloadHash(r)); err != nil {
		t.Fatal(err)
	}
	lease := claimFixtureRun(t, s, &r)
	scope := subscriptionScope{WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID}
	entered := make(chan int32, 2)
	release := make(chan struct{})
	hold := func(c context.Context, pauseOnServer bool) <-chan error {
		done := make(chan error, 1)
		go func() {
			done <- s.withRunReplaySnapshot(c, func(ctx context.Context, tx pgx.Tx) error {
				var pid int32
				var before, after int
				var settingsOK bool
				err := tx.QueryRow(ctx, `SELECT pg_backend_pid(),head_seq,
 current_setting('transaction_read_only')='on' AND current_setting('default_transaction_read_only')='on'
 AND current_setting('transaction_isolation')='repeatable read' FROM studio_run_streams WHERE run_id=$1`, r.ID).Scan(&pid, &before, &settingsOK)
				if err != nil {
					return err
				}
				if !settingsOK || before != 0 {
					return errors.New("invalid reader snapshot")
				}
				entered <- pid
				if pauseOnServer {
					_, err = tx.Exec(ctx, "SELECT pg_sleep(20)")
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
				}
				if err = tx.QueryRow(ctx, "SELECT head_seq FROM studio_run_streams WHERE run_id=$1", r.ID).Scan(&after); err != nil {
					return err
				}
				if before != after {
					return errors.New("read committed head advanced inside snapshot")
				}
				return nil
			})
		}()
		return done
	}
	readPID := func(done <-chan error) int32 {
		t.Helper()
		select {
		case pid := <-entered:
			return pid
		case err := <-done:
			t.Fatalf("reader failed: %v", err)
		case <-time.After(3 * time.Second):
			t.Fatal("reader did not start")
		}
		return 0
	}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	first := hold(firstCtx, true)
	firstPID := readPID(first)
	second := hold(ctx, false)
	secondPID := readPID(second)
	if firstPID == secondPID || firstPID == s.replay.writerPID || secondPID == s.replay.writerPID {
		t.Fatal("reader shared physical writer or reader session")
	}

	// Wait until PostgreSQL itself is running the first query before canceling.
	waitReplayCount(t, admin, 1, "SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND wait_event='PgSleep'", firstPID)
	short, cancelShort := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err := s.readRunStreamPage(short, scope, r.ID, 0, false)
	cancelShort()
	requireOperation(t, err, "RUN_REPLAY_UNAVAILABLE")
	if err = s.appendLeasedRunDelta(lease, 0, "A", time.Now()); err != nil {
		t.Fatal("writer blocked by both readers", err)
	}
	cancelFirst()
	select {
	case err = <-first:
		requireOperation(t, err, "RUN_REPLAY_UNAVAILABLE")
	case <-time.After(2 * time.Second):
		t.Fatal("active cancellation ignored")
	}
	close(release)
	if err = <-second; err != nil {
		t.Fatal(err)
	}
	page, err := s.readRunStreamPage(ctx, scope, r.ID, 0, false)
	if err != nil || page.high != 1 || len(page.events) != 1 || page.events[0].Seq != 1 {
		t.Fatal("committed gap did not replay", err)
	}
	foreign := scope
	foreign.ProjectID = "other"
	_, err = s.readRunStreamPage(ctx, foreign, r.ID, 0, false)
	requireOperation(t, err, "NOT_FOUND")
	if s.failed.Load() {
		t.Fatal("transient reader error stopped the writer")
	}
}
func waitReplayCount(t *testing.T, conn *pgx.Conn, want int, query string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if outboxCount(t, conn, query, args...) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("database observation did not reach expected count", want)
}

func TestPostgresRunReplayPoolReaderDisconnectAndClose(t *testing.T) {
	ctx := context.Background()
	s, _, admin := outboxStore(t)
	if err := s.openRunReplayPool(ctx, 1); err != nil {
		t.Fatal(err)
	}
	pid := make(chan int32, 1)
	held := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			done <- s.withRunReplaySnapshot(ctx, func(ctx context.Context, tx pgx.Tx) error {
				pid <- int32(tx.Conn().PgConn().PID())
				_, err := tx.Exec(ctx, "SELECT pg_sleep(20)")
				return err
			})
		}()
		return done
	}
	readerPID := func(done <-chan error) int32 {
		t.Helper()
		select {
		case result := <-pid:
			return result
		case err := <-done:
			t.Fatalf("reader did not reach its PostgreSQL session: %v", err)
		case <-time.After(3 * time.Second):
			t.Fatal("reader did not acquire a PostgreSQL session")
		}
		return 0
	}
	killed := held()
	first := readerPID(killed)
	waitReplayCount(t, admin, 1, "SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND wait_event='PgSleep'", first)
	if outboxCount(t, admin, "SELECT count(*) WHERE pg_terminate_backend($1)", first) != 1 {
		t.Fatal("reader termination failed")
	}
	requireOperation(t, <-killed, "RUN_REPLAY_UNAVAILABLE")
	if err := s.health(); err != nil {
		t.Fatal("writer failed after reader disconnect", err)
	}
	finishing := held()
	next := readerPID(finishing)
	if next == first {
		t.Fatal("terminated connection reused")
	}
	waitReplayCount(t, admin, 1, "SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND wait_event='PgSleep'", next)
	s.replay.close()
	requireOperation(t, <-finishing, "RUN_REPLAY_UNAVAILABLE")
	waitReplayCount(t, admin, 0, "SELECT count(*) FROM pg_stat_activity WHERE pid=ANY($1::int[])", []int32{first, next})
	_, err := s.readRunStreamPage(ctx, subscriptionScope{}, "missing", 0, false)
	requireOperation(t, err, "RUN_REPLAY_UNAVAILABLE")
	if err = s.health(); err != nil {
		t.Fatal("closing readers touched writer", err)
	}
}

func TestPostgresRunReplayPoolBindingAndIntegrity(t *testing.T) {
	for _, issue := range []string{"storage", "generation", "writer lock", "writer disconnect", "gap"} {
		t.Run(issue, func(t *testing.T) {
			ctx := context.Background()
			s, _, admin := outboxStore(t)
			if err := s.openRunReplayPool(ctx, 1); err != nil {
				t.Fatal(err)
			}
			r := outboxFixtureRun(newID("run"))
			r.Output = ""
			if err := s.createRun(r, newID("key"), payloadHash(r)); err != nil {
				t.Fatal(err)
			}
			lease := claimFixtureRun(t, s, &r)
			if err := s.appendLeasedRunDelta(lease, 0, "A", time.Now()); err != nil {
				t.Fatal(err)
			}
			var err error
			switch issue {
			case "storage":
				_, err = admin.Exec(ctx, "UPDATE studio_storage_identity SET storage_id=gen_random_uuid() WHERE singleton")
			case "generation":
				_, err = admin.Exec(ctx, "UPDATE studio_run_executor SET generation='replaced' WHERE singleton")
			case "writer lock":
				err = s.do(func(ctx context.Context, c *pgx.Conn) error {
					var unlocked bool
					err := c.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", controlWriterLock).Scan(&unlocked)
					if err == nil && !unlocked {
						return errors.New("writer did not hold lock")
					}
					return err
				})
			case "gap":
				_, err = admin.Exec(ctx, "DELETE FROM studio_run_stream_events WHERE run_id=$1 AND seq=1", r.ID)
			case "writer disconnect":
				if outboxCount(t, admin, "SELECT count(*) WHERE pg_terminate_backend($1)", s.replay.writerPID) != 1 {
					t.Fatal("could not terminate the isolated test writer")
				}
				waitReplayCount(t, admin, 0, "SELECT count(*) FROM pg_stat_activity WHERE pid=$1", s.replay.writerPID)
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := s.readRunStreamPage(ctx, subscriptionScope{WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID}, r.ID, 0, false)
			requireOperation(t, err, "DATABASE_UNAVAILABLE")
			if len(page.events) != 0 || !s.failed.Load() {
				t.Fatal("unbound/corrupt snapshot released or control writer still available")
			}
			requireOperation(t, s.health(), "DATABASE_UNAVAILABLE")
		})
	}
}

func TestPostgresRunReplayPoolStartupFailureReleasesSessions(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	seed, err := openPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	seed.close()
	admin := deliveryAdmin(t, dsn)
	// The reader completes its startup verification before this later startup
	// step. Tag only sessions created by the failing New; observe from admin.
	_, err = admin.Exec(ctx, `CREATE SEQUENCE replay_startup_marker;
 CREATE FUNCTION reject_executor_rotation() RETURNS trigger LANGUAGE plpgsql
 AS $$ BEGIN PERFORM nextval('replay_startup_marker'); RAISE EXCEPTION 'fixture: stop after reader initialization'; END $$;
 CREATE TRIGGER reject_executor_rotation BEFORE UPDATE ON studio_task_executor
 FOR EACH ROW EXECUTE FUNCTION reject_executor_rotation()`)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	label := newID("replay_startup")
	q.Set("application_name", label)
	q.Set("target_session_attrs", "read-write")
	u.RawQuery = q.Encode()
	app, err := newIsolatedApp(t, Config{DatabaseURL: u.String(), RunReplayReadConnections: 2})
	if err == nil || app != nil {
		if app != nil {
			app.Close()
		}
		t.Fatal("fault-injected startup succeeded")
	}
	waitReplayCount(t, admin, 0, "SELECT count(*) FROM pg_stat_activity WHERE application_name=$1", label)
	if outboxCount(t, admin, "SELECT count(*) FROM replay_startup_marker WHERE is_called") != 1 {
		t.Fatal("startup failed before the replay reader had been opened")
	}
	// Reopening the original store must still acquire its sole writer lock.
	reopened, err := openPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	reopened.close()
}

func TestPostgresRunReplayPoolModeratedSnapshot(t *testing.T) {
	ctx := context.Background()
	s, err := openPostgres(testDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	if err := s.openRunReplayPool(ctx, 1); err != nil {
		t.Fatal(err)
	}
	engine, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
	if err != nil {
		t.Fatal(err)
	}
	r := outboxFixtureRun(newID("run"))
	r.Output = ""
	r.ModerationPolicy = engine.Snapshot()
	r.Moderation = InitialModerationSummary(r.ModerationPolicy)
	if err = s.createRunCandidate(&r, newID("key"), payloadHash(r)); err != nil {
		t.Fatal(err)
	}
	lease := claimFixtureRun(t, s, &r)
	requireOperation(t, s.appendLeasedRunDelta(lease, 0, "pending candidate", time.Now()), "RUN_STREAM_CONFLICT")
	checkSnapshot := func(want string, seq uint64) {
		t.Helper()
		page, err := s.readRunStreamPage(ctx, subscriptionScope{WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID}, r.ID, 0, false)
		if err != nil || len(page.events) != 1 || page.events[0].Name != "run.snapshot" || page.high != seq {
			t.Fatal("moderated stream published a gap or increment", err)
		}
		var visible Run
		if err = json.Unmarshal(page.events[0].Data, &visible); err != nil || visible.Output != want || visible.Seq != seq {
			t.Fatal("snapshot did not match published moderation decision", err)
		}
	}
	checkSnapshot("", 0)
	final := copyRun(r)
	final.Moderation.State, final.Moderation.ReasonCode = "ALLOWED", "allowed"
	final.Moderation.Version++
	final.Status, final.Output, final.Seq = "COMPLETED", "approved bytes", 1
	if err = s.saveLeasedRun(&final, lease, 0); err != nil {
		t.Fatal(err)
	}
	checkSnapshot("approved bytes", 1)
}
