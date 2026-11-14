package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperationsStrictJSONDoesNotAcceptAmbiguousOrLargeRequests(t *testing.T) {
	for _, body := range []string{`{"action":"release","action":"charge"}`, `{"Action":"release"}`, `{"usage":{"prompt_tokens":1,"Prompt_Tokens":2}}`, `{"usage":{"reason":"private raw evidence"}}`, `{"password":"` + strings.Repeat("x", 8192) + `"}`, `null`} {
		request := httptest.NewRequest("POST", "/api/v1/operations/cases", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		var input OperationsPlanInput
		if err := decodeOperationsJSON(response, request, &input); err == nil {
			t.Fatal("ambiguous or oversized JSON accepted")
		}
	}
	request := httptest.NewRequest("POST", "/api/v1/operations/cases", strings.NewReader(`{"action":"no_action"}`))
	var input OperationsPlanInput
	if decodeOperationsJSON(httptest.NewRecorder(), request, &input) == nil {
		t.Fatal("non-JSON media type accepted")
	}
}
func TestPostgresOperationsHTTPMetadataPagingAndScopeRevocation(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "operator")
	run1 := f.run(t, false)
	run2 := f.run(t, false)
	listPath := "/api/v1/operations/exceptions?workspace_id=" + f.target.Workspace.ID + "&kind=run&limit=1"
	code, blob, _ := authRequest(t, f.client, f.server.URL, "GET", listPath, "", "", nil)
	if code != 200 {
		t.Fatalf("queue %d %s", code, blob)
	}
	var page struct {
		Items []OperationsException `json:"items"`
		Next  *string               `json:"next_cursor"`
	}
	if json.Unmarshal(blob, &page) != nil || len(page.Items) != 1 || page.Next == nil {
		t.Fatal("queue pagination missing")
	}
	for _, secret := range []string{"PRIVATE_OPERATIONS_", f.target.User.Email, "handle", "password"} {
		if strings.Contains(string(blob), secret) {
			t.Fatal("private metadata leaked")
		}
	}
	code, blob, _ = authRequest(t, f.client, f.server.URL, "GET", listPath+"&cursor="+*page.Next, "", "", nil)
	if code != 200 {
		t.Fatalf("second page %d %s", code, blob)
	}
	var second struct {
		Items []OperationsException `json:"items"`
	}
	_ = json.Unmarshal(blob, &second)
	if len(second.Items) != 1 || second.Items[0].ID == page.Items[0].ID {
		t.Fatal("cursor duplicated exception")
	}
	code, _, _ = authRequest(t, f.client, f.server.URL, "GET", strings.Replace(listPath, "kind=run", "kind=all", 1)+"&cursor="+*page.Next, "", "", nil)
	if code != 400 {
		t.Fatal("filter cursor reused", code)
	}
	code, _, _ = authRequest(t, f.client, f.server.URL, "GET", strings.Replace(listPath, f.target.Workspace.ID, f.identity.Workspace.ID, 1), "", "", nil)
	if code != 404 {
		t.Fatal("creator workspace ownership became ops permission", code)
	}
	input := OperationsCaseInput{WorkspaceID: run1.WorkspaceID, ResourceType: "run", ResourceID: run1.ID, ResourceVersion: run1.Seq, Reason: "manual_review", EvidenceRef: "ticket:HTTP"}
	code, blob, _ = authRequest(t, f.client, f.server.URL, "POST", "/api/v1/operations/cases", "http-case-key", "", input)
	if code != 201 {
		t.Fatalf("open %d %s", code, blob)
	}
	var c OperationsCase
	_ = json.Unmarshal(blob, &c)
	code, blob, header := authRequest(t, f.client, f.server.URL, "POST", "/api/v1/operations/cases", "http-case-key", "", input)
	if code != 201 || header.Get("Idempotency-Replayed") != "true" || !strings.Contains(string(blob), `"resource"`) {
		t.Fatal("case replay omitted canonical result")
	}
	input.ResourceID = run2.ID
	code, _, _ = authRequest(t, f.client, f.server.URL, "POST", "/api/v1/operations/cases", "http-case-key", "", input)
	if code != 409 {
		t.Fatal("changed case reused key")
	}
	code, blob, _ = authRequest(t, f.client, f.server.URL, "GET", "/api/v1/operations/cases?workspace_id="+f.target.Workspace.ID, "", "", nil)
	if code != 200 {
		t.Fatalf("cases list %d %s", code, blob)
	}
	code, blob, _ = authRequest(t, f.client, f.server.URL, "GET", "/api/v1/operations/cases/"+c.ID+"?workspace_id="+f.target.Workspace.ID, "", "", nil)
	if code != 200 || strings.Contains(string(blob), "PRIVATE_OPERATIONS_") {
		t.Fatalf("case details %d", code)
	}
	code, blob, _ = authRequest(t, f.client, f.server.URL, "GET", "/api/v1/operations/cases/"+c.ID+"/events?workspace_id="+f.target.Workspace.ID, "", "", nil)
	if code != 200 || !strings.Contains(string(blob), "case.created") {
		t.Fatalf("events %d %s", code, blob)
	}
	if _, err := f.admin.Exec(context.Background(), "UPDATE studio_operations_grants SET active=false,version=version+1 WHERE user_id=$1", f.identity.User.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{listPath, "/api/v1/operations/cases?workspace_id=" + f.target.Workspace.ID, "/api/v1/operations/cases/" + c.ID + "?workspace_id=" + f.target.Workspace.ID, "/api/v1/operations/cases/" + c.ID + "/events?workspace_id=" + f.target.Workspace.ID} {
		code, _, _ = authRequest(t, f.client, f.server.URL, "GET", path, "", "", nil)
		if code != 404 {
			t.Fatal("revoked grant still read operations", path, code)
		}
	}
}

func TestPostgresOperationsHTTPPasswordChargeAndBoundedPlans(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "finance")
	run := f.run(t, true)
	c := f.open(t, run)
	planInput := OperationsPlanInput{WorkspaceID: c.WorkspaceID, ExpectedVersion: c.Version, Action: "charge", Reason: "usage_missing", EvidenceRef: "provider:case1", Usage: &ChatUsage{Status: "demo", Source: "demo", Complete: true, PromptTokens: 128, CompletionTokens: 64, TotalTokens: 192}}
	path := "/api/v1/operations/cases/" + c.ID
	code, blob, _ := authRequest(t, f.client, f.server.URL, "POST", path+"/plans", "", "", planInput)
	if code != 201 {
		t.Fatalf("charge preview %d %s", code, blob)
	}
	var plan OperationsPlan
	_ = json.Unmarshal(blob, &plan)
	if plan.Effect.ChargedMicros != 256 || plan.Effect.ReleasedMicros != 2744 {
		t.Fatal("charge plan did not use frozen price", plan.Effect)
	}
	execute := map[string]any{"workspace_id": c.WorkspaceID, "expected_version": c.Version, "plan_id": plan.ID, "plan_digest": plan.PlanDigest, "password": "wrong-password-123"}
	code, blob, _ = authRequest(t, f.client, f.server.URL, "POST", path+"/execute", "", "", execute)
	if code != http.StatusForbidden {
		t.Fatalf("wrong password %d %s", code, blob)
	}
	execute["password"] = "fixture-password-123"
	code, _, _ = authRequest(t, f.client, f.server.URL, "POST", "/api/v1/operations/cases/ocase_%00/execute", "", "", execute)
	if code != 404 && code != 422 {
		t.Fatal("invalid case path accepted", code)
	}
	if f.app.store.failed.Load() {
		t.Fatal("path NUL poisoned database")
	}
	ready, _, _ := authRequest(t, f.client, f.server.URL, "GET", "/readyz", "", "", nil)
	if ready != 200 {
		t.Fatal("path validation damaged readiness")
	}
	code, blob, _ = authRequest(t, f.client, f.server.URL, "POST", path+"/execute", "", "", execute)
	if code != 200 {
		t.Fatalf("execute %d %s", code, blob)
	}
	var result OperationsCase
	_ = json.Unmarshal(blob, &result)
	if result.Receipt == nil || result.Resource.Billing.State != "SETTLED" || result.Resource.Billing.ChargedMicros != 256 {
		t.Fatal("charge result incomplete")
	}
	code, _, _ = authRequest(t, f.client, f.server.URL, "POST", path+"/execute", "", "", execute)
	if code != 409 {
		t.Fatal("repeat execute ran twice")
	}
	// Financiers can open billing cases but cannot query provider state or close
	// investigative cases using an invented permission union.
	run2 := f.run(t, false)
	c2 := f.open(t, run2)
	planInput = OperationsPlanInput{WorkspaceID: c2.WorkspaceID, ExpectedVersion: c2.Version, Action: "no_action", Reason: "manual_review", EvidenceRef: "ticket:deny"}
	code, _, _ = authRequest(t, f.client, f.server.URL, "POST", "/api/v1/operations/cases/"+c2.ID+"/plans", "", "", planInput)
	if code != 404 {
		t.Fatal("finance acquired investigate permission", code)
	}
	f.grant(t, "operator")
	// One consumed finance plan is excluded from active count; cap exactly64.
	for i := 0; i < 64; i++ {
		if _, err := f.app.previewOperationsCase(f.principal, c2.ID, planInput); err != nil {
			t.Fatalf("plan %d %v", i, err)
		}
	}
	_, err := f.app.previewOperationsCase(f.principal, c2.ID, planInput)
	requireOperation(t, err, "OPERATIONS_PLAN_LIMIT")
	if f.app.store.failed.Load() {
		t.Fatal("plan quota poisoned database")
	}
}
