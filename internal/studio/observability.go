package studio

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Every label is selected from a compile-time vocabulary. Neither raw URLs,
// methods, model configuration, identities nor error messages become labels.
var observedRoutes = [...]string{"other", "health", "ready", "metrics", "runtime", "bootstrap", "audit", "register", "login", "logout", "session", "projects", "draft", "draft_history", "draft_revision", "asset", "runs", "run_snapshot", "run_events", "run_cancel", "tasks", "task_snapshot", "task_cancel", "task_reconcile", "workspace_events", "demo_asset", "frontend", "models", "references", "reference_content", "reference_thumbnail", "library", "library_entry", "library_revisions", "library_trash", "library_restore", "library_content", "library_thumbnail", "conversations", "conversation", "conversation_messages", "conversation_branches", "conversation_runs", "email_verification_request", "email_verification_confirm", "password_reset_request", "password_reset_confirm", "password_change", "account_sessions", "account_session_revoke", "account_sessions_revoke_all", "account_security_events", "billing_wallet", "billing_entries", "billing_reservations", "billing_quotes", "operations_access", "operations_exceptions", "operations_cases", "operations_case", "operations_case_events", "operations_case_plan", "operations_case_execute", "generation_access", "generation_appeal_create", "generation_appeals", "generation_appeal", "operations_generation_access", "operations_appeals", "operations_appeal", "operations_generation_plan", "operations_generation_execute", "operations_generation_receipt"}
var observedMethods = [...]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "other"}
var observedStatuses = [...]string{"2xx", "3xx", "4xx", "5xx", "other"}
var observedErrors = [...]string{"none", "validation", "authentication", "forbidden", "not_found", "conflict", "admission", "database", "unavailable", "upstream", "internal", "other"}
var durationBounds = [...]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 15, 60}

var routeNames = map[string]int{
	"GET /api/v1/workspace/generation-access":              63,
	"POST /api/v1/restrictions/{id}/appeals":               64,
	"GET /api/v1/appeals":                                  65,
	"GET /api/v1/appeals/{id}":                             66,
	"GET /api/v1/operations/generation-access":             67,
	"GET /api/v1/operations/appeals":                       68,
	"GET /api/v1/operations/appeals/{id}":                  69,
	"POST /api/v1/operations/generation-plans":             70,
	"POST /api/v1/operations/generation-executions":        71,
	"GET /api/v1/operations/generation-plans/{id}/receipt": 72,

	"GET /api/v1/operations/access": 56, "GET /api/v1/operations/exceptions": 57,
	"GET /api/v1/operations/cases": 58, "POST /api/v1/operations/cases": 58,
	"GET /api/v1/operations/cases/{id}": 59, "GET /api/v1/operations/cases/{id}/events": 60,
	"POST /api/v1/operations/cases/{id}/plans": 61, "POST /api/v1/operations/cases/{id}/execute": 62,
	"GET /api/v1/billing/wallet": 52, "GET /api/v1/billing/entries": 53,
	"GET /api/v1/billing/reservations": 54, "POST /api/v1/billing/quotes": 55,
	"GET /healthz": 1, "GET /readyz": 2, "GET /metrics": 3,
	"GET /api/v1/runtime": 4, "GET /api/v1/bootstrap": 5, "GET /api/v1/audit": 6,
	"POST /api/v1/auth/register": 7, "POST /api/v1/auth/login": 8, "POST /api/v1/auth/logout": 9, "GET /api/v1/auth/session": 10,
	"GET /api/v1/projects": 11, "POST /api/v1/projects": 11,
	"GET /api/v1/projects/{project}/draft": 12, "PUT /api/v1/projects/{project}/draft": 12,
	"GET /api/v1/projects/{project}/draft/revisions": 13, "GET /api/v1/projects/{project}/draft/revisions/{revision}": 14,
	"GET /api/v1/assets/{id}": 15, "GET /api/v1/runs": 16, "POST /api/v1/runs": 16,
	"GET /api/v1/runs/{id}/snapshot": 17, "GET /api/v1/runs/{id}/events": 18, "POST /api/v1/runs/{id}/cancel": 19,
	"GET /api/v1/tasks": 20, "POST /api/v1/tasks": 20, "GET /api/v1/tasks/{id}": 21,
	"POST /api/v1/tasks/{id}/cancel": 22, "POST /api/v1/tasks/{id}/reconcile": 23,
	"GET /api/v1/workspaces/{workspace}/events": 24, "GET /demo-assets/{file}": 25, "/": 26,
	"GET /api/v1/models":     27,
	"GET /api/v1/references": 28, "POST /api/v1/references": 28,
	"GET /api/v1/references/{id}/content": 29, "GET /api/v1/references/{id}/thumbnail": 30,
	"GET /api/v1/library": 31, "GET /api/v1/library/{id}": 32, "PATCH /api/v1/library/{id}": 32,
	"GET /api/v1/library/{id}/revisions": 33, "POST /api/v1/library/{id}/trash": 34, "POST /api/v1/library/{id}/restore": 35,
	"GET /api/v1/library/{id}/content": 36, "GET /api/v1/library/{id}/thumbnail": 37,
	"GET /api/v1/conversations": 38, "POST /api/v1/conversations": 38, "GET /api/v1/conversations/{id}": 39,
	"GET /api/v1/conversations/{id}/messages": 40, "GET /api/v1/conversations/{id}/branches": 41, "POST /api/v1/conversations/{id}/runs": 42,
	"POST /api/v1/auth/email/verification/request": 43, "POST /api/v1/auth/email/verification/confirm": 44,
	"POST /api/v1/auth/password/reset/request": 45, "POST /api/v1/auth/password/reset/confirm": 46,
	"POST /api/v1/auth/password/change": 47, "GET /api/v1/auth/sessions": 48,
	"POST /api/v1/auth/sessions/{id}/revoke": 49, "POST /api/v1/auth/sessions/revoke-all": 50,
	"GET /api/v1/auth/security/events": 51,
}

