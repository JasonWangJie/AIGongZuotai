package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aigongzuotai/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var migrationFiles = db.Migrations

// All mutations use the same connection that holds the session advisory lock.
// A lost connection cannot be silently replaced by a pool while the old process
// keeps writing. This is intentionally a single-writer deployment, not HA.
type postgresStore struct {
	notifications outboxNotifications
	runGeneration string
	mu            sync.Mutex
	conn          *pgx.Conn
	replay        *runReplayPool
	streamAuth    *runReplayPool
	failed        atomic.Bool
	onFailure     func()
}

func openPostgres(databaseURL string) (*postgresStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return nil, errors.New("PostgreSQL connection failed; verify DATABASE_URL and database availability")
	}
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", controlWriterLock).Scan(&locked); err != nil || !locked {
		_ = conn.Close(ctx)
		return nil, errors.New("PostgreSQL writer lock unavailable; another workbench instance may already own this database")
	}
	s := &postgresStore{conn: conn, runGeneration: newID("executor")}
	// Startup DDL/backfill has a separate bounded budget. Ordinary business
	// operations retain their five-second fail-closed deadline.
	if err = s.doWithTimeout(60*time.Second, func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = applyMigrations(ctx, tx); err != nil {
			return err
		}
		var rotated string
		if err = tx.QueryRow(ctx, "UPDATE studio_run_executor SET generation=$1 WHERE singleton RETURNING generation", s.runGeneration).Scan(&rotated); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','本地开发工作区') ON CONFLICT DO NOTHING;
		INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','未命名创意项目') ON CONFLICT DO NOTHING;`)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}); err != nil {
		s.close()
		return nil, errors.New("PostgreSQL migration failed; startup rejected")
	}
	return s, nil
}

func applyMigrations(ctx context.Context, tx pgx.Tx) error {
	return applyMigrationsFrom(ctx, tx, migrationFiles)
}

func applyMigrationsFrom(ctx context.Context, tx pgx.Tx, files fs.FS) error {
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS studio_schema_versions(version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	names, err := fs.Glob(files, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	known := make(map[int]bool)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		prefix, _, ok := strings.Cut(base, "_")
		if !ok {
			return errors.New("migration filename lacks numeric version")
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version < 1 || known[version] {
			return errors.New("invalid or duplicate migration version")
		}
		known[version] = true
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_schema_versions WHERE version=$1)", version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		script, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(script)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_schema_versions(version) VALUES($1) ON CONFLICT DO NOTHING", version); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, "SELECT version FROM studio_schema_versions")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		if err = rows.Scan(&version); err != nil {
			return err
		}
		if !known[version] {
			return errors.New("database contains a migration newer than this executable")
		}
	}
	return rows.Err()
}

