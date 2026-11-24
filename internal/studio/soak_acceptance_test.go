package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type soakPair256 struct {
	index, owner                   int
	runID, taskID, runKey, taskKey string
	run                            RunInput
	task                           TaskInput
	admittedTask                   time.Time
}

type soakTrial256 struct {
	t                *testing.T
	ctx              context.Context
	profile          soakProfile256
	start            time.Time
	windows          []soakWindow256 // last bucket is drain, beyond the fixed schedule
	server           *childAcceptanceServer
	binary, dsn, tag string
	stopAt, readyAt  time.Time
	settings         map[string]string
	gateway          *soakGateway256
	users            []admissionCapacityAccount
	observer         *pgx.Conn
	diagnostics      *soakGC256
	epoch            int
	pending          map[string]*soakPair256
	byOwner          [2]*soakPair256
	issued           []bool
	crashRecovered   bool
	sink             *soakSink256
}

func (r *soakTrial256) windowAt(at time.Time) *soakWindow256 {
	i := min(int(at.Sub(r.start)/r.profile.Width), r.profile.Windows)
	return &r.windows[i]
}
func (r *soakTrial256) launch() {
	r.epoch++
	r.tag = newID(fmt.Sprintf("soak%d", r.epoch))
	r.settings["DATABASE_URL"] = mixedTaggedDSN(r.t, r.dsn, r.tag)
	r.diagnostics = &soakGC256{}
	r.server = startObservedChildAcceptanceServer(r.t, r.ctx, r.binary, r.settings, r.diagnostics)
}
func (r *soakTrial256) readStream(user admissionCapacityAccount, id string, after uint64, visit func(capacityEvent) error) error {
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/api/v1/runs/%s/events?after=%d", r.server.base, id, after)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.New("SSE request construction failed")
	}
	req.Header.Set("X-Project-ID", user.identity.Project.ID)
	req.Header.Set("Accept", "text/event-stream")
	res, err := user.client.Do(req)
	if err != nil {
		return errors.New("SSE connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("SSE status %d", res.StatusCode)
	}
	return visitCapacitySSE(res.Body, visit)
}
func (r *soakTrial256) acceptRun(index int, owner int, pair *soakPair256) Run {
	u := r.users[owner]
	pair.run = RunInput{ProjectID: u.identity.Project.ID, ModelID: "studio-chat", Input: fmt.Sprintf("soak256-%d", index)}
	pair.runKey = newID("run")
	var run Run
	h := u.request(r.t, r.ctx, r.server.base, "POST", "/api/v1/runs", pair.runKey, pair.run, &run, 201)
	if run.ID == "" || run.WorkspaceID != u.identity.Workspace.ID || run.ProjectID != u.identity.Project.ID || run.ProviderMode != "live" || h.Get("Idempotency-Replayed") != "" {
		r.t.Fatal("chat admission returned inconsistent scope or replay")
	}
	pair.runID = run.ID
	return run
}
func (r *soakTrial256) replayRun(pair *soakPair256, status string) {
	var run Run
	h := r.users[pair.owner].request(r.t, r.ctx, r.server.base, "POST", "/api/v1/runs", pair.runKey, pair.run, &run, 201)
	if h.Get("Idempotency-Replayed") != "true" || run.ID != pair.runID || run.Status != status {
		r.t.Fatal("original run receipt or state changed")
	}
	r.windowAt(time.Now()).ChatReplays++
}
func (r *soakTrial256) replayTask(pair *soakPair256) {
	var task Task
	h := r.users[pair.owner].request(r.t, r.ctx, r.server.base, "POST", "/api/v1/tasks", pair.taskKey, pair.task, &task, 202)
	if h.Get("Idempotency-Replayed") != "true" || task.ID != pair.taskID {
		r.t.Fatal("original task receipt changed")
	}
	r.windowAt(time.Now()).MediaReplays++
}
func (r *soakTrial256) offer(index int, due, sent time.Time) {
	t := r.t
	owner := index % len(r.users)
	u := r.users[owner]
	r.record(soakSent256, due, sent)
	pair := &soakPair256{index: index, owner: owner}
	r.acceptRun(index, owner, pair)
	r.record(soakChatAccepted256, sent, time.Now())
	pair.task = taskInput("image")
	pair.task.ProjectID = u.identity.Project.ID
	pair.task.Inputs.Prompt = pair.run.Input
	pair.taskKey = newID("task")
	taskSent := time.Now()
	var task Task
	h := u.request(t, r.ctx, r.server.base, "POST", "/api/v1/tasks", pair.taskKey, pair.task, &task, 202)
	pair.admittedTask = time.Now()
	if task.ID == "" || task.ProjectID != u.identity.Project.ID || task.WorkspaceID != u.identity.Workspace.ID || task.ProviderMode != "live" || h.Get("Idempotency-Replayed") != "" {
		t.Fatal("media admission returned inconsistent scope or replay")
	}
	r.record(soakMediaAccepted256, taskSent, pair.admittedTask)
	pair.taskID = task.ID
	r.pending[task.ID] = pair
	r.byOwner[owner] = pair
	r.issued[index] = true
	admissionWindow := r.windowAt(pair.admittedTask)
	admissionWindow.UnobservedMediaPeak = max(admissionWindow.UnobservedMediaPeak, len(r.pending))
	seq := uint64(0)
	err := r.readStream(u, pair.runID, 0, func(ev capacityEvent) error {
		seq++
		if ev.Data.RunID != pair.runID || ev.ID != seq {
			return errors.New("stream identity or sequence changed")
		}
		switch seq {
		case 1:
			if ev.Name != "message.delta" || ev.Data.Delta != "a" {
				return errors.New("first content differs")
			}
			r.record(soakChatFirst256, sent, ev.At)
		case 2:
			if ev.Name != "message.delta" || ev.Data.Delta != "b" {
				return errors.New("second content differs")
			}
		case 3:
			if ev.Name != "run.completed" || ev.Data.Status != "COMPLETED" {
				return errors.New("chat terminal differs")
			}
			r.record(soakChatComplete256, sent, ev.At)
		default:
			return errors.New("extra stream event")
		}
		return nil
	})
	if err != nil || seq != 3 {
		t.Fatal("ordinary chat was not fully observed", err)
	}
	r.replayRun(pair, "COMPLETED")
}
func (r *soakTrial256) pollMedia() {
	for id, p := range r.pending {
		var task Task
		r.users[p.owner].request(r.t, r.ctx, r.server.base, "GET", "/api/v1/tasks/"+id, "", nil, &task, 200)
		if task.ID != id || task.ProjectID != p.task.ProjectID {
			r.t.Fatal("media poll returned a different resource")
		}
		observed := time.Now()
		done, err := soakMediaObservation256(p.admittedTask, observed, task)
		if err != nil {
			r.t.Fatal(err)
		}
		if done {
			r.record(soakMediaFailed256, p.admittedTask, observed)
			r.replayTask(p)
			delete(r.pending, id)
		}
	}
}
func (r *soakTrial256) waitUntil(due time.Time) {
	for time.Now().Before(due) {
		r.pollMedia()
		delay := time.Until(due)
		if len(r.pending) != 0 {
			delay = min(delay, 400*time.Millisecond)
		}
		if delay <= 0 {
			continue
		}
		select {
		case <-time.After(delay):
		case <-r.ctx.Done():
			r.t.Fatal("test deadline while waiting for planned arrival")
		}
	}
}

