package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type conversationCursor struct {
	Version                                                      int `json:"v"`
	User, Workspace, Project, Kind, Conversation, Leaf, BeforeID string
	CreatedAt                                                    time.Time
}

func chatCursorScope(r *http.Request, kind, conversation, leaf string) conversationCursor {
	p := principalFrom(r)
	u := "local"
	if p.User != nil {
		u = p.User.ID
	}
	return conversationCursor{Version: 1, User: u, Workspace: p.Workspace.ID, Project: p.Project.ID, Kind: kind, Conversation: conversation, Leaf: leaf}
}
func encodeChatCursor(c conversationCursor) string {
	blob, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(blob)
}
func parseChatCursor(raw string, scope conversationCursor) (conversationCursor, error) {
	if raw == "" {
		return scope, nil
	}
	bad := func() (conversationCursor, error) {
		return scope, failure(400, "INVALID_CHAT_CURSOR", "对话游标无效或不属于当前账户、项目和分支")
	}
	if len(raw) > 1024 {
		return bad()
	}
	blob, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return bad()
	}
	var cursor conversationCursor
	d := json.NewDecoder(bytes.NewReader(blob))
	d.DisallowUnknownFields()
	if d.Decode(&cursor) != nil {
		return bad()
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return bad()
	}
	if cursor.Version != 1 || cursor.User != scope.User || cursor.Workspace != scope.Workspace || cursor.Project != scope.Project || cursor.Kind != scope.Kind || cursor.Conversation != scope.Conversation || cursor.Leaf != scope.Leaf || cursor.BeforeID == "" || len(cursor.BeforeID) > 80 {
		return bad()
	}
	prefix := "msg"
	if cursor.Kind == "conversations" {
		prefix = "conv"
	}
	if !validChatID(cursor.BeforeID, prefix) {
		return bad()
	}
	if cursor.Kind != "messages" && cursor.CreatedAt.IsZero() {
		return bad()
	}
	return cursor, nil
}
func conversationLimit(r *http.Request, max int) (int, error) {
	n := 20
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > max {
			return 0, failure(400, "INVALID_CHAT_LIMIT", "分页数量超出允许范围")
		}
		n = parsed
	}
	return n, nil
}

func (a *App) postConversationHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input ConversationInput
	if err = decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	p := principalFrom(r)
	if !a.authorizeProject(p, input.ProjectID) || (r.Header.Get("X-Project-ID") != "" || r.URL.Query().Get("project_id") != "") && p.Project.ID != input.ProjectID {
		writeError(w, conversationNotFound())
		return
	}
	c, replay, err := a.createConversation(input, key, p.Workspace.ID, auditActorID(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 201, c)
}
func (a *App) postConversationRunHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input ConversationRunInput
	if err = decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	p := principalFrom(r)
	run, replay, err := a.createConversationRunContext(r.Context(), p.Workspace.ID, p.Project.ID, r.PathValue("id"), key, auditActorID(r), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 201, run)
}
func (a *App) getConversationHTTP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	a.mu.Lock()
	c, err := a.conversationLocked(p.Workspace.ID, p.Project.ID, r.PathValue("id"))
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (a *App) listConversationsHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := conversationLimit(r, 50)
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, err := parseChatCursor(r.URL.Query().Get("cursor"), chatCursorScope(r, "conversations", "", ""))
	if err != nil {
		writeError(w, err)
		return
	}
	p := principalFrom(r)
	items := []Conversation{}
	a.mu.Lock()
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var before *time.Time
			if cursor.BeforeID != "" {
				before = &cursor.CreatedAt
			}
			rows, e := conn.Query(ctx, "SELECT "+conversationColumns+" FROM studio_conversations c WHERE c.workspace_id=$1 AND c.project_id=$2 AND ($3::timestamptz IS NULL OR (c.created_at,c.id)<($3,$4)) ORDER BY c.created_at DESC,c.id DESC LIMIT $5", p.Workspace.ID, p.Project.ID, before, cursor.BeforeID, limit+1)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				c, e := scanConversation(rows)
				if e != nil {
					return e
				}
				items = append(items, c)
			}
			return rows.Err()
		})
	} else {
		for _, c := range a.conversations {
			if c.WorkspaceID != p.Workspace.ID || c.ProjectID != p.Project.ID {
				continue
			}
			if cursor.BeforeID != "" && (!c.CreatedAt.Before(cursor.CreatedAt) && (!c.CreatedAt.Equal(cursor.CreatedAt) || c.ID >= cursor.BeforeID)) {
				continue
			}
			c, _ = a.conversationLocked(p.Workspace.ID, p.Project.ID, c.ID)
			items = append(items, c)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].ID > items[j].ID
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	page := map[string]any{"items": items}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.BeforeID, cursor.CreatedAt = last.ID, last.CreatedAt
		page["items"] = items
		page["next_cursor"] = encodeChatCursor(cursor)
	}
	writeJSON(w, 200, page)
}

