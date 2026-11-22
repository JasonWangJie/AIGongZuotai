package studio

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPostgresRiskAppealOriginalQuoteAndSinglePermit(t *testing.T) {
	a, s, owner, subject, db := newModeratedBillingFixture(t, true)
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	reviewer := authClient(t)
	agent := registerFixture(t, reviewer, s.URL, "risk-officer@example.test")
	ws, project := subject.Workspace.ID, subject.Project.ID
	for _, pair := range []struct{ id, role string }{{agent.User.ID, "risk_reviewer"}, {subject.User.ID, "risk_reviewer"}, {agent.User.ID, "moderator"}} {
		if _, e := admin.Exec(ctx, "INSERT INTO studio_operations_grants(id,user_id,workspace_id,role) VALUES($1,$2,$3,$4)", newID("opg"), pair.id, ws, pair.role); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := admin.Exec(ctx, "INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES($1,1,true,1,1000)", ws); e != nil {
		t.Fatal(e)
	}
	call := func(c *http.Client, want int, method, path, key string, in any) []byte {
		t.Helper()
		code, b, _ := authRequest(t, c, s.URL, method, path, key, "", in)
		if code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, code, want, b)
		}
		return b
	}
	const reviews = "/api/v1/risk/appeals"
	const ops = "/api/v1/operations/risk-appeals"
	provider := &moderatedExecutionChat{text: "Approved, explicitly requested output"}
	a.chat = provider
	in := RunInput{ProjectID: project, ModelID: "studio-chat", Input: "private risk-bound original"}
	q := httpBillingQuote(t, owner, s, project, "run", in)
	in.QuoteID = q.ID
	raw := call(owner, 429, "POST", "/api/v1/runs", "risk-explicit-attempt", in)
	if !strings.Contains(string(raw), "CREDIT_RISK_HELD_LIMIT") || provider.calls.Load() != 0 {
		t.Fatal("risk did not reject actual request")
	}
	elig := decode[RiskEligibility](t, call(owner, 200, "GET", "/api/v1/risk/eligibility?project_id="+project+"&quote_id="+q.ID, "", nil))
	if !elig.AllowedToAppeal || elig.AmountMicros < 2 || len(elig.ReasonCodes) != 1 || elig.ReasonCodes[0] != "HELD_LIMIT" {
		t.Fatal("missing original quote eligibility", elig)
	}
	submit := RiskAppealInput{ProjectID: project, QuoteID: q.ID, ReasonCode: "limit_error", Statement: "PRIVATE_RISK_STATEMENT"}
	r := decode[RiskAppeal](t, call(owner, 201, "POST", reviews, "risk-appeal-original", submit))
	path := ops + "/" + r.ID
	list := call(reviewer, 200, "GET", ops+"?workspace_id="+ws, "", nil)
	if !strings.Contains(string(list), r.ID) || strings.Contains(string(list), submit.Statement) || strings.Contains(string(list), in.Input) {
		t.Fatal("reviewer list leaked evidence or is incomplete")
	}
	planIn := ContentAppealPlanInput{WorkspaceID: ws, Action: "approve", ExpectedVersion: 1, EvidenceRef: "independent:risk", PublicReply: "准予原报价的一次显式提交"}
	call(owner, 403, "POST", path+"/plans", "", planIn)
	pl := decode[RiskReviewPlan](t, call(reviewer, 201, "POST", path+"/plans", "", planIn))
	if outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_permits") != 0 {
		t.Fatal("preview persisted a permit")
	}
	exec := map[string]string{"workspace_id": ws, "plan_id": pl.ID, "plan_digest": pl.PlanDigest, "password": "wrong-password-123"}
	call(reviewer, 403, "POST", path+"/execute", "", exec)
	exec["password"] = "fixture-password-123"
	rec := decode[RiskReviewReceipt](t, call(reviewer, 200, "POST", path+"/execute", "", exec))
	if rec.Outcome != "APPROVED" || rec.ID != decode[RiskReviewReceipt](t, call(reviewer, 200, "GET", path+"/plans/"+pl.ID+"/receipt?workspace_id="+ws, "", nil)).ID {
		t.Fatal("wrong decision receipt")
	}
	detail := func() RiskAppeal {
		return decode[RiskAppeal](t, call(owner, 200, "GET", reviews+"/receipt?project_id="+project, "risk-appeal-original", nil))
	}
	if detail().ExemptionState != "AVAILABLE" || provider.calls.Load() != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("approval executed generation or created credit")
	}
	_, e := admin.Exec(ctx, `CREATE FUNCTION risk_review_atomicity_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE studio_outbox_limits SET pending=10000 WHERE singleton; RETURN NEW; END $$;
	CREATE TRIGGER risk_review_atomicity_fixture BEFORE INSERT ON studio_runs FOR EACH ROW EXECUTE FUNCTION risk_review_atomicity_fixture()`)
	if e != nil {
		t.Fatal(e)
	}
	call(owner, 503, "POST", "/api/v1/runs", "risk-explicit-attempt", in)
	if _, e = admin.Exec(ctx, "DROP TRIGGER risk_review_atomicity_fixture ON studio_runs"); e != nil {
		t.Fatal(e)
	}
	if detail().ExemptionState != "AVAILABLE" || provider.calls.Load() != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("rolled-back business consumed permit or credit")
	}
	changed := in
	changed.Input += " changed"
	call(owner, 409, "POST", "/api/v1/runs", "other-risk-input", changed)
	accepted := decode[Run](t, call(owner, 201, "POST", "/api/v1/runs", "risk-explicit-attempt", in))
	awaitAuthRun(t, owner, s.URL, accepted.ID)
	repeat := decode[Run](t, call(owner, 201, "POST", "/api/v1/runs", "risk-explicit-attempt", in))
	if repeat.ID != accepted.ID || detail().ExemptionState != "CONSUMED" || provider.calls.Load() != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_permits WHERE reservation_id IS NOT NULL") != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 1 {
		t.Fatal("original paid generation not bound exactly once")
	}
	call(owner, 201, "POST", reviews, "risk-appeal-original", submit)
	conflict := submit
	conflict.Statement += " revised"
	call(owner, 409, "POST", reviews, "risk-appeal-original", conflict)
}

