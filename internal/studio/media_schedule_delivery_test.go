package studio

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresMediaScheduleSourceHTTPRecoveryUsesActualDownloads(t *testing.T) {
	f := newDeliveryHTTPFixture(t, -1)
	original := f.server.Config.Handler
	var attempts atomic.Int32
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/private-download/") && attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		original.ServeHTTP(w, r)
	})
	cfg := deliveryTestConfig(t, f, true)
	cfg.MediaScheduling.DeliveryWorkers = 2
	a, s := testApp(t, cfg)
	client, identity := deliveryIdentity(t, s, true)
	admin := deliveryAdmin(t, a.config.DatabaseURL)
	// An explicit selection can temporarily find no claimable record. Only
	// retry selection: a claimed step has already run and must never be repeated.
	step := func(id string, lane mediaExecutionLane) {
		t.Helper()
		advanceMediaScheduleDue(t, a, id)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			if a.executePersistentTaskLane(lane, id) {
				return
			}
			if !waitTaskPoll(ctx, 10*time.Millisecond) {
				t.Fatalf("%s step could not claim the task (source HTTP attempts=%d)", mediaLaneName(lane), attempts.Load())
			}
		}
	}
	requireDelivered := func(id string) {
		t.Helper()
		record, err := a.store.loadTask(id)
		if err != nil || record == nil {
			t.Fatal("cannot read completed source delivery", err)
		}
		if record.Task.Status != "SUCCEEDED" || len(record.Task.Artifacts) != 1 {
			t.Fatalf("expected one committed delivery, status=%s artifacts=%d", record.Task.Status, len(record.Task.Artifacts))
		}
	}
	first, _, _ := laneAccept(t, a, s, client, identity, "source rate limit fixture")
	step(first.ID, mediaSubmitLane)
	step(first.ID, mediaQueryLane)
	step(first.ID, mediaDeliveryLane)
	r, err := a.store.loadTask(first.ID)
	if err != nil || r.Task.Status != "INGESTING" {
		t.Fatal("source error lost original delivery", err)
	}
	channel, err := loadMediaChannelSQL(context.Background(), admin, "source", "delivery", r.Schedule.ChannelKey, false)
	if err != nil || channel.EffectiveLimit != 1 || channel.FailureCount != 1 {
		t.Fatal("source rate limit did not degrade", err, channel)
	}
	if a.executePersistentTaskStep(first.ID) || attempts.Load() != 1 {
		t.Fatal("source cooldown retried too early")
	}
	// Wait for the actual measured server/channel backoff. Test clock helpers
	// never shorten a real cooldown; they only remove the fixture's hour poll.
	wait := time.Until(channel.CooldownUntil)
	if wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	step(first.ID, mediaDeliveryLane)
	requireDelivered(first.ID)
	for i := 0; i < 7; i++ {
		task, _, _ := laneAccept(t, a, s, client, identity, "source success fixture")
		step(task.ID, mediaSubmitLane)
		step(task.ID, mediaQueryLane)
		step(task.ID, mediaDeliveryLane)
		requireDelivered(task.ID)
	}
	if attempts.Load() != 9 {
		t.Fatal("source imported unexpected byte batches", attempts.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.count != 8 || len(f.queries) != 8 || len(f.downloads) != 8 {
		t.Fatal("expected eight accepted tasks, frozen query batches, and downloaded items", f.count, len(f.queries), len(f.downloads))
	}
	for _, queries := range f.queries {
		if queries != 1 {
			t.Fatal("delivery retry replaced frozen query batch")
		}
	}
	for _, downloads := range f.downloads {
		if downloads != 1 {
			t.Fatal("expected one successful transfer per frozen item")
		}
	}
	channel, err = loadMediaChannelSQL(context.Background(), admin, "source", "delivery", r.Schedule.ChannelKey, false)
	if err != nil || channel.EffectiveLimit != 2 || channel.FailureCount != 0 || channel.SuccessCount != 0 {
		t.Fatal("eight real HTTP successes did not restore bounded source capacity", err, channel)
	}
}
