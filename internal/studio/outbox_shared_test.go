package studio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedFeedCoalescesPagesAndInvalidatesLateReads(t *testing.T) {
	var n outboxNotifications
	scope := subscriptionScope{"w", "p"}
	f, wake, release := n.subscribe(scope)
	defer release()
	peer, _, releasePeer := n.subscribe(scope)
	defer releasePeer()
	var calls atomic.Int32
	started, finish := make(chan struct{}), make(chan struct{})
	load := func(_ context.Context, s subscriptionScope, after uint64) (durablePage, error) {
		if s != scope || after != 1 {
			return durablePage{}, errors.New("wrong project or cursor")
		}
		if calls.Add(1) == 1 {
			close(started)
			<-finish
			return durablePage{High: 2}, nil
		}
		return durablePage{High: 7, Floor: 3}, nil
	}
	old := make(chan durablePage, 1)
	go func() {
		page, _, _ := f.page(t.Context(), 1, load)
		old <- page
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, err := peer.page(ctx, 1, load)
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatal("a cancelled follower started another read or stranded the loader")
	}
	n.projected(map[subscriptionScope]bool{scope: true})
	select {
	case <-wake:
	default:
		t.Fatal("missing committed project wake")
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			p, expires, e := peer.page(t.Context(), 1, load)
			if e != nil || p.High != 7 || p.Floor != 3 || expires.IsZero() {
				t.Error("post-trim readers did not share the current generation")
			}
		})
	}
	wg.Wait()
	close(finish)
	if p := <-old; p.Floor != 3 || calls.Load() != 2 {
		t.Fatal("late page resurrected the old window or fanout repeated reads")
	}
	// Simulate a missed wake and an elapsed shared fallback deadline.
	f.owner.mu.Lock()
	f.pages[1].expires = time.Now().Add(-time.Second)
	f.owner.mu.Unlock()
	if _, _, err = f.page(t.Context(), 1, load); err != nil || calls.Load() != 3 {
		t.Fatal("the fallback failed to recheck the database")
	}
}

func TestSharedFeedBoundsAndReleasesScopeCache(t *testing.T) {
	var n outboxNotifications
	loads := 0
	load := func(_ context.Context, s subscriptionScope, after uint64) (durablePage, error) {
		loads++
		return durablePage{StorageID: s.ProjectID, High: after}, nil
	}
	var releases []func()
	for i := range feedPageCacheLimit + 7 {
		scope := subscriptionScope{"w", fmt.Sprint(i)}
		f, _, release := n.subscribe(scope)
		releases = append(releases, release)
		for range 4 {
			page, _, err := f.page(t.Context(), 0, load)
			if err != nil || page.StorageID != scope.ProjectID {
				t.Fatal("project pages mixed")
			}
		}
	}
	if n.recent.Len() != feedPageCacheLimit || loads != len(releases) {
		t.Fatal("metadata cache exceeded its global limit or duplicate hits queried again")
	}
	for _, release := range releases {
		release()
	}
	if len(n.scopes) != 0 || n.recent.Len() != 0 {
		t.Fatal("closed scopes retained subscriptions or pages")
	}
	f, _, release := n.subscribe(subscriptionScope{"w", "0"})
	defer release()
	releases[0]()
	if n.scopes[f.scope] != f {
		t.Fatal("late cleanup released the reconnected scope")
	}
	dbErr := errors.New("database read failed")
	fail := func(context.Context, subscriptionScope, uint64) (durablePage, error) { return durablePage{}, dbErr }
	if _, _, err := f.page(t.Context(), 0, fail); !errors.Is(err, dbErr) || n.recent.Len() != 0 {
		t.Fatal("a read error entered the page cache")
	}
	for after := range uint64(2) {
		p, _, err := f.page(t.Context(), after, load)
		if err != nil || p.High != after {
			t.Fatal("different page positions were mixed")
		}
	}
}

