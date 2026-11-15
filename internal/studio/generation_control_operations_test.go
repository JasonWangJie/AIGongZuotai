package studio

import (
	"context"
	"net/http"
	"testing"
)

func generationTargetPrincipalFixture(t *testing.T, f operationsCasesFixture) *principal {
	t.Helper()
	client := authClient(t)
	code, _, _ := authRequest(t, client, f.server.URL, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"email": f.target.User.Email, "password": "fixture-password-123"})
	if code != http.StatusOK {
		t.Fatalf("target login failed: %d", code)
	}
	p, err := f.app.currentPrincipal(operationsRequest(t, client, f.server))
	if err != nil || p == nil || p.User.ID != f.target.User.ID || p.Workspace.ID != f.target.Workspace.ID {
		t.Fatalf("target principal unavailable: %v", err)
	}
	return p
}

func generationPlanFixture(t *testing.T, f operationsCasesFixture, action string, version uint64, restriction string) GenerationPlan {
	t.Helper()
	reason := "manual_review"
	if action == "lift_restriction" {
		reason = "review_complete"
	}
	plan, err := f.app.previewGenerationControl(f.principal, GenerationPlanInput{WorkspaceID: f.target.Workspace.ID, Action: action, ExpectedVersion: version, RestrictionID: restriction, ReasonCode: reason, EvidenceRef: "ticket:c1-fixture", PublicReply: "本轮复核公开说明"})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func generationExecuteFixture(t *testing.T, f operationsCasesFixture, proof *operationsPasswordProof, plan GenerationPlan) GenerationReceipt {
	t.Helper()
	receipt, err := f.app.executeGenerationControl(f.principal, proof, plan.WorkspaceID, plan.ID, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PlanID != plan.ID || receipt.Effect != plan.Effect || receipt.WorkspaceID != plan.WorkspaceID {
		t.Fatal("execution receipt differs from reviewed plan")
	}
	return receipt
}

func TestPostgresGenerationRestrictionAppealUpholdLiftAndSecondRestriction(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "moderator")
	user := generationTargetPrincipalFixture(t, f)
	proof := f.proof(t)
	access, err := f.app.generationAccess(user)
	if err != nil || access.State != "OPEN" || access.Version != 1 || access.Restriction != nil {
		t.Fatalf("unexpected initial access: %+v %v", access, err)
	}
	first := generationPlanFixture(t, f, "restrict_future", 1, "")
	staleFirst := generationPlanFixture(t, f, "restrict_future", 1, "")
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_controls") != 0 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_restrictions") != 0 {
		t.Fatal("restriction preview committed its simulated state")
	}
	_, err = f.app.executeGenerationControl(f.principal, nil, first.WorkspaceID, first.ID, first.PlanDigest)
	requireOperation(t, err, "OPERATIONS_REAUTH_REQUIRED")
	firstReceipt := generationExecuteFixture(t, f, proof, first)
	storedReceipt, err := f.app.readGenerationReceipt(f.principal, first.WorkspaceID, first.ID)
	if err != nil || storedReceipt != firstReceipt {
		t.Fatal("executed plan receipt was not durably readable", err)
	}
	_, err = f.app.executeGenerationControl(f.principal, proof, first.WorkspaceID, first.ID, first.PlanDigest)
	requireOperation(t, err, "GENERATION_PLAN_CONSUMED")
	access, err = f.app.generationAccess(user)
	if err != nil || access.State != "RESTRICTED" || access.Version != 2 || access.Restriction == nil || access.Restriction.ID != first.RestrictionID || !access.CanAppeal {
		t.Fatalf("committed restriction unavailable to user: %+v %v", access, err)
	}

	input := GenerationAppealInput{ReasonCode: "request_review", Statement: "PRIVATE_C1_APPEAL_STATEMENT"}
	appeal, replayed, err := f.app.createGenerationAppeal(user, first.RestrictionID, input, "c1-appeal-original")
	if err != nil || replayed || appeal.State != "OPEN" || appeal.Version != 1 {
		t.Fatalf("appeal acceptance failed: %+v %v", appeal, err)
	}
	copy, replayed, err := f.app.createGenerationAppeal(user, first.RestrictionID, input, "c1-appeal-original")
	if err != nil || !replayed || copy.ID != appeal.ID {
		t.Fatal("original appeal key did not replay", err)
	}
	changed := input
	changed.Statement = "different statement"
	_, _, err = f.app.createGenerationAppeal(user, first.RestrictionID, changed, "c1-appeal-original")
	requireOperation(t, err, "IDEMPOTENCY_CONFLICT")
	_, _, err = f.app.createGenerationAppeal(user, first.RestrictionID, input, "c1-appeal-second-key")
	requireOperation(t, err, "GENERATION_APPEAL_EXISTS")
	_, err = f.app.readGenerationAppeal(f.principal, first.WorkspaceID, appeal.ID, false)
	requireOperation(t, err, "NOT_FOUND")
	_, err = f.app.previewGenerationControl(user, GenerationPlanInput{WorkspaceID: first.WorkspaceID, Action: "lift_restriction", ExpectedVersion: 2, RestrictionID: first.RestrictionID, ReasonCode: "review_complete", EvidenceRef: "ticket:owner", PublicReply: "owner cannot self-release"})
	requireOperation(t, err, "OPERATIONS_NOT_FOUND")
	if f.app.store.failed.Load() {
		t.Fatal("expected scope/idempotency errors poisoned the writer")
	}

	uphold, err := f.app.previewGenerationControl(f.principal, GenerationPlanInput{WorkspaceID: first.WorkspaceID, Action: "uphold_restriction", ExpectedVersion: 2, RestrictionID: first.RestrictionID, AppealID: appeal.ID, ExpectedAppealVersion: 1, ReasonCode: "policy_concern", EvidenceRef: "ticket:uphold", PublicReply: "原复核维持答复"})
	if err != nil {
		t.Fatal(err)
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_appeals WHERE id=$1 AND state='OPEN' AND version=1", appeal.ID) != 1 {
		t.Fatal("uphold preview committed an appeal decision")
	}
	generationExecuteFixture(t, f, proof, uphold)
	upheld, err := f.app.readGenerationAppeal(user, first.WorkspaceID, appeal.ID, false)
	if err != nil || upheld.State != "RESOLVED" || upheld.Outcome != "UPHELD" || upheld.Version != 2 || upheld.PublicReply != "原复核维持答复" {
		t.Fatalf("uphold result differs: %+v %v", upheld, err)
	}
	lift := generationPlanFixture(t, f, "lift_restriction", 2, first.RestrictionID)
	if lift.Effect.AffectedAppeals != 0 {
		t.Fatal("lift would rewrite an already resolved appeal")
	}
	generationExecuteFixture(t, f, proof, lift)
	afterLift, replayed, err := f.app.createGenerationAppeal(user, first.RestrictionID, input, "c1-appeal-original")
	if err != nil || !replayed || afterLift != upheld {
		t.Fatal("lift changed immutable upheld outcome or blocked original-key replay", err)
	}
	access, err = f.app.generationAccess(user)
	if err != nil || access.State != "OPEN" || access.Version != 3 || access.Restriction != nil {
		t.Fatalf("lift did not reopen future acceptance: %+v %v", access, err)
	}
	second := generationPlanFixture(t, f, "restrict_future", 3, "")
	if second.RestrictionID == first.RestrictionID {
		t.Fatal("new restriction reused an old episode")
	}
	generationExecuteFixture(t, f, proof, second)
	_, err = f.app.executeGenerationControl(f.principal, proof, staleFirst.WorkspaceID, staleFirst.ID, staleFirst.PlanDigest)
	requireOperation(t, err, "GENERATION_CONTROL_CHANGED")
	_, err = f.app.executeGenerationControl(f.principal, proof, lift.WorkspaceID, lift.ID, lift.PlanDigest)
	requireOperation(t, err, "GENERATION_PLAN_CONSUMED")
	access, err = f.app.generationAccess(user)
	if err != nil || access.State != "RESTRICTED" || access.Version != 4 || access.Restriction == nil || access.Restriction.ID != second.RestrictionID {
		t.Fatalf("old plans disturbed the new restriction: %+v %v", access, err)
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_restrictions WHERE workspace_id=$1", first.WorkspaceID) != 2 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_restrictions WHERE id=$1 AND released_at IS NOT NULL", first.RestrictionID) != 1 {
		t.Fatal("restriction history was overwritten or release was not durable")
	}
}

