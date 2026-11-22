package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestContentAppealBoundedInputAndPublicProjection(t *testing.T) {
	in := ContentAppealInput{ProjectID: newID("prj"), ResourceType: "run", ResourceID: newID("run"), ModerationVersion: 2, ReasonCode: "decision_error", Statement: strings.Repeat("x", 1024)}
	if !validContentAppeal(in) {
		t.Fatal("rejected bounded statement")
	}
	for _, change := range []func(*ContentAppealInput){
		func(i *ContentAppealInput) { i.Statement += "x" }, func(i *ContentAppealInput) { i.Statement = "a\u200db" }, func(i *ContentAppealInput) { i.ResourceType = "reference" }, func(i *ContentAppealInput) { i.ResourceID = newID("task") }, func(i *ContentAppealInput) { i.ModerationVersion = 1 },
	} {
		bad := in
		change(&bad)
		if validContentAppeal(bad) {
			t.Fatal("accepted invalid scope, statement, or moderation version")
		}
	}
	v := ContentAppeal{ID: newID("capl"), applicant: "PRIVATE_USER", evidence: "PRIVATE_DIGEST", policyDigest: "PRIVATE_POLICY", State: "OPEN"}
	b, err := json.Marshal(v)
	if err != nil || strings.Contains(string(b), "PRIVATE") {
		t.Fatal("private evidence leaked", err)
	}
	input := ContentAppealPlanInput{WorkspaceID: newID("ws"), ExpectedVersion: 1, Action: "record_opinion", PublicReply: "独立复核意见", EvidenceRef: "ticket:fixture"}
	if !validContentPlan(input) {
		t.Fatal("valid plan rejected")
	}
	input.Action = "approve_output"
	if validContentPlan(input) {
		t.Fatal("review can promote old output")
	}
}

