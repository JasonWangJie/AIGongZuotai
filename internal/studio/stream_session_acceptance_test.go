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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Each socket consumes real frames until the server closes it. Revocation
// checks inspect this EOF before closing the client side.
type sessionSSEReader struct {
	body     io.ReadCloser
	done     chan struct{}
	err      error // read only after done
	run      bool
	progress atomic.Uint64 // Run event sequence or observed draft revision
}

func sessionFixtureText(account, seq int) string {
	return fmt.Sprintf("scope-%d-segment-%d", account, seq)
}

func (r *sessionSSEReader) readProject(body io.Reader, identity testIdentity, storage string) error {
	expected := durableCursor{Version: 1, UserID: identity.User.ID, WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, StorageID: storage}
	scan := bufio.NewScanner(body)
	scan.Buffer(make([]byte, 1024), 8192)
	var name, id, data string
	var previous uint64
	var initial bool
	for scan.Scan() {
		line := scan.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "id: ") && id == "":
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: ") && name == "":
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && data == "":
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && id == "" && name == "" && data == "":
			continue
		case line == "":
			cursor, err := parseDurableCursor(id, expected)
			if err != nil {
				return errors.New("project cursor changed scope")
			}
			if !initial {
				var reset struct{ Reason, Cursor string }
				if name != "stream.reset" || json.Unmarshal([]byte(data), &reset) != nil || reset.Reason != "initial" || reset.Cursor != id {
					return errors.New("initial project reset missing")
				}
				initial = true
			} else {
				var event DurableEvent
				if name != "resource.changed" || json.Unmarshal([]byte(data), &event) != nil || cursor.Seq != previous+1 || event.Seq != cursor.Seq {
					return errors.New("project event sequence changed")
				}
				if event.Action == "draft.saved" {
					if event.ResourceType != "draft" || event.ResourceID != identity.Project.ID || event.ResourceVersion != r.progress.Load()+1 {
						return errors.New("incorrect project draft evidence")
					}
					r.progress.Store(event.ResourceVersion)
				}
			}
			previous = cursor.Seq
			name, id, data = "", "", ""
		default:
			return errors.New("unexpected project SSE field")
		}
	}
	if scan.Err() != nil || !initial || name != "" || id != "" || data != "" {
		return errors.New("project stream did not end between complete frames")
	}
	return nil
}

func watchSessionSSE(body io.ReadCloser, scope capacityScope, storage, runID string, account int, after uint64) *sessionSSEReader {
	r := &sessionSSEReader{body: body, done: make(chan struct{}), run: runID != ""}
	r.progress.Store(after)
	go func() {
		defer close(r.done)
		if !r.run {
			r.err = r.readProject(body, scope.identity, storage)
			return
		}
		r.err = visitCapacitySSE(body, func(e capacityEvent) error {
			if e.Data.RunID != runID || e.ID != r.progress.Load()+1 {
				return errors.New("Run replay crossed identity or cursor")
			}
			switch e.ID {
			case 1, 2:
				if e.Name != "message.delta" || e.Data.Delta != sessionFixtureText(account, int(e.ID)) {
					return errors.New("missing scoped model bytes")
				}
			case 3:
				if e.Name != "run.completed" || e.Data.Status != "COMPLETED" {
					return errors.New("missing committed terminal event")
				}
			default:
				return errors.New("unexpected extra event")
			}
			r.progress.Store(e.ID)
			return nil
		})
	}()
	return r
}

