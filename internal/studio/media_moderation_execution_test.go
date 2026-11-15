package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func newMediaExecutionFixture(t *testing.T, billing bool) (*App, *httptest.Server, *http.Client, testIdentity, string) {
	t.Helper()
	database := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: database, AuthMode: "required", Moderation: executionModerationConfig(t, []ModerationRule{{Phase: "input", Contains: "DENY-MEDIA-PROMPT", Decision: "block"}}), MediaModeration: MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}, PollInterval: time.Hour, DemoTaskDuration: time.Millisecond, TaskWorkers: 2})
	c := authClient(t)
	identity := registerFixture(t, c, s.URL, "media-execution@example.test")
	if billing {
		a.pricing = &pricingCatalog{Version: "media-test-price-v1", ProviderMode: "demo", Rates: map[string]LedgerPriceSnapshot{}}
		for _, model := range a.catalog.Public.Models {
			if model.Kind == "image" || model.Kind == "video" {
				for _, preset := range model.Capabilities.ParameterPresets {
					for _, operation := range []string{"generate", "edit"} {
						a.pricing.Rates[pricingKey(model.ID, model.Kind, operation, preset.ID)] = LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 100}
					}
				}
			}
		}
		if err := ledgerTx(a.store, func(ctx context.Context, tx pgx.Tx) error {
			_, e := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: identity.Workspace.ID, AmountMicros: 100000, OperationKey: newID("grant"), Reason: "fixture", EvidenceRef: "fixture:media"})
			return e
		}); err != nil {
			t.Fatal(err)
		}
	}
	return a, s, c, identity, database
}

type countedModeratedMedia struct {
	MediaProvider
	submits, queries atomic.Int32
}

func (p *countedModeratedMedia) Submit(ctx context.Context, in TaskInput) (MediaHandle, error) {
	p.submits.Add(1)
	return p.MediaProvider.Submit(ctx, in)
}
func (p *countedModeratedMedia) Query(ctx context.Context, h MediaHandle) (MediaResult, error) {
	p.queries.Add(1)
	return p.MediaProvider.Query(ctx, h)
}

type mixedModeratedMedia struct {
	MediaProvider
	storage AssetStorage
	blocked []byte
}

// The provider delivers one valid item from a request for two. It does not
// manufacture a second result and rely on review to hide it.
type partialModeratedMedia struct{ MediaProvider }

func (p partialModeratedMedia) Query(ctx context.Context, h MediaHandle) (MediaResult, error) {
	requested := h.Count
	h.Count = 1
	result, err := p.MediaProvider.Query(ctx, h)
	if err == nil && result.Status == "SUCCEEDED" && requested > 1 {
		result.Status = "PARTIAL"
		result.Error = &APIError{Code: "PARTIAL_RESULT", Message: "fixture returned one deliverable item"}
	}
	return result, err
}

func (p *mixedModeratedMedia) Query(ctx context.Context, h MediaHandle) (MediaResult, error) {
	result, err := p.MediaProvider.Query(ctx, h)
	if err != nil || result.Status != "SUCCEEDED" || len(result.Artifacts) != 2 {
		return result, err
	}
	info, err := importMediaModerationFixture(ctx, p.storage, "mixed-"+h.ID, "image", p.blocked)
	if err != nil {
		return MediaResult{}, err
	}
	result.Artifacts[1] = Artifact{ID: info.ID, Kind: "image", URL: "/api/v1/assets/" + info.ID, Title: "second private fixture", StorageInfo: &info}
	return result, nil
}

