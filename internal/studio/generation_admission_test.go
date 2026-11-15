package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// These fixtures alter only disposable database policy state. The operations
// domain tests separately exercise password/proof/plan/audit and immutable
// receipts; these tests focus on every downstream acceptance boundary.
func restrictGenerationFixture(t *testing.T, a *App, identity testIdentity) string {
	t.Helper()
	id := newID("grst")
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = lockGenerationWorkspaceSQL(ctx, tx, identity.Workspace.ID, true); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO studio_generation_restrictions(id,workspace_id,reason_code,public_notice,evidence_ref,created_by) VALUES($1,$2,'manual_review','fixture new generation restriction','fixture:admission',$3)`, id, identity.Workspace.ID, identity.User.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO studio_generation_controls(workspace_id,state,version,active_restriction_id) VALUES($1,'RESTRICTED',2,$2) ON CONFLICT(workspace_id) DO UPDATE SET state='RESTRICTED',version=studio_generation_controls.version+1,active_restriction_id=EXCLUDED.active_restriction_id`, identity.Workspace.ID, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type generationAdmissionRequest struct {
	path, key, id, quoteID string
	body, quoteBody        any
	status                 int
}

func generationAdmissionRequestFixture(t *testing.T, a *App, server *httptest.Server, client *http.Client, identity testIdentity, kind, label string) generationAdmissionRequest {
	t.Helper()
	v := generationAdmissionRequest{key: newID("key"), status: 201}
	var run RunInput
	var task TaskInput
	var turn ConversationRunInput
	switch kind {
	case "run":
		run = RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: label}
		v.path = "/api/v1/runs"
		v.quoteBody = map[string]any{"resource_type": "run", "run_input": run}
	case "task":
		task = taskInput("image")
		task.ProjectID = identity.Project.ID
		task.Inputs.Prompt = label
		v.path, v.status = "/api/v1/tasks", 202
		v.quoteBody = map[string]any{"resource_type": "task", "task_input": task}
	case "conversation":
		conversation := createPGConversation(t, client, server, identity)
		turn = conversationInput(conversation.Version, nil, label)
		v.path = "/api/v1/conversations/" + conversation.ID + "/runs"
		v.quoteBody = map[string]any{"resource_type": "conversation_run", "conversation_id": conversation.ID, "conversation_input": turn}
	}
	code, raw, _ := authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", identity.Project.ID, v.quoteBody)
	if code != 201 {
		t.Fatal("fixture quote", code, string(raw))
	}
	v.quoteID = decode[BillingQuote](t, raw).ID
	switch kind {
	case "run":
		run.QuoteID = v.quoteID
		v.body = run
	case "task":
		task.QuoteID = v.quoteID
		v.body = task
	case "conversation":
		turn.QuoteID = v.quoteID
		v.body = turn
	}
	return v
}

