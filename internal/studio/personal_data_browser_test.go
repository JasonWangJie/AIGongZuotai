package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresPersonalDataBrowserDownloads(t *testing.T) {
	if os.Getenv("ACCOUNT_EXPORT_BROWSER_TEST") != "1" {
		t.Skip("requires local Chromium and a built workbench")
	}
	f := newWorkflowIntegrationFixture(t)
	f.cfg.OperationsEnabled = true
	app, server := f.open(t)
	const normalEmail = "export-reader@example.test"
	const limitedEmail = "export-applicant@example.test"
	normal, restricted, operator := authClient(t), authClient(t), authClient(t)
	reader := registerFixture(t, normal, server.URL, normalEmail)
	target := registerFixture(t, restricted, server.URL, limitedEmail)
	op := registerFixture(t, operator, server.URL, "export-scope-operator@example.test")
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Seed a multi-page history using the same insertion routine as account
	// login. The browser additionally creates genuine authenticated events.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for i := 0; i < 110; i++ {
		if err := insertSecurityEventSQL(ctx, tx, reader.User.ID, "account.login"); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "INSERT INTO studio_account_governance_grants(id,operator_user_id,target_user_id) VALUES($1,$2,$3)", newID("agnt"), op.User.ID, target.User.ID); err != nil {
		t.Fatal(err)
	}
	opr := operationsRequest(t, operator, server)
	principal, err := app.currentPrincipal(opr)
	if err != nil {
		t.Fatal(err)
	}
	pwd, err := app.verifyOperationsPassword(opr, "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.previewAccountControl(principal, target.User.ID, AccountPlanInput{Action: "suspend", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "test-private-export-evidence", PublicReply: "可查看本人资料"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.executeAccountControl(principal, target.User.ID, plan.ID, plan.PlanDigest, pwd); err != nil {
		t.Fatal(err)
	}
	presentationRequest(t, restricted, server.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": limitedEmail, "password": "fixture-password-123"}, 200)
	limited, err := app.currentPrincipal(operationsRequest(t, restricted, server))
	if err != nil || limited == nil || !limited.AccountRestricted {
		t.Fatal("restricted session required")
	}
	const privateStatement = "这是本人的复核说明。"
	appeal, _, err := app.createAccountAppeal(limited, plan.RestrictionID, GenerationAppealInput{ReasonCode: "restriction_error", Statement: privateStatement}, "account-export-own-appeal")
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("ACCOUNT_EXPORT_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("ACCOUNT_EXPORT_BROWSER_OUTPUT must be absolute")
	}
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/account-data-export-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "ACCOUNT_EXPORT_BROWSER_URL="+server.URL, "ACCOUNT_EXPORT_BROWSER_OUTPUT="+output,
		"ACCOUNT_EXPORT_USER_EMAIL="+normalEmail, "ACCOUNT_EXPORT_USER_PASSWORD=fixture-password-123",
		"ACCOUNT_EXPORT_RESTRICTED_EMAIL="+limitedEmail, "ACCOUNT_EXPORT_RESTRICTED_PASSWORD=fixture-password-123")
	raw, err := cmd.CombinedOutput()
	if bytes.Contains(raw, []byte("fixture-password-123")) {
		t.Fatal("browser disclosed a fixture credential")
	}
	if err != nil {
		t.Fatalf("account export browser: %v\n%s", err, raw)
	}
	t.Log(strings.TrimSpace(string(raw)))
	load := func(name, section, user string) accountExportDocument {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || len(body) == 0 || len(body) > 512<<10 {
			t.Fatal("missing or oversized export", name)
		}
		if bytes.Contains(body, []byte("test-private-export-evidence")) || bytes.Contains(body, []byte("fixture-password-123")) {
			t.Fatal("export leaked internal evidence or a password")
		}
		page := decode[accountExportDocument](t, body)
		if page.Format != "studio.account-export.v1" || page.Section != section || page.AccountID != user || page.CreatedThrough.IsZero() || page.ReadAt.IsZero() {
			t.Fatal("unexpected export, scope or clock", name)
		}
		return page
	}
	profile := load("profile.json", "profile", reader.User.ID)
	if len(profile.Items) != 1 || profile.NextCursor != nil || decode[accountExportProfile](t, profile.Items[0]).Email != normalEmail {
		t.Fatal("wrong profile download")
	}
	ap := load("restricted-appeals.json", "account_appeals", target.User.ID)
	if len(ap.Items) != 1 {
		t.Fatal("expected only the applicant's appeal")
	}
	exported := decode[accountExportAppeal](t, ap.Items[0])
	if exported.ID != appeal.ID || exported.Statement != privateStatement || exported.RestrictionID != plan.RestrictionID {
		t.Fatal("incorrect applicant record")
	}
	first := load("security-events-page-1.json", "security_events", reader.User.ID)
	second := load("security-events-page-2.json", "security_events", reader.User.ID)
	if first.NextCursor == nil || second.NextCursor != nil || !first.CreatedThrough.Equal(second.CreatedThrough) || len(first.Items) != 100 || len(second.Items) < 10 {
		t.Fatal("invalid download page boundaries")
	}
	var ids []string
	for _, data := range append(first.Items, second.Items...) {
		var event SecurityEvent
		if err = json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, event.ID)
	}
	rows, err := db.Query(ctx, "SELECT id FROM studio_security_events WHERE user_id=$1 AND created_at<=$2 ORDER BY created_at DESC,id DESC", reader.User.ID, first.CreatedThrough)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !reflect.DeepEqual(ids, wanted) {
		t.Fatal("downloaded pages differ from this actor's original database history")
	}
}
