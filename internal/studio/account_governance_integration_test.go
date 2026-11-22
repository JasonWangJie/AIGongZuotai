package studio

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPostgresAccountGovernanceUpgrade026PreservesAppeal(t *testing.T) {
	ctx := context.Background()
	db := testDatabase(t)
	conn := deliveryAdmin(t, db)
	old := workflowOldMigrations(t, "026")
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	// A running 026 service has already bootstrapped its local workspace and project.
	_, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','prior local workspace');
	INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','prior local project');
	INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_appeal','prior@example.test','prior applicant',decode('00','hex'));
	INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_prior','usr_appeal','existing');
	INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_prior','ws_prior','existing');
	INSERT INTO studio_runs(id,workspace_id,project_id,document) VALUES('run_prior','ws_prior','prj_prior','{"status":"FAILED"}');
	INSERT INTO studio_run_moderation(run_id,policy_id,policy_version,policy_digest,policy,state,version,reason_code)
	VALUES('run_prior','old-text','v1',repeat('a',64),'{}','BLOCKED',2,'policy_block');
	INSERT INTO studio_moderation_decisions(run_id,version,state,reason_code) VALUES('run_prior',2,'BLOCKED','policy_block');
	INSERT INTO studio_content_appeals(id,workspace_id,project_id,applicant_id,resource_type,resource_id,resource_version,moderation_version,decision,decision_reason,policy_id,policy_version,policy_digest,evidence_digest,reason_code,statement)
	VALUES('capl_prior','ws_prior','prj_prior','usr_appeal','run','run_prior',2,2,'BLOCKED','policy_block','old-text','v1',repeat('a',64),repeat('b',64),'decision_error','existing open content appeal')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_users", "studio_workspaces", "studio_projects", "studio_runs", "studio_content_appeals", "studio_run_moderation", "studio_moderation_decisions", "studio_ledger_reservations"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	s, err := openPostgres(db)
	if err != nil {
		t.Fatal(err)
	}
	s.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("027 changed a pre-existing account, content appeal, decision or financial record")
	}
	if outboxCount(t, conn, "SELECT (SELECT count(*) FROM studio_account_controls)+(SELECT count(*) FROM studio_account_governance_grants)+(SELECT count(*) FROM studio_account_appeals)") != 0 {
		t.Fatal("upgrade created an account restriction, grant or appeal")
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("026 writer accepted 027")
	}
}

