package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func coreDeliveryKey() string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{47}, 32)) }

type coreDeliveryQueryFixture struct {
	MediaProvider
	queries, submits, imports atomic.Int32
	handle                    string
	item                      MediaDeliveryItem
}

func (p *coreDeliveryQueryFixture) Submit(context.Context, TaskInput) (MediaHandle, error) {
	p.submits.Add(1)
	return MediaHandle{}, mediaDeliveryFailure()
}
func (p *coreDeliveryQueryFixture) QueryStatus(_ context.Context, h MediaHandle) (MediaQueryResult, error) {
	p.queries.Add(1)
	if h.ID != p.handle {
		return MediaQueryResult{}, mediaDeliveryFailure()
	}
	return MediaQueryResult{Result: MediaResult{Status: "SUCCEEDED"}, Items: []MediaDeliveryItem{p.item}}, nil
}
func (p *coreDeliveryQueryFixture) LoadDeliveryReceipt(context.Context, string, MediaDeliveryItem) (Artifact, bool, error) {
	return Artifact{}, false, nil
}
func (p *coreDeliveryQueryFixture) ImportDeliveryItem(context.Context, string, MediaDeliveryItem) (Artifact, error) {
	p.imports.Add(1)
	return Artifact{}, mediaDeliveryFailure()
}

func TestMediaDeliveryCipherBindsPrivateManifestAndStableIdentity(t *testing.T) {
	c, err := newMediaDeliveryCipher(coreDeliveryKey(), true, false)
	if err != nil {
		t.Fatal(err)
	}
	c.databaseID = "database-fixture"
	record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo"}, Handle: MediaHandle{ID: "original-handle", Kind: "image", Count: 1}}
	result := MediaQueryResult{Result: MediaResult{Status: "SUCCEEDED"}, Items: []MediaDeliveryItem{{Slot: 0, Kind: "image", SourceURL: "https://fixture.invalid/result?signature=PRIVATE", ContentType: "image/png", ByteSize: 100}}}
	record.Delivery, err = c.seal(record, result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(record.Delivery.Ciphertext, []byte("PRIVATE")) {
		t.Fatal("private source stored in plaintext")
	}
	got, err := c.open(record)
	if err != nil || !reflect.DeepEqual(got, result.Items) {
		t.Fatal("frozen descriptor changed", err)
	}
	// Mutable progress must never participate in immutable AAD.
	record.Delivery.Version, record.Delivery.State = 9, "COMPLETE"
	if _, err = c.open(record); err != nil {
		t.Fatal("progress invalidated immutable manifest", err)
	}
	for _, change := range []string{"database", "task", "scope", "handle", "digest", "ciphertext", "key"} {
		t.Run(change, func(t *testing.T) {
			copy := cloneLeasedRecord(record)
			cipher := *c
			switch change {
			case "database":
				cipher.databaseID += "-other"
			case "task":
				copy.Task.ID = newID("task")
			case "scope":
				copy.Task.ProjectID = newID("prj")
			case "handle":
				copy.Handle.ID += "-other"
			case "digest":
				copy.Delivery.ManifestDigest = strings.Repeat("a", 64)
			case "ciphertext":
				copy.Delivery.Ciphertext[len(copy.Delivery.Ciphertext)-1] ^= 1
			case "key":
				other, _ := newMediaDeliveryCipher(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{48}, 32)), true, false)
				cipher = *other
			}
			if _, err := cipher.open(copy); err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("tampered manifest accepted or source exposed", err)
			}
		})
	}
	// Four independently signed 8 KiB URLs exceed the former 32 KiB budget.
	record.Handle.Count = 4
	result.Items = nil
	for i := range 4 {
		result.Items = append(result.Items, MediaDeliveryItem{Slot: i, Kind: "image", SourceURL: "https://fixture.invalid/" + strings.Repeat("x", 8100)})
	}
	if _, err = c.seal(record, result); err != nil {
		t.Fatal("valid bounded four-file manifest rejected", err)
	}
	result.Items[0].SourceURL = "https://fixture.invalid/" + strings.Repeat("x", 8192)
	if _, err = c.seal(record, result); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestMediaDeliveryEncryptionConfigurationIsIndependent(t *testing.T) {
	for _, key := range []string{"", "invalid", coreDeliveryKey() + "\n", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := newMediaDeliveryCipher(key, true, false); err == nil {
			t.Fatal("invalid persistent key accepted")
		}
		app, err := newIsolatedApp(t, Config{DatabaseURL: "postgres://127.0.0.1:1/unused", mediaProvider: &coreDeliveryQueryFixture{}, MediaDelivery: MediaDeliveryConfig{EncryptionKey: key}})
		if err == nil {
			app.Close()
			t.Fatal("persistent phased adapter started without valid key")
		}
		if !strings.Contains(err.Error(), "media delivery requires") {
			t.Fatal("key was not rejected before database migration", err)
		}
	}
	if c, err := newMediaDeliveryCipher("", false, false); err != nil || c != nil {
		t.Fatal("legacy provider unexpectedly requires key")
	}
	if c, err := newMediaDeliveryCipher("", true, true); err != nil || c == nil {
		t.Fatal("memory phased provider lacks ephemeral key")
	}
	for _, secret := range []string{coreDeliveryKey(), strings.Repeat("/", 32)} {
		for _, field := range []string{"gateway", "media", "mail", "password"} {
			c := Config{MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()}}
			switch field {
			case "gateway":
				c.APIKey = secret
			case "media":
				c.AsyncImageAPIKey = secret
			case "mail":
				c.Mail.OutboxKey = secret
			case "password":
				c.Mail.Password = secret
			}
			if err := validateMediaDeliveryKeyIndependence(c); err == nil || strings.Contains(err.Error(), secret) {
				t.Fatal("shared key accepted or disclosed", field)
			}
		}
	}
}