func TestPostgresGenerationLiftPlanFencesNewAppealsAndRepliesToAllOpen(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "moderator")
	user := generationTargetPrincipalFixture(t, f)
	proof := f.proof(t)
	restrict := generationPlanFixture(t, f, "restrict_future", 1, "")
	generationExecuteFixture(t, f, proof, restrict)
	staleLift := generationPlanFixture(t, f, "lift_restriction", 2, restrict.RestrictionID)
	appeal, _, err := f.app.createGenerationAppeal(user, restrict.RestrictionID, GenerationAppealInput{ReasonCode: "restriction_error", Statement: "请核对本轮限制"}, "c1-new-appeal")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.app.executeGenerationControl(f.principal, proof, staleLift.WorkspaceID, staleLift.ID, staleLift.PlanDigest)
	requireOperation(t, err, "GENERATION_CONTROL_CHANGED")
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_receipts WHERE plan_id=$1", staleLift.ID) != 0 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_controls WHERE workspace_id=$1 AND state='RESTRICTED' AND version=2", restrict.WorkspaceID) != 1 {
		t.Fatal("stale lift partially released the restriction")
	}
	// Seed another applicant's legitimate domain row to exercise the batch
	// update; no browser session or principal is forged into another workspace.
	otherAppeal := newID("gapl")
	if _, err = f.admin.Exec(context.Background(), `INSERT INTO studio_generation_appeals(id,workspace_id,restriction_id,applicant_id,reason_code,statement) VALUES($1,$2,$3,$4,'request_review','second applicant fixture')`, otherAppeal, restrict.WorkspaceID, restrict.RestrictionID, f.identity.User.ID); err != nil {
		t.Fatal(err)
	}
	freshLift := generationPlanFixture(t, f, "lift_restriction", 2, restrict.RestrictionID)
	if freshLift.Effect.AffectedAppeals != 2 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_appeals WHERE workspace_id=$1 AND state='OPEN'", restrict.WorkspaceID) != 2 {
		t.Fatal("fresh preview did not preserve and describe all pending appeals")
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_plans WHERE id=$1", staleLift.ID) != 0 {
		t.Fatal("replacement lift preview did not reclaim its reserved plan lane")
	}
	generationExecuteFixture(t, f, proof, freshLift)
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_appeals WHERE id=ANY($1::text[]) AND state='RESOLVED' AND outcome='LIFTED' AND version=2 AND public_reply=$2", []string{appeal.ID, otherAppeal}, "本轮复核公开说明") != 2 {
		t.Fatal("lift did not atomically resolve and reply to every pending appeal")
	}
	access, err := f.app.generationAccess(user)
	if err != nil || access.State != "OPEN" || access.Version != 3 || access.CanAppeal {
		t.Fatalf("user restriction state did not reopen: %+v %v", access, err)
	}
}

