package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// These fixed, open-loop arrivals are the first poll of previously accepted
// handles. The fixture returns a terminal status to make repeats detectable.
const (
	queryCapacityCount   = 128
	queryCapacityRate    = 40
	queryCapacityService = 200 * time.Millisecond
	queryCapacityPoll    = 20 * time.Millisecond
)

type queryCapacitySample struct {
	due, dispatch, written, arrived, responded, headers time.Time
	sends, writes, gets, responses                      int
}
type queryCapacityProbe struct {
	sync.Mutex
	samples                         map[string]*queryCapacitySample
	active, peak, posts, unexpected int
	handled                         int
	handlersDone                    chan struct{}
}

func (p *queryCapacityProbe) serve(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutPrefix(r.URL.Path, mediaQueryPrefix)
	p.Lock()
	s := p.samples[id]
	if r.Method != http.MethodGet || !ok || s == nil || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
		p.unexpected++
		if r.Method == http.MethodPost {
			p.posts++
		}
		p.Unlock()
		http.Error(w, "unexpected capacity request", http.StatusBadRequest)
		return
	}
	s.gets++
	s.arrived = time.Now()
	p.active++
	p.peak = max(p.peak, p.active)
	p.Unlock()
	defer func() {
		p.Lock()
		p.active--
		p.handled++
		if p.handled == len(p.samples) {
			close(p.handlersDone)
		}
		p.Unlock()
	}()
	select {
	case <-r.Context().Done():
		return
	case <-time.After(queryCapacityService):
	}
	p.Lock()
	s.responded = time.Now()
	s.responses++
	p.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"task_id": id, "media_type": "image", "stage": "failed"})
}

// Observe the actual HTTP client at the transport boundary. WroteRequest is
// downstream of connection acquisition; handler arrival has its own timestamp.
type queryCapacityTransport struct {
	base  http.RoundTripper
	probe *queryCapacityProbe
}

