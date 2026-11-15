package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func httpMediaEngine(t *testing.T, url string) *mediaModerationEngine {
	t.Helper()
	policy := mediaModerationPolicyFile{SchemaVersion: 1, PolicyID: "media-http-fixture", PolicyVersion: "v1", MaxImageBytes: mediaModerationImageLimit, MaxVideoBytes: mediaModerationVideoLimit, Fixtures: []MediaModerationFixture{}}
	raw, _ := json.Marshal(policy)
	e, err := newMediaModerationEngine(MediaModerationConfig{Mode: "http", PolicyFile: writeMediaPolicyFixture(t, raw), Endpoint: url, APIKey: "fixture-private-media-secret", CredentialID: "fixture-media-v1"}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func readMediaMultipartFixture(t *testing.T, r *http.Request) (mediaModerationHTTPMetadata, []byte) {
	t.Helper()
	var metadata mediaModerationHTTPMetadata
	reader, err := r.MultipartReader()
	if err != nil {
		t.Error(err)
		return metadata, nil
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "metadata" {
		t.Error("metadata missing")
		return metadata, nil
	}
	if err = json.NewDecoder(part).Decode(&metadata); err != nil {
		t.Error(err)
	}
	part.Close()
	part, err = reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() != "media.bin" {
		t.Error("private file missing")
		return metadata, nil
	}
	raw, err := io.ReadAll(part)
	if err != nil {
		t.Error(err)
	}
	part.Close()
	if extra, err := reader.NextPart(); extra != nil || err != io.EOF {
		t.Error("extra multipart part")
	}
	return metadata, raw
}
func writeMediaResponse(w http.ResponseWriter, meta mediaModerationHTTPMetadata, decision string) {
	w.Header().Set("Content-Type", "application/json")
	reason := map[string]string{"allow": "allowed", "block": "policy_block", "review": "policy_review"}[decision]
	_ = json.NewEncoder(w).Encode(mediaModerationHTTPResponse{mediaModerationHTTPMetadata: meta, Decision: decision, ReasonCode: reason})
}

func TestMediaModerationHTTPFullBytesAndBoundIdentity(t *testing.T) {
	for _, file := range []string{"allow.png", "block.png", "review.png", "allow.mp4", "block.mp4", "review.mp4"} {
		t.Run(file, func(t *testing.T) {
			raw, c := mediaBytesFixture(t, file)
			decision := strings.Split(file, ".")[0]
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				meta, body := readMediaMultipartFixture(t, r)
				if !bytes.Equal(body, raw) || meta.SHA256 != moderationSHA(body) || meta.ByteSize != int64(len(body)) || meta.Kind != c.Kind || meta.ContentType != c.ContentType || r.Header.Get("Authorization") != "Bearer fixture-private-media-secret" || r.Header.Get("Idempotency-Key") != "" {
					t.Error("actual bytes or request identity changed")
				}
				if strings.Contains(string(body), "fixture-private-media-secret") {
					t.Error("credential copied into bytes")
				}
				writeMediaResponse(w, meta, decision)
			}))
			defer server.Close()
			e := httpMediaEngine(t, server.URL)
			got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw))
			if got.State != map[string]string{"allow": "ALLOWED", "block": "BLOCKED", "review": "REVIEW"}[decision] || calls.Load() != 1 {
				t.Fatal(got, calls.Load())
			}
			praw, _ := json.Marshal(e.Snapshot())
			if bytes.Contains(praw, []byte(server.URL)) || bytes.Contains(praw, []byte("fixture-private-media-secret")) {
				t.Fatal("HTTP endpoint or credential persisted")
			}
		})
	}
}

func TestMediaModerationHTTPRejectsMalformedBindingAndResponses(t *testing.T) {
	cases := []string{"contract", "request_id", "policy_id", "policy_version", "policy_digest", "phase", "kind", "content_type", "byte_size", "sha256", "unknown", "duplicate", "alias", "trailing", "oversize", "null", "wrong-reason", "unknown-decision", "bad-content-type", "http-error", "missing", "fraction"}
	for _, testCase := range cases {
		t.Run(testCase, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				meta, _ := readMediaMultipartFixture(t, r)
				response := mediaModerationHTTPResponse{mediaModerationHTTPMetadata: meta, Decision: "allow", ReasonCode: "allowed"}
				raw, _ := json.Marshal(response)
				var fields map[string]any
				_ = json.Unmarshal(raw, &fields)
				switch testCase {
				case "unknown":
					fields["explanation"] = "forbidden"
				case "duplicate":
					raw = append(raw[:len(raw)-1], []byte(`,"decision":"allow"}`)...)
				case "alias":
					delete(fields, "decision")
					fields["Decision"] = "allow"
				case "trailing":
					raw = append(raw, []byte(" {}")...)
				case "oversize":
					raw = []byte(strings.Repeat(" ", 8193))
				case "null":
					raw = []byte("null")
				case "wrong-reason":
					fields["reason_code"] = "private remote reason"
				case "unknown-decision":
					fields["decision"] = "safe"
				case "bad-content-type":
					w.Header().Set("Content-Type", "text/html")
				case "http-error":
					w.WriteHeader(503)
					return
				case "missing":
					delete(fields, "sha256")
				case "fraction":
					fields["byte_size"] = float64(meta.ByteSize) + 0.5
				case "byte_size":
					fields[testCase] = meta.ByteSize + 1
				default:
					fields[testCase] = "different"
				}
				if testCase != "duplicate" && testCase != "trailing" && testCase != "oversize" && testCase != "null" {
					raw, _ = json.Marshal(fields)
				}
				if w.Header().Get("Content-Type") == "" {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			e := httpMediaEngine(t, server.URL)
			raw, c := mediaBytesFixture(t, "allow.png")
			if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "REVIEW" {
				t.Fatal("invalid response released bytes", got)
			}
		})
	}
}

