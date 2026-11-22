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
)

// Use the same offline dry-run and apply entry point as cmd/operations.
func supportOfflineGrant(t *testing.T, database, action, actor, workspace, key string) OperationsRoleGrant {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := OperationsAdminConfig{DatabaseURL: database, Action: action, WorkspaceID: workspace, UserID: actor, Role: "support", OperationKey: key, Reason: "staff_assignment", EvidenceRef: "fixture:support"}
	preview, err := AdministerOperations(ctx, cfg)
	if err != nil || preview.Applied || preview.SchemaVersion != maintenanceSchemaVersion || len(preview.PlanDigest) != 64 {
		t.Fatal("offline support preview", err)
	}
	cfg.PlanDigest = preview.PlanDigest
	cfg.Apply = true
	report, err := AdministerOperations(ctx, cfg)
	if err != nil || !report.Applied || report.Result == nil {
		t.Fatal("offline support execution", err)
	}
	grant := report.Result.Grant
	if grant.Active != (action == "grant") || grant.UserID != actor || grant.WorkspaceID != workspace || grant.Role != "support" {
		t.Fatal("wrong support grant scope or state")
	}
	return grant
}

func TestPostgresSupportTicketThreeAccountBrowser(t *testing.T) {
	if os.Getenv("SUPPORT_BROWSER_TEST") != "1" {
		t.Skip("requires local Chromium and a built workbench")
	}
	f := newWorkflowIntegrationFixture(t)
	f.cfg.OperationsEnabled = true
	a, s := f.open(t)
	const ownerEmail = "support-owner@example.test"
	const agentEmail = "support-agent@example.test"
	const otherEmail = "support-outsider@example.test"
	owner := registerFixture(t, authClient(t), s.URL, ownerEmail)
	agent := registerFixture(t, authClient(t), s.URL, agentEmail)
	registerFixture(t, authClient(t), s.URL, otherEmail)
	s.Close()
	a.Close()
	supportOfflineGrant(t, f.cfg.DatabaseURL, "grant", agent.User.ID, owner.Workspace.ID, "support:browser:grant")
	_, s = f.open(t)
	output := os.Getenv("SUPPORT_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("SUPPORT_BROWSER_OUTPUT must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/support-ticket-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "SUPPORT_BROWSER_URL="+s.URL, "SUPPORT_BROWSER_OUTPUT="+output, "SUPPORT_WORKSPACE_ID="+owner.Workspace.ID,
		"SUPPORT_OWNER_EMAIL="+ownerEmail, "SUPPORT_OWNER_PASSWORD=fixture-password-123", "SUPPORT_AGENT_EMAIL="+agentEmail, "SUPPORT_AGENT_PASSWORD=fixture-password-123", "SUPPORT_OTHER_EMAIL="+otherEmail, "SUPPORT_OTHER_PASSWORD=fixture-password-123")
	raw, err := cmd.CombinedOutput()
	if bytes.Contains(raw, []byte("fixture-password-123")) {
		t.Fatal("browser diagnostics contained fixture credentials")
	}
	if err != nil {
		t.Fatalf("support browser: %v\n%s", err, raw)
	}
	t.Log(strings.TrimSpace(string(raw)))
	d := deliveryAdmin(t, f.cfg.DatabaseURL)
	var matched int
	err = d.QueryRow(ctx, `SELECT count(*) FROM studio_support_tickets t WHERE t.owner_user_id=$1 AND t.workspace_id=$2 AND t.state='CLOSED'
 AND EXISTS (SELECT FROM studio_support_messages m WHERE m.ticket_id=t.id AND m.actor_user_id=$3 AND m.operation='reply')
 AND EXISTS (SELECT FROM studio_support_messages m WHERE m.ticket_id=t.id AND m.actor_user_id=$1 AND m.operation='followup')`, owner.User.ID, owner.Workspace.ID, agent.User.ID).Scan(&matched)
	if err != nil || matched == 0 {
		t.Fatal("closed browser conversation lacks the original requester's follow-up or the independent agent's reply", err)
	}
	if outboxCount(t, d, "SELECT count(*) FROM studio_ledger_postings") != 0 || outboxCount(t, d, "SELECT count(*) FROM studio_tasks") != 0 {
		t.Fatal("support changed entitlements or started a media task")
	}
}