func TestPostgresAccountGovernanceBrowserAndRestore(t *testing.T) {
	if os.Getenv("ACCOUNT_GOVERNANCE_BROWSER_TEST") != "1" {
		t.Skip("set ACCOUNT_GOVERNANCE_BROWSER_TEST=1 with local Chromium and built assets")
	}
	const userEmail = "account-governance-user@example.test"
	const operatorEmail = "account-governance-operator@example.test"
	const fixturePassword = "fixture-password-123"
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f := newWorkflowIntegrationFixture(t)
	f.cfg.OperationsEnabled = true
	app, server := f.open(t)
	oldSession := authClient(t)
	user := registerFixture(t, oldSession, server.URL, userEmail)
	operator := registerFixture(t, authClient(t), server.URL, operatorEmail)
	draftPath := "/api/v1/projects/" + user.Project.ID + "/draft"
	if status, _, _ := authRequest(t, oldSession, server.URL, "PUT", draftPath, "original-account-draft", user.Project.ID, draftInput(0, "original account draft")); status != http.StatusOK {
		t.Fatal("could not create original private draft", status)
	}
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	originals := []string{"studio_draft_revisions", "studio_project_drafts", "studio_runs", "studio_tasks", "studio_ledger_reservations", "studio_ledger_journals", "studio_content_appeals"}
	before := schedulerOriginalSnapshot(t, admin, originals)
	grant := OperationsAdminConfig{DatabaseURL: f.cfg.DatabaseURL, UserID: operator.User.ID, TargetUserID: user.User.ID, Action: "grant", OperationKey: "browser-target-grant", Reason: "local_review", EvidenceRef: "fixture-evidence"}
	if _, err := AdministerOperations(ctx, grant); err == nil || !strings.Contains(err.Error(), "writer lock") {
		t.Fatal("running workbench allowed offline governance command", err)
	}
	app.Close()
	server.Close()
	preview, err := AdministerOperations(ctx, grant)
	if err != nil || preview.Applied || preview.PlanDigest == "" {
		t.Fatal("offline target grant preview failed", err)
	}
	grant.Apply, grant.PlanDigest = true, preview.PlanDigest
	applied, err := AdministerOperations(ctx, grant)
	if err != nil || !applied.Applied {
		t.Fatal("exact target grant could not be applied", err)
	}
	app, server = f.open(t)
	if status, _, _ := authRequest(t, oldSession, server.URL, "GET", draftPath, "", "", nil); status != http.StatusOK {
		t.Fatal("original session did not survive the offline authorization restart", status)
	}
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/account-governance-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "ACCOUNT_GOVERNANCE_BROWSER_URL="+server.URL, "ACCOUNT_GOVERNANCE_TARGET_ID="+user.User.ID,
		"ACCOUNT_GOVERNANCE_USER_EMAIL="+userEmail, "ACCOUNT_GOVERNANCE_OPERATOR_EMAIL="+operatorEmail,
		"ACCOUNT_GOVERNANCE_USER_PASSWORD="+fixturePassword, "ACCOUNT_GOVERNANCE_OPERATOR_PASSWORD="+fixturePassword)
	out, err := cmd.CombinedOutput()
	if bytes.Contains(out, []byte(fixturePassword)) {
		t.Fatal("browser diagnostics exposed a credential")
	}
	if err != nil {
		t.Fatalf("account governance browser failed: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
	if status, _, _ := authRequest(t, oldSession, server.URL, "GET", draftPath, "", "", nil); status != http.StatusUnauthorized {
		t.Fatal("original session regained access after the restriction was lifted", status)
	}
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, admin, originals)) {
		t.Fatal("account governance changed an original draft, generation, content appeal or ledger record")
	}
	f.media.mu.Lock()
	mediaCalls := f.media.count
	f.media.mu.Unlock()
	if f.chatCalls.Load() != 0 || mediaCalls != 0 {
		t.Fatal("governance triggered generation")
	}
	var appealID string
	if err := admin.QueryRow(ctx, `SELECT a.id FROM studio_account_appeals a JOIN studio_account_controls c ON c.user_id=a.target_user_id
	WHERE c.state='OPEN' AND c.version=3 AND a.state='RESOLVED' AND a.outcome='UPHELD' AND a.target_user_id=$1`, user.User.ID).Scan(&appealID); err != nil {
		t.Fatal("expected a completed appeal and subsequently lifted account", err)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_account_receipts") != 3 {
		t.Fatal("suspend, uphold and lift must each leave a single receipt")
	}
	app.Close()
	server.Close()
	tables := append(originals, "studio_account_governance_grants", "studio_account_grant_operations", "studio_account_restrictions", "studio_account_controls", "studio_account_appeals", "studio_account_appeal_idempotency", "studio_account_plans", "studio_account_receipts", "studio_account_governance_audit")
	restoreWorkflowFixture(t, f, tables...)
	_, restored := f.open(t)
	client := authClient(t)
	if status, _, _ := authRequest(t, client, restored.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": userEmail, "password": fixturePassword}); status != http.StatusOK {
		t.Fatal("restored account login failed", status)
	}
	if status, raw, _ := authRequest(t, client, restored.URL, "GET", draftPath, "", "", nil); status != http.StatusOK || !bytes.Contains(raw, []byte("original account draft")) {
		t.Fatal("original private draft lost after recovery", status)
	}
	if status, raw, _ := authRequest(t, client, restored.URL, "GET", "/api/v1/account/appeals/"+appealID, "", "", nil); status != http.StatusOK || !bytes.Contains(raw, []byte("UPHELD")) {
		t.Fatal("owner cannot read restored appeal", status)
	}
}
