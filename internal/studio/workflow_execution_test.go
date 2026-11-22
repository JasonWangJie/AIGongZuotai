package studio

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type workflowStepTest struct {
	t             *testing.T
	c             *http.Client
	server        *httptest.Server
	project, path string
	run           WorkflowExecution
}

func startWorkflowStepTest(t *testing.T, a *App, server *httptest.Server, c *http.Client, id testIdentity) workflowStepTest {
	t.Helper()
	w := workflowStepTest{t: t, c: c, server: server, project: id.Project.ID}
	root := "/api/v1/projects/" + w.project
	_, data, _ := w.request(201, "POST", root+"/workflow-definitions", "define-priced-workflow", map[string]any{"expected_version": 0, "graph": workflowFixtureGraph(a)})
	def := decode[WorkflowDefinition](t, data)
	_, data, _ = w.request(201, "POST", root+"/workflow-runs", "start-priced-workflow", workflowStartInput{DefinitionID: def.ID, DefinitionVersion: def.Version, Input: "a coastal scene"})
	w.run = decode[WorkflowExecution](t, data)
	w.path = root + "/workflow-runs/" + w.run.ID
	return w
}

func (w workflowStepTest) request(want int, method, path, key string, body any) (int, []byte, http.Header) {
	w.t.Helper()
	code, data, h := authRequest(w.t, w.c, w.server.URL, method, path, key, w.project, body)
	if code != want {
		w.t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, data)
	}
	return code, data, h
}

func (w workflowStepTest) read() workflowDetail {
	w.t.Helper()
	_, data, _ := w.request(200, "GET", w.path, "", nil)
	return decode[workflowDetail](w.t, data)
}

func (w workflowStepTest) input(node string) workflowInput {
	w.t.Helper()
	_, data, _ := w.request(200, "GET", w.path+"/nodes/"+node+"/input", "", nil)
	return decode[workflowInput](w.t, data)
}

func (w workflowStepTest) execute(version uint64, node, key, quote string, want int) ([]byte, http.Header) {
	w.t.Helper()
	_, data, h := w.request(want, "POST", w.path+"/nodes/"+node+"/execute", key, map[string]any{"expected_version": version, "quote_id": quote})
	return data, h
}

func TestPostgresWorkflowBilledStepsUseFrozenInputsOnce(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	prices := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"4000000"},{"model_id":"studio-image","kind":"image","operation":"generate","preset_id":"preset-3","artifact_micros":"200000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	a, server := f.open(t)
	chat := &moderatedExecutionChat{text: "lighthouse in a coastal storm"}
	a.chat = chat
	c := authClient(t)
	id := registerFixture(t, c, server.URL, "workflow-billing@example.test")
	httpCredit(t, a, id.Workspace.ID, 1_000_000)
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	w := startWorkflowStepTest(t, a, server, c, id)
	text := w.input("script")
	if text.ResourceType != "run" || text.Run == nil || text.Run.Input != workflowPrompt("Describe the scene", w.run.Input) {
		t.Fatal("incorrect text quote input", text)
	}
	version := w.run.Version
	data, _ := w.execute(version, "script", "without-text-quote", "", 422)
	if !strings.Contains(string(data), "QUOTE_REQUIRED") {
		t.Fatal(string(data))
	}
	wrongInput := *text.Run
	wrongInput.Input += " changed"
	wrong := httpBillingQuote(t, c, server, w.project, "run", wrongInput)
	w.execute(version, "script", "different-text-quote", wrong.ID, 409)
	if outboxCount(t, db, "SELECT (SELECT count(*) FROM studio_runs)+(SELECT count(*) FROM studio_tasks)+(SELECT count(*) FROM studio_ledger_reservations)+(SELECT count(*) FROM studio_workflow_intents WHERE body IS NOT NULL)") != 0 || chat.calls.Load() != 0 {
		t.Fatal("rejected quote created a resource, held credits or left a frozen intent")
	}
	q := httpBillingQuote(t, c, server, w.project, "run", text.Run)
	data, _ = w.execute(version, "script", "accept-text-quote", q.ID, 200)
	accepted := decode[workflowDetail](t, data)
	run := awaitAuthRun(t, c, server.URL, accepted.TextRunID)
	if run.Status != "COMPLETED" || run.Output != chat.text || run.Billing == nil || run.Billing.State != "SETTLED" || run.Billing.ChargedMicros != 384 {
		t.Fatal("unexpected billed text", run)
	}
	data, h := w.execute(version, "script", "accept-text-quote", q.ID, 200)
	if decode[workflowDetail](t, data).TextRunID != run.ID || h.Get("Idempotency-Replayed") != "true" {
		t.Fatal("text receipt did not replay its original child")
	}
	w.execute(version+1, "script", "accept-text-quote", q.ID, 409)
	ready := w.read()
	if ready.Status != "IMAGE_READY" {
		t.Fatal("image step not available", ready)
	}
	image := w.input("picture")
	if image.ResourceType != "task" || image.Task == nil || image.Task.Inputs.Prompt != workflowPrompt("Light the scene", chat.text) || image.Task.Parameters.Count != 2 || image.Task.Operation != "generate" {
		t.Fatal("incorrect image quote input", image)
	}
	w.execute(ready.Version, "picture", "without-image-quote", "", 422)
	if outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 1 {
		t.Fatal("unquoted image created a task or a hold")
	}
	quote := httpBillingQuote(t, c, server, w.project, "task", image.Task)
	data, _ = w.execute(ready.Version, "picture", "accept-image-quote", quote.ID, 200)
	taskID := decode[workflowDetail](t, data).ImageTaskID
	task := laneTaskStatus(t, a, taskID, "SUCCEEDED")
	if task.Billing == nil || task.Billing.State != "SETTLED" || task.Billing.ChargedMicros != 400_000 || len(task.Artifacts) != 2 {
		t.Fatal("unexpected billed image", task)
	}
	data, h = w.execute(ready.Version, "picture", "accept-image-quote", quote.ID, 200)
	if decode[workflowDetail](t, data).ImageTaskID != taskID || h.Get("Idempotency-Replayed") != "true" {
		t.Fatal("image receipt did not replay its original task")
	}
	w.request(200, "GET", w.path+"/nodes/picture/receipt", "accept-image-quote", nil)
	f.mu.Lock()
	posts := len(f.prompts)
	f.mu.Unlock()
	if chat.calls.Load() != 1 || posts != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_journals WHERE kind='CHARGE'") != 2 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_journals WHERE kind='RESERVE'") != 2 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", wrong.ID) != 1 {
		t.Fatal("unexpected duplicate execution, additional reservation or charge")
	}
}

