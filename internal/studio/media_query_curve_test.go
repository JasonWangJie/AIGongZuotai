package studio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const queryCurveCount040 = 512
const queryCurveRate040 = 320

type queryCurveReport040 struct {
	queryCapacityReport
	ConfiguredWorkspaceLimit, ConfiguredChannelLimit                            int
	SampledPeakExecutionSlots, SampledPeakScopeSlots, OccupancySampleMS         int
	CompletionPercent                                                           float64
	PeakDueAwaitingWire, DueAwaitingWireAtLastArrival, RemainingAfterDrain      int
	DrainAfterLastArrivalMS                                                     float64
	WorkspacePageRows, TaskPageRows, MaxWorkspacesPerPass, MaxTasksPerWorkspace int
	ExaminedAtFirstSample, ExaminedDuringWindow, DispatcherAttemptsDuringWindow uint64
	ExaminedPerClaim, ObservationStartOffsetMS                                  float64
}

// Reconstruct exact due-to-wire occupancy from all 512 timestamp pairs,
// independent of sampling interval. At a common instant apply both events.
func queryCurveBacklog040(samples map[string]*queryCapacitySample) (peak, atLast int) {
	type event struct {
		when  time.Time
		delta int
	}
	events := make([]event, 0, 2*len(samples))
	var last time.Time
	for _, s := range samples {
		events = append(events, event{s.due, 1}, event{s.written, -1})
		if s.due.After(last) {
			last = s.due
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].when.Before(events[j].when) })
	queue := 0
	for i := 0; i < len(events); {
		tm := events[i].when
		for i < len(events) && events[i].when.Equal(tm) {
			queue += events[i].delta
			i++
		}
		peak = max(peak, queue)
		if !tm.After(last) {
			atLast = queue
		}
	}
	return peak, atLast
}

func TestQueryCurveBacklog040(t *testing.T) {
	base := time.Unix(100, 0)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Millisecond) }
	peak, last := queryCurveBacklog040(map[string]*queryCapacitySample{
		"a": {due: at(0), written: at(9)}, "b": {due: at(1), written: at(3)},
		"c": {due: at(2), written: at(4)}, "d": {due: at(4), written: at(4)},
	})
	if peak != 3 || last != 1 {
		t.Fatal("incorrect exact backlog reconstruction", peak, last)
	}
}

func TestPostgresOriginalQueryRateCurve040(t *testing.T) {
	if os.Getenv("STUDIO_MEDIA_QUERY_CURVE") != "1" {
		t.Skip("explicit local PostgreSQL high-rate fixture")
	}
	runOriginalQueryRateCurve(t, false)
}

func requireLocalQueryCurveDatabase(t *testing.T) {
	t.Helper()
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("local PostgreSQL fixture is not configured")
	}
	parsed, err := pgx.ParseConfig(source)
	if err != nil {
		t.Fatal("invalid local PostgreSQL configuration")
	}
	local := func(host string) bool { return isLoopbackHost(host) || strings.HasPrefix(host, "/") }
	if !local(parsed.Host) {
		t.Fatal("curve requires a local PostgreSQL endpoint")
	}
	for _, fallback := range parsed.Fallbacks {
		if !local(fallback.Host) {
			t.Fatal("curve requires local PostgreSQL fallback endpoints")
		}
	}
}

