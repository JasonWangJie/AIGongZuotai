package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

// Generation and download services are deliberately unavailable after restore.
// This provider lacks the phased interface and records any mistaken fallback.
type reviewRestoreUnavailableMedia struct{ calls atomic.Int32 }

func (p *reviewRestoreUnavailableMedia) Submit(context.Context, TaskInput) (MediaHandle, error) {
	p.calls.Add(1)
	return MediaHandle{}, errors.New("restore fixture generation disabled")
}
func (p *reviewRestoreUnavailableMedia) Query(context.Context, MediaHandle) (MediaResult, error) {
	p.calls.Add(1)
	return MediaResult{}, errors.New("restore fixture query disabled")
}
func (p *reviewRestoreUnavailableMedia) Cancel(context.Context, MediaHandle) error {
	p.calls.Add(1)
	return errors.New("restore fixture cancellation disabled")
}

type reviewRestoreSnapshot struct {
	storageID  string
	review     *MediaReviewDispatch
	media      *MediaModerationRecord
	delivery   *MediaDeliveryRecord
	candidates []byte
}

func readReviewRestoreSnapshot(t *testing.T, conn *pgx.Conn, taskID string) reviewRestoreSnapshot {
	t.Helper()
	ctx := context.Background()
	var snapshot reviewRestoreSnapshot
	var err error
	if snapshot.review, err = loadMediaReviewDispatchSQL(ctx, conn, taskID); err != nil || snapshot.review == nil {
		t.Fatal("private review dispatch unavailable in restore fixture")
	}
	if snapshot.media, err = loadTaskMediaModerationSQL(ctx, conn, taskID); err != nil || snapshot.media == nil || len(snapshot.media.Candidates) != 2 {
		t.Fatal("complete private candidates unavailable in restore fixture")
	}
	if snapshot.delivery, err = loadMediaDeliverySQL(ctx, conn, taskID); err != nil || snapshot.delivery == nil || len(snapshot.delivery.Items) != 2 {
		t.Fatal("immutable delivery unavailable in restore fixture")
	}
	for _, item := range snapshot.delivery.Items {
		if item.Receipt == nil || item.Receipt.Delivery == nil || item.Receipt.Thumbnail == nil {
			t.Fatal("restore requires complete original and thumbnail receipts")
		}
	}
	if snapshot.candidates, err = encodeMediaCandidates(snapshot.media.Candidates, "image"); err != nil {
		t.Fatal("private candidate codec failed")
	}
	if err = conn.QueryRow(ctx, "SELECT storage_id::text FROM studio_storage_identity").Scan(&snapshot.storageID); err != nil {
		t.Fatal("stable storage identity unavailable")
	}
	return snapshot
}

func assertReviewRestoreIdentity(t *testing.T, before, after reviewRestoreSnapshot) {
	t.Helper()
	if before.storageID != after.storageID || !sameMediaReviewIdentity(before.review, after.review) ||
		!bytes.Equal(before.candidates, after.candidates) || !reflect.DeepEqual(before.media.Policy, after.media.Policy) ||
		!reflect.DeepEqual(before.media.TextPolicy, after.media.TextPolicy) || !sameMediaDeliveryHeader(before.delivery, after.delivery) ||
		!reflect.DeepEqual(before.delivery.Items, after.delivery.Items) {
		t.Fatal("restore changed database UUID, dispatch identity, frozen policy, candidates, ciphertext or complete receipts")
	}
}