func TestPostgresMediaModerationMixedBatchNeverPartiallyReleases(t *testing.T) {
	a, s, client, identity, database := newMediaExecutionFixture(t, true)
	blocked, _ := mediaBytesFixture(t, "block.png")
	a.media = &mixedModeratedMedia{MediaProvider: a.media, storage: a.assets, blocked: blocked}
	task, _ := acceptMediaExecution(t, a, s, client, identity, "image", "mixed ordered fixture", newID("key"), 2)
	final := finishMediaExecution(t, a, task.ID)
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(private.Candidates) != 2 || final.Status != "FAILED" || final.Moderation.State != "BLOCKED" || len(final.Artifacts) != 0 || final.Billing.State != "REVIEW" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 0 {
		t.Fatal("mixed group released its allowed prefix", final)
	}
	for _, item := range private.Candidates {
		code, _, _ := authRequest(t, client, s.URL, "GET", item.URL, "", identity.Project.ID, nil)
		if code != 404 {
			t.Fatal("mixed candidate download exposed", code)
		}
	}
}

func TestPostgresMediaModerationDecisionFailureRollsBackRelease(t *testing.T) {
	a, s, client, identity, database := newMediaExecutionFixture(t, true)
	entered, release := gatedMediaChecker(t, a)
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	task, _ := acceptMediaExecution(t, a, s, client, identity, "image", "safe decision failure fixture", newID("key"), 1)
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("submit missing")
	}
	time.Sleep(5 * time.Millisecond)
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("candidate READY handoff missing")
	}
	advanceMediaScheduleDue(t, a, task.ID)
	done := make(chan struct{})
	go func() { defer close(done); a.executePersistentTaskStep(task.ID) }()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("checker not reached")
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err = admin.Exec(context.Background(), `CREATE FUNCTION media_fail_allow() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='ALLOWED' THEN RAISE EXCEPTION 'fixture media decision failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER media_fail_allow BEFORE INSERT ON studio_media_moderation_decisions FOR EACH ROW EXECUTE FUNCTION media_fail_allow()`); err != nil {
		t.Fatal(err)
	}
	events := make(chan event, 16)
	a.mu.Lock()
	a.workspaceSubscribers[events] = subscriptionScope{WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID}
	a.mu.Unlock()
	close(release)
	released = true
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("failing decision worker did not stop")
	}
	if !a.store.failed.Load() {
		t.Fatal("decision failure did not fail closed")
	}
	a.mu.Lock()
	cached := copyTask(a.tasks[task.ID].Task)
	a.mu.Unlock()
	if cached.Status != "MODERATING" || cached.Moderation.State != "PENDING" || len(cached.Artifacts) != 0 {
		t.Fatal("rollback candidate entered cache", cached)
	}
	for len(events) > 0 {
		event := <-events
		if strings.Contains(string(event.Data), `"state":"ALLOWED"`) || strings.Contains(string(event.Data), `"status":"SUCCEEDED"`) || strings.Contains(string(event.Data), `/api/v1/assets/`) {
			t.Fatal("rollback candidate entered SSE")
		}
	}
	durable := readLeasedTask(t, admin, task.ID)
	private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if durable.Status != "MODERATING" || durable.Moderation.State != "PENDING" || len(durable.Artifacts) != 0 || durable.Billing.State != "HELD" || len(private.Candidates) != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals WHERE kind='CHARGE'") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_media_moderation_decisions WHERE task_id=$1", task.ID) != 1 {
		t.Fatal("decision/catalog/business/ledger transaction did not roll back together", durable)
	}
}

func acceptMediaExecution(t *testing.T, a *App, s *httptest.Server, c *http.Client, identity testIdentity, kind, prompt, key string, count int) (Task, TaskInput) {
	t.Helper()
	input := taskInput(kind)
	input.ProjectID = identity.Project.ID
	input.Inputs.Prompt = prompt
	input.Parameters.Count = count
	if a.pricing != nil {
		quote := httpBillingQuote(t, c, s, identity.Project.ID, "task", input)
		input.QuoteID = quote.ID
	}
	code, body, _ := authRequest(t, c, s.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, input)
	if code != 202 {
		t.Fatalf("media accept %d %s", code, body)
	}
	return decode[Task](t, body), input
}

