package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const openLoopModel259 = "fixture259-image"
const openLoopPromptPrefix259 = "fixture259-prompt-"
const openLoopHandlePrefix259 = "asyncimg_fixture259_"

func openLoopPrompt259(i int) string { return openLoopPromptPrefix259 + strconv.Itoa(i) }

// These per-handle records never enter the report. They are limited by the
// planned slot count and an explicit upper bound on trace records per slot.
type openLoopQuery259 struct {
	written, arrived, responded, headers time.Time
	status, writes                       int
	terminal                             bool
}
type openLoopWireRow259 struct {
	original openLoopOriginal259
	queries  []*openLoopQuery259
}
type openLoopSnapshot259 struct {
	rows                  []openLoopWireRow259
	active, peak, invalid int
	revision              uint64
	one429                bool
}
type openLoopGateway259 struct {
	mu sync.Mutex
	openLoopSnapshot259
	p            openLoopProfile259
	host         string
	keys         map[string]bool
	maxPerHandle int
}

func newOpenLoopGateway259(p openLoopProfile259, limited bool) *openLoopGateway259 {
	return &openLoopGateway259{
		p: p, keys: make(map[string]bool),
		maxPerHandle:        int((p.Warm+p.Steady+p.DrainLimit)/p.Poll) + 3,
		openLoopSnapshot259: openLoopSnapshot259{rows: make([]openLoopWireRow259, p.slots()), one429: limited},
	}
}

func (g *openLoopGateway259) reject(w http.ResponseWriter) {
	g.mu.Lock()
	g.invalid++
	g.revision++
	g.mu.Unlock()
	w.WriteHeader(http.StatusBadRequest)
}

func (g *openLoopGateway259) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
		g.reject(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == imageGenerationEndpoint:
		g.submit(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, mediaQueryPrefix):
		g.query(w, r)
	default:
		g.reject(w)
	}
}

func (g *openLoopGateway259) submit(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var body struct {
		Model, Prompt string
		N             int
	}
	if err != nil || json.Unmarshal(raw, &body) != nil || body.Model != openLoopModel259 || body.N != 1 {
		g.reject(w)
		return
	}
	i, ok := admissionCapacityIndex(body.Prompt, openLoopPromptPrefix259, len(g.rows))
	key := r.Header.Get("Idempotency-Key")
	if !ok || len(key) == 0 || len(key) > 256 {
		g.reject(w)
		return
	}
	accepted := time.Now()
	handle := openLoopHandlePrefix259 + strconv.Itoa(i)
	sum := sha256.Sum256(raw)
	g.mu.Lock()
	if g.keys[key] || g.rows[i].original.Handle != "" || g.host != "" && g.host != r.Host {
		g.mu.Unlock()
		g.reject(w)
		return
	}
	g.host = r.Host
	g.keys[key] = true
	g.rows[i].original = openLoopOriginal259{Handle: handle, Key: key, SHA: hex.EncodeToString(sum[:]), AcceptedAt: accepted}
	g.revision++
	g.mu.Unlock()
	w.Header().Set("Retry-After", strconv.Itoa(int(g.p.Poll/time.Second)))
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"task_id": handle})
}

// Evaluate readiness at the gateway's response decision, independent of how
// often this task was polled. Normal progressing replies carry the poll hint.
func openLoopWireResult259(accepted, decision time.Time, life time.Duration) (string, int) {
	elapsed := decision.Sub(accepted)
	if elapsed >= life {
		return "failed", 99
	}
	return "generating", min(98, max(0, int(elapsed*99/life)))
}

