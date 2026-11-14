package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /readyz", a.readyHTTP)
	mux.HandleFunc("GET /metrics", a.metricsHTTP)
	mux.HandleFunc("GET /api/v1/runtime", a.runtimeHTTP)
	mux.HandleFunc("GET /api/v1/bootstrap", a.bootstrap)
	mux.HandleFunc("GET /api/v1/models", a.modelsHTTP)
	mux.HandleFunc("GET /api/v1/operations/access", a.operationsAccessHTTP)
	mux.HandleFunc("GET /api/v1/operations/exceptions", a.operationsExceptionsHTTP)
	mux.HandleFunc("GET /api/v1/operations/cases", a.operationsCasesHTTP)
	mux.HandleFunc("POST /api/v1/operations/cases", a.operationsCreateCaseHTTP)
	mux.HandleFunc("GET /api/v1/operations/cases/{id}", a.operationsCaseHTTP)
	mux.HandleFunc("GET /api/v1/operations/cases/{id}/events", a.operationsCaseEventsHTTP)
	mux.HandleFunc("POST /api/v1/operations/cases/{id}/plans", a.operationsPlanHTTP)
	mux.HandleFunc("POST /api/v1/operations/cases/{id}/execute", a.operationsExecuteHTTP)
	mux.HandleFunc("GET /api/v1/billing/wallet", a.billingWalletHTTP)
	mux.HandleFunc("GET /api/v1/billing/entries", a.billingEntriesHTTP)
	mux.HandleFunc("GET /api/v1/billing/reservations", a.billingReservationsHTTP)
	mux.HandleFunc("POST /api/v1/billing/quotes", a.postBillingQuoteHTTP)
	mux.HandleFunc("GET /api/v1/conversations", a.listConversationsHTTP)
	mux.HandleFunc("POST /api/v1/conversations", a.postConversationHTTP)
	mux.HandleFunc("GET /api/v1/conversations/{id}", a.getConversationHTTP)
	mux.HandleFunc("POST /api/v1/conversations/{id}/runs", a.postConversationRunHTTP)
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", a.conversationMessagesHTTP)
	mux.HandleFunc("GET /api/v1/conversations/{id}/branches", a.conversationBranchesHTTP)
	mux.HandleFunc("GET /api/v1/library", a.listLibraryHTTP)
	mux.HandleFunc("GET /api/v1/library/{id}", a.getLibraryHTTP)
	mux.HandleFunc("PATCH /api/v1/library/{id}", a.mutateLibraryHTTP)
	mux.HandleFunc("POST /api/v1/library/{id}/trash", a.mutateLibraryHTTP)
	mux.HandleFunc("POST /api/v1/library/{id}/restore", a.mutateLibraryHTTP)
	mux.HandleFunc("GET /api/v1/library/{id}/revisions", a.listLibraryRevisionsHTTP)
	mux.HandleFunc("GET /api/v1/library/{id}/content", a.serveLibraryHTTP)
	mux.HandleFunc("GET /api/v1/library/{id}/thumbnail", a.serveLibraryHTTP)
	mux.HandleFunc("GET /api/v1/references", a.listReferences)
	mux.HandleFunc("POST /api/v1/references", a.postReference)
	mux.HandleFunc("GET /api/v1/references/{id}/content", a.serveReference)
	mux.HandleFunc("GET /api/v1/references/{id}/thumbnail", a.serveReference)
	mux.HandleFunc("GET /api/v1/audit", a.listAuditHTTP)
	mux.HandleFunc("POST /api/v1/auth/register", a.authRegister)
	mux.HandleFunc("POST /api/v1/auth/login", a.authLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", a.authLogout)
	mux.HandleFunc("GET /api/v1/auth/session", a.authSession)
	mux.HandleFunc("POST /api/v1/auth/email/verification/request", a.accountMailRequest)
	mux.HandleFunc("POST /api/v1/auth/email/verification/confirm", a.accountTokenConfirm)
	mux.HandleFunc("POST /api/v1/auth/password/reset/request", a.accountMailRequest)
	mux.HandleFunc("POST /api/v1/auth/password/reset/confirm", a.accountTokenConfirm)
	mux.HandleFunc("POST /api/v1/auth/password/change", a.accountPasswordChange)
	mux.HandleFunc("GET /api/v1/auth/sessions", a.accountSessions)
	mux.HandleFunc("POST /api/v1/auth/sessions/revoke-all", a.accountRevokeSessions)
	mux.HandleFunc("POST /api/v1/auth/sessions/{id}/revoke", a.accountRevokeSessions)
	mux.HandleFunc("GET /api/v1/auth/security/events", a.accountSecurityEvents)
	mux.HandleFunc("GET /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"items": principalFrom(r).Projects})
	})
	mux.HandleFunc("POST /api/v1/projects", a.postProject)
	mux.HandleFunc("GET /api/v1/projects/{project}/draft", a.getProjectDraft)
	mux.HandleFunc("PUT /api/v1/projects/{project}/draft", a.putProjectDraft)
	mux.HandleFunc("GET /api/v1/projects/{project}/draft/revisions", a.listProjectDraftRevisions)
	mux.HandleFunc("GET /api/v1/projects/{project}/draft/revisions/{revision}", a.getProjectDraftRevision)
	mux.HandleFunc("GET /api/v1/assets/{id}", a.serveAsset)
	mux.HandleFunc("GET /api/v1/runs", a.listRunsHTTP)
	mux.HandleFunc("POST /api/v1/runs", a.postRun)
	mux.HandleFunc("GET /api/v1/runs/{id}/snapshot", a.runSnapshot)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", a.runEvents)
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkRunAccess(r); err != nil {
			writeError(w, err)
			return
		}
		v, e := a.cancelRun(r.PathValue("id"), auditActorID(r))
		if e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/tasks", a.listTasksHTTP)
	mux.HandleFunc("POST /api/v1/tasks", a.postTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}", a.taskSnapshot)
	mux.HandleFunc("POST /api/v1/tasks/{id}/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkTaskAccess(r); err != nil {
			writeError(w, err)
			return
		}
		if r.ContentLength != 0 {
			var body struct{}
			if err := decodeJSON(w, r, &body); err != nil {
				writeError(w, err)
				return
			}
		}
		task, err := a.reconcileTask(r.PathValue("id"), auditActorID(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 202, task)
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := a.checkTaskAccess(r); err != nil {
			writeError(w, err)
			return
		}
		v, e := a.cancelTask(r.PathValue("id"), auditActorID(r))
		if e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/workspaces/{workspace}/events", a.workspaceEvents)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, failure(404, "NOT_FOUND", "API不存在")) })
	mux.HandleFunc("GET /demo-assets/{file}", serveDemoAsset)
	mux.HandleFunc("/", a.serveFrontend)
	return a.observeHTTP(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if !allowedHost(r.Host) {
			writeError(w, failure(403, "HOST_FORBIDDEN", "本地开发服务仅接受回环地址 Host"))
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, failure(403, "ORIGIN_FORBIDDEN", "不接受跨站请求"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !allowedOrigin(origin, r) {
			writeError(w, failure(403, "ORIGIN_FORBIDDEN", "请求来源不在同源或本地开发白名单"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if ready, state := a.readiness(); !ready {
				if state == "database_unavailable" {
					writeError(w, failure(503, "DATABASE_UNAVAILABLE", "数据库不可用，服务已停止执行；请恢复数据库后重启"))
				} else {
					writeError(w, failure(503, "SHUTTING_DOWN", "服务正在停止"))
				}
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			p, err := a.currentPrincipal(r)
			if err != nil {
				writeError(w, err)
				return
			}
			if p == nil {
				writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		}
		mux.ServeHTTP(w, r)
	}))
}

func allowedHost(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(name, "[]"))
	return ip != nil && ip.IsLoopback()
}

func allowedOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return origin == "http://127.0.0.1:5173" || origin == "http://localhost:5173"
}

func (a *App) bootstrap(w http.ResponseWriter, r *http.Request) {
	result := a.identityJSON(principalFrom(r))
	result["models"] = a.catalog.Public.Models
	result["catalog_version"] = a.catalog.Public.Version
	result["reference_limits"] = referenceLimits()
	result["mode"] = a.config.Mode
	result["storage"] = a.storageMode()
	result["capabilities"] = a.legacyCapabilities()
	result["billing"] = a.billingCapabilities()
	result["moderation"] = moderationCapabilities(a.moderation)
	writeJSON(w, 200, result)
}

const maxRequestBody = 64 << 10

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return failure(415, "JSON_REQUIRED", "请求必须为 application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return failure(413, "BODY_TOO_LARGE", "请求体超过64 KiB上限")
		}
		return failure(400, "INVALID_JSON", "请求JSON无效或包含未知字段")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return failure(413, "BODY_TOO_LARGE", "请求体超过64 KiB上限")
		}
		return failure(400, "INVALID_JSON", "请求体只能包含一个JSON对象")
	}
	return nil
}