// The real server commits, then the TCP fixture drops only that COMMIT ACK.
// Worker sessions use the direct URL so renew/claim cannot consume the fault.
func TestPostgresMediaDeliveryUnknownCommitPreservesFourBoundaries(t *testing.T) {
	for _, stage := range []string{"manifest", "receipt", "candidate", "final"} {
		t.Run(stage, func(t *testing.T) {
			database := testDatabase(t)
			proxy, armed, dropped := commitAckDropProxy(t, database)
			s, err := openPostgres(proxy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.close)
			cfg := Config{DatabaseURL: database, Mode: "demo", TaskWorkers: 1, PollInterval: time.Hour, AssetDir: filepath.Join(t.TempDir(), "assets"), ReferenceDir: filepath.Join(t.TempDir(), "references"), MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()}}
			cfg.defaults()
			cipher, err := newMediaDeliveryCipher(coreDeliveryKey(), true, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
				cipher.databaseID, err = storageOwnerID(ctx, conn)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			a := &App{store: s, config: cfg, delivery: cipher, ctx: context.Background(), tasks: map[string]*taskRecord{}, workspaceSubscribers: map[chan event]subscriptionScope{}}
			if err = a.initTaskLeases(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.taskLeases.close)
			raw, _ := mediaBytesFixture(t, "allow.png")
			var downloads atomic.Int32
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); _, _ = w.Write(raw) }))
			defer gateway.Close()
			provider := deliveryTestProvider(t, gateway, cfg.AssetDir, AssetStorageConfig{}, 1)
			a.media = provider
			text, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
			if err != nil {
				t.Fatal(err)
			}
			policy := mediaPolicyFixture(t).Snapshot()
			input, key, now := taskInput("image"), newID("key"), time.Now().UTC()
			record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ModelID: input.ModelID, ModelVersion: "frozen-fixture", ProviderMode: "demo", Prompt: input.Inputs.Prompt, Status: "RUNNING", Version: 1, CreatedAt: now, UpdatedAt: now, Parameters: &input.Parameters, Artifacts: []Artifact{}, Moderation: InitialMediaModerationSummary(policy, "output")}, Input: input, Handle: mediaFixtureHandle(provider, "image", 1), NextPoll: now}
			record.MediaModeration = &MediaModerationRecord{Policy: policy, TextPolicy: text.Snapshot(), Summary: record.Task.Moderation, Candidates: []Artifact{}}
			if err = s.createTask(record, "ws_demo:tasks:"+key, payloadHash(input)); err != nil {
				t.Fatal(err)
			}
			conn, err := a.taskLeases.acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			candidate := claimTask(t, a, conn, record.Task.ID)
			a.taskLeases.release(conn)
			a.tasks[record.Task.ID] = cloneLeasedRecord(candidate)
			item := MediaDeliveryItem{Slot: 0, Kind: "image", SourceURL: gateway.URL + "/original?signed=PRIVATE", ContentType: "image/png", ByteSize: int64(len(raw))}
			candidate.Delivery, err = cipher.seal(candidate, MediaQueryResult{Result: MediaResult{Status: "SUCCEEDED"}, Items: []MediaDeliveryItem{item}})
			if err != nil {
				t.Fatal(err)
			}
			candidate.Task.Status = "INGESTING"
			candidate.Task.Version++
			var artifact Artifact
			if stage != "manifest" {
				if err = a.persistTaskClaim(candidate, false); err != nil {
					t.Fatal(err)
				}
				artifact, err = provider.ImportDeliveryItem(context.Background(), candidate.Delivery.ID, item)
				if err != nil {
					t.Fatal(err)
				}
				candidate.Delivery.Items[0].Receipt = artifact.StorageInfo
				candidate.Task.Version++
				if stage != "receipt" {
					if err = a.persistTaskClaim(candidate, false); err != nil {
						t.Fatal(err)
					}
					candidate.MediaModeration.Candidates = []Artifact{artifact}
					candidate.Task.Status = "MODERATING"
					candidate.Task.Version++
					if stage == "final" {
						if err = a.persistTaskClaim(candidate, false); err != nil {
							t.Fatal(err)
						}
						candidate.Task.Moderation.State = "ALLOWED"
						candidate.Task.Moderation.ReasonCode = "allowed"
						candidate.Task.Moderation.Version = 2
						candidate.MediaModeration.Summary = candidate.Task.Moderation
						candidate.Task.Status = "SUCCEEDED"
						candidate.Task.Progress = 100
						candidate.Task.Artifacts = []Artifact{artifact}
						candidate.Task.Version++
					}
				}
			}
			before := cloneLeasedRecord(a.tasks[record.Task.ID])
			events := make(chan event, 8)
			a.workspaceSubscribers[events] = subscriptionScope{WorkspaceID: "ws_demo", ProjectID: "prj_demo"}
			armed.Store(true)
			err = a.persistTaskClaim(candidate, stage == "final")
			if err == nil || !s.failed.Load() {
				t.Fatal("lost acknowledgement did not fail closed")
			}
			select {
			case <-dropped:
			case <-time.After(time.Second):
				t.Fatal("target COMMIT ACK was not dropped")
			}
			cached := a.tasks[record.Task.ID]
			if cached.Task.Version != before.Task.Version || !reflect.DeepEqual(cached.Delivery, before.Delivery) || len(cached.Task.Artifacts) != 0 || len(events) != 0 {
				t.Fatal("unconfirmed private/public result escaped into cache or notifications")
			}
			a.taskLeases.close()
			s.close()
			_ = provider.Close()
			cfg.mediaProvider = deliveryTestProvider(t, gateway, cfg.AssetDir, AssetStorageConfig{}, 1)
			restarted, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			restarted.mu.Lock()
			recovered, err := restarted.getTaskLocked(record.Task.ID)
			restarted.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Delivery == nil || recovered.Delivery.ID != candidate.Delivery.ID {
				t.Fatal("committed immutable manifest was lost")
			}
			if stage == "manifest" && recovered.Delivery.Items[0].Receipt != nil || stage != "manifest" && !sameMediaDeliveryReceipt(recovered.Delivery.Items[0].Receipt, artifact.StorageInfo) {
				t.Fatal("receipt boundary recovery differs")
			}
			if stage == "candidate" {
				if recovered.Task.Status != "FAILED" || recovered.Task.Moderation.State != "REVIEW" || recovered.Delivery.State != "PARKED" {
					t.Fatal("persisted candidate automatically resumed review")
				}
			} else if stage == "final" {
				if recovered.Task.Status != "SUCCEEDED" || recovered.Task.Moderation.State != "ALLOWED" || recovered.Delivery.State != "COMPLETE" || len(recovered.Task.Artifacts) != 1 {
					t.Fatal("committed final release lost")
				}
			} else if recovered.Delivery.State != "READY" || len(recovered.Task.Artifacts) != 0 {
				t.Fatal("private handoff leaked or disappeared")
			}
			body, _ := json.Marshal(recovered.Task)
			if bytes.Contains(body, []byte("PRIVATE")) || bytes.Contains(body, []byte(gateway.URL)) {
				t.Fatal("private source exposed by task")
			}
			got, replay, err := restarted.createTask(input, key)
			if err != nil || !replay || got.ID != record.Task.ID {
				t.Fatal("original idempotency was lost", err)
			}
			wantDownloads := int32(1)
			if stage == "manifest" {
				wantDownloads = 0
			}
			if downloads.Load() != wantDownloads {
				t.Fatal("restart repeated remote request")
			}
		})
	}
}