// Opt-in genuine custom-format PostgreSQL dump/restore, never a schema clone
// passed off as backup recovery. All CREATE/DROP targets are generated test DBs.
func TestPostgresMediaReviewDispatchPrivateBackupRestore(t *testing.T) {
	bin := os.Getenv("TEST_POSTGRES_BIN_DIR")
	if bin == "" {
		t.Skip("TEST_POSTGRES_BIN_DIR absent; real review dump/restore not run")
	}
	for _, state := range []string{"READY", "STARTED"} {
		t.Run("local-"+strings.ToLower(state), func(t *testing.T) {
			verifyReviewDispatchPrivateBackupRestore(t, bin, state, AssetStorageConfig{})
		})
	}
	t.Run("real-s3-retained-objects-ready", func(t *testing.T) {
		endpoint := os.Getenv("TEST_S3_ENDPOINT")
		if endpoint == "" {
			t.Skip("TEST_S3_* absent; real S3 retained-object review restore not run")
		}
		if !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
			t.Fatal("S3 restore fixture requires an isolated loopback service")
		}
		storage := AssetStorageConfig{Backend: "s3", Endpoint: endpoint, Region: "us-east-1",
			Bucket: os.Getenv("TEST_S3_BUCKET"), Prefix: "review-restore/" + newID("run"),
			AccessKeyID: os.Getenv("TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"), ForcePathStyle: true}
		verifyReviewDispatchPrivateBackupRestore(t, bin, "READY", storage)
	})
}