func (s *postgresStore) do(fn func(context.Context, *pgx.Conn) error) error {
	return s.doWithTimeout(5*time.Second, fn)
}
func (s *postgresStore) doWithTimeout(timeout time.Duration, fn func(context.Context, *pgx.Conn) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed.Load() {
		return failure(503, "DATABASE_UNAVAILABLE", "数据库不可用，服务已停止执行；请恢复数据库后重启")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := fn(ctx, s.conn)
	var operation *operationError
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !errors.As(err, &operation) {
		return s.stopOnDatabaseFailure()
	}
	return err
}
func (s *postgresStore) stopOnDatabaseFailure() error {
	if s.failed.CompareAndSwap(false, true) && s.onFailure != nil {
		s.onFailure()
	}
	return failure(503, "DATABASE_UNAVAILABLE", "数据库操作失败；已停止执行以保护任务状态")
}
func (s *postgresStore) health() error {
	return s.do(func(ctx context.Context, conn *pgx.Conn) error { return conn.Ping(ctx) })
}
func (s *postgresStore) close() {
	if s.streamAuth != nil {
		s.streamAuth.close()
	}
	if s.replay != nil {
		s.replay.close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.conn.Close(ctx)
}

func (s *postgresStore) createRun(run Run, index string, hash [32]byte, events ...AuditEvent) error {
	return s.createRunCandidate(&run, index, hash, events...)
}
func (s *postgresStore) createRunCandidate(run *Run, index string, hash [32]byte, events ...AuditEvent) error {
	return s.createGuardedRun(run, index, hash, nil, nil, events...)
}
func (s *postgresStore) createGuardedRun(run *Run, index string, hash [32]byte, guard *workflowChildGuard, actor *principal, events ...AuditEvent) error {
	next := copyRun(*run)
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = checkOrdinaryAccountSQL(ctx, tx, actor); err != nil {
			return err
		}
		if err = guard.check(ctx, tx, next.WorkspaceID, next.ProjectID, "text", hash); err != nil {
			return err
		}
		if err = checkWorkspaceGenerationSQL(ctx, tx, next.WorkspaceID); err != nil {
			return err
		}
		if err = insertBilledRunSQL(ctx, tx, &next, index, hash, events, actor); err != nil {
			return err
		}
		if err = guard.attach(ctx, tx, next.ID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err == nil {
		*run = next
	}
	return err
}
func insertBilledRunSQL(ctx context.Context, tx pgx.Tx, run *Run, index string, hash [32]byte, events []AuditEvent, actor *principal) error {
	if run.BillingQuoteID != "" {
		billing, err := reserveLedgerSQL(ctx, tx, LedgerReservationRequest{ActorUserID: riskActorID(actor), QuoteID: run.BillingQuoteID, WorkspaceID: run.WorkspaceID, ProjectID: run.ProjectID, ResourceType: "run", ResourceID: run.ID, ConversationID: run.ConversationID, ModelID: run.ModelID, ModelVersion: run.ModelVersion, ProviderMode: run.ProviderMode, RequestHash: run.BillingRequestHash})
		if err != nil {
			return err
		}
		run.Billing = billing
	}
	return insertRunSQL(ctx, tx, *run, index, hash, events)
}
func insertRunSQL(ctx context.Context, tx pgx.Tx, run Run, index string, hash [32]byte, events []AuditEvent) error {
	run = copyRun(run)
	doc, _ := json.Marshal(run)
	if _, err := tx.Exec(ctx, "INSERT INTO studio_runs(id,workspace_id,project_id,document,updated_at,conversation_id) VALUES($1,$2,$3,$4,$5,nullif($6,''))", run.ID, run.WorkspaceID, run.ProjectID, doc, run.UpdatedAt, run.ConversationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO studio_run_streams(run_id,head_seq,floor_seq,snapshot_seq,output_bytes,closed,moderated) VALUES($1,$2,$2,$2,$3,$4,$5)", run.ID, run.Seq, len(run.Output), terminal(run.Status), run.Moderation != nil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type) VALUES($1,$2,$3,'run')", index, hash[:], run.ID); err != nil {
		return err
	}
	if err := insertRunModerationSQL(ctx, tx, run); err != nil {
		return err
	}
	for _, event := range events {
		if err := insertAuditSQL(ctx, tx, event); err != nil {
			return err
		}
	}
	return appendRunOutboxSQL(ctx, tx, run)
}
func (s *postgresStore) saveRun(run Run, events ...AuditEvent) error {
	return s.saveRunCandidate(&run, events...)
}
func (s *postgresStore) saveRunCandidate(run *Run, events ...AuditEvent) error {
	next := copyRun(*run)
	err := s.runTx(next.ID, func(ctx context.Context, tx pgx.Tx) error {
		h, err := lockTerminalRunHead(ctx, tx, next.ID, next.Status)
		if err != nil {
			return err
		}
		return saveRunBusinessSQL(ctx, tx, &next, h, events)
	})
	if err == nil {
		*run = next
	}
	return err
}

func saveRunBusinessSQL(ctx context.Context, tx pgx.Tx, run *Run, h *runStreamHead, events []AuditEvent) error {
	if run.Billing != nil {
		billing, err := applyResourceBillingSQL(ctx, tx, run.WorkspaceID, "run", run.ID, run.Status, run.BillingUsage, 0)
		if err != nil {
			return err
		}
		run.Billing = billing
	}
	if err := writeRunSnapshotSQL(ctx, tx, *run, h); err != nil {
		return err
	}
	if err := saveRunModerationSQL(ctx, tx, *run); err != nil {
		return err
	}
	for _, event := range events {
		if err := insertAuditSQL(ctx, tx, event); err != nil {
			return err
		}
	}
	return appendRunOutboxSQL(ctx, tx, *run)
}

type sqlExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func saveRunSQL(ctx context.Context, tx pgx.Tx, run Run) error {
	h, err := lockTerminalRunHead(ctx, tx, run.ID, run.Status)
	if err != nil {
		return err
	}
	return writeRunSnapshotSQL(ctx, tx, run, h)
}

// Generic snapshots are for terminal billing/operations revisions only. Old
// format rows without a header can still receive such a revision after restore.
func lockTerminalRunHead(ctx context.Context, tx pgx.Tx, id, status string) (*runStreamHead, error) {
	h, err := getRunStreamHead(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	var prior string
	if h != nil {
		prior = h.status
	} else if err = tx.QueryRow(ctx, "SELECT document->>'status' FROM studio_runs WHERE id=$1 FOR UPDATE", id).Scan(&prior); err != nil {
		return nil, err
	}
	if !terminal(prior) {
		return nil, runLeaseLost()
	}
	if status != prior {
		return nil, runStreamConflict()
	}
	return h, nil
}

func writeRunSnapshotSQL(ctx context.Context, tx pgx.Tx, run Run, h *runStreamHead) error {
	if err := saveRunStreamSnapshotSQL(ctx, tx, copyRun(run), h); err != nil {
		return err
	}
	return writeRunDocumentSQL(ctx, tx, run)
}

func writeRunDocumentSQL(ctx context.Context, conn sqlExecer, run Run) error {
	run = copyRun(run)
	doc, _ := json.Marshal(run)
	tag, err := conn.Exec(ctx, "UPDATE studio_runs SET document=$2,updated_at=$3 WHERE id=$1 AND workspace_id=$4", run.ID, doc, run.UpdatedAt, run.WorkspaceID)
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("run missing during checkpoint")
	}
	return err
}
func (s *postgresStore) createTask(t *taskRecord, index string, hash [32]byte, events ...AuditEvent) error {
	return s.createGuardedTask(t, index, hash, nil, nil, events...)
}
func (s *postgresStore) createGuardedTask(t *taskRecord, index string, hash [32]byte, guard *workflowChildGuard, actor *principal, events ...AuditEvent) error {
	return s.createSourcedTask(t, index, hash, guard, nil, actor, events...)
}
func (s *postgresStore) createSourcedTask(t *taskRecord, index string, hash [32]byte, guard *workflowChildGuard, panel *panelVideoGuard, actor *principal, events ...AuditEvent) error {
	candidate := t
	next := *t
	next.Task = copyTask(t.Task)
	t = &next
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		kind := "image"
		if t.Task.Operation == "first_frame" {
			kind = "video"
		}
		if err = checkOrdinaryAccountSQL(ctx, tx, actor); err != nil {
			return err
		}
		if err = guard.check(ctx, tx, t.Task.WorkspaceID, t.Task.ProjectID, kind, hash); err != nil {
			return err
		}
		if panel != nil {
			if err = panel.checkSQL(ctx, tx, actor, t.Task); err != nil {
				return err
			}
		}
		if err = checkWorkspaceGenerationSQL(ctx, tx, t.Task.WorkspaceID); err != nil {
			return err
		}
		if t.Task.BillingQuoteID != "" {
			t.Task.Billing, err = reserveLedgerSQL(ctx, tx, LedgerReservationRequest{ActorUserID: riskActorID(actor), QuoteID: t.Task.BillingQuoteID, WorkspaceID: t.Task.WorkspaceID, ProjectID: t.Task.ProjectID, ResourceType: "task", ResourceID: t.Task.ID, ModelID: t.Task.ModelID, ModelVersion: t.Task.ModelVersion, ProviderMode: t.Task.ProviderMode, RequestHash: t.Task.BillingRequestHash})
			if err != nil {
				return err
			}
		}
		doc, _ := json.Marshal(t.Task)
		input, _ := json.Marshal(t.Input)
		handle, _ := json.Marshal(t.Handle)
		submission, _ := json.Marshal(t.Submission)
		if _, err = tx.Exec(ctx, "INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,submission,next_poll) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", t.Task.ID, t.Task.WorkspaceID, t.Task.ProjectID, doc, input, handle, submission, t.NextPoll); err != nil {
			return err
		}
		for position, referenceID := range t.Task.ReferenceAssetIDs {
			if _, err = tx.Exec(ctx, "INSERT INTO studio_task_references(task_id,position,reference_id,workspace_id,project_id) VALUES($1,$2,$3,$4,$5)", t.Task.ID, position, referenceID, t.Task.WorkspaceID, t.Task.ProjectID); err != nil {
				return err
			}
		}
		if err = insertTaskMediaModerationSQL(ctx, tx, t.Task, t.MediaModeration); err != nil {
			return err
		}
		if err = saveMediaScheduleSQL(ctx, tx, t, nil); err != nil {
			return err
		}
		if err = registerTaskLibrarySQL(ctx, tx, t.Task); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type) VALUES($1,$2,$3,'task')", index, hash[:], t.Task.ID); err != nil {
			return err
		}
		for _, event := range events {
			if err = insertAuditSQL(ctx, tx, event); err != nil {
				return err
			}
		}
		if err = appendTaskOutboxSQL(ctx, tx, t.Task); err != nil {
			return err
		}
		if err = guard.attach(ctx, tx, t.Task.ID); err != nil {
			return err
		}
		if panel != nil {
			if err = panel.insertSQL(ctx, tx, t.Task); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	if err == nil {
		candidate.Task = t.Task
		candidate.Schedule = cloneMediaTaskSchedule(t.Schedule)
	}
	return err
}
func saveTaskSQL(ctx context.Context, conn sqlExecer, t *taskRecord) error {
	return writeTaskWithLeaseSQL(ctx, conn, t)
}

func (s *postgresStore) saveTask(t *taskRecord, events ...AuditEvent) error {
	candidate := t
	next := *t
	next.Task = copyTask(t.Task)
	next.Delivery = cloneMediaDelivery(t.Delivery)
	next.ReviewDispatch = cloneMediaReviewDispatch(t.ReviewDispatch)
	next.Schedule = cloneMediaTaskSchedule(t.Schedule)
	t = &next
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err := saveBilledTaskSQL(ctx, tx, t); err != nil {
			return err
		}
		for _, event := range events {
			if err := insertAuditSQL(ctx, tx, event); err != nil {
				return err
			}
		}
		if err := appendTaskOutboxSQL(ctx, tx, t.Task); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err == nil {
		candidate.Task = t.Task
		candidate.Delivery = cloneMediaDelivery(t.Delivery)
		candidate.ReviewDispatch = cloneMediaReviewDispatch(t.ReviewDispatch)
		candidate.Schedule = cloneMediaTaskSchedule(t.Schedule)
		candidate.NextPoll = t.NextPoll
		candidate.ScheduleOutcome = nil
		candidate.ScheduleReconcile = false
	}
	return err
}
