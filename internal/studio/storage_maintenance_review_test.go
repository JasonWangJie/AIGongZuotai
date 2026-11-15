package studio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresMediaReviewDispatchMaintenancePreservesFilesAndInvalidatesPlans(t *testing.T) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "")
	cfg := reviewDispatchConfig(t, media, review, true, 1)
	a, s := testApp(t, cfg)
	client, identity := deliveryIdentity(t, s, true)
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	task, _, _ := laneAccept(t, a, s, client, identity, "private maintenance review phase")
	prepareReviewDispatch(t, a, task.ID)
	reviewDispatchState(t, admin, task.ID, "READY")
	private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
	if err != nil || private == nil || len(private.Candidates) != 1 {
		t.Fatal("missing durable maintenance candidate", err)
	}
	assetID := private.Candidates[0].ID
	beforeBytes := map[string][]byte{}
	for _, suffix := range []string{".bin", ".json", ".thumb.png"} {
		body, err := os.ReadFile(filepath.Join(cfg.AssetDir, assetID+suffix))
		if err != nil {
			t.Fatal(err)
		}
		beforeBytes[suffix] = body
	}
	maintenance := StorageMaintenanceConfig{DatabaseURL: cfg.DatabaseURL, ReferenceDir: cfg.ReferenceDir, AssetDir: cfg.AssetDir}
	if _, err = MaintainPrivateStorage(context.Background(), maintenance); err == nil {
		t.Fatal("maintenance bypassed the active application locks")
	}
	a.Close()
	s.Close()
	orphan := "blob_" + strings.Repeat("e", 64) + ".bin"
	orphanPath := filepath.Join(cfg.ReferenceDir, orphan)
	if err = os.WriteFile(orphanPath, []byte("review maintenance isolated orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err = os.Chtimes(orphanPath, old, old); err != nil {
		t.Fatal(err)
	}
	readyPlan, err := MaintainPrivateStorage(context.Background(), maintenance)
	if err != nil || readyPlan.SchemaVersion != 20 || len(readyPlan.Candidates) != 1 || readyPlan.Candidates[0].Name != orphan || readyPlan.ProtectedFiles != 3 {
		t.Fatal("READY candidates or receipts were not protected", err, readyPlan)
	}
	// A task-version change alone must invalidate a plan even when the dispatch
	// identity, file set and immutable receipts have not changed.
	if _, err = admin.Exec(context.Background(), `UPDATE studio_tasks SET document=jsonb_set(document,'{version}',to_jsonb((document->>'version')::bigint+1)) WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	maintenance.Apply, maintenance.PlanDigest = true, readyPlan.PlanDigest
	if _, err = MaintainPrivateStorage(context.Background(), maintenance); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatal("task revision did not fence old maintenance plan", err)
	}
	maintenance.Apply, maintenance.PlanDigest = false, ""
	versionPlan, err := MaintainPrivateStorage(context.Background(), maintenance)
	if err != nil || versionPlan.PlanDigest == readyPlan.PlanDigest {
		t.Fatal("task version missing from maintenance identity", err)
	}
	ledger, err := AdministerLedger(context.Background(), LedgerAdminConfig{DatabaseURL: cfg.DatabaseURL, Action: "inspect", WorkspaceID: identity.Workspace.ID})
	if err != nil || ledger.SchemaVersion != 20 {
		t.Fatal("ledger inspection does not accept exact020", err)
	}
	roles, err := AdministerOperations(context.Background(), OperationsAdminConfig{DatabaseURL: cfg.DatabaseURL, Action: "inspect", WorkspaceID: identity.Workspace.ID, UserID: identity.User.ID})
	if err != nil || roles.SchemaVersion != 20 {
		t.Fatal("operations inspection does not accept exact020", err)
	}
	if _, err = MigrateOwnedAssets(context.Background(), AssetMigrationConfig{DatabaseURL: cfg.DatabaseURL, Directory: cfg.AssetDir, Limit: 100}); err == nil || !strings.Contains(err.Error(), "delivery assets") {
		t.Fatal("019 migration did not preserve immutable delivery locator", err)
	}

	cfg.mediaProvider = media.provider(t, cfg.AssetDir)
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	reviewDispatchState(t, admin, task.ID, "READY")
	reviewDispatchStep(t, restarted, task.ID)
	reviewDispatchState(t, admin, task.ID, "FINISHED")
	restarted.Close()
	finishedPlan, err := MaintainPrivateStorage(context.Background(), maintenance)
	if err != nil || finishedPlan.PlanDigest == versionPlan.PlanDigest || finishedPlan.ProtectedFiles != 3 || len(finishedPlan.Candidates) != 1 {
		t.Fatal("finished first-review phase lost reachability or digest fencing", err)
	}
	maintenance.Apply, maintenance.PlanDigest = true, versionPlan.PlanDigest
	if _, err = MaintainPrivateStorage(context.Background(), maintenance); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatal("review phase change left prior plan executable", err)
	}
	for suffix, expected := range beforeBytes {
		actual, err := os.ReadFile(filepath.Join(cfg.AssetDir, assetID+suffix))
		if err != nil || string(actual) != string(expected) {
			t.Fatal("review or rejected maintenance changed retained private bytes", suffix, err)
		}
	}
	if _, err = os.Stat(orphanPath); err != nil {
		t.Fatal("rejected plans deleted a candidate", err)
	}
}
