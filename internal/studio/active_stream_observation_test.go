package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const activeStreamSteps = 12

func activePrompt(i int) string      { return fmt.Sprintf("capacity-run-%02d", i) }
func activeDelta(i, step int) string { return fmt.Sprintf("C%02d.%02d|", i, step+1) }
func activeModel(i int) int          { return (i/4 + i%4) % 2 }

type capacityGateway struct {
	ctx     context.Context
	steps   [activeStreamSteps]chan struct{}
	finish  chan struct{}
	entered chan int
	mu      sync.Mutex
	// These fixed-size arrays are indexed solely by synthetic input; no
	// user data or identifiers appear in the report or failure diagnostics.
	starts                       []time.Time
	sent                         [][activeStreamSteps]time.Time
	posts, active, peak, invalid int
}

func newCapacityGateway(ctx context.Context, n int) *capacityGateway {
	g := &capacityGateway{ctx: ctx, finish: make(chan struct{}), entered: make(chan int, n), starts: make([]time.Time, n), sent: make([][activeStreamSteps]time.Time, n)}
	for i := range g.steps {
		g.steps[i] = make(chan struct{})
	}
	return g
}

func (g *capacityGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model    string
		Stream   bool
		Messages []ChatMessage
	}
	i := -1
	if r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" && r.Header.Get("Authorization") == "Bearer local-stream-fixture" && json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) == nil && body.Stream && len(body.Messages) == 1 && body.Messages[0].Role == "user" {
		if text := body.Messages[0].Content; strings.HasPrefix(text, "capacity-run-") {
			value, err := strconv.Atoi(strings.TrimPrefix(text, "capacity-run-"))
			if err == nil && value >= 0 && value < len(g.starts) && text == activePrompt(value) && body.Model == fmt.Sprintf("synthetic-stream-%d", activeModel(value)) {
				i = value
			}
		}
	}
	g.mu.Lock()
	if r.Method == http.MethodPost {
		g.posts++
	}
	if i < 0 || !g.starts[i].IsZero() {
		g.invalid++
		g.mu.Unlock()
		http.Error(w, "invalid fixture request", 400)
		return
	}
	g.starts[i] = time.Now()
	g.active++
	g.peak = max(g.peak, g.active)
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.active--; g.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	if err := http.NewResponseController(w).Flush(); err != nil {
		return
	}
	g.entered <- i
	wait := func(gate <-chan struct{}) bool {
		select {
		case <-gate:
			return true
		case <-r.Context().Done():
			return false
		case <-g.ctx.Done():
			return false
		}
	}
	for j, gate := range g.steps {
		if !wait(gate) {
			return
		}
		line := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", activeDelta(i, j))
		g.mu.Lock()
		g.sent[i][j] = time.Now()
		g.mu.Unlock()
		if _, err := io.WriteString(w, line); err != nil {
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
	}
	if wait(g.finish) {
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = http.NewResponseController(w).Flush()
	}
}

type capacityEvent struct {
	Name string
	ID   uint64
	At   time.Time
	Data struct {
		RunID         string `json:"run_id"`
		Seq           uint64
		Delta, Status string
	}
}

// Timestamp the terminating blank line, before unmarshalling. Comments and
// heartbeats carry no model content and produce no latency sample.
func visitCapacitySSE(r io.Reader, visit func(capacityEvent) error) error {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 2048), 8192)
	var name, id, data string
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, ":") {
			continue
		}
		if line == "" {
			if name == "" && id == "" && data == "" {
				continue
			}
			ev := capacityEvent{Name: name, At: time.Now()}
			v, err := strconv.ParseUint(id, 10, 64)
			if err != nil || name == "" || data == "" || v == 0 || json.Unmarshal([]byte(data), &ev.Data) != nil || ev.Data.Seq != v {
				return errors.New("invalid SSE frame")
			}
			ev.ID = v
			if err = visit(ev); err != nil {
				return err
			}
			name, id, data = "", "", ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: ") && name == "":
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: ") && id == "":
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: ") && data == "":
			data = strings.TrimPrefix(line, "data: ")
		default:
			return errors.New("unexpected SSE field")
		}
	}
	if scan.Err() != nil {
		return errors.New("SSE body read failed")
	}
	if name != "" || id != "" || data != "" {
		return errors.New("incomplete SSE frame")
	}
	return nil
}