func TestPostgresGenerationAdmissionOriginalReplayAndExistingWork(t *testing.T) {
	database := testDatabase(t)
	a, server := testApp(t, Config{DatabaseURL: database, AuthMode: "required", OperationsEnabled: true, BillingMode: "credits", ModelCatalogFile: filepath.Join("..", "..", "config", "models.example.json"), BillingPriceFile: filepath.Join("..", "..", "config", "prices.demo.example.json"), PollInterval: time.Hour, DemoTaskDuration: time.Millisecond})
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "generation-admission@example.test")
	httpCredit(t, a, identity.Workspace.ID, 10_000_000)
	provider := &moderatedExecutionChat{text: "existing accepted result", emitted: make(chan struct{}, 2), release: make(chan struct{})}
	defer func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
	}()
	a.chat = provider
	var accepted, pending []generationAdmissionRequest
	for _, kind := range []string{"run", "task", "conversation"} {
		v := generationAdmissionRequestFixture(t, a, server, client, identity, kind, "accepted "+kind)
		code, raw, _ := authRequest(t, client, server.URL, "POST", v.path, v.key, identity.Project.ID, v.body)
		if code != v.status {
			t.Fatal("accept", code, string(raw))
		}
		var resource struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &resource); err != nil {
			t.Fatal(err)
		}
		v.id = resource.ID
		accepted = append(accepted, v)
		pending = append(pending, generationAdmissionRequestFixture(t, a, server, client, identity, kind, "unaccepted "+kind))
	}
	awaitModeratedEmission(t, provider)
	awaitModeratedEmission(t, provider)
	if !a.executePersistentTaskStep(accepted[1].id) {
		t.Fatal("existing media submission missing")
	}
	restrictGenerationFixture(t, a, identity)
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	beforeRuns := outboxCount(t, admin, "SELECT count(*) FROM studio_runs")
	beforeTasks := outboxCount(t, admin, "SELECT count(*) FROM studio_tasks")
	beforeIdem := outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency")
	beforeHolds := outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations")
	beforeNodes := outboxCount(t, admin, "SELECT count(*) FROM studio_chat_messages")
	for _, v := range accepted {
		code, raw, h := authRequest(t, client, server.URL, "POST", v.path, v.key, identity.Project.ID, v.body)
		if code != v.status || h.Get("Idempotency-Replayed") != "true" || !strings.Contains(string(raw), v.id) {
			t.Fatal("restriction intercepted original replay", code, string(raw))
		}
	}
	changed := accepted[0].body.(RunInput)
	changed.Input = "different original request"
	code, raw, _ := authRequest(t, client, server.URL, "POST", accepted[0].path, accepted[0].key, identity.Project.ID, changed)
	if code != 409 || !strings.Contains(string(raw), "IDEMPOTENCY_CONFLICT") {
		t.Fatal("restriction hid idempotency conflict", code, string(raw))
	}
	for _, v := range pending {
		code, raw, _ := authRequest(t, client, server.URL, "POST", v.path, v.key, identity.Project.ID, v.body)
		if code != 403 || !strings.Contains(string(raw), "GENERATION_RESTRICTED") {
			t.Fatal("new request not restricted", v.path, code, string(raw))
		}
		code, raw, _ = authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", identity.Project.ID, v.quoteBody)
		if code != 403 || !strings.Contains(string(raw), "GENERATION_RESTRICTED") {
			t.Fatal("new quote not restricted", code, string(raw))
		}
		var unused bool
		if err = admin.QueryRow(context.Background(), "SELECT reservation_id IS NULL FROM studio_ledger_quotes WHERE id=$1", v.quoteID).Scan(&unused); err != nil || !unused {
			t.Fatal("rejected request consumed original quote", err)
		}
	}
	// Exercise persistence entry points directly too: the HTTP/preflight gate
	// alone must never be the only protection before reservation or INSERT.
	now := time.Now().UTC()
	directRun := Run{ID: newID("run"), WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, ModelID: "studio-chat", ProviderMode: "demo", Prompt: "direct rejected", Status: "QUEUED", CreatedAt: now, UpdatedAt: now, BillingQuoteID: pending[0].quoteID}
	if err := a.store.createRunCandidate(&directRun, identity.Workspace.ID+":runs:"+newID("key"), payloadHash(directRun)); !isFailureCode(err, "GENERATION_RESTRICTED") {
		t.Fatal("run transaction omitted final gate", err)
	}
	directTask := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, ModelID: "studio-image", Kind: "image", ProviderMode: "demo", Prompt: "direct rejected", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, BillingQuoteID: pending[1].quoteID}, NextPoll: now}
	if err := a.store.createTask(directTask, identity.Workspace.ID+":tasks:"+newID("key"), payloadHash(directTask.Input)); !isFailureCode(err, "GENERATION_RESTRICTED") {
		t.Fatal("task transaction omitted final gate", err)
	}
	if beforeRuns != outboxCount(t, admin, "SELECT count(*) FROM studio_runs") || beforeTasks != outboxCount(t, admin, "SELECT count(*) FROM studio_tasks") || beforeIdem != outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency") || beforeHolds != outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") || beforeNodes != outboxCount(t, admin, "SELECT count(*) FROM studio_chat_messages") {
		t.Fatal("restriction rejection left business/hold/idem/message side effects")
	}
	close(provider.release)
	for _, v := range []generationAdmissionRequest{accepted[0], accepted[2]} {
		run := waitConversationRun(t, a, v.id)
		if run.Status != "COMPLETED" || run.Billing.State != "SETTLED" || run.Output == "" {
			t.Fatal("restriction altered accepted LLM", run)
		}
	}
	advanceMediaScheduleDue(t, a, accepted[1].id)
	if !a.executePersistentTaskStep(accepted[1].id) {
		t.Fatal("restricted workspace stopped original media query")
	}
	media := readLeasedTask(t, admin, accepted[1].id)
	if media.Status != "SUCCEEDED" || media.Billing.State != "SETTLED" || len(media.Artifacts) != 1 {
		t.Fatal("restriction altered original media delivery", media)
	}
	code, raw, _ = uploadReferenceFixture(t, client, server.URL, identity.Project.ID, newID("upload"), "still-allowed.png", referencePNG(t, 5), false)
	if code != 201 {
		t.Fatal("generation restriction blocked reference upload", code, string(raw))
	}
	ref := decode[ReferenceAsset](t, raw)
	code, _, _ = authRequest(t, client, server.URL, "GET", ref.URL, "", identity.Project.ID, nil)
	if code != 200 {
		t.Fatal("generation restriction blocked private reference read", code)
	}
	cfg := a.config
	a.Close()
	cfg.OperationsEnabled = false
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := httptest.NewServer(reopened.Handler())
	defer second.Close()
	code, raw, _ = authRequest(t, client, second.URL, "POST", pending[0].path, pending[0].key, identity.Project.ID, pending[0].body)
	if code != 403 || !strings.Contains(string(raw), "GENERATION_RESTRICTED") {
		t.Fatal("disabled management removed persistent restriction", code, string(raw))
	}
	code, _, h := authRequest(t, client, second.URL, "POST", accepted[0].path, accepted[0].key, identity.Project.ID, accepted[0].body)
	if code != 201 || h.Get("Idempotency-Replayed") != "true" {
		t.Fatal("restart lost prior acceptance")
	}
}

