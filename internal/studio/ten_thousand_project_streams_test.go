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
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const fanoutWorkspaces = 80
const fanoutSessions = 4

// Populate synthetic identities only in the disposable schema created by
// sseSocketDatabase. Socket admission still uses the production cookie lookup,
// workspace authorization and continuous database session checks.
func fanoutScopes(t *testing.T, ctx context.Context, a *App, s *httptest.Server) [fanoutWorkspaces][fanoutSessions]capacityScope {
	t.Helper()
	var scopes [fanoutWorkspaces][fanoutSessions]capacityScope
	scopes[0][0] = sseSocketScope(t, s, 0, true)
	for i := 1; i < fanoutSessions; i++ {
		scopes[0][i] = sessionLogin(t, ctx, s, scopes[0][0], 0)
	}
	origin, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal("invalid local fixture origin")
	}
	for w := 1; w < fanoutWorkspaces; w++ {
		var identity testIdentity
		identity.User.ID = newID("usr")
		identity.Workspace.ID = newID("ws")
		identity.Project.ID = newID("prj")
		mail := fmt.Sprintf("project-fanout-%d@example.test", w)
		var tokens [fanoutSessions]string
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, "INSERT INTO studio_users(id,email,name,password_hash) VALUES($1,$2,'Fixture',$3)", identity.User.ID, mail, []byte(dummyPasswordHash)); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "INSERT INTO studio_workspaces(id,owner_id,name) VALUES($1,$2,'Fixture')", identity.Workspace.ID, identity.User.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "INSERT INTO studio_projects(id,workspace_id,name,created_at) VALUES($1,$2,'Fixture',clock_timestamp())", identity.Project.ID, identity.Workspace.ID); err != nil {
					return err
				}
				for i := range tokens {
					token, digest, e := tokenPair()
					if e != nil {
						return e
					}
					if _, e = tx.Exec(ctx, "INSERT INTO studio_sessions(token_hash,user_id,workspace_id,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '1 hour')", digest, identity.User.ID, identity.Workspace.ID); e != nil {
						return e
					}
					tokens[i] = token
				}
				return nil
			})
		})
		if err != nil {
			t.Fatal("isolated fixture provisioning failed")
		}
		for i, token := range tokens {
			client := authClient(t)
			client.Transport = s.Client().Transport
			client.Timeout = 0
			client.Jar.SetCookies(origin, []*http.Cookie{{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true}})
			scopes[w][i] = capacityScope{client, identity}
		}
	}
	return scopes
}

func fanoutPath(scope capacityScope) string {
	return "/api/v1/workspaces/" + scope.identity.Workspace.ID + "/events"
}

func fanoutSocket(ctx context.Context, server *httptest.Server, scope capacityScope, after string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+fanoutPath(scope), nil)
	if err != nil {
		return nil, errors.New("invalid fixture request")
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Project-ID", scope.identity.Project.ID)
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	res, err := scope.client.Do(req)
	if err != nil {
		var code syscall.Errno
		if errors.As(err, &code) {
			return nil, fmt.Errorf("local TCP request failed (OS error %d)", code)
		}
		return nil, errors.New("local HTTP request failed")
	}
	if res.StatusCode != 200 || res.ProtoMajor != 1 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		res.Body.Close()
		return nil, fmt.Errorf("expected an HTTP/1 SSE response, got status %d", res.StatusCode)
	}
	return res.Body, nil
}

func fanoutDraft(t *testing.T, ctx context.Context, s *httptest.Server, scope capacityScope, base uint64) {
	t.Helper()
	input := draftSaveInput{ExpectedRevision: &base, Nodes: []DraftNode{{ID: "fanout", Title: "Broadcast"}}}
	var doc ProjectDraft
	capacityRequest(t, ctx, scope, "PUT", s.URL+"/api/v1/projects/"+scope.identity.Project.ID+"/draft", newID("draft"), input, &doc, 200)
	if doc.Revision != base+1 {
		t.Fatal("draft revision mismatch")
	}
}

func fanoutDraftCursor(t *testing.T, r *bufio.Reader, expected durableCursor, revision uint64) (string, durableCursor) {
	t.Helper()
	name, raw, fields := readSSEFixture(t, r)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal("invalid event payload")
	}
	c, err := validateFanoutDraft(expected, revision, name, raw, data)
	if err != nil {
		t.Fatal(err)
	}
	return raw, c
}

func validateFanoutDraft(expected durableCursor, revision uint64, name, raw string, data []byte) (durableCursor, error) {
	var e DurableEvent
	c, err := parseDurableCursor(raw, expected)
	if err != nil || json.Unmarshal(data, &e) != nil || name != "resource.changed" || e.Kind != "audit.created" ||
		e.Action != "draft.saved" || e.ResourceType != "draft" || e.ResourceID != expected.ProjectID ||
		e.ResourceVersion != revision || e.Seq != expected.Seq+1 || c.Seq != e.Seq {
		return durableCursor{}, errors.New("draft cursor, project or committed revision differs")
	}
	return c, nil
}

