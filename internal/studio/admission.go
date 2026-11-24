package studio

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxGlobalStreams is the opt-in ceiling for authenticated PostgreSQL subscriptions.
const MaxGlobalStreams = 16_384

// AdmissionLimits are process-local policy, not a purchased entitlement or a
// distributed semaphore. Zero selects defaults only for in-process Config;
// explicitly configured environment values are validated before New.
type AdmissionLimits struct {
	WorkspaceRuns, ModelRuns, WorkspaceTasks, ModelTasks, WorkspaceStreams, GlobalStreams int
}

func (l *AdmissionLimits) defaults() {
	for _, v := range []struct {
		p     *int
		value int
	}{{&l.WorkspaceRuns, 64}, {&l.ModelRuns, 32}, {&l.WorkspaceTasks, 128}, {&l.ModelTasks, 64}, {&l.WorkspaceStreams, 32}, {&l.GlobalStreams, 128}} {
		if *v.p == 0 {
			*v.p = v.value
		}
	}
}

func (l AdmissionLimits) validate() error {
	for _, v := range []struct {
		name       string
		value, max int
	}{
		{"ADMISSION_WORKSPACE_RUNS", l.WorkspaceRuns, 4096}, {"ADMISSION_MODEL_RUNS", l.ModelRuns, 4096},
		{"ADMISSION_WORKSPACE_TASKS", l.WorkspaceTasks, 4096}, {"ADMISSION_MODEL_TASKS", l.ModelTasks, 4096},
		{"ADMISSION_WORKSPACE_STREAMS", l.WorkspaceStreams, 128},
		{"ADMISSION_GLOBAL_STREAMS", l.GlobalStreams, MaxGlobalStreams},
	} {
		if v.value < 1 || v.value > v.max {
			return fmt.Errorf("%s must be an integer between 1 and %d", v.name, v.max)
		}
	}
	return nil
}

type AdmissionSnapshot struct {
	Runs  AdmissionResource `json:"runs"`
	Tasks AdmissionResource `json:"tasks"`
}
type AdmissionResource struct {
	Active              int              `json:"active"`
	Limit               int              `json:"limit"`
	Queued              int              `json:"queued"`
	OldestQueuedSeconds float64          `json:"oldest_queued_seconds"`
	Models              []AdmissionModel `json:"models"`
}
type AdmissionModel struct {
	ModelID             string  `json:"model_id"`
	Kind                string  `json:"kind"`
	Active              int     `json:"active"`
	Limit               int     `json:"limit"`
	Queued              int     `json:"queued"`
	OldestQueuedSeconds float64 `json:"oldest_queued_seconds"`
}
type admissionCount struct {
	ModelID, Kind  string
	Active, Queued int
	OldestQueued   time.Time
}

// a.mu spans this check and the eventual resource/idempotency transaction. The
// session advisory lock rejects other writer processes; this is not safe to
// turn into a multi-writer deployment merely by adding a connection pool.
func (a *App) checkAdmissionLocked(workspaceID, modelID, kind string) error {
	workspaceLimit, modelLimit := a.config.Admission.WorkspaceTasks, a.config.Admission.ModelTasks
	if kind == "chat" {
		workspaceLimit, modelLimit = a.config.Admission.WorkspaceRuns, a.config.Admission.ModelRuns
	}
	workspaceFull, modelFull, err := a.admissionAtLimitLocked(workspaceID, modelID, kind, workspaceLimit, modelLimit)
	if err != nil {
		return err
	}
	if workspaceFull {
		return failure(429, "ADMISSION_WORKSPACE_LIMIT", "当前工作区待完成生成已达上限；本次未受理，请等待任务完成或核实待处理任务")
	}
	if modelFull {
		return failure(429, "ADMISSION_MODEL_LIMIT", "当前工作区此模型待完成生成已达上限；本次未受理，请稍后再试")
	}
	return nil
}

// This predicate also appears in migration 004's partial indexes.
const activeAdmissionSQL = `document->>'status' NOT IN ('SUCCEEDED','COMPLETED','FAILED','CANCELLED','INTERRUPTED','PARTIAL')`

func (a *App) admissionAtLimitLocked(workspaceID, modelID, kind string, workspaceLimit, modelLimit int) (bool, bool, error) {
	if a.store != nil && workspaceID != "" {
		// Both thresholds must use the same database snapshot. The two probes
		// need only the matching index entries up to their respective limits.
		table := "studio_tasks"
		if kind == "chat" {
			table = "studio_runs"
		}
		from := " FROM " + table + " WHERE workspace_id=$1 AND " + activeAdmissionSQL
		query := "SELECT EXISTS(SELECT 1" + from + " OFFSET $3 LIMIT 1)," +
			" EXISTS(SELECT 1" + from + " AND document->>'model_id'=$2 OFFSET $4 LIMIT 1)"
		var workspaceFull, modelFull bool
		err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			return conn.QueryRow(ctx, query, workspaceID, modelID, workspaceLimit-1, modelLimit-1).Scan(&workspaceFull, &modelFull)
		})
		return workspaceFull, modelFull, err
	}
	counts, err := a.admissionCountsLocked(workspaceID)
	if err != nil {
		return false, false, err
	}
	workspaceActive, modelActive := 0, 0
	for _, count := range counts {
		if (kind == "chat") != (count.Kind == "chat") {
			continue
		}
		workspaceActive += count.Active
		if count.ModelID == modelID {
			modelActive += count.Active
		}
	}
	return workspaceActive >= workspaceLimit, modelActive >= modelLimit, nil
}

