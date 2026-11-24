package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// All three original submissions go to this one private HTTP service. One is
// observed generating, one hits 429 on its first GET, and the third receives
// a 202 without a handle. Only the known handles are legal GET targets.
type queryUpgradeWire struct {
	sync.Mutex
	posts, gets      map[string]int
	restored         bool
	entered, release chan struct{}
}

func (p *queryUpgradeWire) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey || r.URL.RawQuery != "" {
		w.WriteHeader(401)
		return
	}
	if r.Method == "POST" && r.URL.Path == imageGenerationEndpoint {
		var in struct{ Prompt string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		p.Lock()
		p.posts[in.Prompt]++
		p.Unlock()
		w.WriteHeader(202)
		if in.Prompt == "unknown" {
			_, _ = w.Write([]byte("{}"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"task_id": "old-" + in.Prompt})
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, mediaQueryPrefix)
	if r.Method != "GET" || !ok || (id != "old-running" && id != "old-cold") {
		p.Lock()
		p.gets["unexpected"]++
		p.Unlock()
		w.WriteHeader(400)
		return
	}
	p.Lock()
	p.gets[id]++
	restored := p.restored
	p.Unlock()
	if !restored && id == "old-cold" {
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(429)
		return
	}
	if restored && id == "old-cold" {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-p.release:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Retry-After", "8")
	_ = json.NewEncoder(w).Encode(map[string]any{"task_id": id, "media_type": "image", "stage": "generating", "progress": 37})
}

func queryUpgradeCondition(t *testing.T, limit time.Duration, what string, test func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !test() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func queryUpgradeLimit(t *testing.T, conn *pgx.Conn, lane string, limit int, allowed bool) {
	t.Helper()
	ctx := t.Context()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO studio_media_channel_runtime(scope,lane,channel_key,effective_limit) VALUES('gateway',$1,$2,$3)", lane, strings.Repeat("9", 64), limit)
	var pgErr *pgconn.PgError
	if allowed && err != nil || !allowed && !(errors.As(err, &pgErr) && pgErr.Code == "23514") {
		t.Fatalf("unexpected %s persisted limit %d outcome (expected accepted=%t)", lane, limit, allowed)
	}
}

func TestPostgresMediaQueryUpgrade039Restore040(t *testing.T) {
	if os.Getenv("MEDIA_QUERY_UPGRADE_TEST") != "1" {
		t.Skip("set MEDIA_QUERY_UPGRADE_TEST=1 for the real old-writer upgrade and paired restore")
	}
	binary := os.Getenv("MEDIA_QUERY_UPGRADE_039_SERVER")
	if !filepath.IsAbs(binary) || !mixedLoopbackDatabase(os.Getenv("TEST_DATABASE_URL")) || !filepath.IsAbs(os.Getenv("TEST_POSTGRES_BIN_DIR")) {
		t.Fatal("requires a 039 workbench binary, loopback test database, and PostgreSQL backup tools")
	}
	ctx := t.Context()
	wire := &queryUpgradeWire{posts: map[string]int{}, gets: map[string]int{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	gateway := httptest.NewServer(http.HandlerFunc(wire.serve))
	defer gateway.Close()
	unblock := sync.OnceFunc(func() { close(wire.release) })
	defer unblock()
	cfg := Config{DatabaseURL: testDatabase(t), Mode: "live", AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(),
		AsyncImageBaseURL: gateway.URL, AsyncImageAPIKey: fixtureMediaKey, AsyncImageCredentialID: "local-upgrade", AsyncImageModel: "local-upgrade-image",
		BillingMode: "credits", BillingPriceFile: filepath.Join(t.TempDir(), "local-prices.json"), MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()},
		PollInterval: time.Hour, MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 8, WorkspaceConcurrency: 4, ChannelConcurrency: 8, QueryDBConnections: 2}}
	prices := `{"schema_version":1,"provider_mode":"live","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-image","kind":"image","operation":"generate","preset_id":"preset-1","artifact_micros":"200000"}]}`
	if err := os.WriteFile(cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"DATABASE_URL": cfg.DatabaseURL, "PROVIDER_MODE": cfg.Mode, "AUTH_MODE": cfg.AuthMode, "ASSET_DIR": cfg.AssetDir,
		"REFERENCE_DIR": cfg.ReferenceDir, "ASYNC_IMAGE_BASE_URL": cfg.AsyncImageBaseURL, "ASYNC_IMAGE_API_KEY": cfg.AsyncImageAPIKey,
		"ASYNC_IMAGE_CREDENTIAL_ID": cfg.AsyncImageCredentialID, "ASYNC_IMAGE_MODEL": cfg.AsyncImageModel, "MEDIA_DELIVERY_KEY": cfg.MediaDelivery.EncryptionKey,
		"BILLING_MODE": cfg.BillingMode, "BILLING_PRICE_FILE": cfg.BillingPriceFile, "MEDIA_SUBMIT_WORKERS": "1", "MEDIA_QUERY_WORKERS": "8",
		"MEDIA_WORKSPACE_CONCURRENCY": "4", "MEDIA_CHANNEL_CONCURRENCY": "8"}
	old := startChildAcceptanceServer(t, ctx, binary, settings)
	conn := deliveryAdmin(t, cfg.DatabaseURL)
	if outboxCount(t, conn, "SELECT count(*) FROM studio_schema_versions") != 39 || outboxCount(t, conn, "SELECT max(version) FROM studio_schema_versions") != 39 {
		t.Fatal("the supplied executable did not initialize the 039 schema")
	}
	client, outsider := authClient(t), authClient(t)
	owner := registerFixture(t, client, old.base, "query-040-owner@example.test")
	other := registerFixture(t, outsider, old.base, "query-040-other@example.test")
	old.crash(t)
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		_, err := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: owner.Workspace.ID, OperationKey: newID("fixture"), AmountMicros: 2000000, Reason: "local-fixture", EvidenceRef: "fixture:040"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	old = startChildAcceptanceServer(t, ctx, binary, settings)
	type accepted struct {
		input TaskInput
		key   string
		task  Task
	}
	accept := func(prompt string) accepted {
		in := taskInput("image")
		in.ProjectID = owner.Project.ID
		in.Inputs.Prompt = prompt
		code, body, _ := authRequest(t, client, old.base, "POST", "/api/v1/billing/quotes", "", in.ProjectID, map[string]any{"resource_type": "task", "task_input": in})
		if code != 201 {
			t.Fatalf("039 quote returned %d", code)
		}
		in.QuoteID = decode[BillingQuote](t, body).ID
		key := newID("old_key")
		code, body, _ = authRequest(t, client, old.base, "POST", "/api/v1/tasks", key, in.ProjectID, in)
		task := decode[Task](t, body)
		if code != 202 || task.Billing == nil || task.Billing.QuoteID != in.QuoteID || task.Billing.ReservationID == "" {
			t.Fatalf("039 submission returned %d without original reservation", code)
		}
		return accepted{in, key, task}
	}
	running := accept("running")
	waitForStatus := func(id, status string) {
		queryUpgradeCondition(t, 20*time.Second, "039 task state", func() bool {
			return outboxCount(t, conn, "SELECT count(*) FROM studio_tasks WHERE id=$1 AND document->>'status'=$2", id, status) == 1
		})
	}
	waitForStatus(running.task.ID, "RUNNING")
	cold := accept("cold")
	waitForStatus(cold.task.ID, "PROVIDER_STATE_UNKNOWN")
	unknown := accept("unknown")
	waitForStatus(unknown.task.ID, "SUBMIT_UNKNOWN")
	old.crash(t)
	tables := []string{"studio_users", "studio_projects", "studio_workspaces", "studio_sessions", "studio_tasks", "studio_idempotency",
		"studio_task_schedule", "studio_workspace_schedule", "studio_lane_schedule", "studio_media_channel_runtime",
		"studio_ledger_wallets", "studio_ledger_accounts", "studio_ledger_quotes", "studio_ledger_reservations", "studio_ledger_journals", "studio_ledger_postings", "studio_task_video_sources"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	frozenTasks := func(db *pgx.Conn) string {
		var frozen string
		err := db.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(id,workspace_id,project_id,input,submission,handle) ORDER BY id)::text
			FROM studio_tasks WHERE id IN ($1,$2,$3)`, running.task.ID, cold.task.ID, unknown.task.ID).Scan(&frozen)
		if err != nil {
			t.Fatal("could not read original frozen task identities")
		}
		return frozen
	}
	frozen := frozenTasks(conn)
	projection, err := loadMediaScheduleSQL(ctx, conn, cold.task.ID)
	if err != nil || projection == nil || !projection.Active || projection.Lane != "query" {
		t.Fatal("missing 039 query projection")
	}
	originalChannel, err := loadMediaChannelSQL(ctx, conn, "gateway", "query", projection.ChannelKey, false)
	if err != nil || !originalChannel.ProbeRequired || originalChannel.ProbeID != "" || originalChannel.FailureCount < 1 ||
		originalChannel.EffectiveLimit <= 1 || originalChannel.CooldownUntil.Before(projection.ServerNotBefore) {
		t.Fatal("039 did not preserve backoff and the independent single-probe obligation")
	}
	if outboxCount(t, conn, "SELECT count(*) FROM studio_tasks WHERE id=$1 AND document->>'status'='RUNNING' AND handle->>'ID'='old-running'", running.task.ID) != 1 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_tasks WHERE id=$1 AND handle->>'ID'='old-cold'", cold.task.ID) != 1 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_tasks WHERE id=$1 AND COALESCE(handle->>'ID','')=''", unknown.task.ID) != 1 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_ledger_reservations WHERE resource_id=$1 AND project_id=$2 AND workspace_id=$3 AND state='REVIEW' AND reserved_micros=200000 AND charged_micros=0", unknown.task.ID, owner.Project.ID, owner.Workspace.ID) != 1 {
		t.Fatal("039 did not retain known handles and a distinct unknown credit reservation")
	}
	wire.Lock()
	oldRunningGETs := wire.gets["old-running"]
	oldWireOK := reflect.DeepEqual(wire.posts, map[string]int{"running": 1, "cold": 1, "unknown": 1}) && wire.gets["old-cold"] == 1 && oldRunningGETs >= 1 && len(wire.gets) == 2
	wire.Unlock()
	if !oldWireOK {
		t.Fatal("039 supplier traffic differed from the three original submissions")
	}
	maintenance := StorageMaintenanceConfig{DatabaseURL: cfg.DatabaseURL, ReferenceDir: cfg.ReferenceDir, AssetDir: cfg.AssetDir, WebDir: t.TempDir()}
	if _, err = MaintainPrivateStorage(ctx, maintenance); err == nil || !strings.Contains(err.Error(), "schema versions") {
		t.Fatal("current offline maintenance did not reject the 039 schema at its version check")
	}
	queryUpgradeLimit(t, conn, "query", 33, false)
	format040 := workflowOldMigrations(t, "040")
	if err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { return applyMigrationsFrom(ctx, tx, format040) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("040 migration rewrote existing execution, schedule, financial, or identity evidence")
	}
	queryUpgradeLimit(t, conn, "query", 1536, true)
	queryUpgradeLimit(t, conn, "query", 1537, false)
	queryUpgradeLimit(t, conn, "query", 0, false)
	for _, lane := range []string{"submit", "delivery", "review"} {
		queryUpgradeLimit(t, conn, lane, 32, true)
		queryUpgradeLimit(t, conn, lane, 33, false)
	}
	oldFormat := workflowOldMigrations(t, "039")
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { return applyMigrationsFrom(ctx, tx, oldFormat) })
	if err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("039 writer accepted upgraded schema")
	}
	oldCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	cmd := exec.CommandContext(oldCtx, binary)
	cmd.Dir, cmd.Env = t.TempDir(), childFixtureEnvironment(settings)
	diagnostic, startupErr := cmd.CombinedOutput()
	rejected := startupErr != nil && oldCtx.Err() == nil && bytes.Contains(diagnostic, []byte("PostgreSQL migration failed; startup rejected"))
	cancel()
	if !rejected {
		t.Fatal("actual old 039 binary did not reject upgraded storage at its migration fence")
	}
	if maintenanceSchemaVersion == 40 {
		report, err := MaintainPrivateStorage(ctx, maintenance)
		if err != nil || report.SchemaVersion != 40 || report.Applied || report.RemovedFiles != 0 {
			t.Fatal("040 maintenance rejected the matching upgraded format")
		}
	} else if maintenanceSchemaVersion < 40 {
		t.Fatal("maintenance format predates 040")
	}

	// No controller owns the source. The common backup helper takes a real
	// custom-format dump, copies both private trees and the delivery key, and
	// restores them into separately allocated paths and a separate database.
	paired := &workflowIntegrationFixture{cfg: cfg}
	restoreWorkflowFixture(t, paired, tables...)
	restoredConn := deliveryAdmin(t, paired.cfg.DatabaseURL)
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, restoredConn, tables)) {
		t.Fatal("joint restore altered the original 039 evidence")
	}
	a, s := testApp(t, paired.cfg)
	after, err := loadMediaChannelSQL(ctx, restoredConn, "gateway", "query", projection.ChannelKey, false)
	if err != nil || !reflect.DeepEqual(originalChannel, after) {
		t.Fatal("startup reset persisted cooldown or channel concurrency")
	}
	if a.executePersistentTaskLane(mediaSubmitLane, unknown.task.ID) || a.executePersistentTaskLane(mediaQueryLane, unknown.task.ID) {
		t.Fatal("040 scheduled handleless SUBMIT_UNKNOWN")
	}
	for _, op := range []accepted{running, cold, unknown} {
		path := "/api/v1/tasks/" + op.task.ID
		status, raw, _ := authRequest(t, client, s.URL, "GET", path, "", owner.Project.ID, nil)
		if status != 200 {
			t.Fatalf("restored owner read returned %d", status)
		}
		task := decode[Task](t, raw)
		if task.ID != op.task.ID || task.ProjectID != owner.Project.ID || task.Billing == nil || task.Billing.ReservationID != op.task.Billing.ReservationID {
			t.Fatal("wrong restored task, owner, or reservation")
		}
		status, _, _ = authRequest(t, outsider, s.URL, "GET", path, "", other.Project.ID, nil)
		if status != 404 && status != 403 {
			t.Fatalf("unrelated project read returned %d", status)
		}
		status, raw, h := authRequest(t, client, s.URL, "POST", "/api/v1/tasks", op.key, owner.Project.ID, op.input)
		if status != 202 || h.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != op.task.ID {
			t.Fatal("040 lost 039 original-key replay")
		}
	}
	// Wait using the database's own clock without overwriting a persisted
	// deadline or shortening the running task's original eligibility.
	if time.Until(after.CooldownUntil) > time.Second && a.executePersistentTaskLane(mediaQueryLane, cold.task.ID) {
		t.Fatal("040 contradicted a still-live 039 Retry-After")
	}
	queryUpgradeCondition(t, 50*time.Second, "original database deadlines", func() bool {
		return outboxCount(t, restoredConn, `SELECT count(*) FROM studio_task_schedule s JOIN studio_media_channel_runtime c
		ON c.channel_key=s.channel_key AND c.lane='query' AND c.scope='gateway'
		WHERE s.task_id IN ($1,$2) AND greatest(s.server_not_before,s.eligible_at,c.cooldown_until)<=clock_timestamp()`, cold.task.ID, running.task.ID) == 2
	})
	wire.Lock()
	wire.restored = true
	wire.Unlock()
	done := schedulerStepAsync(a, mediaQueryLane, cold.task.ID)
	select {
	case <-wire.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restored original-handle GET did not reach the supplier")
	}
	probe, err := loadMediaChannelSQL(ctx, restoredConn, "gateway", "query", projection.ChannelKey, false)
	if err != nil || probe.ProbeID == "" || probe.ProbeUntil == nil || !probe.ProbeRequired {
		t.Fatal("restored GET lacked its durable single-probe identity")
	}
	if a.executePersistentTaskLane(mediaQueryLane, running.task.ID) {
		t.Fatal("a second original GET crossed the channel during its half-open probe")
	}
	unblock()
	schedulerStepDone(t, done, true)
	channel, err := loadMediaChannelSQL(ctx, restoredConn, "gateway", "query", projection.ChannelKey, false)
	if err != nil || channel.ProbeRequired || channel.ProbeID != "" {
		t.Fatal("confirmed original GET failed to close the half-open channel")
	}
	if !a.executePersistentTaskLane(mediaQueryLane, running.task.ID) {
		t.Fatal("second original handle could not resume after probe")
	}
	wire.Lock()
	defer wire.Unlock()
	if !reflect.DeepEqual(wire.posts, map[string]int{"running": 1, "cold": 1, "unknown": 1}) || len(wire.gets) != 2 || wire.gets["old-running"] != oldRunningGETs+1 || wire.gets["old-cold"] != 2 {
		t.Fatal("expected two restored original-handle GETs and zero additional upstream submissions")
	}
	if outboxCount(t, restoredConn, "SELECT count(*) FROM studio_tasks WHERE id=$1 AND document->>'status'='SUBMIT_UNKNOWN' AND COALESCE(handle->>'ID','')=''", unknown.task.ID) != 1 ||
		outboxCount(t, restoredConn, "SELECT count(*) FROM studio_ledger_reservations WHERE id=$1 AND state='REVIEW' AND charged_micros=0", unknown.task.Billing.ReservationID) != 1 {
		t.Fatal("unrelated successful probes changed the unknown original or its credit reservation")
	}
	if frozenTasks(restoredConn) != frozen {
		t.Fatal("querying after restore changed an original handle, frozen request or project identity")
	}
	t.Log("039 original supplier POSTs=3; 040 restored original-handle GETs=2; new supplier POSTs=0; overlapping probes=0; unknown reservations held for review=1")
}