type observedDuration struct {
	count   uint64
	nanos   uint64
	buckets [len(durationBounds)]uint64
}

// A mutex yields coherent cumulative histogram snapshots. Storage is fixed,
// except streamWorkspaces, whose size cannot exceed the global 128 SSE slots.
type observability struct {
	mu                       sync.Mutex
	requests                 [len(observedRoutes)][len(observedMethods)][len(observedStatuses)]uint64
	durations                [len(observedRoutes)][len(observedMethods)]observedDuration
	errors                   [len(observedErrors)]uint64
	streamMu                 sync.Mutex
	streamWorkspaces         map[string]int
	slowConsumers            atomic.Uint64
	slowWrites               atomic.Uint64
	creditRiskHeldRejections atomic.Uint64
	creditRiskRateRejections atomic.Uint64
	durableEvents            [2]atomic.Uint64
	taskLeases               [4]atomic.Uint64
	logger                   *slog.Logger // Fixed before serving; tests can capture safe structured logs.
}

func validateObservabilityConfig(c Config) error {
	if c.MetricsToken == "" {
		return nil
	}
	if len(c.MetricsToken) < 32 || len(c.MetricsToken) > 256 {
		return errors.New("METRICS_TOKEN must contain 32 to 256 visible ASCII characters")
	}
	for _, ch := range c.MetricsToken {
		if ch < 33 || ch > 126 {
			return errors.New("METRICS_TOKEN must contain only visible ASCII characters without spaces")
		}
	}
	if c.MetricsToken == c.APIKey || c.MetricsToken == c.AsyncImageAPIKey || c.MetricsToken == c.AssetStorage.SecretAccessKey || c.MetricsToken == c.AssetStorage.SessionToken || c.MetricsToken == c.Mail.Password || c.MetricsToken == c.Mail.OutboxKey {
		return errors.New("METRICS_TOKEN must be independent of service credentials")
	}
	return nil
}

func observedMethod(method string) int {
	for i, known := range observedMethods[:len(observedMethods)-1] {
		if method == known {
			return i
		}
	}
	return len(observedMethods) - 1
}
func observedStatus(status int) int {
	if status >= 200 && status < 600 {
		return status/100 - 2
	}
	return len(observedStatuses) - 1
}
func observedError(status int, code string) int {
	switch code {
	case "DATABASE_UNAVAILABLE":
		return 7
	case "ADMISSION_WORKSPACE_LIMIT", "ADMISSION_MODEL_LIMIT", "ADMISSION_STREAM_LIMIT", "STREAM_LIMIT", "QUEUE_FULL", "RUN_QUEUE_FULL", "EXECUTION_WORKSPACE_QUEUE_LIMIT":
		return 6
	}
	switch status {
	case 400, 413, 415, 422:
		return 1
	case 401:
		return 2
	case 403:
		return 3
	case 404, 405:
		return 4
	case 409:
		return 5
	case 429:
		return 6
	case 503, 507:
		return 8
	case 502, 504:
		return 9
	}
	if status >= 500 {
		return 10
	}
	if status >= 400 {
		return 11
	}
	return 0
}

