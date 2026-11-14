package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const moderationFixtureSecret = "private-moderation-fixture-secret"

func httpModerationFixture(t *testing.T, handler http.HandlerFunc) (*moderationEngine, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	policy := fixtureModerationPolicy()
	policy.Rules = []ModerationRule{}
	e, err := newModerationEngine(ModerationConfig{Mode: "http", PolicyFile: writeFixtureModerationPolicy(t, policy), Endpoint: server.URL + "/text-v1", APIKey: moderationFixtureSecret, CredentialID: "fixture-credential"}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return e, server
}
func fixtureModerationReply(in moderationHTTPRequest) moderationHTTPResponse {
	return moderationHTTPResponse{Contract: in.Contract, RequestID: in.RequestID, PolicyID: in.PolicyID, PolicyVersion: in.PolicyVersion, PolicyDigest: in.PolicyDigest, Phase: in.Phase, ContentDigest: in.ContentDigest, Decision: "allow", ReasonCode: "allowed"}
}
func readModerationFixture(t *testing.T, r *http.Request) moderationHTTPRequest {
	t.Helper()
	var in moderationHTTPRequest
	if r.Method != "POST" || r.URL.Path != "/text-v1" || r.Header.Get("Authorization") != "Bearer "+moderationFixtureSecret || r.Header.Get("Idempotency-Key") != "" || json.NewDecoder(r.Body).Decode(&in) != nil {
		t.Error("private moderation contract request invalid")
	}
	return in
}

func TestModerationHTTPContractBindingAndPrivacy(t *testing.T) {
	var calls atomic.Int32
	e, server := httpModerationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		in := readModerationFixture(t, r)
		calls.Add(1)
		content, _ := json.Marshal(struct {
			Messages []ChatMessage `json:"messages,omitempty"`
			Text     string        `json:"text,omitempty"`
		}{in.Messages, in.Text})
		if in.ContentDigest != moderationSHA(content) || in.Contract != "text-v1" || !strings.HasPrefix(in.RequestID, "mreq_") {
			t.Error("content not bound")
		}
		if in.Phase == "input" && (len(in.Messages) != 3 || in.Messages[1].Role != "assistant" || in.Messages[2].Content != "最后一句🌌") {
			t.Error("complete context omitted")
		}
		if in.Phase == "output" && (in.Text != "完整结果" || len(in.Messages) != 0) {
			t.Error("output check included another body")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(fixtureModerationReply(in))
	})
	request := fixtureModerationChat("最后一句🌌")
	request.Messages = append([]ChatMessage{{Role: "user", Content: "历史提问"}, {Role: "assistant", Content: "历史回答"}}, request.Messages...)
	policy, err := e.CheckInput(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result := e.CheckOutput(context.Background(), policy, "完整结果"); result.State != "ALLOWED" {
		t.Fatal(result)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected HTTP attempts", calls.Load())
	}
	raw, _ := json.Marshal(policy)
	if strings.Contains(string(raw), server.URL) || strings.Contains(string(raw), moderationFixtureSecret) || strings.Contains(string(raw), "最后一句") {
		t.Fatal("policy persisted destination, secret or input")
	}
	changed := *e
	changed.policy = e.policy
	changed.policy.AdapterIdentity = moderationSHA([]byte("new-identity"))
	if result := changed.CheckOutput(context.Background(), policy, "完整结果"); result.ReasonCode != "policy_unavailable" || calls.Load() != 2 {
		t.Fatal("old policy sent with new credential identity")
	}
}

func TestModerationHTTPMalformedAndUnknownFailClosed(t *testing.T) {
	samples := []string{"decision", "reason", "request", "policy", "version", "policy_digest", "content_digest", "phase", "contract", "unknown_field", "case_alias", "duplicate", "trailing", "oversize", "html", "content_type_parameter", "null", "http_failure"}
	for _, sample := range samples {
		t.Run(sample, func(t *testing.T) {
			var calls atomic.Int32
			e, _ := httpModerationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				in := readModerationFixture(t, r)
				calls.Add(1)
				response := fixtureModerationReply(in)
				switch sample {
				case "decision":
					response.Decision = "unknown"
				case "reason":
					response.ReasonCode = "PRIVATE_PROVIDER_ERROR"
				case "request":
					response.RequestID = newID("mreq")
				case "policy":
					response.PolicyID = "other"
				case "version":
					response.PolicyVersion = "other"
				case "policy_digest":
					response.PolicyDigest = strings.Repeat("f", 64)
				case "content_digest":
					response.ContentDigest = strings.Repeat("f", 64)
				case "phase":
					response.Phase = "input"
				case "contract":
					response.Contract = "vendor-v2"
				}
				w.Header().Set("Content-Type", "application/json")
				if sample == "http_failure" {
					w.WriteHeader(503)
					_, _ = w.Write([]byte("PRIVATE_PROVIDER_ERROR"))
					return
				}
				raw, _ := json.Marshal(response)
				switch sample {
				case "unknown_field":
					raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"raw":"private"}`)
				case "case_alias":
					raw = []byte(strings.Replace(string(raw), `"decision"`, `"Decision"`, 1))
				case "duplicate":
					raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"decision":"allow"}`)
				case "trailing":
					raw = append(raw, []byte(` {}`)...)
				case "oversize":
					raw = append(raw, []byte(strings.Repeat(" ", 8<<10))...)
				case "html":
					w.Header().Set("Content-Type", "text/html")
				case "content_type_parameter":
					w.Header().Set("Content-Type", "application/json; unknown=parameter")
				case "null":
					raw = []byte(`null`)
				}
				_, _ = w.Write(raw)
			})
			decision := e.CheckOutput(context.Background(), e.Snapshot(), "bounded output")
			if decision.State != "REVIEW" || strings.Contains(decision.ReasonCode, "PRIVATE") || calls.Load() != 1 {
				t.Fatal("unknown allowed or retried", decision, calls.Load())
			}
		})
	}
}

