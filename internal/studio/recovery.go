package studio

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Private aggregate startup evidence, not a public capacity/heap metric.
type recoveryStats struct {
	ProjectPages, RunPages, TaskPages                   int
	MaxPageRecords                                      int
	MaxPageEncodedBytes                                 int64
	CachedRunsPeak, CachedTasksPeak, CachedProjectsPeak int
	LeasesCleared                                       int64
}

type recoveryRow struct {
	id   string
	body []byte
}

const recoveryProjectsSQL = `SELECT id,jsonb_build_object('id',id,'workspace_id',workspace_id,'name',name,'created_at',created_at) AS body FROM studio_projects WHERE id>$1 ORDER BY id LIMIT $2`
const recoveryRunsSQL = `SELECT id,document AS body FROM studio_runs WHERE id>$1 AND document->>'status' NOT IN ('COMPLETED','FAILED','CANCELLED','INTERRUPTED') ORDER BY id LIMIT $2`
const recoveryTasksSQL = `SELECT id,jsonb_build_object('task',document,'input',input,'handle',handle,'submission',submission,'next_poll',next_poll) AS body FROM studio_tasks WHERE id>$1 AND document->>'status' NOT IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED') ORDER BY id LIMIT $2`

// Prefix byte gating happens in SQL before bodies are transferred. A row that
// does not fit ends this page without advancing the cursor past that row.
// Sources are fixed internal SQL constants, never client input.
func readRecoveryRows(ctx context.Context, conn *pgx.Conn, source, after string, limit int) ([]recoveryRow, int64, error) {
	query := `WITH page AS MATERIALIZED (` + source + `), sized AS MATERIALIZED (SELECT id,body,octet_length(body::text)::bigint AS n FROM page), bounded AS (SELECT *,sum(n) OVER(ORDER BY id) AS prefix FROM sized) SELECT id,n,CASE WHEN prefix<=$3 THEN body ELSE NULL END FROM bounded ORDER BY id`
	rows, err := conn.Query(ctx, query, after, limit, recoveryMediaPageBytes)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	page := make([]recoveryRow, 0, limit)
	var used int64
	for rows.Next() {
		var row recoveryRow
		var size int64
		if err = rows.Scan(&row.id, &size, &row.body); err != nil {
			return nil, 0, err
		}
		if size <= 0 || size > recoveryMediaPageBytes && len(page) == 0 {
			return nil, 0, errors.New("invalid or oversized recovery record")
		}
		if used+size > recoveryMediaPageBytes {
			break
		}
		if len(row.body) == 0 {
			return nil, 0, errors.New("missing bounded recovery record")
		}
		used += size
		page = append(page, row)
	}
	return page, used, rows.Err()
}

func (a *App) observeRecoveryPage(kind string, count int, used int64) {
	if count == 0 {
		return
	}
	s := &a.recoveryStats
	s.MaxPageRecords = max(s.MaxPageRecords, count)
	s.MaxPageEncodedBytes = max(s.MaxPageEncodedBytes, used)
	switch kind {
	case "project":
		s.ProjectPages++
	case "run":
		s.RunPages++
	case "task":
		s.TaskPages++
	}
}
func (a *App) observeRecoveryCache() {
	s := &a.recoveryStats
	s.CachedProjectsPeak = max(s.CachedProjectsPeak, len(a.projects))
	s.CachedRunsPeak = max(s.CachedRunsPeak, len(a.runs))
	s.CachedTasksPeak = max(s.CachedTasksPeak, len(a.tasks))
}
func recoveryBackpressure(err error) bool {
	var op *operationError
	return errors.As(err, &op) && op.code == "EVENT_BACKLOG_FULL"
}

