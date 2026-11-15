package studio

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerationHTTPStrictJSON(t *testing.T) {
	for _, tc := range []struct {
		name, body, content string
		status              int
	}{
		{"valid", `{"reason_code":"request_review","statement":"请核实\n说明"}`, "application/json; charset=utf-8", 0},
		{"duplicate", `{"reason_code":"request_review","reason_code":"issue_resolved"}`, "application/json", 400},
		{"alias", `{"Reason_code":"request_review"}`, "application/json", 400},
		{"unknown", `{"workspace_id":"ws_other","reason_code":"request_review"}`, "application/json", 400},
		{"array", `[]`, "application/json", 400},
		{"null", `null`, "application/json", 400},
		{"type", `{"statement":42}`, "application/json", 400},
		{"two_objects", `{} {}`, "application/json", 400},
		{"encoding", "{\"statement\":\"\xff\"}", "application/json", 400},
		{"large", `{"statement":"` + strings.Repeat("a", 8192) + `"}`, "application/json", 413},
		{"media_type", `{}`, "text/plain", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.content)
			var in GenerationAppealInput
			err := decodeGenerationJSON(httptest.NewRecorder(), r, &in, "reason_code", "statement")
			if tc.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			op, ok := err.(*operationError)
			if !ok || op.status != tc.status {
				t.Fatal("unexpected strict decoding", err)
			}
		})
	}
}

func TestGenerationHTTPRequiresAccount(t *testing.T) {
	_, s := testApp(t, Config{})
	for _, path := range []string{"/api/v1/workspace/generation-access", "/api/v1/appeals", "/api/v1/appeals/invalid"} {
		code, _, _ := authRequest(t, authClient(t), s.URL, "GET", path, "", "", nil)
		if code != 409 {
			t.Fatal(path, code)
		}
	}
}

func TestGenerationRoutesHaveBoundedMetrics(t *testing.T) {
	a := observerFixture(t, Config{MetricsToken: metricsFixtureToken})
	handler := a.Handler()
	for _, path := range []struct{ method, path, label string }{
		{"GET", "/api/v1/workspace/generation-access", "generation_access"},
		{"POST", "/api/v1/restrictions/private-episode/appeals", "generation_appeal_create"},
		{"GET", "/api/v1/appeals", "generation_appeals"},
		{"GET", "/api/v1/appeals/private-appeal", "generation_appeal"},
		{"GET", "/api/v1/operations/generation-access", "operations_generation_access"},
		{"GET", "/api/v1/operations/appeals", "operations_appeals"},
		{"GET", "/api/v1/operations/appeals/private-appeal", "operations_appeal"},
		{"POST", "/api/v1/operations/generation-plans", "operations_generation_plan"},
		{"POST", "/api/v1/operations/generation-executions", "operations_generation_execute"},
		{"GET", "/api/v1/operations/generation-plans/private-plan/receipt", "operations_generation_receipt"},
	} {
		r := httptest.NewRequest(path.method, "http://127.0.0.1"+path.path+"?workspace_id=private-scope", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatalf("account gate: %s %d", path.label, w.Code)
		}
		metrics := observedRequest(handler, "GET", "/metrics", "Bearer "+metricsFixtureToken).Body.String()
		if !strings.Contains(metrics, `route="`+path.label+`"`) {
			t.Fatalf("missing bounded route %s", path.label)
		}
		for _, private := range []string{"private-episode", "private-appeal", "private-plan", "private-scope"} {
			if strings.Contains(metrics, private) {
				t.Fatal("private identity in metric labels")
			}
		}
	}
}