func (o *observability) record(route, method, status, errorClass int, elapsed time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests[route][method][observedStatus(status)]++
	d := &o.durations[route][method]
	d.count++
	d.nanos += uint64(max(elapsed, 0))
	for i, bound := range durationBounds {
		if elapsed.Seconds() <= bound {
			d.buckets[i]++
		}
	}
	if errorClass != 0 {
		o.errors[errorClass]++
	}
}

type observedWriter struct {
	http.ResponseWriter
	status        int
	errorCode     string
	writeTimedOut bool
}

func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 || status == 101 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *observedWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.observeWriteError(err)
	return n, err
}

// Preserve net/http's ReaderFrom fast path used by ServeContent/io.CopyN for
// large files. Do not pre-flush headers: the original writer owns sniffing and
// transfer selection. The fallback hides ReaderFrom to avoid io.Copy recursion.
func (w *observedWriter) ReadFrom(reader io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	var n int64
	var err error
	if fast, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = fast.ReadFrom(reader)
	} else {
		n, err = io.Copy(struct{ io.Writer }{w}, reader)
	}
	w.observeWriteError(err)
	return n, err
}
func (w *observedWriter) Flush() { _ = w.FlushError() }
func (w *observedWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	w.observeWriteError(err)
	return err
}
func (w *observedWriter) observeWriteError(err error) {
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		w.writeTimedOut = true
	}
}

func markObservedError(w http.ResponseWriter, code string) {
	for {
		if observed, ok := w.(*observedWriter); ok {
			observed.errorCode = code
			return
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = unwrapper.Unwrap()
	}
}

func (a *App) observeHTTP(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		_, pattern := mux.Handler(r)
		route, method := routeNames[pattern], observedMethod(r.Method)
		requestID := newID("req")
		w.Header().Set("X-Request-ID", requestID)
		recorder := &observedWriter{ResponseWriter: w}
		defer func() {
			status := recorder.status
			if status == 0 {
				status = 200
			}
			elapsed := time.Since(started)
			class := observedError(status, recorder.errorCode)
			a.obs.record(route, method, status, class, elapsed)
			switch recorder.errorCode {
			case "CREDIT_RISK_HELD_LIMIT":
				a.obs.creditRiskHeldRejections.Add(1)
			case "CREDIT_RISK_RATE_LIMIT":
				a.obs.creditRiskRateRejections.Add(1)
			}
			if recorder.writeTimedOut {
				a.obs.slowWrites.Add(1)
			}
			logger := a.obs.logger
			if logger == nil {
				logger = slog.Default()
			}
			level := slog.LevelDebug
			if status >= 500 {
				level = slog.LevelWarn
			}
			logger.Log(r.Context(), level, "http_request", "request_id", requestID, "route", observedRoutes[route], "method", observedMethods[method], "status", status, "error_class", observedErrors[class], "duration_ms", float64(elapsed.Microseconds())/1000)
		}()
		next.ServeHTTP(recorder, r)
	})
}

// Cached readiness is a last-observed state, not a synchronous connectivity
// check. The database watchdog and actual SQL operations trip failed closed.
func (a *App) readiness() (bool, string) {
	if a.store != nil && a.store.failed.Load() {
		return false, "database_unavailable"
	}
	if a.ctx.Err() != nil {
		return false, "stopping"
	}
	return true, "ready"
}
func (a *App) readyHTTP(w http.ResponseWriter, _ *http.Request) {
	ready, state := a.readiness()
	status := 200
	if !ready {
		status = 503
	}
	writeJSON(w, status, map[string]any{"ready": ready, "state": state})
}