func (g *openLoopGateway259) query(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	i, ok := admissionCapacityIndex(r.URL.Path, mediaQueryPrefix+openLoopHandlePrefix259, len(g.rows))
	if !ok {
		g.reject(w)
		return
	}
	g.mu.Lock()
	row := &g.rows[i]
	if row.original.Handle == "" || len(row.queries) == 0 || g.host != r.Host || !row.original.TerminalAt.IsZero() {
		g.mu.Unlock()
		g.reject(w)
		return
	}
	q := row.queries[len(row.queries)-1]
	if !q.arrived.IsZero() {
		g.mu.Unlock()
		g.reject(w)
		return
	}
	q.arrived = arrived
	row.original.GETs++
	limited := g.one429 && i == 0 && row.original.GETs == 1
	g.active++
	g.peak = max(g.peak, g.active)
	g.revision++
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.active--; g.revision++; g.mu.Unlock() }()
	deadline := arrived.Add(g.p.Handler)
	for delay := time.Until(deadline); delay > 0; delay = time.Until(deadline) {
		timer := time.NewTimer(delay)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	decision := time.Now()
	g.mu.Lock()
	stage, progress := openLoopWireResult259(row.original.AcceptedAt, decision, g.p.Life)
	q.status = http.StatusOK
	q.responded = decision
	if limited {
		q.status = http.StatusTooManyRequests
		row.original.RateLimits++
	} else if stage == "failed" {
		q.terminal = true
		row.original.TerminalAt = decision
	}
	g.revision++
	handle := row.original.Handle
	g.mu.Unlock()
	if limited || stage != "failed" {
		w.Header().Set("Retry-After", strconv.Itoa(int(g.p.Poll/time.Second)))
	}
	if limited {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "{}")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"task_id": handle, "media_type": "image", "stage": stage, "progress": progress})
}

type openLoopTransport259 struct {
	base http.RoundTripper
	g    *openLoopGateway259
}

func (g *openLoopGateway259) transport(base http.RoundTripper) http.RoundTripper {
	return &openLoopTransport259{base: base, g: g}
}

func (tr *openLoopTransport259) CloseIdleConnections() {
	if base, ok := tr.base.(interface{ CloseIdleConnections() }); ok {
		base.CloseIdleConnections()
	}
}

func (tr *openLoopTransport259) RoundTrip(req *http.Request) (*http.Response, error) {
	g := tr.g
	i, ok := admissionCapacityIndex(req.URL.Path, mediaQueryPrefix+openLoopHandlePrefix259, len(g.rows))
	g.mu.Lock()
	if !ok || req.Method != "GET" || req.URL.Scheme != "http" || req.URL.RawQuery != "" || !net.ParseIP(req.URL.Hostname()).IsLoopback() || g.host != req.URL.Host {
		g.invalid++
		g.revision++
		g.mu.Unlock()
		return nil, errors.New("unexpected original query transport target")
	}
	row := &g.rows[i]
	if row.original.Handle == "" || !row.original.TerminalAt.IsZero() || len(row.queries) >= g.maxPerHandle || len(row.queries) > 0 && row.queries[len(row.queries)-1].headers.IsZero() {
		g.invalid++
		g.revision++
		g.mu.Unlock()
		return nil, errors.New("unexpected overlapping, unknown, excess or post-terminal query")
	}
	q := &openLoopQuery259{}
	row.queries = append(row.queries, q)
	g.revision++
	g.mu.Unlock()
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		written := time.Now()
		g.mu.Lock()
		defer g.mu.Unlock()
		q.writes++
		if info.Err == nil {
			q.written = written
		}
		g.revision++
	}}
	res, err := tr.base.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	headers := time.Now()
	g.mu.Lock()
	if err == nil {
		q.headers = headers
	}
	g.revision++
	g.mu.Unlock()
	return res, err
}

func (g *openLoopGateway259) snapshot() openLoopSnapshot259 {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.openLoopSnapshot259
	s.rows = make([]openLoopWireRow259, len(g.rows))
	for i, src := range g.rows {
		s.rows[i].original = src.original
		s.rows[i].queries = make([]*openLoopQuery259, len(src.queries))
		for j, q := range src.queries {
			v := *q
			s.rows[i].queries[j] = &v
		}
	}
	return s
}