func TestPostgresGenerationHTTPAppealAndResolution(t *testing.T) {
	a, s, c, id, _ := operationsFixture(t)
	otherClient := authClient(t)
	other := registerFixture(t, otherClient, s.URL, "generation-other@example.test")
	workspace := id.Workspace.ID
	code, data, _ := authRequest(t, c, s.URL, "GET", "/api/v1/workspace/generation-access", "", "", nil)
	access := decode[GenerationAccess](t, data)
	if code != 200 || access.State != "OPEN" || access.Version != 1 || access.CanAppeal {
		t.Fatal("initial access", code, access)
	}
	for _, role := range []string{"observer", "operator", "finance"} {
		operationsGrantFixture(t, a, id.User.ID, workspace, role)
	}
	code, _, _ = authRequest(t, c, s.URL, "GET", "/api/v1/operations/generation-access?workspace_id="+workspace, "", "", nil)
	if code != 404 {
		t.Fatal("old roles gained new restriction power", code)
	}
	operationsGrantFixture(t, a, id.User.ID, workspace, "moderator")
	for _, path := range []string{"/api/v1/appeals/gapl_%00", "/api/v1/operations/appeals/gapl_%00?workspace_id=" + workspace, "/api/v1/operations/generation-access?workspace_id=ws_%00", "/api/v1/operations/generation-plans/gpl_%00/receipt?workspace_id=" + workspace} {
		code, _, _ = authRequest(t, c, s.URL, "GET", path, "", "", nil)
		if code != 404 || a.store.failed.Load() {
			t.Fatal("malformed identity reached PG", path, code)
		}
	}
	for _, path := range []string{"/api/v1/appeals?state=%ff", "/api/v1/appeals?cursor=%00", "/api/v1/appeals?limit=0"} {
		code, _, _ = authRequest(t, c, s.URL, "GET", path, "", "", nil)
		if code != 400 || a.store.failed.Load() {
			t.Fatal("malformed page reached PG", path, code)
		}
	}
	createPlan := func(in GenerationPlanInput) GenerationPlan {
		t.Helper()
		status, body, _ := authRequest(t, c, s.URL, "POST", "/api/v1/operations/generation-plans", "", "", in)
		if status != 201 {
			t.Fatalf("plan %d %s", status, body)
		}
		return decode[GenerationPlan](t, body)
	}
	execute := func(plan GenerationPlan) GenerationReceipt {
		t.Helper()
		status, body, _ := authRequest(t, c, s.URL, "POST", "/api/v1/operations/generation-executions", "", "", map[string]any{"workspace_id": workspace, "plan_id": plan.ID, "plan_digest": plan.PlanDigest, "password": "fixture-password-123"})
		if status != 200 {
			t.Fatalf("execute %d %s", status, body)
		}
		return decode[GenerationReceipt](t, body)
	}
	plan := createPlan(GenerationPlanInput{WorkspaceID: workspace, Action: "restrict_future", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "ticket:private-evidence", PublicReply: "请申请复核，核对后恢复新生成。"})
	code, data, _ = authRequest(t, c, s.URL, "GET", "/api/v1/workspace/generation-access", "", "", nil)
	if code != 200 || decode[GenerationAccess](t, data).State != "OPEN" {
		t.Fatal("preview mutated access")
	}
	code, _, _ = authRequest(t, c, s.URL, "POST", "/api/v1/operations/generation-executions", "", "", map[string]any{"workspace_id": workspace, "plan_id": plan.ID, "plan_digest": plan.PlanDigest, "password": "incorrect-password"})
	if code != 403 {
		t.Fatal("invalid password executed", code)
	}
	receipt := execute(plan)
	code, data, _ = authRequest(t, c, s.URL, "GET", "/api/v1/workspace/generation-access", "", "", nil)
	access = decode[GenerationAccess](t, data)
	if code != 200 || access.State != "RESTRICTED" || !access.CanAppeal || access.Restriction == nil || strings.Contains(string(data), "private-evidence") {
		t.Fatal("restricted projection", code, string(data))
	}
	if receipt.Effect.RestrictionID != access.Restriction.ID {
		t.Fatal("receipt episode mismatch")
	}
	path := "/api/v1/restrictions/" + access.Restriction.ID + "/appeals"
	in := GenerationAppealInput{ReasonCode: "request_review", Statement: "PRIVATE_APPLICANT_STATEMENT 请检查本次限制。"}
	code, data, _ = authRequest(t, c, s.URL, "POST", path, "appeal-original-key", "", in)
	appeal := decode[GenerationAppeal](t, data)
	if code != 201 || appeal.State != "OPEN" || appeal.Statement != in.Statement {
		t.Fatal("appeal creation", code, appeal)
	}
	code, _, headers := authRequest(t, c, s.URL, "POST", path, "appeal-original-key", "", in)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("appeal replay", code)
	}
	code, _, _ = authRequest(t, c, s.URL, "POST", path, "appeal-original-key", "", GenerationAppealInput{ReasonCode: "issue_resolved"})
	if code != 409 {
		t.Fatal("changed original input", code)
	}
	code, _, _ = authRequest(t, c, s.URL, "POST", path, "appeal-second-key", "", in)
	if code != 409 {
		t.Fatal("duplicate episode appeal", code)
	}
	for _, listPath := range []string{"/api/v1/appeals", "/api/v1/operations/appeals?workspace_id=" + workspace} {
		code, data, _ = authRequest(t, c, s.URL, "GET", listPath, "", "", nil)
		if code != 200 || strings.Contains(string(data), "PRIVATE_APPLICANT_STATEMENT") || strings.Contains(string(data), "private-evidence") {
			t.Fatal("list leaked private detail", code, string(data))
		}
	}
	code, _, _ = authRequest(t, otherClient, s.URL, "GET", "/api/v1/appeals/"+appeal.ID, "", "", nil)
	if code != 404 {
		t.Fatal("cross-user appeal visible", code)
	}
	code, _, _ = authRequest(t, otherClient, s.URL, "POST", path, "foreign-appeal-key", "", in)
	if code != 404 && code != 409 {
		t.Fatal("cross-workspace appeal accepted", code)
	}
	code, _, _ = authRequest(t, c, s.URL, "GET", "/api/v1/operations/generation-access?workspace_id="+other.Workspace.ID, "", "", nil)
	if code != 404 {
		t.Fatal("ungranted workspace visible", code)
	}
	code, data, _ = authRequest(t, c, s.URL, "GET", "/api/v1/operations/appeals/"+appeal.ID+"?workspace_id="+workspace, "", "", nil)
	if code != 200 || decode[GenerationAppeal](t, data).Statement != in.Statement {
		t.Fatal("authorized detail missing", code)
	}
	lift := createPlan(GenerationPlanInput{WorkspaceID: workspace, Action: "lift_restriction", ExpectedVersion: access.Version, RestrictionID: access.Restriction.ID, ReasonCode: "review_complete", EvidenceRef: "ticket:private-resolution", PublicReply: "复核完成，已恢复后续新生成。"})
	execute(lift)
	code, data, _ = authRequest(t, c, s.URL, "GET", "/api/v1/appeals/"+appeal.ID, "", "", nil)
	closed := decode[GenerationAppeal](t, data)
	if code != 200 || closed.State != "RESOLVED" || closed.Outcome != "LIFTED" || closed.Version != 2 || closed.PublicReply == "" || strings.Contains(string(data), "private-resolution") {
		t.Fatal("reply not committed", code, string(data))
	}
	code, data, _ = authRequest(t, c, s.URL, "POST", path, "appeal-original-key", "", in)
	if code != 201 || decode[GenerationAppeal](t, data).Outcome != "LIFTED" {
		t.Fatal("replay lost resolved answer", code)
	}
	code, data, _ = authRequest(t, c, s.URL, "GET", "/api/v1/operations/generation-plans/"+lift.ID+"/receipt?workspace_id="+workspace, "", "", nil)
	if code != 200 || decode[GenerationReceipt](t, data).Effect.ToState != "OPEN" {
		t.Fatal("receipt not readable", code)
	}
	code, _, _ = authRequest(t, c, s.URL, "GET", "/readyz", "", "", nil)
	if code != 200 {
		t.Fatal("business rejection tripped readiness")
	}
}

