package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type childConversation struct {
	scope    capacityScope
	run      Run
	url, key string
	input    ConversationRunInput
}

func childAcceptTurn(t *testing.T, ctx context.Context, base string, scope capacityScope, index int) childConversation {
	t.Helper()
	var c Conversation
	capacityRequest(t, ctx, scope, "POST", base+"/api/v1/conversations", newID("key"), ConversationInput{ProjectID: scope.identity.Project.ID, Title: "Execution fixture"}, &c, 201)
	body := conversationInput(c.Version, nil, activePrompt(index))
	body.ModelID = childUpstream(index)
	var quote BillingQuote
	capacityRequest(t, ctx, scope, "POST", base+"/api/v1/billing/quotes", "", map[string]any{"resource_type": "conversation_run", "conversation_id": c.ID, "conversation_input": body}, &quote, 201)
	if quote.ID == "" || quote.ConversationID != c.ID || quote.ProjectID != scope.identity.Project.ID || quote.ReserveMicros <= 5 {
		t.Fatal("invalid bound conversation quote")
	}
	body.QuoteID = quote.ID
	out := childConversation{scope: scope, url: "/api/v1/conversations/" + c.ID + "/runs", input: body, key: newID("key")}
	capacityRequest(t, ctx, scope, "POST", base+out.url, out.key, body, &out.run, 201)
	if out.run.ID == "" || out.run.ConversationID != c.ID || out.run.Billing == nil || out.run.Billing.QuoteID != quote.ID {
		t.Fatal("conversation admission lost original resource or reservation")
	}
	return out
}
func (c childConversation) replay(t *testing.T, ctx context.Context, base string) {
	t.Helper()
	var got Run
	capacityRequest(t, ctx, c.scope, "POST", base+c.url, c.key, c.input, &got, 201)
	if got.ID != c.run.ID || got.UserMessageID != c.run.UserMessageID || got.AssistantMessageID != c.run.AssistantMessageID || got.Billing == nil || got.Billing.ReservationID != c.run.Billing.ReservationID {
		t.Fatal("original key did not identify the original run, branch and reservation")
	}
}
func (c childConversation) wait(t *testing.T, ctx context.Context, base, status string, seq uint64) Run {
	t.Helper()
	end := time.Now().Add(30 * time.Second)
	for time.Now().Before(end) {
		var r Run
		capacityRequest(t, ctx, c.scope, "GET", base+"/api/v1/runs/"+c.run.ID+"/snapshot", "", nil, &r, 200)
		if r.Status == status && r.Seq >= seq {
			return r
		}
		if terminal(r.Status) {
			t.Fatalf("unexpected observed state=%s seq=%d", r.Status, r.Seq)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run state transition timed out")
	return Run{}
}

type childFixture struct {
	dsn, asset, reference, catalog, prices string
	scopes                                 []capacityScope
}

func TestChatChildAcceptanceQueuedSnapshot(t *testing.T) {
	app, server := testApp(t, Config{ChatScheduling: ChatSchedulingConfig{Workers: 1}})
	app.chat = &conversationChatFixture{hold: true}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	scope := capacityScope{client: server.Client(), identity: testIdentity{Project: Project{ID: "prj_demo"}}}
	create := func() childConversation {
		conversation := memoryConversation(t, app)
		input := conversationInput(conversation.Version, nil, "queued snapshot fixture")
		var run Run
		capacityRequest(t, ctx, scope, "POST", server.URL+"/api/v1/conversations/"+conversation.ID+"/runs", newID("key"), input, &run, 201)
		return childConversation{scope: scope, run: run}
	}
	occupied := create()
	executionState(t, app, 0, 1)
	queued := create()
	snapshot := queued.wait(t, ctx, server.URL, "QUEUED", 0)
	if snapshot.ID != queued.run.ID || snapshot.ID == occupied.run.ID || snapshot.ProjectID != scope.identity.Project.ID {
		t.Fatal("snapshot did not identify the queued conversation run and its project")
	}
}

// Register normal accounts before the measurement; fund each with the existing
// offline preview and digest-checked apply while its control writer is stopped.
func prepareChildFixture(t *testing.T, ctx context.Context) childFixture {
	t.Helper()
	f := childFixture{dsn: testDatabase(t), asset: t.TempDir(), reference: t.TempDir(), scopes: make([]capacityScope, 5)}
	catalog := customCatalogFixture()
	first := catalog.Models[0]
	catalog.Models = nil
	prices := pricingFile{SchemaVersion: 1, ProviderMode: "live", Unit: LedgerUnit, Scale: LedgerScale}
	for i := range 4 {
		m := first
		m.ID, m.UpstreamModel = childUpstream(i), childUpstream(i)
		m.Capabilities.ContextWindowTokens, m.Capabilities.MaxOutputTokens = 2048, 128
		catalog.Models = append(catalog.Models, m)
		prices.Rates = append(prices.Rates, pricingRate{ModelID: m.ID, Kind: "chat", Operation: "chat", InputPerMillionMicros: "1000000", OutputPerMillionMicros: "1000000"})
	}
	f.catalog = writeCatalogFixture(t, catalog)
	f.prices = filepath.Join(t.TempDir(), "fixture-prices.json")
	raw, _ := json.Marshal(prices)
	if err := os.WriteFile(f.prices, raw, 0600); err != nil {
		t.Fatal("price fixture write failed")
	}
	app, err := New(Config{DatabaseURL: f.dsn, AuthMode: "required", AssetDir: f.asset, ReferenceDir: f.reference})
	if err != nil {
		t.Fatal("isolated registration service failed")
	}
	func() {
		server := httptest.NewServer(app.Handler())
		defer app.Close()
		defer server.Close()
		for i := range f.scopes {
			c := authClient(t)
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.Proxy = nil
			tr.MaxIdleConnsPerHost = 30
			t.Cleanup(tr.CloseIdleConnections)
			c.Transport = tr
			c.Timeout = 0
			var id testIdentity
			capacityRequest(t, ctx, capacityScope{client: c}, "POST", server.URL+"/api/v1/auth/register", "", map[string]string{"email": fmt.Sprintf("child-%d@example.test", i), "name": "Fixture", "password": newID("password")}, &id, 201)
			f.scopes[i] = capacityScope{client: c, identity: id}
		}
	}()
	for _, s := range f.scopes {
		cfg := LedgerAdminConfig{DatabaseURL: f.dsn, Action: "grant", Credit: LedgerCreditRequest{WorkspaceID: s.identity.Workspace.ID, AmountMicros: 1_000_000, OperationKey: newID("grant"), EvidenceRef: "fixture:child", Reason: "local-fixture"}}
		p, err := AdministerLedger(ctx, cfg)
		if err != nil {
			t.Fatal("offline fixture credit preview failed")
		}
		cfg.Apply, cfg.PlanDigest = true, p.PlanDigest
		if _, err = AdministerLedger(ctx, cfg); err != nil {
			t.Fatal("offline fixture credit grant failed")
		}
	}
	return f
}

func (f childFixture) settings(g *childAcceptanceGateway, origin string) map[string]string {
	n := g.normal
	return map[string]string{
		"DATABASE_URL": f.dsn, "AUTH_MODE": "required", "ASSET_DIR": f.asset, "REFERENCE_DIR": f.reference,
		"PROVIDER_MODE": "live", "NEWAPI_BASE_URL": origin + "/v1", "NEWAPI_API_KEY": g.key, "MODEL_CATALOG_FILE": f.catalog,
		"BILLING_MODE": "credits", "BILLING_PRICE_FILE": f.prices, "RUN_REPLAY_READ_CONNECTIONS": "2",
		"CHAT_CHILD_PROCESSES": "2", "CHAT_CHILD_SLOTS": strconv.Itoa(n / 2), "CHAT_WORKERS": "1", "CHAT_QUEUE_SIZE": "12", "CHAT_WORKSPACE_QUEUE_SIZE": "3",
		"CHAT_WORKSPACE_CONCURRENCY": strconv.Itoa(max(2, n/5)), "CHAT_MODEL_CONCURRENCY": strconv.Itoa(max(2, n/4)),
		"ADMISSION_WORKSPACE_RUNS": "32", "ADMISSION_MODEL_RUNS": "32", "ADMISSION_WORKSPACE_STREAMS": "32",
	}
}

func childGate(t *testing.T, ctx context.Context, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(25 * time.Second):
		t.Fatal("fixture observation timed out")
	case <-ctx.Done():
		t.Fatal("fixture deadline")
	}
}

func assertChildLease(t *testing.T, ctx context.Context, admin *pgx.Conn, r childConversation, model, generation string) string {
	t.Helper()
	var owner, gen string
	var active bool
	err := admin.QueryRow(ctx, `SELECT h.lease_owner,h.lease_generation,h.lease_until>clock_timestamp()
 AND h.lease_required AND h.lease_epoch>0 AND h.execution_id<>'' AND NOT h.closed
 AND c.snapshot->>'upstream_model'=$2 AND v.document->>'status'='RUNNING' AND v.workspace_id=$3 AND v.project_id=$4
 FROM studio_run_streams h JOIN studio_runs v ON v.id=h.run_id JOIN studio_chat_contexts c ON c.run_id=h.run_id WHERE h.run_id=$1`,
		r.run.ID, model, r.scope.identity.Workspace.ID, r.scope.identity.Project.ID).Scan(&owner, &gen, &active)
	if err != nil {
		capacityDBFailure(t, "active_child_lease", err)
	}
	if !active || owner == "" || gen != generation {
		t.Fatal("physical execution did not have the bound active lease and frozen context")
	}
	return owner
}

func childLedgerState(t *testing.T, admin *pgx.Conn, r childConversation, state string, started bool, charged int64) {
	t.Helper()
	if outboxCount(t, admin, `SELECT count(*) FROM studio_ledger_reservations WHERE resource_id=$1 AND workspace_id=$2 AND project_id=$3
 AND quote_id=$4 AND id=$5 AND state=$6 AND started=$7 AND charged_micros=$8
 AND (state<>'REVIEW' OR released_micros=0) AND (state NOT IN ('RELEASED','SETTLED') OR released_micros+charged_micros=reserved_micros)`, r.run.ID, r.scope.identity.Workspace.ID, r.scope.identity.Project.ID, r.input.QuoteID, r.run.Billing.ReservationID, state, started, charged) != 1 {
		t.Fatalf("original reservation disagrees with execution state=%s", state)
	}
}

func verifyChildJournal(t *testing.T, ctx context.Context, admin *pgx.Conn, c childConversation, lane int, base string) {
	t.Helper()
	var high, floor, events, distinct int
	var output, deltas, status string
	var closed bool
	err := admin.QueryRow(ctx, `SELECT h.head_seq,h.floor_seq,h.closed,r.document->>'status',r.document->>'output',count(e.seq),
 count(DISTINCT e.seq),string_agg(e.delta,'' ORDER BY e.seq) FROM studio_runs r JOIN studio_run_streams h ON h.run_id=r.id
 JOIN studio_run_stream_events e ON e.run_id=r.id WHERE r.id=$1 AND r.workspace_id=$2 AND r.project_id=$3 GROUP BY r.id,h.run_id`, c.run.ID, c.scope.identity.Workspace.ID, c.scope.identity.Project.ID).Scan(&high, &floor, &closed, &status, &output, &events, &distinct, &deltas)
	if err != nil {
		capacityDBFailure(t, "child_persisted_journal", err)
	}
	var want strings.Builder
	for step := range activeStreamSteps {
		want.WriteString(activeDelta(lane, step))
	}
	if high != activeStreamSteps+1 || floor != 0 || events != high || distinct != high || !closed || status != "COMPLETED" || output != want.String() || deltas != output {
		t.Fatal("stored output or ordered journal differs")
	}
	childLedgerState(t, admin, c, "SETTLED", true, 5)
	res := capacityOpen(t, ctx, c.scope, base, c.run.ID)
	err = readCapacityStream(res.Body, c.run.ID, lane, func(int, time.Time) error { return nil })
	res.Body.Close()
	if err != nil {
		t.Fatal("replayed journal did not match originally read sequence")
	}
}

func verifyStoppedChildReplay(t *testing.T, ctx context.Context, base string, run childConversation, lane int, status string) {
	t.Helper()
	response := capacityOpen(t, ctx, run.scope, base, run.run.ID)
	defer response.Body.Close()
	var seq uint64
	err := visitCapacitySSE(response.Body, func(e capacityEvent) error {
		seq++
		if e.ID != seq || e.Data.RunID != run.run.ID {
			return fmt.Errorf("incorrect stopped resource sequence")
		}
		switch seq {
		case 1:
			if e.Name == "message.delta" && e.Data.Delta == activeDelta(lane, 0) {
				return nil
			}
		case 2:
			if e.Name == "run."+strings.ToLower(status) && e.Data.Status == status {
				return nil
			}
		}
		return fmt.Errorf("incorrect stopped resource event")
	})
	if err != nil || seq != 2 {
		t.Fatalf("stopped prefix/terminal replay disagrees with committed %s state", status)
	}
}

func observeChildFallback(t *testing.T, ctx context.Context, f childFixture, g *childAcceptanceGateway, port int, s *childAcceptanceServer, admin *pgx.Conn) {
	t.Helper()
	base := g.normal + 5
	var running [2]childConversation
	var peers [2]int
	var generation string
	if err := admin.QueryRow(ctx, "SELECT generation FROM studio_run_executor WHERE singleton").Scan(&generation); err != nil {
		capacityDBFailure(t, "recovery_generation", err)
	}
	for i := range running {
		lane := base + i
		running[i] = childAcceptTurn(t, ctx, s.base, f.scopes[i], lane)
		childGate(t, ctx, g.lanes[lane].entered)
		running[i].wait(t, ctx, s.base, "RUNNING", 1)
		g.mu.Lock()
		peers[i] = g.lanes[lane].peerPort
		g.mu.Unlock()
	}
	processes := childOwners(t, ctx, s, port, peers[:])
	if processes[peers[0]] == processes[peers[1]] {
		t.Fatal("one-slot crash phase requires two independent processes")
	}
	if assertChildLease(t, ctx, admin, running[0], childUpstream(base), generation) == assertChildLease(t, ctx, admin, running[1], childUpstream(base+1), generation) {
		t.Fatal("one-slot processes did not own distinct leases")
	}
	var queued [2]childConversation
	requireQueued := func(c childConversation, lane int) {
		t.Helper()
		c.wait(t, ctx, s.base, "QUEUED", 0)
		if outboxCount(t, admin, `SELECT count(*) FROM studio_run_streams WHERE run_id=$1 AND execution_id='' AND lease_epoch=0 AND lease_owner='' AND NOT closed`, c.run.ID) != 1 {
			t.Fatal("waiting resource already had execution evidence")
		}
		g.mu.Lock()
		posts := g.lanes[lane].posts
		g.mu.Unlock()
		if posts != 0 {
			t.Fatal("waiting resource issued a model request")
		}
		childLedgerState(t, admin, c, "HELD", false, 0)
	}
	for i := range queued {
		lane := base + 2 + i
		queued[i] = childAcceptTurn(t, ctx, s.base, f.scopes[2], lane)
		requireQueued(queued[i], lane)
		queued[i].replay(t, ctx, s.base)
	}
	// Observe the prior durable state at arrival of the first local model POST,
	// before returning any headers or usage for the fallback. This connection
	// belongs only to that observation and is independent of the control service.
	probe, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal("fallback observer database connection unavailable")
	}
	observation := make(chan bool, 1)
	g.mu.Lock()
	g.lanes[base+2].atAdmission = func() {
		defer probe.Close(context.Background())
		var count int
		err := probe.QueryRow(ctx, `SELECT count(*) FROM studio_runs r JOIN studio_run_streams h ON r.id=h.run_id
 JOIN studio_ledger_reservations l ON l.resource_id=r.id
 WHERE r.id=ANY($1::text[]) AND r.document->>'status'='INTERRUPTED' AND h.closed AND h.lease_owner=''
 AND l.state='REVIEW' AND l.started AND l.charged_micros=0 AND l.released_micros=0`, []string{running[0].run.ID, running[1].run.ID}).Scan(&count)
		observation <- err == nil && count == 2
	}
	g.mu.Unlock()
	// If a preceding check fails before first dispatch, close the unborrowed probe.
	// The mutex only guards ownership of the callback, never a database call.
	t.Cleanup(func() {
		g.mu.Lock()
		dispatched := g.lanes[base+2].posts > 0
		g.mu.Unlock()
		if !dispatched {
			_ = probe.Close(context.Background())
		}
	})
	for i, c := range running {
		killObservedChild(t, ctx, s, port, peers[i], processes[peers[i]])
		childGate(t, ctx, g.lanes[base+i].finished)
		terminal := c.wait(t, ctx, s.base, "INTERRUPTED", 1)
		if terminal.Output != activeDelta(base+i, 0) || terminal.Error == nil || terminal.Error.Code != "CHAT_CHILD_INTERRUPTED" {
			t.Fatal("all-child loss modified previously accepted run")
		}
		c.replay(t, ctx, s.base)
		verifyStoppedChildReplay(t, ctx, s.base, c, base+i, "INTERRUPTED")
		childLedgerState(t, admin, c, "REVIEW", true, 0)
		if i == 0 {
			requireQueued(queued[0], base+2)
			requireQueued(queued[1], base+3)
		}
	}
	childGate(t, ctx, g.lanes[base+2].entered)
	if !<-observation {
		t.Fatal("local first POST preceded old children terminal persistence or frozen review")
	}
	requireQueued(queued[1], base+3)
	for i, c := range queued {
		lane := base + 2 + i
		childGate(t, ctx, g.lanes[lane].entered)
		c.wait(t, ctx, s.base, "RUNNING", 1)
		g.mu.Lock()
		peer := g.lanes[lane].peerPort
		g.mu.Unlock()
		if observedOwners(t, ctx, s, port, []int{peer}, true)[peer] != s.proc.Process.Pid {
			t.Fatal("fallback did not use original controller process")
		}
		_ = assertChildLease(t, ctx, admin, c, childUpstream(lane), generation)
		close(g.lanes[lane].release)
		r := c.wait(t, ctx, s.base, "COMPLETED", 2)
		if r.Output != activeDelta(lane, 0) {
			t.Fatal("fallback output mismatch")
		}
		childLedgerState(t, admin, c, "SETTLED", true, 5)
		c.replay(t, ctx, s.base)
	}
}