func TestFanoutDraftReplayUsesOriginalCursorAndRevision(t *testing.T) {
	prev := durableCursor{Version: 1, StorageID: "store", UserID: "user", WorkspaceID: "workspace", ProjectID: "project", Seq: 7}
	next := prev
	next.Seq++
	other := next
	other.UserID = "different"
	event := DurableEvent{Seq: 8, Kind: "audit.created", ResourceType: "draft", ResourceID: "project", ResourceVersion: 2, Action: "draft.saved"}
	input, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	check := func(prior durableCursor, revision uint64, name string, cursor durableCursor, valid bool) {
		t.Helper()
		got, e := validateFanoutDraft(prior, revision, name, encodeDurableCursor(cursor), input)
		if (e == nil) != valid || valid && got != next {
			t.Fatal("wrong replay decision")
		}
	}
	check(prev, 2, "resource.changed", next, true)
	check(prev, 2, "stream.reset", next, false)
	check(prev, 2, "resource.changed", other, false)
	check(prev, 1, "resource.changed", next, false)
	check(next, 2, "resource.changed", next, false)
}

// Explicit profiles share the same 320 distinct authorized sessions. Only full
// holds ten thousand responses. No host or service URL is accepted as input.
func TestPostgresTenThousandProjectSubscribers(t *testing.T) {
	mode := os.Getenv("STUDIO_SSE_FANOUT")
	per := 125
	switch mode {
	case "":
		t.Skip("opt in with STUDIO_SSE_FANOUT=full or pilot")
	case "pilot":
		per = 4
	case "full":
	default:
		t.Fatal("invalid fanout profile")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("explicit fanout requires a disposable loopback database")
	}
	db := sseSocketDatabase(t)
	total := fanoutWorkspaces * per
	a, server := testApp(t, Config{Mode: "demo", DatabaseURL: db, AuthMode: "required", Admission: AdmissionLimits{GlobalStreams: total, WorkspaceStreams: 128}, RunReplayReadConnections: 1, Heartbeat: 5 * time.Second})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	if a.streamSessions == nil || a.store.streamAuth == nil {
		t.Fatal("shared session validation required")
	}
	scopes := fanoutScopes(t, ctx, a, server)
	first := scopes[0][0]
	// The real registration emits project.created. Start cursor measurement
	// after that setup transaction has reached the normal durable feed.
	sseSocketWait(t, func() bool {
		var idle bool
		if err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			return conn.QueryRow(ctx, "SELECT pending=0 FROM studio_outbox_limits WHERE singleton").Scan(&idle)
		}); err != nil {
			t.Fatal("could not observe setup outbox")
		}
		return idle
	})
	body, err := fanoutSocket(ctx, server, first, "")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	sample := bufio.NewReader(body)
	name, raw, firstData := readSSEFixture(t, sample)
	want := durableCursor{Version: 1, UserID: first.identity.User.ID, WorkspaceID: first.identity.Workspace.ID, ProjectID: first.identity.Project.ID, StorageID: a.store.streamAuth.storageID}
	initial, err := parseDurableCursor(raw, want)
	var reason, resetToken string
	if err != nil || name != "stream.reset" || json.Unmarshal(firstData["reason"], &reason) != nil || reason != "initial" || json.Unmarshal(firstData["cursor"], &resetToken) != nil || raw != resetToken {
		t.Fatal("initial project cursor missing")
	}

	start := time.Now()
	readers := make([]*sessionSSEReader, total)
	var next, opened atomic.Int32
	var once sync.Once
	var dialErr error
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for ctx.Err() == nil {
				i := int(next.Add(1))
				if i >= total {
					return
				}
				scope := scopes[i/per][(i%per)%fanoutSessions]
				b, e := fanoutSocket(ctx, server, scope, "")
				if e != nil {
					once.Do(func() { dialErr = e; cancel() })
					return
				}
				readers[i] = watchSessionSSE(b, scope, a.store.streamAuth.storageID, "", 0, 0)
				opened.Add(1)
			}
		})
	}
	wg.Wait()
	defer func() {
		cancel()
		for _, r := range readers {
			if r != nil {
				r.body.Close()
			}
		}
	}()
	if dialErr != nil || int(opened.Load())+1 != total {
		t.Fatalf("established %d/%d; connection stage: %v", opened.Load()+1, total, dialErr)
	}
	if len(a.streamSlots) != total {
		t.Fatalf("simultaneous admission %d/%d", len(a.streamSlots), total)
	}
	for _, workspace := range scopes {
		var runtime struct{ Streams struct{ Active, Limit int } }
		capacityRequest(t, ctx, workspace[0], "GET", server.URL+"/api/v1/runtime", "", nil, &runtime, 200)
		if runtime.Streams.Active != per || runtime.Streams.Limit != 128 {
			t.Fatal("workspace HTTP occupancy differs")
		}
	}
	var denial struct{ Error APIError }
	capacityRequest(t, ctx, first, "GET", server.URL+fanoutPath(first), "", nil, &denial, 503)
	if denial.Error.Code != "STREAM_LIMIT" {
		t.Fatal("expected global admission denial")
	}
	openMS := time.Since(start).Milliseconds()
	rounds, checked := sessionRounds(t, a, fanoutWorkspaces*fanoutSessions)
	broadcast := time.Now()
	for _, ws := range scopes {
		fanoutDraft(t, ctx, server, ws[0], 0)
	}
	retained, checkpoint := fanoutDraftCursor(t, sample, initial, 1)
	waitSessionGroup(t, readers[1:], 1, false)
	broadcastMS := time.Since(broadcast).Milliseconds()
	if len(a.streamSlots) != total {
		t.Fatal("not all acknowledged subscribers remained admitted")
	}

	// Revoke the login owning the retained cursor. Confirm server EOF before any
	// client cancellation. Other logins of this same account then advance to v2.
	capacityRequest(t, ctx, first, "POST", server.URL+"/api/v1/auth/logout", "", nil, nil, 200)
	eof := make(chan bool, 1)
	go func() {
		for {
			line, err := sample.ReadString('\n')
			if err != nil {
				eof <- err == io.EOF && line == ""
				return
			}
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, ":") {
				eof <- false
				return
			}
		}
	}()
	select {
	case clean := <-eof:
		if !clean {
			t.Fatal("revoked sample did not end cleanly")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("revoked sample remained connected")
	}
	var revoked, survivors []*sessionSSEReader
	for i, r := range readers[1:per] {
		if (i+1)%fanoutSessions == 0 {
			revoked = append(revoked, r)
		} else {
			survivors = append(survivors, r)
		}
	}
	waitSessionGroup(t, revoked, 1, true)
	capacityRequest(t, ctx, first, "GET", server.URL+fanoutPath(first), "", nil, nil, 401)
	lost := len(revoked) + 1
	sseSocketWait(t, func() bool { return len(a.streamSlots) == total-lost })
	fanoutDraft(t, ctx, server, scopes[0][1], 1)
	waitSessionGroup(t, survivors, 2, false)
	waitSessionGroup(t, readers[per:], 1, false)
	resumed, err := fanoutSocket(ctx, server, scopes[0][1], retained)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	_, _ = fanoutDraftCursor(t, bufio.NewReader(resumed), checkpoint, 2)
	if len(a.streamSlots) != total-lost+1 {
		t.Fatal("unexpected occupancy after replay")
	}
	cancel()
	resumed.Close()
	body.Close()
	releaseDeadline := time.After(60 * time.Second)
	for _, r := range readers[1:] {
		select {
		case <-r.done:
		case <-releaseDeadline:
			t.Fatal("local client readers did not exit")
		}
	}
	sseSocketWait(t, func() bool { return len(a.streamSlots) == 0 })
	t.Logf("profile=%s peak=%d verified_first_broadcast=%d unique_sessions=320 workspaces=80 full_auth_rounds=%d session_checks=%d revoked_eofs=%d replayed_drafts=1 final_slots=0 opening_ms=%d broadcast_ms=%d", mode, total, total, rounds, checked, lost, openMS, broadcastMS)
	if file := os.Getenv("STUDIO_SSE_FANOUT_REPORT"); file != "" {
		writeCapacityReport(t, file, map[string]any{
			"profile":                   mode,
			"scope":                     "loopback HTTP/1 project SSE with PostgreSQL authentication",
			"simultaneous_http_readers": total,
			"first_broadcast_verified":  total,
			"unique_sessions":           fanoutWorkspaces * fanoutSessions,
			"http_issued_sessions":      fanoutSessions,
			"prepared_sessions":         (fanoutWorkspaces - 1) * fanoutSessions,
			"workspaces":                fanoutWorkspaces,
			"readers_per_workspace":     per,
			"checked_full_rounds":       rounds,
			"distinct_session_checks":   checked,
			"revoked_server_eofs":       lost,
			"second_draft_live_readers": len(survivors),
			"resumed_original_cursor":   true,
			"released_all_slots":        true,
			"opening_ms":                openMS,
			"broadcast_ms":              broadcastMS,
		})
	}
}