func TestPostgresGenerationLiftAuditFailureRollsBackControlAppealAndReceipt(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "moderator")
	user := generationTargetPrincipalFixture(t, f)
	proof := f.proof(t)
	restrict := generationPlanFixture(t, f, "restrict_future", 1, "")
	generationExecuteFixture(t, f, proof, restrict)
	appeal, _, err := f.app.createGenerationAppeal(user, restrict.RestrictionID, GenerationAppealInput{ReasonCode: "issue_resolved"}, "c1-audit-appeal")
	if err != nil {
		t.Fatal(err)
	}
	lift := generationPlanFixture(t, f, "lift_restriction", 2, restrict.RestrictionID)
	// Fail after the control and restriction updates, while writing the appeal
	// audit, so rollback must cover every business mutation in this execution.
	if _, err = f.admin.Exec(context.Background(), `CREATE FUNCTION reject_generation_lift_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='appeal.lifted' THEN RAISE EXCEPTION 'generation fixture audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_generation_lift_audit BEFORE INSERT ON studio_operations_audit FOR EACH ROW EXECUTE FUNCTION reject_generation_lift_audit()`); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.executeGenerationControl(f.principal, proof, lift.WorkspaceID, lift.ID, lift.PlanDigest)
	requireOperation(t, err, "DATABASE_UNAVAILABLE")
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_controls WHERE workspace_id=$1 AND state='RESTRICTED' AND version=2 AND active_restriction_id=$2", lift.WorkspaceID, restrict.RestrictionID) != 1 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_restrictions WHERE id=$1 AND released_at IS NULL", restrict.RestrictionID) != 1 {
		t.Fatal("failed appeal audit committed a restriction release")
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_appeals WHERE id=$1 AND state='OPEN' AND version=1 AND public_reply=''", appeal.ID) != 1 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_receipts WHERE plan_id=$1", lift.ID) != 0 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_generation_plans WHERE id=$1 AND consumed_at IS NULL", lift.ID) != 1 {
		t.Fatal("failed audit left an appeal decision, receipt, or consumed plan")
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_operations_audit WHERE operation_id=$1 AND action='generation.lifted'", lift.ID) != 0 {
		t.Fatal("earlier audit row escaped the failed execution transaction")
	}
}