func TestPostgresOperationsModeratorScopedPermissionsAndOfflineRole(t *testing.T) {
	a, server, client, identity, database := operationsFixture(t)
	otherClient := authClient(t)
	other := registerFixture(t, otherClient, server.URL, "moderator-target@example.test")
	request := operationsRequest(t, client, server)
	for _, role := range []string{"observer", "operator", "finance"} {
		operationsGrantFixture(t, a, identity.User.ID, other.Workspace.ID, role)
	}
	for _, permission := range []string{"generation_restrict", "appeal_resolve"} {
		if _, err := a.requireOperations(request, other.Workspace.ID, permission); err == nil {
			t.Fatal("legacy role acquired moderation permission", permission)
		}
	}
	cfg := a.config
	a.Close()
	role := OperationsAdminConfig{DatabaseURL: database, Action: "grant", WorkspaceID: other.Workspace.ID, UserID: identity.User.ID, Role: "moderator", OperationKey: "moderator:fixture", Reason: "staff_assignment", EvidenceRef: "fixture:moderator"}
	preview, err := AdministerOperations(context.Background(), role)
	if err != nil || preview.Result == nil || preview.Applied {
		t.Fatal("moderator CLI preview", err)
	}
	role.Apply, role.PlanDigest = true, preview.PlanDigest
	applied, err := AdministerOperations(context.Background(), role)
	if err != nil || !applied.Applied {
		t.Fatal("moderator CLI apply", err)
	}
	inspect, err := AdministerOperations(context.Background(), OperationsAdminConfig{DatabaseURL: database, Action: "inspect", WorkspaceID: other.Workspace.ID, UserID: identity.User.ID})
	if err != nil || len(inspect.Grants) != 4 {
		t.Fatal("fourth explicit role missing", err)
	}
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := httptest.NewServer(reopened.Handler())
	defer second.Close()
	request = operationsRequest(t, client, second)
	for _, permission := range []string{"generation_restrict", "appeal_resolve"} {
		if _, err = reopened.requireOperations(request, other.Workspace.ID, permission); err != nil {
			t.Fatal("scoped moderator denied", permission, err)
		}
		if _, err = reopened.requireOperations(request, identity.Workspace.ID, permission); err == nil {
			t.Fatal("moderator escaped granted workspace")
		}
	}
	p, err := reopened.currentPrincipal(request)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := reopened.verifyOperationsPassword(request, "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if err = operationsCheckFixture(t, reopened, p, other.Workspace.ID, "generation_restrict", proof); err != nil {
		t.Fatal(err)
	}
	securityExec(t, reopened, "UPDATE studio_operations_grants SET active=false,version=version+1 WHERE id=$1", applied.Result.Grant.ID)
	if err = operationsCheckFixture(t, reopened, p, other.Workspace.ID, "generation_restrict", proof); err == nil {
		t.Fatal("revoked moderator proof retained authority")
	}
	if reopened.store.failed.Load() {
		t.Fatal("expected role rejection poisoned DB")
	}
}

