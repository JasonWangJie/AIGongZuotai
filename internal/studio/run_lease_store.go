package studio

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Until is the database's reported deadline. Authorization always uses the
// current locked row and the database clock, including after a blocking write.
type runLease struct {
	RunID, Owner, ExecutionID, Generation string
	Epoch                                 uint64
	Until                                 time.Time
}

func runLeaseLost() error {
	return failure(409, "RUN_LEASE_LOST", "运行执行权已失效，请读取原运行状态")
}

// Serialize process rotation against every Run mutation. Generation is captured
// once by the control writer, never discovered or adopted on a failed write.
func (s *postgresStore) runTx(billingRunID string, fn func(context.Context, pgx.Tx) error) error {
	return s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = s.lockRunWriterSQL(ctx, tx, billingRunID); err != nil {
			return err
		}
		if err = fn(ctx, tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

func (s *postgresStore) lockRunWriterSQL(ctx context.Context, tx pgx.Tx, billingRunID string) error {
	var generation string
	if err := tx.QueryRow(ctx, "SELECT generation FROM studio_run_executor WHERE singleton FOR SHARE").Scan(&generation); err != nil {
		return err
	}
	if s.runGeneration == "" || generation != s.runGeneration {
		return runLeaseLost()
	}
	// Offline ledger and operations lock the reservation before the Run.
	// A delta or heartbeat never touches billing and may skip this lock.
	if billingRunID != "" {
		if err := lockRunReservation(ctx, tx, billingRunID, ""); err != nil {
			return err
		}
	}
	return nil
}

// Public operations are workspace-scoped even when acquiring a lock. Internal
// execution already carries the globally unique accepted Run identity.
func lockRunReservation(ctx context.Context, tx pgx.Tx, id, workspace string) error {
	_, err := tx.Exec(ctx, `SELECT id FROM studio_ledger_reservations
		WHERE resource_type='run' AND resource_id=$1 AND ($2='' OR workspace_id=$2) FOR UPDATE`, id, workspace)
	return err
}

const matchRunLease = "run_id=$1 AND lease_owner=$2 AND lease_epoch=$3 AND lease_generation=$4 AND execution_id=$5 AND lease_owner<>''"

func (l runLease) checkDeadline(ctx context.Context, tx pgx.Tx) error {
	var valid bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_run_streams WHERE "+matchRunLease+" AND lease_until>clock_timestamp())",
		l.RunID, l.Owner, l.Epoch, l.Generation, l.ExecutionID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return runLeaseLost()
	}
	return nil
}

func (s *postgresStore) lockActiveRunLease(ctx context.Context, tx pgx.Tx, l runLease) (*runStreamHead, error) {
	if l.Generation != s.runGeneration || l.Owner == "" || l.ExecutionID == "" || l.Epoch == 0 {
		return nil, runLeaseLost()
	}
	h, err := getRunStreamHead(ctx, tx, l.RunID, true)
	if err != nil {
		return nil, err
	}
	if h == nil || !h.leaseRequired || h.closed || h.status != "RUNNING" || h.execution != l.ExecutionID {
		return nil, runLeaseLost()
	}
	return h, l.checkDeadline(ctx, tx)
}

func (s *postgresStore) claimRunLease(next *Run, owner string, ttl time.Duration) (runLease, error) {
	if owner == "" || ttl.Microseconds() < 1 || next.Status != "RUNNING" {
		return runLease{}, runLeaseLost()
	}
	l := runLease{RunID: next.ID, Owner: owner, ExecutionID: newID("exec"), Generation: s.runGeneration}
	var run Run
	err := s.runTx(next.ID, func(ctx context.Context, tx pgx.Tx) error {
		h, err := getRunStreamHead(ctx, tx, l.RunID, true)
		if err != nil {
			return err
		}
		if h == nil || !h.leaseRequired || h.closed || h.execution != "" || h.status != "QUEUED" {
			return runLeaseLost()
		}
		if next.Seq != h.head {
			return runStreamConflict()
		}
		run, err = readRunDocumentSQL(ctx, tx, l.RunID)
		if err != nil {
			return err
		}
		if h.moderated {
			// The frozen policy is private and absent from studio_runs.document.
			// The model worker must receive the policy used at acceptance.
			if err = tx.QueryRow(ctx, "SELECT policy FROM studio_run_moderation WHERE run_id=$1", l.RunID).Scan(&run.ModerationPolicy); err != nil {
				return err
			}
		}
		run.Status, run.UpdatedAt = "RUNNING", next.UpdatedAt
		if err = tx.QueryRow(ctx, `UPDATE studio_run_streams SET execution_id=$2,lease_owner=$3,
			lease_generation=$4,lease_epoch=lease_epoch+1,lease_until=clock_timestamp()+$5::bigint*interval '1 microsecond'
			WHERE run_id=$1 RETURNING lease_epoch,lease_until`, l.RunID, l.ExecutionID, l.Owner, l.Generation, ttl.Microseconds()).Scan(&l.Epoch, &l.Until); err != nil {
			return err
		}
		h.execution = l.ExecutionID
		if err = saveRunBusinessSQL(ctx, tx, &run, h, nil); err != nil {
			return err
		}
		return l.checkDeadline(ctx, tx)
	})
	if err != nil {
		return runLease{}, err
	}
	*next = run
	return l, nil
}

