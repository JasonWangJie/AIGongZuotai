package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const recurringKnown257 = 24
const recurringRounds257 = 3
const recurringWindow257 = 3 * time.Second

// Index zero is the accepted POST without a handle. All other fixture handles
// retain their original IDs throughout the run. Stop observing live tasks only
// after the final response requests a future poll.
func recurringReply257(index, round int) (progress int, terminal bool) {
	progress = 10
	if round >= 2 && index%4 == 0 {
		progress = 24
	}
	if round == recurringRounds257 && index%4 == 1 {
		progress = 37
	}
	return progress, round == recurringRounds257 && index%8 == 0
}

type recurringSample257 struct {
	queryCapacitySample
	effective, created time.Time
	handleHash, status string
	version, epoch     int64
	progress           int
}

type recurringTask257 struct {
	admissionCapacityRecord
	workspace, idFromHandle string
	submitted, ack, created time.Time
	ready                   chan struct{}
	samples                 []*recurringSample257
}

type recurringGateway257 struct {
	post   *admissionCapacityWire
	db     *pgx.Conn
	read   sync.Mutex
	mu     sync.Mutex
	tasks  [recurringKnown257 + 1]recurringTask257
	bad    int
	active int
	peak   int
}

func (g *recurringGateway257) invalid() {
	g.mu.Lock()
	g.bad++
	g.mu.Unlock()
}

// This independent read-only observer executes after handler arrival and
// before the response. The live worker still holds its lease. No SQL runs
// on the client path between selection, wire authorization and WroteRequest.
func (g *recurringGateway257) observe(ctx context.Context, task *recurringTask257, s *recurringSample257) error {
	g.read.Lock()
	defer g.read.Unlock()
	return g.db.QueryRow(ctx, `SELECT s.eligible_at,GREATEST(s.eligible_at,s.server_not_before,c.cooldown_until),
	 (t.document->>'created_at')::timestamptz,md5(t.handle::text),t.document->>'status',
	 (t.document->>'version')::bigint,t.lease_epoch,(t.document->>'progress')::int
	 FROM studio_tasks t JOIN studio_task_schedule s ON s.task_id=t.id
	 JOIN studio_media_channel_runtime c ON c.scope='gateway' AND c.lane=s.lane AND c.channel_key=s.channel_key
	 WHERE t.id=$1 AND t.workspace_id=$2 AND t.project_id=$3 AND t.handle->>'ID'=$4
	 AND s.active AND s.lane='query' AND s.provider_mode='live' AND s.workspace_id=t.workspace_id
	 AND t.handle->>'ProviderIdentity'=s.channel_key AND t.submission->>'provider_identity'=s.channel_key
	 AND t.handle->>'CredentialID'=t.submission->>'credential_id' AND t.handle->>'Kind'='image'
	 AND t.handle->>'Count'=t.submission->>'count' AND t.handle->>'SubmittedAt'=t.submission->>'prepared_at'
	 AND t.lease_owner<>'' AND t.lease_until>clock_timestamp() AND t.next_poll=s.eligible_at`,
		task.id, task.workspace, task.input.ProjectID, task.idFromHandle).Scan(&s.due, &s.effective, &s.created, &s.handleHash, &s.status, &s.version, &s.epoch, &s.progress)
}

func (g *recurringGateway257) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		g.post.ServeHTTP(w, r)
		return
	}
	at := time.Now()
	i, ok := admissionCapacityIndex(r.URL.Path, mediaQueryPrefix+"fixture246-", len(g.tasks))
	if r.Method != http.MethodGet || !ok || i == 0 || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
		g.invalid()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	task := &g.tasks[i]
	g.mu.Lock()
	round := len(task.samples)
	if round == 0 || task.samples[round-1].gets != 0 {
		g.bad++
		g.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s := task.samples[round-1]
	s.gets++
	s.arrived = at
	g.active++
	g.peak = max(g.peak, g.active)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.active--
		g.mu.Unlock()
	}()
	select {
	case <-r.Context().Done():
		return
	case <-task.ready:
	}
	if err := g.observe(r.Context(), task, s); err != nil {
		g.invalid()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	select {
	case <-r.Context().Done():
		return
	case <-time.After(time.Until(at.Add(queryCapacityService))):
	}
	progress, terminal := recurringReply257(i, round)
	stage := "generating"
	if terminal {
		stage = "failed"
	} else if round == recurringRounds257 {
		w.Header().Set("Retry-After", "60")
	}
	w.Header().Set("Content-Type", "application/json")
	g.mu.Lock()
	s.responded = time.Now()
	s.responses++
	g.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"task_id": task.idFromHandle, "media_type": "image", "stage": stage, "progress": progress})
}

