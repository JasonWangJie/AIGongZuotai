package studio

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountRestrictedRoutesAndTargetGrantInputs(t *testing.T) {
	for _, p := range []string{"/api/v1/account/access", "/api/v1/account/appeals", "/api/v1/account/appeals/receipt", "/api/v1/account/appeals/" + newID("aapl")} {
		if !limitedAccountRoute("GET", p) {
			t.Fatal("blocked own account path", p)
		}
	}
	for _, p := range []string{"/api/v1/account/appeals/other", "/api/v1/account/appeals/../library", "/api/v1/bootstrap", "/api/v1/content-appeals", "/api/v1/operations/account-targets", "/api/v1/runs"} {
		if limitedAccountRoute("GET", p) || limitedAccountRoute("POST", p) {
			t.Fatal("ordinary path allowed", p)
		}
	}
	c := OperationsAdminConfig{Action: "grant", UserID: newID("usr"), TargetUserID: newID("usr"), OperationKey: "binding:1", Reason: "manual_review", EvidenceRef: "evidence:1"}
	if err := validateOperationsAdmin(c); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*OperationsAdminConfig){func(x *OperationsAdminConfig) { x.UserID = x.TargetUserID }, func(x *OperationsAdminConfig) { x.Role = "moderator" }, func(x *OperationsAdminConfig) { x.WorkspaceID = newID("ws") }} {
		bad := c
		edit(&bad)
		if validateOperationsAdmin(bad) == nil {
			t.Fatal("confused account/workspace authority")
		}
	}
}

func TestPostgresAccountGovernanceOfflineGrantReplay(t *testing.T) {
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required"})
	op := registerFixture(t, authClient(t), s.URL, "account-grant-operator@example.test")
	target := registerFixture(t, authClient(t), s.URL, "account-grant-target@example.test")
	c := OperationsAdminConfig{DatabaseURL: db, Action: "grant", UserID: op.User.ID, TargetUserID: target.User.ID, OperationKey: "account-grant:one", Reason: "review_assignment", EvidenceRef: "ticket:original"}
	ctx := context.Background()
	if _, e := AdministerOperations(ctx, c); e == nil || !strings.Contains(e.Error(), "writer lock") {
		t.Fatal("active control writer allowed offline mutation", e)
	}
	a.Close()
	s.Close()
	preview, e := AdministerOperations(ctx, c)
	if e != nil || preview.TargetResult == nil || !preview.TargetResult.Changed || preview.Applied {
		t.Fatal("grant preview", preview, e)
	}
	inspect := OperationsAdminConfig{DatabaseURL: db, Action: "inspect", UserID: c.UserID, TargetUserID: c.TargetUserID}
	r, e := AdministerOperations(ctx, inspect)
	if e != nil || r.TargetGrant != nil {
		t.Fatal("preview persisted grant", r, e)
	}
	c.Apply = true
	c.PlanDigest = preview.PlanDigest
	applied, e := AdministerOperations(ctx, c)
	if e != nil || !applied.Applied || !applied.TargetResult.Grant.Active {
		t.Fatal("grant not applied", applied, e)
	}
	revoke := c
	revoke.Action = "revoke"
	revoke.OperationKey = "account-grant:two"
	revoke.Apply = false
	revoke.PlanDigest = ""
	r, e = AdministerOperations(ctx, revoke)
	if e != nil {
		t.Fatal(e)
	}
	revoke.Apply = true
	revoke.PlanDigest = r.PlanDigest
	r, e = AdministerOperations(ctx, revoke)
	if e != nil || r.TargetResult.Grant.Active || r.TargetResult.Grant.Version != 2 {
		t.Fatal("revocation", r, e)
	}
	r, e = AdministerOperations(ctx, c)
	if e != nil || !r.TargetResult.Replayed || r.TargetResult.ID != applied.TargetResult.ID {
		t.Fatal("lost original operation receipt", r, e)
	}
	r, e = AdministerOperations(ctx, inspect)
	if e != nil || r.TargetGrant.Active {
		t.Fatal("grant replay restored access")
	}
	admin := deliveryAdmin(t, db)
	if outboxCount(t, admin, "SELECT count(*) FROM studio_account_governance_audit WHERE actor_user_id IS NULL AND target_user_id=$1", target.User.ID) != 2 {
		t.Fatal("offline audit missing or falsely attributed")
	}
}

