package studio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
)

func TestPostgresOperationsRunSSEReplaysCommittedBilling(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "finance")
	run := f.run(t, true)
	customer := authClient(t)
	code, _, _ := authRequest(t, customer, f.server.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{
		"email": f.target.User.Email, "password": "fixture-password-123",
	})
	if code != 200 {
		t.Fatal("subject could not log in", code)
	}
	// The persisted fixture is terminal. Populate the same replay event as a
	// completed worker so that cold-cache snapshot fallback cannot mask a gap.
	f.app.mu.Lock()
	record, err := f.app.getRunLocked(run.ID)
	if err == nil {
		f.app.publishRunLocked(record, "run.failed", map[string]any{"run_id": run.ID, "seq": run.Seq, "status": run.Status})
	}
	f.app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c := f.open(t, run)
	plan := f.plan(t, c, "release")
	result, err := f.app.executeOperationsCase(f.principal, f.proof(t), c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	previous := strconv.FormatUint(run.Seq, 10)
	path := "/api/v1/runs/" + run.ID + "/events?after=" + previous
	read := func() Run {
		t.Helper()
		code, raw, _ := authRequest(t, customer, f.server.URL, "GET", path, "", run.ProjectID, nil)
		if code != 200 {
			t.Fatal("terminal stream failed", code)
		}
		name, id, data := readSSEFixture(t, bufio.NewReader(bytes.NewReader(raw)))
		if name != "run.snapshot" || id != strconv.FormatUint(result.Receipt.ResourceVersion, 10) {
			t.Fatal("terminal cursor did not advance to the committed resolution", name, id)
		}
		var snapshot Run
		payload, err := json.Marshal(data)
		if err != nil || json.Unmarshal(payload, &snapshot) != nil {
			t.Fatal("invalid terminal snapshot")
		}
		return snapshot
	}
	hot := read()
	f.app.mu.Lock()
	delete(f.app.runs, run.ID)
	f.app.mu.Unlock()
	cold := read()
	for _, snapshot := range []Run{hot, cold} {
		if snapshot.ID != run.ID || snapshot.Status != run.Status || snapshot.Seq != run.Seq+1 ||
			snapshot.Billing == nil || snapshot.Billing.State != "RELEASED" || snapshot.Output != run.Output {
			t.Fatal("hot or cold terminal replay lost the committed billing or public state")
		}
	}
}

func TestOperationsRunSnapshotPreservesModerationGate(t *testing.T) {
	record := &runRecord{Subscribers: make(map[chan event]bool)}
	app := &App{config: Config{ReplaySize: 1}, runs: map[string]*runRecord{"run_fixture": record}}
	private := "unreleased private candidate"
	run := Run{ID: "run_fixture", Status: "FAILED", Seq: 4, Output: private, Moderation: &ModerationSummary{State: "BLOCKED"}}
	app.cacheOperationsResourceLocked(operationsResource{Run: &run})
	if record.Run.Output != "" || len(record.Events) != 1 || record.Events[0].Name != "run.snapshot" || bytes.Contains(record.Events[0].Data, []byte(private)) {
		t.Fatal("financial resolution released an unapproved output")
	}
}
