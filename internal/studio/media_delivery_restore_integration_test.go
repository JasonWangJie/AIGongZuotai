package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

func deliveryCopyPrivateTree(t *testing.T, from, to string) {
	t.Helper()
	if e := filepath.WalkDir(from, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(from, p)
		if e != nil {
			return e
		}
		dst := filepath.Join(to, rel)
		if d.Type()&os.ModeSymlink != 0 {
			t.Fatal("fixture backup must not follow symlinks")
		}
		if d.IsDir() {
			return os.MkdirAll(dst, 0700)
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			t.Fatal("fixture backup contains a nonregular file")
		}
		raw, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		return os.WriteFile(dst, raw, 0600)
	}); e != nil {
		t.Fatal("private fixture copy failed")
	}
}

// Database credentials are passed only through the child's environment. Never
// place a connection URI/password in command arguments, logs or test evidence.
func deliveryPostgresTool(t *testing.T, dir, tool, dsn string, args ...string) {
	t.Helper()
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal("invalid local test database")
	}
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(v), "PG") {
			env = append(env, v)
		}
	}
	password, _ := u.User.Password()
	ssl := u.Query().Get("sslmode")
	if ssl == "" {
		ssl = "disable"
	}
	env = append(env, "PGHOST="+u.Hostname(), "PGPORT="+u.Port(), "PGUSER="+u.User.Username(), "PGPASSWORD="+password, "PGDATABASE="+strings.TrimPrefix(u.Path, "/"), "PGSSLMODE="+ssl, "PGOPTIONS=")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := filepath.Join(dir, tool)
	if _, e = os.Stat(name); e != nil {
		name += ".exe"
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	// No external database contents or diagnostic SQL are printed on failure.
	if _, e = cmd.CombinedOutput(); e != nil {
		t.Fatalf("local %s failed; no database secrets logged", tool)
	}
}

// Opt-in real pg_dump/pg_restore acceptance. Normal unit runs do not invent a
// restore result when the PostgreSQL tooling/CREATE DATABASE privilege is absent.
func TestPostgresMediaDeliveryPrivateBackupRestore(t *testing.T) {
	bin := os.Getenv("TEST_POSTGRES_BIN_DIR")
	if bin == "" {
		t.Skip("TEST_POSTGRES_BIN_DIR not configured; real dump/restore not run")
	}
	t.Run("local", func(t *testing.T) {
		verifyMediaDeliveryPrivateBackupRestore(t, bin, AssetStorageConfig{})
	})
	t.Run("real-s3-retained-objects", func(t *testing.T) {
		endpoint := os.Getenv("TEST_S3_ENDPOINT")
		if endpoint == "" {
			t.Skip("TEST_S3_* absent; actual S3 retained-object restore not run")
		}
		if !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
			t.Fatal("S3 restore acceptance requires an isolated loopback service")
		}
		storage := AssetStorageConfig{Backend: "s3", Endpoint: endpoint, Region: "us-east-1",
			Bucket: os.Getenv("TEST_S3_BUCKET"), Prefix: "delivery-restore/" + newID("run"),
			AccessKeyID: os.Getenv("TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"), ForcePathStyle: true}
		verifyMediaDeliveryPrivateBackupRestore(t, bin, storage)
	})
}

