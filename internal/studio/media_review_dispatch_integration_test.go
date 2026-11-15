package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Exercise the actual media-v1 HTTP body, not a replacement engine callback.
// The server can deliberately return ALLOW after the caller was cancelled.
type reviewDispatchHTTPFixture struct {
	server       *httptest.Server
	mu           sync.Mutex
	phase        string
	release      chan struct{}
	once         sync.Once
	entered      chan string
	calls        map[string]int
	active, peak int
	blockSHA     string
	requestIDs   []string
}

func newReviewDispatchHTTPFixture(t *testing.T, phase string) *reviewDispatchHTTPFixture {
	t.Helper()
	f := &reviewDispatchHTTPFixture{phase: phase, release: make(chan struct{}), entered: make(chan string, 16), calls: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, body := readMediaMultipartFixture(t, r)
		if r.Header.Get("Authorization") != "Bearer review-dispatch-fixture-secret" || meta.SHA256 != moderationSHA(body) || meta.ByteSize != int64(len(body)) || len(body) == 0 {
			t.Error("review HTTP identity or complete media bytes mismatch")
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.calls[meta.Phase]++
		f.requestIDs = append(f.requestIDs, meta.RequestID)
		f.active++
		f.peak = max(f.peak, f.active)
		blocked, blockSHA := meta.Phase == f.phase, f.blockSHA
		f.mu.Unlock()
		defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
		if blocked {
			f.entered <- meta.Phase
			<-f.release
		}
		decision := "allow"
		if meta.Phase == "output" && meta.SHA256 == blockSHA {
			decision = "block"
		}
		writeMediaResponse(w, meta, decision)
	}))
	t.Cleanup(func() { f.unblock(); f.server.Close() })
	return f
}

func (f *reviewDispatchHTTPFixture) unblock() { f.once.Do(func() { close(f.release) }) }
func (f *reviewDispatchHTTPFixture) count(phase string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[phase]
}
func (f *reviewDispatchHTTPFixture) config(t *testing.T) MediaModerationConfig {
	t.Helper()
	p := mediaModerationPolicyFile{SchemaVersion: 1, PolicyID: "review-dispatch-http", PolicyVersion: "v1", MaxImageBytes: mediaModerationImageLimit, MaxVideoBytes: mediaModerationVideoLimit, Fixtures: []MediaModerationFixture{}}
	raw, _ := json.Marshal(p)
	return MediaModerationConfig{Mode: "http", PolicyFile: writeMediaPolicyFixture(t, raw), Endpoint: f.server.URL, APIKey: "review-dispatch-fixture-secret", CredentialID: "review-dispatch-fixture-v1"}
}

func reviewDispatchConfig(t *testing.T, media *deliveryHTTPFixture, review *reviewDispatchHTTPFixture, persistent bool, workers int) Config {
	t.Helper()
	c := deliveryTestConfig(t, media, persistent)
	c.Moderation = executionModerationConfig(t, nil)
	c.MediaModeration = review.config(t)
	c.MediaScheduling.ReviewWorkers = workers
	return c
}

func reviewDispatchStep(t *testing.T, a *App, id string) {
	t.Helper()
	advanceMediaScheduleDue(t, a, id)
	done := make(chan bool, 1)
	go func() {
		if a.store != nil {
			done <- a.executePersistentTaskLane(mediaReviewLane, id)
		} else {
			done <- a.executeMemoryTaskLane(mediaReviewLane, id)
		}
	}()
	select {
	case worked := <-done:
		if !worked {
			t.Fatal("ready review was not executed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("review step did not return")
	}
}

func reviewDispatchDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("review worker did not exit")
	}
}

func reviewDispatchState(t *testing.T, conn *pgx.Conn, id, want string) (int64, string) {
	t.Helper()
	var state, attempt string
	var version int64
	if err := conn.QueryRow(context.Background(), "SELECT state,version,attempt_id FROM studio_media_review_dispatch WHERE task_id=$1", id).Scan(&state, &version, &attempt); err != nil || state != want {
		t.Fatalf("review dispatch state: got %s want %s: %v", state, want, err)
	}
	return version, attempt
}

func prepareReviewDispatch(t *testing.T, a *App, id string) {
	t.Helper()
	for i := 0; i < 3; i++ { // native POST, status-only GET, private import
		deliveryStep(t, a, id)
	}
	private := deliveryRead(t, a, id)
	if private.Status != "MODERATING" || private.Moderation == nil || private.Moderation.State != "PENDING" || len(private.Artifacts) != 0 {
		t.Fatal("delivery did not stop at a private first-review handoff", private)
	}
}

