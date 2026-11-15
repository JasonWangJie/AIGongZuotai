package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediaScheduleObservationRetryAfterFacts(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	response := &http.Response{StatusCode: 429, Header: make(http.Header)}
	response.Header.Add("Retry-After", "3")
	response.Header.Add("Retry-After", "90000")
	if o := mediaResponseObservation("gateway", response, now); !o.DelayExceeded || !o.RetryNotBefore.Equal(now.Add(25*time.Hour)) {
		t.Fatal("multiple delay headers were shortened", o)
	}
	for _, tc := range []struct {
		header        string
		has, exceeded bool
		delay         time.Duration
	}{{"", false, false, 0}, {"bad", false, false, 0}, {"-1", false, false, 0}, {"+5", false, false, 0}, {"1.5", false, false, 0}, {"0", true, false, 0}, {"7", true, false, 7 * time.Second}, {"86400", true, false, 24 * time.Hour}, {"86401", true, true, 24*time.Hour + time.Second}, {"18446744073709551616", true, true, 0}, {now.Add(4 * time.Hour).Format(http.TimeFormat), true, false, 4 * time.Hour}, {now.Add(-time.Minute).Format(http.TimeFormat), true, false, -time.Minute}, {now.Add(48 * time.Hour).Format(http.TimeFormat), true, true, 48 * time.Hour}} {
		t.Run(tc.header, func(t *testing.T) {
			o := mediaHTTPObservation("gateway", 429, tc.header, now)
			if o.Class != "rate_limit" || o.HasServerDelay != tc.has || o.DelayExceeded != tc.exceeded {
				t.Fatal(o)
			}
			if tc.has && tc.header != "18446744073709551616" && !o.RetryNotBefore.Equal(now.Add(tc.delay)) {
				t.Fatal("server deadline changed", o)
			}
			if tc.exceeded && retryAfterDuration(tc.header, now) <= 24*time.Hour {
				t.Fatal("unsupported delay shortened")
			}
		})
	}
}
func TestMediaScheduleObservationHTTPAndPrivacy(t *testing.T) {
	var status atomic.Int32
	status.Store(202)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "90000")
		w.WriteHeader(int(status.Load()))
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"task_id":"asyncimg_fixture"}`))
		} else {
			_, _ = w.Write([]byte(`{"task_id":"asyncimg_fixture","media_type":"image","stage":"queued"}`))
		}
	}))
	defer server.Close()
	p := mediaFixtureProvider(t, server)
	h, err := p.Submit(context.Background(), mediaFixtureInput("image", 1))
	if err != nil || h.Observation == nil || h.Observation.Class != "success" || !h.Observation.DelayExceeded {
		t.Fatal("accepted response lost scheduling facts", err)
	}
	raw, _ := json.Marshal(h)
	if strings.Contains(string(raw), "Observation") || strings.Contains(string(raw), "RetryNotBefore") {
		t.Fatal("observation persisted with handle")
	}
	for _, tc := range []struct {
		status int
		class  string
	}{{429, "rate_limit"}, {503, "temporary"}, {401, "identity"}, {422, "protocol"}} {
		status.Store(int32(tc.status))
		_, err = p.Submit(context.Background(), mediaFixtureInput("image", 1))
		var pe *MediaProviderError
		if !errors.As(err, &pe) || pe.Observation == nil || pe.Observation.Class != tc.class || pe.Observation.Scope != "gateway" || !pe.Observation.DelayExceeded {
			t.Fatal("POST fact classification failed", tc, err)
		}
		if tc.status == 503 && !pe.SubmissionUnknown {
			t.Fatal("503 became safe to resubmit")
		}
		_, err = p.QueryStatus(context.Background(), h)
		if !errors.As(err, &pe) || pe.Observation == nil || pe.Observation.Class != tc.class {
			t.Fatal("GET fact classification failed", tc, err)
		}
	}
	status.Store(200)
	query, err := p.QueryStatus(context.Background(), h)
	if err != nil || query.Result.Observation == nil || query.Result.Observation.Class != "success" {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(query.Result)
	if strings.Contains(string(raw), "Observation") {
		t.Fatal("result leaked observation")
	}
	if calls.Load() != 10 {
		t.Fatal("adapter automatically retried a request", calls.Load())
	}
}
func TestMediaScheduleObservationAbsentHeaderProtocolAndCancel(t *testing.T) {
	var bad atomic.Bool
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if bad.Load() {
			_, _ = w.Write([]byte("not-json"))
			return
		}
		_, _ = w.Write([]byte(`{"task_id":"asyncimg_fixture","media_type":"image","stage":"queued"}`))
	}))
	defer server.Close()
	p := mediaFixtureProvider(t, server)
	h := mediaFixtureHandle(p, "image", 1)
	q, err := p.QueryStatus(context.Background(), h)
	if err != nil || q.Result.RetryAfter != 3*time.Second || q.Result.Observation.HasServerDelay {
		t.Fatal("default poll delay became server throttle", err)
	}
	bad.Store(true)
	_, err = p.QueryStatus(context.Background(), h)
	var pe *MediaProviderError
	if !errors.As(err, &pe) || pe.Observation.Class != "protocol" {
		t.Fatal("malformed 200 response classified as success", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.QueryStatus(ctx, h)
	if !errors.As(err, &pe) || pe.Observation.Class != "cancelled" || calls.Load() != 2 {
		t.Fatal("caller cancellation penalized or contacted gateway", err, calls.Load())
	}
	h.ProviderIdentity = "changed"
	_, err = p.QueryStatus(context.Background(), h)
	if !errors.As(err, &pe) || pe.Observation.Class != "identity" || calls.Load() != 2 {
		t.Fatal("identity mismatch made a new request", err)
	}
}
func TestMediaScheduleObservationSourceAndStorageAreSeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "8"); w.WriteHeader(429) }))
	defer server.Close()
	p := mediaFixtureProvider(t, server)
	_, err := p.assets.Import(context.Background(), "source-throttle", server.URL+"/asset", "image")
	var pe *MediaProviderError
	if !errors.As(err, &pe) || pe.Observation == nil || pe.Observation.Scope != "source" || pe.Observation.Class != "rate_limit" || !pe.Observation.HasServerDelay {
		t.Fatal("source throttle became gateway fault", err)
	}
	err = assetStorageError()
	if !errors.As(err, &pe) || pe.Observation.Scope != "storage" || pe.Observation.Class != "temporary" {
		t.Fatal("storage fault became source/gateway fault")
	}
}

func TestMediaScheduleObservationReviewHTTPReportsFactsWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		class    string
		decision string
	}{{"limited", 429, "rate_limit", ""}, {"temporary", 503, "temporary", ""}, {"credentials", 401, "identity", ""}, {"allowed", 200, "success", "allow"}, {"blocked-content", 200, "success", "block"}, {"protocol", 200, "protocol", "malformed"}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				meta, _ := readMediaMultipartFixture(t, r)
				w.Header().Set("Retry-After", "90000")
				if tc.status != 200 {
					w.WriteHeader(tc.status)
					return
				}
				if tc.decision == "malformed" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"decision":"allow"}`))
					return
				}
				writeMediaResponse(w, meta, tc.decision)
			}))
			defer server.Close()
			e := httpMediaEngine(t, server.URL)
			permit, err := e.acquireReviewPermit(context.Background(), "fixture-ws")
			if err != nil {
				t.Fatal(err)
			}
			defer permit.Close()
			raw, c := mediaBytesFixture(t, "allow.mp4")
			decision, obs := permit.CheckObserved(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw))
			if obs == nil || obs.Scope != "review" || obs.Class != tc.class || !obs.HasServerDelay || !obs.DelayExceeded || calls.Load() != 1 {
				t.Fatal("review observation incorrect or retried", obs, calls.Load())
			}
			if tc.decision == "allow" && decision.State != "ALLOWED" || tc.decision == "block" && decision.State != "BLOCKED" || tc.class != "success" && decision.State != "REVIEW" {
				t.Fatal("transport facts changed content decision", decision)
			}
		})
	}
}