func (s openLoopSnapshot259) original(i int) openLoopOriginal259 { return s.rows[i].original }
func (s openLoopSnapshot259) writtenSnapshot() map[string][]time.Time {
	out := make(map[string][]time.Time, len(s.rows))
	for _, row := range s.rows {
		if row.original.Handle == "" {
			continue
		}
		times := make([]time.Time, 0, len(row.queries))
		for _, q := range row.queries {
			if !q.written.IsZero() {
				times = append(times, q.written)
			}
		}
		out[row.original.Handle] = times
	}
	return out
}
func (g *openLoopGateway259) unchangedAfterReplay(s openLoopSnapshot259) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.revision == s.revision && g.active == 0
}

type openLoopWireMetrics259 struct {
	UniquePOST, GETWritten, GETArrived, GETResponded, GETHeaders int
	HTTP2xxRunning, HTTP2xxTerminal, HTTP429                     int
	SameHandleWriteInterval, HandlerService, WriteToHeaders      openLoopLatency259
}

type openLoopWireSummary259 struct {
	openLoopWireMetrics259
	PeakSimultaneousHandlers, UnrecognizedRequests              int
	HandlesWithObservedInterval, HandlesWithoutObservedInterval int
	AdmissionToLastSupplierResponse                             openLoopLatency259
}

type openLoopWireWindow259 struct {
	openLoopWireMetrics259
	TimeMeanGETsInHandler                                                         float64
	GETWrittenPerSecond, GETReceivedPerSecond, HTTP2xxPerSecond, HTTP429PerSecond float64
}

