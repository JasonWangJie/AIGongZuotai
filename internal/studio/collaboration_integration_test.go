package studio

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresCollaborationPairedRestoreKeepsRolesAndDraftReceipts(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("requires pg_dump and pg_restore")
	}
	f := newWorkflowIntegrationFixture(t)
	app, server := f.open(t)
	const ownerEmail = "collab-backup-owner@example.test"
	const readerEmail = "collab-backup-reader@example.test"
	const revokedEmail = "collab-backup-revoked@example.test"
	ownerClient, readerClient, revokedClient := authClient(t), authClient(t), authClient(t)
	owner := registerFixture(t, ownerClient, server.URL, ownerEmail)
	reader := registerFixture(t, readerClient, server.URL, readerEmail)
	revoked := registerFixture(t, revokedClient, server.URL, revokedEmail)
	project := owner.Project.ID
	admin := "/api/v1/projects/" + project + "/collaboration"
	draft := "/api/v1/collaboration/projects/" + project + "/draft"
	accept := func(member testIdentity, session *http.Client, key string) CollaborationReceipt {
		t.Helper()
		raw, _ := presentationRequest(t, ownerClient, server.URL, "", "POST", admin+"/invitations", "invite-"+key,
			map[string]any{"target_user_id": member.User.ID, "role": "editor", "expected_version": 0}, 201)
		inv := decode[CollaborationReceipt](t, raw)
		raw, _ = presentationRequest(t, session, server.URL, "", "POST", "/api/v1/collaboration/invitations/"+inv.InvitationID+"/accept", "accept-"+key,
			map[string]any{"expected_version": inv.Version}, 200)
		return decode[CollaborationReceipt](t, raw)
	}
	first := accept(reader, readerClient, "before-backup-reader")
	second := accept(revoked, revokedClient, "before-backup-revoke")
	input := map[string]any{"expected_revision": 0, "nodes": []DraftNode{{ID: "shared-node", Title: "两人协作", Note: "停写前的原稿"}}}
	presentationRequest(t, readerClient, server.URL, "", "PUT", draft, "member-original-save", input, 200)
	for _, change := range []struct {
		member    testIdentity
		access    CollaborationReceipt
		operation string
	}{
		{reader, first, "downgrade"}, {revoked, second, "revoke"},
	} {
		presentationRequest(t, ownerClient, server.URL, "", "POST", admin+"/members/"+change.member.User.ID+"/"+change.operation, "change-"+change.operation,
			map[string]any{"invitation_id": change.access.InvitationID, "expected_version": change.access.Version}, 200)
	}
	server.Close()
	app.Close()
	restoreWorkflowFixture(t, f, "studio_project_drafts", "studio_draft_revisions", "studio_draft_idempotency", "studio_collaboration_members", "studio_collaboration_receipts", "studio_collaboration_draft_keys")
	_, restored := f.open(t)
	login := func(email string) *http.Client {
		session := authClient(t)
		presentationRequest(t, session, restored.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
		return session
	}
	ownerClient, readerClient, revokedClient = login(ownerEmail), login(readerEmail), login(revokedEmail)
	for _, read := range []struct {
		session    *http.Client
		route, key string
	}{
		{ownerClient, "/api/v1/projects/" + project + "/draft", ""}, {readerClient, draft + "/revisions/1", ""}, {readerClient, draft + "/receipt", "member-original-save"},
	} {
		raw, _ := presentationRequest(t, read.session, restored.URL, "", "GET", read.route, read.key, nil, 200)
		existing := decode[ProjectDraft](t, raw)
		if existing.Revision != 1 || len(existing.Nodes) != 1 || existing.Nodes[0].Note != "停写前的原稿" {
			t.Fatal("shared draft or original receipt changed after restore")
		}
	}
	presentationRequest(t, readerClient, restored.URL, "", "PUT", draft, "member-original-save", input, 403)
	presentationRequest(t, revokedClient, restored.URL, "", "GET", draft, "", nil, 404)
	presentationRequest(t, revokedClient, restored.URL, "", "GET", draft+"/revisions/1", "", nil, 404)
	raw, _ := presentationRequest(t, readerClient, restored.URL, "", "GET", "/api/v1/collaboration/shared-projects", "", nil, 200)
	available := decode[CollaborationPage](t, raw)
	if len(available.Items) != 1 || available.Items[0].Role != "viewer" || available.Items[0].ProjectID != project {
		t.Fatal("restored member discovery has incorrect access")
	}
}

func TestPostgresCollaborationTwoAccountBrowser(t *testing.T) {
	if os.Getenv("COLLAB_BROWSER_TEST") != "1" {
		t.Skip("requires built workbench and a local Chromium browser")
	}
	f := newWorkflowIntegrationFixture(t)
	_, server := f.open(t)
	alice, bob := authClient(t), authClient(t)
	const aliceEmail, bobEmail = "collab-owner@example.test", "collab-member@example.test"
	owner := registerFixture(t, alice, server.URL, aliceEmail)
	member := registerFixture(t, bob, server.URL, bobEmail)
	if member.Workspace.ID == owner.Workspace.ID || member.Project.ID == owner.Project.ID {
		t.Fatal("two independent accounts and workspaces are required")
	}
	raw, _ := presentationRequest(t, alice, server.URL, "", "POST", "/api/v1/projects", "",
		map[string]string{"name": "独立私人项目"}, 201)
	sibling := decode[Project](t, raw)
	output := os.Getenv("COLLAB_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("COLLAB_BROWSER_OUTPUT must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/project-collaboration-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "COLLAB_BROWSER_URL="+server.URL, "COLLAB_BROWSER_OUTPUT="+output,
		"COLLAB_OWNER_EMAIL="+aliceEmail, "COLLAB_OWNER_PASSWORD=fixture-password-123",
		"COLLAB_MEMBER_EMAIL="+bobEmail, "COLLAB_MEMBER_PASSWORD=fixture-password-123",
		"COLLAB_PROJECT_ID="+owner.Project.ID, "COLLAB_SIBLING_PROJECT_ID="+sibling.ID,
		"COLLAB_MEMBER_PROJECT_ID="+member.Project.ID, "COLLAB_MEMBER_ID="+member.User.ID)
	result, err := cmd.CombinedOutput()
	if bytes.Contains(result, []byte("fixture-password-123")) {
		t.Fatal("browser disclosed a fixture credential")
	}
	if err != nil {
		t.Fatalf("collaboration browser: %v\n%s", err, result)
	}
	t.Log(strings.TrimSpace(string(result)))
	for _, id := range []string{sibling.ID, member.Project.ID} {
		// The team only edits the explicitly shared draft. Both untouched
		// private projects must remain at revision zero.
		c := alice
		if id == member.Project.ID {
			c = bob
		}
		raw, _ = presentationRequest(t, c, server.URL, id, "GET", "/api/v1/projects/"+id+"/draft", "", nil, 200)
		if head := decode[ProjectDraft](t, raw); head.Revision != 0 || len(head.Nodes) != 0 {
			t.Fatal("another project was edited")
		}
	}
}
