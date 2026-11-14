package studio

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOperationsRequirePersistedIdentity(t *testing.T) {
	for _, config := range []Config{
		{OperationsEnabled: true},
		{OperationsEnabled: true, AuthMode: "required"},
		{OperationsEnabled: true, DatabaseURL: "postgres://unused.invalid/db", AuthMode: "disabled"},
	} {
		app, err := New(config)
		if err == nil {
			app.Close()
			t.Fatal("operations accepted without a persisted authenticated identity")
		}
	}
}

func TestPostgresOperationsUnknownCommitRecoversOneReceipt(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "finance")
	run := f.run(t, true)
	c := f.open(t, run)
	plan := f.plan(t, c, "release")
	proof := f.proof(t)
	database := f.app.config.DatabaseURL
	config := f.app.config
	// Stop unrelated background consumers before arming the protocol proxy, so
	// the dropped COMMIT acknowledgement belongs to this business transaction.
	f.app.cancel()
	f.app.wg.Wait()
	f.app.taskLeases.close()
	f.app.mu.Lock()
	f.app.runs[run.ID] = &runRecord{Run: copyRun(run), Subscribers: map[chan event]bool{}}
	f.app.mu.Unlock()
	f.app.store.close()
	proxy, armed, dropped := commitAckDropProxy(t, database)
	s, err := openPostgres(proxy)
	if err != nil {
		t.Fatal(err)
	}
	f.app.store = s
	armed.Store(true)
	_, err = f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "DATABASE_UNAVAILABLE")
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("proxy did not drop the committed operations ACK")
	}
	f.app.mu.Lock()
	cached := f.app.runs[run.ID].Run.Billing.State
	f.app.mu.Unlock()
	if cached != "REVIEW" || !s.failed.Load() {
		t.Fatal("unconfirmed execution published speculative cache or continued writing")
	}
	f.server.Close()
	f.app.Close()
	restarted, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	code, data, _ := authRequest(t, f.client, server.URL, "GET", "/api/v1/operations/cases/"+c.ID+"?workspace_id="+c.WorkspaceID, "", "", nil)
	if code != 200 {
		t.Fatalf("receipt recovery failed: %d %s", code, data)
	}
	recovered := decode[OperationsCase](t, data)
	if recovered.State != "RESOLVED" || recovered.Receipt == nil || recovered.Receipt.PlanID != plan.ID || recovered.Resource == nil || recovered.Resource.Billing == nil || recovered.Resource.Billing.State != "RELEASED" {
		t.Fatal("committed case, receipt and billing did not recover together")
	}
	before := outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_journals")
	code, _, _ = authRequest(t, f.client, server.URL, "POST", "/api/v1/operations/cases/"+c.ID+"/execute", "", "", map[string]any{
		"workspace_id": c.WorkspaceID, "expected_version": c.Version, "plan_id": plan.ID, "plan_digest": plan.PlanDigest, "password": "fixture-password-123",
	})
	if code != 409 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_operations_receipts WHERE case_id=$1", c.ID) != 1 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_journals") != before {
		t.Fatal("recovered execution was performed twice")
	}
	var state string
	if err = f.admin.QueryRow(context.Background(), "SELECT state FROM studio_ledger_reservations WHERE id=$1", run.Billing.ReservationID).Scan(&state); err != nil || state != "RELEASED" {
		t.Fatal("recovered hold state is incorrect", err)
	}
	t.Log("Dropped actual PostgreSQL COMMIT ACK: no speculative cache; App restart and authenticated GET recover one receipt and one financial operation; repeat execution rejected.")
}
