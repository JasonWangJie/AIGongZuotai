package studio

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPostgresOfflineLedgerPlanReplayAndWriterFence(t *testing.T) {
	database := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: database, AuthMode: "required"})
	client := authClient(t)
	identity := registerFixture(t, client, s.URL, "ledger-operator@example.test")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := LedgerAdminConfig{DatabaseURL: database, Action: "grant", Credit: LedgerCreditRequest{WorkspaceID: identity.Workspace.ID, OperationKey: "operator-grant-001", AmountMicros: 1234567, Reason: "local-fixture", EvidenceRef: "fixture:offline-001"}}
	if _, err := AdministerLedger(ctx, c); err == nil || !strings.Contains(err.Error(), "writer lock") {
		t.Fatal("active writer not fenced", err)
	}
	a.Close()
	s.Close()
	preview, err := AdministerLedger(ctx, c)
	if err != nil || preview.Applied || preview.Result == nil || preview.Result.Wallet.AvailableMicros != 1234567 || len(preview.PlanDigest) != 64 {
		t.Fatal("invalid preview", preview, err)
	}
	inspection, err := AdministerLedger(ctx, LedgerAdminConfig{DatabaseURL: database, Action: "inspect", WorkspaceID: identity.Workspace.ID})
	if err != nil || inspection.Before.AvailableMicros != 0 || inspection.Reconciliation.JournalCount != 0 {
		t.Fatal("dry-run mutated ledger", inspection, err)
	}
	c.Apply = true
	c.PlanDigest = strings.Repeat("a", 64)
	if _, err = AdministerLedger(ctx, c); err == nil {
		t.Fatal("wrong plan accepted")
	}
	c.PlanDigest = preview.PlanDigest
	applied, err := AdministerLedger(ctx, c)
	if err != nil || !applied.Applied || applied.Result.Replayed || applied.Result.Wallet.AvailableMicros != 1234567 {
		t.Fatal("apply failed", applied, err)
	}
	replay, err := AdministerLedger(ctx, c)
	if err != nil || !replay.Result.Replayed || replay.Result.OperationID != applied.Result.OperationID || replay.Reconciliation.JournalCount != 1 {
		t.Fatal("replay minted credits", replay, err)
	}
	c.Credit.AmountMicros++
	if _, err = AdministerLedger(ctx, c); err == nil {
		t.Fatal("operation key accepted another amount")
	}
	c.Apply = false
	c.PlanDigest = ""
	c.Credit.OperationKey = "operator-grant-002"
	stale, err := AdministerLedger(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	other := c
	other.Credit.OperationKey = "operator-grant-003"
	op, err := AdministerLedger(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	other.Apply = true
	other.PlanDigest = op.PlanDigest
	if _, err = AdministerLedger(ctx, other); err != nil {
		t.Fatal(err)
	}
	c.Apply = true
	c.PlanDigest = stale.PlanDigest
	if _, err = AdministerLedger(ctx, c); err == nil {
		t.Fatal("stale wallet plan accepted")
	}
	final, err := AdministerLedger(ctx, LedgerAdminConfig{DatabaseURL: database, Action: "inspect", WorkspaceID: identity.Workspace.ID})
	if err != nil || final.Reconciliation.JournalCount != 2 || final.Before.AvailableMicros != 2469135 {
		t.Fatal("failed apply left changes", final, err)
	}
}
