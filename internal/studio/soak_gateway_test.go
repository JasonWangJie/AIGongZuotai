package studio

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type soakGatewayCounts256 struct {
	ChatPosts, MediaPosts, MediaQueries, RateLimits, Invalid int
}

type soakGatewayRow256 struct {
	chatPosts, mediaPosts, mediaQueries int
	mediaAccepted                       bool
}

// Each normal input has independent chat and image identities. The last row
// belongs to the chat whose already emitted prefix will survive the crash.
type soakGateway256 struct {
	mu           sync.Mutex
	rows         []soakGatewayRow256
	mediaKeys    map[[sha256.Size]byte]bool
	counts       soakGatewayCounts256
	limitedAt    time.Time
	retryElapsed time.Duration
	crashPrefix  chan struct{}
	crashDone    chan struct{}
}

func newSoakGateway256(total int) *soakGateway256 {
	if total < 1 || total > 100_000 {
		panic("invalid bounded soak workload")
	}
	return &soakGateway256{
		rows:        make([]soakGatewayRow256, total+1),
		mediaKeys:   make(map[[sha256.Size]byte]bool),
		crashPrefix: make(chan struct{}),
		crashDone:   make(chan struct{}),
	}
}

func (g *soakGateway256) snapshot() soakGatewayCounts256 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts
}

func (g *soakGateway256) countsAt(index int) (chatPosts, mediaPosts, mediaQueries int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	row := g.rows[index]
	return row.chatPosts, row.mediaPosts, row.mediaQueries
}

func (g *soakGateway256) retryDelay() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.retryElapsed
}

func (g *soakGateway256) reject(w http.ResponseWriter, status int) {
	g.mu.Lock()
	g.counts.Invalid++
	g.mu.Unlock()
	http.Error(w, "unexpected fixture request", status)
}

func soakRequestBody256(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	return err == nil && json.Unmarshal(raw, dst) == nil
}

const soakPromptPrefix256 = "soak256-"
const soakHandlePrefix256 = "asyncimg_soak256_"

func (g *soakGateway256) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey || r.URL.RawQuery != "" {
		g.reject(w, http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		g.chat(w, r)
	case r.Method == http.MethodPost && r.URL.Path == imageGenerationEndpoint:
		g.submit(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, mediaQueryPrefix):
		g.query(w, r)
	default:
		g.reject(w, http.StatusNotFound)
	}
}

