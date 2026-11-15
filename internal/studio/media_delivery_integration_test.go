package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

// This is a real loopback gateway and private download server. The App uses
// the production asynchronous adapter, its HTTP pools and its storage reader.
type deliveryHTTPFixture struct {
	server *httptest.Server
	mu     sync.Mutex
	count  int
	tasks  map[string]struct {
		kind  string
		count int
	}
	queries      map[string]int
	downloads    map[string]int
	first        string
	blockSlot    int
	reverse      bool
	partial      bool
	image, video []byte
	blockedImage []byte
	entered      chan string
	release      chan struct{}
	once         sync.Once
}

func newDeliveryHTTPFixture(t *testing.T, blockSlot int) *deliveryHTTPFixture {
	t.Helper()
	img, _ := mediaBytesFixture(t, "allow.png")
	vid, _ := mediaBytesFixture(t, "allow.mp4")
	f := &deliveryHTTPFixture{tasks: make(map[string]struct {
		kind  string
		count int
	}), queries: map[string]int{}, downloads: map[string]int{}, blockSlot: blockSlot, image: img, video: vid, entered: make(chan string, 16), release: make(chan struct{})}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
				t.Error("gateway submit credential missing")
				w.WriteHeader(401)
				return
			}
			var body struct {
				N int `json:"n"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(400)
				return
			}
			kind := "image"
			if strings.Contains(r.URL.Path, "video") {
				kind = "video"
			}
			f.mu.Lock()
			f.count++
			id := fmt.Sprintf("asyncimg_delivery_%d", f.count)
			if f.first == "" {
				f.first = id
			}
			f.tasks[id] = struct {
				kind  string
				count int
			}{kind, max(1, body.N)}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": id})
			return
		}
		if strings.HasPrefix(r.URL.Path, mediaQueryPrefix) {
			if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
				t.Error("gateway query credential missing")
				w.WriteHeader(401)
				return
			}
			id := strings.TrimPrefix(r.URL.Path, mediaQueryPrefix)
			f.mu.Lock()
			task, ok := f.tasks[id]
			f.queries[id]++
			reverse, partial := f.reverse, f.partial
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(404)
				return
			}
			count := task.count
			if partial && count > 1 {
				count--
			}
			items := make([]map[string]any, 0, count)
			for i := 0; i < count; i++ {
				slot := i
				if reverse {
					slot = count - i - 1
				}
				items = append(items, map[string]any{"url": fmt.Sprintf("/private-download/%s/%d?signature=delivery-private-url", id, slot)})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": id, "media_type": task.kind, "stage": "completed", "status": "succeeded", "data": items})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/private-download/") {
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("private download received gateway/user credential")
				w.WriteHeader(403)
				return
			}
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/private-download/"), "/")
			if len(parts) != 2 {
				w.WriteHeader(404)
				return
			}
			slot, e := strconv.Atoi(parts[1])
			if e != nil {
				w.WriteHeader(404)
				return
			}
			f.mu.Lock()
			task, ok := f.tasks[parts[0]]
			f.downloads[parts[0]+"/"+parts[1]]++
			blocked := parts[0] == f.first && slot == f.blockSlot
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(404)
				return
			}
			if blocked {
				select {
				case f.entered <- parts[0] + "/" + parts[1]:
				default:
				}
				select {
				case <-f.release:
				case <-r.Context().Done():
					return
				}
			}
			f.mu.Lock()
			blockedBytes := f.blockedImage
			f.mu.Unlock()
			if slot == 1 && len(blockedBytes) > 0 {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(blockedBytes)
			} else if task.kind == "video" {
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = w.Write(f.video)
			} else {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(f.image)
			}
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(func() { f.unblock(); f.server.Close() })
	return f
}
func (f *deliveryHTTPFixture) unblock() { f.once.Do(func() { close(f.release) }) }
func (f *deliveryHTTPFixture) provider(t *testing.T, dir string, storage ...AssetStorageConfig) *AsyncMediaProvider {
	t.Helper()
	var target AssetStorageConfig
	if len(storage) > 0 {
		target = storage[0]
	}
	p, e := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: f.server.URL, APIKey: fixtureMediaKey, CredentialID: "delivery-integration", ImageModel: "fixture-image", VideoModel: "fixture-video", VideoSeconds: 8, AssetDir: dir, AssetStorage: target, allowLocalAssets: true, SubmitConcurrency: 1, QueryConcurrency: 1, DeliveryConcurrency: 1})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func deliveryTestConfig(t *testing.T, f *deliveryHTTPFixture, persistent bool) Config {
	t.Helper()
	database := ""
	if persistent {
		database = testDatabase(t)
	}
	dir := t.TempDir()
	c := Config{AssetDir: dir, ReferenceDir: t.TempDir(), PollInterval: time.Hour, MetricsToken: "delivery-integration-metric-only", mediaProvider: f.provider(t, dir), MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 1, DeliveryWorkers: 1}}
	if persistent {
		c.DatabaseURL = database
		c.AuthMode = "required"
		c.MediaDelivery = MediaDeliveryConfig{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32))}
	}
	return c
}
func deliveryIdentity(t *testing.T, s *httptest.Server, persistent bool) (*http.Client, testIdentity) {
	t.Helper()
	if persistent {
		c := authClient(t)
		return c, registerFixture(t, c, s.URL, "delivery-integration@example.test")
	}
	return s.Client(), testIdentity{Project: Project{ID: "prj_demo", WorkspaceID: "ws_demo"}}
}
func deliveryStep(t *testing.T, a *App, id string) {
	t.Helper()
	advanceMediaScheduleDue(t, a, id)
	done := make(chan struct{})
	go func() { defer close(done); a.executeTaskStep(id) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery step failed to return")
	}
}
func deliveryWait(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("private download not entered")
	}
}
func deliveryRead(t *testing.T, a *App, id string) Task {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	r, e := a.getTaskLocked(id)
	if e != nil {
		t.Fatal(e)
	}
	return copyTask(r.Task)
}
func deliveryAdmin(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, e := pgx.Connect(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func TestMediaDeliveryMemorySeparatesDownloadFromQueries(t *testing.T) {
	f := newDeliveryHTTPFixture(t, 0)
	a, s := testApp(t, deliveryTestConfig(t, f, false))
	client, id := deliveryIdentity(t, s, false)
	first, _, _ := laneAccept(t, a, s, client, id, "first private download")
	deliveryStep(t, a, first.ID)
	deliveryStep(t, a, first.ID)
	f.mu.Lock()
	downloads := len(f.downloads)
	f.mu.Unlock()
	if downloads != 0 {
		t.Fatal("query downloaded before separate delivery execution")
	}
	done := make(chan struct{})
	go func() { defer close(done); a.executeTaskStep(first.ID) }()
	deliveryWait(t, f.entered)
	if snapshot := a.mediaExecution.snapshot(); snapshot.DeliveryInFlight != 1 || snapshot.QueryInFlight != 0 {
		t.Fatal("slow download held query capacity", snapshot)
	}
	metricRequest, _ := http.NewRequest("GET", s.URL+"/metrics", nil)
	metricRequest.Header.Set("Authorization", "Bearer delivery-integration-metric-only")
	metricResponse, err := client.Do(metricRequest)
	if err != nil {
		t.Fatal(err)
	}
	metricBody, err := io.ReadAll(metricResponse.Body)
	_ = metricResponse.Body.Close()
	if err != nil || metricResponse.StatusCode != 200 || !bytes.Contains(metricBody, []byte("studio_media_lane_reserved{lane=\"delivery\"} 1")) || !bytes.Contains(metricBody, []byte("studio_media_lane_reserved{lane=\"query\"} 0")) || bytes.Contains(metricBody, []byte(first.ID)) || bytes.Contains(metricBody, []byte("delivery-private-url")) {
		t.Fatal("delivery metrics are inaccurate or expose private identity")
	}
	second, _, _ := laneAccept(t, a, s, client, id, "second query during blocked delivery")
	deliveryStep(t, a, second.ID)
	deliveryStep(t, a, second.ID)
	f.mu.Lock()
	queries := 0
	for _, n := range f.queries {
		queries += n
	}
	f.mu.Unlock()
	if queries != 2 {
		t.Fatal("query could not make progress during slow download")
	}
	code, raw, _ := authRequest(t, client, s.URL, "GET", "/api/v1/tasks/"+first.ID, "", id.Project.ID, nil)
	if code != 200 || bytes.Contains(raw, []byte("delivery-private-url")) || bytes.Contains(raw, []byte("ciphertext")) || len(decode[Task](t, raw).Artifacts) != 0 {
		t.Fatal("private delivery escaped task read")
	}
	f.unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not finish")
	}
	deliveryStep(t, a, second.ID)
	if deliveryRead(t, a, first.ID).Status != "SUCCEEDED" || deliveryRead(t, a, second.ID).Status != "SUCCEEDED" {
		t.Fatal("phased memory pipeline did not finish")
	}
}

func TestPostgresMediaDeliveryRestartUsesFrozenManifestAndReceipts(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(fmt.Sprintf("tamper=%t", tamper), func(t *testing.T) {
			f := newDeliveryHTTPFixture(t, 1)
			cfg := deliveryTestConfig(t, f, true)
			a, s := testApp(t, cfg)
			client, id := deliveryIdentity(t, s, true)
			admin := deliveryAdmin(t, cfg.DatabaseURL)
			key := newID("delivery_key")
			task, input := acceptMediaExecution(t, a, s, client, id, "image", "two original private items", key, 2)
			deliveryStep(t, a, task.ID)
			deliveryStep(t, a, task.ID)
			var ciphertext []byte
			var count int
			if e := admin.QueryRow(context.Background(), "SELECT ciphertext,item_count FROM studio_media_deliveries WHERE task_id=$1", task.ID).Scan(&ciphertext, &count); e != nil {
				t.Fatal(e)
			}
			if count != 2 || len(ciphertext) < 32 || bytes.Contains(ciphertext, []byte("delivery-private-url")) || bytes.Contains(ciphertext, []byte(f.server.URL)) {
				t.Fatal("delivery manifest is not encrypted")
			}
			done := make(chan struct{})
			go func() { defer close(done); a.executeTaskStep(task.ID) }()
			deliveryWait(t, f.entered)
			if got := outboxCount(t, admin, "SELECT count(*) FROM studio_media_delivery_items WHERE task_id=$1 AND receipt IS NOT NULL", task.ID); got != 1 {
				t.Fatal("first item was not checkpointed before second download", got)
			}
			var firstAsset string
			if e := admin.QueryRow(context.Background(), "SELECT asset_id FROM studio_media_delivery_items WHERE task_id=$1 AND position=0", task.ID).Scan(&firstAsset); e != nil {
				t.Fatal(e)
			}
			a.Close()
			s.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not drain delivery")
			}
			if tamper {
				name := filepath.Join(cfg.AssetDir, firstAsset+".bin")
				raw, e := os.ReadFile(name)
				if e != nil {
					t.Fatal(e)
				}
				raw[len(raw)-1] ^= 0xff
				if e = os.WriteFile(name, raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
			f.mu.Lock()
			f.reverse = true
			f.mu.Unlock()
			f.unblock()
			cfg.mediaProvider = f.provider(t, cfg.AssetDir)
			reopened, restarted := testApp(t, cfg)
			deliveryStep(t, reopened, task.ID)
			final := deliveryRead(t, reopened, task.ID)
			if tamper {
				if len(final.Artifacts) != 0 || final.CanReconcile || final.Error == nil {
					t.Fatal("tampered receipt was silently replaced or reopened")
				}
			} else if final.Status != "SUCCEEDED" || len(final.Artifacts) != 2 {
				t.Fatal("frozen delivery did not recover", final.Status)
			}
			f.mu.Lock()
			submits := f.count
			queries, firstDownloads := f.queries[f.first], f.downloads[f.first+"/0"]
			f.mu.Unlock()
			if submits != 1 || queries != 1 || firstDownloads != 1 {
				t.Fatal("restart repeated generation/query or replaced completed first item", submits, queries, firstDownloads)
			}
			code, body, headers := authRequest(t, client, restarted.URL, "POST", "/api/v1/tasks", key, id.Project.ID, input)
			if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, body).ID != task.ID {
				t.Fatal("original idempotency changed on delivery recovery")
			}
			if !tamper {
				for _, art := range final.Artifacts {
					code, raw, _ := authRequest(t, client, restarted.URL, "GET", art.URL, "", id.Project.ID, nil)
					if code != 200 || !bytes.Equal(raw, f.image) {
						t.Fatal("recovered private bytes unavailable")
					}
				}
			}
		})
	}
}

// A completed import is still private and metered as held until the distinct
// first-review step commits an approved whole-group result.
func deliveryAwaitingFirstReview(t *testing.T, a *App, id string) {
	t.Helper()
	current := deliveryRead(t, a, id)
	a.mu.Lock()
	dispatch := cloneMediaReviewDispatch(a.tasks[id].ReviewDispatch)
	a.mu.Unlock()
	if current.Status != "MODERATING" || current.Moderation == nil || current.Moderation.State != "PENDING" || len(current.Artifacts) != 0 || dispatch == nil || dispatch.State != "READY" || dispatch.AttemptID != "" {
		t.Fatal("completed import did not durably hand off an unstarted private review")
	}
	if current.Billing != nil && current.Billing.State != "HELD" {
		t.Fatal("unreviewed imported group settled billing")
	}
}

func TestPostgresMediaDeliverySlowReviewDoesNotOccupyQueryAndCannotResurrect(t *testing.T) {
	f := newDeliveryHTTPFixture(t, -1)
	cfg := deliveryTestConfig(t, f, true)
	cfg.Moderation = executionModerationConfig(t, nil)
	cfg.MediaModeration = MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}
	a, s := testApp(t, cfg)
	client, id := deliveryIdentity(t, s, true)
	entered, release := gatedMediaChecker(t, a)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	first, _, _ := laneAccept(t, a, s, client, id, "private review")
	deliveryStep(t, a, first.ID)
	deliveryStep(t, a, first.ID)
	deliveryStep(t, a, first.ID)
	deliveryAwaitingFirstReview(t, a, first.ID)
	done := make(chan struct{})
	go func() { defer close(done); a.executeTaskStep(first.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review not entered")
	}
	second, _, _ := laneAccept(t, a, s, client, id, "query while reviewing")
	deliveryStep(t, a, second.ID)
	deliveryStep(t, a, second.ID)
	deliveryStep(t, a, second.ID)
	deliveryAwaitingFirstReview(t, a, second.ID)
	if v := a.mediaExecution.snapshot(); v.QueryInFlight != 0 || v.DeliveryInFlight != 0 || v.ReviewInFlight != 1 {
		t.Fatal("review occupied wrong execution budget", v)
	}
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	private, e := loadTaskMediaModerationSQL(context.Background(), admin, first.ID)
	if e != nil || len(private.Candidates) != 1 {
		t.Fatal("private review candidate not saved")
	}
	laneCancel(t, s, client, id, first.ID)
	laneCancel(t, s, client, id, second.ID)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not interrupt the local review request")
	}
	if a.mediaExecution.snapshot().ReviewInFlight != 0 {
		t.Fatal("returned cancelled review retained its execution budget")
	}
	// The upstream ignores cancellation and returns ALLOW later; its late
	// response must not revive a task after the local request has exited.
	unblock()
	final := deliveryRead(t, a, first.ID)
	if final.Status != "CANCELLED" || len(final.Artifacts) != 0 || final.Moderation.State != "REVIEW" {
		t.Fatal("late approval resurrected cancelled delivery")
	}
	code, _, _ := authRequest(t, client, s.URL, "GET", private.Candidates[0].URL, "", id.Project.ID, nil)
	if code != 404 {
		t.Fatal("cancelled private candidate became downloadable")
	}
	if len(entered) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", first.ID) != 0 {
		t.Fatal("cancelled delivery was reviewed again or registered")
	}
}

func TestPostgresMediaDeliveryWholeBatchAndBilling(t *testing.T) {
	for _, tc := range []struct {
		name, kind, status string
		count              int
		partial, blocked   bool
		delivered          int
	}{
		{"images", "image", "SUCCEEDED", 2, false, false, 2},
		{"partial", "image", "PARTIAL", 2, true, false, 1},
		{"video", "video", "SUCCEEDED", 1, false, false, 1},
		{"blocked-group", "image", "FAILED", 2, false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeliveryHTTPFixture(t, -1)
			f.partial = tc.partial
			if tc.blocked {
				f.blockedImage, _ = mediaBytesFixture(t, "block.png")
			}
			cfg := deliveryTestConfig(t, f, true)
			cfg.Moderation = executionModerationConfig(t, nil)
			cfg.MediaModeration = MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}
			a, s := testApp(t, cfg)
			client, id := deliveryIdentity(t, s, true)
			admin := deliveryAdmin(t, cfg.DatabaseURL)
			a.pricing = &pricingCatalog{Version: "delivery-integration-v1", ProviderMode: "demo", Rates: map[string]LedgerPriceSnapshot{}}
			for _, m := range a.catalog.Public.Models {
				if m.Kind == "image" || m.Kind == "video" {
					for _, preset := range m.Capabilities.ParameterPresets {
						a.pricing.Rates[pricingKey(m.ID, m.Kind, "generate", preset.ID)] = LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 100}
					}
				}
			}
			if e := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
				_, e := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: id.Workspace.ID, AmountMicros: 100000, OperationKey: newID("credit"), Reason: "fixture", EvidenceRef: "fixture:delivery"})
				return e
			}); e != nil {
				t.Fatal(e)
			}
			task, _ := acceptMediaExecution(t, a, s, client, id, tc.kind, "private delivery billing", newID("key"), tc.count)
			deliveryStep(t, a, task.ID)
			deliveryStep(t, a, task.ID)
			before := deliveryRead(t, a, task.ID)
			if len(before.Artifacts) != 0 || before.Billing == nil || before.Billing.State != "HELD" {
				t.Fatal("manifest alone delivered or settled content")
			}
			deliveryStep(t, a, task.ID)
			deliveryAwaitingFirstReview(t, a, task.ID)
			deliveryStep(t, a, task.ID)
			final := deliveryRead(t, a, task.ID)
			if final.Status != tc.status || len(final.Artifacts) != tc.delivered || final.Billing == nil {
				t.Fatal("wrong whole-group delivery", final.Status)
			}
			if tc.blocked {
				if final.Moderation.State != "BLOCKED" || final.Billing.State != "REVIEW" || final.Billing.ChargedMicros != 0 {
					t.Fatal("blocked execution was charged or exposed")
				}
			} else if final.Moderation.State != "ALLOWED" || final.Billing.State != "SETTLED" || final.Billing.ChargedMicros != int64(tc.delivered)*100 || final.Billing.ReleasedMicros != int64(tc.count-tc.delivered)*100 {
				t.Fatal("delivery count and ledger settlement differ")
			}
			if n := outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID); n != tc.delivered {
				t.Fatal("asset registration differs from approved delivery", n)
			}
			private, e := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
			if e != nil || private == nil {
				t.Fatal("missing frozen review evidence")
			}
			for _, art := range private.Candidates {
				code, raw, _ := authRequest(t, client, s.URL, "GET", art.URL, "", id.Project.ID, nil)
				if tc.blocked {
					if code != 404 {
						t.Fatal("blocked group had downloadable member")
					}
				} else if code != 200 || len(raw) == 0 {
					t.Fatal("approved original unavailable")
				}
			}
			var state string
			if e = admin.QueryRow(context.Background(), "SELECT state FROM studio_media_deliveries WHERE task_id=$1", task.ID).Scan(&state); e != nil {
				t.Fatal(e)
			}
			if tc.blocked && state != "PARKED" || !tc.blocked && state != "COMPLETE" {
				t.Fatal("private phase not committed with final decision", state)
			}
		})
	}
}

func TestPostgresMediaDeliveryRealS3AndProviderIndependentDownloads(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_* absent; actual loopback S3 service not tested")
	}
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
		t.Fatal("S3 acceptance requires an isolated loopback service")
	}
	f := newDeliveryHTTPFixture(t, -1)
	cfg := deliveryTestConfig(t, f, true)
	_ = cfg.mediaProvider.(*AsyncMediaProvider).Close()
	cfg.AssetStorage = AssetStorageConfig{Backend: "s3", Endpoint: endpoint, Region: "us-east-1", Bucket: os.Getenv("TEST_S3_BUCKET"), Prefix: "delivery/" + newID("run"), AccessKeyID: os.Getenv("TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"), ForcePathStyle: true}
	provider := f.provider(t, cfg.AssetDir, cfg.AssetStorage)
	cfg.mediaProvider = provider
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, e := provider.assets.destination.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(cfg.AssetStorage.Bucket)})
	if e != nil {
		if _, e = provider.assets.destination.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(cfg.AssetStorage.Bucket)}); e != nil {
			t.Fatal("isolated real S3 bucket unavailable")
		}
	}
	cfg.Moderation = executionModerationConfig(t, nil)
	cfg.MediaModeration = MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}
	a, s := testApp(t, cfg)
	client, id := deliveryIdentity(t, s, true)
	var delivered []Artifact
	for _, kind := range []string{"image", "video"} {
		task, _ := acceptMediaExecution(t, a, s, client, id, kind, "real local object delivery", newID("key"), 1)
		deliveryStep(t, a, task.ID)
		deliveryStep(t, a, task.ID)
		deliveryStep(t, a, task.ID)
		deliveryAwaitingFirstReview(t, a, task.ID)
		deliveryStep(t, a, task.ID)
		final := deliveryRead(t, a, task.ID)
		if final.Status != "SUCCEEDED" || len(final.Artifacts) != 1 || final.Moderation.State != "ALLOWED" {
			t.Fatal("real S3 phased delivery failed", kind, final.Status)
		}
		info, e := a.assets.Stat(context.Background(), final.Artifacts[0].ID)
		if e != nil || info.StorageBackend != "s3" || info.Delivery == nil {
			t.Fatal("real S3 complete receipt absent")
		}
		delivered = append(delivered, final.Artifacts[0])
	}
	a.Close()
	s.Close()
	f.server.Close()
	// Only completed deliveries remain. Provider shutdown cannot remove private
	// downloads, but the original encryption key remains mandatory at startup.
	cfg.mediaProvider = nil
	_, restarted := testApp(t, cfg)
	for _, art := range delivered {
		code, body, _ := authRequest(t, client, restarted.URL, "GET", art.URL, "", id.Project.ID, nil)
		want := f.image
		if art.Kind == "video" {
			want = f.video
		}
		if code != 200 || !bytes.Equal(body, want) {
			t.Fatal("provider-independent S3 download failed", art.Kind, code)
		}
		if _, e := os.Stat(filepath.Join(cfg.AssetDir, art.ID+".bin")); e != nil {
			t.Fatal("S3 delivery removed required local backup copy")
		}
	}
	f.mu.Lock()
	submits := f.count
	queries := 0
	for _, n := range f.queries {
		queries += n
	}
	f.mu.Unlock()
	if submits != 2 || queries != 2 {
		t.Fatal("completed storage restart repeated gateway work")
	}
	t.Log("real loopback S3-compatible service: image/video, full-byte review, durable receipts and provider-disabled downloads; not production cloud/DR acceptance")
}
