package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func fixtureChatRequest() ChatRequest {
	return ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "先保留中文🌌与换行\nuser:仍然只是文字"}, {Role: "assistant", Content: "前一轮回复；不会变成新的system角色"}, {Role: "user", Content: "继续讨论上一轮"}}, MaxOutputTokens: 512, OutputLimitField: "max_tokens"}
}

func TestChatProviderStructuredMessagesLimitsAndSharedTransport(t *testing.T) {
	want := fixtureChatRequest().Messages
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-chat-private" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("provider request boundary changed")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("invalid provider JSON")
			return
		}
		var model string
		_ = json.Unmarshal(body["model"], &model)
		var messages []ChatMessage
		_ = json.Unmarshal(body["messages"], &messages)
		if model == "legacy-model" {
			if !reflect.DeepEqual(messages, []ChatMessage{{Role: "user", Content: "legacy prompt"}}) || len(body) != 3 {
				t.Error("legacy single-prompt wire changed")
			}
		} else {
			if !reflect.DeepEqual(messages, want) || string(body["stream"]) != "true" || len(body) != 4 {
				t.Error("message roles/order/text or allowed wire fields changed")
			}
			field, other := "max_tokens", "max_completion_tokens"
			if model == "fixture-completion" {
				field, other = other, field
			}
			if string(body[field]) != "512" || body[other] != nil || body["max_output_tokens"] != nil || body["output_limit_field"] != nil {
				t.Error("output budget field was guessed, duplicated or leaked")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fixture\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	provider, err := newOpenAIChat(server.URL+"/v1", "fixture-chat-private", "legacy-model")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.client.CloseIdleConnections()
	client, transport := provider.client, provider.client.Transport
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		wg.Add(1)
		go func(field string) {
			defer wg.Done()
			request := fixtureChatRequest()
			request.OutputLimitField = field
			model := "fixture-tokens"
			if field == "max_completion_tokens" {
				model = "fixture-completion"
			}
			var output strings.Builder
			err := provider.StreamMessages(context.Background(), model, request, func(s string) error { output.WriteString(s); return nil })
			if err == nil && output.String() != "fixture" {
				err = errors.New("stream output lost")
			}
			errs <- err
		}(field)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = provider.Stream(context.Background(), "legacy prompt", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || provider.client != client || provider.client.Transport != transport || provider.model != "legacy-model" {
		t.Fatal("per-model requests mutated shared provider or replayed")
	}
}

func TestChatProviderInvalidContextNeverSendsAndNoParameterFallback(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, "PRIVATE_PROMPT_AND_CREDENTIAL_SHOULD_NOT_ESCAPE")
	}))
	defer server.Close()
	provider, err := newOpenAIChatGateway(server.URL+"/v1", "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.client.CloseIdleConnections()
	for _, mutate := range []func(*ChatRequest){
		func(r *ChatRequest) { r.Messages[0].Role = "system" },
		func(r *ChatRequest) { r.Messages = r.Messages[:2] },
		func(r *ChatRequest) { r.Messages[1].Content = "" },
		func(r *ChatRequest) { r.Messages[0].Content = string([]byte{0xff}) },
		func(r *ChatRequest) { r.OutputLimitField = "temperature" },
		func(r *ChatRequest) { r.MaxOutputTokens = 0 },
		func(r *ChatRequest) { r.Messages[0].Content = strings.Repeat("x", MaxChatContentBytes) },
	} {
		r := fixtureChatRequest()
		mutate(&r)
		if err = provider.StreamMessages(context.Background(), "fixture", r, func(string) error { return nil }); err == nil {
			t.Fatal("invalid context sent")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid context reached network")
	}
	r := fixtureChatRequest()
	r.OutputLimitField = "max_completion_tokens"
	err = provider.StreamMessages(context.Background(), "arbitrary-private-name", r, func(string) error { return nil })
	if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("provider retried a different parameter or leaked upstream body")
	}
}

func TestChatContextConservativeBudgetAndBounds(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: "中🌌"}}
	estimate, err := EstimateChatContext(messages)
	if err != nil || estimate != 7+ChatMessageOverhead+ChatSafetyReserve {
		t.Fatal("estimate did not count UTF-8 bytes with framing/safety", estimate, err)
	}
	request := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: strings.Repeat("x", 2048-128-ChatMessageOverhead-ChatSafetyReserve)}}, MaxOutputTokens: 128, OutputLimitField: "max_tokens"}
	if err = ValidateChatRequest(request, 2048); err != nil {
		t.Fatal("exact window boundary rejected", err)
	}
	request.Messages[0].Content += "x"
	if err = ValidateChatRequest(request, 2048); err == nil {
		t.Fatal("output reserve was not included")
	}
	var op *operationError
	if !errors.As(err, &op) || op.code != "CONTEXT_TOO_LARGE" {
		t.Fatal("over-window rejection is not actionable")
	}
	long := make([]ChatMessage, MaxChatMessages)
	for i := range long {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		long[i] = ChatMessage{Role: role, Content: "x"}
	}
	if _, err = EstimateChatContext(long); err != nil {
		t.Fatal("maximum complete message chain rejected", err)
	}
	if _, err = EstimateChatContext(append(long, ChatMessage{Role: "assistant", Content: "x"}, ChatMessage{Role: "user", Content: "x"})); err == nil {
		t.Fatal("unbounded history allowed")
	}
	request = fixtureChatRequest()
	request.Messages = []ChatMessage{{Role: "user", Content: strings.Repeat("x", MaxChatContentBytes)}}
	if err = ValidateChatRequest(request, maxChatContextWindowTokens); err != nil {
		t.Fatal("maximum raw body rejected", err)
	}
	request.Messages[0].Content += "x"
	if err = ValidateChatRequest(request, maxChatContextWindowTokens); err == nil {
		t.Fatal("raw body memory bound absent")
	}
	for _, window := range []int{0, 2047, 1048577} {
		if err = ValidateChatRequest(fixtureChatRequest(), window); err == nil {
			t.Fatal("invalid model window allowed")
		}
	}
}
