package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// HTTP uploads exercise the production scoped permit entry point and actual
// reference transactions. The two idle output permits deliberately occupy the
// same physical pool; they never manufacture a moderation decision.
func TestPostgresMediaSchedulerReferenceQueueRejectsWithoutRegistrationAndReplaysFirst(t *testing.T) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "no-block")
	cfg := reviewDispatchConfig(t, media, review, true, 2)
	a, server := testApp(t, cfg)
	client, identity := deliveryIdentity(t, server, true)
	data, _ := mediaBytesFixture(t, "allow.png")
	const key = "fair-reference-original-key"
	code, raw, _ := uploadReferenceFixture(t, client, server.URL, identity.Project.ID, key, "private-ref.png", data, false)
	var original ReferenceAsset
	if code != 201 || json.Unmarshal(raw, &original) != nil || original.ID == "" || review.count("input") != 1 {
		t.Fatal("initial real reference check failed", code)
	}
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	count := func() []int {
		var result []int
		for _, table := range []string{"studio_references", "studio_reference_idempotency", "studio_reference_media_moderation", "studio_library_entries", "studio_audit_events", "studio_outbox_events"} {
			result = append(result, outboxCount(t, admin, "SELECT count(*) FROM "+table))
		}
		return result
	}
	before := count()
	for range 2 {
		p, err := a.mediaModeration.acquireReviewPermit(context.Background(), identity.Workspace.ID)
		if err != nil || p == nil {
			t.Fatal("output reservation unavailable", err)
		}
		t.Cleanup(p.Close)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			p, err := a.mediaModeration.acquireInputPermit(ctx, identity.Workspace.ID)
			if p != nil {
				p.Close()
			}
			done <- err
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.mediaModeration.permitSnapshot().InputWaiting != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := a.mediaModeration.permitSnapshot(); got.InputWaiting != 2 || got.InFlight != 2 {
		t.Fatal("bounded input waiters did not share the physical review pool", got)
	}
	code, raw, _ = uploadReferenceFixture(t, client, server.URL, identity.Project.ID, "fair-reference-new-key", "private-ref.png", data, false)
	if code != 429 || !strings.Contains(string(raw), "MEDIA_MODERATION_BUSY") {
		t.Fatal("queue capacity did not return an admission error", code)
	}
	if strings.Contains(string(raw), "BLOCKED") || strings.Contains(string(raw), "REVIEW") || review.count("input") != 1 {
		t.Fatal("permit pressure fabricated a content decision or called the checker")
	}
	code, raw, headers := uploadReferenceFixture(t, client, server.URL, identity.Project.ID, key, "private-ref.png", data, false)
	var replay ReferenceAsset
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || json.Unmarshal(raw, &replay) != nil || replay.ID != original.ID || review.count("input") != 1 {
		t.Fatal("full input queue blocked original accepted idempotency")
	}
	after := count()
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("rejected admission or replay wrote a new durable record", i, before, after)
		}
	}
	metrics := observedRequest(a.Handler(), "GET", "/metrics", "Bearer "+cfg.MetricsToken)
	body := metrics.Body.String()
	if metrics.Code != 200 || !strings.Contains(body, "studio_media_review_waiting{phase=\"input\"} 2\n") || !strings.Contains(body, "studio_media_review_permits 2\n") {
		t.Fatal("actual permit pressure not observable")
	}
	validatePrometheusFixture(t, body)
	for _, private := range []string{identity.Workspace.ID, identity.Project.ID, original.ID, key, "private-ref.png", review.server.URL, cfg.MediaModeration.APIKey} {
		if private != "" && strings.Contains(body, private) {
			t.Fatal("scheduling metrics leaked a private reference identity")
		}
	}
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled input waiter acquired a still-held permit")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled permit waiter leaked")
		}
	}
	if got := a.mediaModeration.permitSnapshot(); got.InputWaiting != 0 || got.InFlight != 2 {
		t.Fatal("cancellation changed somebody else's physical reservation", got)
	}
}