func TestPostgresMediaReviewDispatchSlowHTTPDoesNotHoldDelivery(t *testing.T) {
	verifyReviewDispatchIsolation(t, true)
}
func verifyReviewDispatchIsolation(t *testing.T, persistent bool) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "output")
	cfg := reviewDispatchConfig(t, media, review, persistent, 1)
	a, s := testApp(t, cfg)
	client, identity := deliveryIdentity(t, s, persistent)
	first, _, _ := laneAccept(t, a, s, client, identity, "private-review-isolation-prompt")
	prepareReviewDispatch(t, a, first.ID)
	if review.count("output") != 0 {
		t.Fatal("delivery performed review before handoff")
	}
	done := make(chan struct{})
	go func() { defer close(done); a.executeTaskStep(first.ID) }()
	deliveryWait(t, review.entered)
	if got := a.mediaExecution.snapshot(); got.ReviewInFlight != 1 || got.DeliveryInFlight != 0 || got.QueryInFlight != 0 || got.ReviewLimit != 1 {
		t.Fatal("slow review held a delivery/query slot", got)
	}
	request, _ := http.NewRequest("GET", s.URL+"/metrics", nil)
	request.Header.Set("Authorization", "Bearer delivery-integration-metric-only")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatal("metrics unavailable", err)
	}
	for _, expected := range []string{`studio_media_lane_reserved{lane="review"} 1`, `studio_media_lane_limit{lane="review"} 1`, `studio_media_lane_reserved{lane="delivery"} 0`, `studio_media_lane_reserved{lane="query"} 0`} {
		if !strings.Contains(string(metrics), expected) {
			t.Fatal("review metrics missing", expected)
		}
	}
	review.mu.Lock()
	privateIDs := append([]string{}, review.requestIDs...)
	review.mu.Unlock()
	for _, private := range append(privateIDs, first.ID, "private-review-isolation-prompt", "delivery-private-url", review.server.URL, "review-dispatch-fixture-secret") {
		if strings.Contains(string(metrics), private) {
			t.Fatal("metrics exposed private review identity")
		}
	}
	second, _, _ := laneAccept(t, a, s, client, identity, "independent submit query and delivery")
	prepareReviewDispatch(t, a, second.ID)
	if review.count("output") != 1 {
		t.Fatal("busy review lane executed another request")
	}
	if persistent {
		admin := deliveryAdmin(t, cfg.DatabaseURL)
		reviewDispatchState(t, admin, first.ID, "STARTED")
		_, attempt := reviewDispatchState(t, admin, second.ID, "READY")
		if attempt != "" {
			t.Fatal("waiting review already has an attempt")
		}
	}
	media.mu.Lock()
	submits, queries, downloads := media.count, 0, 0
	for _, n := range media.queries {
		queries += n
	}
	for _, n := range media.downloads {
		downloads += n
	}
	media.mu.Unlock()
	if submits != 2 || queries != 2 || downloads != 2 {
		t.Fatal("another media stage could not progress", submits, queries, downloads)
	}
	review.unblock()
	reviewDispatchDone(t, done)
	reviewDispatchStep(t, a, second.ID)
	for _, id := range []string{first.ID, second.ID} {
		final := deliveryRead(t, a, id)
		if final.Status != "SUCCEEDED" || final.Moderation.State != "ALLOWED" || len(final.Artifacts) != 1 {
			t.Fatal("approved review was not delivered", final)
		}
	}
}