func waitSessionGroup(t *testing.T, readers []*sessionSSEReader, progress uint64, closed bool) {
	t.Helper()
	end := time.Now().Add(12 * time.Second)
	for {
		matched := 0
		for _, r := range readers {
			done := false
			select {
			case <-r.done:
				done = true
				if r.err != nil {
					t.Fatal("reader failed a frame or EOF check", r.err)
				}
				if !closed {
					t.Fatal("active session closed early")
				}
			default:
			}
			if r.progress.Load() > progress {
				t.Fatal("reader exceeded authorized boundary")
			}
			if r.progress.Load() == progress && done == closed {
				matched++
			}
		}
		if matched == len(readers) {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("observed readers=%d/%d boundary=%d closed=%t", matched, len(readers), progress, closed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sessionRounds(t *testing.T, a *App, unique uint64) (uint64, uint64) {
	t.Helper()
	count := func() (uint64, uint64) {
		m := a.streamSessions
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.rounds.Load(), m.checked.Load()
	}
	start, _ := count()
	// Let any already sampled round finish before measuring two full rounds.
	sseSocketWait(t, func() bool { now, _ := count(); return now > start })
	start, base := count()
	sseSocketWait(t, func() bool { now, _ := count(); return now >= start+2 })
	now, checked := count()
	if checked-base != (now-start)*unique {
		t.Fatalf("authorization rounds=%d unique checks=%d expected per round=%d", now-start, checked-base, unique)
	}
	return now - start, checked - base
}

func sessionLogin(t *testing.T, ctx context.Context, s *httptest.Server, old capacityScope, n int) capacityScope {
	t.Helper()
	c := authClient(t)
	c.Transport = s.Client().Transport
	c.Timeout = 0
	scope := capacityScope{client: c}
	capacityRequest(t, ctx, scope, "POST", s.URL+"/api/v1/auth/login", "", map[string]string{"email": fmt.Sprintf("socket-%d@example.test", n), "password": "fixture-password-123"}, &scope.identity, 200)
	if scope.identity.User.ID != old.identity.User.ID || scope.identity.Project.ID != old.identity.Project.ID {
		t.Fatal("independent login resolved a different account")
	}
	return scope
}

func holdSessionReaderConn(t *testing.T, ctx context.Context, p *runReplayPool) (*pgx.Conn, func()) {
	t.Helper()
	var conn *pgx.Conn
	select {
	case conn = <-p.slots:
	case <-ctx.Done():
		t.Fatal("could not reserve verified read connection")
	}
	release := sync.OnceFunc(func() { p.slots <- conn })
	t.Cleanup(release)
	if conn == nil {
		t.Fatal("read pool lacked a physical connection")
	}
	if err := pgx.BeginTxFunc(ctx, conn, replayTxOptions(), func(tx pgx.Tx) error { return p.verify(ctx, tx) }); err != nil {
		t.Fatal("read connection identity differs from this fixture")
	}
	return conn, release
}

func TestPostgresStreamSessionShared192AndFailure(t *testing.T) {
	db := sseSocketDatabase(t)
	stages := [3]chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	release := [3]func(){}
	for i := range stages {
		release[i] = sync.OnceFunc(func() { close(stages[i]) })
	}
	var posts [2]atomic.Int32
	var allPosts atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			allPosts.Add(1)
		}
		var req struct {
			Messages []ChatMessage `json:"messages"`
			Model    string        `json:"model"`
			Stream   bool          `json:"stream"`
		}
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-fixture" ||
			json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil || !req.Stream || req.Model != "session-fixture" || len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			http.Error(w, "fixture request", 400)
			return
		}
		account := -1
		for i := range posts {
			if req.Messages[len(req.Messages)-1].Content == sessionFixtureText(i, 0) {
				account = i
			}
		}
		if account < 0 {
			http.Error(w, "fixture scope", 400)
			return
		}
		posts[account].Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		for i, stage := range stages {
			select {
			case <-stage:
			case <-r.Context().Done():
				return
			}
			if i == 2 {
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", sessionFixtureText(account, i+1))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(up.Close)
	a, server := testApp(t, Config{Mode: "live", DatabaseURL: db, AuthMode: "required", ChatModel: "session-fixture", BaseURL: up.URL + "/v1", APIKey: "local-fixture",
		Admission: AdmissionLimits{GlobalStreams: 192, WorkspaceStreams: 96}, RunReplayReadConnections: 1, Heartbeat: 500 * time.Millisecond})
	defer func() {
		for _, done := range release {
			done()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	if a.streamSessions == nil || a.store.streamAuth == nil || a.store.replay == nil || a.store.replay == a.store.streamAuth {
		t.Fatal("independent authentication pool missing")
	}
	a1 := sseSocketScope(t, server, 0, true)
	a2 := sessionLogin(t, ctx, server, a1, 0)
	b := sseSocketScope(t, server, 1, true)
	idle := sseSocketScope(t, server, 2, true)
	scopes := [3]capacityScope{a1, a2, b}
	var runs [2]Run
	for i, scope := range []capacityScope{a1, b} {
		var conversation Conversation
		capacityRequest(t, ctx, scope, "POST", server.URL+"/api/v1/conversations", newID("sessionchat"), ConversationInput{ProjectID: scope.identity.Project.ID, Title: "SSE capacity fixture"}, &conversation, 201)
		capacityRequest(t, ctx, scope, "POST", server.URL+"/api/v1/conversations/"+conversation.ID+"/runs", newID("sessionrun"), conversationInput(conversation.Version, nil, sessionFixtureText(i, 0)), &runs[i], 201)
		if runs[i].ConversationID != conversation.ID || runs[i].AssistantMessageID == "" {
			t.Fatal("normal conversation turn not attached")
		}
	}
	runPath := func(id string) string { return "/api/v1/runs/" + id + "/events" }
	projectPath := func(s capacityScope) string { return "/api/v1/workspaces/" + s.identity.Workspace.ID + "/events" }
	capacityRequest(t, ctx, b, "GET", server.URL+runPath(runs[0].ID), "", nil, nil, 404)
	capacityRequest(t, ctx, b, "GET", server.URL+projectPath(a1), "", nil, nil, 404)
	anonymous := capacityScope{client: server.Client()}
	capacityRequest(t, ctx, anonymous, "GET", server.URL+runPath(runs[0].ID), "", nil, nil, 401)
	var groups [3][]*sessionSSEReader
	for ix, count := range [3]int{64, 32, 96} {
		scope := scopes[ix]
		account := 0
		if ix == 2 {
			account = 1
		}
		for j := range count {
			path, id := projectPath(scope), ""
			if j%2 == 0 {
				id = runs[account].ID
				path = runPath(id) + "?after=0"
			}
			body := sseSocketOpen(t, ctx, server, scope, path, "").Body
			defer body.Close()
			groups[ix] = append(groups[ix], watchSessionSSE(body, scope, a.store.streamAuth.storageID, id, account, 0))
		}
	}
	if len(a.streamSlots) != 192 || a.scopedStreams(a1.identity.Workspace.ID) != 96 || a.scopedStreams(b.identity.Workspace.ID) != 96 {
		t.Fatal("expected 192 concurrent authorized response bodies")
	}
	var denied struct{ Error APIError }
	capacityRequest(t, ctx, idle, "GET", server.URL+projectPath(idle), "", nil, &denied, 503)
	if denied.Error.Code != "STREAM_LIMIT" {
		t.Fatal("missing global project stream denial")
	}
	capacityRequest(t, ctx, a2, "GET", server.URL+runPath(runs[0].ID), "", nil, &denied, 429)
	if denied.Error.Code != "ADMISSION_STREAM_LIMIT" {
		t.Fatal("missing per-workspace Run denial")
	}
	saveDraft := func(scope capacityScope, expected uint64) {
		input := draftSaveInput{ExpectedRevision: &expected, Nodes: []DraftNode{{ID: "session-scope", Title: fmt.Sprintf("revision-%d", expected+1)}}}
		var doc ProjectDraft
		capacityRequest(t, ctx, scope, "PUT", server.URL+"/api/v1/projects/"+scope.identity.Project.ID+"/draft", newID("draft"), input, &doc, 200)
		if doc.Revision != expected+1 {
			t.Fatal("fixture draft transaction failed")
		}
	}
	release[0]()
	saveDraft(a2, 0)
	saveDraft(b, 0)
	for _, g := range groups {
		waitSessionGroup(t, g, 1, false)
	}
	rounds, checked := sessionRounds(t, a, 3)
	// Occupy the single verified replay connection through revocation. Its
	// absence cannot prevent the independent authentication snapshot.
	replayConn, releaseReplay := holdSessionReaderConn(t, ctx, a.store.replay)
	replayPID := replayConn.PgConn().PID()
	capacityRequest(t, ctx, a1, "POST", server.URL+"/api/v1/auth/logout", "", nil, nil, 200)
	waitSessionGroup(t, groups[0], 1, true)
	capacityRequest(t, ctx, a1, "GET", server.URL+runPath(runs[0].ID), "", nil, nil, 401)
	if len(a.streamSlots) != 128 {
		t.Fatal("logout changed another session's admission")
	}
	// A real authorized HTTP read now waits for the held replay connection.
	// It receives no success headers while the two remaining sessions complete
	// their normal authentication rounds through the separate connection.
	probeCtx, stopProbe := context.WithCancel(ctx)
	defer stopProbe()
	probeReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, server.URL+runPath(runs[0].ID)+"?after=0", nil)
	if err != nil {
		t.Fatal("could not prepare replay probe")
	}
	probeReq.Header.Set("X-Project-ID", a2.identity.Project.ID)
	probeReq.Header.Set("Accept", "text/event-stream")
	probeResult := make(chan *http.Response, 1)
	go func() { res, _ := a2.client.Do(probeReq); probeResult <- res }()
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 129 })
	sessionRounds(t, a, 2)
	select {
	case res := <-probeResult:
		if res != nil {
			res.Body.Close()
		}
		t.Fatal("replay finished while its only connection was reserved")
	default:
	}
	releaseReplay()
	select {
	case res := <-probeResult:
		if res == nil {
			t.Fatal("resumed replay connection failed")
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			t.Fatal("resumed replay returned wrong status")
		}
		reqReader := watchSessionSSE(res.Body, a2, a.store.streamAuth.storageID, runs[0].ID, 0, 0)
		waitSessionGroup(t, []*sessionSSEReader{reqReader}, 1, false)
		res.Body.Close()
	case <-ctx.Done():
		t.Fatal("replay probe did not resume")
	}
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 128 })
	release[1]()
	saveDraft(a2, 1)
	saveDraft(b, 1)
	for _, g := range groups[1:] {
		waitSessionGroup(t, g, 2, false)
	}
	// Break only the verified pool's own idle TCP socket. The next real
	// snapshot must fail closed. PG, the control writer, and the replay
	// pool continue operating and the next reader can establish a new socket.
	conn, releaseAuth := holdSessionReaderConn(t, ctx, a.store.streamAuth)
	oldAuthPID := conn.PgConn().PID()
	if oldAuthPID == replayPID || oldAuthPID == a.store.conn.PgConn().PID() {
		t.Fatal("test did not identify a separate authentication backend")
	}
	if err := conn.PgConn().Conn().Close(); err != nil {
		t.Fatal("could not interrupt owned authentication socket")
	}
	releaseAuth()
	for _, g := range groups[1:] {
		waitSessionGroup(t, g, 2, true)
	}
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 0 })
	if a.store.failed.Load() {
		t.Fatal("read connection failure stopped the control writer")
	}
	fresh := sessionLogin(t, ctx, server, a2, 0)
	restore := func(scope capacityScope, account int) *sessionSSEReader {
		body := sseSocketOpen(t, ctx, server, scope, runPath(runs[account].ID), strconv.Itoa(1)).Body
		t.Cleanup(func() { body.Close() })
		return watchSessionSSE(body, scope, a.store.streamAuth.storageID, runs[account].ID, account, 1)
	}
	recovered := []*sessionSSEReader{restore(fresh, 0), restore(b, 1)}
	waitSessionGroup(t, recovered, 2, false)
	sessionRounds(t, a, 2) // Validate both sessions using the new physical connection.
	replacement, returnReplacement := holdSessionReaderConn(t, ctx, a.store.streamAuth)
	if replacement.PgConn().PID() == oldAuthPID || replacement.PgConn().PID() == replayPID {
		t.Fatal("authentication did not reconnect independently")
	}
	returnReplacement()
	release[2]()
	waitSessionGroup(t, recovered, 3, true)
	for i, scope := range []capacityScope{fresh, b} {
		var got Run
		capacityRequest(t, ctx, scope, "GET", server.URL+"/api/v1/runs/"+runs[i].ID+"/snapshot", "", nil, &got, 200)
		if got.ID != runs[i].ID || got.Seq != 3 || got.Status != "COMPLETED" || got.Output != sessionFixtureText(i, 1)+sessionFixtureText(i, 2) || posts[i].Load() != 1 {
			t.Fatal("original Run, durable output, or model call count changed")
		}
	}
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 0 })
	if allPosts.Load() != 2 {
		t.Fatal("unexpected additional model POST", allPosts.Load())
	}
	t.Logf("peak=192 run_readers=96 project_readers=96 session_readers=64,32,96 full_rounds=%d unique_session_checks=%d logout_eofs=64 auth_transport_failure_eofs=128 reconnect_readers=2 original_runs=2 model_posts=2", rounds, checked)
}