func (g *soakGateway256) chat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model    string
		Messages []ChatMessage
		Stream   bool
	}
	if !soakRequestBody256(w, r, &body) || body.Model != "soak-chat" || !body.Stream ||
		len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		g.reject(w, http.StatusBadRequest)
		return
	}
	index, ok := admissionCapacityIndex(body.Messages[0].Content, soakPromptPrefix256, len(g.rows))
	if !ok {
		g.reject(w, http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	row := &g.rows[index]
	row.chatPosts++
	g.counts.ChatPosts++
	first := row.chatPosts == 1
	g.mu.Unlock()
	if !first {
		g.reject(w, http.StatusConflict)
		return
	}
	crash := index == len(g.rows)-1
	if crash {
		defer close(g.crashDone)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flush := http.NewResponseController(w).Flush
	if flush() != nil {
		return
	}
	emit := func(delta string) bool {
		select {
		case <-r.Context().Done():
			return false
		case <-time.After(16 * time.Millisecond):
		}
		_, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", delta)
		return err == nil && flush() == nil
	}
	if !emit("a") {
		return
	}
	if crash {
		close(g.crashPrefix)
		<-r.Context().Done()
		return
	}
	if emit("b") {
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
		_ = flush()
	}
}

func (g *soakGateway256) submit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model, Prompt string
		N             int
	}
	if !soakRequestBody256(w, r, &body) || body.Model != "soak-image" || body.N != 1 {
		g.reject(w, http.StatusBadRequest)
		return
	}
	index, ok := admissionCapacityIndex(body.Prompt, soakPromptPrefix256, len(g.rows)-1)
	key := r.Header.Get("Idempotency-Key")
	if !ok || len(key) == 0 || len(key) > 256 {
		g.reject(w, http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(key))
	g.mu.Lock()
	row := &g.rows[index]
	row.mediaPosts++
	g.counts.MediaPosts++
	conflict := row.mediaPosts != 1 || g.mediaKeys[sum]
	if !conflict {
		g.mediaKeys[sum] = true
		row.mediaAccepted = true
	}
	g.mu.Unlock()
	if conflict {
		g.reject(w, http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"task_id": soakHandlePrefix256 + strconv.Itoa(index)})
}

func (g *soakGateway256) query(w http.ResponseWriter, r *http.Request) {
	index, ok := admissionCapacityIndex(r.URL.Path, mediaQueryPrefix+soakHandlePrefix256, len(g.rows)-1)
	if !ok {
		g.reject(w, http.StatusNotFound)
		return
	}
	g.mu.Lock()
	row := &g.rows[index]
	if !row.mediaAccepted {
		g.mu.Unlock()
		g.reject(w, http.StatusNotFound)
		return
	}
	row.mediaQueries++
	g.counts.MediaQueries++
	limited := index == 0 && row.mediaQueries == 1
	if limited {
		g.counts.RateLimits++
		g.limitedAt = time.Now()
	} else if index == 0 && row.mediaQueries == 2 {
		g.retryElapsed = time.Since(g.limitedAt)
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if limited {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "{}")
		return
	}
	// The standard binary disallows loopback output downloads. A known failure
	// exercises its real terminal write without opening any asset/network gate.
	_, _ = fmt.Fprintf(w, `{"task_id":"%s%d","media_type":"image","stage":"failed","status":"failed"}`, soakHandlePrefix256, index)
}

func TestSoakGateway256(t *testing.T) {
	g := newSoakGateway256(3)
	s := httptest.NewServer(g)
	defer s.Close()
	request := func(ctx context.Context, method, path, body, auth, key string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, s.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+auth)
		req.Header.Set("Idempotency-Key", key)
		res, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	chatInput := func(i int) string {
		return fmt.Sprintf(`{"model":"soak-chat","stream":true,"messages":[{"role":"user","content":"soak256-%d"}]}`, i)
	}
	started := time.Now()
	stream := request(t.Context(), "POST", "/v1/chat/completions", chatInput(0), fixtureMediaKey, "")
	data, err := io.ReadAll(stream.Body)
	stream.Body.Close()
	if err != nil || stream.StatusCode != 200 || !strings.Contains(string(data), `"content":"a"`) ||
		!strings.Contains(string(data), `"content":"b"`) || !strings.Contains(string(data), "[DONE]") || time.Since(started) < 25*time.Millisecond {
		t.Fatal("paced normal chat response missing")
	}
	// A context-bound client must actually observe the partial stream and then
	// close it; merely starting a goroutine does not prove cancellation.
	interrupted, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream = request(interrupted, "POST", "/v1/chat/completions", chatInput(3), fixtureMediaKey, "")
	line := make([]byte, len("data: "))
	if _, err = io.ReadFull(stream.Body, line); err != nil || string(line) != "data: " {
		t.Fatal("missing prefix before cancel")
	}
	select {
	case <-g.crashPrefix:
	case <-interrupted.Done():
		t.Fatal("partial stream never flushed")
	}
	cancel()
	stream.Body.Close()
	select {
	case <-g.crashDone:
	case <-time.After(time.Second):
		t.Fatal("fixture did not release interrupted request")
	}
	// Use the actual adapter for submission, 429 classification and final
	// state. The live soak separately checks elapsed scheduled retry time.
	p, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: s.URL, APIKey: fixtureMediaKey, CredentialID: "soak-test", ImageModel: "soak-image", AssetDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	submit := taskInput("image")
	submit.Inputs.Prompt = "soak256-0"
	prepared, err := p.PrepareModel(submit, "soak-image")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := p.SubmitPrepared(t.Context(), prepared)
	if err != nil || handle.ID != soakHandlePrefix256+"0" {
		t.Fatal("actual image adapter did not accept handle")
	}
	_, err = p.QueryStatus(t.Context(), handle)
	limited := requireMediaError(t, err)
	if limited.Code != "QUERY_TEMPORARY" || limited.Observation == nil || !limited.Observation.HasServerDelay || limited.RetryAfter != 3*time.Second {
		t.Fatal("original handle did not receive 429 with a server deadline and normal poll floor")
	}
	result, err := p.QueryStatus(t.Context(), handle)
	// This direct adapter test intentionally issues the two GETs back to back.
	// On Windows both may have the same clock reading. Count the two calls;
	// the scheduled integration run independently verifies the elapsed delay.
	_, _, polls := g.countsAt(0)
	if err != nil || result.Result.Status != "FAILED" || polls != 2 {
		t.Fatal("missing known media terminal or same-handle retry")
	}
	for _, bad := range []struct {
		method, path, body, auth, key string
		status                        int
	}{
		{"POST", "/v1/chat/completions", chatInput(0), fixtureMediaKey, "", 409},
		{"POST", "/v1/chat/completions", chatInput(1), "invalid", "", 403},
		{"POST", "/v1/chat/completions", chatInput(1) + "{}", fixtureMediaKey, "", 400},
		{"POST", imageGenerationEndpoint, `{"model":"soak-image","prompt":"soak256-0","n":1}`, fixtureMediaKey, "new-test-key", 409},
		{"POST", imageGenerationEndpoint, `{"model":"soak-image","prompt":"soak256-1","n":1}`, fixtureMediaKey, prepared.IdempotencyKey, 409},
		{"GET", mediaQueryPrefix + soakHandlePrefix256 + "1", "", fixtureMediaKey, "", 404},
	} {
		res := request(t.Context(), bad.method, bad.path, bad.body, bad.auth, bad.key)
		res.Body.Close()
		if res.StatusCode != bad.status {
			t.Fatal("bad method, key, identity or body was not rejected", res.StatusCode, bad.status)
		}
	}
	if got := g.snapshot(); got != (soakGatewayCounts256{ChatPosts: 3, MediaPosts: 3, MediaQueries: 2, RateLimits: 1, Invalid: 6}) {
		t.Fatal("fixture did not account for each attempt", got)
	}
	if chat, media, polls := g.countsAt(0); chat != 2 || media != 2 || polls != 2 {
		t.Fatal("per-input counts differ")
	}
}
