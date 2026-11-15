package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func schedulerStepAsync(a *App, lane mediaExecutionLane, id string) <-chan bool {
	done := make(chan bool, 1)
	go func() { done <- a.executePersistentTaskLane(lane, id) }()
	return done
}
func schedulerStepDone(t *testing.T, done <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-done:
		if got != want {
			t.Fatalf("scheduler step returned %t, want %t", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler step did not exit")
	}
}
func schedulerDueFixture(t *testing.T, conn *pgx.Conn, id string) {
	t.Helper()
	// Explicit simulated eligibility only; the production trigger runs and the
	// independent persisted channel cooldown is deliberately left untouched.
	if _, err := conn.Exec(context.Background(), `UPDATE studio_tasks SET next_poll=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMediaSchedulerWorkspaceFairnessAndCancelKeepsBudgets(t *testing.T) {
	p := newMediaLaneProvider()
	cfg := Config{DatabaseURL: testDatabase(t), AuthMode: "required", PollInterval: time.Hour, mediaProvider: p, MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 3, QueryWorkers: 1, WorkspaceConcurrency: 1, ChannelConcurrency: 2}}
	a, s := testApp(t, cfg)
	t.Cleanup(func() { close(p.stop) })
	clients := []*http.Client{authClient(t), authClient(t), authClient(t)}
	ids := []testIdentity{registerFixture(t, clients[0], s.URL, "scheduler-a@example.test"), registerFixture(t, clients[1], s.URL, "scheduler-b@example.test"), registerFixture(t, clients[2], s.URL, "scheduler-c@example.test")}
	var backlog []Task
	var original TaskInput
	var key string
	for i := 0; i < 12; i++ {
		task, input, k := laneAccept(t, a, s, clients[0], ids[0], "private scheduler A")
		backlog = append(backlog, task)
		if i == 0 {
			original, key = input, k
		}
	}
	bt, _, _ := laneAccept(t, a, s, clients[1], ids[1], "private scheduler B")
	ct, _, _ := laneAccept(t, a, s, clients[2], ids[2], "private scheduler C")
	firstDone := schedulerStepAsync(a, mediaSubmitLane, backlog[0].ID)
	first := laneCall(t, p.submits, "private scheduler A")
	if a.executePersistentTaskLane(mediaSubmitLane, backlog[1].ID) {
		t.Fatal("same workspace exceeded one executing submit")
	}
	// While A is blocked, production keyset selection must reach B or C even
	// if it initially chooses another A task whose local scope is full.
	var second *mediaLaneCall
	var secondDone <-chan bool
	for tick := 0; tick < 6 && second == nil; tick++ {
		done := schedulerStepAsync(a, mediaSubmitLane, "")
		select {
		case call := <-p.submits:
			second = call
			secondDone = done
		case worked := <-done:
			if worked {
				t.Fatal("step claimed without entering controlled provider")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("workspace rotation did not return")
		}
	}
	if second == nil || second.prompt == "private scheduler A" {
		t.Fatal("deep A queue starved a different workspace")
	}
	other, otherIdentity, otherClient := ct, ids[2], clients[2]
	if second.prompt == "private scheduler C" {
		other, otherIdentity, otherClient = bt, ids[1], clients[1]
	}
	if a.executePersistentTaskLane(mediaSubmitLane, other.ID) {
		t.Fatal("same frozen channel exceeded two execution slots")
	}
	laneCancel(t, s, clients[0], ids[0], backlog[0].ID)
	if got := a.mediaScheduler.snapshot().Lanes[mediaSubmitLane].ScopeInFlight; got != 2 {
		t.Fatal("cancel released provider scope before actual return", got)
	}
	if a.executePersistentTaskLane(mediaSubmitLane, backlog[1].ID) || a.executePersistentTaskLane(mediaSubmitLane, other.ID) {
		t.Fatal("late cancelled provider stopped occupying workspace/channel")
	}
	code, raw, headers := authRequest(t, clients[0], s.URL, "POST", "/api/v1/tasks", key, ids[0].Project.ID, original)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != backlog[0].ID {
		t.Fatal("scope pressure changed original idempotency", code)
	}
	second.finish()
	schedulerStepDone(t, secondDone, true)
	thirdDone := schedulerStepAsync(a, mediaSubmitLane, other.ID)
	third := laneCall(t, p.submits, "private scheduler "+map[bool]string{true: "B", false: "C"}[other.ID == bt.ID])
	laneCancel(t, s, otherClient, otherIdentity, other.ID)
	first.finish()
	schedulerStepDone(t, firstDone, true)
	third.finish()
	schedulerStepDone(t, thirdDone, true)
	if got := deliveryRead(t, a, backlog[0].ID); got.Status != "CANCELLED" || len(got.Artifacts) != 0 {
		t.Fatal("late provider return replaced cancelled task")
	}
	if got := a.mediaScheduler.snapshot().Lanes[mediaSubmitLane].ScopeInFlight; got != 0 {
		t.Fatal("exited providers retained scoped permits", got)
	}
	p.mu.Lock()
	peak := p.peakSubmit
	p.mu.Unlock()
	if peak != 2 {
		t.Fatal("real provider peak did not match shared channel cap", peak)
	}
}

func TestPostgresMediaSchedulerHTTP429CooldownSurvivesRestart(t *testing.T) {
	fixture := newDeliveryHTTPFixture(t, -1)
	fixture.server.Close()
	var posts, queries atomic.Int32
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			t.Error("missing private fixture credential")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(202)
			_, _ = w.Write(mediaFixture(t, "accepted-image.json"))
			return
		}
		if queries.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"fixture rate limit"}}`))
			return
		}
		_, _ = w.Write(mediaFixture(t, "generating.json"))
	}))
	cfg := deliveryTestConfig(t, fixture, true)
	a, s := testApp(t, cfg)
	client, id := deliveryIdentity(t, s, true)
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	task, body, key := laneAccept(t, a, s, client, id, "private persisted HTTP cooldown")
	if !a.executePersistentTaskLane(mediaSubmitLane, task.ID) {
		t.Fatal("initial POST not executed")
	}
	schedulerDueFixture(t, admin, task.ID)
	if !a.executePersistentTaskLane(mediaQueryLane, task.ID) {
		t.Fatal("initial GET not executed")
	}
	before, err := a.store.loadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	frozenSubmission, _ := json.Marshal(before.Submission)
	frozenHandle, _ := json.Marshal(before.Handle)
	schedule, err := loadMediaScheduleSQL(context.Background(), admin, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := loadMediaChannelSQL(context.Background(), admin, "gateway", "query", schedule.ChannelKey, false)
	if err != nil || channel.FailureCount != 1 || !channel.ProbeRequired || !channel.CooldownUntil.After(time.Now()) || schedule.FailureCount != 1 {
		t.Fatal("429 did not persist retry/channel facts", err, channel)
	}
	if posts.Load() != 1 || queries.Load() != 1 {
		t.Fatal("unexpected initial HTTP request count")
	}
	a.Close()
	s.Close()
	cfg.mediaProvider = fixture.provider(t, cfg.AssetDir)
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	server := httptest.NewServer(restarted.Handler())
	t.Cleanup(server.Close)
	afterSchedule, err := loadMediaScheduleSQL(context.Background(), admin, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterChannel, err := loadMediaChannelSQL(context.Background(), admin, "gateway", "query", schedule.ChannelKey, false)
	if err != nil || !reflect.DeepEqual(schedule, afterSchedule) || !sameMediaReviewTime(&channel.CooldownUntil, &afterChannel.CooldownUntil) || afterChannel.Version != channel.Version {
		t.Fatal("restart shortened or reset persisted cooldown", err)
	}
	// A targeted worker cannot bypass the original due time, nor the separate
	// channel wait if a test moves only this task's eligibility into the past.
	if restarted.executePersistentTaskLane(mediaQueryLane, task.ID) {
		t.Fatal("explicit id bypassed persisted next due")
	}
	schedulerDueFixture(t, admin, task.ID)
	if restarted.executePersistentTaskLane(mediaQueryLane, task.ID) || queries.Load() != 1 {
		t.Fatal("explicit id bypassed persisted cooldown")
	}
	code, raw, headers := authRequest(t, client, server.URL, "POST", "/api/v1/tasks", key, id.Project.ID, body)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != task.ID {
		t.Fatal("cooldown changed original replay", code)
	}
	wait := time.Until(channel.CooldownUntil) + 20*time.Millisecond
	if wait > 5*time.Second {
		t.Fatal("fixture cooldown unexpectedly exceeds bounded test duration")
	}
	if wait > 0 {
		time.Sleep(wait)
	}
	if !restarted.executePersistentTaskLane(mediaQueryLane, task.ID) {
		t.Fatal("due original GET probe did not execute")
	}
	if posts.Load() != 1 || queries.Load() != 2 {
		t.Fatal("probe regenerated or duplicated upstream request")
	}
	recovered, err := restarted.store.loadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotSubmission, _ := json.Marshal(recovered.Submission)
	gotHandle, _ := json.Marshal(recovered.Handle)
	if string(gotSubmission) != string(frozenSubmission) || string(gotHandle) != string(frozenHandle) {
		t.Fatal("restart/probe changed original body or handle")
	}
	afterChannel, err = loadMediaChannelSQL(context.Background(), admin, "gateway", "query", schedule.ChannelKey, false)
	if err != nil || afterChannel.ProbeRequired || afterChannel.ProbeID != "" || afterChannel.CooldownUntil.After(time.Now()) {
		t.Fatal("successful current probe did not close cooldown", err, afterChannel)
	}
}
