package studio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func schedulePostgresFixture(t *testing.T, p MediaProvider) (*App, *pgx.Conn, string) {
	t.Helper()
	dsn := testDatabase(t)
	a, _ := testApp(t, Config{DatabaseURL: dsn, PollInterval: time.Hour, mediaProvider: p})
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return a, conn, dsn
}

func TestPostgresMediaScheduleCooldownAndStrictID(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	p := &leaseMedia{query: func(context.Context, MediaHandle) (MediaResult, error) {
		return MediaResult{}, &MediaProviderError{Code: "QUERY_TEMPORARY", Retryable: true, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "rate_limit", HasServerDelay: true, RetryNotBefore: deadline}}
	}}
	a, conn, _ := schedulePostgresFixture(t, p)
	task := leaseTask(t, a)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("first submit missing")
	}
	advanceMediaScheduleDue(t, a, task.ID)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("query missing")
	}
	r, err := a.store.loadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Task.Status != "PROVIDER_STATE_UNKNOWN" || r.Schedule.FailureCount != 1 || r.Schedule.EligibleAt.Before(deadline) || r.Schedule.ServerNotBefore.Before(deadline) {
		t.Fatal("cooldown projection missing", r.Task.Status, r.Schedule)
	}
	c, err := loadMediaChannelSQL(context.Background(), conn, "gateway", "query", r.Schedule.ChannelKey, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.CooldownUntil.Before(deadline) || !c.ProbeRequired || c.EffectiveLimit != 1 {
		t.Fatal("channel cooldown missing", c)
	}
	for range 3 {
		if a.executePersistentTaskStep(task.ID) {
			t.Fatal("explicit id bypassed server cooldown")
		}
	}
	if p.submits.Load() != 1 || p.queries.Load() != 1 {
		t.Fatal("cooling task repeated external call")
	}
	if _, err = a.reconcileTask(task.ID); err == nil {
		t.Fatal("reconcile bypassed server deadline")
	}
	if a.store.failed.Load() {
		t.Fatal("business cooldown poisoned DB")
	}
}

func TestPostgresMediaScheduleSingleProbeCompetition(t *testing.T) {
	a, conn, dsn := schedulePostgresFixture(t, &leaseMedia{})
	ids := []string{}
	for range 2 {
		task := leaseTask(t, a)
		a.executePersistentTaskStep(task.ID)
		advanceMediaScheduleDue(t, a, task.ID)
		ids = append(ids, task.ID)
	}
	s, err := loadMediaScheduleSQL(context.Background(), conn, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(context.Background(), `INSERT INTO studio_media_channel_runtime(scope,lane,channel_key,effective_limit,probe_required) VALUES('gateway','query',$1,1,true)`, s.ChannelKey); err != nil {
		t.Fatal(err)
	}
	other, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(context.Background())
	conns := []*pgx.Conn{conn, other}
	candidates := make([]*mediaScheduleCandidate, 2)
	for i := range ids {
		candidates[i], err = a.mediaScheduleCandidateByID(context.Background(), conns[i], mediaQueryLane, ids[i])
		if err != nil || candidates[i] == nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan *taskRecord, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, e := a.taskLeases.claimScheduled(context.Background(), conns[i], *candidates[i])
			results <- r
			errs <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	count := 0
	for r := range results {
		if r != nil {
			count++
			if r.ScheduleClaim.ProbeID == "" {
				t.Fatal("query probe lacks identity")
			}
		}
	}
	if count != 1 {
		t.Fatal("multiple half-open probes", count)
	}
}

func TestPostgresMediaScheduleLongDeadlineReconcile(t *testing.T) {
	deadline := time.Now().UTC().Add(30 * time.Hour)
	p := &leaseMedia{query: func(context.Context, MediaHandle) (MediaResult, error) {
		return MediaResult{}, &MediaProviderError{Code: "QUERY_TEMPORARY", Retryable: true, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "rate_limit", HasServerDelay: true, DelayExceeded: true, RetryNotBefore: deadline}}
	}}
	a, conn, _ := schedulePostgresFixture(t, p)
	task := leaseTask(t, a)
	a.executePersistentTaskStep(task.ID)
	advanceMediaScheduleDue(t, a, task.ID)
	a.executePersistentTaskStep(task.ID)
	r, err := a.store.loadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Task.Status != "RECONCILE_REQUIRED" || r.Handle.ID == "" || r.Schedule.ServerNotBefore.Before(deadline) {
		t.Fatal("long deadline shortened or handle lost")
	}
	c, err := loadMediaChannelSQL(context.Background(), conn, "gateway", "query", r.Schedule.ChannelKey, false)
	if err != nil || c.Parked || c.CooldownUntil.Before(deadline) {
		t.Fatal("irreversible or truncated channel delay", err, c)
	}
	if _, err = a.reconcileTask(task.ID); err == nil {
		t.Fatal("user reconcile bypassed deadline")
	}
	before := r.Task.Version
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		r.ScheduleReconcile = true
		r.Task.Status = "QUEUED"
		r.Task.Version++
		return saveBilledTaskSQL(ctx, tx, r)
	})
	var op *operationError
	if !errors.As(err, &op) || op.status != 429 {
		t.Fatalf("operations transaction bypassed deadline: %v", err)
	}
	fresh, _ := a.store.loadTask(task.ID)
	if fresh.Task.Version != before || fresh.Task.Status != "RECONCILE_REQUIRED" || a.store.failed.Load() {
		t.Fatal("refused reconcile changed durable resource")
	}
}

