package studio

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Count statements only. Never collect SQL arguments, prompts or descriptors.
type recoveryReadTracer struct {
	selects        int
	afterPreflight func()
}

type recoveryPreflightTraceKey struct{}

func (t *recoveryReadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT") {
		t.selects++
	}
	if strings.HasSuffix(data.SQL, ") bounded") {
		return context.WithValue(ctx, recoveryPreflightTraceKey{}, true)
	}
	return ctx
}
func (t *recoveryReadTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(recoveryPreflightTraceKey{}) == true && t.afterPreflight != nil {
		t.afterPreflight()
	}
}

func TestRecoveryMediaPageRejectsInvalidBoundsBeforeDatabase(t *testing.T) {
	valid := &taskRecord{Task: Task{ID: "task_fixture"}}
	for _, page := range [][]*taskRecord{{nil}, {{}}, {valid, valid}, make([]*taskRecord, recoveryMediaPageRecords+1)} {
		if _, err := loadRecoveryMediaPhasesSQL(context.Background(), nil, page, recoveryMediaPageBytes); err == nil {
			t.Fatal("invalid page reached database")
		}
	}
	for _, budget := range []int64{0, -1, recoveryMediaPageBytes + 1} {
		if _, err := loadRecoveryMediaPhasesSQL(context.Background(), nil, []*taskRecord{valid}, budget); err == nil {
			t.Fatal("invalid byte budget reached database")
		}
	}
}

func TestPostgresRecoveryMediaPagePreservesPrivatePhases(t *testing.T) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "output")
	cfg := reviewDispatchConfig(t, media, review, true, 1)
	a, server := testApp(t, cfg)
	client, identity := deliveryIdentity(t, server, true)
	task, _, _ := laneAccept(t, a, server, client, identity, "recovery-private-page")
	prepareReviewDispatch(t, a, task.ID)
	a.mu.Lock()
	original := cloneLeasedRecord(a.tasks[task.ID])
	a.mu.Unlock()
	connectionConfig, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		t.Fatal("fixture connection configuration")
	}
	tracer := &recoveryReadTracer{}
	connectionConfig.Tracer = tracer
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, connectionConfig)
	if err != nil {
		t.Fatal("fixture connection unavailable")
	}
	defer conn.Close(ctx)
	page := []*taskRecord{cloneLeasedRecord(original)}
	for len(page) < recoveryMediaPageRecords {
		page = append(page, &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: identity.Project.WorkspaceID, ProjectID: identity.Project.ID}})
	}
	page[0].Delivery, page[0].ReviewDispatch, page[0].MediaModeration = nil, nil, nil
	used, err := loadRecoveryMediaPhasesSQL(ctx, conn, page, recoveryMediaPageBytes)
	if err != nil || used <= 0 || tracer.selects != 5 {
		t.Fatalf("bounded read: error=%v selects=%d bytes=%d", err, tracer.selects, used)
	}
	if !reflect.DeepEqual(original.Delivery, page[0].Delivery) || !sameMediaReviewRecord(original.ReviewDispatch, page[0].ReviewDispatch) || !reflect.DeepEqual(original.MediaModeration, page[0].MediaModeration) {
		t.Fatal("page changed private delivery, dispatch, policy or candidate bytes")
	}
	if review.count("output") != 0 {
		t.Fatal("loading private metadata performed review")
	}
	before := cloneLeasedRecord(page[0])
	tracer.selects = 0
	if _, err = loadRecoveryMediaPhasesSQL(ctx, conn, page, used-1); !errors.Is(err, errRecoveryMediaPageBudget) || tracer.selects != 1 || !reflect.DeepEqual(before, page[0]) {
		t.Fatal("byte preflight failed to reject before private reads or changed caller state")
	}
	tracer.selects = 0
	if _, err = loadRecoveryMediaPhasesSQL(ctx, conn, page, used); err != nil || tracer.selects != 5 {
		t.Fatal("exact encoded budget was not accepted", err)
	}
	// The final query fails after delivery and dispatch have been decoded.
	// None of those tentative replacements may escape on a late error.
	page[0].Task.Moderation = nil
	before = cloneLeasedRecord(page[0])
	tracer.selects = 0
	if _, err = loadRecoveryMediaPhasesSQL(ctx, conn, page, recoveryMediaPageBytes); err == nil || tracer.selects != 5 || !reflect.DeepEqual(before, page[0]) {
		t.Fatal("late validation failure partially replaced private state")
	}
	if err = conn.Ping(ctx); err != nil {
		t.Fatal("failed page left transaction open", err)
	}
	// A concurrent auxiliary writer changes metadata after the byte preflight.
	// All body reads must still observe the same old snapshot as that budget.
	writer, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal("snapshot writer unavailable")
	}
	defer writer.Close(ctx)
	var writeErr error
	tracer.afterPreflight = func() {
		_, writeErr = writer.Exec(ctx, "UPDATE studio_media_deliveries SET state='PARKED',version=version+1 WHERE task_id=$1", task.ID)
	}
	page[0] = cloneLeasedRecord(original)
	if _, err = loadRecoveryMediaPhasesSQL(ctx, conn, page, recoveryMediaPageBytes); err != nil || writeErr != nil || !reflect.DeepEqual(original.Delivery, page[0].Delivery) {
		t.Fatal("page mixed preflight and concurrent metadata snapshots", err, writeErr)
	}
	tracer.afterPreflight = nil
	if _, err = loadRecoveryMediaPhasesSQL(ctx, conn, page, recoveryMediaPageBytes); err != nil || page[0].Delivery.State != "PARKED" || page[0].Delivery.Version != original.Delivery.Version+1 {
		t.Fatal("next page did not observe committed metadata", err)
	}
	// A read-only COMMIT ACK is still required before attaching private state.
	proxyURL, armed, dropped := commitAckDropProxy(t, cfg.DatabaseURL)
	proxy, err := pgx.Connect(ctx, proxyURL)
	if err != nil {
		t.Fatal("ACK proxy connection unavailable")
	}
	defer proxy.Close(ctx)
	page[0] = cloneLeasedRecord(original)
	page[0].Delivery, page[0].ReviewDispatch, page[0].MediaModeration = nil, nil, nil
	before = cloneLeasedRecord(page[0])
	armed.Store(true)
	if _, err = loadRecoveryMediaPhasesSQL(ctx, proxy, page, recoveryMediaPageBytes); err == nil || !reflect.DeepEqual(before, page[0]) {
		t.Fatal("unknown COMMIT attached private state")
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("read-only COMMIT ACK fault did not fire")
	}
}

