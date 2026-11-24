package studio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type childAcceptanceTopology struct {
	normal, readers, processes, models, workspaces int
	integrated                                     bool
}

func childAcceptanceProfile(mode string) childAcceptanceTopology {
	switch mode {
	case "pilot":
		return childAcceptanceTopology{4, 1, 2, 4, 5, false}
	case "full":
		return childAcceptanceTopology{80, 1, 2, 4, 5, false}
	case "integrated-pilot":
		return childAcceptanceTopology{8, 3, 2, 4, 5, true}
	case "integrated":
		return childAcceptanceTopology{80, 3, 2, 4, 5, true}
	case "integrated-264":
		return childAcceptanceTopology{264, 1, 8, 6, 5, true}
	case "integrated-512":
		return childAcceptanceTopology{512, 1, 8, 8, 9, true}
	case "integrated-1024":
		return childAcceptanceTopology{1024, 1, 16, 16, 17, true}
	default:
		return childAcceptanceTopology{}
	}
}

func (p childAcceptanceTopology) upstream(lane int) string {
	return fmt.Sprintf("child-model-%d", lane%p.models)
}

// The delayed reader has its own HTTP connection.
func (p childAcceptanceTopology) streamLimit() int { return max(256, p.normal*p.readers+1) }

// A reader has a stable identity within the same Run for the whole wave.
// readCapacityStream independently checks the actual Run, sequence, text and terminal.
type childReaderEvent struct {
	reader, lane, step int
	at                 time.Time
}

func (e childReaderEvent) mark(seen []bool, step, readersPerRun int) error {
	if readersPerRun < 1 || e.reader < 0 || e.reader >= len(seen) || e.lane != e.reader/readersPerRun || e.step != step {
		return errors.New("stream event belongs to a different reader, Run or step")
	}
	if seen[e.reader] {
		return errors.New("duplicate delivery to a reader at wave boundary")
	}
	seen[e.reader] = true
	return nil
}

type childJointEvidence struct {
	ModelRequests     int                  `json:"simultaneous_original_model_requests"`
	Streams           int                  `json:"simultaneous_admitted_readers"`
	Workspaces        []int                `json:"simultaneous_readers_per_workspace"`
	GlobalLimit       int                  `json:"configured_global_stream_limit"`
	WorkspaceLimit    int                  `json:"observed_workspace_stream_limit"`
	ReplayConnections int                  `json:"configured_replay_read_connections"`
	MonitorRounds     int                  `json:"steady_peak_session_rounds"`
	SessionChecks     int                  `json:"steady_peak_session_checks"`
	NormalsPerRun     int                  `json:"normal_readers_per_run"`
	NormalDeltas      int                  `json:"normal_delta_deliveries"`
	DelayedDeltas     int                  `json:"delayed_delta_deliveries"`
	NormalTerminals   int                  `json:"normal_terminal_deliveries"`
	DelayedTerminals  int                  `json:"delayed_terminal_deliveries"`
	NormalLatency     capacityDistribution `json:"normal_reader_delta_deliveries_latency"`
	DelayedLatency    capacityDistribution `json:"delayed_reader_delta_deliveries_latency"`
	AllReaderLatency  capacityDistribution `json:"all_reader_delta_deliveries_latency"`
	Postgres          batchDeltaEvidence   `json:"postgres_original_deltas"`
	Coordinator       map[string]int       `json:"coordinator"`
	WaveMillis        float64              `json:"first_wave_to_last_normal_delta_ms"`
}