func TestPostgresWorkflowBlockedTextNeverBecomesImageInput(t *testing.T) {
	a, server, c, id, database := newModeratedBillingFixture(t)
	chat := &moderatedExecutionChat{text: "BLOCK-OUTPUT unapproved candidate"}
	a.chat = chat
	w := startWorkflowStepTest(t, a, server, c, id)
	q := httpBillingQuote(t, c, server, w.project, "run", w.input("script").Run)
	data, _ := w.execute(w.run.Version, "script", "accept-reviewed-text", q.ID, 200)
	run := awaitAuthRun(t, c, server.URL, decode[workflowDetail](t, data).TextRunID)
	if run.Moderation == nil || run.Moderation.State != "BLOCKED" || run.Output != "" || run.Billing == nil || run.Billing.State != "REVIEW" {
		t.Fatal("incorrect moderation or billing outcome", run)
	}
	db := deliveryAdmin(t, database)
	stopped := w.read()
	if stopped.Status != "FAILED" || stopped.TextRun == nil || stopped.TextRun.Output != "" || stopped.ImageTaskID != "" || len(stopped.Candidates) != 0 {
		t.Fatal("workflow exposed unapproved text", stopped)
	}
	_, receipt, _ := w.request(200, "GET", w.path+"/nodes/script/receipt", "accept-reviewed-text", nil)
	if strings.Contains(string(receipt), chat.text) {
		t.Fatal("receipt exposed private text")
	}
	w.request(409, "GET", w.path+"/nodes/picture/input", "", nil)
	w.execute(stopped.Version, "picture", "never-start-image", q.ID, 409)
	w.request(404, "GET", w.path+"/nodes/picture/receipt", "never-start-image", nil)
	if outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_workflow_intents WHERE kind='image' AND body IS NOT NULL") != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations WHERE state='REVIEW' AND charged_micros=0 AND released_micros=0") != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_journals WHERE kind='CHARGE'") != 0 || chat.calls.Load() != 1 {
		t.Fatal("blocked text advanced or settled a new step")
	}
}

func TestWorkflowNodeInputUsesFrozenModelAndApprovedText(t *testing.T) {
	a, _ := testApp(t, Config{})
	g := workflowFixtureGraph(a)
	n := g.Nodes[1]
	d := workflowDetail{WorkflowExecution: WorkflowExecution{ProjectID: "prj_demo", Status: "IMAGE_READY", ProviderMode: a.config.Mode, Graph: g, FrozenModels: map[string]Model{"image": a.catalog.Entries[n.ModelID].Model}}, TextRun: &Run{Status: "COMPLETED", Output: "pending", Moderation: &ModerationSummary{State: "REVIEW"}}}
	if _, err := a.workflowNodeInput(d, n); err == nil {
		t.Fatal("unapproved text supplied to image model")
	}
	d.TextRun.Moderation.State = "ALLOWED"
	ok, err := a.workflowNodeInput(d, n)
	if err != nil || ok.Task == nil || ok.Task.Inputs.Prompt != "Light the scene\n\npending" {
		t.Fatal(ok, err)
	}
	d.ProviderMode = "other"
	if _, err = a.workflowNodeInput(d, n); err == nil {
		t.Fatal("model mode change accepted")
	}
}
