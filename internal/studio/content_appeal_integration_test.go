package studio

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresContentAppealUpgrade025PreservesDecisions(t *testing.T) {
	ctx := context.Background()
	db := testDatabase(t)
	conn := deliveryAdmin(t, db)
	old := workflowOldMigrations(t, "025")
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_prior','existing');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_prior','ws_prior','existing');
	 INSERT INTO studio_runs(id,workspace_id,project_id,document) VALUES('run_prior','ws_prior','prj_prior','{"status":"FAILED"}');
	 INSERT INTO studio_run_moderation(run_id,policy_id,policy_version,policy_digest,policy,state,version,reason_code)
	 VALUES('run_prior','old-text','v1',repeat('a',64),'{}','BLOCKED',2,'policy_block');
	 INSERT INTO studio_moderation_decisions(run_id,version,state,reason_code) VALUES('run_prior',2,'BLOCKED','policy_block');
	 INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
	 VALUES('tsk_prior','ws_prior','prj_prior','{"id":"tsk_prior","kind":"image","provider_mode":"demo","model_id":"studio-image","version":3,"status":"FAILED"}','{}','{}',now());
	 INSERT INTO studio_task_media_moderation(task_id,workspace_id,project_id,policy,text_policy,policy_id,policy_version,policy_digest,state,version,reason_code)
	 VALUES('tsk_prior','ws_prior','prj_prior','{}','{}','old-media','v1',repeat('b',64),'REVIEW',2,'policy_review')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_runs", "studio_tasks", "studio_run_moderation", "studio_moderation_decisions", "studio_task_media_moderation", "studio_ledger_reservations", "studio_operations_cases"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	s, err := openPostgres(db)
	if err != nil {
		t.Fatal(err)
	}
	s.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("026 altered an existing resource, original decision or financial record")
	}
	if outboxCount(t, conn, "SELECT (SELECT count(*) FROM studio_content_appeals)+(SELECT count(*) FROM studio_content_appeal_plans)+(SELECT count(*) FROM studio_content_appeal_finance_links)") != 0 {
		t.Fatal("migration manufactured an appeal or financial follow-up")
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("025 writer accepted 026")
	}
}

type appealBrowserFixture struct {
	channel *workflowIntegrationFixture
	app     *App
	server  *httptest.Server
	owner   testIdentity
	client  *http.Client
	blocked Task
}

func appealFixturePhase(value string) string {
	switch value {
	case "":
		return "none"
	case "submit", "query", "delivery", "review", "READY", "STARTED", "COMPLETE", "FINISHED", "PARKED",
		"QUEUED", "SUBMITTING", "RUNNING", "INGESTING", "MODERATING", "SUCCEEDED", "PARTIAL", "FAILED",
		"CANCELLED", "INTERRUPTED", "SUBMIT_UNKNOWN", "RECONCILE_REQUIRED", "PROVIDER_STATE_UNKNOWN",
		"PENDING", "ALLOWED", "BLOCKED", "REVIEW", "HELD", "SETTLED", "RELEASED", "REFUNDED":
		return value
	default:
		return "unknown"
	}
}