// Read only fixed numeric fields. The measurement client never exposes account or
// Run identifiers, credentials, private content or arbitrary metric labels.
func childJointMetrics(t *testing.T, ctx context.Context, client *http.Client, base, token string) map[string]int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/metrics", nil)
	if err != nil {
		t.Fatal("invalid metrics request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal("joint measurement metrics unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("joint measurement metrics HTTP status %d", res.StatusCode)
	}
	fields := []string{
		"studio_active_streams", "studio_stream_session_rounds_total", "studio_stream_sessions_checked_total",
		"studio_run_delta_batch_commits_total", "studio_run_delta_batch_appended_events_total",
		"studio_run_delta_batch_solo_attempts_total", "studio_run_delta_batch_confirmed_events_total",
		"studio_run_delta_batch_pending", "studio_run_delta_batch_wait_seconds_count",
	}
	for _, reason := range []string{"store", "queue_timeout", "cancelled", "duplicate"} {
		fields = append(fields, fmt.Sprintf("studio_run_delta_batch_rejections_total{reason=%q}", reason))
	}
	got := make(map[string]int, len(fields))
	for _, field := range fields {
		got[field] = -1
	}
	body := &io.LimitedReader{R: res.Body, N: 4 << 20}
	scan := bufio.NewScanner(body)
	for scan.Scan() {
		name, value, ok := strings.Cut(scan.Text(), " ")
		current, wanted := got[name]
		if !ok || !wanted {
			continue
		}
		n, err := strconv.Atoi(value)
		if current != -1 || err != nil || n < 0 {
			t.Fatalf("invalid aggregate %s", name)
		}
		got[name] = n
	}
	if scan.Err() != nil || body.N == 0 {
		t.Fatal("incomplete metrics exposition")
	}
	for _, name := range fields {
		if got[name] < 0 {
			t.Fatalf("missing aggregate %s", name)
		}
	}
	return got
}

func observeChildJointOccupancy(t *testing.T, ctx context.Context, s *childAcceptanceServer, f childFixture, g *childAcceptanceGateway, token string, runsPerScope []int, copies int) childJointEvidence {
	t.Helper()
	e := childJointEvidence{NormalsPerRun: copies, GlobalLimit: f.streamLimit(), ReplayConnections: 2}
	sessions := 0
	for i, scope := range f.scopes {
		var runtime struct{ Streams struct{ Active, Limit int } }
		capacityRequest(t, ctx, scope, "GET", s.base+"/api/v1/runtime", "", nil, &runtime, 200)
		want := copies * runsPerScope[i]
		if i == 0 {
			want++
		} // The deliberately paused reader also uses a real slot.
		if runtime.Streams.Active != want || runtime.Streams.Limit != 64 {
			t.Fatalf("workspace %d active readers=%d limit=%d, expected active=%d limit=64", i, runtime.Streams.Active, runtime.Streams.Limit, want)
		}
		if want > 0 {
			sessions++
		}
		e.Streams += runtime.Streams.Active
		e.Workspaces = append(e.Workspaces, runtime.Streams.Active)
		e.WorkspaceLimit = runtime.Streams.Limit
	}
	// Hold all model responses open. Compare successive rounds while the
	// cohort is stable; an earlier round may have begun during admission.
	base := childJointMetrics(t, ctx, f.scopes[0].client, s.base, token)
	deadline := time.NewTimer(18 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("joint occupancy context expired")
		case <-deadline.C:
			t.Fatal("no complete grouped session validation at joint peak")
		case <-ticker.C:
		}
		m := childJointMetrics(t, ctx, f.scopes[0].client, s.base, token)
		if m["studio_active_streams"] != e.Streams {
			t.Fatalf("global active readers=%d, expected %d", m["studio_active_streams"], e.Streams)
		}
		rounds := m["studio_stream_session_rounds_total"] - base["studio_stream_session_rounds_total"]
		checks := m["studio_stream_sessions_checked_total"] - base["studio_stream_sessions_checked_total"]
		if rounds > 0 && checks == sessions*rounds {
			e.MonitorRounds, e.SessionChecks = rounds, checks
			break
		}
		if rounds > 0 {
			base = m
		}
	}
	g.mu.Lock()
	e.ModelRequests = g.active
	g.mu.Unlock()
	if e.ModelRequests != g.normal || e.Streams != copies*g.normal+1 {
		t.Fatal("model and subscription occupancies did not coincide")
	}
	return e
}