func TestPostgresContentAppealEvidenceOwnershipPrivacyAndSecondOpinion(t *testing.T) {
	a, s, owner, identity, dsn := newModeratedBillingFixture(t, true)
	other := authClient(t)
	peer := registerFixture(t, other, s.URL, "appeal-evidence-reviewer@example.test")
	operationsGrantFixture(t, a, peer.User.ID, identity.Workspace.ID, "moderator")
	operationsGrantFixture(t, a, identity.User.ID, identity.Workspace.ID, "moderator")
	p, err := a.currentPrincipal(operationsRequest(t, owner, s))
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := a.currentPrincipal(operationsRequest(t, other, s))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := a.verifyOperationsPassword(operationsRequest(t, other, s), "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	admin := deliveryAdmin(t, dsn)
	ctx := context.Background()
	chat := &moderatedExecutionChat{text: "UNKNOWN-OUTPUT private candidate"}
	a.chat = chat
	request := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "private prompt for moderation fixture"}
	request.QuoteID = httpBillingQuote(t, owner, s, identity.Project.ID, "run", request).ID
	code, body, _ := authRequest(t, owner, s.URL, "POST", "/api/v1/runs", newID("runkey"), identity.Project.ID, request)
	if code != 201 {
		t.Fatal(code)
	}
	r := awaitAuthRun(t, owner, s.URL, decode[Run](t, body).ID)
	if r.Status != "FAILED" || r.Moderation == nil || r.Moderation.State != "REVIEW" || r.Billing == nil || r.Billing.State != "REVIEW" {
		t.Fatal("fixture must have genuine output REVIEW")
	}
	in := ContentAppealInput{ProjectID: identity.Project.ID, ResourceType: "run", ResourceID: r.ID, ModerationVersion: r.Moderation.Version, ReasonCode: "decision_error", Statement: "private applicant statement"}
	wrong := in
	wrong.ModerationVersion++
	_, _, err = a.createContentAppeal(p, wrong, newID("key"))
	requireOperation(t, err, "CONTENT_APPEAL_CHANGED")
	err = pgx.BeginFunc(ctx, admin, func(tx pgx.Tx) error {
		_, e := sourceContentAppealSQL(ctx, tx, p.Workspace.ID, reviewer.User.ID, in)
		return e
	})
	requireOperation(t, err, "CONTENT_APPEAL_NOT_FOUND")
	// Data predating 021 has no token-stream head. Its original, immutable
	// output decision and creation audit remain usable, but deleting a head
	// from a modern record must not make it appear to be historical.
	legacy, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Rollback(ctx)
	if _, err = legacy.Exec(ctx, "DELETE FROM studio_run_stream_events WHERE run_id=$1", r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec(ctx, "DELETE FROM studio_run_streams WHERE run_id=$1", r.ID); err != nil {
		t.Fatal(err)
	}
	_, err = sourceContentAppealSQL(ctx, legacy, p.Workspace.ID, p.User.ID, in)
	requireOperation(t, err, "CONTENT_APPEAL_NOT_FOUND")
	if _, err = legacy.Exec(ctx, "UPDATE studio_schema_versions SET applied_at=clock_timestamp() WHERE version=21"); err != nil {
		t.Fatal(err)
	}
	if _, err = sourceContentAppealSQL(ctx, legacy, p.Workspace.ID, p.User.ID, in); err != nil {
		t.Fatal("genuine pre-stream decision was discarded", err)
	}
	if err = legacy.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	key := newID("key")
	x, replayed, err := a.createContentAppeal(p, in, key)
	if err != nil || replayed || x.Statement != in.Statement || x.Decision != r.Moderation.State || x.ResourceVersion != r.Seq || x.evidence == "" {
		t.Fatal("original evidence was not frozen", err)
	}
	again, replayed, err := a.createContentAppeal(p, in, key)
	if err != nil || !replayed || again.ID != x.ID {
		t.Fatal("application receipt", err)
	}
	_, _, err = a.createContentAppeal(p, in, newID("key"))
	requireOperation(t, err, "CONTENT_APPEAL_EXISTS")
	wrong = in
	wrong.Statement += " changed"
	_, _, err = a.createContentAppeal(p, wrong, key)
	requireOperation(t, err, "IDEMPOTENCY_CONFLICT")
	public := func(v any) string {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	ws := identity.Workspace.ID
	list, err := a.listContentAppeals(reviewer, ws, "", "all", "", 1, true)
	if err != nil || len(list.Items) != 1 || strings.Contains(public(list), in.Statement) || strings.Contains(public(list), request.Input) || strings.Contains(public(list), chat.text) {
		t.Fatal("list disclosed private content", err)
	}
	_, err = a.readContentAppeal(reviewer, ws, identity.Project.ID, x.ID, false)
	requireOperation(t, err, "CONTENT_APPEAL_NOT_FOUND")
	decision := ContentAppealPlanInput{WorkspaceID: ws, ExpectedVersion: 1, Action: "record_opinion", EvidenceRef: "case:private-evidence-ref", PublicReply: "复核意见仅答复申请人"}
	_, err = a.previewContentAppeal(p, x.ID, decision)
	requireOperation(t, err, "CONTENT_APPEAL_INDEPENDENT_REVIEW_REQUIRED")
	plan, err := a.previewContentAppeal(reviewer, x.ID, decision)
	if err != nil {
		t.Fatal(err)
	}
	open, err := a.contentAppealReceipt(p, identity.Project.ID, key)
	if err != nil || open.State != "OPEN" || open.PublicReply != "" {
		t.Fatal("preview mutated complaint", err)
	}
	_, err = a.executeContentAppeal(reviewer, ws, x.ID, plan.ID, plan.PlanDigest, nil)
	requireOperation(t, err, "OPERATIONS_REAUTH_REQUIRED")
	receipt, err := a.executeContentAppeal(reviewer, ws, x.ID, plan.ID, plan.PlanDigest, proof)
	if err != nil || receipt.Outcome != "OPINION_RECORDED" || receipt.Version != 2 {
		t.Fatal("separate opinion not recorded", err)
	}
	final, err := a.readContentAppeal(p, ws, identity.Project.ID, x.ID, false)
	if err != nil || final.PublicReply != decision.PublicReply || final.Statement != in.Statement || final.Decision != "REVIEW" || final.Finance != nil {
		t.Fatal("complaint detail inconsistent", err)
	}
	finalList, err := a.listContentAppeals(p, ws, identity.Project.ID, "RESOLVED", "", 50, false)
	if err != nil || len(finalList.Items) != 1 || strings.Contains(public(finalList), decision.PublicReply) || strings.Contains(public(finalList), in.Statement) {
		t.Fatal("resolved list leaked private discussion", err)
	}
	if _, e := admin.Exec(ctx, "UPDATE studio_content_appeals SET statement='rewrite' WHERE id=$1", x.ID); e == nil {
		t.Fatal("completed evidence could be rewritten")
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_operations_audit WHERE resource_id=$1 AND (evidence_ref=$2 OR evidence_ref=$3)", x.ID, decision.EvidenceRef, in.Statement); n != 0 {
		t.Fatal("raw evidence leaked to shared audit")
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations WHERE id=$1 AND state='REVIEW' AND released_micros=0 AND charged_micros=0", r.Billing.ReservationID); n != 1 || chat.calls.Load() != 1 {
		t.Fatal("opinion mutated original hold or dispatched generation")
	}
}

func TestPostgresContentAppealFinancialPlanFencesStaleReserveAndSession(t *testing.T) {
	a, s, owner, identity, dsn := newModeratedBillingFixture(t, true)
	admin := deliveryAdmin(t, dsn)
	ctx := context.Background()
	other := authClient(t)
	reviewID := registerFixture(t, other, s.URL, "appeal-stale-moderator@example.test")
	operationsGrantFixture(t, a, reviewID.User.ID, identity.Workspace.ID, "moderator")
	p, e := a.currentPrincipal(operationsRequest(t, owner, s))
	if e != nil {
		t.Fatal(e)
	}
	reviewer, e := a.currentPrincipal(operationsRequest(t, other, s))
	if e != nil {
		t.Fatal(e)
	}
	proof, e := a.verifyOperationsPassword(operationsRequest(t, other, s), "fixture-password-123")
	if e != nil {
		t.Fatal(e)
	}
	a.chat = &moderatedExecutionChat{text: "BLOCK-OUTPUT"}
	rin := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "fixture"}
	rin.QuoteID = httpBillingQuote(t, owner, s, identity.Project.ID, "run", rin).ID
	code, b, _ := authRequest(t, owner, s.URL, "POST", "/api/v1/runs", newID("runkey"), identity.Project.ID, rin)
	if code != 201 {
		t.Fatal(code)
	}
	r := awaitAuthRun(t, owner, s.URL, decode[Run](t, b).ID)
	if r.Moderation == nil || r.Billing == nil || r.Billing.State != "REVIEW" {
		t.Fatal("fixture must contain original held credit")
	}
	in := ContentAppealInput{ProjectID: identity.Project.ID, ResourceType: "run", ResourceID: r.ID, ModerationVersion: r.Moderation.Version, ReasonCode: "billing_concern"}
	x, _, e := a.createContentAppeal(p, in, newID("key"))
	if e != nil {
		t.Fatal(e)
	}
	planIn := ContentAppealPlanInput{WorkspaceID: identity.Workspace.ID, ExpectedVersion: 1, Action: "refer_finance", EvidenceRef: "case:review", PublicReply: "转交财务核实"}
	plan, e := a.previewContentAppeal(reviewer, x.ID, planIn)
	if e != nil {
		t.Fatal(e)
	}
	// A reservation change after preview must be noticed even when it still
	// has the same amount in REVIEW.
	if _, e = admin.Exec(ctx, "UPDATE studio_ledger_reservations SET version=version+1 WHERE id=$1", r.Billing.ReservationID); e != nil {
		t.Fatal(e)
	}
	_, e = a.executeContentAppeal(reviewer, x.WorkspaceID, x.ID, plan.ID, plan.PlanDigest, proof)
	requireOperation(t, e, "CONTENT_APPEAL_CHANGED")
	plan, e = a.previewContentAppeal(reviewer, x.ID, planIn)
	if e != nil {
		t.Fatal(e)
	}
	// A valid plan waiting behind a settlement barrier must observe expiry
	// again after the wait and leave the user's complaint open.
	if _, e = admin.Exec(ctx, "UPDATE studio_content_appeal_plans SET expires_at=clock_timestamp()+interval '800 milliseconds' WHERE id=$1", plan.ID); e != nil {
		t.Fatal(e)
	}
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM studio_ledger_reservations WHERE id=$1 FOR UPDATE", r.Billing.ReservationID); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, e := a.executeContentAppeal(reviewer, x.WorkspaceID, x.ID, plan.ID, plan.PlanDigest, proof)
		result <- e
	}()
	observer := deliveryAdmin(t, dsn)
	awaitRunLeaseWaiters(t, observer, int32(a.store.conn.PgConn().PID()))
	var passed bool
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if e = observer.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM studio_content_appeal_plans WHERE id=$1", plan.ID).Scan(&passed); e != nil {
			t.Fatal(e)
		}
		if passed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !passed {
		t.Fatal("test plan did not expire")
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	requireOperation(t, <-result, "CONTENT_APPEAL_PLAN_EXPIRED")
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_content_appeals WHERE id=$1 AND state='OPEN' AND version=1", x.ID); n != 1 {
		t.Fatal("stale or expired plan changed complaint")
	}
	plan, e = a.previewContentAppeal(reviewer, x.ID, planIn)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = admin.Exec(ctx, "DELETE FROM studio_sessions WHERE id=$1", reviewer.SessionID); e != nil {
		t.Fatal(e)
	}
	_, e = a.executeContentAppeal(reviewer, x.WorkspaceID, x.ID, plan.ID, plan.PlanDigest, proof)
	requireOperation(t, e, "AUTH_REQUIRED")
}
