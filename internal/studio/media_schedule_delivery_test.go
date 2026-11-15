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
	first, _, _ := laneAccept(t, a, s, client, identity, "source rate limit fixture")
	deliveryStep(t, a, first.ID)
	deliveryStep(t, a, first.ID)
	deliveryStep(t, a, first.ID)
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
	deliveryStep(t, a, first.ID)
	for i := 0; i < 7; i++ {
		task, _, _ := laneAccept(t, a, s, client, identity, "source success fixture")
		deliveryStep(t, a, task.ID)
		deliveryStep(t, a, task.ID)
		deliveryStep(t, a, task.ID)
	}
	channel, err = loadMediaChannelSQL(context.Background(), admin, "source", "delivery", r.Schedule.ChannelKey, false)
	if err != nil || channel.EffectiveLimit != 2 || channel.FailureCount != 0 || channel.SuccessCount != 0 {
		t.Fatal("eight real HTTP successes did not restore bounded source capacity", err, channel)
	}
	if attempts.Load() != 9 {
		t.Fatal("source imported unexpected byte batches", attempts.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.count != 8 {
		t.Fatal("delivery retry repeated POST")
	}
	for _, queries := range f.queries {
		if queries != 1 {
			t.Fatal("delivery retry replaced frozen query batch")
		}
	}
}