// Intervals belong to the window containing the later event. Arrival and
// header observations are independent of WroteRequest callbacks and grouped
// by their own timestamps. A response is timed at the server's decision to
// emit its JSON; headers measure the client's independent read.
func (s openLoopSnapshot259) report(p openLoopProfile259, start, end time.Time) (openLoopWireSummary259, []openLoopWireWindow259, error) {
	series := newOpenLoopSeries259(p, start, end)
	windows := make([]openLoopWireWindow259, len(series.w))
	summary := openLoopWireSummary259{PeakSimultaneousHandlers: s.peak, UnrecognizedRequests: s.invalid}
	// The last distribution bucket contains the full-run samples.
	type durations struct{ interval, service, toHeaders []time.Duration }
	samples := make([]durations, len(windows)+1)
	all := len(windows)
	occupied := make([]time.Duration, len(windows))
	var life []time.Duration
	record := func(at time.Time, f func(*openLoopWireMetrics259, *durations)) error {
		i := series.index(at)
		if i < 0 {
			return errors.New("supplier observation outside experiment")
		}
		f(&windows[i].openLoopWireMetrics259, &samples[i])
		f(&summary.openLoopWireMetrics259, &samples[all])
		return nil
	}
	for _, row := range s.rows {
		original := row.original
		if original.Handle == "" {
			if len(row.queries) != 0 {
				return summary, windows, errors.New("GET for an unaccepted slot")
			}
			continue
		}
		if err := record(original.AcceptedAt, func(m *openLoopWireMetrics259, _ *durations) { m.UniquePOST++ }); err != nil {
			return summary, windows, err
		}
		if len(row.queries) == 0 || original.GETs != len(row.queries) || original.TerminalAt.IsZero() || original.TerminalAt.Sub(original.AcceptedAt) < p.Life {
			return summary, windows, errors.New("original submission did not reach its time-based terminal")
		}
		life = append(life, original.TerminalAt.Sub(original.AcceptedAt))
		if len(row.queries) > 1 {
			summary.HandlesWithObservedInterval++
		} else {
			summary.HandlesWithoutObservedInterval++
		}
		var prev *openLoopQuery259
		for j, q := range row.queries {
			if q.writes != 1 || q.written.IsZero() || q.arrived.IsZero() || q.responded.IsZero() || q.headers.IsZero() || q.headers.Before(q.written) || q.headers.Before(q.responded) || q.arrived.Add(time.Microsecond).Before(original.AcceptedAt.Add(p.Poll)) || q.responded.Sub(q.arrived) < p.Handler {
				return summary, windows, errors.New("incomplete original GET or invalid handler duration")
			}
			if q.terminal != (j == len(row.queries)-1) || q.terminal && !original.TerminalAt.Equal(q.responded) {
				return summary, windows, errors.New("response received after terminal or incorrect final timestamp")
			}
			if prev != nil && (q.written.Before(prev.written) || q.arrived.Add(time.Microsecond).Before(prev.responded.Add(p.Poll))) {
				return summary, windows, errors.New("same-handle GET arrived before its retry hint")
			}
			if err := record(q.written, func(m *openLoopWireMetrics259, d *durations) {
				m.GETWritten++
				if prev != nil {
					d.interval = append(d.interval, q.written.Sub(prev.written))
				}
			}); err != nil {
				return summary, windows, err
			}
			if err := record(q.arrived, func(m *openLoopWireMetrics259, _ *durations) { m.GETArrived++ }); err != nil {
				return summary, windows, err
			}
			// Integrate actual handler residency across window boundaries. This
			// differs from configured worker count or handled requests per second.
			until := series.index(q.responded)
			if until < 0 {
				return summary, windows, errors.New("handler completion outside the run")
			}
			for bucket := series.index(q.arrived); bucket <= until; bucket++ {
				lo := start.Add(time.Duration(bucket) * p.Window)
				hi := lo.Add(p.Window)
				if lo.Before(q.arrived) {
					lo = q.arrived
				}
				if hi.After(q.responded) {
					hi = q.responded
				}
				occupied[bucket] += hi.Sub(lo)
			}
			if q.status != http.StatusOK && q.status != http.StatusTooManyRequests {
				return summary, windows, errors.New("unknown response status on original handle")
			}
			if err := record(q.responded, func(m *openLoopWireMetrics259, d *durations) {
				m.GETResponded++
				switch {
				case q.status == http.StatusTooManyRequests:
					m.HTTP429++
				case q.terminal:
					m.HTTP2xxTerminal++
				default:
					m.HTTP2xxRunning++
				}
				d.service = append(d.service, q.responded.Sub(q.arrived))
			}); err != nil {
				return summary, windows, err
			}
			if err := record(q.headers, func(m *openLoopWireMetrics259, d *durations) {
				m.GETHeaders++
				d.toHeaders = append(d.toHeaders, q.headers.Sub(q.written))
			}); err != nil {
				return summary, windows, err
			}
			prev = q
		}
	}
	for i, v := range samples {
		m := &summary.openLoopWireMetrics259
		if i != all {
			m = &windows[i].openLoopWireMetrics259
		}
		m.SameHandleWriteInterval = openLoopLatencies259(v.interval)
		m.HandlerService = openLoopLatencies259(v.service)
		m.WriteToHeaders = openLoopLatencies259(v.toHeaders)
		if i != all {
			seconds := min(p.Window, end.Sub(start.Add(time.Duration(i)*p.Window))).Seconds()
			windows[i].TimeMeanGETsInHandler = occupied[i].Seconds() / seconds
			windows[i].GETWrittenPerSecond = float64(m.GETWritten) / seconds
			windows[i].GETReceivedPerSecond = float64(m.GETArrived) / seconds
			windows[i].HTTP2xxPerSecond = float64(m.HTTP2xxRunning+m.HTTP2xxTerminal) / seconds
			windows[i].HTTP429PerSecond = float64(m.HTTP429) / seconds
		}
	}
	summary.AdmissionToLastSupplierResponse = openLoopLatencies259(life)
	expected429 := 0
	if s.one429 {
		expected429 = 1
	}
	if s.active != 0 || s.invalid != 0 || s.peak < 1 || s.peak > 64 || summary.UniquePOST != len(s.rows) || summary.HTTP2xxTerminal != summary.UniquePOST || summary.HTTP429 != expected429 ||
		summary.HandlesWithObservedInterval == 0 || summary.GETWritten != summary.GETHeaders || summary.GETResponded != summary.GETArrived || summary.GETHeaders != summary.GETArrived || summary.GETResponded != summary.HTTP2xxRunning+summary.HTTP2xxTerminal+summary.HTTP429 {
		return summary, windows, errors.New("incomplete or mismatched gateway observations")
	}
	return summary, windows, nil
}