func TestPostgresMediaDeliveryAtomicFencesAndImmutableEvidence(t *testing.T) {
	database := testDatabase(t)
	a, err := newIsolatedApp(t, Config{DatabaseURL: database, PollInterval: time.Hour, MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	task, _, err := a.createTask(taskInput("image"), "delivery-atomic-fences")
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	original := a.tasks[task.ID]
	original.Handle = MediaHandle{ID: "original-handle", Kind: "image", Count: 1}
	original.Task.Status = "RUNNING"
	original.Task.Version++
	err = a.store.saveTask(original)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTask(t, a, admin, task.ID)
	item := MediaDeliveryItem{Slot: 0, Kind: "image", SourceURL: "https://fixture.invalid/original?signature=PRIVATE"}
	claim.Delivery, err = a.delivery.seal(claim, MediaQueryResult{Result: MediaResult{Status: "SUCCEEDED"}, Items: []MediaDeliveryItem{item}})
	if err != nil {
		t.Fatal(err)
	}
	claim.Task.Status = "INGESTING"
	claim.Task.Version++
	setBacklog := func(full bool) {
		t.Helper()
		query := "UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton"
		if full {
			query = "UPDATE studio_outbox_limits SET pending=10000 WHERE singleton"
		}
		if _, e := admin.Exec(context.Background(), query); e != nil {
			t.Fatal(e)
		}
	}
	setBacklog(true)
	if err = a.persistTaskClaim(claim, false); !isEventBacklog(err) {
		t.Fatal("manifest commit ignored bounded outbox", err)
	}
	var count int
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_media_deliveries WHERE task_id=$1", task.ID).Scan(&count); err != nil || count != 0 || claim.Delivery.Version != 0 {
		t.Fatal("rolled back manifest/plan escaped", err)
	}
	files, err := os.ReadDir(a.config.AssetDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "asset_") {
			t.Fatal("uncommitted source caused asset import")
		}
	}
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_media_delivery_items WHERE task_id=$1", task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("uncommitted source retained plans or receipts", err)
	}
	setBacklog(false)
	// There was no committed manifest and no downloaded byte. Recovery may
	// query the same handle again, and freeze a whole new single response.
	expireTaskLease(t, admin, task.ID)
	recovery := claimTask(t, a, admin, task.ID)
	if recovery.Delivery != nil {
		t.Fatal("rollback left a recoverable manifest")
	}
	item.SourceURL = "https://fixture.invalid/new-response?signature=PRIVATE"
	provider := &coreDeliveryQueryFixture{handle: recovery.Handle.ID, item: item}
	a.media = provider
	a.executeTaskClaim(admin, recovery)
	if provider.queries.Load() != 1 || provider.submits.Load() != 0 || provider.imports.Load() != 0 {
		t.Fatal("uncommitted manifest recovery was not status-only")
	}
	claim = claimTask(t, a, admin, task.ID)
	opened, err := a.delivery.open(claim)
	if err != nil || len(opened) != 1 || opened[0].SourceURL != item.SourceURL {
		t.Fatal("recovery mixed descriptors from uncommitted response", err)
	}
	stored := cloneMediaDelivery(claim.Delivery)
	raw, _ := mediaBytesFixture(t, "allow.png")
	info, err := importMediaModerationFixture(context.Background(), a.assets, mediaDeliveryIdentity(stored.ID, 0), "image", raw)
	if err != nil {
		t.Fatal(err)
	}
	info.Delivery = &AssetDeliveryReceipt{Version: 1, DeliveryID: stored.ID, Slot: 0, ItemSHA256: mediaDeliveryItemDigest(item)}
	claim.Delivery.Items[0].Receipt = &info
	claim.Task.Version++
	setBacklog(true)
	if err = a.persistTaskClaim(claim, false); !isEventBacklog(err) {
		t.Fatal("receipt commit ignored bounded outbox", err)
	}
	var receipts int
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_media_delivery_items WHERE task_id=$1 AND receipt IS NOT NULL", task.ID).Scan(&receipts); err != nil || receipts != 0 || claim.Delivery.Version != stored.Version {
		t.Fatal("rolled back receipt escaped", err)
	}
	setBacklog(false)
	// A replacement owner invalidates the old record before receipt publication.
	expireTaskLease(t, admin, task.ID)
	successor := claimTask(t, a, admin, task.ID)
	if err = a.persistTaskClaim(claim, false); !isLeaseLost(err) {
		t.Fatal("old lease registered receipt", err)
	}
	if a.store.failed.Load() {
		t.Fatal("lease conflict poisoned database readiness")
	}
	successor.Delivery.Items[0].Receipt = &info
	successor.Task.Version++
	if err = a.persistTaskClaim(successor, false); err != nil {
		t.Fatal(err)
	}
	// Immutable SQL evidence remains immutable even outside the Go helper.
	for _, query := range []string{"UPDATE studio_media_deliveries SET manifest_digest=repeat('0',64) WHERE task_id=$1", "DELETE FROM studio_media_deliveries WHERE task_id=$1", "UPDATE studio_media_delivery_items SET receipt=receipt WHERE task_id=$1", "DELETE FROM studio_media_delivery_items WHERE task_id=$1"} {
		tx, e := admin.Begin(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(context.Background(), query, task.ID)
		_ = tx.Rollback(context.Background())
		if e == nil {
			t.Fatal("immutable delivery evidence was changed")
		}
	}
	// A legacy/private-nil save must preserve both ciphertext and receipt.
	nilPrivate := cloneLeasedRecord(successor)
	nilPrivate.Delivery = nil
	nilPrivate.Task.Version++
	if err = a.persistTaskClaim(nilPrivate, false); err != nil {
		t.Fatal(err)
	}
	if nilPrivate.Delivery == nil || !sameMediaDeliveryReceipt(nilPrivate.Delivery.Items[0].Receipt, &info) || !bytes.Equal(nilPrivate.Delivery.Ciphertext, stored.Ciphertext) {
		t.Fatal("nil private save lost frozen evidence")
	}
	if _, err = a.cancelTask(task.ID); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	parked := cloneLeasedRecord(a.tasks[task.ID])
	a.mu.Unlock()
	if parked.Delivery.State != "PARKED" || parked.Task.Status != "CANCELLED" || a.canReconcileTask(parked) {
		t.Fatal("cancel did not retain parked private handoff")
	}
	late := cloneLeasedRecord(nilPrivate)
	late.Task.Status = "SUCCEEDED"
	late.Task.Artifacts = []Artifact{mediaDeliveryArtifact(item, info)}
	late.Task.Version++
	if err = a.persistTaskClaim(late, true); !isLeaseLost(err) {
		t.Fatal("late final release crossed cancellation fence", err)
	}
	var assets int
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID).Scan(&assets); err != nil || assets != 0 {
		t.Fatal("unreleased receipt entered public catalog", err)
	}
}
