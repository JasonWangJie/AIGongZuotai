package studio

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCollaborationMembershipBodyLimitsAndProtocol(t *testing.T) {
	decode := func(op, body string) (collaborationMutation, error) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("project", "p")
		r.SetPathValue("invitation", "i")
		r.SetPathValue("user", "u")
		return decodeCollaborationMutation(httptest.NewRecorder(), r, op)
	}
	in, e := decode("invite", `{"target_user_id":"u","role":"editor","expected_version":0}`)
	if e != nil || in.Target != "u" || in.Role != "editor" || in.Project != "p" {
		t.Fatal(in, e)
	}
	for _, tc := range []struct{ op, body string }{
		{"invite", `{"target_user_id":"u","email":"person@test","role":"editor","expected_version":0}`}, {"invite", `{"target_user_id":"u","role":"editor"}`},
		{"accept", `{"expected_version":1,"target_user_id":"u"}`}, {"revoke", `{"expected_version":2,"invitation_id":"i"}{}`}, {"downgrade", strings.Repeat("a", 4097)},
	} {
		if _, e := decode(tc.op, tc.body); e == nil {
			t.Fatal("invalid collaboration payload", tc.op)
		}
	}
}

func collaborationTestAccess(a *App, p *principal, project string, write bool) (role string, v uint64, e error) {
	e = a.collaborationTx(p, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		_, role, v, e = collaborationProjectAccessSQL(ctx, tx, p, project, write)
		return e
	})
	return
}
func TestPostgresCollaborationMembershipLifecycleAndPrivateInbox(t *testing.T) {
	dsn := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required"})
	ownerC, memberC, otherC := authClient(t), authClient(t), authClient(t)
	owner := registerFixture(t, ownerC, s.URL, "collab-owner@example.test")
	member := registerFixture(t, memberC, s.URL, "collab-member@example.test")
	registerFixture(t, otherC, s.URL, "collab-other@example.test")
	older := authClient(t)
	presentationRequest(t, older, s.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": member.User.Email, "password": "fixture-password-123"}, 200)
	proj := owner.Project.ID
	host := "/api/v1/projects/" + proj + "/collaboration"
	personal := "/api/v1/collaboration"
	call := func(c *http.Client, method, path, key string, body any, status int) []byte {
		t.Helper()
		data, _ := presentationRequest(t, c, s.URL, "", method, path, key, body, status)
		return data
	}
	entry := func(c *http.Client, path, key string, body any, status int) CollaborationReceipt {
		return decode[CollaborationReceipt](t, call(c, "POST", path, key, body, status))
	}
	body := map[string]any{"target_user_id": member.User.ID, "role": "editor", "expected_version": 0}
	invKey := newID("invite")
	first := entry(ownerC, host+"/invitations", invKey, body, 201)
	if first.Role != "editor" || first.State != "PENDING" || first.Round != 1 || first.Version != 1 || !validOperationsID(first.InvitationID, "pci") || first.ExpiresAt == nil {
		t.Fatal("first invitation", first)
	}
	inbox := call(memberC, "GET", personal+"/invitations", "", nil, 200)
	if !bytes.Contains(inbox, []byte(first.InvitationID)) || bytes.Contains(inbox, []byte(owner.User.Email)) || bytes.Contains(inbox, []byte(owner.Workspace.ID)) {
		t.Fatal("recipient inbox leaked identities or failed to show the invitation")
	}
	if q := decode[CollaborationPage](t, call(otherC, "GET", personal+"/invitations", "", nil, 200)); len(q.Items) != 0 {
		t.Fatal("another account discovered this invite")
	}
	call(memberC, "GET", host+"/members", "", nil, 404)
	accept := personal + "/invitations/" + first.InvitationID + "/accept"
	aKey := newID("accept")
	call(otherC, "POST", accept, aKey, map[string]any{"expected_version": 1}, 404)
	accepted := entry(memberC, accept, aKey, map[string]any{"expected_version": 1}, 200)
	if accepted.State != "ACTIVE" || accepted.Version != 2 {
		t.Fatal("accepted", accepted)
	}
	olderP, e := a.currentPrincipal(operationsRequest(t, older, s))
	if e != nil {
		t.Fatal(e)
	}
	role, version, e := collaborationTestAccess(a, olderP, proj, true)
	if e != nil || role != "editor" || version != 2 {
		t.Fatal("valid pre-existing editor session", role, version, e)
	}
	if olderP.Workspace.ID != member.Workspace.ID || olderP.Project.ID != member.Project.ID {
		t.Fatal("shared grant changed home workspace")
	}
	for _, route := range []string{"/api/v1/billing/wallet", "/api/v1/tasks", "/api/v1/library"} {
		status, _, _ := authRequest(t, older, s.URL, "GET", route, "", proj, nil)
		if status != 404 {
			t.Fatal("active editor gained another workspace API", route, status)
		}
	}
	discover := call(older, "GET", personal+"/shared-projects", "", nil, 200)
	if !bytes.Contains(discover, []byte(proj)) || bytes.Contains(discover, []byte(owner.Workspace.ID)) {
		t.Fatal("discovery disclosed host workspace")
	}
	path := host + "/members/" + member.User.ID
	act := func(invitation string, v uint64) map[string]any {
		return map[string]any{"invitation_id": invitation, "expected_version": v}
	}
	entry(ownerC, path+"/downgrade", newID("down"), act(first.InvitationID, 2), 200)
	_, _, e = collaborationTestAccess(a, olderP, proj, true)
	requireOperation(t, e, "COLLABORATION_READ_ONLY")
	if role, _, e = collaborationTestAccess(a, olderP, proj, false); e != nil || role != "viewer" {
		t.Fatal(role, e)
	}
	revoked := entry(ownerC, path+"/revoke", newID("revoke"), act(first.InvitationID, 3), 200)
	if revoked.Version != 4 || revoked.State != "REVOKED" {
		t.Fatal(revoked)
	}
	_, _, e = collaborationTestAccess(a, olderP, proj, false)
	requireOperation(t, e, "COLLABORATION_NOT_FOUND")
	data := call(memberC, "GET", personal+"/receipt?operation=accept", aKey, nil, 200)
	if got := decode[CollaborationReceipt](t, data); got.Operation != "accept" || got.Version != 2 {
		t.Fatal("historic receipt missing")
	}
	if got := entry(memberC, accept, aKey, map[string]any{"expected_version": 1}, 200); got.Version != 2 {
		t.Fatal("changed original acceptance")
	}
	if q := decode[CollaborationPage](t, call(memberC, "GET", personal+"/shared-projects", "", nil, 200)); len(q.Items) != 0 {
		t.Fatal("old key revived access")
	}
	body["expected_version"] = 4
	body["role"] = "viewer"
	again := entry(ownerC, host+"/invitations", newID("invite"), body, 201)
	if again.Round != 2 || again.Version != 5 || again.InvitationID == first.InvitationID {
		t.Fatal("new invitation round", again)
	}
	call(ownerC, "POST", path+"/withdraw", newID("withdraw"), act(first.InvitationID, 5), 409)
	call(memberC, "POST", accept, newID("late"), map[string]any{"expected_version": 1}, 404)
	call(ownerC, "POST", host+"/invitations", invKey, body, 409)
	closed := entry(ownerC, path+"/withdraw", newID("withdraw"), act(again.InvitationID, 5), 200)
	if closed.State != "WITHDRAWN" || closed.Version != 6 {
		t.Fatal(closed)
	}
	call(memberC, "POST", personal+"/invitations/"+again.InvitationID+"/accept", newID("late"), map[string]any{"expected_version": 5}, 409)
	call(otherC, "GET", personal+"/receipt?operation=accept", aKey, nil, 404)
	// The ordinary private workspace remains the caller's only ordinary API scope.
	for _, route := range []string{"/api/v1/billing/wallet", "/api/v1/tasks", "/api/v1/library"} {
		status, _, _ := authRequest(t, older, s.URL, "GET", route, "", proj, nil)
		if status != 404 {
			t.Fatal("ordinary route accepted another workspace project", route, status)
		}
	}
}

func TestPostgresCollaborationExpiredRoundAndCapacityExit(t *testing.T) {
	dsn := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required"})
	admin := deliveryAdmin(t, dsn)
	ctx := context.Background()
	oc, mc := authClient(t), authClient(t)
	owner := registerFixture(t, oc, s.URL, "expire-owner@example.test")
	member := registerFixture(t, mc, s.URL, "expire-member@example.test")
	op, e := a.currentPrincipal(operationsRequest(t, oc, s))
	if e != nil {
		t.Fatal(e)
	}
	mp, e := a.currentPrincipal(operationsRequest(t, mc, s))
	if e != nil {
		t.Fatal(e)
	}
	// Start with an actually expired database deadline; no wall-clock sleep or
	// process timer is required. Exercise re-issue, then all capacity exits.
	old := newID("pci")
	_, e = admin.Exec(ctx, `INSERT INTO studio_collaboration_members(project_id,workspace_id,target_user_id,invitation_id,inviter_id,role,state,round,version,expires_at)
	 VALUES($1,$2,$3,$4,$5,'viewer','PENDING',1,1,clock_timestamp()-interval '1 second')`, owner.Project.ID, owner.Workspace.ID, member.User.ID, old, owner.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	inbox, e := a.collaborationList(mp, "", "invitations", "")
	if e != nil || len(inbox.Items) != 0 {
		t.Fatal("expired inbox", e)
	}
	_, _, e = a.mutateCollaboration(mp, "accept", newID("accept"), collaborationMutation{Invitation: old, ExpectedVersion: 1})
	requireOperation(t, e, "COLLABORATION_VERSION_CONFLICT")
	makeInvite := func(v uint64) (CollaborationReceipt, error) {
		r, _, e := a.mutateCollaboration(op, "invite", newID("invite"), collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Role: "editor", ExpectedVersion: v})
		return r, e
	}
	v := uint64(1)
	for round := 2; round <= 128; round++ {
		inv, e := makeInvite(v)
		if e != nil || inv.Round != uint64(round) {
			t.Fatal("re-invite", round, e)
		}
		current, _, e := a.mutateCollaboration(mp, "accept", newID("accept"), collaborationMutation{Invitation: inv.InvitationID, ExpectedVersion: inv.Version})
		if e != nil {
			t.Fatal(e)
		}
		for _, action := range []string{"downgrade", "revoke"} {
			current, _, e = a.mutateCollaboration(op, action, newID(action), collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Invitation: inv.InvitationID, ExpectedVersion: current.Version})
			if e != nil {
				t.Fatal("required exit", round, action, e)
			}
		}
		v = current.Version
	}
	_, e = makeInvite(v)
	requireOperation(t, e, "COLLABORATION_LIMIT")
	if _, _, e = collaborationTestAccess(a, mp, owner.Project.ID, false); e == nil {
		t.Fatal("final revocation lost at capacity")
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_collaboration_receipts"); n != 127*4 {
		t.Fatal(fmt.Sprintf("receipt count %d", n))
	}
}
