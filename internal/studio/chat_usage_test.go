package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const reportedUsageFixture = `{"prompt_tokens":23,"completion_tokens":11,"total_tokens":34}`

func usageSSE(raw string) string { return "data: {\"choices\":[],\"usage\":" + raw + "}\n\n" }

func TestChatUsageFullFragmentedStreamAndRepeatedTotals(t *testing.T) {
	fixture := "data: {\"choices\":[{\"delta\":{\"content\":\"答案\"},\"finish_reason\":null}],\"usage\":null}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}],\"usage\":null}\n\n" + usageSSE(reportedUsageFixture) + usageSSE(reportedUsageFixture) + "data: [DONE]\n\n"
	var output strings.Builder
	usage, err := parseOpenAIStreamUsage(&fragmentedReader{source: fixture}, func(s string) error { output.WriteString(s); return nil })
	if err != nil || output.String() != "答案" || usage.Status != "reported" || usage.Source != "gateway" || !usage.Complete || usage.PromptTokens != 23 || usage.CompletionTokens != 11 || usage.TotalTokens != 34 || usage.Reason != "" {
		t.Fatalf("unexpected complete usage: %+v err=%v", usage, err)
	}
	// Cached/reasoning breakdowns are subsets, never added a second time.
	withDetails := `{"prompt_tokens":23,"completion_tokens":11,"total_tokens":34,"prompt_tokens_details":{"cached_tokens":9},"completion_tokens_details":{"reasoning_tokens":7}}`
	usage, err = parseOpenAIStreamUsage(strings.NewReader(usageSSE(withDetails)+"data: [DONE]\n\n"), func(string) error { return nil })
	if err != nil || usage.TotalTokens != 34 || usage.Status != "reported" {
		t.Fatal("nested token details were treated as additive units")
	}
}

