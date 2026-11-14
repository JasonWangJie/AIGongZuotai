package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOpenAIModelsShareGatewayAndKeepLegacyDefault(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 || body.Model != body.Messages[0].Content {
			t.Error("concurrent operation used another operation's model")
			w.WriteHeader(400)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("model routing changed the gateway credential boundary")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\ndata: [DONE]\n\n", body.Model)
	}))
	defer server.Close()
	provider, err := newOpenAIChat(server.URL+"/v1", "fixture-key", "legacy-model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.client.CloseIdleConnections)
	client, transport := provider.client, provider.client.Transport
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("private-model-%d", i)
			var output strings.Builder
			if err := provider.StreamModel(context.Background(), model, model, func(s string) error { output.WriteString(s); return nil }); err != nil || output.String() != model {
				t.Errorf("incorrect explicit stream: output=%q err=%v", output.String(), err)
			}
		}(i)
	}
	wg.Wait()
	if err := provider.Stream(context.Background(), "legacy-model", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if provider.model != "legacy-model" || provider.client != client || provider.client.Transport != transport || calls.Load() != 17 {
		t.Fatal("per-request routing mutated legacy configuration or created a new client")
	}
}

func TestOpenAIModelGatewayReusesConnectionWithoutDefault(t *testing.T) {
	addresses := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addresses <- r.RemoteAddr
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider, err := newOpenAIChatGateway(server.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.client.CloseIdleConnections)
	for _, model := range []string{"model-a", "model-b"} {
		if err := provider.StreamModel(context.Background(), model, "fixture", func(string) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if first, second := <-addresses, <-addresses; first != second {
		t.Fatal("different models did not reuse the gateway connection")
	}
	if provider.model != "" {
		t.Fatal("catalog gateway gained a mutable default model")
	}
	if err := provider.Stream(context.Background(), "fixture", func(string) error { return nil }); err == nil {
		t.Fatal("missing legacy default submitted a generation")
	}
}

func TestOpenAIResolvedModelValidationBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	provider, err := newOpenAIChatGateway(server.URL, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.client.CloseIdleConnections)
	for _, model := range []string{"", "  ", strings.Repeat("m", 257), "model\nprivate", "model\x00private", string([]byte{0xff})} {
		if err := provider.StreamModel(context.Background(), model, "secret-prompt", func(string) error { return nil }); err == nil || strings.Contains(err.Error(), "secret-prompt") || strings.Contains(err.Error(), "fixture-key") {
			t.Fatal("invalid resolved model was accepted or secret appeared in error")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid model reached the gateway")
	}
}

func TestAsyncMediaResolvedParametersAreFrozen(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	client, transport := provider.client, provider.client.Transport
	image := mediaFixtureInput("image", 2)
	image.Parameters.Size, image.Parameters.Resolution = "1024x1024", "2K"
	video := mediaFixtureInput("video", 1)
	video.Parameters.Size, video.Parameters.Resolution, video.Parameters.Seconds = "1920x1080", "1080p", 12
	noDefaults := mediaFixtureInput("video", 1)
	for _, tc := range []struct {
		name, model, endpoint string
		input                 TaskInput
		want                  map[string]any
	}{
		{"image", "image-model-a", imageGenerationEndpoint, image, map[string]any{"model": "image-model-a", "prompt": image.Inputs.Prompt, "n": float64(2), "aspect_ratio": "16:9", "size": "1024x1024", "resolution": "2K"}},
		{"video", "video-model-b", videoGenerationEndpoint, video, map[string]any{"model": "video-model-b", "prompt": video.Inputs.Prompt, "aspect_ratio": "16:9", "size": "1920x1080", "resolution": "1080p", "seconds": float64(12)}},
		{"omitted_optional_video", "video-model-c", videoGenerationEndpoint, noDefaults, map[string]any{"model": "video-model-c", "prompt": noDefaults.Inputs.Prompt, "aspect_ratio": "16:9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submission, err := provider.PrepareModel(tc.input, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(submission.Body, &body); err != nil || !reflect.DeepEqual(body, tc.want) || submission.Endpoint != tc.endpoint || submission.Count != tc.input.Parameters.Count {
				t.Fatalf("frozen parameters mismatch: body=%s err=%v", submission.Body, err)
			}
			if submission.ProviderIdentity != provider.providerIdentity {
				t.Fatal("model was included in provider identity")
			}
		})
	}
	if calls.Load() != 0 || provider.imageModel != "fixture-image-model" || provider.videoModel != "fixture-video-model" || provider.videoSeconds != 8 || provider.videoRes != "720p" || provider.client != client || provider.client.Transport != transport {
		t.Fatal("preparation performed network I/O or mutated shared gateway defaults")
	}
}

func TestAsyncMediaCatalogRemovalKeepsSavedBytesAndHandles(t *testing.T) {
	var calls atomic.Int32
	var saved MediaSubmission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			t.Error("query-only gateway changed credentials")
		}
		if r.Method == "GET" {
			_, _ = fmt.Fprintf(w, `{"task_id":"fixture-history","media_type":%q,"stage":"generating","status":"processing","progress":25}`, saved.Kind)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, saved.Body) || r.Header.Get("Idempotency-Key") != saved.IdempotencyKey || r.URL.Path != saved.Endpoint {
			t.Error("saved request was rebuilt after catalog removal")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"task_id":"fixture-history"}`)
	}))
	defer server.Close()
	original := mediaFixtureProvider(t, server)
	queryOnly, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: server.URL, APIKey: fixtureMediaKey, CredentialID: "fixture-credential", AssetDir: t.TempDir(), AllowNoDefaultModel: true, allowLocalAssets: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { queryOnly.Close() })
	if queryOnly.providerIdentity != original.providerIdentity {
		t.Fatal("legacy model deletion invalidated saved gateway identity")
	}
	for _, kind := range []string{"image", "video"} {
		t.Run(kind, func(t *testing.T) {
			prepared, err := original.Prepare(mediaFixtureInput(kind, 1))
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := json.Marshal(prepared)
			if err != nil || bytes.Contains(persisted, []byte(fixtureMediaKey)) {
				t.Fatal("submission persistence leaked gateway key")
			}
			if err := json.Unmarshal(persisted, &saved); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				handle, err := queryOnly.SubmitPrepared(context.Background(), saved)
				if err != nil {
					t.Fatal(err)
				}
				persistedHandle, _ := json.Marshal(handle)
				var restoredHandle MediaHandle
				if err := json.Unmarshal(persistedHandle, &restoredHandle); err != nil {
					t.Fatal(err)
				}
				result, err := queryOnly.Query(context.Background(), restoredHandle)
				if err != nil || result.Status != "RUNNING" {
					t.Fatalf("old handle did not resume query: result=%+v err=%v", result, err)
				}
			}
			if _, err := queryOnly.Prepare(mediaFixtureInput(kind, 1)); err == nil {
				t.Fatal("legacy submit accepted without its default model")
			}
		})
	}
	if calls.Load() != 8 {
		t.Fatal("unexpected replay or query request count")
	}
	// An accepted snapshot contains its own model; absence of a legacy default
	// does not prevent the core from preparing a valid new catalog operation.
	if _, err := queryOnly.PrepareModel(mediaFixtureInput("image", 1), "explicit-current-model"); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncMediaResolvedModelValidationBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	for _, mutate := range []func(*TaskInput){
		func(in *TaskInput) { in.Parameters.Count = 0 },
		func(in *TaskInput) { in.Parameters.Count = 5 },
		func(in *TaskInput) { in.Parameters.Seconds = 1 },
		func(in *TaskInput) { in.Parameters.Size = strings.Repeat("x", 65) },
		func(in *TaskInput) { in.Parameters.Resolution = "2K\nprivate" },
		func(in *TaskInput) { in.Parameters.AspectRatio = string([]byte{0xff}) },
		func(in *TaskInput) { in.Inputs.Prompt = string([]byte{0xff}) },
		func(in *TaskInput) { in.Kind = "video"; in.Parameters.Seconds = 601 },
		func(in *TaskInput) { in.Kind = "video"; in.Parameters.Seconds = -1 },
	} {
		input := mediaFixtureInput("image", 1)
		mutate(&input)
		if _, err := provider.PrepareModel(input, "valid-model"); err == nil {
			t.Fatal("invalid explicit parameter accepted")
		}
	}
	for _, model := range []string{"", "  ", strings.Repeat("m", 257), "model\nprivate", "model\x00private", string([]byte{0xff})} {
		if _, err := provider.PrepareModel(mediaFixtureInput("image", 1), model); err == nil || strings.Contains(err.Error(), fixtureMediaKey) {
			t.Fatal("invalid explicit model accepted or key exposed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid snapshot reached network")
	}
}
