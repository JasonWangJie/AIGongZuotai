package studio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fragmentedReader struct{ source string }

func (r *fragmentedReader) Read(p []byte) (int, error) {
	if len(r.source) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), min(3, len(r.source)))
	copy(p, r.source[:n])
	r.source = r.source[n:]
	return n, nil
}

func TestOpenAIParserFragmentationCRLFMultilineAndUsage(t *testing.T) {
	fixture := ": heartbeat\r\ndata: {\"choices\":\r\ndata: [{\"delta\":{\"content\":\"你好🌌\"}}]}\r\n\r\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":3}}\n\ndata: [DONE]\n\n"
	var output strings.Builder
	err := parseOpenAIStream(&fragmentedReader{source: fixture}, func(s string) error { output.WriteString(s); return nil })
	if err != nil || output.String() != "你好🌌" {
		t.Fatalf("output=%q error=%v", output.String(), err)
	}
}

func TestOpenAIParserRejectsPartialMalformedAndOversizedStreams(t *testing.T) {
	for name, fixture := range map[string]string{
		"partial":     "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
		"malformed":   "data: {invalid}\n\n",
		"oversized":   "data: " + strings.Repeat("x", maxUpstreamEventBytes+1) + "\n\n",
		"error":       "data: {\"error\":{\"message\":\"secret-upstream-content\"}}\n\n",
		"error event": "event: error\ndata: {\"message\":\"secret-upstream-content\"}\n\ndata: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := parseOpenAIStream(strings.NewReader(fixture), func(string) error { return nil })
			if err == nil {
				t.Fatal("invalid stream accepted")
			}
			if strings.Contains(err.Error(), "secret-upstream") {
				t.Fatal("provider body leaked")
			}
		})
	}
}

func TestLiveAdapterUsesConfiguredEndpointAndDoesNotForwardIdempotency(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-private-key" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("incorrect upstream boundary")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fixture output\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	a, s := testApp(t, Config{Mode: "live", BaseURL: upstream.URL + "/v1", APIKey: "fixture-private-key", ChatModel: "fixture-model"})
	_, data, _ := request(t, s, "POST", "/api/v1/runs", "live-fixture-key", runInput())
	run := waitRun(t, s, decode[Run](t, data).ID)
	if run.Status != "COMPLETED" || run.Output != "fixture output" || calls.Load() != 1 {
		t.Fatalf("bad live fixture run: %+v", run)
	}
	_, _, _ = request(t, s, "POST", "/api/v1/runs", "live-fixture-key", runInput())
	if calls.Load() != 1 {
		t.Fatal("idempotent replay resubmitted upstream")
	}
	status, data, _ := request(t, s, "POST", "/api/v1/tasks", "live-media-key", taskInput("image"))
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(data), "MODEL_DISABLED") || a.media != nil {
		t.Fatal("live media must remain explicitly unavailable")
	}
}

func TestLiveConfigAndRedirectFailClosed(t *testing.T) {
	for _, config := range []Config{{Mode: "live"}, {Mode: "unknown"}, {Mode: "live", BaseURL: "http://remote.invalid/v1", APIKey: "fixture", ChatModel: "model"}} {
		if app, err := newIsolatedApp(t, config); err == nil {
			app.Close()
			t.Fatal("unsafe config accepted")
		}
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/credential-trap", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	provider, err := newOpenAIChat(upstream.URL+"/v1", "fixture-key", "model")
	if err != nil {
		t.Fatal(err)
	}
	defer provider.client.CloseIdleConnections()
	err = provider.Stream(context.Background(), "test", func(string) error { return nil })
	if err == nil || calls.Load() != 1 {
		t.Fatal("live adapter followed redirect or retried")
	}
}