func (t *queryCapacityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	id, ok := strings.CutPrefix(r.URL.Path, mediaQueryPrefix)
	t.probe.Lock()
	s := t.probe.samples[id]
	if r.Method != http.MethodGet || !ok || s == nil {
		t.probe.unexpected++
		t.probe.Unlock()
		return nil, fmt.Errorf("unexpected capacity transport request")
	}
	s.dispatch = time.Now()
	s.sends++
	t.probe.Unlock()
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		t.probe.Lock()
		defer t.probe.Unlock()
		s.writes++
		if info.Err == nil {
			s.written = time.Now()
		}
	}}
	resp, err := t.base.RoundTrip(r.WithContext(httptrace.WithClientTrace(r.Context(), trace)))
	t.probe.Lock()
	s.headers = time.Now()
	t.probe.Unlock()
	return resp, err
}
func (t *queryCapacityTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

type queryCapacityDistribution struct {
	MeanMS, P50MS, P95MS, P99MS, MaxMS float64
}

func queryCapacityPercentiles(values []time.Duration) queryCapacityDistribution {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(f float64) float64 {
		return float64(values[int(math.Ceil(float64(len(values))*f))-1]) / float64(time.Millisecond)
	}
	var sum time.Duration
	for _, v := range values {
		sum += v
	}
	return queryCapacityDistribution{float64(sum) / float64(time.Millisecond) / float64(len(values)), percentile(.5), percentile(.95), percentile(.99), percentile(1)}
}

type queryCapacityReport struct {
	PGVersion, Fsync, SynchronousCommit                                                                    string `json:",omitempty"`
	LeaseQueryConnections                                                                                  int
	Storage, GoVersion, GOOS, GOARCH                                                                       string
	Workers, QueryConnections, WorkspaceLimit, ChannelLimit, Processors                                    int
	OfferedPerSecond, ServiceMS, IdlePollMS, Scheduled, Completed, UniqueGETs, PeakHTTP, Posts, Unexpected int
	WindowSeconds, CompletedPerSecond                                                                      float64
	ScheduleToDispatch, ScheduleToWire, WireToHeaders, ScheduleToHandler, HandlerService                   queryCapacityDistribution
	Examined, Claims, CapacityDeferrals                                                                    uint64
}

func (p *queryCapacityProbe) results(t *testing.T, storage string, a *App, firstDue, completed time.Time) queryCapacityReport {
	t.Helper()
	p.Lock()
	defer p.Unlock()
	workers := a.config.MediaScheduling.QueryWorkers
	if p.unexpected != 0 || p.posts != 0 || p.active != 0 || p.peak <= 0 || p.peak > workers {
		t.Fatalf("invalid GET accounting: unexpected=%d posts=%d active=%d peak=%d workers=%d", p.unexpected, p.posts, p.active, p.peak, workers)
	}
	var dispatch, wire, headers, handler, service []time.Duration
	for _, s := range p.samples {
		if s.sends != 1 || s.gets != 1 || s.writes != 1 || s.responses != 1 || s.dispatch.Before(s.due) || s.written.Before(s.dispatch) || s.headers.Before(s.written) || s.responded.Before(s.arrived.Add(queryCapacityService)) {
			t.Fatalf("missing, early, duplicated or incomplete original GET: sends=%d writes=%d gets=%d responses=%d", s.sends, s.writes, s.gets, s.responses)
		}
		dispatch = append(dispatch, s.dispatch.Sub(s.due))
		wire = append(wire, s.written.Sub(s.due))
		handler = append(handler, s.arrived.Sub(s.due))
		headers = append(headers, s.headers.Sub(s.written))
		service = append(service, s.responded.Sub(s.arrived))
	}
	stat := a.mediaScheduler.snapshot().Lanes[mediaQueryLane]
	lanes := a.mediaExecution.snapshot()
	if int(lanes.QuerySteps) != len(p.samples) || int(stat.Claims) != len(p.samples) || lanes.SubmitSteps != 0 {
		t.Fatalf("original GETs do not match actual query claims or a submit step occurred: queries=%d claims=%d submits=%d", lanes.QuerySteps, stat.Claims, lanes.SubmitSteps)
	}
	window := completed.Sub(firstDue).Seconds()
	return queryCapacityReport{Storage: storage, GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Processors: runtime.GOMAXPROCS(0),
		Workers: workers, QueryConnections: workers, WorkspaceLimit: a.config.MediaScheduling.WorkspaceConcurrency, ChannelLimit: a.config.MediaScheduling.ChannelConcurrency,
		OfferedPerSecond: queryCapacityRate, ServiceMS: int(queryCapacityService.Milliseconds()), IdlePollMS: int(queryCapacityPoll.Milliseconds()),
		Scheduled: len(p.samples), Completed: len(p.samples), UniqueGETs: len(p.samples), PeakHTTP: p.peak, Posts: p.posts, Unexpected: p.unexpected,
		WindowSeconds: window, CompletedPerSecond: float64(len(p.samples)) / window,
		ScheduleToDispatch: queryCapacityPercentiles(dispatch), ScheduleToWire: queryCapacityPercentiles(wire), WireToHeaders: queryCapacityPercentiles(headers),
		ScheduleToHandler: queryCapacityPercentiles(handler), HandlerService: queryCapacityPercentiles(service),
		Examined: stat.ExaminedCandidates, Claims: stat.Claims, CapacityDeferrals: stat.CapacityDeferrals}
}

func queryCapacityRecords(p *AsyncMediaProvider, firstDue time.Time) []*taskRecord {
	now := time.Now().UTC()
	records := make([]*taskRecord, queryCapacityCount)
	for i := range records {
		id := fmt.Sprintf("task_query_capacity_%03d", i)
		in := mediaFixtureInput("image", 1)
		in.Operation, in.ProjectID = "generate", "prj_demo"
		h := mediaFixtureHandle(p, "image", 1)
		h.ID = fmt.Sprintf("asyncimg_capacity_%03d", i)
		doc := Task{ID: id, WorkspaceID: "ws_demo", ProjectID: in.ProjectID, Operation: in.Operation, Prompt: in.Inputs.Prompt, Kind: in.Kind, ModelID: in.ModelID,
			ProviderMode: "live", Status: "RUNNING", Version: 1, Parameters: &in.Parameters, Artifacts: []Artifact{}, CreatedAt: now, UpdatedAt: now}
		records[i] = &taskRecord{Task: doc, Input: in, Handle: h, NextPoll: firstDue.Add(time.Duration(i) * time.Second / queryCapacityRate)}
	}
	return records
}

// Install the original handle and its due time atomically, without sending a
// submission, manually claiming by ID, advancing the clock or disabling triggers.
func seedQueryCapacity(t *testing.T, a *App, conn *pgx.Conn, records []*taskRecord) {
	t.Helper()
	if conn == nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, r := range records {
			a.tasks[r.Task.ID] = r
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	batch := &pgx.Batch{}
	for _, r := range records {
		batch.Queue(`INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
		 VALUES($1,$2,$3,$4,$5,$6,$7)`, r.Task.ID, r.Task.WorkspaceID, r.Task.ProjectID, r.Task, r.Input, r.Handle, r.NextPoll)
	}
	if err = tx.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
func queryCapacityFinal(t *testing.T, a *App, conn *pgx.Conn, ids, handles []string) bool {
	t.Helper()
	if conn == nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, id := range ids {
			r := a.tasks[id]
			if r == nil || r.Handle.ID != handles[i] || r.Task.Status != "FAILED" || r.Task.Error == nil || r.Task.Error.Code != "UPSTREAM_GENERATION_FAILED" || r.InFlight {
				return false
			}
		}
		return true
	}
	// This single SELECT at the end is independent of the cache. All original
	// IDs must have a committed terminal, released lease and inactive projection.
	var count int
	err := conn.QueryRow(context.Background(), `SELECT count(*) FROM unnest($1::text[], $2::text[]) s(task_id,handle_id)
		JOIN studio_tasks t ON t.id=s.task_id JOIN studio_task_schedule q ON q.task_id=t.id
		WHERE t.handle->>'ID'=s.handle_id AND t.document->>'status'='FAILED'
		AND t.document->'error'->>'code'='UPSTREAM_GENERATION_FAILED' AND t.lease_owner='' AND NOT q.active`, ids, handles).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count == len(ids)
}
func runQueryCapacity(t *testing.T, durable bool, workersList []int, dbConnections int) {
	t.Helper()
	if os.Getenv("STUDIO_MEDIA_QUERY_MEASURE") != "1" {
		t.Skip("opt-in original-handle capacity fixture")
	}
	for _, workers := range workersList {
		t.Run(fmt.Sprintf("workers_%02d", workers), func(t *testing.T) {
			dsn, storage := "", "memory"
			if durable {
				dsn, storage = testDatabase(t), "postgres"
			}
			probe := &queryCapacityProbe{samples: make(map[string]*queryCapacitySample), handlersDone: make(chan struct{})}
			server := httptest.NewServer(http.HandlerFunc(probe.serve))
			t.Cleanup(server.Close)
			c := Config{Mode: "live", DatabaseURL: dsn, AsyncImageBaseURL: server.URL, AsyncImageAPIKey: fixtureMediaKey, AsyncImageModel: "capacity-fixture",
				AsyncImageCredentialID: "capacity-fixture", PollInterval: queryCapacityPoll, MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()},
				MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: workers, QueryDBConnections: dbConnections, DeliveryWorkers: 1, ReviewWorkers: 1, WorkspaceConcurrency: 16, ChannelConcurrency: 16}}
			a, _ := testApp(t, c)
			p := a.media.(*AsyncMediaProvider)
			httpLimit := p.queryClient.Transport.(*http.Transport).MaxConnsPerHost
			if httpLimit != workers {
				t.Fatal("query HTTP limit differs from tested worker count", httpLimit)
			}
			p.queryClient.Transport = &queryCapacityTransport{base: p.queryClient.Transport, probe: probe}
			var conn *pgx.Conn
			if durable {
				var err error
				conn, err = pgx.Connect(context.Background(), dsn)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Close(context.Background()) })
			}
			firstDue := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
			records := queryCapacityRecords(p, firstDue)
			ids, handles := make([]string, len(records)), make([]string, len(records))
			probe.Lock()
			for i, r := range records {
				ids[i], handles[i] = r.Task.ID, r.Handle.ID
				probe.samples[r.Handle.ID] = &queryCapacitySample{due: r.NextPoll}
			}
			probe.Unlock()
			seedQueryCapacity(t, a, conn, records)
			if !time.Now().Before(firstDue) {
				t.Fatal("fixture setup exceeded fixed arrival lead time")
			}
			deadline := time.NewTimer(45 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-deadline.C:
					probe.Lock()
					n, peak := 0, probe.peak
					for _, s := range probe.samples {
						n += s.gets
					}
					probe.Unlock()
					t.Fatalf("GET capacity fixture timed out: gets=%d peak=%d", n, peak)
				case <-a.ctx.Done():
					t.Fatal("workbench shut down during query measurement")
				case <-tick.C:
					select {
					case <-probe.handlersDone:
					default:
						continue
					}
					if !queryCapacityFinal(t, a, conn, ids, handles) {
						continue
					}
					report := probe.results(t, storage, a, firstDue, time.Now())
					report.QueryConnections = httpLimit
					if conn != nil {
						report.LeaseQueryConnections = cap(a.taskLeases.connections[mediaQueryLane])
						if err := conn.QueryRow(t.Context(), `SELECT current_setting('server_version'),current_setting('fsync'),current_setting('synchronous_commit')`).Scan(&report.PGVersion, &report.Fsync, &report.SynchronousCommit); err != nil {
							t.Fatal(err)
						}
					}
					raw, err := json.Marshal(report)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("MEDIA_QUERY_CAPACITY %s", raw)
					return
				}
			}
		})
	}
}
func TestMediaOriginalQueryCapacity(t *testing.T)         { runQueryCapacity(t, false, []int{2, 16}, 0) }
func TestPostgresMediaOriginalQueryCapacity(t *testing.T) { runQueryCapacity(t, true, []int{2, 16}, 0) }

func TestPostgresPooledOriginalQueryCapacity(t *testing.T) { runQueryCapacity(t, true, []int{32}, 4) }