type recurringTransport257 struct {
	*http.Transport
	gateway *recurringGateway257
}

func (t *recurringTransport257) RoundTrip(r *http.Request) (*http.Response, error) {
	g := t.gateway
	i, ok := admissionCapacityIndex(r.URL.Path, mediaQueryPrefix+"fixture246-", len(g.tasks))
	g.mu.Lock()
	if !ok || i == 0 || r.Method != http.MethodGet || r.URL.RawQuery != "" || len(g.tasks[i].samples) >= recurringRounds257 {
		g.bad++
		g.mu.Unlock()
		return nil, errors.New("unexpected repeated-query transport request")
	}
	s := &recurringSample257{}
	s.sends = 1
	s.dispatch = time.Now()
	g.tasks[i].samples = append(g.tasks[i].samples, s)
	g.mu.Unlock()
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		g.mu.Lock()
		defer g.mu.Unlock()
		s.writes++
		if info.Err == nil {
			s.written = time.Now()
		}
	}}
	resp, err := t.Transport.RoundTrip(r.WithContext(httptrace.WithClientTrace(r.Context(), trace)))
	g.mu.Lock()
	s.headers = time.Now()
	g.mu.Unlock()
	return resp, err
}

type recurringPeriod257 struct {
	Number, GETs, Advanced, Unchanged, Terminal                    int
	EligibleToWire, EffectiveToWire, WireToHeaders, HandlerService queryCapacityDistribution
}

type recurringBucket257 struct {
	Window                       int
	StartMS, EndMS               float64
	Eligible, Written, Responded int
}

type recurringReport257 struct {
	Workspaces, QueryWorkers, QueryDBConnections, ServiceMS, WindowMS                                                      int
	Accepted, UniquePOST, UnknownWithoutHandle, KnownHandles, GETs, Terminals, Running, RepeatedPOST, Unexpected, PeakHTTP int
	AdmissionWindowMS, QueryWindowMS, GETsPerSecond                                                                        float64
	API202Latency, CreatedToFirstWire                                                                                      queryCapacityDistribution
	Rounds                                                                                                                 []recurringPeriod257
	Windows                                                                                                                []recurringBucket257
}

func recurringBuckets257(first, end time.Time, samples []*recurringSample257) ([]recurringBucket257, error) {
	if !end.After(first) {
		return nil, errors.New("invalid observation window")
	}
	n := int(end.Sub(first)/recurringWindow257) + 1
	buckets := make([]recurringBucket257, n)
	for i := range buckets {
		start := time.Duration(i) * recurringWindow257
		buckets[i] = recurringBucket257{Window: i + 1, StartMS: float64(start) / float64(time.Millisecond), EndMS: float64(min(start+recurringWindow257, end.Sub(first))) / float64(time.Millisecond)}
	}
	for _, s := range samples {
		if s.due.Before(first) || s.written.Before(s.due) || s.responded.Before(s.due) || s.responded.After(end) || s.written.After(end) {
			return nil, errors.New("sample outside its real observation window")
		}
		buckets[int(s.due.Sub(first)/recurringWindow257)].Eligible++
		buckets[int(s.written.Sub(first)/recurringWindow257)].Written++
		buckets[int(s.responded.Sub(first)/recurringWindow257)].Responded++
	}
	return buckets, nil
}