// The caller holds the single control writer/owner locks and has fenced old
// media generations. Every read and each business transaction gets its own
// ordinary five-second budget. No workers or HTTP handlers run during restore.
func (s *postgresStore) restore(a *App) error {
	if err := s.prepareOutbox(); err != nil {
		return err
	}
	for after := ""; ; {
		var page []recoveryRow
		var used int64
		err := s.do(func(ctx context.Context, conn *pgx.Conn) (err error) {
			page, used, err = readRecoveryRows(ctx, conn, recoveryProjectsSQL, after, recoveryMediaPageRecords)
			return err
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		a.observeRecoveryPage("project", len(page), used)
		for _, row := range page {
			var p Project
			if json.Unmarshal(row.body, &p) != nil || p.ID != row.id {
				return errors.New("invalid recovery project")
			}
			a.projects[p.ID] = p
			after = row.id
		}
		a.observeRecoveryCache()
	}
	if err := s.restoreRuns(a); err != nil {
		return err
	}
	return s.restoreTasks(a)
}

func (s *postgresStore) restoreRuns(a *App) error {
	for after := ""; ; {
		var page []recoveryRow
		var used int64
		err := s.do(func(ctx context.Context, conn *pgx.Conn) (err error) {
			page, used, err = readRecoveryRows(ctx, conn, recoveryRunsSQL, after, recoveryMediaPageRecords)
			return err
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		a.observeRecoveryPage("run", len(page), used)
		for _, row := range page {
			var run Run
			if json.Unmarshal(row.body, &run) != nil || run.ID != row.id {
				return errors.New("invalid recovery run")
			}
			if !terminal(run.Status) {
				quarantineInterruptedRun(&run, "process_restarted")
				run.Status = "INTERRUPTED"
				run.Error = &APIError{Code: "PROCESS_RESTARTED", Message: "服务已重启，保留最后检查点；未自动重新请求模型"}
				run.Seq += recoverySequenceGap
				run.UpdatedAt = time.Now().UTC()
				err = s.saveRunCandidate(&run)
				if recoveryBackpressure(err) {
					break
				}
				if err != nil {
					return err
				}
			}
			r := &runRecord{Run: run, Subscribers: make(map[chan event]bool), LastCheckpoint: time.Now()}
			r.Output.WriteString(run.Output)
			a.runs[run.ID] = r
			after = row.id
			a.observeRecoveryCache()
		}
		// An outbox rejection has already rolled back and released store.mu.
		// Reread from the last confirmed ID rather than mutating the old page.
		if err = s.prepareOutbox(); err != nil {
			return err
		}
	}
}

func (s *postgresStore) recoveryTaskPage(after string) ([]*taskRecord, int64, error) {
	for limit := recoveryMediaPageRecords; ; limit = max(1, limit/2) {
		var page []*taskRecord
		var used int64
		budgetExceeded := false
		err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
			rows, baseBytes, err := readRecoveryRows(ctx, conn, recoveryTasksSQL, after, limit)
			if err != nil {
				return err
			}
			for _, row := range rows {
				var wire struct {
					Task       Task             `json:"task"`
					Input      TaskInput        `json:"input"`
					Handle     MediaHandle      `json:"handle"`
					Submission *MediaSubmission `json:"submission"`
					NextPoll   time.Time        `json:"next_poll"`
				}
				if json.Unmarshal(row.body, &wire) != nil || wire.Task.ID != row.id {
					return errors.New("invalid recovery task")
				}
				page = append(page, &taskRecord{Task: wire.Task, Input: wire.Input, Handle: wire.Handle, Submission: wire.Submission, NextPoll: wire.NextPoll})
			}
			if len(page) == 0 {
				return nil
			}
			if baseBytes >= recoveryMediaPageBytes {
				budgetExceeded = true
				return nil
			}
			privateBytes, err := loadRecoveryMediaPhasesSQL(ctx, conn, page, recoveryMediaPageBytes-baseBytes)
			if errors.Is(err, errRecoveryMediaPageBudget) {
				budgetExceeded = true
				return nil
			}
			used = baseBytes + privateBytes
			return err
		})
		if err != nil {
			return nil, 0, err
		}
		if !budgetExceeded {
			return page, used, nil
		}
		if limit == 1 {
			return nil, 0, errors.New("recovery task exceeds encoded page budget")
		}
	}
}

func prepareRecoveredTask(a *App, t *taskRecord) bool {
	changed := false
	if t.Task.ProviderMode != a.config.Mode {
		t.Task.Status = "RECONCILE_REQUIRED"
		t.Task.Error = &APIError{Code: "PROVIDER_MODE_CHANGED", Message: "任务供应模式与当前配置不同，停止自动执行以待核实"}
		changed = true
	} else if t.Task.Status == "SUBMITTING" && t.Handle.ID == "" {
		t.Task.Status = "SUBMIT_UNKNOWN"
		t.Task.Error = &APIError{Code: "SUBMISSION_UNKNOWN", Message: "提交期间进程中断，受理结果未知；禁止自动重新提交"}
		changed = true
	} else if a.config.Mode == "live" && t.Handle.ID == "" && t.Submission == nil {
		t.Task.Status = "RECONCILE_REQUIRED"
		t.Task.Error = &APIError{Code: "EXECUTION_SNAPSHOT_MISSING", Message: "历史任务没有可验证的执行快照，停止自动提交以待核实"}
		changed = true
	}
	if changed {
		t.Task.Version++
		t.Task.UpdatedAt = time.Now().UTC()
	}
	if _, err := mediaFrozenChannel(t, mediaLaneForTask(t)); err != nil && markMediaScheduleIdentity(t) {
		changed = true
	}
	if t.Task.Moderation != nil && t.MediaModeration != nil && len(t.MediaModeration.Candidates) > 0 && t.Task.Moderation.State == "PENDING" && (t.ReviewDispatch == nil || t.ReviewDispatch.State != "READY" || !a.mediaModeration.compatiblePolicy(t.MediaModeration.Policy)) {
		quarantineMediaTask(t, "process_restarted")
		t.Task.Status = "FAILED"
		t.Task.Error = &APIError{Code: "MEDIA_MODERATION_REVIEW", Message: "媒体审核期间服务中断，原候选保持隔离，不自动重新审核或生成"}
		t.Task.Version++
		t.Task.UpdatedAt = time.Now().UTC()
		changed = true
	}
	return changed
}

func (s *postgresStore) restoreTasks(a *App) error {
	for after := ""; ; {
		page, used, err := s.recoveryTaskPage(after)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		a.observeRecoveryPage("task", len(page), used)
		for _, t := range page {
			if prepareRecoveredTask(a, t) {
				err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
					tx, err := conn.Begin(ctx)
					if err != nil {
						return err
					}
					defer tx.Rollback(ctx)
					if err = saveBilledTaskSQL(ctx, tx, t); err != nil {
						return err
					}
					if err = appendTaskOutboxSQL(ctx, tx, t.Task); err != nil {
						return err
					}
					return tx.Commit(ctx)
				})
				if recoveryBackpressure(err) {
					break
				}
				if err != nil {
					return err
				}
			}
			a.tasks[t.Task.ID] = t
			after = t.Task.ID
			a.observeRecoveryCache()
		}
		if err = s.prepareOutbox(); err != nil {
			return err
		}
	}
}