func readCapacityStream(body io.Reader, run string, lane int, observed func(int, time.Time) error) error {
	next := 0
	err := visitCapacitySSE(body, func(e capacityEvent) error {
		if next > activeStreamSteps || e.Data.RunID != run || e.ID != uint64(next+1) {
			return errors.New("stream identity or order mismatch")
		}
		if next == activeStreamSteps {
			if e.Name != "run.completed" || e.Data.Status != "COMPLETED" {
				return errors.New("unexpected stream terminal")
			}
		} else if e.Name != "message.delta" || e.Data.Delta != activeDelta(lane, next) {
			return errors.New("missing or incorrect model increment")
		}
		next++
		return observed(next-1, e.At)
	})
	if err == nil && next != activeStreamSteps+1 {
		return errors.New("stream ended with missing events")
	}
	return err
}

type capacityDistribution struct {
	Samples int     `json:"samples"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
	Max     float64 `json:"max_ms"`
}

func capacityDistributionOf(values []time.Duration) capacityDistribution {
	if len(values) == 0 {
		return capacityDistribution{}
	}
	v := slices.Clone(values)
	slices.Sort(v)
	at := func(p int) float64 { return float64(v[(len(v)*p+99)/100-1]) / float64(time.Millisecond) }
	return capacityDistribution{len(v), at(50), at(95), at(99), at(100)}
}

func TestActiveStreamCapacityObservation(t *testing.T) {
	first := ": connected\n\nid: 1\nevent: message.delta\ndata: {\"run_id\":\"test\",\"seq\":1,\"delta\":\"C00.01|\"}\n\n"
	var rest strings.Builder
	for j := 1; j < activeStreamSteps; j++ {
		fmt.Fprintf(&rest, "id: %d\nevent: message.delta\ndata: {\"run_id\":\"test\",\"seq\":%d,\"delta\":%q}\n\n", j+1, j+1, activeDelta(0, j))
	}
	fmt.Fprintf(&rest, "id: 13\nevent: run.completed\ndata: {\"run_id\":\"test\",\"seq\":13,\"status\":\"COMPLETED\"}\n\n")
	whole := first + rest.String()
	visit := func(int, time.Time) error { return nil }
	if err := readCapacityStream(strings.NewReader(whole), "test", 0, visit); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{first + whole, rest.String(), strings.Replace(whole, "C00.01|", "", 1), strings.Replace(whole, "COMPLETED", "FAILED", 1), strings.TrimRight(whole, "\n"), strings.Replace(whole, "\"seq\":4", "\"seq\":3", 1)} {
		if readCapacityStream(strings.NewReader(bad), "test", 0, visit) == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	v := make([]time.Duration, 100)
	for i := range v {
		v[99-i] = time.Duration(i+1) * time.Millisecond
	}
	if capacityDistributionOf(v) != (capacityDistribution{100, 50, 95, 99, 100}) || v[0] != 100*time.Millisecond {
		t.Fatal("sample quantiles or original order changed")
	}
}

func TestActiveStreamCapacityModelFrames(t *testing.T) {
	g := newCapacityGateway(t.Context(), 1)
	for _, step := range g.steps {
		close(step)
	}
	close(g.finish)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"stream":true,"model":"synthetic-stream-0","messages":[{"role":"user","content":"capacity-run-00"}]}`))
	req.Header.Set("Authorization", "Bearer local-stream-fixture")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	var got []string
	err := parseOpenAIStream(w.Body, func(v string) error { got = append(got, v); return nil })
	if err != nil || w.Code != 200 || len(got) != activeStreamSteps || g.posts != 1 || g.peak != 1 || g.active != 0 {
		t.Fatal("model fixture frames disagree with the production decoder")
	}
	for i, s := range got {
		if s != activeDelta(0, i) {
			t.Fatal("model fixture content changed order")
		}
	}
}
