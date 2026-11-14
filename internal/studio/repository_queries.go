package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *postgresStore) idempotency(index string) (idemRecord, bool, error) {
	var value idemRecord
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		var hash []byte
		err := conn.QueryRow(ctx, "SELECT payload_hash,resource_id FROM studio_idempotency WHERE scope_key=$1", index).Scan(&hash, &value.ID)
		copy(value.Hash[:], hash)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}
func (s *postgresStore) loadRun(id string) (Run, error) {
	var run Run
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		var data []byte
		if err := conn.QueryRow(ctx, "SELECT document FROM studio_runs WHERE id=$1", id).Scan(&data); err != nil {
			return err
		}
		return json.Unmarshal(data, &run)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = failure(404, "NOT_FOUND", "运行不存在")
	}
	return run, err
}
func (s *postgresStore) loadTask(id string) (*taskRecord, error) {
	t := &taskRecord{}
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		var data, input, handle, submission []byte
		if err := conn.QueryRow(ctx, "SELECT document,input,handle,submission,next_poll FROM studio_tasks WHERE id=$1", id).Scan(&data, &input, &handle, &submission, &t.NextPoll); err != nil {
			return err
		}
		for _, pair := range []struct {
			data []byte
			out  any
		}{{data, &t.Task}, {input, &t.Input}, {handle, &t.Handle}, {submission, &t.Submission}} {
			if len(pair.data) > 0 {
				if err := json.Unmarshal(pair.data, pair.out); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = failure(404, "NOT_FOUND", "任务不存在")
	}
	return t, err
}
func (a *App) getRunLocked(id string) (*runRecord, error) {
	if r, ok := a.runs[id]; ok {
		return r, nil
	}
	if a.store == nil {
		return nil, failure(404, "NOT_FOUND", "运行不存在")
	}
	run, err := a.store.loadRun(id)
	if err != nil {
		return nil, err
	}
	a.evictTerminalsLocked()
	r := &runRecord{Run: run, Subscribers: make(map[chan event]bool), LastCheckpoint: time.Now()}
	r.Output.WriteString(run.Output)
	a.runs[id] = r
	return r, nil
}
func (a *App) getTaskLocked(id string) (*taskRecord, error) {
	if r, ok := a.tasks[id]; ok {
		return r, nil
	}
	if a.store == nil {
		return nil, failure(404, "NOT_FOUND", "任务不存在")
	}
	t, err := a.store.loadTask(id)
	if err != nil {
		return nil, err
	}
	t.Task.CanReconcile = a.canReconcileTask(t)
	a.evictTerminalsLocked()
	a.tasks[id] = t
	return t, nil
}
func (a *App) evictTerminalsLocked() {
	if a.store == nil {
		return
	}
	if len(a.runs) >= a.config.MaxRecords {
		for id, r := range a.runs {
			if terminal(r.Run.Status) && len(r.Subscribers) == 0 {
				delete(a.runs, id)
				if len(a.runs) < a.config.MaxRecords {
					break
				}
			}
		}
	}
	if len(a.tasks) >= a.config.MaxRecords {
		for id, t := range a.tasks {
			if (terminal(t.Task.Status) || t.Task.Status == "SUBMIT_UNKNOWN" || t.Task.Status == "RECONCILE_REQUIRED") && !t.InFlight {
				delete(a.tasks, id)
				if len(a.tasks) < a.config.MaxRecords {
					break
				}
			}
		}
	}
}
func (a *App) findIdempotencyLocked(index string) (idemRecord, bool, error) {
	if a.store != nil {
		return a.store.idempotency(index)
	}
	value, ok := a.idem[index]
	return value, ok, nil
}

func (s *postgresStore) assetOwned(workspaceID, projectID, assetID string) (bool, error) {
	var found bool
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_library_entries WHERE workspace_id=$1 AND ($2='' OR project_id=$2) AND artifact_id=$3 AND source='generated' AND state='active')", workspaceID, projectID, assetID).Scan(&found)
	})
	return found, err
}

func pageBounds(r *http.Request) (int, int, error) {
	limit, offset := 100, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return 0, 0, failure(400, "INVALID_PAGE", "limit需要1至100")
		}
		limit = n
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100000 {
			return 0, 0, failure(400, "INVALID_PAGE", "offset需要0至100000")
		}
		offset = n
	}
	return limit, offset, nil
}
func (a *App) listRunsHTTP(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := pageBounds(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := principalFrom(r)
	items := []Run{}
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			rows, err := conn.Query(ctx, "SELECT document FROM studio_runs WHERE workspace_id=$1 AND project_id=$2 ORDER BY (document->>'created_at')::timestamptz DESC,id DESC LIMIT $3 OFFSET $4", p.Workspace.ID, p.Project.ID, limit, offset)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var data []byte
				if err = rows.Scan(&data); err != nil {
					return err
				}
				var run Run
				if err = json.Unmarshal(data, &run); err != nil {
					return err
				}
				items = append(items, run)
			}
			return rows.Err()
		})
		if err != nil {
			writeError(w, err)
			return
		}
		// Replace checkpoint rows with fresher in-process text without querying per token.
		a.mu.Lock()
		for i := range items {
			if current, ok := a.runs[items[i].ID]; ok {
				items[i] = current.Run
			}
		}
		a.mu.Unlock()
	} else {
		for _, run := range a.listRuns() {
			if run.WorkspaceID == p.Workspace.ID && run.ProjectID == p.Project.ID {
				items = append(items, run)
			}
		}
		if offset >= len(items) {
			items = []Run{}
		} else {
			items = items[offset:min(offset+limit, len(items))]
		}
	}
	for i := range items {
		items[i] = copyRun(items[i])
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "has_more": len(items) == limit})
}
func (a *App) listTasksHTTP(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := pageBounds(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := principalFrom(r)
	items := []Task{}
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			rows, err := conn.Query(ctx, "SELECT document FROM studio_tasks WHERE workspace_id=$1 AND project_id=$2 ORDER BY (document->>'created_at')::timestamptz DESC,id DESC LIMIT $3 OFFSET $4", p.Workspace.ID, p.Project.ID, limit, offset)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var data []byte
				if err = rows.Scan(&data); err != nil {
					return err
				}
				var task Task
				if err = json.Unmarshal(data, &task); err != nil {
					return err
				}
				items = append(items, task)
			}
			return rows.Err()
		})
		if err != nil {
			writeError(w, err)
			return
		}
	} else {
		for _, task := range a.listTasks() {
			if task.WorkspaceID == p.Workspace.ID && task.ProjectID == p.Project.ID {
				items = append(items, task)
			}
		}
		if offset >= len(items) {
			items = []Task{}
		} else {
			items = items[offset:min(offset+limit, len(items))]
		}
	}
	for i := range items {
		if items[i].ProviderMode != a.config.Mode || a.media == nil {
			items[i].CanReconcile = false
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "has_more": len(items) == limit})
}