func TestPostgresGenerationAdmissionRechecksAfterModeration(t *testing.T) {
	a, server, client, identity, database := newModeratedBillingFixture(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	a.moderation = executionHTTPModerator(t, func(w http.ResponseWriter, r *http.Request, in moderationHTTPRequest) {
		if in.Phase == "input" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		allowExecutionHTTP(w, in)
	})
	provider := &moderatedExecutionChat{text: "must not generate"}
	a.chat = provider
	in := RunInput{ProjectID: identity.Project.ID, ModelID: "studio-chat", Input: "safe input before restriction"}
	q := httpBillingQuote(t, client, server, identity.Project.ID, "run", in)
	in.QuoteID = q.ID
	result := make(chan error, 1)
	go func() { _, _, err := a.createRun(in, newID("key"), identity.User.ID); result <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("moderation not reached")
	}
	restrictGenerationFixture(t, a, identity)
	close(release)
	select {
	case err := <-result:
		if !isFailureCode(err, "GENERATION_RESTRICTED") {
			t.Fatal("restriction during moderation bypassed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("moderation did not return")
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if provider.calls.Load() != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_runs") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_reservations") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency WHERE resource_type='run'") != 0 {
		t.Fatal("moderation race created or charged generation")
	}
}

func TestPostgresGenerationAdmissionRechecksAfterEditPreparation(t *testing.T) {
	database := testDatabase(t)
	provider := &blockingReferencePrepare{started: make(chan struct{}, 1), release: make(chan struct{})}
	defer func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
	}()
	a, server := testApp(t, Config{DatabaseURL: database, AuthMode: "required", PollInterval: time.Hour, mediaProvider: provider})
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "generation-edit@example.test")
	code, raw, _ := uploadReferenceFixture(t, client, server.URL, identity.Project.ID, newID("upload"), "fixture.png", referencePNG(t, 7), false)
	if code != 201 {
		t.Fatal(code, string(raw))
	}
	ref := decode[ReferenceAsset](t, raw)
	in := taskInput("image")
	in.ProjectID = identity.Project.ID
	in.Operation = "edit"
	in.Inputs.ReferenceAssetIDs = []string{ref.ID}
	result := make(chan error, 1)
	go func() { _, _, err := a.createTask(in, newID("key"), identity.User.ID); result <- err }()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("edit prepare not reached")
	}
	restrictGenerationFixture(t, a, identity)
	close(provider.release)
	select {
	case err := <-result:
		if !isFailureCode(err, "GENERATION_RESTRICTED") {
			t.Fatal("restriction during edit preparation bypassed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("edit prepare did not return")
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if outboxCount(t, admin, "SELECT count(*) FROM studio_tasks") != 0 || outboxCount(t, admin, "SELECT count(*) FROM studio_idempotency WHERE resource_type='task'") != 0 {
		t.Fatal("edit preparation race accepted new task")
	}
}