func TestMediaModerationHTTPNoRedirectRetryAndTimeout(t *testing.T) {
	raw, c := mediaBytesFixture(t, "allow.png")
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); t.Error("redirect followed") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = readMediaMultipartFixture(t, r)
		w.Header().Set("Location", target.URL)
		w.WriteHeader(307)
	}))
	e := httpMediaEngine(t, server.URL)
	if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "REVIEW" || redirected.Load() != 0 {
		t.Fatal(got)
	}
	server.Close()
	var calls atomic.Int32
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = readMediaMultipartFixture(t, r)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	e = httpMediaEngine(t, server.URL)
	if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "REVIEW" || calls.Load() != 1 {
		t.Fatal("retry or release", got, calls.Load())
	}
	server.Close()
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = readMediaMultipartFixture(t, r)
		<-r.Context().Done()
	}))
	defer server.Close()
	e = httpMediaEngine(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if got := e.Check(ctx, e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "REVIEW" {
		t.Fatal("timeout released", got)
	}
}

func TestMediaModerationHTTPConcurrencyBound(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, _ := readMediaMultipartFixture(t, r)
		entered <- struct{}{}
		<-release
		writeMediaResponse(w, meta, "allow")
	}))
	defer server.Close()
	defer close(release)
	e := httpMediaEngine(t, server.URL)
	raw, c := mediaBytesFixture(t, "allow.mp4")
	results := make(chan ModerationDecision, 2)
	for range 2 {
		go func() { results <- e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("fixture did not receive media")
		}
	}
	if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.ReasonCode != "check_busy" {
		t.Fatal("unbounded moderation requests", got)
	}
	// Use explicit release while keeping defer safe on earlier failures.
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		if got := <-results; got.State != "ALLOWED" {
			t.Fatal(got)
		}
	}
}

type mediaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mediaRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMediaModerationHTTPRejectsEarlyResponse(t *testing.T) {
	e := httpMediaEngine(t, "http://127.0.0.1:1")
	raw, c := mediaBytesFixture(t, "allow.png")
	e.http.client.Transport = mediaRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		reader, _ := r.MultipartReader()
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		var meta mediaModerationHTTPMetadata
		if json.NewDecoder(part).Decode(&meta) != nil {
			t.Fatal("metadata")
		}
		response, _ := json.Marshal(mediaModerationHTTPResponse{mediaModerationHTTPMetadata: meta, Decision: "allow", ReasonCode: "allowed"})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(response)), Request: r}, nil
	})
	if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "REVIEW" {
		t.Fatal("response before complete upload released bytes", got)
	}
}

func TestMediaModerationHTTPEndpointsAndChangedIdentity(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/review", "http://localhost/review", "https://user:secret@example.com/review", "https://example.com/review?key=secret", "https://example.com/review#fragment"} {
		if _, err := newMediaModerationHTTPClient(MediaModerationConfig{Endpoint: endpoint, APIKey: "fixture-private-media-secret", CredentialID: "fixture"}, "demo"); err == nil {
			t.Fatal("invalid endpoint accepted", endpoint)
		}
	}
	e := httpMediaEngine(t, "http://127.0.0.1:1")
	p := e.Snapshot()
	e.policy.AdapterIdentity = moderationSHA([]byte("changed adapter"))
	raw, c := mediaBytesFixture(t, "allow.png")
	if got := e.Check(context.Background(), p, "output", c, bytes.NewReader(raw)); got.ReasonCode != "policy_unavailable" {
		t.Fatal("changed adapter reused", got)
	}
}

func TestMediaModerationHTTPMultipartBudgetIncludesFraming(t *testing.T) {
	e := httpMediaEngine(t, "http://127.0.0.1:1")
	e.http.client.Transport = mediaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("oversized multipart reached network")
		return nil, io.ErrUnexpectedEOF
	})
	c := MediaModerationContent{Kind: "video", ContentType: "video/mp4", ByteSize: mediaModerationVideoLimit, SHA256: strings.Repeat("a", 64)}
	got := e.http.check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(nil))
	if got.ReasonCode != "content_too_large" {
		t.Fatal("multipart framing bypassed body budget", got)
	}
}