func TestPostgresMediaScheduleProjectionRollback(t *testing.T) {
	a, conn, _ := schedulePostgresFixture(t, &leaseMedia{})
	task := leaseTask(t, a)
	candidate, err := a.mediaScheduleCandidateByID(context.Background(), conn, mediaSubmitLane, task.ID)
	if err != nil || candidate == nil {
		t.Fatal(err)
	}
	r, err := a.taskLeases.claimScheduled(context.Background(), conn, *candidate)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	before, err := loadMediaScheduleSQL(context.Background(), conn, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := loadMediaChannelSQL(context.Background(), conn, "gateway", "submit", before.ChannelKey, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(context.Background(), `CREATE FUNCTION fail_schedule_channel() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'schedule fixture rollback'; END $$; CREATE TRIGGER fail_schedule_channel BEFORE UPDATE ON studio_media_channel_runtime FOR EACH ROW EXECUTE FUNCTION fail_schedule_channel()`); err != nil {
		t.Fatal(err)
	}
	r.Task.Status = "FAILED"
	r.Task.Version++
	r.LeaseComplete = true
	r.ScheduleOutcome = &mediaScheduleOutcome{ID: newID("observation"), At: time.Now().UTC(), Lane: mediaSubmitLane, ChannelKey: r.Schedule.ChannelKey, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "rate_limit"}}
	if err = a.store.saveTask(r); err == nil {
		t.Fatal("injected rollback committed")
	}
	after, err := loadMediaScheduleSQL(context.Background(), conn, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version || !after.Active {
		t.Fatal("projection escaped rollback")
	}
	if got := readLeasedTask(t, conn, task.ID); got.Status != "QUEUED" || got.Version != task.Version {
		t.Fatal("business escaped rollback")
	}
	channel, err := loadMediaChannelSQL(context.Background(), conn, "gateway", "submit", before.ChannelKey, false)
	if err != nil || channel.Version != baseline.Version || channel.FailureCount != baseline.FailureCount || !channel.CooldownUntil.Equal(baseline.CooldownUntil) || channel.EffectiveLimit != baseline.EffectiveLimit {
		t.Fatal("channel observation escaped rollback", err)
	}
}

func TestMediaScheduleUnrepresentableDelayIsNotPermanentChannelPark(t *testing.T) {
	o := &mediaScheduleOutcome{At: time.Now().UTC(), Lane: mediaQueryLane, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "rate_limit", HasServerDelay: true, DelayExceeded: true}}
	r := applyMediaChannelObservation(mediaChannelRuntime{EffectiveLimit: 2}, o, nil, 2)
	if r.Parked || r.CooldownUntil.After(o.At.Add(time.Minute)) || !r.ProbeRequired {
		t.Fatal("invalid protocol delay permanently blocked unrelated work")
	}
	task := &taskRecord{Task: Task{Status: "RUNNING"}, Handle: MediaHandle{ID: "original"}, ScheduleOutcome: o}
	applyMediaScheduleExceeded(task)
	if task.Task.Status != "RECONCILE_REQUIRED" || task.Handle.ID != "original" {
		t.Fatal("invalid delay did not stop automatic task query")
	}
}

func TestPostgresMediaScheduleCursorUnknownCommitDoesNotClaim(t *testing.T) {
	p := &leaseMedia{}
	a, admin, dsn := schedulePostgresFixture(t, p)
	task := leaseTask(t, a)
	proxy, armed, dropped := commitAckDropProxy(t, dsn)
	conn, err := pgx.Connect(context.Background(), proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	armed.Store(true)
	candidate, err := a.selectMediaScheduleCandidate(context.Background(), conn, mediaSubmitLane)
	if err == nil || candidate != nil {
		t.Fatal("uncertain cursor supplied executable candidate")
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("cursor COMMIT acknowledgement not dropped")
	}
	var cursor, owner string
	if err = admin.QueryRow(context.Background(), `SELECT workspace_cursor FROM studio_lane_schedule WHERE lane='submit' AND provider_mode='demo'`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(context.Background(), `SELECT lease_owner FROM studio_tasks WHERE id=$1`, task.ID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if cursor != "ws_demo" || owner != "" || p.submits.Load() != 0 {
		t.Fatal("cursor ambiguity leased or sent work")
	}
	if !a.executePersistentTaskStep(task.ID) || p.submits.Load() != 1 {
		t.Fatal("confirmed fresh selection could not resume")
	}
}

func TestPostgresMediaScheduleClaimUnknownCommitDoesNotSend(t *testing.T) {
	p := &leaseMedia{}
	a, admin, dsn := schedulePostgresFixture(t, p)
	task := leaseTask(t, a)
	candidate, err := a.mediaScheduleCandidateByID(context.Background(), admin, mediaSubmitLane, task.ID)
	if err != nil || candidate == nil {
		t.Fatal(err)
	}
	proxy, armed, dropped := commitAckDropProxy(t, dsn)
	conn, err := pgx.Connect(context.Background(), proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	armed.Store(true)
	record, err := a.taskLeases.claimScheduled(context.Background(), conn, *candidate)
	if err == nil || record != nil {
		t.Fatal("uncertain claim returned executable work")
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("claim COMMIT acknowledgement not dropped")
	}
	var owner string
	if err = admin.QueryRow(context.Background(), `SELECT lease_owner FROM studio_tasks WHERE id=$1`, task.ID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner == "" || p.submits.Load() != 0 {
		t.Fatal("unknown claim sent work or did not commit")
	}
	if a.executePersistentTaskStep(task.ID) {
		t.Fatal("unexpired uncertain claim stolen")
	}
	expireTaskLease(t, admin, task.ID)
	if !a.executePersistentTaskStep(task.ID) || p.submits.Load() != 1 {
		t.Fatal("never-sent QUEUED could not get its first submit")
	}
}

func TestPostgresMediaScheduleOutboxBackpressureKeepsObservation(t *testing.T) {
	var admin *pgx.Conn
	queried := make(chan struct{})
	deadline := time.Now().UTC().Add(time.Minute)
	p := &leaseMedia{query: func(context.Context, MediaHandle) (MediaResult, error) {
		_, e := admin.Exec(context.Background(), `UPDATE studio_outbox_limits SET pending=$1 WHERE singleton`, outboxPendingLimit)
		close(queried)
		if e != nil {
			return MediaResult{}, e
		}
		return MediaResult{}, &MediaProviderError{Code: "QUERY_TEMPORARY", Retryable: true, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "rate_limit", HasServerDelay: true, RetryNotBefore: deadline}}
	}}
	a, conn, _ := schedulePostgresFixture(t, p)
	admin = conn
	task := leaseTask(t, a)
	a.executePersistentTaskStep(task.ID)
	advanceMediaScheduleDue(t, a, task.ID)
	if _, err := admin.Exec(context.Background(), `CREATE SEQUENCE schedule_observation_probe; CREATE FUNCTION schedule_observation_probe_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('schedule_observation_probe');RETURN NEW;END $$; CREATE TRIGGER schedule_observation_probe_tr BEFORE UPDATE ON studio_media_channel_runtime FOR EACH ROW EXECUTE FUNCTION schedule_observation_probe_fn()`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); a.executePersistentTaskStep(task.ID) }()
	defer func() {
		a.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("schedule worker failed to drain")
		}
	}()
	select {
	case <-queried:
	case <-time.After(5 * time.Second):
		t.Fatal("query not entered")
	}
	until := time.Now().Add(5 * time.Second)
	for {
		var called bool
		if err := admin.QueryRow(context.Background(), `SELECT is_called FROM schedule_observation_probe`).Scan(&called); err != nil {
			t.Fatal(err)
		}
		if called {
			break
		}
		if time.Now().After(until) {
			t.Fatal("observation transaction not attempted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.Lock()
	snapshot := a.mediaScheduler.snapshot()
	a.mu.Unlock()
	if snapshot.Lanes[mediaQueryLane].ScopeInFlight != 1 || p.queries.Load() != 1 {
		t.Fatal("backpressure released or repeated scope")
	}
	projection, e := loadMediaScheduleSQL(context.Background(), admin, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	healthy, e := loadMediaChannelSQL(context.Background(), admin, "gateway", "query", projection.ChannelKey, false)
	if e != nil || healthy.Version != 1 || healthy.FailureCount != 0 || healthy.ProbeRequired || healthy.CooldownUntil.After(time.Unix(0, 0)) {
		t.Fatal("uncommitted observation altered healthy claim baseline", e)
	}
	if _, err := admin.Exec(context.Background(), `UPDATE studio_outbox_limits SET pending=(SELECT count(*) FROM studio_outbox_events WHERE projected_at IS NULL) WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("observation did not commit after backlog cleared")
	}
	r, err := a.store.loadTask(task.ID)
	if err != nil || r.Schedule.FailureCount != 1 || r.Schedule.EligibleAt.Before(deadline) || p.queries.Load() != 1 {
		t.Fatal("retry changed observation or repeated GET", err)
	}
}

func TestPostgresMediaScheduleObservationUnknownCommitSurvivesRestart(t *testing.T) {
	dsn := testDatabase(t)
	proxy, armed, dropped := commitAckDropProxy(t, dsn)
	s, err := openPostgres(proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	cfg := Config{DatabaseURL: dsn, PollInterval: time.Hour, AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), mediaProvider: &leaseMedia{}}
	cfg.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &App{store: s, config: cfg, ctx: ctx, cancel: cancel, media: cfg.mediaProvider, tasks: map[string]*taskRecord{}, workspaceSubscribers: map[chan event]subscriptionScope{}}
	if err = a.initTaskLeases(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.taskLeases.close)
	in := taskInput("image")
	now := time.Now().UTC()
	task := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", Kind: "image", ModelID: in.ModelID, Status: "RUNNING", Version: 1, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{}}, Input: in, Handle: MediaHandle{ID: "original-handle", Kind: "image"}, NextPoll: now}
	key := newID("key")
	if err = s.createTask(task, "ws_demo:tasks:"+key, payloadHash(in)); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	candidate, err := a.mediaScheduleCandidateByID(ctx, admin, mediaQueryLane, task.Task.ID)
	if err != nil || candidate == nil {
		t.Fatal(err)
	}
	r, err := a.taskLeases.claimScheduled(ctx, admin, *candidate)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	a.tasks[r.Task.ID] = cloneLeasedRecord(r)
	deadline := now.Add(time.Minute)
	r.Task.Status = "PROVIDER_STATE_UNKNOWN"
	r.Task.Version++
	a.observeMediaStep(r, MediaResult{}, &MediaProviderError{Code: "QUERY_TEMPORARY", Retryable: true, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "rate_limit", HasServerDelay: true, RetryNotBefore: deadline}})
	armed.Store(true)
	if err = a.persistTaskClaim(r, true); err == nil || !s.failed.Load() {
		t.Fatal("lost observation COMMIT did not stop writer")
	}
	select {
	case <-dropped:
	case <-time.After(time.Second):
		t.Fatal("observation COMMIT not dropped")
	}
	if a.tasks[r.Task.ID].Task.Version != 1 {
		t.Fatal("unconfirmed projection replaced cache")
	}
	stored, err := loadMediaScheduleSQL(ctx, admin, r.Task.ID)
	if err != nil || stored.EligibleAt.Before(deadline) || stored.ServerNotBefore.Before(deadline) {
		t.Fatal("committed cooldown missing", err)
	}
	a.taskLeases.close()
	s.close()
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.executePersistentTaskStep(r.Task.ID) {
		t.Fatal("restart ignored original server delay")
	}
	p := cfg.mediaProvider.(*leaseMedia)
	if p.submits.Load() != 0 || p.queries.Load() != 0 {
		t.Fatal("restart repeated original external operation")
	}
	got, err := restarted.store.loadTask(r.Task.ID)
	if err != nil || got.Task.Status != "PROVIDER_STATE_UNKNOWN" || got.Handle.ID != "original-handle" {
		t.Fatal("restart changed original accepted identity", err)
	}
}