func observeChildJointCommits(t *testing.T, ctx context.Context, s *childAcceptanceServer, client *http.Client, token string, admin *pgx.Conn, turns []childConversation, wave time.Duration, e *childJointEvidence) {
	t.Helper()
	m := activeCapacityMeasurement{admin: admin, traces: make([]capacityTrace, len(turns)), wave: wave}
	for i, turn := range turns {
		m.traces[i].run = turn.run
	}
	e.Postgres = observeBatchTransactions(t, m, len(turns), true)
	e.WaveMillis = float64(wave) / float64(time.Millisecond)
	stats := childJointMetrics(t, ctx, client, s.base, token)
	if stats["studio_active_streams"] != 0 {
		t.Fatal("authorized readers still occupy admission slots after terminal replay and EOF")
	}
	group := stats["studio_run_delta_batch_commits_total"]
	appended := stats["studio_run_delta_batch_appended_events_total"]
	solo := stats["studio_run_delta_batch_solo_attempts_total"]
	confirmed := stats["studio_run_delta_batch_confirmed_events_total"]
	if group+solo != e.Postgres.Commits || appended+solo != e.Postgres.Events || confirmed != e.Postgres.Events || stats["studio_run_delta_batch_wait_seconds_count"] != confirmed || stats["studio_run_delta_batch_pending"] != 0 {
		t.Fatalf("coordinator group=%d appended=%d solo=%d confirmed=%d, physical delta transactions=%d rows=%d", group, appended, solo, confirmed, e.Postgres.Commits, e.Postgres.Events)
	}
	for _, reason := range []string{"store", "queue_timeout", "cancelled", "duplicate"} {
		if stats[fmt.Sprintf("studio_run_delta_batch_rejections_total{reason=%q}", reason)] != 0 {
			t.Fatalf("coordinator recorded %s rejection in joint wave", reason)
		}
	}
	e.Coordinator = map[string]int{"group_commits": group, "group_delta_events": appended, "single_attempts": solo, "confirmed_events": confirmed}
	if e.NormalTerminals != len(turns)*e.NormalsPerRun || e.NormalLatency.Samples != e.Postgres.Events*e.NormalsPerRun || e.AllReaderLatency.Samples != e.NormalLatency.Samples+e.DelayedLatency.Samples {
		t.Fatal("verified per-reader deliveries disagree with independent original PG rows")
	}
}

func TestChatChildIntegratedReaderBarrier(t *testing.T) {
	for _, tc := range []struct {
		mode         string
		runs, copies int
		joint        bool
	}{
		{"pilot", 4, 1, false}, {"full", 80, 1, false}, {"integrated-pilot", 8, 3, true}, {"integrated", 80, 3, true}, {"integrated-264", 264, 1, true}, {"integrated-512", 512, 1, true}, {"integrated-1024", 1024, 1, true}, {"other", 0, 0, false},
	} {
		p := childAcceptanceProfile(tc.mode)
		if p.normal != tc.runs || p.readers != tc.copies || p.integrated != tc.joint {
			t.Fatalf("incorrect %s profile", tc.mode)
		}
	}
	const runs = 80
	const copies = 3
	count := 0
	for step := 0; step <= activeStreamSteps; step++ {
		seen := make([]bool, runs*copies)
		for reader := len(seen) - 1; reader >= 0; reader-- {
			ev := childReaderEvent{reader: reader, lane: reader / copies, step: step}
			if err := ev.mark(seen, step, copies); err != nil {
				t.Fatal(err)
			}
			if ev.mark(seen, step, copies) == nil {
				t.Fatal("duplicate reader accepted")
			}
			if step < activeStreamSteps {
				count++
			}
		}
	}
	if count != 2880 {
		t.Fatal("incorrect number of deliveries")
	}
	for _, bad := range []childReaderEvent{{reader: -1}, {reader: 240}, {reader: 3, lane: 0}, {reader: 0, step: 1}} {
		if bad.mark(make([]bool, 240), 0, copies) == nil {
			t.Fatal("out-of-scope reader event accepted")
		}
	}
}