func (a *App) scopedStreams(workspaceID string) int {
	a.obs.streamMu.Lock()
	defer a.obs.streamMu.Unlock()
	return a.obs.streamWorkspaces[workspaceID]
}
func (a *App) releaseObservedStream(workspaceID string) {
	a.obs.streamMu.Lock()
	defer a.obs.streamMu.Unlock()
	<-a.streamSlots
	if a.obs.streamWorkspaces[workspaceID] <= 1 {
		delete(a.obs.streamWorkspaces, workspaceID)
	} else {
		a.obs.streamWorkspaces[workspaceID]--
	}
}

func (a *App) runtimeHTTP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if p == nil {
		writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
		return
	}
	snapshot, err := a.admissionSnapshot(p.Workspace.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	ready, state := a.readiness()
	if !ready {
		writeError(w, failure(503, "SERVICE_UNAVAILABLE", "服务当前不可接收新的执行请求"))
		return
	}
	writeJSON(w, 200, map[string]any{"scope": "workspace", "ready": ready, "state": state, "runs": snapshot.Runs, "tasks": snapshot.Tasks, "streams": map[string]int{"active": a.scopedStreams(p.Workspace.ID), "limit": a.config.Admission.WorkspaceStreams}})
}

func (a *App) metricsHTTP(w http.ResponseWriter, r *http.Request) {
	if a.config.MetricsToken == "" {
		writeError(w, failure(404, "NOT_FOUND", "资源不存在"))
		return
	}
	header := r.Header.Get("Authorization")
	provided, ok := strings.CutPrefix(header, "Bearer ")
	expectedHash, providedHash := sha256.Sum256([]byte(a.config.MetricsToken)), sha256.Sum256([]byte(provided))
	if !ok || len(provided) > 256 || subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		writeError(w, failure(401, "METRICS_AUTH_REQUIRED", "需要独立运维令牌"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(a.prometheusText()))
}

func (a *App) prometheusText() string {
	var out strings.Builder
	a.obs.mu.Lock()
	requests, durations, errors := a.obs.requests, a.obs.durations, a.obs.errors
	a.obs.mu.Unlock()
	metricHeader(&out, "studio_http_requests_total", "Completed HTTP requests; SSE counted when the connection ends.", "counter")
	for route, methods := range requests {
		for method, statuses := range methods {
			for status, count := range statuses {
				if count > 0 {
					fmt.Fprintf(&out, "studio_http_requests_total{route=%q,method=%q,status=%q} %d\n", observedRoutes[route], observedMethods[method], observedStatuses[status], count)
				}
			}
		}
	}
	metricHeader(&out, "studio_http_request_duration_seconds", "Time until the handler returns; includes the complete SSE connection lifetime.", "histogram")
	for route, methods := range durations {
		for method, d := range methods {
			if d.count == 0 {
				continue
			}
			labels := fmt.Sprintf("route=%q,method=%q", observedRoutes[route], observedMethods[method])
			for i, bound := range durationBounds {
				fmt.Fprintf(&out, "studio_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(bound, 'g', -1, 64), d.buckets[i])
			}
			fmt.Fprintf(&out, "studio_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\nstudio_http_request_duration_seconds_sum{%s} %s\nstudio_http_request_duration_seconds_count{%s} %d\n", labels, d.count, labels, strconv.FormatFloat(float64(d.nanos)/1e9, 'g', -1, 64), labels, d.count)
		}
	}
	metricHeader(&out, "studio_http_errors_total", "HTTP errors grouped into fixed classifications, without messages or raw codes.", "counter")
	for class, count := range errors {
		if class != 0 {
			fmt.Fprintf(&out, "studio_http_errors_total{error_class=%q} %d\n", observedErrors[class], count)
		}
	}
	metricHeader(&out, "studio_credit_risk_rejections_total", "Paid admission requests rejected by the primary workspace credit risk rule.", "counter")
	fmt.Fprintf(&out, "studio_credit_risk_rejections_total{rule=\"held_limit\"} %d\nstudio_credit_risk_rejections_total{rule=\"rate_limit\"} %d\n", a.obs.creditRiskHeldRejections.Load(), a.obs.creditRiskRateRejections.Load())
	metricHeader(&out, "studio_active_streams", "Currently occupied global SSE admission slots.", "gauge")
	fmt.Fprintf(&out, "studio_active_streams %d\n", len(a.streamSlots))
	var sessionRounds, sessionsChecked uint64
	if a.streamSessions != nil {
		sessionRounds = a.streamSessions.rounds.Load()
		sessionsChecked = a.streamSessions.checked.Load()
	}
	metricHeader(&out, "studio_stream_session_rounds_total", "Completed batched SSE session validation rounds.", "counter")
	fmt.Fprintf(&out, "studio_stream_session_rounds_total %d\n", sessionRounds)
	metricHeader(&out, "studio_stream_sessions_checked_total", "Distinct sessions submitted for checking, summed over completed rounds.", "counter")
	fmt.Fprintf(&out, "studio_stream_sessions_checked_total %d\n", sessionsChecked)
	metricHeader(&out, "studio_slow_consumers_total", "Subscribers disconnected because a bounded event buffer filled or an HTTP write timed out.", "counter")
	fmt.Fprintf(&out, "studio_slow_consumers_total{reason=\"buffer_full\"} %d\nstudio_slow_consumers_total{reason=\"write_timeout\"} %d\n", a.obs.slowConsumers.Load(), a.obs.slowWrites.Load())
	a.obs.writeExecutionMetrics(&out)
	a.runDeltaBatch.writeMetrics(&out)
	writeMediaSchedulingMetrics(&out, a.mediaExecution.snapshot())
	writeMediaFairnessMetrics(&out, a.mediaScheduler.snapshot(), a.mediaModeration.permitSnapshot())
	ready, _ := a.readiness()
	readyValue := 0
	if ready {
		readyValue = 1
	}
	metricHeader(&out, "studio_ready", "Last-observed process readiness, without probing external providers.", "gauge")
	fmt.Fprintf(&out, "studio_ready %d\n", readyValue)
	a.mu.Lock()
	counts, _ := a.admissionCountsLocked("") // Explicitly the bounded local cache, never a global SQL scan.
	scheduling := a.execution.snapshotLocked(time.Now())
	a.writeManagedChatMetricsLocked(&out)
	a.mu.Unlock()
	writeChatSchedulingMetrics(&out, scheduling)
	type count struct {
		active, queued int
		oldest         time.Time
	}
	var cached [6]count
	for _, item := range counts {
		index := -1
		switch item.ModelID {
		case "studio-chat":
			index = 0
		case "studio-image":
			index = 1
		case "studio-video":
			index = 2
		}
		// User-configured and historical model IDs must count without becoming
		// unbounded or sensitive labels. Keep the three legacy series stable.
		if index < 0 {
			switch item.Kind {
			case "chat":
				index = 3
			case "image":
				index = 4
			case "video":
				index = 5
			}
		}
		if index < 0 {
			continue
		}
		cached[index].active += item.Active
		cached[index].queued += item.Queued
		if !item.OldestQueued.IsZero() && (cached[index].oldest.IsZero() || item.OldestQueued.Before(cached[index].oldest)) {
			cached[index].oldest = item.OldestQueued
		}
	}
	models := [...]string{"studio-chat", "studio-image", "studio-video", "other", "other", "other"}
	kinds := [...]string{"chat", "image", "video", "chat", "image", "video"}
	metricHeader(&out, "studio_cached_active_resources", "Nonterminal resources in the bounded process cache; evicted parked database records are excluded.", "gauge")
	for i, item := range cached {
		fmt.Fprintf(&out, "studio_cached_active_resources{kind=%q,model=%q} %d\n", kinds[i], models[i], item.active)
	}
	metricHeader(&out, "studio_cached_queued_resources", "QUEUED resources in the bounded process cache, grouped by fixed platform models.", "gauge")
	for i, item := range cached {
		fmt.Fprintf(&out, "studio_cached_queued_resources{kind=%q,model=%q} %d\n", kinds[i], models[i], item.queued)
	}
	metricHeader(&out, "studio_cached_oldest_queued_seconds", "Age of the oldest QUEUED resource in the bounded process cache.", "gauge")
	for i, item := range cached {
		age := 0.0
		if !item.oldest.IsZero() {
			age = max(0, time.Since(item.oldest).Seconds())
		}
		fmt.Fprintf(&out, "studio_cached_oldest_queued_seconds{kind=%q,model=%q} %s\n", kinds[i], models[i], strconv.FormatFloat(age, 'g', -1, 64))
	}
	return out.String()
}

func metricHeader(out *strings.Builder, name, help, kind string) {
	fmt.Fprintf(out, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}