func TestPostgresRiskAppealExpiryAndIndependentDenial(t *testing.T) {
	_, s, owner, subject, db := newModeratedBillingFixture(t, true)
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	ws, pid := subject.Workspace.ID, subject.Project.ID
	c := authClient(t)
	reviewer := registerFixture(t, c, s.URL, "risk-expiry@example.test")
	if _, e := admin.Exec(ctx, "INSERT INTO studio_operations_grants(id,user_id,workspace_id,role) VALUES($1,$2,$3,'risk_reviewer')", newID("opg"), reviewer.User.ID, ws); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(ctx, "INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES($1,1,true,1,1000)", ws); e != nil {
		t.Fatal(e)
	}
	call := func(cl *http.Client, want int, method, path, key string, in any) []byte {
		t.Helper()
		code, b, _ := authRequest(t, cl, s.URL, method, path, key, "", in)
		if code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, code, want, b)
		}
		return b
	}
	in := RunInput{ProjectID: pid, ModelID: "studio-chat", Input: "expiry comparison"}
	q := httpBillingQuote(t, owner, s, pid, "run", in)
	var expires time.Time
	if e := admin.QueryRow(ctx, "UPDATE studio_ledger_quotes SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at", q.ID).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	r := decode[RiskAppeal](t, call(owner, 201, "POST", "/api/v1/risk/appeals", "expire-approval", RiskAppealInput{ProjectID: pid, QuoteID: q.ID, ReasonCode: "request_review"}))
	path := "/api/v1/operations/risk-appeals/" + r.ID
	inPlan := ContentAppealPlanInput{WorkspaceID: ws, ExpectedVersion: 1, Action: "approve", EvidenceRef: "expiry:fixture", PublicReply: "已核实"}
	pl := decode[RiskReviewPlan](t, call(c, 201, "POST", path+"/plans", "", inPlan))
	if pl.ExpiresAt.After(expires) {
		t.Fatal("plan outlived quote")
	}
	if _, e := admin.Exec(ctx, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM ($1::timestamptz-clock_timestamp())))::double precision+0.03)`, expires); e != nil {
		t.Fatal(e)
	}
	call(c, 409, "POST", path+"/execute", "", map[string]string{"workspace_id": ws, "plan_id": pl.ID, "plan_digest": pl.PlanDigest, "password": "fixture-password-123"})
	call(owner, 200, "GET", "/api/v1/risk/appeals/receipt?project_id="+pid, "expire-approval", nil)
	inPlan.Action = "deny"
	d := decode[RiskReviewPlan](t, call(c, 201, "POST", path+"/plans", "", inPlan))
	out := decode[RiskReviewReceipt](t, call(c, 200, "POST", path+"/execute", "", map[string]string{"workspace_id": ws, "plan_id": d.ID, "plan_digest": d.PlanDigest, "password": "fixture-password-123"}))
	if out.Outcome != "DENIED" || outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_permits") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("expired application authorized resources")
	}
}
func TestPostgresRiskAppealLostCommitReceipts(t *testing.T) {
	for _, phase := range []string{"application", "approval"} {
		t.Run(phase, func(t *testing.T) {
			a, s, owner, subject, db := newModeratedBillingFixture(t, true)
			client := authClient(t)
			reviewer := registerFixture(t, client, s.URL, "risk-commit-reviewer@example.test")
			ws, project := subject.Workspace.ID, subject.Project.ID
			ctx := context.Background()
			admin := deliveryAdmin(t, db)
			if _, e := admin.Exec(ctx, "INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES($1,1,true,1,1000)", ws); e != nil {
				t.Fatal(e)
			}
			if _, e := admin.Exec(ctx, "INSERT INTO studio_operations_grants(id,user_id,workspace_id,role) VALUES($1,$2,$3,'risk_reviewer')", newID("opg"), reviewer.User.ID, ws); e != nil {
				t.Fatal(e)
			}
			q := httpBillingQuote(t, owner, s, project, "run", RunInput{ProjectID: project, ModelID: "studio-chat", Input: "preserve only original quote and receipt"})
			path := "/api/v1/risk/appeals"
			key := "risk-commit-original"
			var input any = RiskAppealInput{ProjectID: project, QuoteID: q.ID, ReasonCode: "limit_error", Statement: "private original"}
			caller := owner
			var reviewID, planID string
			table := "studio_credit_risk_review_keys"
			if phase == "approval" {
				code, blob, _ := authRequest(t, owner, s.URL, "POST", path, key, "", input)
				if code != 201 {
					t.Fatal(code, string(blob))
				}
				reviewID = decode[RiskAppeal](t, blob).ID
				scope := "/api/v1/operations/risk-appeals/" + reviewID
				code, blob, _ = authRequest(t, client, s.URL, "POST", scope+"/plans", "", "", ContentAppealPlanInput{WorkspaceID: ws, ExpectedVersion: 1, Action: "approve", EvidenceRef: "verified:owner", PublicReply: "同意一次显式尝试"})
				if code != 201 {
					t.Fatal(code, string(blob))
				}
				pl := decode[RiskReviewPlan](t, blob)
				planID = pl.ID
				path = scope + "/execute"
				caller = client
				key = ""
				input = map[string]string{"workspace_id": ws, "plan_id": pl.ID, "plan_digest": pl.PlanDigest, "password": "fixture-password-123"}
				table = "studio_credit_risk_receipts"
			}
			cfg := a.config
			a.Close()
			s.Close()
			proxy, armed, dropped := recoveryControlAckProxy(t, db)
			cfg.DatabaseURL = proxy
			app, live := testApp(t, cfg)
			if _, e := admin.Exec(ctx, `CREATE FUNCTION hold_risk_commit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(913028,28); RETURN NEW; END $$; CREATE TRIGGER hold_risk_commit_fixture BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION hold_risk_commit_fixture()`); e != nil {
				t.Fatal(e)
			}
			barrier, e := admin.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer barrier.Rollback(ctx)
			if _, e = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(913028,28)"); e != nil {
				t.Fatal(e)
			}
			result := make(chan int, 1)
			go func() { code, _, _ := authRequest(t, caller, live.URL, "POST", path, key, "", input); result <- code }()
			verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, result)
			app.Close()
			live.Close()
			cfg.DatabaseURL = db
			_, restored := testApp(t, cfg)
			totalPermits := 0
			if phase == "application" {
				code, blob, _ := authRequest(t, owner, restored.URL, "GET", "/api/v1/risk/appeals/receipt?project_id="+project, key, "", nil)
				v := decode[RiskAppeal](t, blob)
				if code != 200 || v.State != "OPEN" || v.QuoteID != q.ID {
					t.Fatal("original application GET", code, string(blob))
				}
			} else {
				scope := "/api/v1/operations/risk-appeals/" + reviewID
				code, blob, _ := authRequest(t, client, restored.URL, "GET", scope+"/plans/"+planID+"/receipt?workspace_id="+ws, "", "", nil)
				receipt := decode[RiskReviewReceipt](t, blob)
				if code != 200 || receipt.Outcome != "APPROVED" || receipt.AppealID != reviewID {
					t.Fatal("original execution receipt GET", code, string(blob))
				}
				totalPermits = 1
			}
			if outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_reviews") != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_credit_risk_permits") != totalPermits || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_runs") != 0 {
				t.Fatal("response loss created generation or duplicate evidence")
			}
		})
	}
}