func TestChatChildExpandedIntegratedTopology(t *testing.T) {
	p := childAcceptanceProfile("integrated-264")
	f := childFixture{childAcceptanceTopology: p, scopes: make([]capacityScope, 5)}
	g := newChildAcceptanceGateway(t.Context(), p)
	cfg := f.settings(g, "http://127.0.0.1")
	if cfg["CHAT_CHILD_PROCESSES"] != "8" || cfg["CHAT_CHILD_SLOTS"] != "33" || p.streamLimit() != 265 ||
		cfg["CHAT_WORKSPACE_CONCURRENCY"] != "53" || cfg["ADMISSION_WORKSPACE_RUNS"] != "54" || cfg["CHAT_MODEL_CONCURRENCY"] != "44" {
		t.Fatal("execution, queue or stream budget cannot support the expanded fixture")
	}
	models := map[string]int{}
	workspace := make([]int, len(f.scopes))
	for i := range p.normal {
		models[f.upstream(i)]++
		workspace[i%len(workspace)]++
	}
	if len(models) != 6 {
		t.Fatal("need six distinct upstream identities")
	}
	for _, count := range models {
		if count != 44 {
			t.Fatal("expanded fixture would strand a frozen model")
		}
	}
	for _, count := range workspace {
		if count != 52 && count != 53 {
			t.Fatal("expanded fixture would strand a workspace")
		}
	}
}

func TestChatChildFullCapacityProfile253(t *testing.T) {
	p := childAcceptanceProfile("integrated-512")
	if p.normal != 512 || p.processes != 8 || p.models != 8 || p.readers != 1 || !p.integrated || p.workspaces != 9 || p.streamLimit() != 513 {
		t.Fatal("512 physical model streams and a separate delayed reader must fit")
	}
	f := childFixture{childAcceptanceTopology: p, scopes: make([]capacityScope, p.workspaces)}
	cfg := f.settings(newChildAcceptanceGateway(t.Context(), p), "http://127.0.0.1")
	if cfg["CHAT_CHILD_SLOTS"] != "64" || cfg["CHAT_MODEL_CONCURRENCY"] != "64" ||
		cfg["CHAT_WORKSPACE_CONCURRENCY"] != "57" || cfg["ADMISSION_WORKSPACE_RUNS"] != "58" ||
		cfg["ADMISSION_MODEL_RUNS"] != "32" || cfg["CHAT_WORKSPACE_QUEUE_SIZE"] != "3" {
		t.Fatal("managed, workspace, model or wait budget differs from the fixture")
	}
	perModel := map[string]int{}
	perWorkspace := make([]int, p.workspaces)
	perPair := make(map[[2]int]int)
	for lane := range p.normal {
		workspace := lane % p.workspaces
		perModel[p.upstream(lane)]++
		perWorkspace[workspace]++
		perPair[[2]int{workspace, lane % p.models}]++
	}
	if len(perModel) != 8 || len(perPair) != 72 || perWorkspace[8] != 56 || perWorkspace[0]+1 != 58 {
		t.Fatal("a workspace or frozen model cannot hold its intended cohort")
	}
	for _, n := range perModel {
		if n != 64 {
			t.Fatal("expected a full frozen model")
		}
	}
	for i, n := range perWorkspace {
		if n != 57 && (i != 8 || n != 56) {
			t.Fatal("workspace execution or subscription budget exhausted")
		}
	}
	for _, n := range perPair {
		if n != 7 && n != 8 {
			t.Fatal("one workspace-model cohort would be rejected at admission")
		}
	}
	for _, mode := range []string{"pilot", "full", "integrated-pilot", "integrated", "integrated-264"} {
		if childAcceptanceProfile(mode).workspaces != 5 {
			t.Fatal("historical profile lost its original registration cohort")
		}
	}
}

func TestChatChildIntegratedMetrics(t *testing.T) {
	token := newID("metrics")
	_, server := testApp(t, Config{MetricsToken: token})
	m := childJointMetrics(t, t.Context(), server.Client(), server.URL, token)
	for name, value := range m {
		if value != 0 {
			t.Fatalf("fresh fixture metric %s=%d", name, value)
		}
	}
}