func TestPostgresAccountGovernanceAdmissionAndOpenStream(t *testing.T) {
	a, s, client, user, db := newModeratedBillingFixture(t, true)
	operator := authClient(t)
	op := registerFixture(t, operator, s.URL, "target-control-officer@example.test")
	admin := deliveryAdmin(t, db)
	if _, e := admin.Exec(context.Background(), "INSERT INTO studio_account_governance_grants(id,operator_user_id,target_user_id) VALUES($1,$2,$3)", newID("agnt"), op.User.ID, user.User.ID); e != nil {
		t.Fatal(e)
	}
	call := func(c *http.Client, want int, method, path, key string, input any) []byte {
		t.Helper()
		code, body, _ := authRequest(t, c, s.URL, method, path, key, "", input)
		if code != want {
			t.Fatalf("%s %s got %d, want %d: %s", method, path, code, want, body)
		}
		return body
	}
	scope := "/api/v1/operations/account-targets/" + user.User.ID
	call(operator, 200, "GET", scope, "", nil)
	p := decode[AccountPlan](t, call(operator, 201, "POST", scope+"/plans", "", AccountPlanInput{Action: "suspend", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "ticket:account", PublicReply: "申请人工复核"}))
	entered, release := make(chan struct{}, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	a.moderation = executionHTTPModerator(t, func(w http.ResponseWriter, r *http.Request, in moderationHTTPRequest) {
		if in.Phase == "input" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		allowExecutionHTTP(w, in)
	})
	provider := &moderatedExecutionChat{text: "must not be invoked"}
	a.chat = provider
	input := RunInput{ProjectID: user.Project.ID, ModelID: "studio-chat", Input: "safe original request"}
	q := httpBillingQuote(t, client, s, user.Project.ID, "run", input)
	input.QuoteID = q.ID
	streamClient := &http.Client{Jar: client.Jar, Timeout: 14 * time.Second}
	stream, e := streamClient.Get(s.URL + "/api/v1/workspaces/" + user.Workspace.ID + "/events")
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatal("initial SSE", stream.StatusCode)
	}
	ended := make(chan error, 1)
	go func() { _, e := io.Copy(io.Discard, stream.Body); ended <- e }()
	status := make(chan int, 1)
	go func() {
		code, _, _ := authRequest(t, client, s.URL, "POST", "/api/v1/runs", "blocked-during-moderation", user.Project.ID, input)
		status <- code
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("input reviewer not reached")
	}
	execute := map[string]any{"plan_id": p.ID, "plan_digest": p.PlanDigest, "password": "fixture-password-123"}
	call(operator, 200, "POST", scope+"/execute", "", execute)
	access := decode[AccountAccess](t, call(operator, 200, "GET", scope, "", nil))
	if access.Restriction == nil || access.State != "SUSPENDED" {
		t.Fatal("target suspension missing", access)
	}
	call(client, 401, "GET", "/api/v1/models", "", nil)
	limited := authClient(t)
	login := call(limited, 200, "POST", "/api/v1/auth/login", "", map[string]string{"email": user.User.Email, "password": "fixture-password-123"})
	if !strings.Contains(string(login), `"account_restricted":true`) || strings.Contains(string(login), `"projects"`) || strings.Contains(string(login), `"operations"`) {
		t.Fatal("limited identity returned creative or operational scope")
	}
	call(limited, 200, "GET", "/api/v1/auth/security/events", "", nil)
	for _, path := range []string{"/api/v1/bootstrap", "/api/v1/models", "/api/v1/operations/account-targets", "/api/v1/billing/wallet"} {
		b := call(limited, 403, "GET", path, "", nil)
		if !strings.Contains(string(b), "ACCOUNT_RESTRICTED") {
			t.Fatal("wrong access status", path, string(b))
		}
	}
	call(limited, 200, "GET", "/api/v1/account/access", "", nil)
	ap := decode[AccountAppeal](t, call(limited, 201, "POST", "/api/v1/account/appeals", "limited-user-appeal", map[string]string{"restriction_id": access.Restriction.ID, "reason_code": "request_review", "statement": "Please review the recorded restriction"}))
	call(limited, 200, "GET", "/api/v1/account/appeals/receipt", "limited-user-appeal", nil)
	lift := decode[AccountPlan](t, call(operator, 201, "POST", scope+"/plans", "", AccountPlanInput{Action: "lift", ExpectedVersion: access.Version, RestrictionID: access.Restriction.ID, ReasonCode: "review_complete", EvidenceRef: "ticket:lift", PublicReply: "该限制已解除"}))
	call(operator, 200, "POST", scope+"/execute", "", map[string]string{"plan_id": lift.ID, "plan_digest": lift.PlanDigest, "password": "fixture-password-123"})
	call(limited, 401, "GET", "/api/v1/account/access", "", nil)
	close(release)
	select {
	case code := <-status:
		if code != 401 {
			t.Fatalf("old authorized request committed or returned wrong status after suspend/lift: %d", code)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("unresolved in-flight generation")
	}
	if provider.calls.Load() != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_runs") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", q.ID) != 1 {
		t.Fatal("revoked pre-admission request generated, consumed quote or froze credit")
	}
	select {
	case e := <-ended:
		if e != nil {
			t.Fatalf("SSE ended on client timeout or error instead of server EOF: %v", e)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("old stream outlived periodic session check")
	}
	fresh := authClient(t)
	call(fresh, 200, "POST", "/api/v1/auth/login", "", map[string]string{"email": user.User.Email, "password": "fixture-password-123"})
	call(fresh, 200, "GET", "/api/v1/models", "", nil)
	final := decode[AccountAppeal](t, call(fresh, 200, "GET", "/api/v1/account/appeals/"+ap.ID, "", nil))
	if final.Outcome != "LIFTED" {
		t.Fatal("lost closed appeal", final)
	}
}
