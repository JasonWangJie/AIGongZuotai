package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestAccountGovernanceBoundedPlanAndProjection(t *testing.T) {
	in := AccountPlanInput{Action: "suspend", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "fixture-123", PublicReply: "请核对账户"}
	if !validAccountPlan(in) {
		t.Fatal("valid suspend plan rejected")
	}
	for _, fn := range []func(*AccountPlanInput){func(p *AccountPlanInput) { p.Action = "grant" }, func(p *AccountPlanInput) { p.RestrictionID = newID("arst") }, func(p *AccountPlanInput) { p.ExpectedVersion = 0 }, func(p *AccountPlanInput) { p.EvidenceRef = "https://private.invalid" }, func(p *AccountPlanInput) { p.PublicReply = strings.Repeat("a", 1025) }} {
		bad := in
		fn(&bad)
		if validAccountPlan(bad) {
			t.Fatal("invalid plan accepted")
		}
	}
	z := accountControlPlanSnapshot{Plan: AccountPlan{TargetUserID: newID("usr")}, Input: in, Grant: accountTargetGrant{ID: newID("agnt"), Version: 1}, OpenAppeal: generationOpenAppeal{ID: newID("aapl"), Version: 1}}
	before := z.digest()
	z.OpenAppeal = generationOpenAppeal{}
	if z.digest() == before {
		t.Fatal("plan did not bind open appeal set")
	}
	b, _ := json.Marshal(z.Plan)
	if strings.Contains(string(b), "fixture-123") || strings.Contains(string(b), "agnt_") {
		t.Fatal("private grant or evidence exposed in plan")
	}
}