type conversationMessagePage struct {
	Items               []MessageNode `json:"items"`
	LeafMessageID       *string       `json:"leaf_message_id"`
	NextCursor          string        `json:"next_cursor,omitempty"`
	HasMore             bool          `json:"has_more"`
	ConversationVersion uint64        `json:"conversation_version"`
}

func (a *App) conversationMessagesHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := conversationLimit(r, 40)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := a.conversationMessagesPage(r, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (a *App) conversationMessagesPage(r *http.Request, limit int) (conversationMessagePage, error) {
	p := principalFrom(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.conversationLocked(p.Workspace.ID, p.Project.ID, r.PathValue("id"))
	if err != nil {
		return conversationMessagePage{}, err
	}
	leaf := r.URL.Query().Get("leaf_id")
	if leaf == "" {
		leaf = stringID(c.HeadMessageID)
	}
	if leaf != "" && !validChatID(leaf, "msg") {
		return conversationMessagePage{}, conversationNotFound()
	}
	cursor, err := parseChatCursor(r.URL.Query().Get("before"), chatCursorScope(r, "messages", c.ID, leaf))
	if err != nil {
		return conversationMessagePage{}, err
	}
	var path []chatNode
	records := map[string]Run{}
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var e error
			path, e = readChatPathSQL(ctx, conn, p.Workspace.ID, p.Project.ID, c.ID, leaf)
			if e != nil {
				return e
			}
			records, e = readChatRunMapSQL(ctx, conn, p.Workspace.ID, p.Project.ID, path)
			return e
		})
	} else {
		path, err = a.chatPathMemory(c, leaf)
		for _, n := range path {
			if n.RunID != nil {
				if run, ok := a.runs[*n.RunID]; ok {
					records[*n.RunID] = run.Run
				}
			}
		}
	}
	if err != nil {
		return conversationMessagePage{}, err
	}
	start := 0
	if cursor.BeforeID != "" {
		start = -1
		for i, n := range path {
			if n.ID == cursor.BeforeID {
				start = i
				break
			}
		}
		if start < 0 {
			return conversationMessagePage{}, failure(400, "INVALID_CHAT_CURSOR", "分页祖先不属于选中分支")
		}
	}
	page := conversationMessagePage{Items: []MessageNode{}, ConversationVersion: c.Version}
	if leaf != "" {
		page.LeafMessageID = &leaf
	}
	size := 4096
	load := func(id string) (Run, error) {
		run, ok := records[id]
		if !ok {
			return Run{}, conversationNotFound()
		}
		return run, nil
	}
	for i := start; i < len(path) && len(page.Items) < limit; i++ {
		node, e := hydrateChatNode(path[i], load)
		if e != nil {
			return conversationMessagePage{}, e
		}
		blob, e := json.Marshal(node)
		if e != nil {
			return conversationMessagePage{}, e
		}
		if size+len(blob)+1 > 1<<20 {
			if len(page.Items) == 0 {
				return conversationMessagePage{}, failure(413, "CHAT_PAGE_TOO_LARGE", "单条消息超过读取预算")
			}
			break
		}
		size += len(blob) + 1
		page.Items = append(page.Items, node)
	}
	next := start + len(page.Items)
	if next < len(path) {
		page.HasMore = true
		cursor.BeforeID = path[next].ID
		page.NextCursor = encodeChatCursor(cursor)
	}
	for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
		page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
	}
	return page, nil
}

