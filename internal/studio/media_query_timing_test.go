package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const maxQueryStageSamples248 = 8192

type queryStageInterval248 struct{ start, end time.Time }
type queryStageBuffer248 struct {
	sync.Mutex
	items   []queryStageInterval248
	dropped int
}
type queryStageSamples248 struct {
	begin  atomic.Pointer[time.Time]
	phases [queryTimingPhaseCount]queryStageBuffer248
}

func (s *queryStageSamples248) record(phase queryTimingPhase, start, end time.Time) {
	from := s.begin.Load()
	if from == nil || end.Before(*from) {
		return
	}
	b := &s.phases[phase]
	b.Lock()
	defer b.Unlock()
	if len(b.items) >= maxQueryStageSamples248 {
		b.dropped++
		return
	}
	b.items = append(b.items, queryStageInterval248{start, end})
}

type queryStageSummary248 struct {
	Count, PeakConcurrent                                          int
	TotalSampleMS, WindowOccupancyMS, ActiveWallMS, MeanConcurrent float64
	P50MS, P95MS, P99MS                                            float64
}

func summarizeQueryStage248(items []queryStageInterval248, from, to time.Time) queryStageSummary248 {
	var result queryStageSummary248
	var durations []time.Duration
	type boundary struct {
		when   time.Time
		change int
	}
	edges := make([]boundary, 0, 2*len(items))
	for _, v := range items {
		if v.end.Before(from) || v.start.After(to) || v.end.Before(v.start) {
			continue
		}
		duration := v.end.Sub(v.start)
		durations = append(durations, duration)
		result.TotalSampleMS += float64(duration) / float64(time.Millisecond)
		lo, hi := v.start, v.end
		if lo.Before(from) {
			lo = from
		}
		if hi.After(to) {
			hi = to
		}
		if hi.After(lo) {
			edges = append(edges, boundary{lo, 1}, boundary{hi, -1})
			result.WindowOccupancyMS += float64(hi.Sub(lo)) / float64(time.Millisecond)
		}
	}
	result.Count = len(durations)
	if result.Count == 0 {
		return result
	}
	distribution := queryCapacityPercentiles(durations)
	result.P50MS, result.P95MS, result.P99MS = distribution.P50MS, distribution.P95MS, distribution.P99MS
	sort.Slice(edges, func(i, j int) bool { return edges[i].when.Before(edges[j].when) })
	var active int
	previous := from
	for i := 0; i < len(edges); {
		when := edges[i].when
		if active > 0 {
			result.ActiveWallMS += float64(when.Sub(previous)) / float64(time.Millisecond)
		}
		for i < len(edges) && edges[i].when.Equal(when) {
			active += edges[i].change
			i++
		}
		result.PeakConcurrent = max(active, result.PeakConcurrent)
		previous = when
	}
	result.MeanConcurrent = result.WindowOccupancyMS / (float64(to.Sub(from)) / float64(time.Millisecond))
	return result
}

var phaseNames248 = [queryTimingPhaseCount]string{
	querySelectReady: "select_ready", querySelectEmpty: "select_empty_or_error", queryStrictClaim: "strict_claim", queryClaimMiss: "strict_claim_miss_or_error",
	queryHandoff: "claim_return_to_executor", queryBeforeRenew: "executor_entry_to_first_renew", queryFirstRenew: "first_renew_total", queryHeartbeatRenew: "heartbeat_renew_total",
	queryLeasePoolWait: "renew_pool_wait", queryLeaseTransaction: "renew_transaction", queryResultAppWait: "known_result_app_mutex_wait", queryResultSave: "known_result_save_total",
	queryControlWait: "control_mutex_wait", querySaveTransaction: "control_save_transaction",
}

