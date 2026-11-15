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

// All statements use the same connection that holds the session advisory lock.
// A lost connection cannot be silently replaced by a pool while the old process
// keeps writing. This is intentionally a single-writer deployment, not HA.
type postgresStore struct {
	notifications outboxNotifications
	mu            sync.Mutex
	conn          *pgx.Conn
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
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(0x53545544494F)).Scan(&locked); err != nil || !locked {
		_ = conn.Close(ctx)
		return nil, errors.New("PostgreSQL writer lock unavailable; another workbench instance may already own this database")
	}
	s := &postgresStore{conn: conn}
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
		s.failed.Store(true)
		if s.onFailure != nil {
			s.onFailure()
		}
		return failure(503, "DATABASE_UNAVAILABLE", "数据库操作失败；已停止执行以保护任务状态")
	}
	return err
}
func (s *postgresStore) health() error {
	return s.do(func(ctx context.Context, conn *pgx.Conn) error { return conn.Ping(ctx) })
}
func (s *postgresStore) close() {
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
	next := copyRun(*run)
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = checkWorkspaceGenerationSQL(ctx, tx, next.WorkspaceID); err != nil {
			return err
		}
		if err = insertBilledRunSQL(ctx, tx, &next, index, hash, events); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err == nil {
		*run = next
	}
	return err
}
func insertBilledRunSQL(ctx context.Context, tx pgx.Tx, run *Run, index string, hash [32]byte, events []AuditEvent) error {
	if run.BillingQuoteID != "" {
		billing, err := reserveLedgerSQL(ctx, tx, LedgerReservationRequest{QuoteID: run.BillingQuoteID, WorkspaceID: run.WorkspaceID, ProjectID: run.ProjectID, ResourceType: "run", ResourceID: run.ID, ConversationID: run.ConversationID, ModelID: run.ModelID, ModelVersion: run.ModelVersion, ProviderMode: run.ProviderMode, RequestHash: run.BillingRequestHash})
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
	return s.do(func(ctx context.Context, conn *pgx.Conn) error {
		return saveRunCandidateTransaction(ctx, conn, run, events)
	})
}

func saveRunTransaction(ctx context.Context, conn *pgx.Conn, run Run, events []AuditEvent) error {
	return saveRunCandidateTransaction(ctx, conn, &run, events)
}
func saveRunCandidateTransaction(ctx context.Context, conn *pgx.Conn, candidate *Run, events []AuditEvent) error {
	run := copyRun(*candidate)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if run.Billing != nil {
		run.Billing, err = applyResourceBillingSQL(ctx, tx, run.WorkspaceID, "run", run.ID, run.Status, run.BillingUsage, 0)
		if err != nil {
			return err
		}
	}
	if err = saveRunSQL(ctx, tx, run); err != nil {
		return err
	}
	if err = saveRunModerationSQL(ctx, tx, run); err != nil {
		return err
	}
	for _, event := range events {
		if err = insertAuditSQL(ctx, tx, event); err != nil {
			return err
		}
	}
	if err = appendRunOutboxSQL(ctx, tx, run); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err == nil {
		*candidate = run
	}
	return err
}

type sqlExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func saveRunSQL(ctx context.Context, conn sqlExecer, run Run) error {
	run = copyRun(run)
	doc, _ := json.Marshal(run)
	tag, err := conn.Exec(ctx, "UPDATE studio_runs SET document=$2,updated_at=$3 WHERE id=$1 AND workspace_id=$4", run.ID, doc, run.UpdatedAt, run.WorkspaceID)
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("run missing during checkpoint")
	}
	return err
}
func (s *postgresStore) createTask(t *taskRecord, index string, hash [32]byte, events ...AuditEvent) error {
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
		if err = checkWorkspaceGenerationSQL(ctx, tx, t.Task.WorkspaceID); err != nil {
			return err
		}
		if t.Task.BillingQuoteID != "" {
			t.Task.Billing, err = reserveLedgerSQL(ctx, tx, LedgerReservationRequest{QuoteID: t.Task.BillingQuoteID, WorkspaceID: t.Task.WorkspaceID, ProjectID: t.Task.ProjectID, ResourceType: "task", ResourceID: t.Task.ID, ModelID: t.Task.ModelID, ModelVersion: t.Task.ModelVersion, ProviderMode: t.Task.ProviderMode, RequestHash: t.Task.BillingRequestHash})
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