func TestOpenLoopWireLifetime259(t *testing.T) {
	base := time.Unix(100, 0)
	life := 600 * time.Second
	for _, tc := range []struct {
		age      time.Duration
		stage    string
		progress int
	}{
		{-time.Second, "generating", 0}, {0, "generating", 0}, {300 * time.Second, "generating", 49},
		{life - time.Nanosecond, "generating", 98}, {life, "failed", 99}, {life + time.Hour, "failed", 99},
	} {
		stage, progress := openLoopWireResult259(base, base.Add(tc.age), life)
		if stage != tc.stage || progress != tc.progress {
			t.Fatalf("incorrect status or progress at age %v", tc.age)
		}
	}
}

func TestOpenLoopWireWindows259(t *testing.T) {
	base := time.Unix(100, 0)
	p := openLoopProfile259{Warm: 2 * time.Second, Steady: 2 * time.Second, Life: 3 * time.Second, Poll: time.Second, Handler: 200 * time.Millisecond, Window: 2 * time.Second, Slot: 4 * time.Second, DrainLimit: 10 * time.Second}
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	src := openLoopSnapshot259{peak: 1, rows: []openLoopWireRow259{{original: openLoopOriginal259{Handle: openLoopHandlePrefix259 + "0", AcceptedAt: at(1), TerminalAt: at(4201), GETs: 2}, queries: []*openLoopQuery259{
		{writes: 1, written: at(1799), arrived: at(1800), responded: at(2000), headers: at(2001), status: 200},
		{writes: 1, written: at(4000), arrived: at(4001), responded: at(4201), headers: at(4202), status: 200, terminal: true},
	}}}}
	s, w, err := src.report(p, base, at(5000))
	if err != nil || len(w) != 3 || w[0].GETWritten != 1 || w[0].GETResponded != 0 || w[1].GETWritten != 0 || w[1].HTTP2xxRunning != 1 || w[2].HTTP2xxTerminal != 1 || w[2].HTTP2xxPerSecond != 1 || w[0].TimeMeanGETsInHandler != 0.1 || w[2].TimeMeanGETsInHandler != 0.2 || s.HandlerService.Samples != 2 || s.SameHandleWriteInterval.Samples != 1 {
		t.Fatal("cross-window query accounting failed", err)
	}
	q := src.rows[0].queries[1]
	q.written = at(2100)
	q.arrived = at(2101)
	if _, _, err := src.report(p, base, at(5000)); err == nil {
		t.Fatal("accepted a GET before the original Retry-After")
	}
}

