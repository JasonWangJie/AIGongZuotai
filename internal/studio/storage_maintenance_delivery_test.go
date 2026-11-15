package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func maintenanceReceiptFixture(t *testing.T, directory, delivery string, slot int) AssetInfo {
	t.Helper()
	body, err := os.ReadFile("testdata/references/source.png")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	info := AssetInfo{ID: mediaDeliveryAssetID(delivery, slot), ByteSize: int64(len(body)), SHA256: hex.EncodeToString(hash[:]), ContentType: "image/png", CreatedAt: time.Now().UTC(), Delivery: &AssetDeliveryReceipt{Version: 1, DeliveryID: delivery, Slot: slot, ItemSHA256: strings.Repeat("a", 64)}}
	info.Thumbnail = &AssetInfo{ID: info.ID, ByteSize: info.ByteSize, SHA256: info.SHA256, ContentType: "image/png", CreatedAt: info.CreatedAt}
	raw, _ := json.Marshal(info)
	for name, data := range map[string][]byte{info.ID + ".bin": body, info.ID + ".thumb.png": body, info.ID + ".json": raw} {
		if err = os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return info
}

func maintenanceDeliveryFixture(t *testing.T) (StorageMaintenanceConfig, *pgx.Conn, string, AssetInfo) {
	t.Helper()
	database := testDatabase(t)
	c := StorageMaintenanceConfig{DatabaseURL: database, ReferenceDir: t.TempDir(), AssetDir: t.TempDir()}
	store, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.close)
	lease, err := AcquireStorageLease(c.ReferenceDir, c.AssetDir)
	if err != nil {
		t.Fatal(err)
	}
	err = BindStorageOwnership(context.Background(), store.conn, c.ReferenceDir, c.AssetDir)
	_ = lease.Close()
	if err != nil {
		t.Fatal(err)
	}
	delivery := newID("delivery")
	receipt := maintenanceReceiptFixture(t, c.AssetDir, delivery, 2)
	if err = os.WriteFile(filepath.Join(c.AssetDir, mediaDeliveryAssetID(delivery, 1)+".bin"), []byte("partial import without a receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	task := Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", Status: "DELIVERING", ProviderMode: "demo", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	document, _ := json.Marshal(task)
	_, err = store.conn.Exec(context.Background(), `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll) VALUES($1,$2,$3,$4,'{}','{}',now())`, task.ID, task.WorkspaceID, task.ProjectID, document)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.conn.Exec(context.Background(), `INSERT INTO studio_media_deliveries(task_id,id,workspace_id,project_id,format_version,state,version,key_id,manifest_digest,handle_digest,ciphertext,item_count,result_status) VALUES($1,$2,'ws_demo','prj_demo',1,'READY',1,$3,$4,$4,$5,3,'SUCCEEDED')`, task.ID, delivery, strings.Repeat("a", 32), strings.Repeat("b", 64), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		var raw []byte
		var timestamp *time.Time
		if i == 2 {
			raw, _ = json.Marshal(receipt)
			now := time.Now().UTC()
			timestamp = &now
		}
		_, err = store.conn.Exec(context.Background(), `INSERT INTO studio_media_delivery_items(task_id,position,asset_id,receipt,receipted_at) VALUES($1,$2,$3,$4,$5)`, task.ID, i, mediaDeliveryAssetID(delivery, i), raw, timestamp)
		if err != nil {
			t.Fatal(err)
		}
	}
	store.close()
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	return c, admin, delivery, receipt
}

func TestPostgresDeliveryMaintenancePreservesPlansReceiptsAndStateDigest(t *testing.T) {
	c, admin, delivery, _ := maintenanceDeliveryFixture(t)
	orphan := "blob_" + strings.Repeat("d", 64) + ".bin"
	if err := os.WriteFile(filepath.Join(c.ReferenceDir, orphan), []byte("orphan fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{c.ReferenceDir, c.AssetDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if err = os.Chtimes(filepath.Join(dir, entry.Name()), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	plan, err := MaintainPrivateStorage(context.Background(), c)
	if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Name != orphan || plan.ProtectedFiles != 4 {
		t.Fatal("planned or complete files were not protected", err, plan)
	}
	// Only the durable plan state changes; asset IDs and files stay identical.
	if _, err = admin.Exec(context.Background(), `UPDATE studio_media_deliveries SET state='PARKED',version=version+1 WHERE id=$1`, delivery); err != nil {
		t.Fatal(err)
	}
	c.Apply, c.PlanDigest = true, plan.PlanDigest
	if _, err = MaintainPrivateStorage(context.Background(), c); err == nil {
		t.Fatal("old plan ignored changed durable delivery state")
	}
	if _, err = os.Stat(filepath.Join(c.ReferenceDir, orphan)); err != nil {
		t.Fatal("rejected apply removed file")
	}
	c.Apply = false
	parked, err := MaintainPrivateStorage(context.Background(), c)
	if err != nil || parked.PlanDigest == plan.PlanDigest || parked.ProtectedFiles != 4 {
		t.Fatal("terminal plan lost reachability", err)
	}
	c.Apply, c.PlanDigest = true, parked.PlanDigest
	result, err := MaintainPrivateStorage(context.Background(), c)
	if err != nil || result.RemovedFiles != 1 || !result.Applied {
		t.Fatal("reviewed orphan-only apply failed", err)
	}
	for _, suffix := range []string{".bin", ".json", ".thumb.png"} {
		if _, err = os.Stat(filepath.Join(c.AssetDir, mediaDeliveryAssetID(delivery, 2)+suffix)); err != nil {
			t.Fatal("receipt file removed")
		}
	}
	if _, err = os.Stat(filepath.Join(c.AssetDir, mediaDeliveryAssetID(delivery, 1)+".bin")); err != nil {
		t.Fatal("unreceipted planned file removed")
	}
	if _, err = os.Stat(filepath.Join(c.AssetDir, mediaDeliveryAssetID(delivery, 0)+".bin")); !os.IsNotExist(err) {
		t.Fatal("maintenance created missing plan content")
	}
}

func TestPostgresDeliveryMaintenanceRejectsMissingTamperedAndChangedReceipt(t *testing.T) {
	c, _, _, receipt := maintenanceDeliveryFixture(t)
	for _, suffix := range []string{".bin", ".json", ".thumb.png"} {
		path := filepath.Join(c.AssetDir, receipt.ID+suffix)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err = MaintainPrivateStorage(context.Background(), c); err == nil {
			t.Fatal("missing completed delivery file accepted", suffix)
		}
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		mutated := append([]byte(nil), body...)
		mutated[len(mutated)-1] ^= 1
		if err = os.WriteFile(path, mutated, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = MaintainPrivateStorage(context.Background(), c); err == nil {
			t.Fatal("changed completed delivery file accepted", suffix)
		}
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	changed := receipt
	changed.CreatedAt = changed.CreatedAt.Add(time.Second)
	raw, _ := json.Marshal(changed)
	if err := os.WriteFile(filepath.Join(c.AssetDir, receipt.ID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaintainPrivateStorage(context.Background(), c); err == nil {
		t.Fatal("valid manifest differing from frozen receipt accepted")
	}
}

func TestPostgresDeliveryMigrationRequiresOwnerAndProtectsEveryPlan(t *testing.T) {
	c, admin, delivery, _ := maintenanceDeliveryFixture(t)
	if _, err := MigrateAssets(context.Background(), AssetStorageConfig{}, c.AssetDir, false, 100, ""); err == nil {
		t.Fatal("legacy migration bypassed persistent owner")
	}
	migration := AssetMigrationConfig{Directory: c.AssetDir, Limit: 100}
	if _, err := MigrateOwnedAssets(context.Background(), migration); err == nil {
		t.Fatal("owned migration guessed missing database")
	}
	migration.DatabaseURL = c.DatabaseURL
	for _, state := range []string{"READY", "PARKED"} {
		if state == "PARKED" {
			if _, err := admin.Exec(context.Background(), `UPDATE studio_media_deliveries SET state='PARKED',version=2 WHERE id=$1`, delivery); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := MigrateOwnedAssets(context.Background(), migration); err == nil || !strings.Contains(err.Error(), "delivery assets") {
			t.Fatal("registered delivery migrated", state, err)
		}
	}
	lease, err := AcquireStorageLease(c.AssetDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = MigrateOwnedAssets(context.Background(), migration)
	_ = lease.Close()
	if err == nil {
		t.Fatal("migration bypassed active directory owner")
	}
	var locked bool
	if err = admin.QueryRow(context.Background(), "SELECT pg_try_advisory_lock($1)", int64(0x53545544494F)).Scan(&locked); err != nil || !locked {
		t.Fatal(err)
	}
	_, err = MigrateOwnedAssets(context.Background(), migration)
	_, unlockErr := admin.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", int64(0x53545544494F))
	if err == nil || unlockErr != nil {
		t.Fatal("migration bypassed active database writer", err, unlockErr)
	}
}

func TestPostgresDeliveryOfflineToolsRequireExact020(t *testing.T) {
	for _, mode := range []string{"legacy019", "gap019", "future021"} {
		t.Run(mode, func(t *testing.T) {
			database := testDatabase(t)
			conn, err := pgx.Connect(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(context.Background())
			sources := fstest.MapFS{}
			names, err := fs.Glob(migrationFiles, "migrations/*.sql")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				if mode == "legacy019" && strings.Contains(name, "/020_") || mode == "gap019" && strings.Contains(name, "/019_") {
					continue
				}
				body, readErr := migrationFiles.ReadFile(name)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if mode == "gap019" && strings.Contains(name, "/020_") {
					// 020 now reads the original 019 table during set-based
					// backfill. This deliberately corrupt registration fixture
					// executes the original 019 bytes without ever registering
					// 019, then executes/registers 020. It never deletes a prior
					// migration or changes the repository's SQL files.
					original019, sourceErr := migrationFiles.ReadFile("migrations/019_media_review_dispatch.sql")
					if sourceErr != nil {
						t.Fatal(sourceErr)
					}
					body = append(append(append([]byte{}, original019...), '\n'), body...)
				}
				sources[name] = &fstest.MapFile{Data: body}
			}
			tx, err := conn.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = applyMigrationsFrom(context.Background(), tx, sources); err != nil {
				_ = tx.Rollback(context.Background())
				t.Fatal(err)
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "future021" {
				if _, err = conn.Exec(context.Background(), "INSERT INTO studio_schema_versions(version) VALUES(21)"); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err = maintenanceReachable(context.Background(), conn); err == nil {
				t.Fatal("maintenance accepted incompatible schema")
			}
			if _, err = AdministerLedger(context.Background(), LedgerAdminConfig{DatabaseURL: database, Action: "inspect", WorkspaceID: "ws_demo"}); err == nil || !strings.Contains(err.Error(), "schema") {
				t.Fatal("ledger accepted incompatible schema", err)
			}
			if _, err = AdministerOperations(context.Background(), OperationsAdminConfig{DatabaseURL: database, Action: "inspect", WorkspaceID: newID("ws"), UserID: newID("usr")}); err == nil || !strings.Contains(err.Error(), "schema") {
				t.Fatal("roles accepted incompatible schema", err)
			}
			ownerID, err := storageOwnerID(context.Background(), conn)
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			owner, _ := json.Marshal(storageOwner{Version: 1, ID: ownerID, Role: "assets"})
			if err = os.WriteFile(filepath.Join(directory, storageOwnerName), owner, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = MigrateOwnedAssets(context.Background(), AssetMigrationConfig{Directory: directory, DatabaseURL: database, Limit: 100}); err == nil || !strings.Contains(err.Error(), "schema") {
				t.Fatal("migration accepted incompatible schema", err)
			}
		})
	}
}

func TestPostgresDeliveryMigrationStillCopiesOwnedLegacyAssets(t *testing.T) {
	database := testDatabase(t)
	referenceDir, assetDir := t.TempDir(), t.TempDir()
	store, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.close)
	lease, err := AcquireStorageLease(referenceDir, assetDir)
	if err != nil {
		t.Fatal(err)
	}
	err = BindStorageOwnership(context.Background(), store.conn, referenceDir, assetDir)
	_ = lease.Close()
	store.close()
	if err != nil {
		t.Fatal(err)
	}
	legacy := seedStorageAsset(t, assetDir)
	fixture := &s3Fixture{objects: map[string]s3StoredFixture{}}
	server := httptest.NewServer(fixture)
	defer server.Close()
	config := AssetMigrationConfig{Directory: assetDir, DatabaseURL: database, Limit: 100, Storage: AssetStorageConfig{Backend: "s3", Endpoint: server.URL, Region: "test", Bucket: "fixture-private", AccessKeyID: "fixture", SecretAccessKey: "not-real", ForcePathStyle: true}}
	plan, err := MigrateOwnedAssets(context.Background(), config)
	if err != nil || len(plan.Items) != 1 || plan.Items[0].ID != legacy.ID || plan.Applied {
		t.Fatal("owned legacy dry-run failed", err)
	}
	config.Apply = true
	applied, err := MigrateOwnedAssets(context.Background(), config)
	if err != nil || !applied.Applied || !applied.LocalCopiesRetained {
		t.Fatal("owned legacy copy failed", err)
	}
	fixture.mu.Lock()
	puts := fixture.puts
	fixture.mu.Unlock()
	if puts < 1 {
		t.Fatal("legacy migration did not use real fixture HTTP")
	}
	if _, err = MigrateOwnedAssets(context.Background(), config); err != nil {
		t.Fatal("verify-existing failed", err)
	}
	fixture.mu.Lock()
	after := fixture.puts
	fixture.mu.Unlock()
	if puts != after {
		t.Fatal("verify-existing rewrote objects")
	}
	for _, suffix := range []string{".bin", ".json", ".thumb.png"} {
		if _, err = os.Stat(filepath.Join(assetDir, legacy.ID+suffix)); err != nil {
			t.Fatal("migration lost local backup", suffix)
		}
	}
	wrongDatabase := testDatabase(t)
	wrong, err := openPostgres(wrongDatabase)
	if err != nil {
		t.Fatal(err)
	}
	wrong.close()
	config.DatabaseURL = wrongDatabase
	if _, err = MigrateOwnedAssets(context.Background(), config); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatal("wrong database migrated private directory", err)
	}
}