// The fault targets an already-streaming chat. Wait for every admitted image
// to have its exact original handle on disk, so an ambiguous native submission
// is never turned into a test retry during recovery.
func (r *soakTrial256) waitForKnownMedia() {
	r.t.Helper()
	deadline, cancel := context.WithTimeout(r.ctx, 12*time.Second)
	defer cancel()
	for _, pair := range r.pending {
		want := fmt.Sprintf("%s%d", soakHandlePrefix256, pair.index)
		for {
			var handle string
			err := r.observer.QueryRow(deadline, "SELECT COALESCE(handle->>'ID','') FROM studio_tasks WHERE id=$1 AND project_id=$2", pair.taskID, pair.task.ProjectID).Scan(&handle)
			if err != nil || handle != "" && handle != want {
				r.t.Fatal("could not verify original media handle before the fault")
			}
			if handle == want {
				break
			}
			select {
			case <-deadline.Done():
				r.t.Fatal("original media submission has no verified handle at the fault boundary")
			case <-time.After(80 * time.Millisecond):
			}
		}
	}
}
func (r *soakTrial256) interrupt() {
	t := r.t
	special := &soakPair256{index: r.profile.total(), owner: 0}
	r.acceptRun(special.index, 0, special)
	stopped := errors.New("original cursor observed")
	var cursor uint64
	if err := r.readStream(r.users[0], special.runID, 0, func(ev capacityEvent) error {
		if ev.ID != 1 || ev.Name != "message.delta" || ev.Data.Delta != "a" || ev.Data.RunID != special.runID {
			return errors.New("original durable prefix mismatch")
		}
		cursor = ev.ID
		return stopped
	}); !errors.Is(err, stopped) {
		t.Fatal("fixture did not produce its first committed delta", err)
	}
	r.waitForKnownMedia()
	before := r.diagnostics.observe(t, r.ctx, r.start, r.epoch, r.server, r.observer, r.tag)
	if before.ActiveChatRuns != 1 {
		t.Fatal("fault boundary did not contain exactly one active chat")
	}
	r.stopAt = time.Now()
	r.server.crash(t) // Waits for this exact process to exit.
	select {
	case <-r.gateway.crashDone:
	case <-time.After(8 * time.Second):
		t.Fatal("owned upstream held request survived the old controller")
	}
	releaseCtx, cancel := context.WithTimeout(r.ctx, 8*time.Second)
	err := waitSoakPGRelease256(releaseCtx, 60*time.Millisecond, func(ctx context.Context) (int, error) {
		var connections int
		e := r.observer.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity a WHERE "+mixedOwnedSession, r.tag).Scan(&connections)
		return connections, e
	})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	r.launch()
	r.readyAt = time.Now()
	s := r.users[0]
	var recovered Run
	s.request(t, r.ctx, r.server.base, "GET", "/api/v1/runs/"+special.runID+"/snapshot", "", nil, &recovered, 200)
	if recovered.ID != special.runID || recovered.Output != "a" || recovered.Seq != cursor+1 || recovered.Status != "INTERRUPTED" || recovered.Error == nil || recovered.Error.Code != "PROCESS_RESTARTED" {
		t.Fatal("new controller did not retain the committed prefix")
	}
	events := 0
	err = r.readStream(s, special.runID, cursor, func(ev capacityEvent) error {
		events++
		if ev.ID != cursor+1 || ev.Data.RunID != special.runID || ev.Name != "run.interrupted" || ev.Data.Status != "INTERRUPTED" {
			return errors.New("resume did not continue from original cursor")
		}
		return nil
	})
	if err != nil || events != 1 {
		t.Fatal("recovery cursor failure", err)
	}
	r.windowAt(time.Now()).CursorReplays++
	r.replayRun(special, "INTERRUPTED")
	for _, p := range r.byOwner {
		if p == nil {
			t.Fatal("recovery requires accepted work for each tenant")
		}
		r.replayRun(p, "COMPLETED")
		r.replayTask(p)
	}
	after := r.diagnostics.observe(t, r.ctx, r.start, r.epoch, r.server, r.observer, r.tag)
	if after.ActiveChatRuns != 0 {
		t.Fatal("recovered controller still has an active chat")
	}
	r.sink.write(t, map[string]any{"type": "restart", "before": before, "after": after, "stop_start_ns": r.stopAt.Sub(r.start).Nanoseconds(), "replacement_ready_ns": r.readyAt.Sub(r.start).Nanoseconds(), "retired_pg_connections": 0, "kill_to_verified_replay_ms": time.Since(r.stopAt).Milliseconds(), "replayed_tenants": len(r.byOwner), "retained_deltas": cursor, "recovered_terminal_events": events})
	r.crashRecovered = true
}
func (r *soakTrial256) windowRecord(i int, wallStart, wallEnd time.Time, obs soakObs256) {
	w := &r.windows[i]
	if !w.samplesMatch() || w.Offered+w.Missed != w.Planned {
		r.t.Errorf("window %d counts and samples did not reconcile", i)
	}
	r.sink.write(r.t, map[string]any{"type": "window", "index": i, "drain": i == r.profile.Windows, "planned_start_ms": wallStart.Sub(r.start).Milliseconds(), "planned_end_ms": wallEnd.Sub(r.start).Milliseconds(), "counts": w.soakCounts256, "missed_slots": w.misses, "latency": w.distribution(), "resources": obs, "upstream_cumulative": r.gateway.snapshot(), "observed_unfinished_media": len(r.pending)})
}
func (r *soakTrial256) run() {
	t := r.t
	index := 0
	r.start = time.Now()
	baseline := r.diagnostics.observe(t, r.ctx, r.start, r.epoch, r.server, r.observer, r.tag)
	r.sink.write(t, map[string]any{"type": "baseline", "resources": baseline})
	for i := range r.profile.Windows {
		slots := r.profile.slots(i)
		period := r.profile.Width / time.Duration(slots)
		start := r.start.Add(time.Duration(i) * r.profile.Width)
		end := start.Add(r.profile.Width)
		r.windows[i].Planned = slots
		r.windows[i].UnobservedMediaPeak = max(r.windows[i].UnobservedMediaPeak, len(r.pending))
		for j := range slots {
			due := start.Add(time.Duration(j) * period)
			r.waitUntil(due)
			dispatch := time.Now()
			deadline := due.Add(period)
			switch {
			case !dispatch.Before(deadline):
				r.noteMiss(index, due, deadline, dispatch, "expired")
			case len(r.pending) >= 16:
				r.noteMiss(index, due, deadline, dispatch, "outstanding_media_limit")
			default:
				r.offer(index, due, dispatch)
			}
			index++
			if i == r.profile.Windows/2 && j == 0 {
				r.interrupt()
			}
		}
		r.waitUntil(end)
		r.windowRecord(i, start, end, r.diagnostics.observe(t, r.ctx, r.start, r.epoch, r.server, r.observer, r.tag))
	}
	drainStart := r.start.Add(r.profile.Width * time.Duration(r.profile.Windows))
	for len(r.pending) > 0 {
		r.pollMedia()
		if len(r.pending) > 0 {
			r.waitUntil(time.Now().Add(250 * time.Millisecond))
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	final := r.diagnostics.observe(t, r.ctx, r.start, r.epoch, r.server, r.observer, r.tag)
	for final.OutboxPending != 0 && time.Now().Before(deadline) {
		r.waitUntil(time.Now().Add(250 * time.Millisecond))
		final = r.diagnostics.observe(t, r.ctx, r.start, r.epoch, r.server, r.observer, r.tag)
	}
	if final.OutboxPending != 0 || final.OutboxRetained < 1 || final.ActiveChatRuns != 0 || final.ActiveMediaTasks != 0 || final.UncertainMediaTasks != 0 {
		t.Fatal("unfinished notification or workload after bounded drain")
	}
	r.windowRecord(r.profile.Windows, drainStart, time.Now(), final)
	// Independent observation from the database and gateway, checked after all
	// originally offered work and before deleting the disposable schema.
	total := 0
	for i, issued := range r.issued {
		chat, media, queries := r.gateway.countsAt(i)
		if issued {
			total++
			if chat != 1 || media != 1 || queries < 1 {
				t.Fatal("accepted operation did not have precisely one upstream submission")
			}
		} else if chat != 0 || media != 0 || queries != 0 {
			t.Fatal("unoffered slot generated upstream activity")
		}
	}
	snapshot := r.gateway.snapshot()
	if !r.crashRecovered || snapshot.Invalid != 0 || snapshot.ChatPosts != total+1 || snapshot.MediaPosts != total || snapshot.RateLimits != 1 || r.gateway.retryDelay() < time.Second {
		t.Fatal("wire and recovery totals differ from the planned workload")
	}
	var runs, completed, interrupted, tasks, failed int
	err := r.observer.QueryRow(r.ctx, `SELECT count(*),count(*) FILTER (WHERE document->>'status'='COMPLETED' AND document->>'output'='ab'),count(*) FILTER (WHERE document->>'status'='INTERRUPTED' AND document->>'output'='a') FROM studio_runs`).Scan(&runs, &completed, &interrupted)
	if err != nil {
		t.Fatal("database run observation unavailable")
	}
	err = r.observer.QueryRow(r.ctx, `SELECT count(*),count(*) FILTER(WHERE document->>'status'='FAILED' AND document->'error'->>'code'='UPSTREAM_GENERATION_FAILED') FROM studio_tasks`).Scan(&tasks, &failed)
	if err != nil || runs != total+1 || completed != total || interrupted != 1 || tasks != total || failed != total {
		t.Fatal("durable scope totals disagree with submitted and observed results")
	}
	var planned, offered, missed, runAccept, mediaAccept, firstDelta, runDone, mediaDone, runReplay, mediaReplay, cursors int
	for i, w := range r.windows {
		planned += w.Planned
		offered += w.Offered
		missed += w.Missed
		runAccept += w.ChatAccepted
		mediaAccept += w.MediaAccepted
		firstDelta += w.ChatFirstDelta
		runDone += w.ChatComplete
		mediaDone += w.MediaFailed
		runReplay += w.ChatReplays
		mediaReplay += w.MediaReplays
		cursors += w.CursorReplays
		if w.Missed != w.MissedDuringStop {
			t.Errorf("window %d missed %d pairs outside controller unavailability", i, w.Missed-w.MissedDuringStop)
		}
	}
	if planned != r.profile.total() || total == 0 || offered != total || offered+missed != planned || runAccept != total || mediaAccept != total || firstDelta != total || runDone != total || mediaDone != total || runReplay != total+1+len(r.byOwner) || mediaReplay != total+len(r.byOwner) || cursors != 1 {
		t.Fatal("application window totals did not reconcile")
	}
	r.sink.write(t, map[string]any{"type": "summary", "complete": !t.Failed(), "scheduled_seconds": int64(r.profile.Width/time.Second) * int64(r.profile.Windows), "observed_seconds": time.Since(r.start).Seconds(), "planned_pairs": planned, "offered_pairs": offered, "missed_pairs": missed, "completed_chats": completed, "interrupted_chats": interrupted, "expected_media_failure_terminals": failed, "original_chat_receipts": runReplay, "original_media_receipts": mediaReplay, "cursor_recoveries": cursors, "gateway": snapshot, "original_retry_after_ms": r.gateway.retryDelay().Milliseconds()})
}

func TestPostgresMixedServiceSoak256(t *testing.T) {
	mode := os.Getenv("STUDIO_MIXED_SOAK")
	if mode == "" {
		t.Skip("explicit sustained mixed workload")
	}
	prof, ok := soakProfileFor256(mode)
	if !ok || runtime.GOOS != "windows" || !admissionCapacityLocalDSN(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("select pilot or 24h and a dedicated Windows loopback test database")
	}
	binary := os.Getenv("STUDIO_MIXED_SOAK_SERVER")
	if !filepath.IsAbs(binary) {
		t.Fatal("supply the absolute locally built controller path")
	}
	hash := soakFileDigest256(t, binary)
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(prof.Windows)*prof.Width+5*time.Minute)
	defer cancel()
	dsn := testDatabase(t)
	r := &soakTrial256{t: t, ctx: ctx, profile: prof, windows: make([]soakWindow256, prof.Windows+1), binary: binary, dsn: dsn, gateway: newSoakGateway256(prof.total()), issued: make([]bool, prof.total()), pending: make(map[string]*soakPair256), sink: openSoakSink256(t, os.Getenv("STUDIO_MIXED_SOAK_REPORT"))}
	r.observer = deliveryAdmin(t, dsn)
	upstream := httptest.NewServer(r.gateway)
	t.Cleanup(upstream.Close)
	r.settings = map[string]string{"AUTH_MODE": "required", "ASSET_DIR": t.TempDir(), "REFERENCE_DIR": t.TempDir(), "PROVIDER_MODE": "live", "NEWAPI_BASE_URL": upstream.URL + "/v1", "NEWAPI_API_KEY": fixtureMediaKey, "NEWAPI_CHAT_MODEL": "soak-chat", "ASYNC_IMAGE_BASE_URL": upstream.URL, "ASYNC_IMAGE_API_KEY": fixtureMediaKey, "ASYNC_IMAGE_MODEL": "soak-image", "ASYNC_IMAGE_CREDENTIAL_ID": "soak256", "MEDIA_DELIVERY_KEY": coreDeliveryKey(), "GODEBUG": "gctrace=1", "RUN_REPLAY_READ_CONNECTIONS": "1"}
	r.sink.write(t, map[string]any{"type": "configuration", "profile": mode, "service_sha256": hash, "scheduled_windows": prof.Windows, "seconds_per_window": prof.Width.Seconds(), "ordinary_pairs_per_window": prof.Pairs, "burst_window": 1, "fault_window": prof.Windows / 2, "step": "one normal chat and one normal image per pair", "controlled_media_outcome": "upstream_failure", "host": "single Windows host; driver includes loopback upstream", "platform": runtime.GOOS + "/" + runtime.GOARCH, "go": runtime.Version()})
	r.launch()
	r.users = admissionCapacityRegister(t, ctx, r.server.base, 2)
	for i := range r.users {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		original := r.users[i].identity
		// Start with an empty jar, then exercise the regular email/password login.
		r.users[i] = admissionCapacityAccount{client: authClient(t)}
		r.users[i].client.Transport = tr
		r.users[i].client.Timeout = 15 * time.Second
		t.Cleanup(tr.CloseIdleConnections)
		r.users[i].request(t, ctx, r.server.base, "POST", "/api/v1/auth/login", "", map[string]string{"email": original.User.Email, "password": "test-only-capacity-246"}, &r.users[i].identity, 200)
		current := r.users[i].identity
		if current.User.ID != original.User.ID || current.Workspace.ID != original.Workspace.ID || current.Project.ID != original.Project.ID {
			t.Fatal("password login did not restore the registered scope")
		}
	}
	if r.users[0].identity.Workspace.ID == r.users[1].identity.Workspace.ID {
		t.Fatal("distinct workspace registration is required")
	}
	r.run()
}
