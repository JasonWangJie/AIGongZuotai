package studio

import (
	"bytes"
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPostgresMediaSchedulerReferenceCancelledWaitIsUnavailableAndReplaysFirst(t *testing.T) {
	media := newDeliveryHTTPFixture(t, -1)
	review := newReviewDispatchHTTPFixture(t, "no-block")
	cfg := reviewDispatchConfig(t, media, review, true, 2)
	a, server := testApp(t, cfg)
	_, identity := deliveryIdentity(t, server, true)
	data, _ := mediaBytesFixture(t, "allow.png")
	blob, err := a.references.PutImage(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	name := "private-cancelled-reference.png"
	index := identity.Workspace.ID + ":references:cancel-original-key"
	original, replayed, err := a.acceptReferenceContext(context.Background(), identity.Workspace.ID, identity.Project.ID, name, index, blob, identity.User.ID)
	if err != nil || replayed || original.ID == "" || review.count("input") != 1 {
		t.Fatal("initial actual HTTP review and persistent reference failed", err)
	}
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	counts := func() []int {
		var values []int
		for _, table := range []string{"studio_references", "studio_reference_idempotency", "studio_reference_media_moderation", "studio_library_entries", "studio_audit_events", "studio_outbox_events"} {
			values = append(values, outboxCount(t, admin, "SELECT count(*) FROM "+table))
		}
		return values
	}
	before := counts()
	for range 2 {
		permit, err := a.mediaModeration.acquireReviewPermit(context.Background(), identity.Workspace.ID)
		if err != nil || permit == nil {
			t.Fatal("shared output reservation unavailable", err)
		}
		defer permit.Close()
	}
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
			}
			defer cancel()
			type response struct {
				asset    ReferenceAsset
				replayed bool
				err      error
			}
			done := make(chan response, 1)
			go func() {
				asset, replayed, err := a.acceptReferenceContext(ctx, identity.Workspace.ID, identity.Project.ID, name, index+"-"+mode, blob, identity.User.ID)
				done <- response{asset, replayed, err}
			}()
			until := time.Now().Add(time.Second)
			for a.mediaModeration.permitSnapshot().InputWaiting != 1 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if snapshot := a.mediaModeration.permitSnapshot(); snapshot.InputWaiting != 1 || snapshot.InFlight != 2 {
				t.Fatal("new reference never entered bounded shared waiting", snapshot)
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case result := <-done:
				if result.asset.ID != "" || result.replayed || !isFailureCode(result.err, "MEDIA_MODERATION_UNAVAILABLE") {
					t.Fatal("cancelled admission did not return explicit unavailable", result.err)
				}
				writer := httptest.NewRecorder()
				writeError(writer, result.err)
				if writer.Code != 503 || strings.Contains(writer.Body.String(), "INTERNAL_ERROR") {
					t.Fatal("caller cancellation was recorded as an internal failure")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled reference waiter did not exit")
			}
			if snapshot := a.mediaModeration.permitSnapshot(); snapshot.InputWaiting != 0 || snapshot.InFlight != 2 || review.count("input") != 1 {
				t.Fatal("cancelled waiter called reviewer or released another reservation", snapshot)
			}
			if after := counts(); !reflect.DeepEqual(before, after) {
				t.Fatal("cancelled reference changed persistent acceptance state", before, after)
			}
			// Even an already cancelled/deadline-expired caller must receive an
			// accepted original answer before current permit/context conditions.
			asset, replayed, err := a.acceptReferenceContext(ctx, identity.Workspace.ID, identity.Project.ID, name, index, blob, identity.User.ID)
			if err != nil || !replayed || asset.ID != original.ID || review.count("input") != 1 {
				t.Fatal("cancelled context or full permits overrode original replay", err)
			}
		})
	}
}
