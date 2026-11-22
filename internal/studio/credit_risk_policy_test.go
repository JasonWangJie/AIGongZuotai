package studio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreditRiskPolicyCommandBounds(t *testing.T) {
	cfg := OperationsAdminConfig{WorkspaceID: newID("ws"), Action: "set", RiskPolicy: &CreditRiskPolicyLimits{100, 2}, OperationKey: "policy-01", Reason: "local_review", EvidenceRef: "ticket-01"}
	if err := validateOperationsAdmin(cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*OperationsAdminConfig){
		func(c *OperationsAdminConfig) { c.Action = "grant" },
		func(c *OperationsAdminConfig) { c.Action = "disable" },
		func(c *OperationsAdminConfig) { c.Action = "inspect" },
		func(c *OperationsAdminConfig) { c.Apply = true },
		func(c *OperationsAdminConfig) { c.TargetUserID = newID("usr") },
		func(c *OperationsAdminConfig) { c.RiskPolicy = &CreditRiskPolicyLimits{MaxLedgerMicros + 1, 1} },
		func(c *OperationsAdminConfig) { c.RiskPolicy = &CreditRiskPolicyLimits{1, 1001} },
		func(c *OperationsAdminConfig) { c.EvidenceRef = "https://example.test/private" },
	} {
		bad := cfg
		change(&bad)
		if validateOperationsAdmin(bad) == nil {
			t.Fatal("invalid risk policy command accepted")
		}
	}
}

func applyFixtureRiskPolicy(t *testing.T, cfg OperationsAdminConfig) CreditRiskPolicyMutation {
	t.Helper()
	ctx := context.Background()
	preview, err := AdministerOperations(ctx, cfg)
	if err != nil || preview.Applied || preview.PolicyChange == nil || preview.PlanDigest == "" {
		t.Fatal("risk policy dry-run failed", err)
	}
	cfg.Apply, cfg.PlanDigest = true, preview.PlanDigest
	result, err := AdministerOperations(ctx, cfg)
	if err != nil || !result.Applied || result.PolicyChange == nil || result.PolicyChange.After != preview.PolicyChange.After {
		t.Fatal("risk policy apply differs from preview", err)
	}
	return *result.PolicyChange
}