func TestPostgresRecoveryMediaPageFull128Deliveries(t *testing.T) {
	database := testDatabase(t)
	s, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	ctx := context.Background()
	cipher, err := newMediaDeliveryCipher(coreDeliveryKey(), true, false)
	if err != nil {
		t.Fatal(err)
	}
	cipher.databaseID, err = storageOwnerID(ctx, s.conn)
	if err != nil {
		t.Fatal(err)
	}
	page := make([]*taskRecord, 0, recoveryMediaPageRecords)
	for len(page) < recoveryMediaPageRecords {
		input, now := taskInput("image"), time.Now().UTC()
		r := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ModelID: input.ModelID, ProviderMode: "demo", Status: "RUNNING", Version: 1, CreatedAt: now, UpdatedAt: now, Parameters: &input.Parameters, Artifacts: []Artifact{}}, Input: input, Handle: MediaHandle{ID: newID("handle"), Kind: "image", Count: 1}, NextPoll: now}
		if err = s.createTask(r, "ws_demo:tasks:"+newID("key"), payloadHash(input)); err != nil {
			t.Fatal(err)
		}
		r.Delivery, err = cipher.seal(r, MediaQueryResult{Result: MediaResult{Status: "SUCCEEDED"}, Items: []MediaDeliveryItem{{Slot: 0, Kind: "image", SourceURL: "https://fixture.invalid/private", ContentType: "image/png", ByteSize: 100}}})
		if err != nil {
			t.Fatal(err)
		}
		r.Task.Status = "INGESTING"
		tx, err := s.conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = saveTaskMediaDeliverySQL(ctx, tx, r); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		r.Delivery = nil
		page = append(page, r)
	}
	config, err := pgx.ParseConfig(database)
	if err != nil {
		t.Fatal("page tracer configuration")
	}
	tracer := &recoveryReadTracer{}
	config.Tracer = tracer
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("page tracer connection")
	}
	defer conn.Close(ctx)
	if _, err = loadRecoveryMediaPhasesSQL(ctx, conn, page, recoveryMediaPageBytes); err != nil {
		t.Fatal(err)
	}
	if tracer.selects != 5 {
		t.Fatalf("128 populated records required %d SELECTs, want 5", tracer.selects)
	}
	for _, r := range page {
		if r.Delivery == nil || len(r.Delivery.Items) != 1 || r.Delivery.Items[0].Receipt != nil {
			t.Fatal("full page lost a pending delivery or invented a receipt")
		}
		if _, err = cipher.open(r); err != nil {
			t.Fatal("page broke immutable encrypted binding")
		}
	}
}