func TestPostgresMediaReviewDispatchInputPermitsKeepOutputReady(t *testing.T) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "input")
	defer review.unblock()
	cfg := reviewDispatchConfig(t, media, review, true, 2)
	a, s := testApp(t, cfg)
	client, identity := deliveryIdentity(t, s, true)
	// This test intentionally holds both real HTTP input requests while an
	// output worker joins the bounded arbiter. The normal five-second auth
	// fixture deadline must not cancel an input and silently free a permit.
	uploadClient := *client
	uploadClient.Timeout = 15 * time.Second
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	task, _, _ := laneAccept(t, a, s, client, identity, "output waits for shared input permits")
	prepareReviewDispatch(t, a, task.ID)
	version, _ := reviewDispatchState(t, admin, task.ID, "READY")
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		blob := referencePNG(t, byte(70+i))
		go func(i int) {
			code, _, _ := uploadReferenceFixture(t, &uploadClient, s.URL, identity.Project.ID, fmt.Sprintf("review-permit-upload-%d", i), "private.png", blob, false)
			results <- code
		}(i)
	}
	deliveryWait(t, review.entered)
	deliveryWait(t, review.entered)
	advanceMediaScheduleDue(t, a, task.ID)
	outputDone := schedulerStepAsync(a, mediaReviewLane, task.ID)
	deadline := time.Now().Add(2 * time.Second)
	for a.mediaModeration.permitSnapshot().OutputWaiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := a.mediaModeration.permitSnapshot(); snapshot.OutputWaiting != 1 || snapshot.InFlight != 2 {
		t.Fatal("output did not wait behind both real input checks", snapshot)
	}
	select {
	case <-outputDone:
		t.Fatal("output worker exited or executed while both input checks held permits")
	default:
	}
	afterVersion, attempt := reviewDispatchState(t, admin, task.ID, "READY")
	if version != afterVersion || attempt != "" || review.count("output") != 0 || a.mediaExecution.snapshot().ReviewInFlight != 1 {
		t.Fatal("waiting output changed READY evidence or its bounded worker reservation")
	}
	review.unblock()
	for i := 0; i < 2; i++ {
		select {
		case code := <-results:
			if code != 201 {
				t.Fatal("input upload failed", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("input upload did not finish")
		}
	}
	// Releasing an input grants the already waiting worker its first permit;
	// do not dispatch a second worker to manufacture the successful result.
	schedulerStepDone(t, outputDone, true)
	reviewDispatchState(t, admin, task.ID, "FINISHED")
	review.mu.Lock()
	peak := review.peak
	review.mu.Unlock()
	if peak != 2 || review.count("input") != 2 || review.count("output") != 1 || deliveryRead(t, a, task.ID).Moderation.State != "ALLOWED" {
		t.Fatal("shared input/output concurrency or first review changed", peak)
	}
	if snapshot := a.mediaModeration.permitSnapshot(); snapshot.OutputWaiting != 0 || snapshot.InFlight != 0 || a.mediaExecution.snapshot().ReviewInFlight != 0 {
		t.Fatal("completed inputs/output retained permits or worker budget", snapshot)
	}
}

func TestPostgresMediaReviewDispatchCompetingConnectionsCancelLateAllow(t *testing.T) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "output")
	cfg := reviewDispatchConfig(t, media, review, true, 2)
	a, s := testApp(t, cfg)
	client, identity := deliveryIdentity(t, s, true)
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	firstConn, ok := a.taskLeases.tryAcquireLane(mediaReviewLane)
	if !ok {
		t.Fatal("first independent review connection absent")
	}
	secondConn, ok := a.taskLeases.tryAcquireLane(mediaReviewLane)
	if !ok {
		a.taskLeases.release(firstConn)
		t.Fatal("second independent review connection absent")
	}
	if firstConn.PgConn().PID() == secondConn.PgConn().PID() {
		t.Fatal("review workers share one connection")
	}
	a.taskLeases.release(firstConn)
	a.taskLeases.release(secondConn)
	task, input, key := laneAccept(t, a, s, client, identity, "cancel before late review ALLOW")
	prepareReviewDispatch(t, a, task.ID)
	done := make(chan struct{})
	go func() { defer close(done); a.executePersistentTaskLane(mediaReviewLane, task.ID) }()
	deliveryWait(t, review.entered)
	_, attempt := reviewDispatchState(t, admin, task.ID, "STARTED")
	if attempt == "" || a.executePersistentTaskLane(mediaReviewLane, task.ID) || review.count("output") != 1 {
		t.Fatal("two connections started the same review")
	}
	laneCancel(t, s, client, identity, task.ID)
	review.unblock() // Real HTTP response is late; it cannot overwrite cancellation.
	reviewDispatchDone(t, done)
	final := deliveryRead(t, a, task.ID)
	if final.Status != "CANCELLED" || final.Moderation.State != "REVIEW" || len(final.Artifacts) != 0 {
		t.Fatal("late review published after cancellation", final)
	}
	_, finalAttempt := reviewDispatchState(t, admin, task.ID, "FINISHED")
	if finalAttempt != attempt || a.executePersistentTaskLane(mediaReviewLane, task.ID) || review.count("output") != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 0 {
		t.Fatal("cancel changed attempt, repeated review, or registered assets")
	}
	private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
	if err != nil || private == nil || len(private.Candidates) != 1 {
		t.Fatal("cancel lost private candidate", err)
	}
	code, _, _ := authRequest(t, client, s.URL, "GET", private.Candidates[0].URL, "", identity.Project.ID, nil)
	if code != 404 {
		t.Fatal("cancelled candidate became downloadable", code)
	}
	code, raw, headers := authRequest(t, client, s.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, input)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != task.ID || review.count("output") != 1 {
		t.Fatal("original request replay changed cancelled review")
	}
}