func finishMediaExecution(t *testing.T, a *App, id string) Task {
	t.Helper()
	advanceMediaScheduleDue(t, a, id)
	if !a.executePersistentTaskStep(id) {
		t.Fatal("submit step missing")
	}
	time.Sleep(5 * time.Millisecond)
	advanceMediaScheduleDue(t, a, id)
	if !a.executePersistentTaskStep(id) {
		t.Fatal("query step missing")
	}
	a.mu.Lock()
	pending, err := a.getTaskLocked(id)
	ready := err == nil && pending.ReviewDispatch != nil && pending.ReviewDispatch.State == "READY"
	a.mu.Unlock()
	if !ready {
		t.Fatal("query did not commit independent first-review READY", err)
	}
	advanceMediaScheduleDue(t, a, id)
	if !a.executePersistentTaskStep(id) {
		t.Fatal("first review step missing")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	record, err := a.getTaskLocked(id)
	if err != nil {
		t.Fatal(err)
	}
	return copyTask(record.Task)
}

func TestPostgresMediaModerationWholeBatchReleaseAndBilling(t *testing.T) {
	a, s, client, identity, database := newMediaExecutionFixture(t, true)
	provider := &countedModeratedMedia{MediaProvider: a.media}
	a.media = provider
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	for _, kind := range []string{"image", "video"} {
		for _, variant := range []string{"allow", "block", "review"} {
			t.Run(kind+"-"+variant, func(t *testing.T) {
				prompt := "local fixture"
				if variant == "block" {
					prompt += " [MEDIA_FIXTURE_BLOCK]"
				}
				if variant == "review" {
					prompt += " [MEDIA_FIXTURE_REVIEW]"
				}
				count := 1
				if kind == "image" {
					count = 2
				}
				key := newID("key")
				task, input := acceptMediaExecution(t, a, s, client, identity, kind, prompt, key, count)
				final := finishMediaExecution(t, a, task.ID)
				state := map[string]string{"allow": "ALLOWED", "block": "BLOCKED", "review": "REVIEW"}[variant]
				if final.Moderation == nil || final.Moderation.State != state || final.Moderation.Version != 2 {
					t.Fatalf("wrong final %+v", final)
				}
				private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if private == nil || len(private.Candidates) != count || private.TextPolicy == nil {
					t.Fatal("candidate or frozen input policy missing")
				}
				entries := outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID)
				if variant == "allow" {
					if final.Status != "SUCCEEDED" || len(final.Artifacts) != count || entries != count || final.Billing.State != "SETTLED" || final.Billing.ChargedMicros != int64(100*count) {
						t.Fatal("allowed content and settlement differ", final)
					}
				} else if final.Status != "FAILED" || len(final.Artifacts) != 0 || entries != 0 || final.Billing.State != "REVIEW" || final.Billing.ChargedMicros != 0 || final.Billing.ReleasedMicros != 0 || final.CanReconcile {
					t.Fatal("quarantine released content or money", final)
				}
				for _, candidate := range private.Candidates {
					code, _, _ := authRequest(t, client, s.URL, "GET", "/api/v1/assets/"+candidate.ID, "", identity.Project.ID, nil)
					if variant == "allow" && code != 200 || variant != "allow" && code != 404 {
						t.Fatal("private asset gate", variant, code)
					}
				}
				before := provider.submits.Load()
				code, body, h := authRequest(t, client, s.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, input)
				if code != 202 || h.Get("Idempotency-Replayed") != "true" || decode[Task](t, body).ID != task.ID || provider.submits.Load() != before {
					t.Fatal("original replay regenerated")
				}
			})
		}
	}
	t.Run("image-partial-all-allowed", func(t *testing.T) {
		a.media = partialModeratedMedia{MediaProvider: provider}
		defer func() { a.media = provider }()
		key := newID("key")
		task, input := acceptMediaExecution(t, a, s, client, identity, "image", "safe partial fixture", key, 2)
		final := finishMediaExecution(t, a, task.ID)
		private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if final.Status != "PARTIAL" || final.Moderation == nil || final.Moderation.State != "ALLOWED" || final.Moderation.Version != 2 || len(final.Artifacts) != 1 || private == nil || private.Summary.State != "ALLOWED" || len(private.Candidates) != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 1 {
			t.Fatal("allowed partial result was hidden or expanded", final)
		}
		if final.Billing == nil || final.Billing.State != "SETTLED" || final.Billing.ReservedMicros != 200 || final.Billing.ChargedMicros != 100 || final.Billing.ReleasedMicros != 100 {
			t.Fatal("partial delivery did not settle actual count and release remainder", final.Billing)
		}
		durable := readLeasedTask(t, admin, task.ID)
		if durable.Status != "PARTIAL" || durable.Moderation.State != "ALLOWED" || len(durable.Artifacts) != 1 || durable.Billing.ChargedMicros != 100 || durable.Billing.ReleasedMicros != 100 {
			t.Fatal("partial moderation/delivery/billing did not persist together", durable)
		}
		code, _, _ := authRequest(t, client, s.URL, "GET", final.Artifacts[0].URL, "", identity.Project.ID, nil)
		if code != 200 {
			t.Fatal("approved partial artifact not readable", code)
		}
		beforeSubmits, beforeQueries := provider.submits.Load(), provider.queries.Load()
		code, body, headers := authRequest(t, client, s.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, input)
		if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, body).ID != task.ID || provider.submits.Load() != beforeSubmits || provider.queries.Load() != beforeQueries {
			t.Fatal("partial result replay repeated upstream work")
		}
	})
}