func TestSharedFeedTenThousandReadPositions(t *testing.T) {
	var n outboxNotifications
	var feeds []*projectFeed
	var releases []func()
	for i := range 80 {
		scope := subscriptionScope{fmt.Sprint(i), "p"}
		for range 125 {
			f, _, release := n.subscribe(scope)
			feeds = append(feeds, f)
			releases = append(releases, release)
		}
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	loads := 0
	load := func(_ context.Context, scope subscriptionScope, after uint64) (durablePage, error) {
		loads++
		return durablePage{High: after + 1}, nil
	}
	for pos := range uint64(2) {
		for _, f := range feeds {
			if p, _, err := f.page(t.Context(), pos, load); err != nil || p.High != pos+1 {
				t.Fatal("cache lost the cursor position")
			}
		}
		if loads != 80*int(pos+1) {
			t.Fatalf("shared page reads=%d for 80 scopes at cursor %d", loads, pos)
		}
	}
}

type brokenFeedWriter struct {
	*httptest.ResponseRecorder
	failed bool
}

func (w *brokenFeedWriter) Write(b []byte) (int, error) {
	if bytes.HasPrefix(b, []byte("event: resource.changed")) {
		w.failed = true
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(b)
}

func TestPostgresSharedFeedTrimRestartTaskAndFailedWriter(t *testing.T) {
	s, databaseURL, admin := outboxStore(t)
	scope := subscriptionScope{"ws_demo", "prj_demo"}
	// The existing retained window is complete. One new task notification is
	// committed but pending. Only projecting it can advance the visible watermark.
	count := eventProjectRetention
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	for _, stmt := range []string{
		"INSERT INTO studio_event_heads(workspace_id,project_id,last_seq,floor_seq) VALUES ('ws_demo','prj_demo',$1+1,0)",
		"INSERT INTO studio_event_consumers(consumer,workspace_id,project_id,last_seq) VALUES ('project-feed-v1','ws_demo','prj_demo',$1)",
		`INSERT INTO studio_outbox_events(event_id,event_key,workspace_id,project_id,project_seq,kind,resource_type,resource_id,resource_version,projected_at)
		 SELECT 'evt_'||n,'k_'||n,'ws_demo','prj_demo',n,'task.updated','task','task_feed',3,CASE WHEN n<=$1 THEN now() END FROM generate_series(1,$1::integer+1) n`,
		`INSERT INTO studio_project_event_feed(event_id,workspace_id,project_id,project_seq)
		 SELECT event_id,workspace_id,project_id,project_seq FROM studio_outbox_events WHERE project_seq<=$1`,
		"UPDATE studio_outbox_limits SET retained=$1,pending=1 WHERE singleton",
	} {
		if _, err = tx.Exec(t.Context(), stmt, count); err != nil {
			t.Fatal(err)
		}
	}
	task := Task{ID: "task_feed", ProjectID: scope.ProjectID, WorkspaceID: scope.WorkspaceID, ProviderMode: "demo", Kind: "image", Status: "SUCCEEDED", Version: 4}
	doc, _ := json.Marshal(task)
	if _, err = tx.Exec(t.Context(), `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
		VALUES('task_feed','ws_demo','prj_demo',$1,'{}','{}',now())`, doc); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	f, wake, release := s.notifications.subscribe(scope)
	before, _, err := f.page(t.Context(), 0, s.readFeedPage)
	if err != nil || before.High != uint64(count) || before.Floor != 0 || len(before.Items) != outboxBatchSize || before.Items[0].Seq != 1 {
		t.Fatal("bad retained page before projection", err)
	}
	if n, err := s.projectOutboxBatch(); err != nil || n != 1 {
		t.Fatal("pending notification did not project", err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("committed retention trim did not wake reader")
	}
	after, _, err := f.page(t.Context(), 0, s.readFeedPage)
	if err != nil || after.High != uint64(count+1) || after.Floor != 1 || len(after.Items) != outboxBatchSize || after.Items[0].Seq != 2 {
		t.Fatal("cache returned the trimmed window", err)
	}
	release()
	s.close()

	app, err := New(Config{DatabaseURL: databaseURL, AssetDir: t.TempDir(), ReferenceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	path := "/api/v1/workspaces/ws_demo/events?project_id=prj_demo"
	cur := durableCursor{Version: 1, StorageID: before.StorageID, UserID: "local", WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID}
	connect := func(c durableCursor) (*http.Response, *bufio.Reader) {
		req, err := http.NewRequestWithContext(t.Context(), "GET", server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Last-Event-ID", encodeDurableCursor(c))
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp, bufio.NewReader(resp.Body)
	}
	expired, r := connect(cur)
	defer expired.Body.Close()
	name, resetToken, data := readSSEFixture(t, r)
	high, err := parseDurableCursor(resetToken, cur)
	if err != nil || name != "stream.reset" || string(data["reason"]) != `"cursor_expired"` || high.Seq != after.High {
		t.Fatal("old cursor did not reset to the consumed watermark after restart")
	}
	cur.Seq = uint64(count)
	failed := &brokenFeedWriter{ResponseRecorder: httptest.NewRecorder()}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
	req.Header.Set("Last-Event-ID", encodeDurableCursor(cur))
	app.Handler().ServeHTTP(failed, req)
	app.store.notifications.mu.Lock()
	live := 0
	if active := app.store.notifications.scopes[scope]; active != nil {
		live = len(active.watchers)
	}
	app.store.notifications.mu.Unlock()
	if !failed.failed {
		t.Fatalf("resource.changed write fault was not reached: status=%d content-type=%q", failed.Code, failed.Header().Get("Content-Type"))
	}
	if live != 1 {
		t.Fatalf("after the write fault, %d project subscriptions remain, want 1", live)
	}
	checkTask := func(want uint64) {
		resp, reader := connect(cur)
		defer resp.Body.Close()
		name, token, event := readSSEFixture(t, reader)
		if name != "resource.changed" || string(event["resource_version"]) != "3" {
			t.Fatal("original task notification was changed on replay")
		}
		name, taskToken, frame := readSSEFixture(t, reader)
		var got Task
		if name != "task.updated" || taskToken != token || json.Unmarshal(frame["task"], &got) != nil || got.ID != task.ID || got.Version != want {
			t.Fatal("task compatibility snapshot was stale or used another cursor")
		}
	}
	checkTask(4)
	if _, err = admin.Exec(t.Context(), `UPDATE studio_tasks SET document=jsonb_set(document,'{version}','5') WHERE id='task_feed'`); err != nil {
		t.Fatal(err)
	}
	checkTask(5)
}