func TestModerationHTTPClosedAllowBlockReview(t *testing.T) {
	for _, kind := range []struct{ decision, reason, state string }{{"allow", "allowed", "ALLOWED"}, {"block", "policy_block", "BLOCKED"}, {"review", "policy_review", "REVIEW"}} {
		e, _ := httpModerationFixture(t, func(w http.ResponseWriter, r *http.Request) {
			in := readModerationFixture(t, r)
			response := fixtureModerationReply(in)
			response.Decision, response.ReasonCode = kind.decision, kind.reason
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		})
		if result := e.CheckOutput(context.Background(), e.Snapshot(), "bounded output"); result.State != kind.state || result.ReasonCode != kind.reason {
			t.Fatal(result)
		}
	}
}

func TestModerationHTTPRedirectTimeoutAndFourSlots(t *testing.T) {
	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer destination.Close()
	e, _ := httpModerationFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) })
	if result := e.CheckOutput(context.Background(), e.Snapshot(), "redirect"); result.State != "REVIEW" || followed.Load() != 0 {
		t.Fatal("redirect followed", result)
	}
	e, _ = httpModerationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	})
	e.http.client.Timeout = 20 * time.Millisecond
	if result := e.CheckOutput(context.Background(), e.Snapshot(), "timeout"); result.State != "REVIEW" || result.ReasonCode != "check_timeout" {
		t.Fatal(result)
	}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	done := make(chan ModerationDecision, 4)
	e, _ = httpModerationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		in := readModerationFixture(t, r)
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixtureModerationReply(in))
	})
	for i := 0; i < 4; i++ {
		go func() { done <- e.CheckOutput(context.Background(), e.Snapshot(), "parallel") }()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("review slots did not start")
		}
	}
	result := e.CheckOutput(context.Background(), e.Snapshot(), "fifth")
	close(release)
	if result.State != "REVIEW" || result.ReasonCode != "check_busy" {
		t.Fatal("unbounded moderation concurrency", result)
	}
	for i := 0; i < 4; i++ {
		if result = <-done; result.State != "ALLOWED" {
			t.Fatal(result)
		}
	}
}

func TestModerationHTTPEndpointAndSecretValidation(t *testing.T) {
	cfg := ModerationConfig{Endpoint: "https://moderation.example.test/text-v1", APIKey: moderationFixtureSecret, CredentialID: "fixture"}
	if _, err := newModerationHTTPClient(cfg, "live"); err != nil {
		t.Fatal("valid configured HTTPS rejected", err)
	}
	for _, endpoint := range []string{"http://127.0.0.1:9000/text-v1", "http://localhost/text-v1", "http://10.0.0.1/text-v1", "https://user:secret@example.test/text-v1", "https://example.test/text-v1?secret=value", "https://example.test/text-v1#fragment", "file:///private", "https://example.test/%00"} {
		c := cfg
		c.Endpoint = endpoint
		if _, err := newModerationHTTPClient(c, "live"); err == nil || strings.Contains(err.Error(), endpoint) {
			t.Fatal("invalid endpoint accepted or leaked", endpoint, err)
		}
	}
	cfg.Endpoint = "http://127.0.0.1:9000/text-v1"
	if _, err := newModerationHTTPClient(cfg, "demo"); err != nil {
		t.Fatal("local fixture rejected", err)
	}
	for _, key := range []string{"short", "private secret with spaces", "private\nsecret\rheader", strings.Repeat("a", 2049)} {
		c := cfg
		c.APIKey = key
		if _, err := newModerationHTTPClient(c, "demo"); err == nil {
			t.Fatal("unsafe bearer accepted")
		}
	}
}