func TestOpenLoopWireProtocol259(t *testing.T) {
	p, _ := mediaOpenLoopProfile259("pilot")
	p.Life = 40 * time.Millisecond
	p.Handler = time.Millisecond
	g := newOpenLoopGateway259(p, true)
	srv := httptest.NewServer(g)
	defer srv.Close()
	client := srv.Client()
	origin := client.Transport
	traced := &http.Client{Transport: g.transport(origin)}
	defer traced.CloseIdleConnections()
	req := func(method, path, body, key string) *http.Request {
		r, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+fixtureMediaKey)
		r.Header.Set("Idempotency-Key", key)
		return r
	}
	call := func(c *http.Client, r *http.Request) (int, string, []byte) {
		res, err := c.Do(r)
		if err != nil {
			t.Fatal("local HTTP operation failed")
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal("could not read fixture response")
		}
		return res.StatusCode, res.Header.Get("Retry-After"), raw
	}
	body := fmt.Sprintf(`{"model":%q,"prompt":%q,"n":1}`, openLoopModel259, openLoopPrompt259(0))
	code, hint, _ := call(client, req("POST", imageGenerationEndpoint, body, "private-fixture-key"))
	if code != 202 || hint != "3" {
		t.Fatal("missing original acknowledgement")
	}
	handle := openLoopHandlePrefix259 + "0"
	code, hint, _ = call(traced, req("GET", mediaQueryPrefix+handle, "", ""))
	if code != 429 || hint != "3" {
		t.Fatal("controlled first GET did not carry the retry hint")
	}
	if !openLoopWait259(t.Context(), g.snapshot().original(0).AcceptedAt.Add(p.Life)) {
		t.Fatal("local lifetime wait failed")
	}
	code, _, raw := call(traced, req("GET", mediaQueryPrefix+handle, "", ""))
	if code != 200 || !strings.Contains(string(raw), `"stage":"failed"`) {
		t.Fatal("elapsed supplier job was not terminal")
	}
	wire := g.snapshot()
	o := wire.original(0)
	sum := sha256.Sum256([]byte(body))
	if o.SHA != hex.EncodeToString(sum[:]) || o.Handle != handle || o.Key != "private-fixture-key" || o.GETs != 2 || o.RateLimits != 1 || len(wire.writtenSnapshot()[handle]) != 2 || !g.unchangedAfterReplay(wire) {
		t.Fatal("original-body proof, GET ledger or replay snapshot differs")
	}
	code, _, _ = call(client, req("POST", imageGenerationEndpoint, body, "private-fixture-key"))
	if code != 400 || g.unchangedAfterReplay(wire) {
		t.Fatal("duplicate POST unobserved")
	}
	bad := req("GET", mediaQueryPrefix+handle, "", "")
	bad.URL.Host = "192.0.2.1"
	if res, err := traced.Do(bad); err == nil {
		res.Body.Close()
		t.Fatal("accepted alternate query authority")
	}
}

func TestOpenLoopWireNativeAdapter259(t *testing.T) {
	p, _ := mediaOpenLoopProfile259("pilot")
	p.Handler = time.Millisecond
	p.Life = time.Millisecond
	g := newOpenLoopGateway259(p, true)
	srv := httptest.NewServer(g)
	defer srv.Close()
	provider, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: srv.URL, APIKey: fixtureMediaKey, CredentialID: "fixture259", ImageModel: openLoopModel259, AssetDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	provider.queryClient.Transport = g.transport(provider.queryClient.Transport)
	task := taskInput("image")
	task.Inputs.Prompt = openLoopPrompt259(0)
	frozen, err := provider.PrepareModel(task, openLoopModel259)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := provider.SubmitPrepared(t.Context(), frozen)
	if err != nil || handle.RetryAfter != p.Poll || handle.ID != openLoopHandlePrefix259+"0" {
		t.Fatal("native submit did not retain original handle or poll hint")
	}
	_, err = provider.QueryStatus(t.Context(), handle)
	providerErr := requireMediaError(t, err)
	if providerErr.Observation == nil || providerErr.Observation.Class != "rate_limit" || !providerErr.Observation.HasServerDelay || providerErr.RetryAfter != p.Poll {
		t.Fatal("native query did not retain original 429 evidence")
	}
	reply, err := provider.QueryStatus(t.Context(), handle)
	if err != nil || reply.Result.Status != "FAILED" || reply.Result.Error == nil || reply.Result.Error.Code != "UPSTREAM_GENERATION_FAILED" || len(reply.Items) != 0 {
		t.Fatal("native terminal did not match controlled failure")
	}
	s := g.snapshot()
	o := s.original(0)
	sum := sha256.Sum256(frozen.Body)
	if o.SHA != hex.EncodeToString(sum[:]) || o.Key != frozen.IdempotencyKey || o.GETs != 2 || o.RateLimits != 1 || s.invalid != 0 {
		t.Fatal("wire identities or adapter counts disagree")
	}
}