func awaitAppealBlockedTask(t *testing.T, a *App, channel *workflowIntegrationFixture, reviewer *reviewDispatchHTTPFixture, owner testIdentity, id string) Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	// Subscribe before the first read. Hints are project-wide and coalesced;
	// only the original task's committed document can satisfy the assertion.
	wake, unsubscribe := a.store.subscribeOutbox(subscriptionScope{WorkspaceID: owner.Workspace.ID, ProjectID: owner.Project.ID})
	defer unsubscribe()
	conn, err := pgx.Connect(ctx, channel.cfg.DatabaseURL)
	if err != nil {
		t.Fatal("could not open the appeal fixture's independent database reader")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = conn.Close(cleanup)
	}()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var last Task
	var lane, delivery, review string
	fail := func(reason string) {
		t.Helper()
		var moderation, billing string
		if last.Moderation != nil {
			moderation = last.Moderation.State
		}
		if last.Billing != nil {
			billing = last.Billing.State
		}
		media := channel.media
		media.mu.Lock()
		submits, queries, downloads := media.count, 0, 0
		for _, n := range media.queries {
			queries += n
		}
		for _, n := range media.downloads {
			downloads += n
		}
		media.mu.Unlock()
		t.Fatalf("appeal fixture %s: status=%s version=%d lane=%s delivery=%s review=%s moderation=%s billing=%s public_artifacts=%d submits=%d queries=%d downloads=%d output_reviews=%d",
			reason, appealFixturePhase(last.Status), last.Version, appealFixturePhase(lane), appealFixturePhase(delivery), appealFixturePhase(review),
			appealFixturePhase(moderation), appealFixturePhase(billing), len(last.Artifacts), submits, queries, downloads, reviewer.count("output"))
	}
	for {
		var task Task
		var nextLane, nextDelivery, nextReview string
		err = conn.QueryRow(ctx, `SELECT t.document, COALESCE(s.lane,''), COALESCE(d.state,''), COALESCE(r.state,'')
			FROM studio_tasks t LEFT JOIN studio_task_schedule s ON s.task_id=t.id
			LEFT JOIN studio_media_deliveries d ON d.task_id=t.id LEFT JOIN studio_media_review_dispatch r ON r.task_id=t.id
			WHERE t.id=$1 AND t.workspace_id=$2 AND t.project_id=$3`, id, owner.Workspace.ID, owner.Project.ID).Scan(&task, &nextLane, &nextDelivery, &nextReview)
		if err != nil {
			if ctx.Err() != nil {
				fail("wait expired")
			}
			fail("original committed task read failed")
		}
		last, lane, delivery, review = task, nextLane, nextDelivery, nextReview
		switch task.Status {
		case "FAILED":
			if review != "FINISHED" || task.Moderation == nil || task.Moderation.State != "BLOCKED" || task.Billing == nil ||
				task.Billing.State != "REVIEW" || task.Billing.ReservedMicros != 200000 || task.Billing.ReleasedMicros != 0 || len(task.Artifacts) != 0 {
				fail("incorrect committed terminal evidence")
			}
			return task
		case "QUEUED", "SUBMITTING", "RUNNING", "INGESTING", "MODERATING":
			// These are the only advancing phases of this original image task.
		default:
			fail("unexpected terminal or uncertain task state")
		}
		select {
		case <-wake:
		case <-tick.C:
		case <-ctx.Done():
			fail("wait expired")
		}
	}
}

func newAppealBrowserFixture(t *testing.T) appealBrowserFixture {
	t.Helper()
	channel := newWorkflowIntegrationFixture(t)
	reviewer := newReviewDispatchHTTPFixture(t, "")
	reviewer.blockSHA = moderationSHA(channel.media.image)
	channel.cfg.MediaModeration = reviewer.config(t)
	channel.cfg.OperationsEnabled = true
	channel.cfg.BillingMode = "credits"
	channel.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "appeal-prices.json")
	prices := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-image","kind":"image","operation":"generate","preset_id":"preset-1","artifact_micros":"200000"}]}`
	if err := os.WriteFile(channel.cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	a, s := channel.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "appeal-owner@example.test")
	for _, role := range []string{"moderator", "finance"} {
		actor := registerFixture(t, authClient(t), s.URL, "appeal-"+role+"@example.test")
		operationsGrantFixture(t, a, actor.User.ID, owner.Workspace.ID, role)
	}
	httpCredit(t, a, owner.Workspace.ID, 1_000_000)
	task, _ := acceptMediaExecution(t, a, s, c, owner, "image", "申诉验收测试图片", newID("original"), 1)
	task = awaitAppealBlockedTask(t, a, channel, reviewer, owner, task.ID)
	return appealBrowserFixture{channel: channel, app: a, server: s, owner: owner, client: c, blocked: task}
}

