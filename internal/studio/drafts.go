package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	draftBodyLimit    = 1 << 20
	draftNodeLimit    = 40
	draftVersionLimit = 1000
)

var draftNodeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,79}$`)

type DraftNode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

type ProjectDraft struct {
	ProjectID string      `json:"project_id"`
	Revision  uint64      `json:"revision"`
	Nodes     []DraftNode `json:"nodes"`
	UpdatedAt *time.Time  `json:"updated_at"`
}

type draftSaveInput struct {
	ExpectedRevision *uint64     `json:"expected_revision"`
	Nodes            []DraftNode `json:"nodes"`
}

type draftRevisionSummary struct {
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
	NodeCount int       `json:"node_count"`
}

type draftRevisionPage struct {
	Items              []draftRevisionSummary `json:"items"`
	HasMore            bool                   `json:"has_more"`
	NextBeforeRevision *uint64                `json:"next_before_revision"`
}

func validateDraft(input draftSaveInput) error {
	if input.ExpectedRevision == nil || *input.ExpectedRevision > draftVersionLimit || input.Nodes == nil || len(input.Nodes) > draftNodeLimit {
		return failure(422, "INVALID_DRAFT", "expected_revision 必须为 0 至 1000 的整数，nodes 必须为最多 40 个节点的数组")
	}
	ids := make(map[string]bool, len(input.Nodes))
	for _, node := range input.Nodes {
		if !draftNodeID.MatchString(node.ID) || ids[node.ID] || !utf8.ValidString(node.Title) || !utf8.ValidString(node.Note) || strings.ContainsRune(node.Title, 0) || strings.ContainsRune(node.Note, 0) || utf8.RuneCountInString(node.Title) > 80 || utf8.RuneCountInString(node.Note) > 5000 {
			return failure(422, "INVALID_DRAFT", "节点 id 必须唯一且为 1 至 80 位字母数字及 . _ : -（首位为字母或数字）；标题最多 80 字符，正文最多 5000 字符")
		}
		ids[node.ID] = true
	}
	return nil
}

func decodeDraftJSON(w http.ResponseWriter, r *http.Request) (draftSaveInput, error) {
	var input draftSaveInput
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return input, failure(415, "UNSUPPORTED_MEDIA_TYPE", "请使用 application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, draftBodyLimit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return input, failure(413, "DRAFT_BODY_TOO_LARGE", "草稿请求体最多 1 MiB")
		}
		return input, failure(400, "INVALID_JSON", "无法读取草稿请求")
	}
	if !utf8.Valid(data) {
		return input, failure(400, "INVALID_JSON", "请求必须为 UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, failure(400, "INVALID_JSON", "草稿 JSON 无效或包含不支持的字段")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return input, failure(400, "INVALID_JSON", "请求只能包含一个 JSON 对象")
	}
	return input, validateDraft(input)
}

// The path project and any explicitly selected project must both be authorized.
// Identity and workspace are derived exclusively from the authenticated principal.
func (a *App) draftScope(r *http.Request) (string, string, error) {
	p := principalFrom(r)
	projectID := r.PathValue("project")
	if p == nil {
		return "", "", failure(401, "AUTH_REQUIRED", "请先登录")
	}
	if !a.authorizeProject(p, projectID) || !resourceAllowed(r, p.Workspace.ID, projectID) {
		return "", "", failure(404, "PROJECT_NOT_FOUND", "项目不存在或不属于当前工作区")
	}
	if a.store == nil {
		return "", "", failure(501, "DRAFT_STORAGE_REQUIRED", "云端草稿需要 PostgreSQL 持久存储；当前模式仅支持浏览器草稿")
	}
	return p.Workspace.ID, projectID, nil
}

func (a *App) getProjectDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID, projectID, err := a.draftScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	draft := ProjectDraft{ProjectID: projectID, Nodes: []DraftNode{}}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var nodes []byte
		err := conn.QueryRow(ctx, `SELECT v.revision,v.nodes,v.created_at FROM studio_project_drafts d JOIN studio_draft_revisions v ON v.project_id=d.project_id AND v.revision=d.revision WHERE d.workspace_id=$1 AND d.project_id=$2`, workspaceID, projectID).Scan(&draft.Revision, &nodes, &draft.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(nodes, &draft.Nodes)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, draft)
}

func (a *App) putProjectDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID, projectID, err := a.draftScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeDraftJSON(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	var draft ProjectDraft
	var replay bool
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		draft, replay, err = saveDraftSQL(ctx, conn, workspaceID, projectID, auditActorID(r), key, input)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, draft)
}

type draftQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readDraftRevisionSQL(ctx context.Context, db draftQuerier, workspaceID, projectID string, revision uint64) (ProjectDraft, error) {
	draft := ProjectDraft{ProjectID: projectID, Revision: revision, Nodes: []DraftNode{}}
	var nodes []byte
	err := db.QueryRow(ctx, `SELECT nodes,created_at FROM studio_draft_revisions WHERE workspace_id=$1 AND project_id=$2 AND revision=$3`, workspaceID, projectID, revision).Scan(&nodes, &draft.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return draft, failure(404, "DRAFT_REVISION_NOT_FOUND", "草稿版本不存在")
	}
	if err != nil {
		return draft, err
	}
	err = json.Unmarshal(nodes, &draft.Nodes)
	return draft, err
}

// Locking the project row also serializes the first save, when no draft head
// exists. The SQL CAS remains effective with independent database connections.
func saveDraftSQL(ctx context.Context, conn *pgx.Conn, workspaceID, projectID, actorID, key string, input draftSaveInput) (ProjectDraft, bool, error) {
	if err := validateDraft(input); err != nil {
		return ProjectDraft{}, false, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return ProjectDraft{}, false, err
	}
	defer tx.Rollback(ctx)
	var lockedProject string
	if err = tx.QueryRow(ctx, `SELECT id FROM studio_projects WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, projectID, workspaceID).Scan(&lockedProject); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = failure(404, "PROJECT_NOT_FOUND", "项目不存在或不属于当前工作区")
		}
		return ProjectDraft{}, false, err
	}
	hash := payloadHash(input)
	var storedHash []byte
	var originalRevision uint64
	err = tx.QueryRow(ctx, `SELECT payload_hash,revision FROM studio_draft_idempotency WHERE workspace_id=$1 AND project_id=$2 AND idempotency_key=$3`, workspaceID, projectID, key).Scan(&storedHash, &originalRevision)
	if err == nil {
		if !bytes.Equal(storedHash, hash[:]) {
			return ProjectDraft{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "该幂等键已用于不同草稿；重试时必须使用原键和原请求")
		}
		draft, err := readDraftRevisionSQL(ctx, tx, workspaceID, projectID, originalRevision)
		if err != nil {
			return ProjectDraft{}, false, err
		}
		return draft, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ProjectDraft{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_project_drafts(project_id,workspace_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, projectID, workspaceID); err != nil {
		return ProjectDraft{}, false, err
	}
	var current uint64
	if err = tx.QueryRow(ctx, `SELECT revision FROM studio_project_drafts WHERE project_id=$1 AND workspace_id=$2 FOR UPDATE`, projectID, workspaceID).Scan(&current); err != nil {
		return ProjectDraft{}, false, err
	}
	if current != *input.ExpectedRevision {
		return ProjectDraft{}, false, failure(409, "DRAFT_CONFLICT", "草稿已有新版本；请读取最新版本并保留本地修改后再保存")
	}
	if current >= draftVersionLimit {
		return ProjectDraft{}, false, failure(429, "DRAFT_REVISION_LIMIT", "该项目已达到 1000 个草稿版本；历史版本仍可读取，不会自动删除")
	}
	draft := ProjectDraft{ProjectID: projectID, Revision: current + 1, Nodes: input.Nodes}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&draft.UpdatedAt); err != nil {
		return ProjectDraft{}, false, err
	}
	nodes, err := json.Marshal(input.Nodes)
	if err != nil {
		return ProjectDraft{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_draft_revisions(project_id,workspace_id,revision,nodes,node_count,created_at) VALUES($1,$2,$3,$4,$5,$6)`, projectID, workspaceID, draft.Revision, nodes, len(input.Nodes), draft.UpdatedAt); err != nil {
		return ProjectDraft{}, false, err
	}
	result, err := tx.Exec(ctx, `UPDATE studio_project_drafts SET revision=$3,updated_at=$4 WHERE project_id=$1 AND workspace_id=$2 AND revision=$5`, projectID, workspaceID, draft.Revision, draft.UpdatedAt, current)
	if err != nil {
		return ProjectDraft{}, false, err
	}
	if result.RowsAffected() != 1 {
		return ProjectDraft{}, false, failure(409, "DRAFT_CONFLICT", "草稿已有新版本，请读取最新版本")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_draft_idempotency(project_id,workspace_id,idempotency_key,payload_hash,revision,created_at) VALUES($1,$2,$3,$4,$5,$6)`, projectID, workspaceID, key, hash[:], draft.Revision, draft.UpdatedAt); err != nil {
		return ProjectDraft{}, false, err
	}
	if err = insertAuditSQL(ctx, tx, newAuditEvent(workspaceID, projectID, actorID, "draft.saved", "draft", projectID, AuditDetails{Revision: draft.Revision, NodeCount: len(input.Nodes)})); err != nil {
		return ProjectDraft{}, false, err
	}
	return draft, false, tx.Commit(ctx)
}

func (a *App) getProjectDraftRevision(w http.ResponseWriter, r *http.Request) {
	workspaceID, projectID, err := a.draftScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	revision, err := strconv.ParseUint(r.PathValue("revision"), 10, 63)
	if err != nil || revision < 1 {
		writeError(w, failure(400, "INVALID_DRAFT_REVISION", "版本号必须为正整数"))
		return
	}
	var draft ProjectDraft
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		draft, err = readDraftRevisionSQL(ctx, conn, workspaceID, projectID, revision)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, draft)
}

func (a *App) listProjectDraftRevisions(w http.ResponseWriter, r *http.Request) {
	workspaceID, projectID, err := a.draftScope(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			writeError(w, failure(400, "INVALID_DRAFT_LIMIT", "limit 必须为 1 至 50"))
			return
		}
	}
	before := uint64(draftVersionLimit + 1)
	if raw := r.URL.Query().Get("before_revision"); raw != "" {
		before, err = strconv.ParseUint(raw, 10, 63)
		if err != nil || before < 1 {
			writeError(w, failure(400, "INVALID_DRAFT_CURSOR", "before_revision 必须为正整数"))
			return
		}
	}
	page := draftRevisionPage{Items: []draftRevisionSummary{}}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		rows, err := conn.Query(ctx, `SELECT revision,created_at,node_count FROM studio_draft_revisions WHERE workspace_id=$1 AND project_id=$2 AND revision<$3 ORDER BY revision DESC LIMIT $4`, workspaceID, projectID, before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item draftRevisionSummary
			if err := rows.Scan(&item.Revision, &item.UpdatedAt, &item.NodeCount); err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		cursor := page.Items[limit-1].Revision
		page.NextBeforeRevision = &cursor
	}
	writeJSON(w, 200, page)
}