func TestRecurringQueryWindow257(t *testing.T) {
	first := time.Unix(1, 0)
	s := &recurringSample257{queryCapacitySample: queryCapacitySample{due: first, written: first.Add(3 * time.Second), responded: first.Add(3200 * time.Millisecond)}}
	b, err := recurringBuckets257(first, first.Add(3250*time.Millisecond), []*recurringSample257{s})
	if err != nil || len(b) != 2 || b[0].Eligible != 1 || b[0].Written != 0 || b[1].Written != 1 || b[1].Responded != 1 || b[1].EndMS != 3250 {
		t.Fatal("real window boundary or last partial window differed", err)
	}
	s.written = first.Add(-time.Microsecond)
	if _, err := recurringBuckets257(first, first.Add(4*time.Second), []*recurringSample257{s}); err == nil {
		t.Fatal("early write accepted")
	}
}

func (g *recurringGateway257) report(t *testing.T, end time.Time, workers, connections int) recurringReport257 {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.post.mu.Lock()
	defer g.post.mu.Unlock()
	if g.active != 0 || g.bad != 0 || g.post.invalid != 0 || g.post.posts != len(g.tasks) || g.peak == 0 || g.peak > workers {
		t.Fatalf("invalid final wire counts: active=%d rejects=%d invalidPOST=%d posts=%d peak=%d", g.active, g.bad, g.post.invalid, g.post.posts, g.peak)
	}
	result := recurringReport257{Workspaces: 2, QueryWorkers: workers, QueryDBConnections: connections, ServiceMS: int(queryCapacityService.Milliseconds()), WindowMS: int(recurringWindow257.Milliseconds()), Accepted: len(g.tasks), UniquePOST: g.post.posts, UnknownWithoutHandle: 1, KnownHandles: recurringKnown257, PeakHTTP: g.peak}
	var api, firstWire []time.Duration
	var all []*recurringSample257
	var first time.Time
	for i := range g.tasks {
		task := &g.tasks[i]
		if task.id == "" || g.post.rows[i].key == "" || task.ack.Before(task.submitted) || task.created.Before(task.submitted.Add(-time.Microsecond)) || task.created.After(task.ack) || i == 0 && len(task.samples) != 0 || i > 0 && len(task.samples) != recurringRounds257 {
			t.Fatal("admission receipt, POST or original GET count differed")
		}
		api = append(api, task.ack.Sub(task.submitted))
		if i > 0 {
			if first.IsZero() || task.samples[0].due.Before(first) {
				first = task.samples[0].due
			}
			firstWire = append(firstWire, task.samples[0].written.Sub(task.created))
		}
	}
	for round := 1; round <= recurringRounds257; round++ {
		bin := recurringPeriod257{Number: round}
		var due, effective, httpTimes, service []time.Duration
		for i := 1; i < len(g.tasks); i++ {
			task := &g.tasks[i]
			s := task.samples[round-1]
			if s.sends != 1 || s.writes != 1 || s.gets != 1 || s.responses != 1 || s.created.Sub(task.created).Abs() > time.Microsecond ||
				s.status != "QUEUED" && round == 1 || s.status != "RUNNING" && round > 1 || s.created.After(s.due) || s.effective.Before(s.due) ||
				s.written.Before(s.dispatch) || s.written.Before(s.effective) || s.headers.Before(s.written) || s.responded.Before(s.arrived.Add(queryCapacityService)) {
				t.Fatalf("missing, early or incomplete observations for round %d", round)
			}
			if round > 1 {
				prior := task.samples[round-2]
				expected, _ := recurringReply257(i, round-1)
				// The production three-second floor is based on a response observed
				// after the fixture's response stamp. Allow DB microsecond precision.
				if s.due.Add(time.Microsecond).Before(prior.responded.Add(3*time.Second)) || s.version != prior.version+1 || s.epoch <= prior.epoch || s.handleHash != prior.handleHash || s.progress != expected {
					t.Fatalf("a later poll changed identity, skipped a committed response or shortened the live interval in round %d", round)
				}
			}
			progress, terminal := recurringReply257(i, round)
			switch {
			case terminal:
				bin.Terminal++
			case progress > s.progress:
				bin.Advanced++
			case progress == s.progress:
				bin.Unchanged++
			default:
				t.Fatal("progress regressed")
			}
			due = append(due, s.written.Sub(s.due))
			effective = append(effective, s.written.Sub(s.effective))
			httpTimes = append(httpTimes, s.headers.Sub(s.written))
			service = append(service, s.responded.Sub(s.arrived))
			all = append(all, s)
			bin.GETs++
		}
		bin.EligibleToWire = queryCapacityPercentiles(due)
		bin.EffectiveToWire = queryCapacityPercentiles(effective)
		bin.WireToHeaders = queryCapacityPercentiles(httpTimes)
		bin.HandlerService = queryCapacityPercentiles(service)
		result.Rounds = append(result.Rounds, bin)
		result.Terminals += bin.Terminal
	}
	result.Running = recurringKnown257 - result.Terminals
	result.GETs = len(all)
	result.API202Latency = queryCapacityPercentiles(api)
	result.CreatedToFirstWire = queryCapacityPercentiles(firstWire)
	result.AdmissionWindowMS = float64(g.tasks[len(g.tasks)-1].ack.Sub(g.tasks[0].submitted)) / float64(time.Millisecond)
	result.QueryWindowMS = float64(end.Sub(first)) / float64(time.Millisecond)
	result.GETsPerSecond = float64(result.GETs) / end.Sub(first).Seconds()
	var err error
	result.Windows, err = recurringBuckets257(first, end, all)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Read the final business state, its original acceptance receipt and its
// scheduling projection after all reported replies have actually committed.
func (g *recurringGateway257) checkFinal(t *testing.T, db *pgx.Conn) {
	t.Helper()
	for i := range g.tasks {
		task := &g.tasks[i]
		var status, handle, digest, code, lane string
		var active, owned, original bool
		var version int64
		var progress int
		var nextDue time.Time
		post := g.post.rows[i]
		err := db.QueryRow(t.Context(), `SELECT t.document->>'status',coalesce(t.handle->>'ID',''),md5(t.handle::text),coalesce(t.document->'error'->>'code',''),s.lane,
		 s.active,t.lease_owner<>'' OR t.lease_until IS NOT NULL,(t.document->>'version')::bigint,(t.document->>'progress')::int,s.eligible_at,
		 t.submission->>'idempotency_key'=$4 AND t.submission->>'body_sha256'=$5 AND k.scope_key=$6
		 FROM studio_tasks t JOIN studio_task_schedule s ON s.task_id=t.id JOIN studio_idempotency k ON k.resource_id=t.id AND k.resource_type='task'
		 WHERE t.id=$1 AND t.workspace_id=$2 AND t.project_id=$3`, task.id, task.workspace, task.input.ProjectID, post.key, post.hash, task.workspace+":tasks:"+task.key).Scan(&status, &handle, &digest, &code, &lane, &active, &owned, &version, &progress, &nextDue, &original)
		if err != nil || owned || !original {
			t.Fatal("missing final scoped receipt or outstanding lease", err)
		}
		if i == 0 {
			if status != "SUBMIT_UNKNOWN" || handle != "" || lane != "submit" || active {
				t.Fatal("handleless acceptance became executable")
			}
			continue
		}
		s := task.samples[recurringRounds257-1]
		expected, terminal := recurringReply257(i, recurringRounds257)
		if handle != task.idFromHandle || digest != s.handleHash || version != s.version+1 || progress != expected || lane != "query" || active == terminal {
			t.Fatal("final committed reply lost its original identity or version")
		}
		if terminal && (status != "FAILED" || code != "UPSTREAM_GENERATION_FAILED") || !terminal && (status != "RUNNING" || nextDue.Before(s.responded.Add(59*time.Second))) {
			t.Fatal("committed terminal or running task with future poll differed")
		}
	}
}

func TestPostgresRecurringOriginalQuery257(t *testing.T) {
	switch os.Getenv("STUDIO_MEDIA_QUERY_RECURRING") {
	case "":
		t.Skip("set STUDIO_MEDIA_QUERY_RECURRING=1 for the local repeated original-GET fixture")
	case "1":
	default:
		t.Fatal("invalid repeated-query fixture switch")
	}
	if !admissionCapacityLocalDSN(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("repeated-query fixture requires an explicit loopback database")
	}
	dsn := testDatabase(t)
	db := deliveryAdmin(t, dsn)
	g := &recurringGateway257{post: &admissionCapacityWire{rows: make([]admissionCapacityWireRow, recurringKnown257+1)}, db: deliveryAdmin(t, dsn)}
	for i := range g.tasks {
		g.tasks[i].ready = make(chan struct{})
		g.tasks[i].idFromHandle = fmt.Sprintf("fixture246-%d", i)
	}
	gateway := httptest.NewServer(g)
	t.Cleanup(gateway.Close)
	cfg := largeQueryConfig040(dsn, gateway.URL, queryCapacityPoll)
	cfg.AuthMode = "required"
	cfg.AssetDir, cfg.ReferenceDir = t.TempDir(), t.TempDir()
	cfg.AsyncImageModel = "fixture246-image"
	cfg.MediaScheduling.QueryWorkers = 64
	a, api, closeApp := admissionCapacityOpen(t, cfg)
	provider := a.media.(*AsyncMediaProvider)
	provider.queryClient.Transport = &recurringTransport257{provider.queryClient.Transport.(*http.Transport), g}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	accounts := admissionCapacityRegister(t, ctx, api.URL, 2)
	records := make([]admissionCapacityRecord, 0, len(g.tasks))
	for i := range g.tasks {
		account := accounts[i%len(accounts)]
		task := &g.tasks[i]
		in := taskInput("image")
		in.ProjectID = account.identity.Project.ID
		in.Inputs.Prompt = task.idFromHandle
		task.admissionCapacityRecord = admissionCapacityRecord{key: fmt.Sprintf("recurring-257-%d", i), input: in, owner: i % len(accounts)}
		task.workspace = account.identity.Workspace.ID
		var accepted Task
		task.submitted = time.Now()
		headers := account.request(t, ctx, api.URL, http.MethodPost, "/api/v1/tasks", task.key, in, &accepted, http.StatusAccepted)
		task.ack = time.Now()
		if headers.Get("Idempotency-Replayed") != "" || accepted.ID == "" || accepted.WorkspaceID != task.workspace || accepted.ProjectID != in.ProjectID || accepted.ProviderMode != "live" || accepted.Version != 1 {
			t.Fatal("initial 202 did not identify the original scoped task")
		}
		task.id, task.created = accepted.ID, accepted.CreatedAt
		records = append(records, task.admissionCapacityRecord)
		close(task.ready)
	}
	admissionCapacityReplay(t, ctx, api.URL, accounts, records)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var committed int
		err := db.QueryRow(ctx, `SELECT count(*) FROM studio_tasks t JOIN studio_task_schedule s ON s.task_id=t.id WHERE t.lease_owner='' AND (
		 (t.document->>'status' IN ('SUBMIT_UNKNOWN','FAILED') AND NOT s.active)
		 OR (t.document->>'status'='RUNNING' AND s.active AND s.server_not_before>clock_timestamp()+interval '20 seconds'))`).Scan(&committed)
		if err != nil {
			t.Fatal("final commit observer failed")
		}
		g.mu.Lock()
		invalid := g.bad
		g.mu.Unlock()
		if invalid > 0 {
			t.Fatal("a real query could not be matched to its original leased schedule")
		}
		if committed == len(g.tasks) {
			break
		}
		select {
		case <-a.ctx.Done():
			t.Fatal("controller stopped before the last committed observation")
		case <-ctx.Done():
			t.Fatalf("timed out after observing %d final commits", committed)
		case <-ticker.C:
		}
	}
	end := time.Now()
	// Keep workers live beyond one real polling floor to expose a late
	// duplicate POST/GET. The quiet period is outside the reported rate.
	select {
	case <-time.After(3250 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("verification window expired")
	case <-a.ctx.Done():
		t.Fatal("controller stopped before the quiet window ended")
	}
	closeApp()
	r := g.report(t, end, cfg.MediaScheduling.QueryWorkers, cfg.MediaScheduling.QueryDBConnections)
	g.checkFinal(t, db)
	stats := a.mediaScheduler.snapshot().Lanes[mediaQueryLane]
	lanes := a.mediaExecution.snapshot()
	if stats.Claims != recurringKnown257*recurringRounds257 || lanes.QuerySteps != stats.Claims || lanes.SubmitSteps != uint64(len(records)) {
		t.Fatal("production claims, original GETs or initial POSTs differed")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEDIA_QUERY_RECURRING_257 %s", raw)
}
