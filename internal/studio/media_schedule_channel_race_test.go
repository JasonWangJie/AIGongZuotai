package studio

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresMediaScheduleFirstChannelObservationSerializesClaim(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := "commit"
		if !commit {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			p := &leaseMedia{}
			a, admin, dsn := schedulePostgresFixture(t, p)
			task := leaseTask(t, a)
			a.executePersistentTaskStep(task.ID)
			advanceMediaScheduleDue(t, a, task.ID)
			candidate, err := a.mediaScheduleCandidateByID(context.Background(), admin, mediaQueryLane, task.ID)
			if err != nil || candidate == nil {
				t.Fatal(err)
			}
			if candidate.ChannelVersion != 0 {
				t.Fatal("fixture query channel already existed")
			}
			worker, err := pgx.Connect(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close(context.Background())
			tx, err := admin.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(context.Background(), `INSERT INTO studio_media_channel_runtime(scope,lane,channel_key,effective_limit,cooldown_until,failure_count,probe_required) VALUES('gateway','query',$1,1,clock_timestamp()+interval '1 minute',1,true)`, candidate.ChannelKey); err != nil {
				t.Fatal(err)
			}
			type result struct {
				record *taskRecord
				err    error
			}
			done := make(chan result, 1)
			drained := false
			go func() {
				r, e := a.taskLeases.claimScheduled(context.Background(), worker, *candidate)
				done <- result{r, e}
			}()
			defer func() {
				if drained {
					return
				}
				_ = tx.Rollback(context.Background())
				select {
				case <-done:
				case <-time.After(6 * time.Second):
					t.Error("claim cleanup failed to drain")
				}
			}()
			// Verify an actual database lock wait, not an assumed goroutine delay.
			until := time.Now().Add(3 * time.Second)
			waiting := false
			for !waiting {
				select {
				case out := <-done:
					drained = true
					t.Fatalf("claim passed an uncommitted first channel observation: %+v", out)
				default:
				}
				if err = tx.QueryRow(context.Background(), `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, int32(worker.PgConn().PID())).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if !waiting {
					if time.Now().After(until) {
						t.Fatal("claim never waited on the first channel row")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if commit {
				err = tx.Commit(context.Background())
			} else {
				err = tx.Rollback(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			var out result
			select {
			case out = <-done:
				drained = true
			case <-time.After(5 * time.Second):
				t.Fatal("channel claim failed to drain")
			}
			if out.err != nil {
				t.Fatal(out.err)
			}
			if commit && out.record != nil {
				t.Fatal("committed first cooldown allowed a later claim")
			}
			if !commit && out.record == nil {
				t.Fatal("rolled back observation permanently prevented first healthy claim")
			}
			if p.submits.Load() != 1 || p.queries.Load() != 0 || a.store.failed.Load() {
				t.Fatal("channel serialization caused external work or database fuse")
			}
		})
	}
}