func idempotencyKey(r *http.Request) (string, error) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		return "", failure(400, "IDEMPOTENCY_KEY_REQUIRED", "需要8至128位 Idempotency-Key；网络重试请复用原键")
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return "", failure(400, "INVALID_IDEMPOTENCY_KEY", "幂等键仅支持字母、数字及 . _ : -")
		}
	}
	return key, nil
}
func validatePrompt(prompt string) bool {
	return utf8.ValidString(prompt) && !strings.ContainsRune(prompt, 0) && strings.TrimSpace(prompt) != "" && len(prompt) <= 16000
}

func (a *App) postRun(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input RunInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if !a.authorizeProject(principalFrom(r), input.ProjectID) {
		writeError(w, failure(422, "INVALID_INPUT", "请使用有效项目、对话模型和1至16000字节非空提示词"))
		return
	}
	run, replayed, err := a.createRunContext(r.Context(), input, key, auditActorID(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 201, run)
}

func (a *App) postTask(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input TaskInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if !a.authorizeProject(principalFrom(r), input.ProjectID) {
		writeError(w, failure(422, "INVALID_INPUT", "请检查项目、媒体模型、提示词、画幅；图片数量1至4，视频数量1"))
		return
	}
	task, replayed, err := a.createTaskContext(r.Context(), input, key, auditActorID(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 202, task)
}

func (a *App) runSnapshot(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	record, err := a.getRunLocked(r.PathValue("id"))
	if err == nil && !resourceAllowed(r, record.Run.WorkspaceID, record.Run.ProjectID) {
		err = failure(404, "NOT_FOUND", "运行不存在")
	}
	var run Run
	if err == nil {
		run = copyRun(record.Run)
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, run)
}
func (a *App) taskSnapshot(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	record, err := a.getTaskLocked(r.PathValue("id"))
	if err == nil && !resourceAllowed(r, record.Task.WorkspaceID, record.Task.ProjectID) {
		err = failure(404, "NOT_FOUND", "任务不存在")
	}
	var task Task
	if err == nil {
		task = copyTask(record.Task)
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, task)
}

func (a *App) acquireStream(w http.ResponseWriter, r *http.Request) bool {
	p := principalFrom(r)
	if p == nil {
		writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
		return false
	}
	a.obs.streamMu.Lock()
	if a.obs.streamWorkspaces[p.Workspace.ID] >= a.config.Admission.WorkspaceStreams {
		a.obs.streamMu.Unlock()
		writeError(w, failure(429, "ADMISSION_STREAM_LIMIT", "当前工作区订阅连接已达上限，请关闭重复页面后重试"))
		return false
	}
	select {
	case a.streamSlots <- struct{}{}:
		if a.obs.streamWorkspaces == nil {
			a.obs.streamWorkspaces = make(map[string]int)
		}
		a.obs.streamWorkspaces[p.Workspace.ID]++
		a.obs.streamMu.Unlock()
		return true
	default:
		a.obs.streamMu.Unlock()
		writeError(w, failure(503, "STREAM_LIMIT", "本地并发订阅连接已达上限"))
		return false
	}
}

func (a *App) runEvents(w http.ResponseWriter, r *http.Request) {
	afterText := r.Header.Get("Last-Event-ID")
	if afterText == "" {
		afterText = r.URL.Query().Get("after")
	}
	var after uint64
	if afterText != "" {
		v, err := strconv.ParseUint(afterText, 10, 64)
		if err != nil {
			writeError(w, failure(400, "INVALID_CURSOR", "事件游标必须为非负整数"))
			return
		}
		after = v
	}
	if !a.acquireStream(w, r) {
		return
	}
	defer a.releaseObservedStream(principalFrom(r).Workspace.ID)
	a.mu.Lock()
	record, err := a.getRunLocked(r.PathValue("id"))
	if err == nil && !resourceAllowed(r, record.Run.WorkspaceID, record.Run.ProjectID) {
		err = failure(404, "NOT_FOUND", "运行不存在")
	}
	if err != nil {
		a.mu.Unlock()
		writeError(w, err)
		return
	}
	if after > record.Run.Seq {
		a.mu.Unlock()
		writeError(w, failure(409, "CURSOR_AHEAD", "事件游标超出当前运行，请重新获取快照"))
		return
	}
	replay := make([]event, 0, len(record.Events))
	isTerminal := terminal(record.Run.Status)
	if record.Run.Moderation != nil || len(record.Events) == 0 && record.Run.Seq > 0 || len(record.Events) > 0 && after < record.Events[record.EventStart].Seq-1 || isTerminal && after == record.Run.Seq {
		data, _ := json.Marshal(copyRun(record.Run))
		replay = append(replay, event{Seq: record.Run.Seq, Name: "run.snapshot", Data: data})
	} else {
		for i := range record.Events {
			e := record.Events[(record.EventStart+i)%len(record.Events)]
			if e.Seq > after {
				replay = append(replay, e)
			}
		}
	}
	ch := make(chan event, a.config.SubscriberBuffer)
	if !isTerminal {
		record.Subscribers[ch] = true
	}
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(record.Subscribers, ch); a.mu.Unlock() }()
	if !startSSE(w) {
		return
	}
	for _, e := range replay {
		if !writeSSE(w, e) {
			return
		}
	}
	if isTerminal {
		return
	}
	a.streamLoop(w, r, ch, true)
}

func (a *App) workspaceEvents(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if r.PathValue("workspace") != p.Workspace.ID {
		writeError(w, failure(404, "NOT_FOUND", "工作区不存在"))
		return
	}
	if a.store != nil {
		a.durableWorkspaceEvents(w, r)
		return
	}
	if !a.acquireStream(w, r) {
		return
	}
	defer a.releaseObservedStream(p.Workspace.ID)
	ch := make(chan event, a.config.SubscriberBuffer)
	a.mu.Lock()
	a.workspaceSubscribers[ch] = subscriptionScope{WorkspaceID: p.Workspace.ID, ProjectID: p.Project.ID}
	initial := make([]event, 0, len(a.tasks))
	for _, t := range a.tasks {
		if t.Task.WorkspaceID != p.Workspace.ID || t.Task.ProjectID != p.Project.ID {
			continue
		}
		data, _ := json.Marshal(map[string]any{"task": t.Task})
		initial = append(initial, event{Name: "task.updated", Data: data})
	}
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.workspaceSubscribers, ch); a.mu.Unlock() }()
	if !startSSE(w) {
		return
	}
	for _, e := range initial {
		if !writeSSE(w, e) {
			return
		}
	}
	a.streamLoop(w, r, ch, false)
}