func (s *queryStageSamples248) report(t *testing.T, from, to time.Time, probe *queryCapacityProbe, base queryCapacityReport) {
	t.Helper()
	report := struct {
		Workers, QueryDBConnections, Completed, UniqueGETs int
		WallMS                                             float64
		Phases                                             map[string]queryStageSummary248
	}{
		base.Workers, base.LeaseQueryConnections, base.Completed, base.UniqueGETs, float64(to.Sub(from)) / float64(time.Millisecond), map[string]queryStageSummary248{}}
	for phase, name := range phaseNames248 {
		b := &s.phases[phase]
		b.Lock()
		items := append([]queryStageInterval248(nil), b.items...)
		dropped := b.dropped
		b.Unlock()
		if dropped != 0 || name == "" {
			t.Fatal("incomplete phase collection", phase, dropped)
		}
		report.Phases[name] = summarizeQueryStage248(items, from, to)
	}
	// These intervals are already captured by the native transport/server probe
	// in 040; aggregate their endpoints without bringing identities into timing.
	var due, transport, wire, service []queryStageInterval248
	probe.Lock()
	for _, v := range probe.samples {
		due = append(due, queryStageInterval248{v.due, v.written})
		transport = append(transport, queryStageInterval248{v.dispatch, v.written})
		wire = append(wire, queryStageInterval248{v.written, v.headers})
		service = append(service, queryStageInterval248{v.arrived, v.responded})
	}
	probe.Unlock()
	for name, items := range map[string][]queryStageInterval248{"due_to_wire": due, "transport_to_wire": transport, "wire_to_response_headers": wire, "upstream_service": service} {
		report.Phases[name] = summarizeQueryStage248(items, from, to)
	}
	for _, name := range []string{"strict_claim", "claim_return_to_executor", "executor_entry_to_first_renew", "first_renew_total", "known_result_app_mutex_wait", "due_to_wire", "transport_to_wire", "wire_to_response_headers", "upstream_service"} {
		if n := report.Phases[name].Count; n != base.UniqueGETs {
			t.Fatal("phase sample count differs from unique queries", name, n, base.UniqueGETs)
		}
	}
	for _, name := range []string{"select_ready", "renew_pool_wait", "renew_transaction", "known_result_save_total", "control_mutex_wait", "control_save_transaction"} {
		if n := report.Phases[name].Count; n < base.UniqueGETs {
			t.Fatal("missing attempts", name, n, base.UniqueGETs)
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEDIA_QUERY_STAGE_TIMING_248 %s", raw)
}

func TestQueryStageOverlap248(t *testing.T) {
	base := time.Unix(0, 0)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	s := summarizeQueryStage248([]queryStageInterval248{{at(-2), at(4)}, {at(2), at(6)}, {at(6), at(8)}, {at(8), at(8)}, {at(12), at(13)}}, at(0), at(10))
	if s.Count != 4 || s.TotalSampleMS != 12 || s.WindowOccupancyMS != 10 || s.ActiveWallMS != 8 || s.MeanConcurrent != 1 || s.PeakConcurrent != 2 || s.P50MS != 2 || s.P95MS != 6 || s.P99MS != 6 {
		t.Fatal("nested/overlapping samples or window boundaries were double counted", s)
	}
	var recorder queryStageSamples248
	recorder.record(queryStrictClaim, at(1), at(2))
	recorder.begin.Store(&base)
	for range maxQueryStageSamples248 + 2 {
		recorder.record(queryStrictClaim, at(1), at(2))
	}
	if got := len(recorder.phases[queryStrictClaim].items); got != maxQueryStageSamples248 || recorder.phases[queryStrictClaim].dropped != 2 {
		t.Fatal("unbounded or silent sampling overflow", got)
	}
}

func TestPostgresQueryStageTiming248(t *testing.T) {
	if os.Getenv("STUDIO_MEDIA_QUERY_TIMINGS") != "1" {
		t.Skip("explicit local PostgreSQL query stage timing")
	}
	runOriginalQueryRateCurve(t, true)
}

func TestPostgresQueryStageHeartbeat248(t *testing.T) {
	if os.Getenv("STUDIO_MEDIA_QUERY_TIMINGS") != "1" {
		t.Skip("explicit local PostgreSQL query stage timing")
	}
	requireLocalQueryCurveDatabase(t)
	dsn := testDatabase(t)
	arrived, beat := make(chan struct{}, 1), make(chan struct{}, 1)
	unblock := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(unblock) }) }
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != mediaQueryPrefix+"asyncimg_capacity_000" || gets.Add(1) != 1 {
			http.Error(w, "unexpected", 400)
			return
		}
		arrived <- struct{}{}
		select {
		case <-unblock:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"asyncimg_capacity_000","media_type":"image","stage":"failed"}`))
	}))
	defer server.Close()
	defer release()
	config := largeQueryConfig040(dsn, server.URL, time.Hour)
	config.TaskLeaseDuration = 5 * time.Second
	var counts [queryTimingPhaseCount]atomic.Int32
	config.queryTiming = &mediaQueryTiming{record: func(p queryTimingPhase, _, _ time.Time) {
		counts[p].Add(1)
		if p == queryHeartbeatRenew {
			select {
			case beat <- struct{}{}:
			default:
			}
		}
	}}
	a, _ := testApp(t, config)
	db := deliveryAdmin(t, dsn)
	r := queryCapacityRecords(a.media.(*AsyncMediaProvider), time.Now().Add(-time.Minute))[0]
	seedQueryCapacity(t, a, db, []*taskRecord{r})
	done := make(chan bool, 1)
	go func() { done <- a.executePersistentTaskLane(mediaQueryLane, r.Task.ID) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("original GET did not arrive")
	}
	select {
	case <-beat:
	case <-time.After(6 * time.Second):
		t.Fatal("active original GET did not record a separate heartbeat")
	}
	release()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("original query was never claimed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GET failed to finish")
	}
	if gets.Load() != 1 || counts[queryFirstRenew].Load() != 1 || counts[queryHeartbeatRenew].Load() < 1 || counts[queryLeaseTransaction].Load() < 2 || counts[querySaveTransaction].Load() != 1 || !queryCapacityFinal(t, a, db, []string{r.Task.ID}, []string{r.Handle.ID}) {
		t.Fatal("in-flight heartbeat was not distinguishable from admission and terminal save")
	}
}