func TestPostgresContentAppealBrowserAndOriginalFinancialReceipt(t *testing.T) {
	if os.Getenv("CONTENT_APPEAL_BROWSER_TEST") != "1" {
		t.Skip("set CONTENT_APPEAL_BROWSER_TEST=1 with local Chromium and built assets")
	}
	f := newAppealBrowserFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/content-appeal-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CONTENT_APPEAL_BROWSER_URL="+f.server.URL, "CONTENT_APPEAL_TASK_ID="+f.blocked.ID, "CONTENT_APPEAL_WORKSPACE_ID="+f.owner.Workspace.ID, "CONTENT_APPEAL_PROJECT_ID="+f.owner.Project.ID)
	for _, role := range []string{"USER", "MODERATOR", "FINANCE"} {
		account := strings.ToLower(role)
		if role == "USER" {
			account = "owner"
		}
		cmd.Env = append(cmd.Env, "CONTENT_APPEAL_"+role+"_EMAIL=appeal-"+account+"@example.test", "CONTENT_APPEAL_"+role+"_PASSWORD=fixture-password-123")
	}
	out, err := cmd.CombinedOutput()
	if bytes.Contains(out, []byte("fixture-password-123")) {
		t.Fatal("browser diagnostics disclosed a test password")
	}
	if err != nil {
		t.Fatalf("content appeal browser failed: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
	status, raw, _ := authRequest(t, f.client, f.server.URL, "GET", "/api/v1/tasks/"+f.blocked.ID, "", f.owner.Project.ID, nil)
	if status != 200 {
		t.Fatal("cannot read original task", status)
	}
	after := decode[Task](t, raw)
	if after.Moderation == nil || after.Moderation.State != "BLOCKED" || after.Moderation.Version != f.blocked.Moderation.Version || len(after.Artifacts) != 0 || after.Billing == nil || after.Billing.ReleasedMicros != 200000 || after.Billing.ChargedMicros != 0 {
		t.Fatal("financial action did not release the original hold or changed the original content decision")
	}
	f.channel.media.mu.Lock()
	calls := f.channel.media.count
	f.channel.media.mu.Unlock()
	if calls != 1 {
		t.Fatal("appeal or financial action resubmitted image generation")
	}
	admin := deliveryAdmin(t, f.channel.cfg.DatabaseURL)
	var appealID string
	if err := admin.QueryRow(ctx, "SELECT id FROM studio_content_appeals WHERE resource_type='task' AND resource_id=$1", f.blocked.ID).Scan(&appealID); err != nil {
		t.Fatal(err)
	}
	appealPath := "/api/v1/content-appeals/" + appealID + "?project_id=" + f.owner.Project.ID
	status, before, _ := authRequest(t, f.client, f.server.URL, "GET", appealPath, "", f.owner.Project.ID, nil)
	if status != 200 {
		t.Fatal("original applicant lost access to financial outcome", status)
	}
	outsider := authClient(t)
	registerFixture(t, outsider, f.server.URL, "appeal-other@example.test")
	if s, _, _ := authRequest(t, outsider, f.server.URL, "GET", appealPath, "", f.owner.Project.ID, nil); s != 404 {
		t.Fatal("another workspace read appeal", s)
	}
	f.app.Close()
	f.server.Close()
	restoreWorkflowFixture(t, f.channel, "studio_content_appeals", "studio_content_appeal_keys", "studio_content_appeal_plans", "studio_content_appeal_receipts", "studio_content_appeal_finance_links", "studio_operations_cases", "studio_operations_receipts", "studio_ledger_reservations", "studio_ledger_journals", "studio_media_moderation_decisions", "studio_task_media_candidates")
	_, restored := f.channel.open(t)
	client := authClient(t)
	if s, _, _ := authRequest(t, client, restored.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": "appeal-owner@example.test", "password": "fixture-password-123"}); s != 200 {
		t.Fatal("could not reauthenticate restored applicant", s)
	}
	status, again, _ := authRequest(t, client, restored.URL, "GET", appealPath, "", f.owner.Project.ID, nil)
	if status != 200 || !bytes.Equal(before, again) {
		t.Fatal("restored appeal/financial outcome differs from the original receipt", status)
	}
}