func startSSE(w http.ResponseWriter) bool {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return false
	}
	return controller.Flush() == nil
}
func writeSSE(w http.ResponseWriter, e event) bool {
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if e.Cursor != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", e.Cursor); err != nil {
			return false
		}
	} else if e.Seq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", e.Seq); err != nil {
			return false
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, e.Data); err != nil {
		return false
	}
	return controller.Flush() == nil
}
func (a *App) streamLoop(w http.ResponseWriter, r *http.Request, ch <-chan event, stopAtTerminal bool) {
	ticker := time.NewTicker(a.config.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case e, ok := <-ch:
			if !ok || !writeSSE(w, e) {
				return
			}
			if stopAtTerminal && strings.HasPrefix(e.Name, "run.") {
				return
			}
		case <-ticker.C:
			if !a.sessionStillValid(r) {
				return
			}
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}

func resourceAllowed(r *http.Request, workspaceID, projectID string) bool {
	p := principalFrom(r)
	if p == nil || p.Workspace.ID != workspaceID {
		return false
	}
	if r.Header.Get("X-Project-ID") != "" || r.URL.Query().Get("project_id") != "" {
		return p.Project.ID == projectID
	}
	return true
}
func (a *App) checkRunAccess(r *http.Request) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, err := a.getRunLocked(r.PathValue("id"))
	if err != nil {
		return err
	}
	if !resourceAllowed(r, record.Run.WorkspaceID, record.Run.ProjectID) {
		return failure(404, "NOT_FOUND", "运行不存在")
	}
	return nil
}
func (a *App) checkTaskAccess(r *http.Request) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, err := a.getTaskLocked(r.PathValue("id"))
	if err != nil {
		return err
	}
	if !resourceAllowed(r, record.Task.WorkspaceID, record.Task.ProjectID) {
		return failure(404, "NOT_FOUND", "任务不存在")
	}
	return nil
}
func (a *App) serveAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := principalFrom(r)
	projectID := ""
	if r.Header.Get("X-Project-ID") != "" || r.URL.Query().Get("project_id") != "" {
		projectID = p.Project.ID
	}
	a.mu.Lock()
	allowed, err := a.legacyAssetAllowedLocked(p.Workspace.ID, projectID, id)
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	if !allowed {
		writeError(w, failure(404, "NOT_FOUND", "资产不存在"))
		return
	}
	a.assets.ServeAsset(w, r, id)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	var operation *operationError
	if !errors.As(err, &operation) {
		operation = &operationError{status: 500, code: "INTERNAL_ERROR", message: "服务器内部错误"}
	}
	markObservedError(w, operation.code)
	switch operation.code {
	case "ADMISSION_WORKSPACE_LIMIT", "ADMISSION_MODEL_LIMIT", "ADMISSION_STREAM_LIMIT", "STREAM_LIMIT", "REFERENCE_UPLOAD_BUSY", "REFERENCE_PREPARATION_BUSY":
		w.Header().Set("Retry-After", "3")
	}
	writeJSON(w, operation.status, map[string]any{"error": map[string]string{"code": operation.code, "message": operation.message, "request_id": w.Header().Get("X-Request-ID")}})
}