func (s *postgresStore) renewRunLease(l runLease, ttl time.Duration) (runLease, error) {
	if ttl.Microseconds() < 1 {
		return runLease{}, runLeaseLost()
	}
	err := s.runTx("", func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.lockActiveRunLease(ctx, tx, l); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `UPDATE studio_run_streams SET lease_until=GREATEST(lease_until,clock_timestamp()+$6::bigint*interval '1 microsecond')
			WHERE `+matchRunLease+" AND lease_until>clock_timestamp() RETURNING lease_until",
			l.RunID, l.Owner, l.Epoch, l.Generation, l.ExecutionID, ttl.Microseconds()).Scan(&l.Until)
		if err == pgx.ErrNoRows {
			return runLeaseLost()
		}
		return err
	})
	if err != nil {
		return runLease{}, err
	}
	return l, nil
}

const clearRunLease = "UPDATE studio_run_streams SET lease_owner='',lease_generation='',lease_until=NULL,lease_epoch=lease_epoch+1 WHERE "

func (s *postgresStore) saveLeasedRun(next *Run, l runLease, expected uint64, events ...AuditEvent) error {
	if next.ID != l.RunID {
		return runLeaseLost()
	}
	run := copyRun(*next)
	err := s.runTx(next.ID, func(ctx context.Context, tx pgx.Tx) error {
		h, err := s.lockActiveRunLease(ctx, tx, l)
		if err != nil {
			return err
		}
		if h.head != expected || run.Status != "RUNNING" && !terminal(run.Status) {
			return runStreamConflict()
		}
		if run.Moderation != nil && terminal(run.Status) && (run.Moderation.State == "ALLOWED") != (run.Status == "COMPLETED") {
			return runStreamConflict()
		}
		if err = saveRunBusinessSQL(ctx, tx, &run, h, events); err != nil {
			return err
		}
		if !terminal(run.Status) {
			return l.checkDeadline(ctx, tx)
		}
		tag, err := tx.Exec(ctx, clearRunLease+matchRunLease+" AND lease_until>clock_timestamp()",
			l.RunID, l.Owner, l.Epoch, l.Generation, l.ExecutionID)
		if err == nil && tag.RowsAffected() != 1 {
			return runLeaseLost()
		}
		return err
	})
	if err == nil {
		*next = run
	}
	return err
}

func (s *postgresStore) saveControlRun(next *Run, events ...AuditEvent) error {
	reason := "execution_incomplete"
	if next.Status == "CANCELLED" {
		reason = "cancelled"
	} else if next.Status != "INTERRUPTED" {
		return runStreamConflict()
	}
	return s.saveStoppedRun(next, reason, nil, events)
}

func (s *postgresStore) saveRecoveredRun(next *Run) error {
	if next.Status != "INTERRUPTED" {
		return runStreamConflict()
	}
	return s.saveStoppedRun(next, "process_restarted", nil, nil)
}

func (s *postgresStore) interruptExpiredRun(l runLease) (*Run, error) {
	run := Run{ID: l.RunID, Status: "INTERRUPTED", UpdatedAt: time.Now().UTC(),
		Error: &APIError{Code: "RUN_LEASE_LOST", Message: "运行执行权已失效，保留已提交的输出"}}
	if err := s.saveStoppedRun(&run, "execution_incomplete", &l, nil); err != nil {
		return nil, err
	}
	return &run, nil
}

// Successful control cancellation, startup recovery and expiry all rebuild
// from the locked public document and committed suffix. No caller-supplied
// usage, moderation decision, private candidate or proposed output is adopted.
func (s *postgresStore) saveStoppedRun(next *Run, reason string, expired *runLease, events []AuditEvent) error {
	var run Run
	err := s.runTx(next.ID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = s.stoppedRunSQL(ctx, tx, next, reason, expired, events)
		return err
	})
	if err == nil {
		*next = run
	}
	return err
}

func (s *postgresStore) stoppedRunSQL(ctx context.Context, tx pgx.Tx, next *Run, reason string, expired *runLease, events []AuditEvent) (Run, error) {
	var run Run
	h, err := getRunStreamHead(ctx, tx, next.ID, true)
	if err != nil {
		return run, err
	}
	run, err = readRunDocumentSQL(ctx, tx, next.ID)
	if err != nil {
		return run, err
	}
	if h != nil {
		if err = applyRunStreamPrefix(ctx, tx, &run, h); err != nil {
			return run, err
		}
	}
	if terminal(run.Status) {
		return run, nil
	}
	if expired != nil {
		var lost bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_run_streams WHERE "+matchRunLease+" AND (lease_generation<>$6 OR lease_until<=clock_timestamp()))",
			expired.RunID, expired.Owner, expired.Epoch, expired.Generation, expired.ExecutionID, s.runGeneration).Scan(&lost); err != nil {
			return run, err
		}
		if !lost {
			return run, runLeaseLost()
		}
	}
	if h != nil {
		run.Seq = h.head + 1
	} else {
		// Pre-021 cursors can include volatile public tokens.
		run.Seq = max(next.Seq, run.Seq+1)
	}
	run.Status, run.Error, run.UpdatedAt = next.Status, next.Error, next.UpdatedAt
	quarantineInterruptedRun(&run, reason)
	if err = saveRunBusinessSQL(ctx, tx, &run, h, events); err != nil {
		return run, err
	}
	if h != nil && h.leaseRequired {
		_, err = tx.Exec(ctx, clearRunLease+"run_id=$1 AND lease_owner<>''", run.ID)
	}
	return run, err
}