func TestPostgresSupportTicketRestoreHonorsRevocation(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("requires pg_dump and pg_restore")
	}
	f := newWorkflowIntegrationFixture(t)
	f.cfg.OperationsEnabled = true
	a, s := f.open(t)
	const ownerEmail = "support-restore-owner@example.test"
	const agentEmail = "support-restore-agent@example.test"
	owner := registerFixture(t, authClient(t), s.URL, ownerEmail)
	agent := registerFixture(t, authClient(t), s.URL, agentEmail)
	s.Close()
	a.Close()
	g := supportOfflineGrant(t, f.cfg.DatabaseURL, "grant", agent.User.ID, owner.Workspace.ID, "support:restore:grant")
	login := func(t *testing.T, server *httptest.Server, email string) *http.Client {
		t.Helper()
		c := authClient(t)
		presentationRequest(t, c, server.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
		return c
	}
	a, s = f.open(t)
	user, staff := login(t, s, ownerEmail), login(t, s, agentEmail)
	const api = "/api/v1/support/tickets"
	const staffAPI = "/api/v1/operations/support/tickets"
	raw, _ := presentationRequest(t, user, s.URL, "", "POST", api, "support-restore-original", SupportTicketCreate{Category: "account", Subject: "资料下载协助", Body: "需要确认我自己的资料范围。"}, 201)
	first := decode[SupportTicketDetail](t, raw)
	raw, _ = presentationRequest(t, staff, s.URL, "", "POST", staffAPI+"/"+first.ID+"/reply", "support-restore-reply", map[string]any{"workspace_id": owner.Workspace.ID, "password": "fixture-password-123", "body": "账户安全页可选择个人资料分区。", "expected_version": 1}, 200)
	reply := decode[SupportTicketDetail](t, raw)
	if len(first.Messages) != 1 || len(reply.Messages) != 2 || first.ID != reply.ID || reply.Version != 2 || first.Messages[0] != reply.Messages[0] {
		t.Fatal("support reply did not preserve original message")
	}
	s.Close()
	a.Close()
	revoked := supportOfflineGrant(t, f.cfg.DatabaseURL, "revoke", agent.User.ID, owner.Workspace.ID, "support:restore:revoke")
	if revoked.ID != g.ID || revoked.Version <= g.Version || revoked.Active {
		t.Fatal("scope was not revoked before backup")
	}
	restoreWorkflowFixture(t, f, "studio_support_tickets", "studio_support_messages", "studio_support_receipts", "studio_operations_grants", "studio_operations_role_operations")
	_, s = f.open(t)
	user, staff = login(t, s, ownerEmail), login(t, s, agentEmail)
	raw, _ = presentationRequest(t, user, s.URL, "", "GET", api+"/"+first.ID, "", nil, 200)
	if detail := decode[SupportTicketDetail](t, raw); !reflect.DeepEqual(detail, reply) {
		t.Fatal("restored conversation changed")
	}
	raw, _ = presentationRequest(t, user, s.URL, "", "GET", api+"/receipt?operation=create", "support-restore-original", nil, 200)
	if receipt := decode[SupportTicketDetail](t, raw); !reflect.DeepEqual(receipt, first) {
		t.Fatal("restored first receipt included a later reply or changed")
	}
	presentationRequest(t, staff, s.URL, "", "GET", staffAPI+"/"+first.ID+"?workspace_id="+owner.Workspace.ID, "", nil, 404)
	presentationRequest(t, staff, s.URL, "", "GET", staffAPI+"/receipt?operation=reply&workspace_id="+owner.Workspace.ID, "support-restore-reply", nil, 404)
	raw, _ = presentationRequest(t, user, s.URL, "", "POST", api+"/"+first.ID+"/close", "support-restore-close", map[string]uint64{"expected_version": 2}, 200)
	if closed := decode[SupportTicketDetail](t, raw); closed.State != "CLOSED" || closed.Version != 3 || len(closed.Messages) != 3 {
		t.Fatal("owner could not close the restored ticket")
	}
}