func TestPostgresCreditRiskPolicyOfflineOriginalReceipt(t *testing.T) {
	db := testDatabase(t)
	cfg := Config{DatabaseURL: db, AuthMode: "required"}
	app, srv := testApp(t, cfg)
	owner := registerFixture(t, authClient(t), srv.URL, "risk-policy@example.test")
	request := OperationsAdminConfig{DatabaseURL: db, Action: "set", WorkspaceID: owner.Workspace.ID, RiskPolicy: &CreditRiskPolicyLimits{300_000, 3}, OperationKey: "risk-set-initial", Reason: "review", EvidenceRef: "ticket:one"}
	ctx := context.Background()
	if _, err := AdministerOperations(ctx, request); err == nil || !strings.Contains(err.Error(), "writer lock") {
		t.Fatal("active control writer did not reject policy change", err)
	}
	app.Close()
	srv.Close()
	inspect := OperationsAdminConfig{DatabaseURL: db, Action: "inspect", WorkspaceID: owner.Workspace.ID, RiskPolicy: &CreditRiskPolicyLimits{}}
	r, err := AdministerOperations(ctx, inspect)
	if err != nil || r.Policy == nil || r.Policy.Enabled || r.Policy.Version != 0 {
		t.Fatal("missing policy is not disabled", err)
	}
	next := request
	next.OperationKey = "risk-set-pending"
	next.RiskPolicy = &CreditRiskPolicyLimits{800_000, 8}
	stale, err := AdministerOperations(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	first := applyFixtureRiskPolicy(t, request)
	if first.After.Version != 1 || first.Before.Enabled || !first.After.Enabled {
		t.Fatal("wrong first revision")
	}
	next.Apply, next.PlanDigest = true, stale.PlanDigest
	if _, err := AdministerOperations(ctx, next); err == nil {
		t.Fatal("stale preview mutated risk policy")
	}
	disable := request
	disable.Action, disable.OperationKey, disable.RiskPolicy = "disable", "risk-disable", &CreditRiskPolicyLimits{}
	stopped := applyFixtureRiskPolicy(t, disable)
	if stopped.After.Enabled || stopped.After.Version != 2 {
		t.Fatal("risk policy disable not recorded")
	}
	request.Apply, request.PlanDigest = true, first.PlanDigest
	replay, err := AdministerOperations(ctx, request)
	if err != nil || replay.PolicyChange == nil || !replay.PolicyChange.Replayed || replay.PolicyChange.ID != first.ID {
		t.Fatal("original operation receipt unavailable", err)
	}
	request.PlanDigest = strings.Repeat("f", 64)
	if _, err := AdministerOperations(ctx, request); err == nil {
		t.Fatal("wrong digest accepted with original operation key")
	}
	r, err = AdministerOperations(ctx, inspect)
	if err != nil || r.Policy == nil || *r.Policy != stopped.After {
		t.Fatal("old receipt re-enabled policy", err)
	}
}

func TestPostgresCreditRiskUpgrade027LeavesLegacyQuoteUnattributed(t *testing.T) {
	ctx := context.Background()
	db := testDatabase(t)
	conn := deliveryAdmin(t, db)
	old := workflowOldMigrations(t, "027")
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_prior','prior@example.test','prior',decode('00','hex'));
	INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_prior','usr_prior','existing');
	INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_prior','ws_prior','existing');
	INSERT INTO studio_ledger_quotes(id,workspace_id,project_id,resource_type,model_id,model_version,price_version,request_hash,price,reserve_micros,expires_at)
	VALUES('quo_prior','ws_prior','prj_prior','task','studio-image','v1','price-v1',decode(repeat('aa',32),'hex'),'{}',200000,clock_timestamp()+interval '10 minutes')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := conn.QueryRow(ctx, "SELECT to_jsonb(q)::text FROM studio_ledger_quotes q WHERE id='quo_prior'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	s, err := openPostgres(db)
	if err != nil {
		t.Fatal(err)
	}
	s.close()
	var owner *string
	if err := conn.QueryRow(ctx, "SELECT (to_jsonb(q)-'owner_user_id')::text,owner_user_id FROM studio_ledger_quotes q WHERE id='quo_prior'").Scan(&after, &owner); err != nil {
		t.Fatal(err)
	}
	if owner != nil || before != after || outboxCount(t, conn, "SELECT (SELECT count(*) FROM studio_credit_risk_policies)+(SELECT count(*) FROM studio_credit_risk_reviews)+(SELECT count(*) FROM studio_credit_risk_permits)") != 0 {
		t.Fatal("028 changed an old quote, guessed an owner or manufactured an active policy or exception")
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("027 writer accepted 028")
	}
}

func TestPostgresCreditRiskBrowserAndOriginalReservation(t *testing.T) {
	if os.Getenv("RISK_BROWSER_TEST") != "1" {
		t.Skip("set RISK_BROWSER_TEST=1 with local Chromium and built assets")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f := newWorkflowIntegrationFixture(t)
	f.cfg.OperationsEnabled, f.cfg.BillingMode = true, "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "price.json")
	price := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-image","kind":"image","operation":"generate","preset_id":"preset-1","artifact_micros":"200000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(price), 0600); err != nil {
		t.Fatal(err)
	}
	app, srv := f.open(t)
	const applicant = "risk-applicant@example.test"
	const reviewer = "risk-reviewer@example.test"
	const password = "fixture-password-123"
	owner := registerFixture(t, authClient(t), srv.URL, applicant)
	op := registerFixture(t, authClient(t), srv.URL, reviewer)
	httpCredit(t, app, owner.Workspace.ID, 1_000_000)
	app.Close()
	srv.Close()
	applyFixtureRiskPolicy(t, OperationsAdminConfig{DatabaseURL: f.cfg.DatabaseURL, Action: "set", WorkspaceID: owner.Workspace.ID, RiskPolicy: &CreditRiskPolicyLimits{100_000, 8}, OperationKey: "risk-browser-policy", Reason: "local", EvidenceRef: "fixture-policy"})
	grant := OperationsAdminConfig{DatabaseURL: f.cfg.DatabaseURL, Action: "grant", WorkspaceID: owner.Workspace.ID, UserID: op.User.ID, Role: "risk_reviewer", OperationKey: "risk-browser-role", Reason: "local", EvidenceRef: "fixture-role"}
	preview, err := AdministerOperations(ctx, grant)
	if err != nil {
		t.Fatal("risk reviewer dry-run failed", err)
	}
	grant.Apply, grant.PlanDigest = true, preview.PlanDigest
	if applied, err := AdministerOperations(ctx, grant); err != nil || !applied.Applied {
		t.Fatal("risk reviewer grant failed", err)
	}
	app, srv = f.open(t)
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/risk-review-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "RISK_BROWSER_URL="+srv.URL, "RISK_WORKSPACE_ID="+owner.Workspace.ID,
		"RISK_USER_EMAIL="+applicant, "RISK_REVIEWER_EMAIL="+reviewer, "RISK_USER_PASSWORD="+password, "RISK_REVIEWER_PASSWORD="+password)
	out, err := cmd.CombinedOutput()
	if bytes.Contains(out, []byte(password)) {
		t.Fatal("browser diagnostics disclosed fixture credentials")
	}
	if err != nil {
		t.Fatalf("risk browser failed: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	var taskID string
	err = db.QueryRow(ctx, `SELECT r.resource_id FROM studio_credit_risk_permits p
		JOIN studio_credit_risk_reviews v ON p.review_id=v.id AND v.state='APPROVED'
		JOIN studio_ledger_reservations r ON r.id=p.reservation_id AND r.quote_id=p.quote_id
		WHERE r.resource_type='task' AND r.workspace_id=$1 AND p.applicant_id=$2`, owner.Workspace.ID, owner.User.ID).Scan(&taskID)
	if err != nil {
		t.Fatal("could not bind the browser decision to the original accepted quote", err)
	}
	task := laneTaskStatus(t, app, taskID, "SUCCEEDED")
	if task.Billing == nil || task.Billing.ChargedMicros != 200_000 || task.Billing.ReleasedMicros != 0 || len(task.Artifacts) != 1 {
		t.Fatal("approved exception changed original price or delivery")
	}
	if outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_credit_risk_receipts") != 1 {
		t.Fatal("review caused extra reservations or decisions")
	}
	f.media.mu.Lock()
	calls := f.media.count
	f.media.mu.Unlock()
	if calls != 1 {
		t.Fatal("unexpected number of upstream generation calls", calls)
	}
	if !strings.Contains(app.prometheusText(), `studio_credit_risk_rejections_total{rule="held_limit"} 1`+"\n") {
		t.Fatal("expected exactly the single original hold-limit rejection")
	}
	app.Close()
	srv.Close()
	restoreWorkflowFixture(t, f, "studio_credit_risk_policies", "studio_credit_risk_policy_operations", "studio_credit_risk_reviews", "studio_credit_risk_review_keys", "studio_credit_risk_plans", "studio_credit_risk_receipts", "studio_credit_risk_permits", "studio_ledger_quotes", "studio_ledger_wallets", "studio_ledger_journals", "studio_ledger_reservations")
	_, restored := f.open(t)
	client := authClient(t)
	if status, _, _ := authRequest(t, client, restored.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": applicant, "password": password}); status != 200 {
		t.Fatal("restored original applicant cannot authenticate", status)
	}
	status, raw, _ := authRequest(t, client, restored.URL, "GET", "/api/v1/tasks/"+taskID, "", owner.Project.ID, nil)
	if status != 200 {
		t.Fatal("restored task unavailable", status)
	}
	if billing := decode[Task](t, raw).Billing; billing == nil || billing.ChargedMicros != 200_000 {
		t.Fatal("restored task lost original settlement")
	}
}