func TestPostgresMediaModerationInputLegacyReferencesAndNoAcceptance(t *testing.T) {
	a, s, client, identity, database := newMediaExecutionFixture(t, false)
	engine := a.mediaModeration
	a.mediaModeration = nil // A pre-B2 private upload, not an approved reference.
	bytes, _ := mediaBytesFixture(t, "block.png")
	code, body, _ := uploadReferenceFixture(t, client, s.URL, identity.Project.ID, "legacy-reference-key", "old.png", bytes, false)
	if code != 201 {
		t.Fatal(code, string(body))
	}
	ref := decode[ReferenceAsset](t, body)
	a.mediaModeration = engine
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	input := taskInput("image")
	input.ProjectID = identity.Project.ID
	input.Operation = "edit"
	input.Inputs.ReferenceAssetIDs = []string{ref.ID}
	code, body, _ = authRequest(t, client, s.URL, "POST", "/api/v1/tasks", "legacy-ref-generation-key", identity.Project.ID, input)
	if code != 422 || !strings.Contains(string(body), "MEDIA_MODERATION_INPUT_BLOCKED") {
		t.Fatal("legacy unreviewed bytes bypassed check", code, string(body))
	}
	input.Operation = "generate"
	input.Inputs.ReferenceAssetIDs = nil
	input.Inputs.Prompt = "DENY-MEDIA-PROMPT"
	code, _, _ = authRequest(t, client, s.URL, "POST", "/api/v1/tasks", "blocked-text-generation-key", identity.Project.ID, input)
	if code != 422 || outboxCount(t, admin, "SELECT count(*) FROM studio_tasks") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_task_media_moderation") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("input failure left accepted task or hold", code)
	}
}

