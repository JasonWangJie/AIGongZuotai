package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type admissionCapacityProfile struct {
	tasks, workspaces int
	budget            time.Duration
}

func admissionCapacityTier(value string) (admissionCapacityProfile, bool) {
	switch value {
	case "128":
		return admissionCapacityProfile{128, 4, 4 * time.Minute}, true
	case "512":
		return admissionCapacityProfile{512, 4, 15 * time.Minute}, true
	case "4096":
		return admissionCapacityProfile{4096, 4, time.Hour}, true
	case "60000":
		return admissionCapacityProfile{60000, 16, 8 * time.Hour}, true
	}
	return admissionCapacityProfile{}, false
}

// Only literal loopback PostgreSQL URLs are permitted, including after pgx
// environment resolution. Do not accept libpq host overrides or alternate hosts.
func admissionCapacityLocalDSN(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || !net.ParseIP(u.Hostname()).IsLoopback() {
		return false
	}
	for key := range u.Query() {
		if key != "sslmode" && key != "connect_timeout" {
			return false
		}
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil || cfg.Host != u.Hostname() {
		return false
	}
	for _, alt := range cfg.Fallbacks {
		if alt.Host != cfg.Host {
			return false
		}
	}
	return true
}

type admissionCapacityWireRow struct {
	key, hash, kind string
	gets            int
}
type admissionCapacityWire struct {
	mu                   sync.Mutex
	rows                 []admissionCapacityWireRow
	posts, gets, invalid int
}

func admissionCapacityIndex(token, prefix string, total int) (int, bool) {
	s, ok := strings.CutPrefix(token, prefix)
	i, err := strconv.Atoi(s)
	return i, ok && err == nil && i >= 0 && i < total && strconv.Itoa(i) == s
}

func (p *admissionCapacityWire) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var payload struct{ Prompt, Model string }
	var original []byte
	var err error
	if r.Method == http.MethodPost {
		original, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
		if err == nil {
			err = json.Unmarshal(original, &payload)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	reject := func() { p.invalid++; w.WriteHeader(http.StatusBadRequest) }
	if err != nil || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey || r.URL.RawQuery != "" {
		reject()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodPost:
		p.posts++
		i, ok := admissionCapacityIndex(payload.Prompt, "fixture246-", len(p.rows))
		kind := ""
		switch r.URL.Path {
		case imageGenerationEndpoint:
			kind = "image"
		case videoGenerationEndpoint:
			kind = "video"
		}
		key := r.Header.Get("Idempotency-Key")
		if !ok || kind == "" || payload.Model != "fixture246-"+kind || key == "" || p.rows[i].key != "" {
			reject()
			return
		}
		digest := sha256.Sum256(original)
		p.rows[i] = admissionCapacityWireRow{key: key, hash: hex.EncodeToString(digest[:]), kind: kind}
		w.WriteHeader(http.StatusAccepted)
		if i == 0 {
			// The POST reached the gateway but its accepted reply has no handle.
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprintf(w, `{"task_id":"fixture246-%d"}`, i)
	case http.MethodGet:
		p.gets++
		i, ok := admissionCapacityIndex(r.URL.Path, mediaQueryPrefix+"fixture246-", len(p.rows))
		if !ok || i == 0 || p.rows[i].key == "" {
			reject()
			return
		}
		p.rows[i].gets++
		// One sentinel stays due periodically so restart is proven by an
		// actual GET. Other tasks remain running without repeated polling.
		if i != 1 {
			w.Header().Set("Retry-After", "86400")
		}
		fmt.Fprintf(w, `{"task_id":"fixture246-%d","media_type":%q,"stage":"generating","progress":7}`, i, p.rows[i].kind)
	default:
		reject()
	}
}

type admissionCapacityAccount struct {
	identity testIdentity
	client   *http.Client
}
type admissionCapacityRecord struct {
	id, key string
	input   TaskInput
	owner   int
}

// Errors are deliberately status-only. No receipt, prompt, key or session
// contents should appear even when an assertion fails.
func (a admissionCapacityAccount) request(t *testing.T, ctx context.Context, base, method, path, key string, body, out any, want int) http.Header {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal("cannot encode fixture request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("could not construct fixture request")
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if a.identity.Project.ID != "" {
		req.Header.Set("X-Project-ID", a.identity.Project.ID)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("local API request did not complete (deadline reached: %t)", ctx.Err() != nil)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (96<<10)+1))
	if err != nil || len(data) > 96<<10 || resp.StatusCode != want {
		t.Fatalf("local API expected HTTP %d, received %d", want, resp.StatusCode)
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		t.Fatal("unreadable local API response")
	}
	return resp.Header
}

func admissionCapacityRegister(t *testing.T, ctx context.Context, base string, n int) []admissionCapacityAccount {
	t.Helper()
	accounts := make([]admissionCapacityAccount, n)
	for i := range accounts {
		// All clients genuinely use the same loopback address. Let the
		// ten-per-fifteen-minute registration window pass in the largest tier.
		if i > 0 && i%10 == 0 {
			select {
			case <-ctx.Done():
				t.Fatal("registration deadline")
			case <-time.After(15*time.Minute + time.Second):
			}
		}
		account := admissionCapacityAccount{client: authClient(t)}
		body := map[string]string{"email": fmt.Sprintf("tenant-%d@fixture246.test", i), "name": "Capacity Fixture", "password": "test-only-capacity-246"}
		account.request(t, ctx, base, "POST", "/api/v1/auth/register", "", body, &account.identity, 201)
		if account.identity.User.ID == "" || account.identity.Workspace.ID == "" || account.identity.Project.WorkspaceID != account.identity.Workspace.ID {
			t.Fatal("registration did not return a scoped project")
		}
		accounts[i] = account
	}
	return accounts
}

func admissionCapacityDeny(t *testing.T, ctx context.Context, base string, a admissionCapacityAccount, r admissionCapacityRecord, code string) {
	t.Helper()
	var e struct {
		Error APIError `json:"error"`
	}
	a.request(t, ctx, base, "POST", "/api/v1/tasks", r.key+"-overflow", r.input, &e, 429)
	if e.Error.Code != code {
		t.Fatal("incorrect admission refusal")
	}
}

func admissionCapacitySubmit(t *testing.T, ctx context.Context, base string, profile admissionCapacityProfile, accounts []admissionCapacityAccount) []admissionCapacityRecord {
	t.Helper()
	records := make([]admissionCapacityRecord, 0, profile.tasks)
	perWorkspace := profile.tasks / profile.workspaces
	for owner, account := range accounts {
		for j := 0; j < perWorkspace; j++ {
			kind := "image"
			if j >= perWorkspace/2 {
				kind = "video"
			}
			in := taskInput(kind)
			in.ProjectID = account.identity.Project.ID
			if kind == "video" {
				in.Parameters.Seconds = 5
				in.Parameters.Resolution = "720p"
			}
			in.Inputs.Prompt = fmt.Sprintf("fixture246-%d", len(records))
			record := admissionCapacityRecord{key: fmt.Sprintf("capacity-246-%d", len(records)), input: in, owner: owner}
			var task Task
			h := account.request(t, ctx, base, "POST", "/api/v1/tasks", record.key, in, &task, 202)
			if h.Get("Idempotency-Replayed") != "" || task.ID == "" || task.WorkspaceID != account.identity.Workspace.ID || task.ProjectID != in.ProjectID || task.ModelID != in.ModelID || task.ProviderMode != "live" {
				t.Fatal("first acceptance did not freeze the requested scope and model")
			}
			record.id = task.ID
			records = append(records, record)
			if j+1 == perWorkspace/2 {
				admissionCapacityDeny(t, ctx, base, account, record, "ADMISSION_MODEL_LIMIT")
			}
		}
		admissionCapacityDeny(t, ctx, base, account, records[len(records)-1], "ADMISSION_WORKSPACE_LIMIT")
		t.Logf("admitted workspaces=%d tasks=%d", owner+1, len(records))
	}
	return records
}

func admissionCapacityReplay(t *testing.T, ctx context.Context, base string, accounts []admissionCapacityAccount, records []admissionCapacityRecord) {
	t.Helper()
	for _, r := range records {
		var task Task
		h := accounts[r.owner].request(t, ctx, base, "POST", "/api/v1/tasks", r.key, r.input, &task, 202)
		if h.Get("Idempotency-Replayed") != "true" || task.ID != r.id || task.WorkspaceID != accounts[r.owner].identity.Workspace.ID || task.ProjectID != r.input.ProjectID {
			t.Fatal("replay did not return its original scoped task")
		}
	}
}
func admissionCapacityWait(t *testing.T, ctx context.Context, conn *pgx.Conn, total int) {
	t.Helper()
	for {
		var rows, running, unknown int
		err := conn.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE document->>'status'='RUNNING'),count(*) FILTER(WHERE document->>'status'='SUBMIT_UNKNOWN') FROM studio_tasks`).Scan(&rows, &running, &unknown)
		if err != nil {
			t.Fatalf("database count unavailable (deadline reached: %t)", ctx.Err() != nil)
		}
		if rows != total {
			t.Fatalf("persisted tasks=%d expected=%d", rows, total)
		}
		if running == total-1 && unknown == 1 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("deadline: persisted=%d running=%d unknown=%d", rows, running, unknown)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

type admissionCapacityCounts struct {
	Records int `json:"pg_tasks"`
	Running int `json:"pg_running_with_original_handle"`
	Unknown int `json:"pg_unknown_without_handle"`
	Posts   int `json:"gateway_posts"`
	Gets    int `json:"gateway_gets"`
}

// All facts come from normal HTTP admission and dispatch. SQL only reads
// committed facts, including the exact original operation/body sent on the wire.
func admissionCapacityCheck(t *testing.T, ctx context.Context, conn *pgx.Conn, wire *admissionCapacityWire, records []admissionCapacityRecord, accounts []admissionCapacityAccount) admissionCapacityCounts {
	t.Helper()
	wire.mu.Lock()
	observed := append([]admissionCapacityWireRow(nil), wire.rows...)
	result := admissionCapacityCounts{Posts: wire.posts, Gets: wire.gets}
	invalid := wire.invalid
	wire.mu.Unlock()
	if invalid != 0 || result.Posts != len(records) {
		t.Fatalf("gateway invalid=%d posts=%d expected=%d", invalid, result.Posts, len(records))
	}
	expected := make(map[string]int, len(records))
	keys := make(map[string]bool, len(records))
	for i, r := range records {
		expected[r.id] = i
	}
	rows, err := conn.Query(ctx, `SELECT t.id,t.workspace_id,t.project_id,t.document->>'model_id',t.document->>'status',coalesce(t.handle->>'ID',''),
	 t.submission->>'idempotency_key',t.submission->>'body_sha256',k.scope_key,
	 COALESCE(t.handle->>'ProviderIdentity'=t.submission->>'provider_identity' AND t.handle->>'CredentialID'=t.submission->>'credential_id'
	 AND t.handle->>'Kind'=t.submission->>'kind' AND t.handle->>'SubmittedAt'=t.submission->>'prepared_at'
	 AND t.handle->>'Count'=t.submission->>'count' AND length(t.handle->>'ProviderIdentity')=64,false)
	 FROM studio_tasks t JOIN studio_idempotency k ON k.resource_id=t.id AND k.resource_type='task'`)
	if err != nil {
		t.Fatal("could not read persisted admission facts")
	}
	defer rows.Close()
	for rows.Next() {
		var id, workspace, project, model, status, handle, key, hash, scope string
		var sameIdentity bool
		if rows.Scan(&id, &workspace, &project, &model, &status, &handle, &key, &hash, &scope, &sameIdentity) != nil {
			t.Fatal("could not decode persisted admission facts")
		}
		i, ok := expected[id]
		if !ok {
			t.Fatal("unexpected or duplicate persisted task/receipt")
		}
		delete(expected, id)
		r := records[i]
		entry := observed[i]
		if workspace != accounts[r.owner].identity.Workspace.ID || project != r.input.ProjectID || model != r.input.ModelID || scope != workspace+":tasks:"+r.key ||
			key == "" || keys[key] || key != entry.key || hash != entry.hash || entry.kind != r.input.Kind {
			t.Fatal("scope, immutable request, key or original body differed from the wire")
		}
		keys[key] = true
		result.Records++
		if i == 0 {
			if status != "SUBMIT_UNKNOWN" || handle != "" || entry.gets != 0 {
				t.Fatal("unconfirmed submit did not remain parked")
			}
			result.Unknown++
		} else {
			if status != "RUNNING" || handle != fmt.Sprintf("fixture246-%d", i) || !sameIdentity || entry.gets < 1 {
				t.Fatal("nonterminal task lost its original handle or GET")
			}
			result.Running++
		}
	}
	if rows.Err() != nil || len(expected) != 0 || result.Records != len(records) || result.Unknown != 1 || result.Running != len(records)-1 {
		t.Fatal("incomplete admission or receipt evidence")
	}
	return result
}

func admissionCapacityVerifyQuota(t *testing.T, ctx context.Context, base string, accounts []admissionCapacityAccount, records []admissionCapacityRecord) {
	t.Helper()
	perWorkspace := len(records) / len(accounts)
	for i, a := range accounts {
		var snapshot AdmissionSnapshot
		a.request(t, ctx, base, "GET", "/api/v1/runtime", "", nil, &snapshot, 200)
		models := map[string]bool{}
		for _, model := range snapshot.Tasks.Models {
			if model.Limit != perWorkspace/2 || model.Active != model.Limit || models[model.ModelID] {
				t.Fatal("incorrect active model occupancy")
			}
			models[model.ModelID] = true
		}
		if snapshot.Tasks.Active != perWorkspace || snapshot.Tasks.Limit != perWorkspace || len(models) != 2 || !models["studio-image"] || !models["studio-video"] {
			t.Fatal("incorrect workspace occupancy")
		}
		admissionCapacityDeny(t, ctx, base, a, records[i*perWorkspace], "ADMISSION_WORKSPACE_LIMIT")
	}
	var unknown Task
	u := records[0]
	accounts[0].request(t, ctx, base, "GET", "/api/v1/tasks/"+u.id, "", nil, &unknown, 200)
	if unknown.ID != u.id || unknown.Status != "SUBMIT_UNKNOWN" || unknown.Error == nil {
		t.Fatal("original unknown task disagrees with public status")
	}
	accounts[1].request(t, ctx, base, "GET", "/api/v1/tasks/"+u.id, "", nil, nil, 404)
}

func admissionCapacityOpen(t *testing.T, c Config) (*App, *httptest.Server, func()) {
	t.Helper()
	a, err := New(c)
	if err != nil {
		t.Fatal("could not open isolated application")
	}
	s := httptest.NewServer(a.Handler())
	closeApp := sync.OnceFunc(func() { s.Close(); a.Close() })
	t.Cleanup(closeApp)
	return a, s, closeApp
}

func TestPostgresMediaNormalAdmissionCapacity(t *testing.T) {
	raw := os.Getenv("STUDIO_MEDIA_ADMISSION_TASKS")
	if raw == "" {
		t.Skip("opt in with STUDIO_MEDIA_ADMISSION_TASKS")
	}
	profile, ok := admissionCapacityTier(raw)
	if !ok {
		t.Fatal("supported task counts: 128, 512, 4096, 60000")
	}
	if !admissionCapacityLocalDSN(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("explicit tier requires an isolated loopback PostgreSQL URL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), profile.budget)
	defer cancel()
	dsn := testDatabase(t)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("could not open isolated database observer")
	}
	defer conn.Close(context.Background())
	probe := &admissionCapacityWire{rows: make([]admissionCapacityWireRow, profile.tasks)}
	gateway := httptest.NewServer(probe)
	t.Cleanup(gateway.Close)
	perWorkspace := profile.tasks / profile.workspaces
	cfg := Config{Mode: "live", AuthMode: "required", DatabaseURL: dsn, AssetDir: t.TempDir(), ReferenceDir: t.TempDir(),
		AsyncImageBaseURL: gateway.URL, AsyncImageAPIKey: fixtureMediaKey, AsyncImageCredentialID: "fixture246",
		AsyncImageModel: "fixture246-image", AsyncVideoModel: "fixture246-video", AsyncVideoSeconds: 5, AsyncVideoResolution: "720p",
		MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()}, PollInterval: 20 * time.Millisecond,
		MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 2, QueryWorkers: 2, DeliveryWorkers: 1, ReviewWorkers: 1, WorkspaceConcurrency: 2, ChannelConcurrency: 2},
		Admission:       AdmissionLimits{WorkspaceTasks: perWorkspace, ModelTasks: perWorkspace / 2}}
	_, api, closeFirst := admissionCapacityOpen(t, cfg)
	accounts := admissionCapacityRegister(t, ctx, api.URL, profile.workspaces)
	t.Logf("registered authorized workspaces=%d", len(accounts))
	started := time.Now()
	records := admissionCapacitySubmit(t, ctx, api.URL, profile, accounts)
	admitMS := time.Since(started).Milliseconds()
	admissionCapacityReplay(t, ctx, api.URL, accounts, records)
	admissionCapacityWait(t, ctx, conn, len(records))
	admissionCapacityVerifyQuota(t, ctx, api.URL, accounts, records)
	before := admissionCapacityCheck(t, ctx, conn, probe, records, accounts)
	closeFirst()
	probe.mu.Lock()
	oldSentinel := probe.rows[1].gets
	stoppedPosts := probe.posts
	stoppedGets := probe.gets
	probe.mu.Unlock()
	reopenedApp, reopened, closeSecond := admissionCapacityOpen(t, cfg)
	// Restore visits every RUNNING handle. The original unconfirmed POST is
	// parked outside the recovery scan and remains available by receipt.
	recovery := reopenedApp.recoveryStats
	minPages := (before.Running + recoveryMediaPageRecords - 1) / recoveryMediaPageRecords
	if recovery.TaskRows != before.Running || recovery.TaskPages < minPages || recovery.TaskPages > recovery.TaskRows ||
		recovery.MaxPageRecords < 1 || recovery.MaxPageRecords > min(before.Running, recoveryMediaPageRecords) ||
		recovery.MaxPageEncodedBytes < 1 || recovery.MaxPageEncodedBytes > recoveryMediaPageBytes ||
		recovery.CachedTasksPeak != min(before.Running, reopenedApp.config.MaxRecords) {
		t.Fatalf("reopen recovery rows=%d pages=%d max_page_records=%d max_page_bytes=%d cached_tasks_peak=%d",
			recovery.TaskRows, recovery.TaskPages, recovery.MaxPageRecords, recovery.MaxPageEncodedBytes, recovery.CachedTasksPeak)
	}
	admissionCapacityReplay(t, ctx, reopened.URL, accounts, records)
	admissionCapacityVerifyQuota(t, ctx, reopened.URL, accounts, records)
	for {
		probe.mu.Lock()
		advanced := probe.rows[1].gets > oldSentinel
		probe.mu.Unlock()
		if advanced {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reopened application never queried original handle")
		case <-time.After(100 * time.Millisecond):
		}
	}
	closeSecond()
	after := admissionCapacityCheck(t, ctx, conn, probe, records, accounts)
	if stoppedPosts != profile.tasks || after.Posts != stoppedPosts {
		t.Fatal("reopen repeated a submission")
	}
	report := struct {
		Workspaces              int   `json:"authorized_workspaces"`
		WorkspaceLimit          int   `json:"workspace_limit"`
		ModelLimit              int   `json:"model_limit"`
		Accepted                int   `json:"normal_api_accepted"`
		ReplayBefore            int   `json:"original_key_replays_before"`
		ReplayAfter             int   `json:"original_key_replays_after"`
		ModelDenials            int   `json:"model_429"`
		WorkspaceDenials        int   `json:"workspace_429"`
		AdmissionMS             int64 `json:"first_admissions_and_overflow_ms"`
		Before, After           admissionCapacityCounts
		ReopenGET               int   `json:"gateway_gets_after_reopen"`
		ReopenPOST              int   `json:"gateway_posts_after_reopen"`
		RecoveryTaskRows        int   `json:"reopen_recovery_task_rows"`
		RecoveryTaskPages       int   `json:"reopen_recovery_task_pages"`
		RecoveryMaxPageRecords  int   `json:"reopen_recovery_max_page_records"`
		RecoveryMaxPageBytes    int64 `json:"reopen_recovery_max_page_bytes"`
		RecoveryCachedTasksPeak int   `json:"reopen_recovery_cached_tasks_peak"`
	}{
		Workspaces: profile.workspaces, WorkspaceLimit: perWorkspace, ModelLimit: perWorkspace / 2,
		Accepted: len(records), ReplayBefore: len(records), ReplayAfter: len(records),
		ModelDenials: len(accounts), WorkspaceDenials: len(accounts) * 3, AdmissionMS: admitMS,
		Before: before, After: after, ReopenGET: after.Gets - stoppedGets, ReopenPOST: after.Posts - stoppedPosts,
		RecoveryTaskRows: recovery.TaskRows, RecoveryTaskPages: recovery.TaskPages, RecoveryMaxPageRecords: recovery.MaxPageRecords,
		RecoveryMaxPageBytes: recovery.MaxPageEncodedBytes, RecoveryCachedTasksPeak: recovery.CachedTasksPeak,
	}
	blob, err := json.Marshal(report)
	if err != nil {
		t.Fatal("could not encode numeric evidence")
	}
	t.Logf("MEDIA_NORMAL_ADMISSION %s", blob)
}
func TestMediaAdmissionCapacityFixture(t *testing.T) {
	for _, item := range []string{"128", "512", "4096", "60000"} {
		p, ok := admissionCapacityTier(item)
		limit := p.tasks / p.workspaces
		if !ok || p.tasks%p.workspaces != 0 || limit%2 != 0 || limit > 4096 {
			t.Fatal("profile cannot be admitted by the configured quotas")
		}
	}
	if _, ok := admissionCapacityTier("120000"); ok {
		t.Fatal("unbounded tier accepted")
	}
	for _, dsn := range []string{"", "postgres://fixture@192.0.2.1/test", "postgres://fixture@localhost/test", "postgres://fixture@127.0.0.1/test?host=192.0.2.1", "postgres://fixture@127.0.0.1/test?service=other"} {
		if admissionCapacityLocalDSN(dsn) {
			t.Fatal("nonlocal or ambiguous PostgreSQL URL accepted")
		}
	}
	if !admissionCapacityLocalDSN("postgres://fixture@127.0.0.1/test?sslmode=disable") {
		t.Fatal("loopback PostgreSQL URL rejected")
	}
	probe := &admissionCapacityWire{rows: make([]admissionCapacityWireRow, 3)}
	s := httptest.NewServer(probe)
	defer s.Close()
	p, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: s.URL, APIKey: fixtureMediaKey, CredentialID: "fixture246", ImageModel: "fixture246-image", VideoModel: "fixture246-video", VideoSeconds: 5, VideoResolution: "720p", AssetDir: t.TempDir()})
	if err != nil {
		t.Fatal("cannot open local media adapter")
	}
	defer p.Close()
	for i, kind := range []string{"image", "image", "video"} {
		input := taskInput(kind)
		input.Inputs.Prompt = fmt.Sprintf("fixture246-%d", i)
		if kind == "video" {
			input.Parameters.Seconds = 5
			input.Parameters.Resolution = "720p"
		}
		sub, err := p.PrepareModel(input, "fixture246-"+kind)
		if err != nil {
			t.Fatal("could not prepare example submission")
		}
		handle, err := p.SubmitPrepared(t.Context(), sub)
		if i == 0 {
			if err == nil || !requireMediaError(t, err).SubmissionUnknown || handle.ID != "" {
				t.Fatal("unconfirmed input did not produce unknown receipt")
			}
			continue
		}
		if err != nil || handle.ID != input.Inputs.Prompt {
			t.Fatal("normal fixture submission failed")
		}
		out, err := p.QueryStatus(t.Context(), handle)
		if err != nil || out.Result.Status != "RUNNING" {
			t.Fatal("fixture did not return an in-flight original handle")
		}
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.invalid != 0 || probe.posts != 3 || probe.gets != 2 {
		t.Fatal("unexpected local protocol traffic")
	}
}