func serveDemoAsset(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	palette := map[string][2]string{"image-1.svg": {"#7d58ff", "#f6a3d5"}, "image-2.svg": {"#35c5b3", "#ffce82"}, "image-3.svg": {"#455ee8", "#a0dcff"}, "image-4.svg": {"#f29d67", "#a05bbb"}, "video-poster.svg": {"#9a6bff", "#31458b"}}
	colors, ok := palette[file]
	if !ok {
		http.NotFound(w, r)
		return
	}
	label := "LOCAL DEMO / CONCEPT IMAGE"
	symbol := ""
	if file == "video-poster.svg" {
		label = "SIMULATED VIDEO POSTER / NOT A VIDEO"
		symbol = `<circle cx="600" cy="330" r="44" fill="#fff" fill-opacity=".16"/><path d="M590 307 L620 330 L590 353 Z" fill="#fff"/>`
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	_, _ = fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 750"><defs><linearGradient id="g" x2="1" y2="1"><stop stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient><filter id="b"><feGaussianBlur stdDeviation="55"/></filter></defs><rect width="1200" height="750" fill="#121323"/><circle cx="420" cy="270" r="260" fill="url(#g)" opacity=".8" filter="url(#b)"/><ellipse cx="780" cy="420" rx="270" ry="150" fill="url(#g)" opacity=".6" filter="url(#b)"/><path d="M-60 600 Q400 200 1260 560 L1260 850 L-60 850Z" fill="#17192e" opacity=".75"/><path d="M-60 670 Q550 370 1260 620" stroke="#fff" stroke-opacity=".17" fill="none"/><circle cx="915" cy="180" r="58" fill="#fff" opacity=".4"/>%s<text x="60" y="675" fill="#fff" font-family="sans-serif" font-size="16" letter-spacing="3">%s</text><text x="60" y="710" fill="#ccc" font-family="sans-serif" font-size="12">LOCAL SVG ARTWORK · NO MODEL GENERATION</text></svg>`, colors[0], colors[1], symbol, label)
}

func (a *App) serveFrontend(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api") || strings.HasPrefix(r.URL.Path, "/demo-assets") {
		writeError(w, failure(404, "NOT_FOUND", "资源不存在"))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, failure(405, "METHOD_NOT_ALLOWED", "请求方法不支持"))
		return
	}
	for _, part := range strings.Split(strings.ReplaceAll(r.URL.Path, "\\", "/"), "/") {
		if strings.HasPrefix(part, ".") {
			http.NotFound(w, r)
			return
		}
	}
	root, err := filepath.EvalSymlinks(a.config.WebDir)
	if err != nil {
		writeError(w, failure(404, "FRONTEND_NOT_BUILT", "Go API已运行；请启动Vite或先构建前端"))
		return
	}
	root, err = filepath.Abs(root)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(path.Clean(r.URL.Path), "/")))
	info, err := os.Stat(name)
	if err != nil || info.IsDir() {
		if path.Ext(r.URL.Path) != "" {
			http.NotFound(w, r)
			return
		}
		name = filepath.Join(root, "index.html")
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
	if filepath.Base(resolved) == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