func TestMediaModerationCopyAndAdmissionDigest(t *testing.T) {
	for _, state := range []string{"PENDING", "BLOCKED", "REVIEW"} {
		task := Task{Status: "SUCCEEDED", Moderation: &ModerationSummary{State: state, Version: 2}, Artifacts: []Artifact{{ID: "private"}}}
		safe := copyTask(task)
		if len(safe.Artifacts) != 0 {
			t.Fatal("unapproved artifact copied")
		}
		safe.Moderation.State = "ALLOWED"
		if task.Moderation.State != state {
			t.Fatal("mutable decision alias")
		}
	}
	a, _ := testApp(t, Config{})
	model, err := a.resolveModel("studio-image", "image", "", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	input := taskInput("image")
	parameters, err := resolveTaskParameters(input.Parameters, model.Model)
	if err != nil {
		t.Fatal(err)
	}
	ref := referenceRecord{Blob: ReferenceBlob{SHA256: strings.Repeat("a", 64), ByteSize: 1, ContentType: "image/png"}}
	approval := &taskMediaModerationAdmission{}
	if !isMediaModerationPreparation(approval.prepare(input, parameters, model, []referenceRecord{ref})) {
		t.Fatal("no preflight")
	}
	approval.checked = true
	ref.Blob.SHA256 = strings.Repeat("b", 64)
	if !isFailureCode(approval.prepare(input, parameters, model, []referenceRecord{ref}), "MEDIA_MODERATION_CONTEXT_CHANGED") {
		t.Fatal("changed reference approved")
	}
}

// A delayed checker fixture lets the real worker prove that the first commit
// contains only private candidates, and old lease decisions cannot publish.
func gatedMediaChecker(t *testing.T, a *App) (chan struct{}, chan struct{}) {
	t.Helper()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, _ := readMediaMultipartFixture(t, r)
		entered <- struct{}{}
		<-release
		writeMediaResponse(w, meta, "allow")
	}))
	t.Cleanup(s.Close)
	a.mediaModeration = httpMediaEngine(t, s.URL)
	return entered, release
}

func TestPostgresMediaModerationPendingFenceCancelAndRestart(t *testing.T) {
	for _, mode := range []string{"cancel", "reclaim", "restart"} {
		t.Run(mode, func(t *testing.T) {
			a, s, client, identity, database := newMediaExecutionFixture(t, true)
			entered, release := gatedMediaChecker(t, a)
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			task, _ := acceptMediaExecution(t, a, s, client, identity, "image", "safe fixture", newID("key"), 1)
			advanceMediaScheduleDue(t, a, task.ID)
			if !a.executePersistentTaskStep(task.ID) {
				t.Fatal("submit missing")
			}
			time.Sleep(5 * time.Millisecond)
			advanceMediaScheduleDue(t, a, task.ID)
			if !a.executePersistentTaskStep(task.ID) {
				t.Fatal("candidate READY handoff missing")
			}
			advanceMediaScheduleDue(t, a, task.ID)
			done := make(chan struct{})
			go func() { defer close(done); a.executePersistentTaskStep(task.ID) }()
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("checker not reached")
			}
			admin, err := pgx.Connect(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(context.Background())
			pending := readLeasedTask(t, admin, task.ID)
			private, err := loadTaskMediaModerationSQL(context.Background(), admin, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pending.Status != "MODERATING" || len(pending.Artifacts) != 0 || pending.Moderation.State != "PENDING" || len(private.Candidates) != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 0 {
				t.Fatal("candidate was publicly registered")
			}
			code, _, _ := authRequest(t, client, s.URL, "GET", "/api/v1/assets/"+private.Candidates[0].ID, "", identity.Project.ID, nil)
			if code != 404 {
				t.Fatal("candidate download allowed")
			}
			if mode == "cancel" {
				if _, err = a.cancelTask(task.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				expireTaskLease(t, admin, task.ID)
			}
			close(release)
			released = true
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("old worker did not stop")
			}
			if mode == "reclaim" {
				advanceMediaScheduleDue(t, a, task.ID)
				if !a.executePersistentTaskStep(task.ID) {
					t.Fatal("reclaim missing")
				}
			}
			if mode == "restart" {
				cfg := a.config
				a.Close()
				cfg.MediaModeration = MediaModerationConfig{}
				cfg.Moderation = ModerationConfig{}
				reopened, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
			}
			final := readLeasedTask(t, admin, task.ID)
			if len(final.Artifacts) != 0 || final.Moderation.State != "REVIEW" || final.Billing.State != "REVIEW" || outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID) != 0 {
				raw, _ := json.Marshal(final)
				t.Fatal("late allow escaped fence", string(raw))
			}
		})
	}
}