func verifyMediaDeliveryPrivateBackupRestore(t *testing.T, bin string, storage AssetStorageConfig) {
	t.Helper()
	f := newDeliveryHTTPFixture(t, 1)
	cfg := deliveryTestConfig(t, f, true)
	_ = cfg.mediaProvider.(*AsyncMediaProvider).Close()
	cfg.AssetStorage = storage
	provider := f.provider(t, cfg.AssetDir, cfg.AssetStorage)
	cfg.mediaProvider = provider
	if storage.Backend == "s3" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := provider.assets.destination.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(storage.Bucket)})
		if err != nil {
			if _, err = provider.assets.destination.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(storage.Bucket)}); err != nil {
				t.Fatal("isolated real S3 restore bucket unavailable")
			}
		}
	}
	a, s := testApp(t, cfg)
	client, id := deliveryIdentity(t, s, true)
	task, _ := acceptMediaExecution(t, a, s, client, id, "image", "backup between delivery items", newID("key"), 2)
	deliveryStep(t, a, task.ID)
	deliveryStep(t, a, task.ID)
	done := make(chan struct{})
	go func() { defer close(done); a.executeTaskStep(task.ID) }()
	deliveryWait(t, f.entered)
	a.Close()
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backup did not stop writer")
	}
	control, e := pgx.Connect(context.Background(), cfg.DatabaseURL)
	if e != nil {
		t.Fatal("local backup control unavailable")
	}
	t.Cleanup(func() { _ = control.Close(context.Background()) })
	beforeScheduling := mediaScheduleBackupState(t, control)
	readFirstReceipt := func(conn *pgx.Conn) AssetInfo {
		t.Helper()
		var raw []byte
		if err := conn.QueryRow(context.Background(), "SELECT receipt FROM studio_media_delivery_items WHERE task_id=$1 AND position=0", task.ID).Scan(&raw); err != nil {
			t.Fatal("completed first-item receipt unavailable")
		}
		var info AssetInfo
		if json.Unmarshal(raw, &info) != nil || info.Delivery == nil || info.Thumbnail == nil || !validAssetInfo(info, info.ID) {
			t.Fatal("first item has no complete original/thumbnail receipt")
		}
		return info
	}
	beforeReceipt := readFirstReceipt(control)
	if storage.Backend == "s3" && (beforeReceipt.StorageBackend != "s3" || beforeReceipt.Thumbnail.StorageBackend != "s3" ||
		beforeReceipt.ObjectETag == "" || beforeReceipt.Thumbnail.ObjectETag == "") {
		t.Fatal("pre-backup receipt did not identify retained remote objects")
	}
	u, e := url.Parse(cfg.DatabaseURL)
	if e != nil {
		t.Fatal(e)
	}
	schema := u.Query().Get("search_path")
	if !strings.HasPrefix(schema, "studio_test_") {
		t.Fatal("restore requires isolated test schema")
	}
	backup := t.TempDir()
	archive := filepath.Join(backup, "database.dump")
	deliveryPostgresTool(t, bin, "pg_dump", cfg.DatabaseURL, "--no-password", "--no-owner", "--no-acl", "--format=custom", "--schema="+schema, "--file="+archive)
	deliveryCopyPrivateTree(t, cfg.AssetDir, filepath.Join(backup, "assets"))
	deliveryCopyPrivateTree(t, cfg.ReferenceDir, filepath.Join(backup, "references"))
	keyPath := filepath.Join(backup, "delivery.key")
	if e = os.WriteFile(keyPath, []byte(cfg.MediaDelivery.EncryptionKey), 0600); e != nil {
		t.Fatal("fixture key backup unavailable")
	}
	restoredDB := newID("studio_delivery_restore")
	if _, e = control.Exec(context.Background(), "CREATE DATABASE "+pgx.Identifier{restoredDB}.Sanitize()); e != nil {
		t.Fatal("local restore requires CREATE DATABASE privilege")
	}
	t.Cleanup(func() {
		if _, e := control.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{restoredDB}.Sanitize()); e != nil {
			t.Error("isolated restored database cleanup failed")
		}
	})
	u.Path = "/" + restoredDB
	restoredURL := u.String()
	deliveryPostgresTool(t, bin, "pg_restore", restoredURL, "--no-password", "--no-owner", "--no-acl", "--exit-on-error", "--dbname="+restoredDB, archive)
	restoredPrivate := deliveryAdmin(t, restoredURL)
	if !reflect.DeepEqual(beforeScheduling, mediaScheduleBackupState(t, restoredPrivate)) {
		t.Fatal("delivery backup changed scheduling projections, cursors or channel recovery evidence")
	}
	_ = restoredPrivate.Close(context.Background())
	restoredAssets, restoredRefs := t.TempDir(), t.TempDir()
	deliveryCopyPrivateTree(t, filepath.Join(backup, "assets"), restoredAssets)
	deliveryCopyPrivateTree(t, filepath.Join(backup, "references"), restoredRefs)
	key, e := os.ReadFile(keyPath)
	if e != nil {
		t.Fatal("fixture key restore unavailable")
	}
	cfg.DatabaseURL, cfg.AssetDir, cfg.ReferenceDir = restoredURL, restoredAssets, restoredRefs
	cfg.MediaDelivery.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{78}, 32))
	cfg.mediaProvider = f.provider(t, restoredAssets, cfg.AssetStorage)
	bad, e := New(cfg)
	if e == nil {
		bad.Close()
		t.Fatal("restored encrypted manifest accepted wrong key")
	}
	_ = cfg.mediaProvider.(*AsyncMediaProvider).Close()
	cfg.MediaDelivery.EncryptionKey = string(key)
	cfg.mediaProvider = f.provider(t, restoredAssets, cfg.AssetStorage)
	f.unblock()
	reopened, _ := testApp(t, cfg)
	deliveryStep(t, reopened, task.ID)
	final := deliveryRead(t, reopened, task.ID)
	if final.Status != "SUCCEEDED" || len(final.Artifacts) != 2 {
		t.Fatal("matching database/private files/key did not restore delivery")
	}
	admin := deliveryAdmin(t, restoredURL)
	afterReceipt := readFirstReceipt(admin)
	if !reflect.DeepEqual(beforeReceipt, afterReceipt) {
		t.Fatal("restore replaced a completed receipt, object identity or retained version")
	}
	if e = cfg.mediaProvider.(*AsyncMediaProvider).assets.verifyDeliveryInfo(context.Background(), afterReceipt, "image", true); e != nil {
		t.Fatal("restored full original/thumbnail receipt did not verify")
	}
	var storageID, beforeStorageID string
	if e = control.QueryRow(context.Background(), "SELECT storage_id::text FROM studio_storage_identity").Scan(&beforeStorageID); e != nil {
		t.Fatal(e)
	}
	if e = admin.QueryRow(context.Background(), "SELECT storage_id::text FROM studio_storage_identity").Scan(&storageID); e != nil || storageID != beforeStorageID {
		t.Fatal("restore changed stable storage identity")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_media_delivery_items WHERE task_id=$1 AND receipt IS NOT NULL", task.ID) != 2 {
		t.Fatal("restored item receipts incomplete")
	}
	f.mu.Lock()
	submits, queries, firstDownloads := f.count, f.queries[f.first], f.downloads[f.first+"/0"]
	f.mu.Unlock()
	if submits != 1 || queries != 1 || firstDownloads != 1 {
		t.Fatal("restore repeated gateway work or replaced completed bytes")
	}
	if storage.Backend == "s3" {
		versions := 0
		for _, info := range []AssetInfo{afterReceipt, *afterReceipt.Thumbnail} {
			if info.ObjectVersion != "" {
				versions++
			}
		}
		t.Logf("real pg_dump/pg_restore with copied private manifests/key and retained remote objects passed; original/thumbnail version IDs retained when supplied (%d/2); remote service and object versions were not backed up or restored, no cloud DR/RPO/RTO claim", versions)
	} else {
		t.Log("real custom-format pg_dump/pg_restore, matching private directories and independent key passed; wrong key rejected; no real provider or production RPO/RTO claim")
	}
}