func TestPostgresAccountGovernanceRoundAppealsAndScopedReceipts(t *testing.T) {
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required", OperationsEnabled: true})
	owner, operator, third := authClient(t), authClient(t), authClient(t)
	target := registerFixture(t, owner, s.URL, "governance-target@example.test")
	op := registerFixture(t, operator, s.URL, "governance-operator@example.test")
	other := registerFixture(t, third, s.URL, "governance-unscoped@example.test")
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	actorOf := func(c *http.Client) *principal {
		p, e := a.currentPrincipal(operationsRequest(t, c, s))
		if e != nil || p == nil {
			t.Fatal("missing fixture login", e)
		}
		return p
	}
	p, oldOwner := actorOf(operator), actorOf(owner)
	operationsGrantFixture(t, a, op.User.ID, target.Workspace.ID, "moderator")
	_, e := a.accountGovernanceAccess(p, target.User.ID, true)
	requireOperation(t, e, "OPERATIONS_NOT_FOUND")
	grant := newID("agnt")
	if _, e = admin.Exec(ctx, `INSERT INTO studio_account_governance_grants(id,operator_user_id,target_user_id) VALUES($1,$2,$3)`, grant, op.User.ID, target.User.ID); e != nil {
		t.Fatal(e)
	}
	_, e = a.accountGovernanceAccess(p, other.User.ID, true)
	requireOperation(t, e, "OPERATIONS_NOT_FOUND")
	absent, e := a.accountGovernanceAccess(oldOwner, target.User.ID, false)
	if e != nil || absent.State != "OPEN" || absent.Version != 1 || absent.Restriction != nil {
		t.Fatal(absent, e)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_account_controls"); n != 0 {
		t.Fatal("status read wrote OPEN control")
	}
	proof, e := a.verifyOperationsPassword(operationsRequest(t, operator, s), "fixture-password-123")
	if e != nil {
		t.Fatal(e)
	}
	preview := func(in AccountPlanInput) AccountPlan {
		pl, e := a.previewAccountControl(p, target.User.ID, in)
		if e != nil {
			t.Fatal(e)
		}
		return pl
	}
	execute := func(pl AccountPlan) AccountReceipt {
		receipt, e := a.executeAccountControl(p, target.User.ID, pl.ID, pl.PlanDigest, proof)
		if e != nil {
			t.Fatal(e)
		}
		return receipt
	}
	suspend := AccountPlanInput{Action: "suspend", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "test-private-ref", PublicReply: "限制期间可登录申诉"}
	pl := preview(suspend)
	if _, e = a.accountGovernanceAccess(oldOwner, target.User.ID, false); e != nil {
		t.Fatal("preview invalidated an existing session", e)
	}
	_, e = a.executeAccountControl(p, target.User.ID, pl.ID, pl.PlanDigest, nil)
	requireOperation(t, e, "OPERATIONS_REAUTH_REQUIRED")
	// Revocation followed by a fresh grant cannot revive the old preview.
	if _, e = admin.Exec(ctx, "UPDATE studio_account_governance_grants SET active=false,version=version+1 WHERE id=$1", grant); e != nil {
		t.Fatal(e)
	}
	_, e = a.executeAccountControl(p, target.User.ID, pl.ID, pl.PlanDigest, proof)
	requireOperation(t, e, "OPERATIONS_NOT_FOUND")
	if _, e = admin.Exec(ctx, "UPDATE studio_account_governance_grants SET active=true,version=version+1 WHERE id=$1", grant); e != nil {
		t.Fatal(e)
	}
	_, e = a.executeAccountControl(p, target.User.ID, pl.ID, pl.PlanDigest, proof)
	requireOperation(t, e, "ACCOUNT_CONTROL_CHANGED")
	pl = preview(suspend)
	receipt := execute(pl)
	if receipt.Effect.ToState != "SUSPENDED" || receipt.Effect.Version != 2 {
		t.Fatal(receipt)
	}
	_, e = a.accountGovernanceAccess(oldOwner, target.User.ID, false)
	requireOperation(t, e, "AUTH_REQUIRED")
	if code, _, _ := authRequest(t, owner, s.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": target.User.Email, "password": "fixture-password-123"}); code != 200 {
		t.Fatal("restricted login", code)
	}
	restricted := actorOf(owner)
	access, e := a.accountGovernanceAccess(restricted, target.User.ID, false)
	if e != nil || !access.CanAppeal || access.Restriction.ID != pl.RestrictionID {
		t.Fatal(access, e)
	}
	lift := AccountPlanInput{Action: "lift", RestrictionID: pl.RestrictionID, ExpectedVersion: 2, ReasonCode: "review_complete", EvidenceRef: "fixture-lift", PublicReply: "已解除本轮限制"}
	staleLift := preview(lift)
	key := newID("key")
	in := GenerationAppealInput{ReasonCode: "restriction_error", Statement: "仅申请人及当前指定审核人可读"}
	appeal, replay, e := a.createAccountAppeal(restricted, pl.RestrictionID, in, key)
	if e != nil || replay || appeal.Statement != in.Statement {
		t.Fatal(appeal, e)
	}
	same, replay, e := a.createAccountAppeal(restricted, pl.RestrictionID, in, key)
	if e != nil || !replay || same.ID != appeal.ID {
		t.Fatal("exact appeal replay", e)
	}
	alt := in
	alt.Statement += "变动"
	_, _, e = a.createAccountAppeal(restricted, pl.RestrictionID, alt, key)
	requireOperation(t, e, "IDEMPOTENCY_CONFLICT")
	_, _, e = a.createAccountAppeal(restricted, pl.RestrictionID, in, newID("key"))
	requireOperation(t, e, "ACCOUNT_APPEAL_EXISTS")
	list, e := a.listAccountAppeals(p, target.User.ID, "OPEN", "", 20, true)
	if e != nil || len(list.Items) != 1 || list.Items[0].Statement != "" {
		t.Fatal("private projection", list, e)
	}
	_, e = a.readAccountAppeal(actorOf(third), target.User.ID, appeal.ID, false)
	requireOperation(t, e, "OPERATIONS_NOT_FOUND")
	_, e = a.executeAccountControl(p, target.User.ID, staleLift.ID, staleLift.PlanDigest, proof)
	requireOperation(t, e, "ACCOUNT_CONTROL_CHANGED")
	uphold := AccountPlanInput{Action: "uphold", ExpectedVersion: 2, RestrictionID: pl.RestrictionID, AppealID: appeal.ID, ExpectedAppealVersion: 1, ReasonCode: "policy_concern", EvidenceRef: "fixture-review", PublicReply: "根据本轮记录维持限制"}
	upheld := execute(preview(uphold))
	if upheld.Effect.Version != 2 || upheld.Effect.AffectedAppeals != 1 {
		t.Fatal(upheld)
	}
	reviewed, e := a.accountAppealReceipt(restricted, key)
	if e != nil || reviewed.Outcome != "UPHELD" || reviewed.PublicReply != uphold.PublicReply {
		t.Fatal(reviewed, e)
	}
	lifted := execute(preview(lift))
	if lifted.Effect.Version != 3 || lifted.Effect.ToState != "OPEN" || lifted.Effect.AffectedAppeals != 0 {
		t.Fatal(lifted)
	}
	_, e = a.accountGovernanceAccess(restricted, target.User.ID, false)
	requireOperation(t, e, "AUTH_REQUIRED")
	suspend.ExpectedVersion = 3
	next := preview(suspend)
	execute(next)
	if next.RestrictionID == pl.RestrictionID {
		t.Fatal("later round reused first restriction")
	}
	_, e = a.previewAccountControl(p, target.User.ID, lift)
	requireOperation(t, e, "ACCOUNT_CONTROL_CHANGED")
	oldReceipt, e := a.accountControlReceipt(p, target.User.ID, pl.ID)
	if e != nil || oldReceipt.ID != receipt.ID {
		t.Fatal("immutable receipt", e)
	}
	_, e = a.accountControlReceipt(p, other.User.ID, pl.ID)
	requireOperation(t, e, "OPERATIONS_NOT_FOUND")
}

