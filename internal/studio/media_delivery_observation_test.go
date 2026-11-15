package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMediaDeliveryObservationRequiresRealSourceTransfer(t *testing.T) {
	var requests atomic.Int32
	data := fixturePNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	p := deliveryTestProvider(t, server, t.TempDir(), AssetStorageConfig{}, 2)
	ctx := context.Background()
	id, item := newID("delivery"), deliveryTestItem(server, "/image")
	first, observation, err := p.ImportDeliveryItemObserved(ctx, id, item)
	if err != nil || first.StorageInfo == nil || first.StorageInfo.Delivery == nil || observation == nil || observation.Scope != "source" || observation.Class != "success" || !observation.HasServerDelay || observation.RetryNotBefore.IsZero() || requests.Load() != 1 {
		t.Fatalf("verified network delivery did not retain source evidence: %+v %v", observation, err)
	}
	replayed, observation, err := p.ImportDeliveryItemObserved(ctx, id, item)
	if err != nil || replayed.ID != first.ID || observation != nil || requests.Load() != 1 {
		t.Fatal("completed receipt replay fabricated source success", observation, err)
	}

	// This is the crash boundary after a complete local import but before its
	// delivery marker. Completing that marker must not report a new GET.
	resumedID := newID("delivery")
	if _, err = p.assets.Import(ctx, mediaDeliveryIdentity(resumedID, item.Slot), item.SourceURL, item.Kind); err != nil {
		t.Fatal(err)
	}
	resumed, observation, err := p.ImportDeliveryItemObserved(ctx, resumedID, item)
	if err != nil || resumed.StorageInfo == nil || resumed.StorageInfo.Delivery == nil || observation != nil || requests.Load() != 2 {
		t.Fatal("local import recovery fabricated source success", observation, err)
	}
}

func TestMediaDeliveryObservationPreservesSourceFailures(t *testing.T) {
	for _, test := range []struct{ name, class string }{
		{"limited", "rate_limit"}, {"bad-image", "protocol"}, {"incomplete", "temporary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch test.name {
				case "limited":
					w.Header().Set("Retry-After", "17")
					w.WriteHeader(http.StatusTooManyRequests)
				case "bad-image":
					_, _ = w.Write([]byte("not an encoded image"))
				case "incomplete":
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("short"))
				}
			}))
			defer server.Close()
			p := deliveryTestProvider(t, server, t.TempDir(), AssetStorageConfig{}, 1)
			id, item := newID("delivery"), deliveryTestItem(server, "/result")
			artifact, observation, err := p.ImportDeliveryItemObserved(context.Background(), id, item)
			if err == nil || artifact.ID != "" || observation == nil || observation.Scope != "source" || observation.Class != test.class || requests.Load() != 1 {
				t.Fatalf("source failure changed or retried: %+v %v", observation, err)
			}
			if _, found, loadErr := p.LoadDeliveryReceipt(context.Background(), id, item); loadErr != nil || found {
				t.Fatal("failed source response produced a completed receipt", loadErr)
			}
		})
	}
}
