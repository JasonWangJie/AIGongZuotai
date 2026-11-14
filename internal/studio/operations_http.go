package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func decodeOperationsJSON(w http.ResponseWriter, r *http.Request, out any) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return failure(415, "UNSUPPORTED_MEDIA_TYPE", "运营请求需要application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	blob, err := io.ReadAll(r.Body)
	if err != nil {
		return failure(413, "OPERATIONS_BODY_TOO_LARGE", "运营请求最多8KiB")
	}
	if !utf8.Valid(blob) || validateCatalogJSONKeys(blob) != nil {
		return failure(400, "INVALID_JSON", "JSON不能包含重复字段或无效编码")
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(blob, &keys) != nil || keys == nil {
		return failure(400, "INVALID_JSON", "需要JSON对象")
	}
	allowed := map[string]bool{"workspace_id": true, "resource_type": true, "resource_id": true, "resource_version": true, "reason": true, "evidence_ref": true, "expected_version": true, "action": true, "usage": true, "delivered_units": true, "plan_id": true, "plan_digest": true, "password": true}
	for key := range keys {
		if !allowed[key] {
			return failure(400, "INVALID_JSON", "请求字段名无效")
		}
	}
	if raw, ok := keys["usage"]; ok && string(raw) != "null" {
		var usage map[string]json.RawMessage
		if json.Unmarshal(raw, &usage) != nil || usage == nil {
			return failure(400, "INVALID_JSON", "用量需要明确字段对象")
		}
		valid := map[string]bool{"status": true, "source": true, "complete": true, "prompt_tokens": true, "completion_tokens": true, "total_tokens": true}
		for key := range usage {
			if !valid[key] {
				return failure(400, "INVALID_JSON", "用量字段名无效")
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return failure(400, "INVALID_JSON", "请求正文或字段类型无效")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return failure(400, "INVALID_JSON", "请求只能包含一个JSON对象")
	}
	return nil
}

type operationsCursor struct {
	Version                                  int
	User, Workspace, Route, Filter, Kind, ID string
	Before                                   time.Time
}

func parseOperationsPage(r *http.Request, p *principal, workspace, route, filter string) (operationsCursor, int, error) {
	expected := operationsCursor{Version: 1, User: p.User.ID, Workspace: workspace, Route: route, Filter: filter}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 50 {
			return expected, 0, failure(400, "INVALID_LIMIT", "每页需要1至50条")
		}
		limit = n
	}
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return expected, limit, nil
	}
	if len(raw) > 2048 {
		return expected, 0, failure(400, "INVALID_CURSOR", "运营游标无效")
	}
	blob, err := base64.RawURLEncoding.DecodeString(raw)
	var got operationsCursor
	if err != nil || json.Unmarshal(blob, &got) != nil || got.Version != 1 || got.User != expected.User || got.Workspace != workspace || got.Route != route || got.Filter != filter || got.Before.IsZero() || got.Before.Year() < 2000 || got.Before.Year() > 9999 || len(got.ID) > 128 || strings.ContainsRune(got.ID, 0) || !operationsIDPattern.MatchString(got.ID) || got.Kind != "" && got.Kind != "run" && got.Kind != "task" {
		return expected, 0, failure(400, "INVALID_CURSOR", "运营游标与当前身份、范围或筛选不匹配")
	}
	return got, limit, nil
}
func nextOperationsCursor(v operationsCursor) *string {
	blob, _ := json.Marshal(v)
	s := base64.RawURLEncoding.EncodeToString(blob)
	return &s
}
func operationsPlanError(w http.ResponseWriter, err error) {
	if op, ok := err.(*operationError); ok && op.status == 429 {
		w.Header().Set("Retry-After", "15")
	}
	writeError(w, err)
}

func (a *App) operationsCreateCaseHTTP(w http.ResponseWriter, r *http.Request) {
	var in OperationsCaseInput
	if err := decodeOperationsJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, "case_open")
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, replayed, err := a.createOperationsCase(p, in, key)
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 201, result)
}
func (a *App) operationsPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var in OperationsPlanInput
	if err := decodeOperationsJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	permission := operationsPermission(in.Action)
	if permission == "" {
		writeError(w, failure(422, "INVALID_OPERATIONS_INPUT", "运营动作不受支持"))
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, permission)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.previewOperationsCase(p, r.PathValue("id"), in)
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	writeJSON(w, 201, result)
}
func (a *App) operationsExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceID     string `json:"workspace_id"`
		ExpectedVersion uint64 `json:"expected_version"`
		PlanID          string `json:"plan_id"`
		PlanDigest      string `json:"plan_digest"`
		Password        string `json:"password"`
	}
	if err := decodeOperationsJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, "read")
	if err != nil {
		writeError(w, err)
		return
	}
	// Bcrypt is bounded and runs outside App.mu and the business transaction.
	proof, err := a.verifyOperationsPassword(r, in.Password)
	in.Password = ""
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	result, err := a.executeOperationsCase(p, proof, r.PathValue("id"), in.WorkspaceID, in.PlanID, in.PlanDigest, in.ExpectedVersion)
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *App) operationsCaseHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "read")
	if err != nil {
		writeError(w, err)
		return
	}
	var result OperationsCase
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "read"); e != nil {
			return e
		}
		result, e = readOperationsCaseSQL(ctx, tx, workspace, r.PathValue("id"), false)
		if e != nil {
			return e
		}
		resource, e := a.readOperationsResourceSQL(ctx, tx, workspace, result.ResourceType, result.ResourceID, false)
		if e != nil {
			return e
		}
		result.Resource = &resource.Summary
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// Fixed projection deliberately excludes document prompt/output and private
// media handles. SQL returns only the same safe metadata exposed by this API.
func operationsResourceProjection(kind, alias string) string {
	version := "seq"
	reconcile := "false"
	if kind == "task" {
		version = "version"
		reconcile = "($5::boolean AND " + alias + ".document->>'provider_mode'=$6 AND " + alias + ".document->>'status' IN('SUBMIT_UNKNOWN','RECONCILE_REQUIRED') AND COALESCE(" + alias + ".handle->>'ID'," + alias + ".handle->>'id','')<>'')"
	}
	return "jsonb_build_object('id'," + alias + ".id,'workspace_id'," + alias + ".workspace_id,'project_id'," + alias + ".project_id,'resource_type','" + kind + "','model_id'," + alias + ".document->>'model_id','provider_mode'," + alias + ".document->>'provider_mode','status'," + alias + ".document->>'status','resource_version',COALESCE((" + alias + ".document->>'" + version + "')::bigint,0),'billing'," + alias + ".document->'billing','can_reconcile'," + reconcile + ",'updated_at'," + alias + ".updated_at)"
}
func (a *App) operationsExceptionsHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "run" && kind != "task" {
		writeError(w, failure(400, "INVALID_FILTER", "异常类型筛选无效"))
		return
	}
	p, err := a.requireOperations(r, workspace, "read")
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, limit, err := parseOperationsPage(r, p, workspace, "exceptions", kind)
	if err != nil {
		writeError(w, err)
		return
	}
	items := []OperationsException{}
	var next *string
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "read"); e != nil {
			return e
		}
		run := `SELECT r.id,r.updated_at,'run'::text AS kind,` + operationsResourceProjection("run", "r") + ` AS summary FROM studio_runs r WHERE r.workspace_id=$1 AND (r.document->>'status' IN('FAILED','INTERRUPTED') OR r.document->'billing'->>'state'='REVIEW') AND $7 IN('all','run')`
		task := `SELECT t.id,t.updated_at,'task'::text AS kind,` + operationsResourceProjection("task", "t") + ` AS summary FROM studio_tasks t WHERE t.workspace_id=$1 AND (t.document->>'status' IN('FAILED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED','PROVIDER_STATE_UNKNOWN') OR t.document->'billing'->>'state'='REVIEW') AND $7 IN('all','task')`
		query := `SELECT x.summary,COALESCE((SELECT id FROM studio_operations_cases c WHERE c.workspace_id=$1 AND c.resource_type=x.kind AND c.resource_id=x.id AND c.state='OPEN'),'') FROM (` + run + ` UNION ALL ` + task + `) x WHERE ($2::timestamptz='0001-01-01T00:00:00Z' OR (x.updated_at,x.kind,x.id)<($2,$3,$4)) ORDER BY x.updated_at DESC,x.kind DESC,x.id DESC LIMIT $8`
		rows, e := tx.Query(ctx, query, workspace, cursor.Before, cursor.Kind, cursor.ID, a.media != nil, a.config.Mode, kind, limit+1)
		if e != nil {
			return e
		}
		for rows.Next() {
			var blob []byte
			var v OperationsException
			var open string
			if e = rows.Scan(&blob, &open); e != nil {
				rows.Close()
				return e
			}
			if e = json.Unmarshal(blob, &v); e != nil {
				rows.Close()
				return e
			}
			v.OpenCaseID = open
			items = append(items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.Before = last.UpdatedAt
		cursor.Kind = last.ResourceType
		cursor.ID = last.ID
		next = nextOperationsCursor(cursor)
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (a *App) operationsCasesHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "all"
	}
	if state != "all" && state != "OPEN" && state != "RESOLVED" {
		writeError(w, failure(400, "INVALID_FILTER", "工单状态筛选无效"))
		return
	}
	p, err := a.requireOperations(r, workspace, "read")
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, limit, err := parseOperationsPage(r, p, workspace, "cases", state)
	if err != nil {
		writeError(w, err)
		return
	}
	items := []OperationsCase{}
	var next *string
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "read"); e != nil {
			return e
		}
		query := `SELECT c.id,c.workspace_id,c.project_id,c.resource_type,c.resource_id,c.resource_version,c.state,c.version,c.reason,c.evidence_ref,c.created_at,c.updated_at,receipt.document,CASE WHEN c.resource_type='run' THEN ` + operationsResourceProjection("run", "r") + ` ELSE ` + operationsResourceProjection("task", "t") + ` END FROM studio_operations_cases c LEFT JOIN studio_runs r ON c.resource_type='run' AND r.id=c.resource_id AND r.workspace_id=c.workspace_id LEFT JOIN studio_tasks t ON c.resource_type='task' AND t.id=c.resource_id AND t.workspace_id=c.workspace_id LEFT JOIN studio_operations_receipts receipt ON receipt.case_id=c.id WHERE c.workspace_id=$1 AND ($2='all' OR c.state=$2) AND ($3::timestamptz='0001-01-01T00:00:00Z' OR (c.created_at,c.id)<($3,$4)) ORDER BY c.created_at DESC,c.id DESC LIMIT $7`
		rows, e := tx.Query(ctx, query, workspace, state, cursor.Before, cursor.ID, a.media != nil, a.config.Mode, limit+1)
		if e != nil {
			return e
		}
		for rows.Next() {
			var v OperationsCase
			var receipt, resource []byte
			if e = rows.Scan(&v.ID, &v.WorkspaceID, &v.ProjectID, &v.ResourceType, &v.ResourceID, &v.ResourceVersion, &v.State, &v.Version, &v.Reason, &v.EvidenceRef, &v.CreatedAt, &v.UpdatedAt, &receipt, &resource); e != nil {
				rows.Close()
				return e
			}
			if len(receipt) > 0 {
				if e = json.Unmarshal(receipt, &v.Receipt); e != nil {
					rows.Close()
					return e
				}
			}
			if e = json.Unmarshal(resource, &v.Resource); e != nil {
				rows.Close()
				return e
			}
			items = append(items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.Before = last.CreatedAt
		cursor.ID = last.ID
		next = nextOperationsCursor(cursor)
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

type operationsCaseEvent struct {
	ID           string    `json:"id"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	OperationID  string    `json:"operation_id"`
	ReasonCode   string    `json:"reason_code"`
	EvidenceRef  string    `json:"evidence_ref"`
	Version      uint64    `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
}

func (a *App) operationsCaseEventsHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "read")
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	cursor, limit, err := parseOperationsPage(r, p, workspace, "case-events", id)
	if err != nil {
		writeError(w, err)
		return
	}
	items := []operationsCaseEvent{}
	var next *string
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "read"); e != nil {
			return e
		}
		if _, e = readOperationsCaseSQL(ctx, tx, workspace, id, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id,action,resource_type,resource_id,operation_id,reason_code,evidence_ref,version,created_at FROM studio_operations_audit WHERE workspace_id=$1 AND case_id=$2 AND ($3::timestamptz='0001-01-01T00:00:00Z' OR (created_at,id)<($3,$4)) ORDER BY created_at DESC,id DESC LIMIT $5`, workspace, id, cursor.Before, cursor.ID, limit+1)
		if e != nil {
			return e
		}
		for rows.Next() {
			var v operationsCaseEvent
			if e = rows.Scan(&v.ID, &v.Action, &v.ResourceType, &v.ResourceID, &v.OperationID, &v.ReasonCode, &v.EvidenceRef, &v.Version, &v.CreatedAt); e != nil {
				rows.Close()
				return e
			}
			items = append(items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.Before = last.CreatedAt
		cursor.ID = last.ID
		next = nextOperationsCursor(cursor)
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