func TestPostgresMediaReviewDispatchWholeGroupBillingAndPrivateAccess(t *testing.T) {
	for _, tc := range []struct {
		name, kind, status string
		count, delivered   int
		partial, blocked   bool
	}{
		{"image-partial", "image", "PARTIAL", 2, 1, true, false},
		{"mixed-image-block", "image", "FAILED", 2, 0, false, true},
		{"video-allow", "video", "SUCCEEDED", 1, 1, false, false},
		{"video-block", "video", "FAILED", 1, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media := newDeliveryHTTPFixture(t, -1)
			media.partial = tc.partial
			review := newReviewDispatchHTTPFixture(t, "")
			if tc.blocked {
				if tc.kind == "image" {
					media.blockedImage, _ = mediaBytesFixture(t, "block.png")
					review.blockSHA = moderationSHA(media.blockedImage)
				} else {
					review.blockSHA = moderationSHA(media.video)
				}
			}
			cfg := reviewDispatchConfig(t, media, review, true, 2)
			a, s := testApp(t, cfg)
			client, identity := deliveryIdentity(t, s, true)
			admin := deliveryAdmin(t, cfg.DatabaseURL)
			a.pricing = &pricingCatalog{Version: "review-dispatch-credit-v1", ProviderMode: "demo", Rates: map[string]LedgerPriceSnapshot{}}
			for _, model := range a.catalog.Public.Models {
				if model.Kind == "image" || model.Kind == "video" {
					for _, preset := range model.Capabilities.ParameterPresets {
						a.pricing.Rates[pricingKey(model.ID, model.Kind, "generate", preset.ID)] = LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 100}
					}
				}
			}
			if err := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
				_, err := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: identity.Workspace.ID, AmountMicros: 100000, OperationKey: newID("credit"), Reason: "fixture", EvidenceRef: "fixture:review-dispatch"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			task, _ := acceptMediaExecution(t, a, s, client, identity, tc.kind, "whole private first-review group", newID("key"), tc.count)
			prepareReviewDispatch(t, a, task.ID)
			reviewDispatchState(t, admin, task.ID, "READY")
			before := deliveryRead(t, a, task.ID)
			if before.Billing == nil || before.Billing.State != "HELD" || review.count("output") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 0 {
				t.Fatal("private READY stage delivered or settled")
			}
			private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
			if err != nil || private == nil || len(private.Candidates) == 0 {
				t.Fatal("private candidates missing", err)
			}
			for _, artifact := range private.Candidates {
				code, _, _ := authRequest(t, client, s.URL, "GET", artifact.URL, "", identity.Project.ID, nil)
				if code != 404 {
					t.Fatal("READY candidate exposed", code)
				}
			}
			reviewDispatchStep(t, a, task.ID)
			version, attempt := reviewDispatchState(t, admin, task.ID, "FINISHED")
			final := deliveryRead(t, a, task.ID)
			if version != 3 || attempt == "" || final.Status != tc.status || len(final.Artifacts) != tc.delivered {
				t.Fatal("review changed original result or group", final)
			}
			if tc.blocked {
				if final.Moderation.State != "BLOCKED" || final.Billing.State != "REVIEW" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != 0 {
					t.Fatal("blocked group altered held credit", final.Billing)
				}
			} else if final.Moderation.State != "ALLOWED" || final.Billing.State != "SETTLED" || final.Billing.ChargedMicros != int64(tc.delivered)*100 || final.Billing.ReleasedMicros != int64(tc.count-tc.delivered)*100 {
				t.Fatal("approved group settlement differs from actual delivery", final.Billing)
			}
			if outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != tc.delivered {
				t.Fatal("catalog does not match whole-group approval")
			}
			for _, artifact := range private.Candidates {
				code, body, _ := authRequest(t, client, s.URL, "GET", artifact.URL, "", identity.Project.ID, nil)
				if tc.blocked && code != 404 || !tc.blocked && (code != 200 || moderationSHA(body) != artifact.StorageInfo.SHA256) {
					t.Fatal("candidate access does not match final decision", code)
				}
			}
			calls := review.count("output")
			if calls != len(private.Candidates) || a.executePersistentTaskLane(mediaReviewLane, task.ID) || review.count("output") != calls {
				t.Fatal("group was skipped or reviewed again", calls)
			}
		})
	}
}