func TestPostgresAccountGovernanceWaitPastDeadlineRollsBackSessionRevocation(t *testing.T) {
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required", OperationsEnabled: true})
	owner, operator := authClient(t), authClient(t)
	target := registerFixture(t, owner, s.URL, "governance-deadline@example.test")
	op := registerFixture(t, operator, s.URL, "governance-deadline-operator@example.test")
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	if _, e := admin.Exec(ctx, "INSERT INTO studio_account_governance_grants(id,operator_user_id,target_user_id) VALUES($1,$2,$3)", newID("agnt"), op.User.ID, target.User.ID); e != nil {
		t.Fatal(e)
	}
	p, e := a.currentPrincipal(operationsRequest(t, operator, s))
	if e != nil {
		t.Fatal(e)
	}
	proof, e := a.verifyOperationsPassword(operationsRequest(t, operator, s), "fixture-password-123")
	if e != nil {
		t.Fatal(e)
	}
	plan, e := a.previewAccountControl(p, target.User.ID, AccountPlanInput{Action: "suspend", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "test-deadline", PublicReply: "核对限制"})
	if e != nil {
		t.Fatal(e)
	}
	// Insert a separate short-lived fixture preview. The production immutable
	// preview and exact-digest validation remain active for the executed record.
	var z accountControlPlanSnapshot
	if e = admin.QueryRow(ctx, "SELECT snapshot FROM studio_account_plans WHERE id=$1", plan.ID).Scan(&z); e != nil {
		t.Fatal(e)
	}
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM studio_sessions WHERE user_id=$1 FOR UPDATE", target.User.ID); e != nil {
		t.Fatal(e)
	}
	observer := deliveryAdmin(t, db)
	if e = observer.QueryRow(ctx, "SELECT clock_timestamp()+interval '1.5 seconds'").Scan(&z.Plan.ExpiresAt); e != nil {
		t.Fatal(e)
	}
	z.Plan.ExpiresAt = z.Plan.ExpiresAt.UTC()
	z.Plan.ID = newID("aplan")
	z.Plan.PlanDigest = z.digest()
	if _, e = observer.Exec(ctx, "INSERT INTO studio_account_plans(id,target_user_id,actor_id,action,snapshot,digest,expires_at) VALUES($1,$2,$3,'suspend',$4,$5,$6)", z.Plan.ID, target.User.ID, p.User.ID, z, z.Plan.PlanDigest, z.Plan.ExpiresAt); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.executeAccountControl(p, target.User.ID, z.Plan.ID, z.Plan.PlanDigest, proof)
		done <- err
	}()
	awaitRunLeaseWaiters(t, observer, int32(a.store.conn.PgConn().PID()))
	var expired bool
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline) && !expired; {
		if e = observer.QueryRow(ctx, "SELECT clock_timestamp()>$1::timestamptz", z.Plan.ExpiresAt).Scan(&expired); e != nil {
			t.Fatal(e)
		}
		if !expired {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !expired {
		t.Fatal("deadline not reached")
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	requireOperation(t, <-done, "ACCOUNT_PLAN_EXPIRED")
	if outboxCount(t, observer, "SELECT count(*) FROM studio_account_controls") != 0 || outboxCount(t, observer, "SELECT count(*) FROM studio_sessions WHERE user_id=$1", target.User.ID) != 1 || outboxCount(t, observer, "SELECT count(*) FROM studio_account_receipts") != 0 {
		t.Fatal("expired action left account, session or receipt effects")
	}
	var used *time.Time
	if e = observer.QueryRow(ctx, "SELECT consumed_at FROM studio_account_plans WHERE id=$1", z.Plan.ID).Scan(&used); errors.Is(e, pgx.ErrNoRows) || e != nil || used != nil {
		t.Fatal("expired plan changed", e)
	}
}
