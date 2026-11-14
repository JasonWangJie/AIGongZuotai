package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type operationsCasesFixture struct {
	app       *App
	server    *httptest.Server
	client    *http.Client
	identity  testIdentity
	target    testIdentity
	principal *principal
	request   *http.Request
	admin     *pgx.Conn
}

func newOperationsFixture(t *testing.T, providers ...MediaProvider) operationsCasesFixture {
	t.Helper()
	database := testDatabase(t)
	cfg := Config{DatabaseURL: database, AuthMode: "required", OperationsEnabled: true, ReferenceDir: filepath.Join(t.TempDir(), "references"), AssetDir: filepath.Join(t.TempDir(), "assets"), PollInterval: time.Hour}
	if len(providers) > 0 {
		cfg.mediaProvider = providers[0]
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "operator-fixture@example.test")
	target := registerFixture(t, authClient(t), server.URL, "subject-fixture@example.test")
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	request, _ := http.NewRequest("GET", server.URL+"/api/v1/auth/session", nil)
	for _, cookie := range client.Jar.Cookies(request.URL) {
		request.AddCookie(cookie)
	}
	p, err := a.currentPrincipal(request)
	if err != nil || p == nil {
		t.Fatal("fixture principal", err)
	}
	return operationsCasesFixture{a, server, client, identity, target, p, request, admin}
}
func (f operationsCasesFixture) grant(t *testing.T, role string) {
	t.Helper()
	if _, err := f.admin.Exec(context.Background(), `INSERT INTO studio_operations_grants(id,user_id,workspace_id,role) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,workspace_id,role) DO UPDATE SET active=true,version=studio_operations_grants.version+1`, newID("ogr"), f.identity.User.ID, f.target.Workspace.ID, role); err != nil {
		t.Fatal(err)
	}
}
func (f operationsCasesFixture) run(t *testing.T, billed bool) Run {
	t.Helper()
	run := outboxFixtureRun(newID("run"))
	run.WorkspaceID = f.target.Workspace.ID
	run.ProjectID = f.target.Project.ID
	run.ModelVersion = "fixture-model"
	run.Status = "FAILED"
	run.Prompt = "PRIVATE_OPERATIONS_PROMPT"
	run.Output = "PRIVATE_OPERATIONS_OUTPUT"
	if billed {
		err := ledgerTx(f.app.store, func(ctx context.Context, tx pgx.Tx) error {
			_, e := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: run.WorkspaceID, OperationKey: newID("credit"), Reason: "fixture", EvidenceRef: "case:fixture", AmountMicros: 5000})
			if e != nil {
				return e
			}
			q := ledgerQuoteRow{ID: newID("quo"), WorkspaceID: run.WorkspaceID, ProjectID: run.ProjectID, ResourceType: "run", ModelID: run.ModelID, ModelVersion: run.ModelVersion, PriceVersion: "fixture-price", RequestHash: payloadHash(run), Price: LedgerPriceSnapshot{ProviderMode: "demo", Basis: "chat_tokens", MaxInputTokens: 1000, MaxOutputTokens: 1000, InputPerMillionMicros: 1_000_000, OutputPerMillionMicros: 2_000_000}, ReserveMicros: 3000, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
			if e = insertLedgerQuoteSQL(ctx, tx, q); e != nil {
				return e
			}
			run.BillingQuoteID = q.ID
			run.BillingRequestHash = q.RequestHash
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		run.Status = "QUEUED"
	}
	if err := f.app.store.createRunCandidate(&run, newID("key"), payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	if billed {
		run.Status = "RUNNING"
		if err := f.app.store.saveRunCandidate(&run); err != nil {
			t.Fatal(err)
		}
		run.Status = "FAILED"
		run.Seq = 1
		if err := f.app.store.saveRunCandidate(&run); err != nil {
			t.Fatal(err)
		}
	}
	return run
}
func (f operationsCasesFixture) open(t *testing.T, run Run) OperationsCase {
	t.Helper()
	c, _, err := f.app.createOperationsCase(f.principal, OperationsCaseInput{WorkspaceID: run.WorkspaceID, ResourceType: "run", ResourceID: run.ID, ResourceVersion: run.Seq, Reason: "manual_review", EvidenceRef: "ticket:fixture"}, newID("key"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func (f operationsCasesFixture) plan(t *testing.T, c OperationsCase, action string) OperationsPlan {
	t.Helper()
	plan, err := f.app.previewOperationsCase(f.principal, c.ID, OperationsPlanInput{WorkspaceID: c.WorkspaceID, ExpectedVersion: c.Version, Action: action, Reason: "manual_review", EvidenceRef: "ticket:fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func (f operationsCasesFixture) proof(t *testing.T) *operationsPasswordProof {
	t.Helper()
	p, e := f.app.verifyOperationsPassword(f.request, "fixture-password-123")
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestOperationsInputAndCursorBoundaries(t *testing.T) {
	for _, v := range []string{"ticket:123", "case-abc_1", "provider.case/123"} {
		if !validOperationsReason("billing_review", v) {
			t.Fatal("valid opaque identifier rejected")
		}
	}
	for _, v := range []string{"", "https://private.test", "secret text", "x\x00y", strings.Repeat("a", 129), "中文证据"} {
		if validOperationsReason("billing_review", v) {
			t.Fatal("unbounded/free-text evidence accepted")
		}
	}
	if validOperationsReason("arbitrary-reason", "ticket:1") {
		t.Fatal("open reason enum")
	}
	p := &principal{User: &User{ID: newID("usr")}}
	r := httptest.NewRequest("GET", "/api/v1/operations/cases?limit=51", nil)
	if _, _, e := parseOperationsPage(r, p, "ws_fixture", "cases", "all"); e == nil {
		t.Fatal("unbounded pagination")
	}
	cursor := operationsCursor{Version: 1, User: p.User.ID, Workspace: newID("ws"), Route: "cases", Filter: "OPEN", Before: time.Now(), ID: newID("ocase")}
	encoded := *nextOperationsCursor(cursor)
	for _, field := range []string{"workspace", "filter", "user", "route"} {
		bad := cursor
		switch field {
		case "workspace":
			bad.Workspace = newID("ws")
		case "filter":
			bad.Filter = "all"
		case "user":
			bad.User = newID("usr")
		case "route":
			bad.Route = "exceptions"
		}
		req := httptest.NewRequest("GET", "/api/v1/operations/cases?cursor="+encoded, nil)
		actor := &principal{User: &User{ID: bad.User}}
		if _, _, err := parseOperationsPage(req, actor, bad.Workspace, bad.Route, bad.Filter); err == nil {
			t.Fatal("foreign cursor accepted", field)
		}
	}
}

func TestPostgresOperationsPlanDryRunExecuteReceiptAndReplay(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "finance")
	run := f.run(t, true)
	c := f.open(t, run)
	before := outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_journals")
	plan := f.plan(t, c, "release")
	if plan.Effect.ReleasedMicros != 3000 || plan.Effect.ChargedMicros != 0 {
		t.Fatalf("incorrect effect %+v", plan)
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_journals") != before || outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_operations WHERE operation_key=$1", "ops:"+plan.ID) != 0 {
		t.Fatal("preview committed financial mutation")
	}
	_, err := f.app.executeOperationsCase(f.principal, nil, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "OPERATIONS_REAUTH_REQUIRED")
	proof := f.proof(t)
	result, err := f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "RESOLVED" || result.Version != 2 || result.Receipt == nil || result.Receipt.Action != "release" || result.Resource.Billing.State != "RELEASED" {
		t.Fatalf("bad receipt/result %+v", result)
	}
	_, err = f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "OPERATIONS_PLAN_CONSUMED")
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_operations_receipts WHERE case_id=$1", c.ID) != 1 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_operations WHERE operation_key=$1", "ops:"+plan.ID) != 1 {
		t.Fatal("duplicate execution")
	}
	var raw string
	if err = f.admin.QueryRow(context.Background(), "SELECT snapshot::text FROM studio_operations_plans WHERE id=$1", plan.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE_OPERATIONS_", "fixture-password-123", "token_hash", "session_hash", f.target.User.Email} {
		if strings.Contains(raw, secret) {
			t.Fatal("private content leaked into plan")
		}
	}
	if err = ledgerTx(f.app.store, func(ctx context.Context, tx pgx.Tx) error {
		v, e := reconcileLedgerSQL(ctx, tx, c.WorkspaceID)
		if e == nil && (!v.Balanced || !v.AccountBalancesMatch || !v.HoldsMatch || !v.TotalsMatch) {
			t.Fatal("ledger drift")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresOperationsStalePlansAndRevokedAuthority(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "operator")
	run := f.run(t, false)
	c := f.open(t, run)
	plan := f.plan(t, c, "no_action")
	proof := f.proof(t)
	_, err := f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version+1)
	requireOperation(t, err, "OPERATIONS_CASE_CHANGED")
	if _, err = f.admin.Exec(context.Background(), "UPDATE studio_operations_grants SET active=false,version=version+1 WHERE user_id=$1", f.identity.User.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "OPERATIONS_NOT_FOUND")
	f.grant(t, "operator")
	_, err = f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "OPERATIONS_PLAN_CHANGED")
	plan = f.plan(t, c, "no_action")
	if _, err = f.admin.Exec(context.Background(), "UPDATE studio_operations_plans SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", plan.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "OPERATIONS_PLAN_EXPIRED")
	plan = f.plan(t, c, "no_action")
	run.Seq++
	if err = f.app.store.saveRunCandidate(&run); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "OPERATIONS_RESOURCE_CHANGED")
	plan = f.plan(t, c, "no_action")
	if _, err = f.admin.Exec(context.Background(), "UPDATE studio_users SET credential_version=credential_version+1 WHERE id=$1", f.identity.User.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "AUTH_REQUIRED")
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_operations_receipts") != 0 {
		t.Fatal("invalid proof/plan mutated business")
	}
}

func TestPostgresOperationsNoActionPreservesLiveCacheAndConcurrentCASE(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "operator")
	run := f.run(t, false)
	f.app.mu.Lock()
	cached := run
	cached.Output = "uncheckpointed private delta"
	cached.Seq = 9
	f.app.runs[run.ID] = &runRecord{Run: cached, Subscribers: map[chan event]bool{}}
	f.app.mu.Unlock()
	c := f.open(t, run)
	plan1 := f.plan(t, c, "no_action")
	plan2 := f.plan(t, c, "no_action")
	proof := f.proof(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, plan := range []OperationsPlan{plan1, plan2} {
		wg.Add(1)
		go func(p OperationsPlan) {
			defer wg.Done()
			_, e := f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, p.ID, p.PlanDigest, c.Version)
			results <- e
		}(plan)
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else {
			requireOperation(t, e, "OPERATIONS_CASE_RESOLVED")
		}
	}
	if successes != 1 {
		t.Fatal("case executed twice")
	}
	f.app.mu.Lock()
	defer f.app.mu.Unlock()
	if f.app.runs[run.ID].Run.Output != cached.Output || f.app.runs[run.ID].Run.Seq != 9 {
		t.Fatal("no_action replaced active uncheckpointed state")
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_operations_receipts WHERE case_id=$1", c.ID) != 1 {
		t.Fatal("multiple receipts")
	}
}

func TestPostgresOperationsExecuteAuditFailureRollsBackEverything(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "finance")
	run := f.run(t, true)
	c := f.open(t, run)
	plan := f.plan(t, c, "release")
	proof := f.proof(t)
	f.app.mu.Lock()
	f.app.runs[run.ID] = &runRecord{Run: copyRun(run), Subscribers: map[chan event]bool{}}
	f.app.mu.Unlock()
	if _, err := f.admin.Exec(context.Background(), `CREATE FUNCTION reject_ops_execute() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='case.executed' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_ops_execute BEFORE INSERT ON studio_operations_audit FOR EACH ROW EXECUTE FUNCTION reject_ops_execute()`); err != nil {
		t.Fatal(err)
	}
	_, err := f.app.executeOperationsCase(f.principal, proof, c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	requireOperation(t, err, "DATABASE_UNAVAILABLE")
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_operations_receipts") != 0 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_ledger_operations WHERE operation_key=$1", "ops:"+plan.ID) != 0 || outboxCount(t, f.admin, "SELECT count(*) FROM studio_operations_plans WHERE id=$1 AND consumed_at IS NOT NULL", plan.ID) != 0 {
		t.Fatal("failed audit partially committed")
	}
	var state string
	if err = f.admin.QueryRow(context.Background(), "SELECT state FROM studio_ledger_reservations WHERE id=$1", run.Billing.ReservationID).Scan(&state); err != nil || state != "REVIEW" {
		t.Fatal("audit rollback released hold")
	}
	f.app.mu.Lock()
	defer f.app.mu.Unlock()
	if f.app.runs[run.ID].Run.Billing.State != "REVIEW" {
		t.Fatal("uncommitted cache published")
	}
}

func TestPostgresOperationsMediaReconcileOnlySchedulesOriginalHandle(t *testing.T) {
	provider := &leaseMedia{query: func(ctx context.Context, h MediaHandle) (MediaResult, error) {
		if h.ID != "PRIVATE_UPSTREAM_HANDLE" {
			t.Error("reconcile queried a different handle")
		}
		return MediaResult{Status: "SUCCEEDED", Progress: 100}, nil
	}}
	f := newOperationsFixture(t, provider)
	f.grant(t, "operator")
	now := time.Now().UTC()
	id := newID("task")
	task := &taskRecord{Task: Task{ID: id, WorkspaceID: f.target.Workspace.ID, ProjectID: f.target.Project.ID, ProviderMode: "demo", Kind: "image", ModelID: "studio-image", Status: "RECONCILE_REQUIRED", Version: 1, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{}}, Input: taskInput("image"), Handle: MediaHandle{ID: "PRIVATE_UPSTREAM_HANDLE"}, NextPoll: now.Add(time.Hour)}
	if err := f.app.store.createTask(task, newID("key"), payloadHash(task.Input)); err != nil {
		t.Fatal(err)
	}
	c, _, err := f.app.createOperationsCase(f.principal, OperationsCaseInput{WorkspaceID: task.Task.WorkspaceID, ResourceType: "task", ResourceID: id, ResourceVersion: 1, Reason: "provider_unknown", EvidenceRef: "ticket:media"}, newID("key"))
	if err != nil {
		t.Fatal(err)
	}
	plan := f.plan(t, c, "reconcile")
	var before []byte
	if err = f.admin.QueryRow(context.Background(), "SELECT handle FROM studio_tasks WHERE id=$1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	result, err := f.app.executeOperationsCase(f.principal, f.proof(t), c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	var after []byte
	var status string
	if err = f.admin.QueryRow(context.Background(), "SELECT handle,document->>'status' FROM studio_tasks WHERE id=$1", id).Scan(&after, &status); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || status != "QUEUED" || result.Resource.CanReconcile {
		t.Fatal("reconcile changed provider identity or failed scheduling")
	}
	if !f.app.executePersistentTaskStep(id) || provider.submits.Load() != 0 || provider.queries.Load() != 1 {
		t.Fatal("reconcile did not query exactly the original accepted task")
	}
	public, _ := json.Marshal(result)
	if strings.Contains(string(public), "PRIVATE_UPSTREAM_HANDLE") {
		t.Fatal("handle leaked")
	}
	task.Task.ID = newID("task")
	task.Handle = MediaHandle{}
	if err = f.app.store.createTask(task, newID("key"), payloadHash(task.Input)); err != nil {
		t.Fatal(err)
	}
	c, _, err = f.app.createOperationsCase(f.principal, OperationsCaseInput{WorkspaceID: task.Task.WorkspaceID, ResourceType: "task", ResourceID: task.Task.ID, ResourceVersion: 1, Reason: "provider_unknown", EvidenceRef: "ticket:no-handle"}, newID("key"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.app.previewOperationsCase(f.principal, c.ID, OperationsPlanInput{WorkspaceID: c.WorkspaceID, ExpectedVersion: c.Version, Action: "reconcile", Reason: "provider_unknown", EvidenceRef: "ticket:no-handle"})
	requireOperation(t, err, "RECONCILE_UNAVAILABLE")
}

func TestPostgresOperationsFinanceDecisionFencesOldMediaLease(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "finance")
	f.run(t, true)
	input := taskInput("image")
	now := time.Now().UTC()
	q := ledgerQuoteRow{ID: newID("quo"), WorkspaceID: f.target.Workspace.ID, ProjectID: f.target.Project.ID, ResourceType: "task", ModelID: "studio-image", ModelVersion: "fixture", PriceVersion: "fixture-price", RequestHash: payloadHash(input), Price: LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: 500, RequestedUnits: 1}, ReserveMicros: 500, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := ledgerTx(f.app.store, func(ctx context.Context, tx pgx.Tx) error { return insertLedgerQuoteSQL(ctx, tx, q) }); err != nil {
		t.Fatal(err)
	}
	task := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: q.WorkspaceID, ProjectID: q.ProjectID, ProviderMode: "demo", Kind: "image", ModelID: q.ModelID, ModelVersion: q.ModelVersion, Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, BillingQuoteID: q.ID, BillingRequestHash: q.RequestHash, Artifacts: []Artifact{}}, Input: input, NextPoll: now}
	if err := f.app.store.createTask(task, newID("key"), payloadHash(input)); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "SUBMITTING"
	task.Task.Version++
	if err := f.app.store.saveTask(task); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "RECONCILE_REQUIRED"
	task.Task.Version++
	task.Handle = MediaHandle{ID: "private-late-handle"}
	if err := f.app.store.saveTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(context.Background(), "UPDATE studio_tasks SET lease_owner='old-owner',lease_epoch=9,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1", task.Task.ID); err != nil {
		t.Fatal(err)
	}
	old := cloneLeasedRecord(task)
	old.Lease = &TaskLease{TaskID: task.Task.ID, Owner: "old-owner", Epoch: 9, Generation: f.app.taskLeases.generation, Version: task.Task.Version}
	old.LeaseStatus = task.Task.Status
	c, _, err := f.app.createOperationsCase(f.principal, OperationsCaseInput{WorkspaceID: q.WorkspaceID, ResourceType: "task", ResourceID: task.Task.ID, ResourceVersion: task.Task.Version, Reason: "billing_review", EvidenceRef: "ticket:late"}, newID("key"))
	if err != nil {
		t.Fatal(err)
	}
	plan := f.plan(t, c, "release")
	if _, err = f.app.executeOperationsCase(f.principal, f.proof(t), c.ID, c.WorkspaceID, plan.ID, plan.PlanDigest, c.Version); err != nil {
		t.Fatal(err)
	}
	old.Task.Status = "SUCCEEDED"
	old.Task.Version++
	old.Task.Artifacts = []Artifact{{ID: "late", Kind: "image", Title: "late", URL: "/demo-assets/image-1.svg"}}
	requireOperation(t, f.app.store.saveTask(old), "TASK_LEASE_LOST")
	var owner string
	var epoch int64
	if err = f.admin.QueryRow(context.Background(), "SELECT lease_owner,lease_epoch FROM studio_tasks WHERE id=$1", task.Task.ID).Scan(&owner, &epoch); err != nil || owner != "" || epoch != 10 {
		t.Fatal("finance did not revoke old owner/epoch")
	}
	if outboxCount(t, f.admin, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.Task.ID) != 0 {
		t.Fatal("old worker registered delivery after financial decision")
	}
}
