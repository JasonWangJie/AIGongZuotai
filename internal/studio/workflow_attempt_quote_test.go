package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresWorkflowRerunNeedsNewTextAndImageQuotes(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	rates := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"4000000"},{"model_id":"studio-image","kind":"image","operation":"generate","preset_id":"preset-3","artifact_micros":"200000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(rates), 0600); err != nil {
		t.Fatal(err)
	}
	a, server := f.open(t)
	chat := &moderatedExecutionChat{text: "same public script for separate attempts"}
	a.chat = chat
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "fork-quotes@example.test")
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	httpCredit(t, a, identity.Workspace.ID, 1_000_000)
	old := startWorkflowStepTest(t, a, server, client, identity)
	original := old.input("script").Run
	oldQuote := httpBillingQuote(t, client, server, old.project, "run", original)
	_, raw, _ := old.request(200, "POST", old.path+"/cancel", newID("stop"), map[string]uint64{"expected_version": old.run.Version})
	source := decode[workflowDetail](t, raw)
	_, raw, _ = old.request(201, "POST", old.path+"/rerun", newID("fork"), workflowRerunInput{ExpectedVersion: source.Version, FromNode: "script"})
	w := old
	w.run = decode[WorkflowExecution](t, raw)
	w.path = "/api/v1/projects/" + old.project + "/workflow-runs/" + w.run.ID
	text := w.input("script").Run
	if text.WorkflowAttemptID != w.run.ID || text.Input != original.Input {
		t.Fatal("fork did not preserve the original text with its own quote identity")
	}
	other := *text
	other.WorkflowAttemptID = old.run.ID
	w.request(409, "POST", "/api/v1/billing/quotes", "", map[string]any{"resource_type": "run", "run_input": other})
	wrong := *text
	wrong.Input += " tampered"
	w.request(409, "POST", "/api/v1/billing/quotes", "", map[string]any{"resource_type": "run", "run_input": wrong})
	data, _ := w.execute(w.run.Version, "script", newID("wrong"), oldQuote.ID, 409)
	if !strings.Contains(string(data), "QUOTE_MISMATCH") {
		t.Fatal("old text quote was accepted or failed for wrong reason", string(data))
	}
	newQuote := httpBillingQuote(t, client, server, w.project, "run", text)
	direct := *text
	direct.QuoteID = newQuote.ID
	_, data, _ = w.request(409, "POST", "/api/v1/runs", newID("direct"), direct)
	if !strings.Contains(string(data), "WORKFLOW_STEP_REQUIRED") {
		t.Fatal("direct run route did not enforce the workflow guard", string(data))
	}
	accepted, _ := w.execute(w.run.Version, "script", newID("text"), newQuote.ID, 200)
	run := awaitAuthRun(t, client, server.URL, decode[workflowDetail](t, accepted).TextRunID)
	if run.Status != "COMPLETED" || run.Billing == nil || run.Billing.QuoteID != newQuote.ID || chat.calls.Load() != 1 {
		t.Fatal("fresh text quote did not execute once", run.Status)
	}
	ready := w.read()
	image := w.input("picture").Task
	if image.WorkflowAttemptID != w.run.ID {
		t.Fatal("image omitted fork quote identity")
	}
	oldImage := *image
	oldImage.WorkflowAttemptID = ""
	oldImageQuote := httpBillingQuote(t, client, server, w.project, "task", oldImage)
	data, _ = w.execute(ready.Version, "picture", newID("wrong"), oldImageQuote.ID, 409)
	if !strings.Contains(string(data), "QUOTE_MISMATCH") {
		t.Fatal("old image quote was accepted or failed for wrong reason", string(data))
	}
	incorrect := *image
	incorrect.Inputs.Prompt += " different"
	w.request(409, "POST", "/api/v1/billing/quotes", "", map[string]any{"resource_type": "task", "task_input": incorrect})
	newImageQuote := httpBillingQuote(t, client, server, w.project, "task", image)
	incorrect = *image
	incorrect.QuoteID = newImageQuote.ID
	_, data, _ = w.request(409, "POST", "/api/v1/tasks", newID("direct"), incorrect)
	if !strings.Contains(string(data), "WORKFLOW_STEP_REQUIRED") {
		t.Fatal("direct image generation bypassed guard", string(data))
	}
	key := newID("image")
	accepted, _ = w.execute(ready.Version, "picture", key, newImageQuote.ID, 200)
	id := decode[workflowDetail](t, accepted).ImageTaskID
	task := laneTaskStatus(t, a, id, "SUCCEEDED")
	if task.Billing == nil || task.Billing.QuoteID != newImageQuote.ID {
		t.Fatal("fresh image quote did not own the resulting task")
	}
	if again, h := w.execute(ready.Version, "picture", key, newImageQuote.ID, 200); h.Get("Idempotency-Replayed") != "true" || decode[workflowDetail](t, again).ImageTaskID != id {
		t.Fatal("original image receipt did not replay")
	}
	if n := outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id IN ($1,$2) AND reservation_id IS NULL", oldQuote.ID, oldImageQuote.ID); n != 2 {
		t.Fatal("unused old quotes consumed", n)
	}
	if n := outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations WHERE workspace_id=$1", identity.Workspace.ID); n != 2 {
		t.Fatal("expected only the two authorized reservations", n)
	}
}