type conversationBranch struct {
	Moderation      *ModerationSummary `json:"moderation,omitempty"`
	LeafMessageID   string             `json:"leaf_message_id"`
	ParentMessageID *string            `json:"parent_message_id"`
	RunID           *string            `json:"run_id"`
	Status          string             `json:"status"`
	Preview         string             `json:"preview"`
	CreatedAt       time.Time          `json:"created_at"`
}

func (a *App) conversationBranchesHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := conversationLimit(r, 50)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := a.conversationBranchesPage(r, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (a *App) conversationBranchesPage(r *http.Request, limit int) (map[string]any, error) {
	p := principalFrom(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.conversationLocked(p.Workspace.ID, p.Project.ID, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	cursor, err := parseChatCursor(r.URL.Query().Get("cursor"), chatCursorScope(r, "branches", c.ID, ""))
	if err != nil {
		return nil, err
	}
	items := []conversationBranch{}
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var before *time.Time
			if cursor.BeforeID != "" {
				before = &cursor.CreatedAt
			}
			rows, e := conn.Query(ctx, `SELECT m.id,m.parent_id,m.run_id,coalesce(r.document->>'status','COMPLETED'),left(CASE WHEN r.document->'moderation' IS NOT NULL AND r.document->'moderation'<>'null'::jsonb AND NOT(coalesce(r.document->'moderation'->>'state','')='ALLOWED' AND r.document->>'status'='COMPLETED') THEN '' ELSE coalesce(nullif(r.document->>'output',''),p.content,m.content) END,160),m.created_at,coalesce(r.document->'moderation','null'::jsonb) FROM studio_chat_messages m LEFT JOIN studio_runs r ON r.id=m.run_id LEFT JOIN studio_chat_messages p ON p.id=m.parent_id WHERE m.conversation_id=$1 AND m.workspace_id=$2 AND m.project_id=$3 AND NOT EXISTS(SELECT 1 FROM studio_chat_messages child WHERE child.parent_id=m.id AND child.conversation_id=m.conversation_id) AND ($4::timestamptz IS NULL OR (m.created_at,m.id)<($4,$5)) ORDER BY m.created_at DESC,m.id DESC LIMIT $6`, c.ID, p.Workspace.ID, p.Project.ID, before, cursor.BeforeID, limit+1)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var b conversationBranch
				var moderation []byte
				if e = rows.Scan(&b.LeafMessageID, &b.ParentMessageID, &b.RunID, &b.Status, &b.Preview, &b.CreatedAt, &moderation); e != nil {
					return e
				}
				if e = json.Unmarshal(moderation, &b.Moderation); e != nil {
					return e
				}
				items = append(items, b)
			}
			return rows.Err()
		})
	} else {
		children := map[string]bool{}
		for _, n := range a.chatMessages {
			if n.ConversationID == c.ID && n.ParentID != nil {
				children[*n.ParentID] = true
			}
		}
		for _, n := range a.chatMessages {
			if n.ConversationID != c.ID || children[n.ID] {
				continue
			}
			if cursor.BeforeID != "" && (!n.CreatedAt.Before(cursor.CreatedAt) && (!n.CreatedAt.Equal(cursor.CreatedAt) || n.ID >= cursor.BeforeID)) {
				continue
			}
			b := conversationBranch{LeafMessageID: n.ID, ParentMessageID: n.ParentID, RunID: n.RunID, Status: n.Status, Preview: n.Content, CreatedAt: n.CreatedAt}
			if n.RunID != nil {
				if run, ok := a.runs[*n.RunID]; ok {
					safe := copyRun(run.Run)
					b.Status, b.Preview, b.Moderation = safe.Status, safe.Output, safe.Moderation
				}
			}
			if b.Preview == "" && n.ParentID != nil && (b.Moderation == nil || b.Moderation.State == "ALLOWED") {
				b.Preview = a.chatMessages[*n.ParentID].Content
			}
			b.Preview = previewText(b.Preview)
			items = append(items, b)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].LeafMessageID > items[j].LeafMessageID
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	}
	if err != nil {
		return nil, err
	}
	page := map[string]any{"items": items}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.BeforeID, cursor.CreatedAt = last.LeafMessageID, last.CreatedAt
		page["items"] = items
		page["next_cursor"] = encodeChatCursor(cursor)
	}
	return page, nil
}