func runOriginalQueryRateCurve(t *testing.T, instrument bool) {
	t.Helper()
	requireLocalQueryCurveDatabase(t)
	var active atomic.Pointer[queryCapacityProbe]
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { active.Load().serve(w, r) }))
	defer gateway.Close()
	var originalIdentity string
	for _, workers := range []int{32, 64, 128} {
		t.Run(fmt.Sprintf("query_%03d", workers), func(t *testing.T) {
			dsn := testDatabase(t)
			admin := deliveryAdmin(t, dsn)
			probe := &queryCapacityProbe{samples: map[string]*queryCapacitySample{}, handlersDone: make(chan struct{})}
			active.Store(probe)
			config := largeQueryConfig040(dsn, gateway.URL, queryCapacityPoll)
			config.MediaScheduling.QueryWorkers = workers
			config.MediaScheduling.QueryWorkspaceConcurrency = 128
			config.MediaScheduling.QueryChannelConcurrency = 128
			var stages *queryStageSamples248
			if instrument {
				stages = &queryStageSamples248{}
				config.queryTiming = &mediaQueryTiming{record: stages.record}
			}
			a, _ := testApp(t, config)
			p := a.media.(*AsyncMediaProvider)
			if originalIdentity == "" {
				originalIdentity = p.providerIdentity
			}
			transport := p.queryClient.Transport.(*http.Transport)
			if originalIdentity != p.providerIdentity || transport.MaxConnsPerHost != workers || cap(a.taskLeases.connections[mediaQueryLane]) != 4 {
				t.Fatal("frozen channel identity or tested budgets differ")
			}
			p.queryClient.Transport = &queryCapacityTransport{base: transport, probe: probe}
			firstDue := time.Now().UTC().Truncate(time.Microsecond).Add(2 * time.Second)
			if stages != nil {
				stages.begin.Store(&firstDue)
			}
			prototypes := queryCapacityRecords(p, firstDue)
			records := make([]*taskRecord, queryCurveCount040)
			ids := make([]string, len(records))
			handles := make([]string, len(records))
			for i := range records {
				r := *prototypes[i%len(prototypes)]
				r.Task.ID = fmt.Sprintf("task_curve_%03d_%04d", workers, i)
				r.Handle.ID = fmt.Sprintf("asyncimg_curve_%03d_%04d", workers, i)
				r.NextPoll = firstDue.Add(time.Duration(i) * time.Second / queryCurveRate040)
				records[i] = &r
				ids[i] = r.Task.ID
				handles[i] = r.Handle.ID
				probe.samples[r.Handle.ID] = &queryCapacitySample{due: r.NextPoll}
			}
			seedQueryCapacity(t, a, admin, records)
			// Run the actual production page statements outside the timed window.
			// Per-pass page limits and the total examined counter are distinct.
			workspaceRows := outboxCount(t, admin, "SELECT count(*) FROM ("+mediaScheduleWorkspaceSelectSQL+") page", "query", "live", "", mediaScheduleWorkspacePage)
			taskRows := outboxCount(t, admin, "SELECT count(*) FROM ("+mediaScheduleTaskSelectSQL[mediaQueryLane]+") page", "live", records[0].Task.WorkspaceID, "", mediaScheduleTaskPage)
			if workspaceRows != 1 || taskRows != mediaScheduleTaskPage || !time.Now().Before(firstDue) {
				t.Fatal("invalid page cardinality or setup overran first arrival", workspaceRows, taskRows)
			}
			time.Sleep(time.Until(firstDue))
			started := time.Now()
			examinedAtStart := a.mediaScheduler.snapshot().Lanes[mediaQueryLane].ExaminedCandidates
			attemptsAtStart := a.mediaExecution.snapshot().QueryDispatchAttempts
			var peakExecution, peakScope int
			const samplePeriod = 10 * time.Millisecond
			tick := time.NewTicker(samplePeriod)
			defer tick.Stop()
			timeout := time.NewTimer(75 * time.Second)
			defer timeout.Stop()
		drain:
			for {
				select {
				case <-probe.handlersDone:
					break drain
				case <-a.ctx.Done():
					t.Fatal("controller stopped before the original requests completed")
				case <-timeout.C:
					t.Fatal("original requests did not drain")
				case <-tick.C:
					peakExecution = max(peakExecution, a.mediaExecution.snapshot().QueryInFlight)
					peakScope = max(peakScope, a.mediaScheduler.snapshot().Lanes[mediaQueryLane].ScopeInFlight)
				}
			}
			deadline := time.Now().Add(15 * time.Second)
			for !queryCapacityFinal(t, a, admin, ids, handles) {
				if time.Now().After(deadline) || a.ctx.Err() != nil {
					t.Fatal("missing original committed terminal")
				}
				time.Sleep(10 * time.Millisecond)
			}
			completed := time.Now()
			base := probe.results(t, "postgres", a, firstDue, completed)
			base.OfferedPerSecond = queryCurveRate040
			base.WorkspaceLimit = a.mediaScheduler.workspaceLimit(mediaQueryLane)
			base.ChannelLimit = a.mediaScheduler.channelLimit(mediaQueryLane)
			base.QueryConnections = transport.MaxConnsPerHost
			base.LeaseQueryConnections = cap(a.taskLeases.connections[mediaQueryLane])
			if err := admin.QueryRow(t.Context(), `SELECT current_setting('server_version'),current_setting('fsync'),current_setting('synchronous_commit')`).Scan(&base.PGVersion, &base.Fsync, &base.SynchronousCommit); err != nil {
				t.Fatal(err)
			}
			probe.Lock()
			peak, last := queryCurveBacklog040(probe.samples)
			probe.Unlock()
			report := queryCurveReport040{queryCapacityReport: base, ConfiguredWorkspaceLimit: config.MediaScheduling.QueryWorkspaceConcurrency, ConfiguredChannelLimit: config.MediaScheduling.QueryChannelConcurrency,
				SampledPeakExecutionSlots: peakExecution, SampledPeakScopeSlots: peakScope, OccupancySampleMS: int(samplePeriod.Milliseconds()), CompletionPercent: 100 * float64(base.Completed) / float64(len(records)),
				PeakDueAwaitingWire: peak, DueAwaitingWireAtLastArrival: last, RemainingAfterDrain: len(records) - base.Completed, DrainAfterLastArrivalMS: float64(completed.Sub(records[len(records)-1].NextPoll)) / float64(time.Millisecond),
				WorkspacePageRows: workspaceRows, TaskPageRows: taskRows, MaxWorkspacesPerPass: mediaScheduleWorkspacePage, MaxTasksPerWorkspace: mediaScheduleTaskPage,
				ExaminedAtFirstSample: examinedAtStart, ExaminedDuringWindow: base.Examined - examinedAtStart, DispatcherAttemptsDuringWindow: a.mediaExecution.snapshot().QueryDispatchAttempts - attemptsAtStart, ObservationStartOffsetMS: float64(started.Sub(firstDue)) / float64(time.Millisecond)}
			report.ExaminedPerClaim = float64(report.ExaminedDuringWindow) / float64(base.Claims)
			if stages != nil {
				// The measured window ended at the independent terminal SELECT.
				// Close in-flight sample intervals before clipping them to that window.
				a.cancel()
				a.wg.Wait()
				stages.report(t, firstDue, completed, probe, base)
			}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("MEDIA_QUERY_RATE_CURVE_040 %s", raw)
		})
	}
}