func verifyReviewDispatchPrivateBackupRestore(t *testing.T, bin, state string, storage AssetStorageConfig) {
	t.Helper()
	media := newDeliveryHTTPFixture(t, -1)
	blockedPhase := ""
	if state == "STARTED" {
		blockedPhase = "output"
	}
	review := newReviewDispatchHTTPFixture(t, blockedPhase)
	cfg := reviewDispatchConfig(t, media, review, true, 1)
	_ = cfg.mediaProvider.(*AsyncMediaProvider).Close()
	cfg.AssetStorage = storage
	provider := media.provider(t, cfg.AssetDir, storage)
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
	a, server := testApp(t, cfg)
	client, identity := deliveryIdentity(t, server, true)
	a.pricing = &pricingCatalog{Version: "review-restore-price-v1", ProviderMode: "demo", Rates: map[string]LedgerPriceSnapshot{}}
	for _, model := range a.catalog.Public.Models {
		if model.Kind == "image" {
			for _, preset := range model.Capabilities.ParameterPresets {
				a.pricing.Rates[pricingKey(model.ID, "image", "generate", preset.ID)] = LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 100}
			}
		}
	}
	if err := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
		_, err := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: identity.Workspace.ID, AmountMicros: 100000, OperationKey: newID("credit"), Reason: "fixture", EvidenceRef: "fixture:review-restore"})
		return err
	}); err != nil {
		t.Fatal("restore fixture credit initialization failed")
	}
	key := newID("key")
	task, input := acceptMediaExecution(t, a, server, client, identity, "image", "restore complete private review batch", key, 2)
	prepareReviewDispatch(t, a, task.ID)
	if review.count("output") != 0 {
		t.Fatal("READY handoff already called the checker")
	}
	var done chan struct{}
	if state == "STARTED" {
		done = make(chan struct{})
		go func() { defer close(done); a.executePersistentTaskLane(mediaReviewLane, task.ID) }()
		deliveryWait(t, review.entered)
	}
	// Stop the only writer before copying the database, manifests and key.
	a.Close()
	server.Close()
	if done != nil {
		reviewDispatchDone(t, done)
	}
	review.unblock() // Any response to the cancelled first attempt is now late.
	media.server.Close()
	control := deliveryAdmin(t, cfg.DatabaseURL)
	before := readReviewRestoreSnapshot(t, control, task.ID)
	beforeScheduling := mediaScheduleBackupState(t, control)
	if before.review.State != state || before.media.Summary.State != "PENDING" || before.delivery.State != "READY" {
		t.Fatal("writer stop did not preserve the intended private recovery boundary")
	}
	beforeChecks := review.count("output")
	if state == "READY" && beforeChecks != 0 || state == "STARTED" && (beforeChecks != 1 || before.review.AttemptID == "") {
		t.Fatal("fixture did not reach the intended real HTTP review boundary")
	}
	if outboxCount(t, control, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 0 {
		t.Fatal("unreviewed candidates were registered before backup")
	}
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatal("invalid isolated database configuration")
	}
	schema := u.Query().Get("search_path")
	if !strings.HasPrefix(schema, "studio_test_") {
		t.Fatal("review restore requires an isolated test schema")
	}
	backup := t.TempDir()
	archive := filepath.Join(backup, "database.dump")
	deliveryPostgresTool(t, bin, "pg_dump", cfg.DatabaseURL, "--no-password", "--no-owner", "--no-acl", "--format=custom", "--schema="+schema, "--file="+archive)
	deliveryCopyPrivateTree(t, cfg.AssetDir, filepath.Join(backup, "assets"))
	deliveryCopyPrivateTree(t, cfg.ReferenceDir, filepath.Join(backup, "references"))
	keyPath := filepath.Join(backup, "delivery.key")
	if err = os.WriteFile(keyPath, []byte(cfg.MediaDelivery.EncryptionKey), 0600); err != nil {
		t.Fatal("private delivery key backup failed")
	}
	restoredDB := newID("studio_review_restore")
	if _, err = control.Exec(context.Background(), "CREATE DATABASE "+pgx.Identifier{restoredDB}.Sanitize()); err != nil {
		t.Fatal("isolated restore fixture requires CREATE DATABASE privilege")
	}
	t.Cleanup(func() {
		if _, err := control.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{restoredDB}.Sanitize()); err != nil {
			t.Error("isolated review restore database cleanup failed")
		}
	})
	u.Path = "/" + restoredDB
	restoredURL := u.String()
	deliveryPostgresTool(t, bin, "pg_restore", restoredURL, "--no-password", "--no-owner", "--no-acl", "--exit-on-error", "--dbname="+restoredDB, archive)
	restoredPrivate := deliveryAdmin(t, restoredURL)
	if !reflect.DeepEqual(beforeScheduling, mediaScheduleBackupState(t, restoredPrivate)) {
		t.Fatal("review backup changed scheduling projections, cursors or channel recovery evidence")
	}
	_ = restoredPrivate.Close(context.Background())
	restoredAssets, restoredRefs := t.TempDir(), t.TempDir()
	deliveryCopyPrivateTree(t, filepath.Join(backup, "assets"), restoredAssets)
	deliveryCopyPrivateTree(t, filepath.Join(backup, "references"), restoredRefs)
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal("private delivery key restore failed")
	}
	cfg.DatabaseURL, cfg.AssetDir, cfg.ReferenceDir = restoredURL, restoredAssets, restoredRefs
	unavailable := &reviewRestoreUnavailableMedia{}
	cfg.mediaProvider = unavailable
	cfg.MediaDelivery.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{79}, 32))
	wrong, err := New(cfg)
	if err == nil {
		wrong.Close()
		t.Fatal("restored dispatch accepted a mismatched delivery encryption key")
	}
	if unavailable.calls.Load() != 0 || review.count("output") != beforeChecks {
		t.Fatal("failed restore attempted generation or review")
	}
	cfg.MediaDelivery.EncryptionKey = string(keyBytes)
	restored, restoredServer := testApp(t, cfg)
	admin := deliveryAdmin(t, restoredURL)
	afterOpen := readReviewRestoreSnapshot(t, admin, task.ID)
	assertReviewRestoreIdentity(t, before, afterOpen)
	if review.count("output") != beforeChecks {
		t.Fatal("opening restored App started review before the explicit worker step")
	}
	if state == "READY" {
		if !sameMediaReviewRecord(before.review, afterOpen.review) || afterOpen.media.Summary.State != "PENDING" {
			t.Fatal("restore mutated an unstarted first-review handoff")
		}
		reviewDispatchStep(t, restored, task.ID)
	} else if afterOpen.review.State != "FINISHED" || afterOpen.review.AttemptID != before.review.AttemptID ||
		afterOpen.media.Summary.State != "REVIEW" || restored.executePersistentTaskLane(mediaReviewLane, task.ID) {
		t.Fatal("restored STARTED was not quarantined without a second checker call")
	}
	final := deliveryRead(t, restored, task.ID)
	after := readReviewRestoreSnapshot(t, admin, task.ID)
	assertReviewRestoreIdentity(t, before, after)
	if after.review.State != "FINISHED" || after.review.Version != 3 || final.Billing == nil || final.Billing.ReservedMicros != 200 {
		t.Fatal("restored task lost its durable first-review lifecycle or original hold")
	}
	wallet, err := readLedgerWalletSQL(context.Background(), admin, identity.Workspace.ID)
	if err != nil {
		t.Fatal("restored ledger unavailable")
	}
	wantLibrary := 0
	if state == "READY" {
		wantLibrary = 2
		if final.Status != "SUCCEEDED" || final.Moderation.State != "ALLOWED" || len(final.Artifacts) != 2 ||
			final.Billing.State != "SETTLED" || final.Billing.ChargedMicros != 200 || final.Billing.ReleasedMicros != 0 ||
			wallet.ChargedMicros != 200 || wallet.AvailableMicros != 99800 || wallet.ReservedMicros != 0 ||
			review.count("output") != 2 || after.delivery.State != "COMPLETE" || after.review.AttemptID == "" {
			t.Fatal("restored READY did not perform exactly one whole-batch review and settlement")
		}
	} else if final.Status != "FAILED" || final.Moderation.State != "REVIEW" || len(final.Artifacts) != 0 ||
		final.Billing.State != "REVIEW" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != 0 ||
		wallet.ChargedMicros != 0 || wallet.AvailableMicros != 99800 || wallet.ReservedMicros != 200 || wallet.ReviewMicros != 200 ||
		review.count("output") != 1 || after.delivery.State != "PARKED" || after.review.AttemptID != before.review.AttemptID {
		t.Fatal("restored STARTED was exposed, reviewed twice, or assumed free")
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != wantLibrary {
		t.Fatal("restored library does not match the committed review decision")
	}
	for _, item := range after.media.Candidates {
		code, body, _ := authRequest(t, client, restoredServer.URL, "GET", item.URL, "", identity.Project.ID, nil)
		if state == "READY" && (code != 200 || moderationSHA(body) != item.StorageInfo.SHA256) || state == "STARTED" && code != 404 {
			t.Fatal("restored original content access differs from the committed review state", code)
		}
		// Read the retained source privately to verify original identity. This
		// helper does not grant the HTTP caller access to quarantined content.
		reader, info, err := OpenMediaModerationAsset(context.Background(), restored.assets, item.ID)
		if err != nil {
			t.Fatal("restored private original identity did not verify")
		}
		_, copyErr := io.Copy(io.Discard, reader)
		closeErr := reader.Close()
		if copyErr != nil || closeErr != nil || !reflect.DeepEqual(info, *item.StorageInfo) {
			t.Fatal("restored private read changed the bound receipt")
		}
	}
	checks := review.count("output")
	code, raw, headers := authRequest(t, client, restoredServer.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, input)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != task.ID ||
		restored.executePersistentTaskLane(mediaReviewLane, task.ID) || unavailable.calls.Load() != 0 || review.count("output") != checks {
		t.Fatal("original-key restore replay re-executed generation or first review")
	}
	media.mu.Lock()
	submits, queries := media.count, media.queries[media.first]
	firstDownloads, secondDownloads := media.downloads[media.first+"/0"], media.downloads[media.first+"/1"]
	media.mu.Unlock()
	if submits != 1 || queries != 1 || firstDownloads != 1 || secondDownloads != 1 {
		t.Fatal("restore repeated provider POST, Query or original source downloads")
	}
	if storage.Backend == "s3" {
		versions := 0
		for _, item := range after.delivery.Items {
			for _, info := range []*AssetInfo{item.Receipt, item.Receipt.Thumbnail} {
				if info.StorageBackend != "s3" || info.ObjectETag == "" {
					t.Fatal("S3 restore did not retain complete remote object identity")
				}
				if info.ObjectVersion != "" {
					versions++
				}
			}
		}
		t.Logf("real pg_dump/pg_restore of READY with copied private files/key and retained S3 objects passed; remote version IDs preserved when supplied (%d/4); remote service/objects were not backed up, no cloud DR/RPO/RTO claim", versions)
	} else {
		t.Logf("real pg_dump/pg_restore of %s with copied private files/key, stable UUID/receipts, original-key replay and stopped generation/download service passed; no real AI/review provider or production DR claim", state)
	}
}