func TestPostgresChatChildAcceptance(t *testing.T) {
	mode := os.Getenv("STUDIO_CHAT_CHILD_ACCEPTANCE")
	if mode == "" {
		t.Skip("opt-in genuine child-process acceptance")
	}
	n, readersPerRun, integrated := childAcceptanceProfile(mode)
	if n == 0 {
		t.Fatal("choose pilot, full, integrated-pilot or integrated")
	}
	if runtime.GOOS != "windows" || !mixedLoopbackDatabase(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("requires the Windows kernel ownership probe and a dedicated loopback PostgreSQL")
	}
	binary, reportPath := os.Getenv("STUDIO_CHAT_CHILD_SERVER"), os.Getenv("STUDIO_CHAT_CHILD_REPORT")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(reportPath) {
		t.Fatal("supply absolute owned executable and new report paths")
	}
	if stat, err := os.Stat(binary); err != nil || !stat.Mode().IsRegular() {
		t.Fatal("standard executable missing")
	}
	if _, err := os.Lstat(reportPath); !os.IsNotExist(err) {
		t.Fatal("report path must be new")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	f := prepareChildFixture(t, ctx)
	g := newChildAcceptanceGateway(ctx, n)
	gateway := httptest.NewServer(g)
	t.Cleanup(gateway.Close)
	gatewayPort, err := childFixturePort(gateway.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	settings := f.settings(g, gateway.URL)
	if integrated {
		settings["RUN_DELTA_BATCHING_ENABLED"] = "true"
		settings["ADMISSION_GLOBAL_STREAMS"] = "256"
		settings["ADMISSION_WORKSPACE_STREAMS"] = "64"
		settings["METRICS_TOKEN"] = newID("metrics")
	}
	start := func() *childAcceptanceServer {
		service := startChildAcceptanceServer(t, ctx, binary, settings)
		t.Cleanup(func() { service.crash(t); waitChildExit(t, context.Background(), service) })
		return service
	}
	s := start()
	admin, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal("independent measurement connection unavailable")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var generation string
	if err := admin.QueryRow(ctx, "SELECT generation FROM studio_run_executor WHERE singleton").Scan(&generation); err != nil {
		capacityDBFailure(t, "controller_generation", err)
	}
	turns := make([]childConversation, n)
	ordinary := n * readersPerRun
	events := make(chan childReaderEvent, ordinary*(activeStreamSteps+1))
	done := make(chan error, ordinary)
	for i := range n {
		turns[i] = childAcceptTurn(t, ctx, s.base, f.scopes[i%5], i)
		childGate(t, ctx, g.lanes[i].entered)
		for copyIndex := range readersPerRun {
			res := capacityOpen(t, ctx, turns[i].scope, s.base, turns[i].run.ID)
			go func(reader, lane int, run string, body io.ReadCloser) {
				defer body.Close()
				done <- readCapacityStream(body, run, lane, func(step int, at time.Time) error {
					events <- childReaderEvent{reader: reader, lane: lane, step: step, at: at}
					return nil
				})
			}(i*readersPerRun+copyIndex, i, turns[i].run.ID, res.Body)
		}
	}
	capacityRequest(t, ctx, f.scopes[1], "GET", s.base+"/api/v1/runs/"+turns[0].run.ID+"/events?after=0", "", nil, nil, 404)
	delayed := capacityOpen(t, ctx, turns[0].scope, s.base, turns[0].run.ID)
	paused, resume := make(chan struct{}), make(chan struct{})
	var pausedAt time.Time
	var lateSamples []time.Duration
	var lateTerminals int
	lateDone := make(chan error, 1)
	go func() {
		defer delayed.Body.Close()
		lateDone <- readCapacityStream(delayed.Body, turns[0].run.ID, 0, func(step int, at time.Time) error {
			if integrated {
				if step == activeStreamSteps {
					lateTerminals++
				} else {
					g.mu.Lock()
					sent := g.lanes[0].sent[step]
					g.mu.Unlock()
					if sent.IsZero() || at.Before(sent) {
						return fmt.Errorf("invalid delayed-reader sample clock")
					}
					lateSamples = append(lateSamples, at.Sub(sent))
				}
			}
			if step == 0 {
				pausedAt = at
				close(paused)
				select {
				case <-resume:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	peers := make([]int, n)
	g.mu.Lock()
	for i := range n {
		peers[i] = g.lanes[i].peerPort
	}
	g.mu.Unlock()
	owners := childOwners(t, ctx, s, gatewayPort, peers)
	counts := map[int]int{}
	leases := map[int]string{}
	workspaceCounts := make([]int, 5)
	modelCounts := make([]int, 4)
	for i, turn := range turns {
		pid := owners[peers[i]]
		owner := assertChildLease(t, ctx, admin, turn, childUpstream(i), generation)
		if leases[pid] != "" && leases[pid] != owner {
			t.Fatal("OS owner disagrees with PG lease owner")
		}
		leases[pid] = owner
		counts[pid]++
		workspaceCounts[i%5]++
		modelCounts[i%4]++
	}
	dist := make([]int, 0, 2)
	identities := map[string]bool{}
	for pid, count := range counts {
		dist = append(dist, count)
		identities[leases[pid]] = true
	}
	slices.Sort(dist)
	if len(dist) != 2 || dist[0] != n/2 || dist[1] != n/2 || len(identities) != 2 {
		t.Fatal("expected two distinct children with equal simultaneous physical execution")
	}
	queued := childAcceptTurn(t, ctx, s.base, f.scopes[0], n)
	queued.wait(t, ctx, s.base, "QUEUED", 0)
	queued.replay(t, ctx, s.base)
	cancelTurn := func(c childConversation) {
		capacityRequest(t, ctx, c.scope, "POST", s.base+"/api/v1/runs/"+c.run.ID+"/cancel", "", nil, nil, 200)
	}
	cancelTurn(queued)
	queued.wait(t, ctx, s.base, "CANCELLED", 0)
	childLedgerState(t, admin, queued, "RELEASED", false, 0)
	turns[0].replay(t, ctx, s.base)
	var joint childJointEvidence
	if integrated {
		joint = observeChildJointOccupancy(t, ctx, s, f, g, settings["METRICS_TOKEN"], workspaceCounts, readersPerRun)
	}
	samples := make([]time.Duration, 0, ordinary*activeStreamSteps)
	waveStarted := time.Now()
	for step := range activeStreamSteps {
		close(g.ticks[step])
		seen := make([]bool, ordinary)
		for range ordinary {
			select {
			case ev := <-events:
				if err := ev.mark(seen, step, readersPerRun); err != nil {
					t.Fatal(err)
				}
				g.mu.Lock()
				sent := g.lanes[ev.lane].sent[step]
				g.mu.Unlock()
				if sent.IsZero() || ev.at.Before(sent) {
					t.Fatal("invalid clock ordering")
				}
				samples = append(samples, ev.at.Sub(sent))
			case err := <-done:
				t.Fatalf("stream ended before barrier: %v", err)
			case <-time.After(20 * time.Second):
				t.Fatal("live stream barrier timed out")
			case <-ctx.Done():
				t.Fatal("fixture deadline")
			}
		}
		if step == 0 {
			childGate(t, ctx, paused)
		}
	}
	waveDuration := time.Since(waveStarted)
	for i := range n {
		close(g.lanes[i].release)
	}
	for range ordinary {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("final SSE deadline")
		}
	}
	pauseTime := time.Since(pausedAt)
	close(resume)
	if err := <-lateDone; err != nil {
		t.Fatal(err)
	}
	if integrated {
		seen := make([]bool, ordinary)
		for range ordinary {
			select {
			case terminal := <-events:
				if err := terminal.mark(seen, activeStreamSteps, readersPerRun); err != nil {
					t.Fatal(err)
				}
				joint.NormalTerminals++
			default:
				t.Fatal("reader exited without a matching terminal")
			}
		}
		if len(lateSamples) != activeStreamSteps || lateTerminals != 1 {
			t.Fatal("delayed reader did not observe every increment and terminal")
		}
		joint.DelayedTerminals = lateTerminals
		joint.NormalDeltas = len(samples)
		joint.DelayedDeltas = len(lateSamples)
		joint.DelayedLatency = capacityDistributionOf(lateSamples)
		joint.AllReaderLatency = capacityDistributionOf(append(slices.Clone(samples), lateSamples...))
		joint.NormalLatency = capacityDistributionOf(samples)
		observeChildJointCommits(t, ctx, s, f.scopes[0].client, settings["METRICS_TOKEN"], admin, turns, waveDuration, &joint)
	}
	for i, turn := range turns {
		verifyChildJournal(t, ctx, admin, turn, i, s.base)
	}
	faults := func(index int) childConversation {
		c := childAcceptTurn(t, ctx, s.base, f.scopes[0], index)
		childGate(t, ctx, g.lanes[index].entered)
		c.wait(t, ctx, s.base, "RUNNING", 1)
		return c
	}
	startedCancel := faults(n + 1)
	cancelTurn(startedCancel)
	childGate(t, ctx, g.lanes[n+1].finished)
	startedCancel.wait(t, ctx, s.base, "CANCELLED", 1)
	startedCancel.replay(t, ctx, s.base)
	verifyStoppedChildReplay(t, ctx, s.base, startedCancel, n+1, "CANCELLED")
	childLedgerState(t, admin, startedCancel, "REVIEW", true, 0)
	killed := faults(n + 2)
	g.mu.Lock()
	peer := g.lanes[n+2].peerPort
	g.mu.Unlock()
	pid := childOwners(t, ctx, s, gatewayPort, []int{peer})[peer]
	if leases[pid] == "" || assertChildLease(t, ctx, admin, killed, childUpstream(n+2), generation) != leases[pid] {
		t.Fatal("kill target is not original child owner")
	}
	killObservedChild(t, ctx, s, gatewayPort, peer, pid)
	childGate(t, ctx, g.lanes[n+2].finished)
	r := killed.wait(t, ctx, s.base, "INTERRUPTED", 1)
	if r.Output != activeDelta(n+2, 0) || r.Error == nil || r.Error.Code != "CHAT_CHILD_INTERRUPTED" {
		t.Fatal("child loss did not retain the committed original prefix")
	}
	killed.replay(t, ctx, s.base)
	verifyStoppedChildReplay(t, ctx, s.base, killed, n+2, "INTERRUPTED")
	childLedgerState(t, admin, killed, "REVIEW", true, 0)
	orphan := faults(n + 3)
	g.mu.Lock()
	peer = g.lanes[n+3].peerPort
	g.mu.Unlock()
	survivor := childOwners(t, ctx, s, gatewayPort, []int{peer})[peer]
	if survivor == pid || leases[survivor] == "" || assertChildLease(t, ctx, admin, orphan, childUpstream(n+3), generation) != leases[survivor] {
		t.Fatal("remaining child not serving original generation")
	}
	s.crash(t)
	childGate(t, ctx, g.lanes[n+3].finished)
	waitChildExit(t, ctx, s)
	// The recovery/failure phase needs two independent one-slot children so
	// their final loss and the original single local slot can be checked directly.
	settings["CHAT_CHILD_SLOTS"] = "1"
	s = start()
	restored := orphan.wait(t, ctx, s.base, "INTERRUPTED", 1)
	if restored.Output != activeDelta(n+3, 0) {
		t.Fatal("recovery lost committed prefix")
	}
	orphan.replay(t, ctx, s.base)
	killed.replay(t, ctx, s.base)
	verifyStoppedChildReplay(t, ctx, s.base, orphan, n+3, "INTERRUPTED")
	childLedgerState(t, admin, orphan, "REVIEW", true, 0)
	if outboxCount(t, admin, "SELECT count(*) FROM studio_run_executor WHERE singleton AND generation<>$1", generation) != 1 {
		t.Fatal("restart failed to fence old generation")
	}
	observeChildFallback(t, ctx, f, g, gatewayPort, s, admin)
	s.crash(t)
	waitChildExit(t, ctx, s)
	observeChildModeration(t, ctx, f, g, gateway.URL, gatewayPort, admin)
	g.mu.Lock()
	posts := 0
	for i := range g.lanes {
		want := 1
		if i == n {
			want = 0
		}
		if g.lanes[i].posts != want {
			g.mu.Unlock()
			t.Fatal("accepted work was unexpectedly dispatched or repeated")
		}
		posts += g.lanes[i].posts
	}
	peak, active, bad := g.peak, g.active, g.invalid
	g.mu.Unlock()
	if peak != n || active != 0 || bad != 0 {
		t.Fatal("invalid final upstream occupancy")
	}
	report := map[string]any{
		"profile": mode, "measurement": "standard_controller_children",
		"kernel_probe":                     "windows_filtered_tcp_owner_and_process_parent",
		"simultaneous_child_model_sockets": dist, "peak_model_requests": peak,
		"active_per_workspace": workspaceCounts, "active_per_frozen_model": modelCounts,
		"authorized_normal_readers": ordinary, "additional_paused_reader": 1, "reader_pause_ms": float64(pauseTime) / float64(time.Millisecond),
		"verified_deltas": len(samples), "live_delta_latency": capacityDistributionOf(samples),
		"standard_executable_model_posts": posts - 1, "all_model_posts_including_moderation": posts, "unexpected_or_repeated_posts": bad,
		"queued_cancel_without_post": 1, "started_cancel_with_review": 1,
		"killed_child_with_review": 1, "controller_crash_with_review": 1,
		"verified_settled_runs": n, "verified_child_owners": len(identities),
		"matching_active_generation": true, "new_generation_after_restart": true,
	}
	if integrated {
		// The legacy field was unambiguous with a single normal reader per Run.
		// With fan-out the number of delivered copies differs from persisted rows.
		delete(report, "verified_deltas")
		report["measurement"] = "standard_controller_children_batched_deltas_authorized_sse"
		report["joint_wave"] = joint
		report["delta_latency_window"] = "original gateway frame write to receipt of the complete authorized SSE frame; terminal, replay and fault-phase reads excluded"
	}
	report["moderation_companion"] = map[string]any{"controller": "same_package_test_host", "child": "actual_production_worker_entrypoint", "model_posts": 1, "input_checks": 1, "output_checks": 1, "decision": "BLOCKED", "candidate_journal_deltas": 0, "credit_state": "REVIEW"}
	report["all_children_lost"] = map[string]any{"child_slots": []int{1, 1}, "interrupted_with_original_prefix": 2, "queued_first_dispatch_local": 2, "local_slots_observed": 1, "controller_remained_alive": true, "old_terminals_and_review_before_first_local_post": true}
	raw, _ := json.MarshalIndent(report, "", "  ")
	file, err := os.OpenFile(reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("could not create new aggregate report")
	}
	_, err = file.Write(append(raw, '\n'))
	ce := file.Close()
	if err != nil || ce != nil {
		t.Fatal("aggregate report write failed")
	}
}
