package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresMediaScheduleLateBatch429UsesActualResponseTime(t *testing.T) {
	a, s, client, identity, database := newMediaExecutionFixture(t, true)
	var calls atomic.Int32
	responded := make(chan time.Time, 1)
	reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, _ := readMediaMultipartFixture(t, r)
		if calls.Add(1) == 2 {
			time.Sleep(3200 * time.Millisecond)
			responded <- time.Now().UTC()
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeMediaResponse(w, meta, "allow")
	}))
	defer reviewer.Close()
	a.mediaModeration = httpMediaEngine(t, reviewer.URL)
	task, _ := acceptMediaExecution(t, a, s, client, identity, "image", "two original images", newID("key"), 2)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("submit absent")
	}
	advanceMediaScheduleDue(t, a, task.ID)
	time.Sleep(5 * time.Millisecond)
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("READY handoff absent")
	}
	if !a.executePersistentTaskStep(task.ID) {
		t.Fatal("review absent")
	}
	at := <-responded
	r, err := a.store.loadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Task.Moderation.State != "REVIEW" || len(r.Task.Artifacts) != 0 || r.Task.Billing.State != "REVIEW" || calls.Load() != 2 {
		t.Fatal("late rate limit leaked partial batch or billing")
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	channel, err := loadMediaChannelSQL(context.Background(), admin, "review", "review", r.MediaModeration.Policy.AdapterIdentity, false)
	if err != nil || channel.CooldownUntil.Before(at.Add(2900*time.Millisecond)) {
		t.Fatal("cooldown anchored to earlier successful image", err, channel.CooldownUntil, at)
	}
	next, _ := acceptMediaExecution(t, a, s, client, identity, "image", "another original image", newID("key"), 1)
	a.executePersistentTaskStep(next.ID)
	advanceMediaScheduleDue(t, a, next.ID)
	time.Sleep(5 * time.Millisecond)
	a.executePersistentTaskStep(next.ID)
	if a.executePersistentTaskStep(next.ID) || calls.Load() != 2 {
		t.Fatal("another READY ignored actual reviewer cooldown")
	}
}
