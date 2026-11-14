package studio

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestOperationsAdminRejectsInputsBeforeConnection(t *testing.T) {
	base := OperationsAdminConfig{DatabaseURL: "postgres://private-secret@invalid", Action: "grant", WorkspaceID: newID("ws"), UserID: newID("usr"), Role: "finance", OperationKey: "role:001", Reason: "staff_assignment", EvidenceRef: "ticket:001"}
	for _, mutate := range []func(*OperationsAdminConfig){
		func(c *OperationsAdminConfig) { c.UserID += "\x00" }, func(c *OperationsAdminConfig) { c.WorkspaceID = "ws_demo" }, func(c *OperationsAdminConfig) { c.Role = "admin" }, func(c *OperationsAdminConfig) { c.OperationKey = "" }, func(c *OperationsAdminConfig) { c.EvidenceRef = "https://example.test/private" }, func(c *OperationsAdminConfig) { c.Reason = strings.Repeat("x", 129) }, func(c *OperationsAdminConfig) { c.Apply = true }, func(c *OperationsAdminConfig) { c.Action = "inspect" },
	} {
		c := base
		mutate(&c)
		_, err := AdministerOperations(context.Background(), c)
		if err == nil || strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid input reached connection or leaked config", err)
		}
	}
	base.DatabaseURL = "invalid private-secret"
	_, err := AdministerOperations(context.Background(), base)
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("connection configuration exposed", err)
	}
}

func TestPostgresOperationsOfflinePlanReplayRevokeAndFence(t *testing.T) {
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required"})
	c := authClient(t)
	id := registerFixture(t, c, s.URL, "offline-operator@example.test")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cfg := OperationsAdminConfig{DatabaseURL: db, Action: "grant", WorkspaceID: id.Workspace.ID, UserID: id.User.ID, Role: "finance", OperationKey: "role:001", Reason: "staff_assignment", EvidenceRef: "ticket:001"}
	if _, err := AdministerOperations(ctx, cfg); err == nil || !strings.Contains(err.Error(), "writer lock") {
		t.Fatal("active writer accepted", err)
	}
	a.Close()
	s.Close()
	preview, err := AdministerOperations(ctx, cfg)
	if err != nil || preview.Applied || preview.Result == nil || !preview.Result.Changed || preview.Result.Grant.Version != 1 || len(preview.PlanDigest) != 64 {
		t.Fatal(preview, err)
	}
	inspect := OperationsAdminConfig{DatabaseURL: db, Action: "inspect", UserID: id.User.ID, WorkspaceID: id.Workspace.ID}
	clean, err := AdministerOperations(ctx, inspect)
	if err != nil || len(clean.Grants) != 0 {
		t.Fatal("dry-run persisted grants", clean, err)
	}
	cfg.Apply = true
	cfg.PlanDigest = strings.Repeat("a", 64)
	if _, err = AdministerOperations(ctx, cfg); err == nil {
		t.Fatal("wrong plan accepted")
	}
	cfg.PlanDigest = preview.PlanDigest
	applied, err := AdministerOperations(ctx, cfg)
	if err != nil || !applied.Applied || applied.Result.Replayed || !applied.Result.Grant.Active {
		t.Fatal(applied, err)
	}
	replay, err := AdministerOperations(ctx, cfg)
	if err != nil || !replay.Result.Replayed || replay.Result.OperationID != applied.Result.OperationID {
		t.Fatal("replay changed receipt", replay, err)
	}
	conflict := cfg
	conflict.Role = "operator"
	if _, err = AdministerOperations(ctx, conflict); err == nil {
		t.Fatal("key reused for different role")
	}
	revoke := cfg
	revoke.Action = "revoke"
	revoke.OperationKey = "role:002"
	revoke.Apply = false
	revoke.PlanDigest = ""
	rp, err := AdministerOperations(ctx, revoke)
	if err != nil {
		t.Fatal(err)
	}
	revoke.Apply = true
	revoke.PlanDigest = rp.PlanDigest
	revoked, err := AdministerOperations(ctx, revoke)
	if err != nil || revoked.Result.Grant.Active || revoked.Result.Grant.Version != 2 {
		t.Fatal(revoked, err)
	}
	replay, err = AdministerOperations(ctx, cfg)
	if err != nil || !replay.Result.Replayed || replay.Result.OperationID != applied.Result.OperationID {
		t.Fatal(replay, err)
	}
	final, err := AdministerOperations(ctx, inspect)
	if err != nil || len(final.Grants) != 1 || final.Grants[0].Active || final.Grants[0].Version != 2 {
		t.Fatal("old replay regranted revoked role", final, err)
	}
	regrant := cfg
	regrant.OperationKey = "role:003"
	regrant.Apply = false
	regrant.PlanDigest = ""
	stale, err := AdministerOperations(ctx, regrant)
	if err != nil {
		t.Fatal(err)
	}
	other := regrant
	other.OperationKey = "role:004"
	op, err := AdministerOperations(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	other.Apply = true
	other.PlanDigest = op.PlanDigest
	if _, err = AdministerOperations(ctx, other); err != nil {
		t.Fatal(err)
	}
	regrant.Apply = true
	regrant.PlanDigest = stale.PlanDigest
	if _, err = AdministerOperations(ctx, regrant); err == nil {
		t.Fatal("stale role plan accepted")
	}
	conn, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal("inspection connection unavailable")
	}
	defer conn.Close(ctx)
	var receipts, audits int
	if err = conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM studio_operations_role_operations),(SELECT count(*) FROM studio_operations_audit)").Scan(&receipts, &audits); err != nil || receipts != 3 || audits != 3 {
		t.Fatal("dry-run or replay duplicated evidence", receipts, audits, err)
	}
	if _, err = conn.Exec(ctx, "DELETE FROM studio_operations_audit"); err == nil {
		t.Fatal("audit deletion accepted")
	}
	if _, err = conn.Exec(ctx, "UPDATE studio_operations_role_operations SET operation_id=operation_id"); err == nil {
		t.Fatal("receipt mutation accepted")
	}
	if _, err = conn.Exec(ctx, "UPDATE studio_operations_grants SET version=9007199254740990 WHERE id=$1", applied.Result.Grant.ID); err != nil {
		t.Fatal(err)
	}
	lastRevoke := revoke
	lastRevoke.OperationKey = "role:last-revoke"
	lastRevoke.Apply = false
	lastRevoke.PlanDigest = ""
	lastPlan, err := AdministerOperations(ctx, lastRevoke)
	if err != nil {
		t.Fatal("reserved revocation unavailable", err)
	}
	lastRevoke.Apply = true
	lastRevoke.PlanDigest = lastPlan.PlanDigest
	lastResult, err := AdministerOperations(ctx, lastRevoke)
	if err != nil || lastResult.Result.Grant.Active || lastResult.Result.Grant.Version != 9007199254740991 {
		t.Fatal("last revocation failed", lastResult, err)
	}
	capacity := regrant
	capacity.OperationKey = "role:exhausted"
	capacity.Apply = false
	capacity.PlanDigest = ""
	if _, err = AdministerOperations(ctx, capacity); err == nil {
		t.Fatal("exhausted grant re-enabled")
	}
	if _, err = conn.Exec(ctx, "UPDATE studio_operations_grants SET version=9007199254740990 WHERE id=$1", applied.Result.Grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = AdministerOperations(ctx, capacity); err == nil {
		t.Fatal("regrant consumed reserved revocation")
	}
}