func (a *App) admissionSnapshot(workspaceID string) (AdmissionSnapshot, error) {
	if workspaceID == "" {
		return AdmissionSnapshot{}, errors.New("workspace is required for admission snapshot")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return AdmissionSnapshot{}, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	counts, err := a.admissionCountsLocked(workspaceID)
	if err != nil {
		return AdmissionSnapshot{}, err
	}
	return a.formatAdmissionSnapshot(counts, time.Now()), nil
}

func (a *App) formatAdmissionSnapshot(counts []admissionCount, now time.Time) AdmissionSnapshot {
	result := AdmissionSnapshot{
		Runs:  AdmissionResource{Limit: a.config.Admission.WorkspaceRuns, Models: []AdmissionModel{}},
		Tasks: AdmissionResource{Limit: a.config.Admission.WorkspaceTasks, Models: []AdmissionModel{}},
	}
	known := map[string]bool{}
	for _, c := range counts {
		known[c.ModelID] = true
	}
	for _, model := range a.catalog.Public.Models {
		if !known[model.ID] {
			counts = append(counts, admissionCount{ModelID: model.ID, Kind: model.Kind})
		}
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].ModelID < counts[j].ModelID })
	for _, c := range counts {
		resource, limit := &result.Tasks, a.config.Admission.ModelTasks
		if c.Kind == "chat" {
			resource, limit = &result.Runs, a.config.Admission.ModelRuns
		}
		age := 0.0
		if !c.OldestQueued.IsZero() {
			age = max(0, now.Sub(c.OldestQueued).Seconds())
		}
		resource.Active += c.Active
		resource.Queued += c.Queued
		resource.OldestQueuedSeconds = max(resource.OldestQueuedSeconds, age)
		resource.Models = append(resource.Models, AdmissionModel{ModelID: c.ModelID, Kind: c.Kind, Active: c.Active, Limit: limit, Queued: c.Queued, OldestQueuedSeconds: age})
	}
	return result
}

// Counts all nonterminal resources, including parked/unknown media. PG is the
// source of truth, so cache eviction, restarts and provider changes cannot free
// slots. The empty workspace is internal-only and means all cached resources.
// Observability must never expose that global view from a tenant endpoint.
func (a *App) admissionCountsLocked(workspaceID string) ([]admissionCount, error) {
	if a.store != nil && workspaceID != "" {
		return a.store.admissionCounts(workspaceID)
	}
	counts := make(map[string]*admissionCount)
	add := func(ws, model, kind, status string, created time.Time) {
		if (workspaceID != "" && ws != workspaceID) || terminal(status) {
			return
		}
		key := kind + ":" + model
		c := counts[key]
		if c == nil {
			c = &admissionCount{ModelID: model, Kind: kind}
			counts[key] = c
		}
		c.Active++
		if status == "QUEUED" {
			c.Queued++
			if c.OldestQueued.IsZero() || created.Before(c.OldestQueued) {
				c.OldestQueued = created
			}
		}
	}
	for _, r := range a.runs {
		add(r.Run.WorkspaceID, r.Run.ModelID, "chat", r.Run.Status, r.Run.CreatedAt)
	}
	for _, t := range a.tasks {
		add(t.Task.WorkspaceID, t.Task.ModelID, t.Task.Kind, t.Task.Status, t.Task.CreatedAt)
	}
	result := make([]admissionCount, 0, len(counts))
	for _, c := range counts {
		result = append(result, *c)
	}
	return result, nil
}

func (s *postgresStore) admissionCounts(workspaceID string) ([]admissionCount, error) {
	result := []admissionCount{}
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		rows, err := conn.Query(ctx, `
		SELECT model_id,kind,count(*),count(*) FILTER (WHERE status='QUEUED'),min(created_at) FILTER (WHERE status='QUEUED') FROM (
		 SELECT document->>'model_id' AS model_id,'chat' AS kind,document->>'status' AS status,(document->>'created_at')::timestamptz AS created_at
		 FROM studio_runs WHERE workspace_id=$1 AND `+activeAdmissionSQL+`
		 UNION ALL
		 SELECT document->>'model_id',document->>'kind',document->>'status',(document->>'created_at')::timestamptz
		 FROM studio_tasks WHERE workspace_id=$1 AND `+activeAdmissionSQL+`
		) AS active GROUP BY model_id,kind`, workspaceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c admissionCount
			var oldest *time.Time
			if err := rows.Scan(&c.ModelID, &c.Kind, &c.Active, &c.Queued, &oldest); err != nil {
				return err
			}
			if oldest != nil {
				c.OldestQueued = *oldest
			}
			result = append(result, c)
		}
		return rows.Err()
	})
	return result, err
}