func TestPostgresGenerationUnknownCommitRecoversOneReceipt(t *testing.T) {
	for _, action := range []string{"restrict_future", "lift_restriction"} {
		t.Run(action, func(t *testing.T) {
			f := newOperationsFixture(t)
			f.grant(t, "moderator")
			in := GenerationPlanInput{WorkspaceID: f.target.Workspace.ID, Action: "restrict_future", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "ticket:commit", PublicReply: "需要复核新生成权限。"}
			if action == "lift_restriction" {
				first, err := f.app.previewGenerationControl(f.principal, in)
				if err != nil {
					t.Fatal(err)
				}
				written, err := f.app.executeGenerationControl(f.principal, f.proof(t), in.WorkspaceID, first.ID, first.PlanDigest)
				if err != nil {
					t.Fatal(err)
				}
				in.Action = action
				in.ExpectedVersion = written.Effect.Version
				in.RestrictionID = written.Effect.RestrictionID
				in.ReasonCode = "review_complete"
				in.PublicReply = "复核完成，恢复后续新生成。"
			}
			plan, err := f.app.previewGenerationControl(f.principal, in)
			if err != nil {
				t.Fatal(err)
			}
			proof := f.proof(t)
			config := f.app.config
			f.app.cancel()
			f.app.wg.Wait()
			f.app.taskLeases.close()
			f.app.store.close()
			proxy, armed, dropped := commitAckDropProxy(t, config.DatabaseURL)
			store, err := openPostgres(proxy)
			if err != nil {
				t.Fatal(err)
			}
			f.app.store = store
			armed.Store(true)
			_, err = f.app.executeGenerationControl(f.principal, proof, in.WorkspaceID, plan.ID, plan.PlanDigest)
			requireOperation(t, err, "DATABASE_UNAVAILABLE")
			select {
			case <-dropped:
			case <-time.After(time.Second):
				t.Fatal("target COMMIT acknowledgement not dropped")
			}
			store.close()
			f.app.Close()
			restarted, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			server := httptest.NewServer(restarted.Handler())
			defer server.Close()
			code, data, _ := authRequest(t, f.client, server.URL, "GET", "/api/v1/operations/generation-plans/"+plan.ID+"/receipt?workspace_id="+in.WorkspaceID, "", "", nil)
			if code != 200 {
				t.Fatalf("receipt recovery %d %s", code, data)
			}
			receipt := decode[GenerationReceipt](t, data)
			if receipt.PlanID != plan.ID || receipt.Effect.ToState != plan.Effect.ToState {
				t.Fatal("wrong committed result")
			}
			code, _, _ = authRequest(t, f.client, server.URL, "POST", "/api/v1/operations/generation-executions", "", "", map[string]any{"workspace_id": in.WorkspaceID, "plan_id": plan.ID, "plan_digest": plan.PlanDigest, "password": "fixture-password-123"})
			if code != 409 {
				t.Fatal("committed plan executed again", code)
			}
			var count int
			if err = f.admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_generation_receipts WHERE plan_id=$1", plan.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate receipt", err, count)
			}
		})
	}
}
