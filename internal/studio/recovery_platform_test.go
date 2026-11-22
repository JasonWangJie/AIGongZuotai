//go:build recoverydrill

package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// scripts/recovery/workbench-drill.mjs creates the two isolated services and
// selects the recovery point. This probe exercises the application's normal
// HTTP and storage paths in each phase, across independent processes.
type recoveryDrillReceipt struct {
	DrillID, DatabaseIdentity, WorkspaceID, ProjectID              string
	TaskID, ArtifactID, AssetVersion, ThumbnailVersion             string
	OriginalURL, ThumbnailURL, ReferenceURL, ReferenceThumbnail    string
	OriginalHash, ThumbnailHash, ReferenceHash, ReferenceThumbHash string
	ReservationID                                                  string
}

func TestLocalRecoveryDrillPlatform(t *testing.T) {
	ctx := context.Background()
	phase, drillID := os.Getenv("RECOVERY_DRILL_PHASE"), os.Getenv("RECOVERY_DRILL_ID")
	if phase != "seed" && phase != "after_point" && phase != "verify" {
		t.Fatal("run the local recovery drill coordinator")
	}
	db := os.Getenv("RECOVERY_DRILL_DATABASE")
	pgURL, err := url.Parse(db)
	if err != nil || pgURL.Hostname() != "127.0.0.1" || pgURL.Port() == "" || drillID == "" {
		t.Fatal("a dedicated loopback drill database is required")
	}
	conn, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal("cannot connect to the local drill database")
	}
	defer conn.Close(ctx)
	var identified bool
	err = conn.QueryRow(ctx, "SELECT count(*)=1 AND bool_and(id=$1) FROM public.pitr_drill_identity", drillID).Scan(&identified)
	if err != nil || !identified {
		t.Fatal("database does not belong to this drill")
	}
	s3url := os.Getenv("TEST_S3_ENDPOINT")
	endpoint, err := url.Parse(s3url)
	if err != nil || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" {
		t.Fatal("a dedicated local object service is required")
	}
	storage := AssetStorageConfig{Backend: "s3", Endpoint: s3url, Region: os.Getenv("TEST_S3_REGION"), Bucket: os.Getenv("TEST_S3_BUCKET"), AccessKeyID: os.Getenv("TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"), ForcePathStyle: true, Prefix: "workbench-drill"}
	cfg := Config{DatabaseURL: db, AuthMode: "required", AssetDir: os.Getenv("RECOVERY_DRILL_ASSETS"), ReferenceDir: os.Getenv("RECOVERY_DRILL_REFERENCES"),
		AssetStorage: storage, BillingMode: "credits", BillingPriceFile: os.Getenv("RECOVERY_DRILL_PRICES"), MediaDelivery: MediaDeliveryConfig{EncryptionKey: os.Getenv("RECOVERY_DRILL_DELIVERY_KEY")}}
	receiptPath := os.Getenv("RECOVERY_DRILL_RECEIPT")
	privateRoot := os.Getenv("RECOVERY_DRILL_ROOT")
	location := filepath.Join(privateRoot, "platform-original")
	if phase == "verify" {
		location = filepath.Join(privateRoot, "platform-restored")
	}
	if !filepath.IsAbs(privateRoot) || filepath.Clean(cfg.AssetDir) != filepath.Join(location, "assets") ||
		filepath.Clean(cfg.ReferenceDir) != filepath.Join(location, "references") || filepath.Clean(cfg.BillingPriceFile) != filepath.Join(location, "price.json") ||
		filepath.Clean(receiptPath) != filepath.Join(privateRoot, "platform-receipt.json") {
		t.Fatal("private drill directories are required")
	}
	resolved, err := filepath.EvalSymlinks(location)
	if err != nil || !strings.EqualFold(resolved, location) {
		t.Fatal("drill platform directory must be local and unlinked")
	}
	var receipt recoveryDrillReceipt
	var channel *deliveryHTTPFixture
	if phase == "seed" {
		channel = newDeliveryHTTPFixture(t, -1)
		cfg.mediaProvider = deliveryTestProvider(t, channel.server, cfg.AssetDir, storage, 1)
	} else {
		data, err := os.ReadFile(receiptPath)
		if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.DrillID != drillID {
			t.Fatal("original drill evidence unavailable")
		}
	}
	app, srv := testApp(t, cfg)
	client := authClient(t)
	const email = "local-recovery-probe@example.test"
	password := os.Getenv("RECOVERY_DRILL_USER_PASSWORD")
	if len(password) < 16 {
		t.Fatal("one-time drill account password required")
	}
	verb := "login"
	body := map[string]string{"email": email, "password": password}
	if phase == "seed" {
		verb = "register"
		body["name"] = "Recovery fixture"
	}
	code, data, _ := authRequest(t, client, srv.URL, "POST", "/api/v1/auth/"+verb, "", "", body)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatal("cannot authenticate drill account", code)
	}
	id := decode[testIdentity](t, data)
	draftPath := "/api/v1/projects/" + id.Project.ID + "/draft"
	fetchHash := func(path string) string {
		t.Helper()
		if !strings.HasPrefix(path, "/api/v1/") {
			t.Fatal("unexpected private resource path")
		}
		status, data, _ := authRequest(t, client, srv.URL, "GET", path, "", id.Project.ID, nil)
		if status != http.StatusOK || len(data) == 0 {
			t.Fatal("could not read original private content", status)
		}
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	const original = "retained before the named restore point"
	const later = "change committed after the named restore point"
	if phase == "after_point" {
		if s, _, _ := authRequest(t, client, srv.URL, "PUT", draftPath, "drill-after-point", id.Project.ID, draftInput(1, later)); s != 200 {
			t.Fatal("could not commit the later revision", s)
		}
		return
	}
	if phase == "seed" {
		if s, _, _ := authRequest(t, client, srv.URL, "PUT", draftPath, "drill-before-point", id.Project.ID, draftInput(0, original)); s != 200 {
			t.Fatal("could not save original draft", s)
		}
		blob := referencePNG(t, 48)
		s, data, _ := uploadReferenceFixture(t, client, srv.URL, id.Project.ID, "drill-reference-key", "drill.png", blob, false)
		if s != 201 {
			t.Fatal("could not register original reference", s)
		}
		reference := decode[ReferenceAsset](t, data)
		httpCredit(t, app, id.Workspace.ID, 1_000_000)
		task, _ := acceptMediaExecution(t, app, srv, client, id, "image", "local recovery fixture", "drill-original-generation", 1)
		task = laneTaskStatus(t, app, task.ID, "SUCCEEDED")
		if len(task.Artifacts) != 1 || task.Billing == nil || task.Billing.ChargedMicros != 200_000 {
			t.Fatal("missing original image or actual internal settlement")
		}
		artifact := task.Artifacts[0]
		info, err := app.assets.Stat(ctx, artifact.ID)
		if err != nil || info.StorageBackend != "s3" || info.Thumbnail == nil || info.ObjectVersion == "" || info.Thumbnail.ObjectVersion == "" || info.ObjectVersion == "null" || info.Thumbnail.ObjectVersion == "null" {
			t.Fatal("original image and thumbnail require versioned objects")
		}
		entryURL := "/api/v1/library/" + libraryID("generated", task.ID, artifact.ID)
		receipt = recoveryDrillReceipt{DrillID: drillID, ProjectID: id.Project.ID, WorkspaceID: id.Workspace.ID, TaskID: task.ID, ArtifactID: artifact.ID, AssetVersion: info.ObjectVersion, ThumbnailVersion: info.Thumbnail.ObjectVersion,
			OriginalURL: entryURL + "/content", ThumbnailURL: entryURL + "/thumbnail", ReferenceURL: reference.URL, ReferenceThumbnail: reference.ThumbnailURL, ReservationID: task.Billing.ReservationID}
		receipt.DatabaseIdentity, err = storageOwnerID(ctx, conn)
		if err != nil {
			t.Fatal("durable storage identity unavailable")
		}
		receipt.OriginalHash, receipt.ThumbnailHash = fetchHash(receipt.OriginalURL), fetchHash(receipt.ThumbnailURL)
		receipt.ReferenceHash, receipt.ReferenceThumbHash = fetchHash(receipt.ReferenceURL), fetchHash(receipt.ReferenceThumbnail)
		sum := sha256.Sum256(channel.image)
		if receipt.OriginalHash != hex.EncodeToString(sum[:]) {
			t.Fatal("original generated image differed from accepted bytes")
		}
		encoded, err := json.Marshal(receipt)
		if err != nil || os.WriteFile(receiptPath, encoded, 0600) != nil {
			t.Fatal("could not retain local comparison evidence")
		}
		return
	}
	restoredID, err := storageOwnerID(ctx, conn)
	if err != nil || restoredID != receipt.DatabaseIdentity || id.Project.ID != receipt.ProjectID || id.Workspace.ID != receipt.WorkspaceID {
		t.Fatal("restored owner or workspace identity differs from original")
	}
	if s, data, _ := authRequest(t, client, srv.URL, "GET", draftPath, "", id.Project.ID, nil); s != 200 || decode[ProjectDraft](t, data).Revision != 1 || !bytes.Contains(data, []byte(original)) || bytes.Contains(data, []byte(later)) {
		t.Fatal("point-in-time recovery returned the wrong draft revision")
	}
	info, err := app.assets.Stat(ctx, receipt.ArtifactID)
	if err != nil || info.Thumbnail == nil || info.ObjectVersion != receipt.AssetVersion || info.Thumbnail.ObjectVersion != receipt.ThumbnailVersion {
		t.Fatal("restored object versions differ from original")
	}
	if fetchHash(receipt.OriginalURL) != receipt.OriginalHash || fetchHash(receipt.ThumbnailURL) != receipt.ThumbnailHash || fetchHash(receipt.ReferenceURL) != receipt.ReferenceHash || fetchHash(receipt.ReferenceThumbnail) != receipt.ReferenceThumbHash {
		t.Fatal("restored private content differs from original")
	}
	if status, _, _ := authRequest(t, authClient(t), srv.URL, "GET", receipt.OriginalURL, "", "", nil); status != 401 {
		t.Fatal("restored private image accessible without login")
	}
	s, data, _ := authRequest(t, client, srv.URL, "GET", "/api/v1/tasks/"+receipt.TaskID, "", id.Project.ID, nil)
	if s != 200 {
		t.Fatal("original task unavailable")
	}
	task := decode[Task](t, data)
	if task.Status != "SUCCEEDED" || task.Billing == nil || task.Billing.ReservationID != receipt.ReservationID || task.Billing.ChargedMicros != 200_000 || outboxCount(t, conn, "SELECT count(*) FROM studio_ledger_reservations") != 1 {
		t.Fatal("original settlement changed across recovery")
	}
	t.Log("Named recovery point restored the original private account, draft, reference bytes, object versions, and settled reservation.")
}