func TestChatUsageRejectsInvalidCountsAndAmbiguity(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"missing", `{"prompt_tokens":1,"total_tokens":1}`},
		{"null", `{"prompt_tokens":null,"completion_tokens":0,"total_tokens":0}`},
		{"negative", `{"prompt_tokens":-1,"completion_tokens":1,"total_tokens":0}`},
		{"fraction", `{"prompt_tokens":1.0,"completion_tokens":1,"total_tokens":2}`},
		{"exponent", `{"prompt_tokens":1e2,"completion_tokens":1,"total_tokens":101}`},
		{"string", `{"prompt_tokens":"1","completion_tokens":1,"total_tokens":2}`},
		{"overflow", `{"prompt_tokens":9223372036854775808,"completion_tokens":1,"total_tokens":1}`},
		{"overbound", `{"prompt_tokens":1000000001,"completion_tokens":0,"total_tokens":1000000001}`},
		{"sum", `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":3}`},
		{"duplicate", `{"prompt_tokens":1,"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}`},
		{"array", `[1,2,3]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, err := parseOpenAIStreamUsage(strings.NewReader(usageSSE(tc.raw)+usageSSE(reportedUsageFixture)+"data: [DONE]\n\n"), func(string) error { return nil })
			if err != nil || usage.Status != "invalid" || usage.Reason != "usage_invalid" || !usage.Complete {
				t.Fatalf("invalid usage was accepted/overwritten: %+v err=%v", usage, err)
			}
		})
	}
	for _, fixture := range []string{
		usageSSE(reportedUsageFixture) + usageSSE(`{"prompt_tokens":24,"completion_tokens":11,"total_tokens":35}`),
		"data: {\"choices\":[],\"usage\":" + reportedUsageFixture + ",\"usage\":" + reportedUsageFixture + "}\n\n",
	} {
		usage, err := parseOpenAIStreamUsage(strings.NewReader(fixture+"data: [DONE]\n\n"), func(string) error { return nil })
		if err != nil || usage.Status != "invalid" || usage.Reason != "usage_conflict" || !usage.Complete {
			t.Fatalf("ambiguous usage accepted: %+v err=%v", usage, err)
		}
	}
	usage, err := parseOpenAIStreamUsage(strings.NewReader(usageSSE(`{"prompt_tokens":1000000000,"completion_tokens":0,"total_tokens":1000000000}`)+"data: [DONE]\n\n"), func(string) error { return nil })
	if err != nil || usage.Status != "reported" || usage.TotalTokens != maxReportedChatTokens {
		t.Fatal("documented integer bound rejected")
	}
}

func TestChatUsageMissingPartialAndEmitFailureNeverComplete(t *testing.T) {
	for _, fixture := range []string{"data: [DONE]\n\n", usageSSE("null") + "data: [DONE]\n\n"} {
		usage, err := parseOpenAIStreamUsage(strings.NewReader(fixture), func(string) error { return nil })
		if err != nil || usage.Status != "missing" || usage.Reason != "usage_missing" || !usage.Complete {
			t.Fatalf("complete missing usage conflated with zero usage: %+v", usage)
		}
	}
	usage, err := parseOpenAIStreamUsage(strings.NewReader(usageSSE(reportedUsageFixture)), func(string) error { return nil })
	if err == nil || usage.Status != "missing" || usage.Reason != "stream_incomplete" || usage.Complete || usage.TotalTokens != 34 {
		t.Fatal("partial reported usage became billable or evidence lost")
	}
	emitErr := errors.New("fixture output stopped")
	usage, err = parseOpenAIStreamUsage(strings.NewReader(usageSSE(reportedUsageFixture)+"data: {\"choices\":[{\"delta\":{\"content\":\"text\"}}]}\n\ndata: [DONE]\n\n"), func(string) error { return emitErr })
	if !errors.Is(err, emitErr) || usage.Status != "missing" || usage.Complete || usage.TotalTokens != 34 {
		t.Fatal("downstream cancellation was treated as settled usage")
	}
}

func TestChatUsageProviderRequestsExplicitUsageWithoutChangingLegacy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid usage request")
			return
		}
		if call == 1 {
			var options map[string]bool
			if json.Unmarshal(body["stream_options"], &options) != nil || len(options) != 1 || !options["include_usage"] {
				t.Error("usage was not explicitly requested")
			}
		} else if body["stream_options"] != nil {
			t.Error("legacy call acquired new optional gateway parameters")
		}
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("local billing key escaped to gateway")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, usageSSE(reportedUsageFixture)+"data: [DONE]\n\n")
	}))
	defer server.Close()
	p, err := newOpenAIChatGateway(server.URL+"/v1", "local-usage-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	client, transport := p.client, p.client.Transport
	usage, err := p.StreamMessagesUsage(context.Background(), "fixture", fixtureChatRequest(), func(string) error { return nil })
	if err != nil || usage.Status != "reported" || !usage.Complete {
		t.Fatalf("metered request failed: %+v %v", usage, err)
	}
	if err = p.StreamMessages(context.Background(), "fixture", fixtureChatRequest(), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = p.StreamModel(context.Background(), "fixture", "legacy", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || p.client != client || p.client.Transport != transport {
		t.Fatal("usage path changed transport ownership or repeated a request")
	}
}

func TestChatUsageProviderFailuresNeverFallbackOrExposeBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, "PRIVATE_PROVIDER_TEXT_AND_SECRET")
	}))
	defer server.Close()
	p, err := newOpenAIChatGateway(server.URL+"/v1", "private-fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	usage, err := p.StreamMessagesUsage(context.Background(), "fixture", fixtureChatRequest(), func(string) error { return nil })
	if err == nil || calls.Load() != 1 || usage.Complete || usage.Status != "missing" || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "private-fixture-key") {
		t.Fatal("usage failure was retried, settled or leaked")
	}
	bad := fixtureChatRequest()
	bad.MaxOutputTokens = 0
	usage, err = p.StreamMessagesUsage(context.Background(), "fixture", bad, func(string) error { return nil })
	if err == nil || calls.Load() != 1 || usage.Reason != "request_not_started" {
		t.Fatal("invalid request reached gateway")
	}
}

func TestChatUsageProviderCancellationAndSyntheticDemo(t *testing.T) {
	flushed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, usageSSE(reportedUsageFixture))
		w.(http.Flusher).Flush()
		close(flushed)
		<-r.Context().Done()
	}))
	defer server.Close()
	p, err := newOpenAIChatGateway(server.URL+"/v1", "local-usage-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-flushed; cancel() }()
	usage, err := p.StreamMessagesUsage(ctx, "fixture", fixtureChatRequest(), func(string) error { return nil })
	if err == nil || usage.Complete || usage.Status != "missing" || usage.Reason != "stream_incomplete" {
		t.Fatal("cancelled network stream became complete usage")
	}
	demo := demoChat{delay: time.Nanosecond}
	for _, text := range []string{"短", strings.Repeat("very different bytes 🌌", 400)} {
		request := fixtureChatRequest()
		request.Messages = []ChatMessage{{Role: "user", Content: text}}
		usage, err = demo.StreamMessagesUsage(context.Background(), "fixture", request, func(string) error { return nil })
		if err != nil || usage.Status != "demo" || usage.Source != "demo" || !usage.Complete || usage.PromptTokens != 128 || usage.CompletionTokens != 64 || usage.TotalTokens != 192 {
			t.Fatal("demo units changed with text or masqueraded as real usage")
		}
	}
	usage, err = demo.StreamMessagesUsage(ctx, "fixture", fixtureChatRequest(), func(string) error { return nil })
	if err == nil || usage.Complete || usage.Status != "missing" {
		t.Fatal("cancelled demo was settled")
	}
}
