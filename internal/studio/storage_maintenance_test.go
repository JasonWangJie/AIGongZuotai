package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageLeaseExclusiveAndReleasesPartialAcquisition(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a")
	second := filepath.Join(dir, "z")
	held, err := AcquireStorageLease(second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if failed, err := AcquireStorageLease(first, second); err == nil {
		failed.Close()
		t.Fatal("accepted another owner")
	}
	available, err := AcquireStorageLease(first, filepath.Join(first, "."))
	if err != nil {
		t.Fatalf("partial acquisition leaked first lock: %v", err)
	}
	available.Close()
	available.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestStorageLeaseChild$")
	command.Env = append(os.Environ(), "STUDIO_LEASE_CHILD="+second)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock failed: %v %s", err, out)
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	available, err = AcquireStorageLease(second)
	if err != nil {
		t.Fatal("OS lock not released", err)
	}
	available.Close()
	if _, err = os.Stat(filepath.Join(second, storageLeaseName)); err != nil {
		t.Fatal("lock file should remain, not be unlinked")
	}
}

func TestStorageLeaseChild(t *testing.T) {
	directory := os.Getenv("STUDIO_LEASE_CHILD")
	if directory == "" {
		return
	}
	lease, err := AcquireStorageLease(directory)
	if err == nil {
		lease.Close()
		t.Fatal("child acquired a held directory")
	}
}

func TestMaintenanceScanAndExactPlanFiles(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	protectedID := "blob_" + strings.Repeat("a", 64)
	orphanID := "blob_" + strings.Repeat("b", 64)
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{protectedID + ".bin", orphanID + ".bin", "reference_part_" + strings.Repeat("c", 32) + ".tmp", "notes.txt", storageLeaseName} {
		if err = os.WriteFile(filepath.Join(directory, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Chtimes(filepath.Join(directory, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	recentName := "blob_" + strings.Repeat("d", 64) + ".bin"
	if err = os.WriteFile(filepath.Join(directory, recentName), []byte("recent"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(directory, "blob_"+strings.Repeat("e", 64)+".bin"), 0700); err != nil {
		t.Fatal(err)
	}
	report := StorageMaintenanceReport{Candidates: []MaintenanceFile{}}
	if err = maintenanceScan(context.Background(), root, "references", map[string]bool{protectedID + ".bin": true}, nil, time.Now().Add(-24*time.Hour), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 2 || report.ProtectedFiles != 1 || report.RecentFiles != 1 || report.IgnoredFiles != 3 {
		t.Fatalf("unexpected scan: %+v", report)
	}
	for _, candidate := range report.Candidates {
		if candidate.Name == "notes.txt" || strings.Contains(candidate.Name, "/") {
			t.Fatal("unsafe candidate")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = maintenanceInspect(ctx, root, "references", orphanID+".bin"); err == nil {
		t.Fatal("cancelled hash accepted")
	}
	if err = os.Symlink(filepath.Join(directory, orphanID+".bin"), filepath.Join(directory, "blob_"+strings.Repeat("f", 64)+".bin")); err == nil {
		if _, err = maintenanceInspect(context.Background(), root, "references", "blob_"+strings.Repeat("f", 64)+".bin"); err == nil {
			t.Fatal("symlink candidate accepted")
		}
	} else {
		t.Log("symlink subcase not run: OS privilege unavailable")
	}
}

func TestPostgresStorageMaintenanceReachabilityAndDryRun(t *testing.T) {
	databaseURL := testDatabase(t)
	referenceDir, assetDir := t.TempDir(), t.TempDir()
	store, err := openPostgres(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// Use actual reference metadata, including a thumbnail, and an immutable
	// prepared body attached to a completed task. Trash does not remove sources.
	referenceStore, err := OpenReferenceStore(referenceDir)
	if err != nil {
		store.close()
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/references/source.png")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := referenceStore.PutImage(context.Background(), bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	referenceStore.Close()
	// The body fixture is a named private blob so no gateway is involved.
	bodyBytes := []byte("immutable accepted multipart request")
	bodyHash := sha256.Sum256(bodyBytes)
	bodyID := "blob_" + hex.EncodeToString(bodyHash[:])
	if err = os.WriteFile(filepath.Join(referenceDir, bodyID+".bin"), bodyBytes, 0600); err != nil {
		t.Fatal(err)
	}
	blobJSON, _ := json.Marshal(blob)
	if _, err = store.conn.Exec(context.Background(), "INSERT INTO studio_references(id,workspace_id,project_id,name,blob,stored_bytes,created_at) VALUES('ref_gc','ws_demo','prj_demo','original',$1,$2,now())", blobJSON, blob.ByteSize+blob.Thumbnail.ByteSize); err != nil {
		store.close()
		t.Fatal(err)
	}
	assetID := "asset_" + strings.Repeat("a", 64)
	assetHash := sha256.Sum256(fixture)
	assetInfo := AssetInfo{ID: assetID, ByteSize: int64(len(fixture)), ContentType: "image/png", SHA256: hex.EncodeToString(assetHash[:]), CreatedAt: time.Now().UTC()}
	assetMetadata, _ := json.Marshal(assetInfo)
	for name, data := range map[string][]byte{assetID + ".bin": fixture, assetID + ".json": assetMetadata} {
		if err = os.WriteFile(filepath.Join(assetDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	task := Task{ID: "task_gc", WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", Status: "SUCCEEDED", ProviderMode: "live", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Artifacts: []Artifact{{ID: assetID, Kind: "image", URL: "/api/v1/assets/" + assetID}}}
	doc, _ := json.Marshal(task)
	submission, _ := json.Marshal(MediaSubmission{BodyFileID: bodyID})
	if _, err = store.conn.Exec(context.Background(), "INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,submission,next_poll) VALUES($1,$2,$3,$4,'{}','{}',$5,now())", task.ID, task.WorkspaceID, task.ProjectID, doc, submission); err != nil {
		store.close()
		t.Fatal(err)
	}
	leaseForBinding, err := AcquireStorageLease(referenceDir, assetDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = BindStorageOwnership(context.Background(), store.conn, referenceDir, assetDir); err != nil {
		leaseForBinding.Close()
		store.close()
		t.Fatal(err)
	}
	leaseForBinding.Close()
	if err = CheckMemoryStorageOwnership(referenceDir, assetDir); err == nil {
		store.close()
		t.Fatal("memory writer accepted persistent owner directories")
	}
	config := StorageMaintenanceConfig{DatabaseURL: databaseURL, ReferenceDir: referenceDir, AssetDir: assetDir}
	if _, err = MaintainPrivateStorage(context.Background(), config); err == nil {
		store.close()
		t.Fatal("maintenance entered while DB writer active")
	}
	store.close()
	wrongDatabase := testDatabase(t)
	wrongStore, err := openPostgres(wrongDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if err = BindStorageOwnership(context.Background(), wrongStore.conn, referenceDir, assetDir); err == nil {
		wrongStore.close()
		t.Fatal("different database adopted an owned directory")
	}
	unknownReferenceDir, unknownAssetDir := t.TempDir(), t.TempDir()
	if err = os.WriteFile(filepath.Join(unknownReferenceDir, "blob_"+strings.Repeat("e", 64)+".bin"), []byte("another database's original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = BindStorageOwnership(context.Background(), wrongStore.conn, unknownReferenceDir, unknownAssetDir); err == nil {
		wrongStore.close()
		t.Fatal("empty unrelated database adopted published private content")
	}
	wrongStore.close()
	wrongConfig := config
	wrongConfig.DatabaseURL = wrongDatabase
	if _, err = MaintainPrivateStorage(context.Background(), wrongConfig); err == nil {
		t.Fatal("wrong database authorized file maintenance")
	}
	orphanName := "blob_" + strings.Repeat("b", 64) + ".bin"
	orphanPath := filepath.Join(referenceDir, orphanName)
	if err = os.WriteFile(orphanPath, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, directory := range []string{referenceDir, assetDir} {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			if entry.Name() != storageLeaseName {
				if err = os.Chtimes(filepath.Join(directory, entry.Name()), old, old); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	plan, err := MaintainPrivateStorage(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Name != orphanName || plan.ProtectedFiles != 5 || plan.Applied {
		t.Fatalf("reachability not protected: %+v", plan)
	}
	if _, err = os.Stat(orphanPath); err != nil {
		t.Fatal("dry run removed file")
	}
	config.Apply = true
	config.PlanDigest = strings.Repeat("0", 64)
	if _, err = MaintainPrivateStorage(context.Background(), config); err == nil {
		t.Fatal("wrong plan accepted")
	}
	config.PlanDigest = plan.PlanDigest
	lease, err := AcquireStorageLease(referenceDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = MaintainPrivateStorage(context.Background(), config); err == nil {
		t.Fatal("active directory writer bypassed")
	}
	lease.Close()
	// Altering candidate bytes after review invalidates the whole plan.
	if err = os.WriteFile(orphanPath, []byte("mutated"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(orphanPath, old, old)
	if _, err = MaintainPrivateStorage(context.Background(), config); err == nil {
		t.Fatal("changed candidate accepted")
	}
	config.Apply = false
	plan, err = MaintainPrivateStorage(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	config.Apply = true
	config.PlanDigest = plan.PlanDigest
	result, err := MaintainPrivateStorage(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedFiles != 1 || !result.Applied {
		t.Fatalf("orphan not removed: %+v", result)
	}
	if _, err = os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatal("orphan still present")
	}
	for _, path := range []string{filepath.Join(referenceDir, blob.ID+".bin"), filepath.Join(referenceDir, blob.Thumbnail.ID+".bin"), filepath.Join(referenceDir, bodyID+".bin"), filepath.Join(assetDir, assetID+".bin"), filepath.Join(assetDir, assetID+".json")} {
		if _, err = os.Stat(path); err != nil {
			t.Fatal("reachable file removed")
		}
	}
	config.Apply = false
	config.ReferenceDir = t.TempDir()
	if _, err = MaintainPrivateStorage(context.Background(), config); err == nil {
		t.Fatal("wrong incomplete storage root accepted")
	}
}
