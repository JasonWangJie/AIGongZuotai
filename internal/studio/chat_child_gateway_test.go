package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The numbered lanes belong exclusively to this synthetic loopback gateway.
// No resource identity, address, credential or message is written to the report.
type childGatewayLane struct {
	entered, finished, release chan struct{}
	peerPort                   int
	posts                      int
	sent                       [activeStreamSteps]time.Time
	atAdmission                func()
}

type childAcceptanceGateway struct {
	ctx context.Context
	key string
	childAcceptanceTopology
	mu                    sync.Mutex
	lanes                 []childGatewayLane
	ticks                 [activeStreamSteps]chan struct{}
	active, peak, invalid int
}

func newChildAcceptanceGateway(ctx context.Context, profile childAcceptanceTopology) *childAcceptanceGateway {
	g := &childAcceptanceGateway{ctx: ctx, key: newID("local"), childAcceptanceTopology: profile, lanes: make([]childGatewayLane, profile.normal+9)}
	for i := range g.lanes {
		g.lanes[i] = childGatewayLane{entered: make(chan struct{}), finished: make(chan struct{}), release: make(chan struct{})}
	}
	for i := range g.ticks {
		g.ticks[i] = make(chan struct{})
	}
	return g
}

func (g *childAcceptanceGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model          string
		Stream         bool
		Messages       []ChatMessage
		MaxTokens      int  `json:"max_tokens"`
		AlternateLimit *int `json:"max_completion_tokens"`
		StreamOptions  struct {
			Include bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	_, portText, e := net.SplitHostPort(r.RemoteAddr)
	port, pErr := strconv.Atoi(portText)
	valid := e == nil && pErr == nil && port > 0 && r.URL.Path == "/v1/chat/completions" && r.Method == "POST" && r.Header.Get("Authorization") == "Bearer "+g.key && json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body) == nil && body.Stream && body.StreamOptions.Include && len(body.Messages) == 1 && body.Messages[0].Role == "user"
	lane := -1
	if valid && body.MaxTokens == 128 && body.AlternateLimit == nil {
		for i := range g.lanes {
			if body.Messages[0].Content == activePrompt(i) && body.Model == g.upstream(i) {
				lane = i
				break
			}
		}
	}
	g.mu.Lock()
	if lane < 0 {
		g.invalid++
		g.mu.Unlock()
		http.Error(w, "fixture contract", 400)
		return
	}
	unit := &g.lanes[lane]
	unit.posts++
	if unit.posts != 1 || lane == g.normal {
		g.invalid++
		g.mu.Unlock()
		http.Error(w, "unexpected dispatch", 409)
		return
	}
	g.active++
	g.peak = max(g.active, g.peak)
	unit.peerPort = port
	probe := unit.atAdmission
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.active--; close(unit.finished); g.mu.Unlock() }()
	if probe != nil {
		probe()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	flush := http.NewResponseController(w).Flush
	if flush() != nil {
		return
	}
	close(unit.entered)
	wait := func(c chan struct{}) bool {
		select {
		case <-g.ctx.Done():
			return false
		case <-r.Context().Done():
			return false
		case <-c:
			return true
		}
	}
	emit := func(i int) bool {
		g.mu.Lock()
		unit.sent[i] = time.Now()
		g.mu.Unlock()
		_, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", activeDelta(lane, i))
		return err == nil && flush() == nil
	}
	if lane < g.normal {
		for step, tick := range g.ticks {
			if !wait(tick) || !emit(step) {
				return
			}
		}
	} else if !emit(0) {
		return
	}
	if !wait(unit.release) {
		return
	}
	_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	_ = flush()
}
