package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestPostgresContentAppealFinanceReceiptAndRelease(t *testing.T) {
	a, server, owner, subject, database := newModeratedBillingFixture(t, true)
	admin := deliveryAdmin(t, database)
	moderator := authClient(t)
	reviewer := registerFixture(t, moderator, server.URL, "content-moderator@example.test")
	finance := authClient(t)
	financier := registerFixture(t, finance, server.URL, "content-finance@example.test")
	workspace := subject.Workspace.ID
	project := subject.Project.ID
	for role, id := range map[string]string{"moderator": reviewer.User.ID, "finance": financier.User.ID} {
		if _, err := admin.Exec(context.Background(), "INSERT INTO studio_operations_grants(id,user_id,workspace_id,role) VALUES($1,$2,$3,$4)", newID("ogr"), id, workspace, role); err != nil {
			t.Fatal(err)
		}
	}
	request := func(c *http.Client, want int, method, path, key string, body any) []byte {
		t.Helper()
		selected := ""
		if c == owner {
			selected = project
		}
		code, blob, _ := authRequest(t, c, server.URL, method, path, key, selected, body)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, blob)
		}
		return blob
	}
	const ownerAPI = "/api/v1/content-appeals"
	const ops = "/api/v1/operations"
	const operationsAPI = ops + "/content-appeals"
	wsQuery := "?workspace_id=" + workspace
	projectQuery := "?project_id=" + project
	chat := &moderatedExecutionChat{text: "BLOCK-OUTPUT NEVER EXPOSE UNREVIEWED TEXT"}
	a.chat = chat
	for _, existing := range []bool{false, true} {
		runInput := RunInput{ProjectID: project, ModelID: "studio-chat", Input: "A request for an audited second opinion"}
		runInput.QuoteID = httpBillingQuote(t, owner, server, project, "run", runInput).ID
		run := decode[Run](t, request(owner, 201, "POST", "/api/v1/runs", newID("key"), runInput))
		run = awaitAuthRun(t, owner, server.URL, run.ID)
		if run.Moderation == nil || run.Moderation.State != "BLOCKED" || run.Billing == nil || run.Billing.State != "REVIEW" {
			t.Fatal("output was not quarantined with held credit")
		}
		statement := "PRIVATE_APPEAL_ORIGINAL_STATEMENT"
		applyKey := newID("key")
		in := ContentAppealInput{ProjectID: project, ResourceType: "run", ResourceID: run.ID, ModerationVersion: run.Moderation.Version, ReasonCode: "billing_concern", Statement: statement}
		appeal := decode[ContentAppeal](t, request(owner, 201, "POST", ownerAPI, applyKey, in))
		path := operationsAPI + "/" + appeal.ID
		held := run.Billing.ReservedMicros - run.Billing.ReleasedMicros - run.Billing.ChargedMicros
		if held <= 0 || appeal.ID == "" {
			t.Fatal("no original hold or appeal receipt")
		}
		request(finance, 404, "GET", path+wsQuery, "", nil)
		request(moderator, 404, "GET", operationsAPI+"/finance"+wsQuery, "", nil)
		request(finance, 404, "GET", path+"/finance-case"+wsQuery, "", nil)
		// One case may already exist for this resource under the original finance workflow.
		var existingID string
		if existing {
			v := decode[OperationsCase](t, request(finance, 201, "POST", ops+"/cases", newID("key"), OperationsCaseInput{WorkspaceID: workspace, ResourceType: "run", ResourceID: run.ID, ResourceVersion: run.Seq, Reason: "billing_review", EvidenceRef: "independent:review"}))
			existingID = v.ID
		}
		plan := decode[ContentAppealPlan](t, request(moderator, 201, "POST", path+"/plans", "", map[string]any{"workspace_id": workspace, "action": "refer_finance", "expected_version": appeal.Version, "evidence_ref": "moderation:private", "public_reply": "已提交独立财务核实"}))
		exec := map[string]any{"workspace_id": workspace, "plan_id": plan.ID, "plan_digest": plan.PlanDigest, "password": "fixture-password-123"}
		request(moderator, 200, "POST", path+"/execute", "", exec)
		request(moderator, 200, "GET", path+"/plans/"+plan.ID+"/receipt"+wsQuery, "", nil)
		queue := request(finance, 200, "GET", operationsAPI+"/finance"+wsQuery, "", nil)
		if !strings.Contains(string(queue), appeal.ID) || strings.Contains(string(queue), statement) || strings.Contains(string(queue), "moderation:private") {
			t.Fatal("finance queue omitted eligible appeal or exposed policy evidence")
		}
		bind := appealFinanceLinkInput{WorkspaceID: workspace, ExpectedVersion: 2}
		bindKey := newID("key")
		request(moderator, 404, "POST", path+"/finance-case", bindKey, bind)
		link := decode[appealFinanceLink](t, request(finance, 201, "POST", path+"/finance-case", bindKey, bind))
		if link.Status != "REFERRED" || link.CaseID == "" || existing && link.CaseID != existingID || link.ReceiptID != "" || link.ReleasedMicros != "" {
			t.Fatal("finance referral fabricated a result or missed the original open case", link)
		}
		ref := request(finance, 200, "GET", ops+"/cases/"+link.CaseID+"/content-appeal"+wsQuery, "", nil)
		if decode[appealFinanceLink](t, ref).AppealID != appeal.ID {
			t.Fatal("reverse case link changed appeal")
		}
		request(finance, 200, "GET", path+"/finance-case"+wsQuery, bindKey, nil)
		request(finance, 404, "GET", path+"/finance-case"+wsQuery, newID("key"), nil)
		request(finance, 201, "POST", path+"/finance-case", bindKey, bind)
		request(finance, 409, "POST", path+"/finance-case", bindKey, appealFinanceLinkInput{WorkspaceID: workspace, ExpectedVersion: 1})
		ownerDetail := request(owner, 200, "GET", ownerAPI+"/"+appeal.ID+projectQuery, "", nil)
		if strings.Contains(string(ownerDetail), link.CaseID) || strings.Contains(string(ownerDetail), "case_id") || strings.Contains(string(ownerDetail), chat.text) {
			t.Fatal("owner was shown privileged case details")
		}
		// Both moderation execution and financial referral leave the existing hold untouched.
		if outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals WHERE reservation_id=$1 AND kind IN ('RELEASE','CHARGE')", run.Billing.ReservationID) != 0 {
			t.Fatal("financial effect before explicit finance confirmation")
		}
		billingPath := ops + "/cases/" + link.CaseID
		financialPlan := decode[OperationsPlan](t, request(finance, 201, "POST", billingPath+"/plans", "", OperationsPlanInput{WorkspaceID: workspace, ExpectedVersion: 1, Action: "release", Reason: "billing_review", EvidenceRef: "finance:independent"}))
		if financialPlan.Effect.ReleasedMicros != held {
			t.Fatal("preview is not backed by actual frozen credit")
		}
		request(owner, 200, "GET", ownerAPI+"/"+appeal.ID+projectQuery, "", nil)
		closed := decode[OperationsCase](t, request(finance, 200, "POST", billingPath+"/execute", "", map[string]any{"workspace_id": workspace, "expected_version": 1, "plan_id": financialPlan.ID, "plan_digest": financialPlan.PlanDigest, "password": "fixture-password-123"}))
		if closed.Receipt == nil {
			t.Fatal("missing executed operations receipt")
		}
		receipt := decode[ContentAppeal](t, request(owner, 200, "GET", ownerAPI+"/receipt"+projectQuery, applyKey, nil))
		if receipt.Finance == nil || receipt.Finance.Status != "RELEASED" || receipt.Finance.ReceiptID != closed.Receipt.ID || receipt.Finance.ReleasedMicros != strconv.FormatInt(held, 10) {
			t.Fatal("applicant did not see the exact executed release", receipt.Finance)
		}
		final := awaitAuthRun(t, owner, server.URL, run.ID)
		if final.Output != "" || final.Moderation.State != "BLOCKED" || final.Billing.State != "RELEASED" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != held {
			t.Fatal("finance action changed isolation, amount or billing result")
		}
		if outboxCount(t, admin, "SELECT count(*) FROM studio_operations_cases WHERE resource_id=$1", run.ID) != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals WHERE reservation_id=$1 AND kind='RELEASE'", run.Billing.ReservationID) != 1 {
			t.Fatal("duplicate case or release")
		}
		if outboxCount(t, admin, "SELECT balance_micros FROM studio_ledger_accounts WHERE id=$1", "available:"+workspace) != 1_000_000 ||
			outboxCount(t, admin, "SELECT balance_micros FROM studio_ledger_accounts WHERE id=$1", "reserved:"+workspace) != 0 {
			t.Fatal("released CREDIT did not return to the original wallet")
		}
	}
	if chat.calls.Load() != 2 {
		t.Fatal("appeal initiated model generation")
	}
}

func TestContentAppealFinancePublicFields(t *testing.T) {
	link := appealFinanceLink{AppealID: "private-appeal", CaseID: "internal-case", ContentAppealFinanceStatus: ContentAppealFinanceStatus{Status: "RELEASED", ReleasedMicros: "12", ReceiptID: "receipt"}}
	data, err := json.Marshal(link.ContentAppealFinanceStatus)
	if err != nil || string(data) != `{"status":"RELEASED","receipt_id":"receipt","released_micros":"12"}` {
		t.Fatalf("applicant finance projection: %s %v", data, err)
	}
}
