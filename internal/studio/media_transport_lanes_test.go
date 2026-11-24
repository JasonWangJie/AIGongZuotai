package studio

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mediaTransportProvider(t *testing.T, server *httptest.Server, submit, query int) *AsyncMediaProvider {
	t.Helper()
	p, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: server.URL, APIKey: fixtureMediaKey,
		CredentialID: "fixture-credential", ImageModel: "fixture-image-model", AssetDir: t.TempDir(),
		SubmitConcurrency: submit, QueryConcurrency: query, BodyStore: &mediaFixtureBodyStore{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func mediaTransportSubmission(t *testing.T, p *AsyncMediaProvider, edit bool) MediaSubmission {
	t.Helper()
	input := mediaFixtureInput("image", 1)
	var submission MediaSubmission
	var err error
	if edit {
		submission, err = p.PrepareEditModel(context.Background(), input, "fixture-image-model", []MediaEditSource{mediaFixtureReference(fixturePNG(t))})
	} else {
		submission, err = p.Prepare(input)
	}
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func mediaTransportResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"task_id":"asyncimg_fixture"}`)
		return
	}
	_, _ = io.WriteString(w, `{"task_id":"asyncimg_fixture","media_type":"image","stage":"generating","status":"processing","progress":20}`)
}

func mediaTransportWait[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("media HTTP fixture did not reach the expected event")
		var zero T
		return zero
	}
}

func TestMediaTransportLaneConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(mediaTransportResponse))
	defer server.Close()
	for _, test := range []struct{ submit, query, wantSubmit, wantQuery int }{{0, 0, 2, 2}, {1, 32, 1, 32}, {32, 1, 32, 1}} {
		p := mediaTransportProvider(t, server, test.submit, test.query)
		submit := p.client.Transport.(*http.Transport)
		query := p.queryClient.Transport.(*http.Transport)
		if submit == query || p.editClient.Transport != submit || p.assets.client.Transport == submit || p.assets.client.Transport == query {
			t.Fatal("submission, query and asset transports are not isolated; edits must share submission transport")
		}
		if submit.MaxConnsPerHost != test.wantSubmit || submit.MaxIdleConnsPerHost != test.wantSubmit || submit.MaxIdleConns != test.wantSubmit ||
			query.MaxConnsPerHost != test.wantQuery || query.MaxIdleConnsPerHost != test.wantQuery || query.MaxIdleConns != test.wantQuery {
			t.Fatal("media connection pools do not match the configured lane budgets")
		}
		if p.client.Timeout != 30*time.Second || p.queryClient.Timeout != 30*time.Second || p.editClient.Timeout != 2*time.Minute ||
			submit.ResponseHeaderTimeout != 20*time.Second || query.ResponseHeaderTimeout != 20*time.Second {
			t.Fatal("lane split changed existing request or response header deadlines")
		}
	}
	for _, test := range []struct{ submit, query int }{{-1, 2}, {33, 2}, {2, -1}, {2, MaxMediaQueryWorkers + 1}} {
		p, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: server.URL, APIKey: fixtureMediaKey, CredentialID: "fixture", ImageModel: "image",
			AssetDir: t.TempDir(), SubmitConcurrency: test.submit, QueryConcurrency: test.query})
		if err == nil {
			_ = p.Close()
			t.Fatalf("invalid concurrency accepted: %+v", test)
		}
	}
}

// These tests use production HTTP clients, not injected RoundTrippers. Holding
// one HTTP/1 connection must neither occupy nor release the other lane's slot.
func TestMediaTransportBlockedLaneDoesNotBlockOtherLane(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		edit   bool
	}{{"json-submit", http.MethodPost, false}, {"edit-submit", http.MethodPost, true}, {"query", http.MethodGet, false}} {
		t.Run(test.name, func(t *testing.T) {
			blocked := make(chan struct{}, 4)
			release := make(chan struct{})
			var once sync.Once
			var posts, queries atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
					t.Error("lane lost credential authorization")
				}
				if r.Method == http.MethodPost {
					posts.Add(1)
				} else {
					queries.Add(1)
				}
				if r.Method == test.method {
					blocked <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				mediaTransportResponse(w, r)
			}))
			defer server.Close()
			defer once.Do(func() { close(release) })
			p := mediaTransportProvider(t, server, 1, 1)
			submission := mediaTransportSubmission(t, p, test.edit)
			handle := mediaFixtureHandle(p, "image", 1)
			first := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() {
				var err error
				if test.method == http.MethodPost {
					_, err = p.SubmitPrepared(ctx, submission)
				} else {
					_, err = p.Query(ctx, handle)
				}
				first <- err
			}()
			mediaTransportWait(t, blocked)
			otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
			defer otherCancel()
			var err error
			if test.method == http.MethodPost {
				_, err = p.Query(otherCtx, handle)
			} else {
				_, err = p.SubmitPrepared(otherCtx, submission)
			}
			if err != nil {
				t.Fatalf("blocked %s occupied the other lane: %v", test.method, err)
			}

			// Exercise the same pool with the other submit format: both JSON and
			// multipart must wait, and cancellation while waiting must send no POST.
			secondSubmission := mediaTransportSubmission(t, p, !test.edit)
			waiting := make(chan struct{}, 1)
			waitCtx, waitCancel := context.WithTimeout(ctx, 150*time.Millisecond)
			defer waitCancel()
			waitCtx = httptrace.WithClientTrace(waitCtx, &httptrace.ClientTrace{GetConn: func(string) { waiting <- struct{}{} }})
			second := make(chan error, 1)
			go func() {
				var err error
				if test.method == http.MethodPost {
					_, err = p.SubmitPrepared(waitCtx, secondSubmission)
				} else {
					_, err = p.Query(waitCtx, handle)
				}
				second <- err
			}()
			mediaTransportWait(t, waiting)
			mediaErr := requireMediaError(t, mediaTransportWait(t, second))
			if test.method == http.MethodPost && (!mediaErr.SubmissionUnknown || mediaErr.Code != "SUBMIT_UNKNOWN") ||
				test.method == http.MethodGet && (!mediaErr.Retryable || mediaErr.Code != "QUERY_TEMPORARY") {
				t.Fatalf("lane cancellation changed recovery classification: %+v", mediaErr)
			}
			if posts.Load() != 1 || queries.Load() != 1 {
				t.Fatalf("same lane exceeded its budget or retried a cancelled request: posts=%d queries=%d", posts.Load(), queries.Load())
			}
			once.Do(func() { close(release) })
			if err := mediaTransportWait(t, first); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Query(ctx, handle); err != nil {
				t.Fatalf("cancelled waiter leaked query capacity: %v", err)
			}
			if _, err := p.SubmitPrepared(ctx, secondSubmission); err != nil {
				t.Fatalf("cancelled waiter leaked submission capacity: %v", err)
			}
		})
	}
}

func TestMediaTransportLaneConcurrentConnectionBudgets(t *testing.T) {
	entered := make(chan string, 16)
	release := make(chan struct{})
	var once sync.Once
	var mutex sync.Mutex
	active, peak := map[string]int{}, map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mutex.Lock()
		active[r.Method]++
		peak[r.Method] = max(peak[r.Method], active[r.Method])
		mutex.Unlock()
		defer func() { mutex.Lock(); active[r.Method]--; mutex.Unlock() }()
		entered <- r.Method
		select {
		case <-release:
			mediaTransportResponse(w, r)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer once.Do(func() { close(release) })
	p := mediaTransportProvider(t, server, 2, 3)
	submission, edit := mediaTransportSubmission(t, p, false), mediaTransportSubmission(t, p, true)
	handle := mediaFixtureHandle(p, "image", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 8)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { started <- struct{}{} }})
	done := make(chan error, 7)
	for i := range 3 {
		go func() {
			request := submission
			if i%2 == 1 {
				request = edit
			}
			_, err := p.SubmitPrepared(ctx, request)
			done <- err
		}()
	}
	for range 4 {
		go func() { _, err := p.Query(ctx, handle); done <- err }()
	}
	for range 7 {
		mediaTransportWait(t, started)
	}
	seen := map[string]int{}
	for range 5 {
		seen[mediaTransportWait(t, entered)]++
	}
	if seen[http.MethodPost] != 2 || seen[http.MethodGet] != 3 {
		t.Fatalf("lane capacity was shared instead of independent: %+v", seen)
	}
	select {
	case method := <-entered:
		t.Fatalf("extra %s escaped the connection budget before release", method)
	case <-time.After(100 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	for range 7 {
		if err := mediaTransportWait(t, done); err != nil {
			t.Fatal(err)
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	if peak[http.MethodPost] != 2 || peak[http.MethodGet] != 3 {
		t.Fatalf("unexpected measured lane peaks: %+v", peak)
	}
}

func TestMediaTransportActiveCancellationReleasesOnlyItsRequest(t *testing.T) {
	for _, test := range []struct {
		name  string
		edit  bool
		query bool
	}{{"json", false, false}, {"edit", true, false}, {"query", false, true}} {
		t.Run(test.name, func(t *testing.T) {
			entered, exited := make(chan struct{}, 1), make(chan struct{}, 1)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) == 1 {
					entered <- struct{}{}
					<-r.Context().Done()
					exited <- struct{}{}
					return
				}
				mediaTransportResponse(w, r)
			}))
			defer server.Close()
			p := mediaTransportProvider(t, server, 1, 1)
			submission := mediaTransportSubmission(t, p, test.edit)
			handle := mediaFixtureHandle(p, "image", 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if test.query {
					_, err = p.Query(ctx, handle)
				} else {
					_, err = p.SubmitPrepared(ctx, submission)
				}
				done <- err
			}()
			mediaTransportWait(t, entered)
			cancel()
			classified := requireMediaError(t, mediaTransportWait(t, done))
			if test.query && classified.Code != "QUERY_TEMPORARY" || !test.query && !classified.SubmissionUnknown {
				t.Fatalf("active cancellation changed recovery semantics: %+v", classified)
			}
			mediaTransportWait(t, exited)
			nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
			defer nextCancel()
			if _, err := p.SubmitPrepared(nextCtx, submission); err != nil {
				t.Fatalf("cancelled active request leaked the submission slot: %v", err)
			}
			if _, err := p.Query(nextCtx, handle); err != nil {
				t.Fatalf("cancelled active request leaked the query slot: %v", err)
			}
			if calls.Load() != 3 {
				t.Fatalf("cancellation triggered an unrequested retry: %d", calls.Load())
			}
		})
	}
}

func TestMediaTransportLaneIdleCloseIsIndependentAndClosesAssets(t *testing.T) {
	requests := make(chan string, 8)
	closed := make(chan string, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests <- r.RemoteAddr
		mediaTransportResponse(w, r)
	}))
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- conn.RemoteAddr().String()
		}
	}
	server.Start()
	defer server.Close()
	p := mediaTransportProvider(t, server, 1, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.SubmitPrepared(ctx, mediaTransportSubmission(t, p, false)); err != nil {
		t.Fatal(err)
	}
	submitAddress := mediaTransportWait(t, requests)
	if _, err := p.Query(ctx, mediaFixtureHandle(p, "image", 1)); err != nil {
		t.Fatal(err)
	}
	queryAddress := mediaTransportWait(t, requests)
	if submitAddress == queryAddress {
		t.Fatal("submission and query used the same TCP connection")
	}
	p.client.CloseIdleConnections()
	if got := mediaTransportWait(t, closed); got != submitAddress {
		t.Fatalf("closing submission pool closed another lane: %q", got)
	}
	if _, err := p.Query(ctx, mediaFixtureHandle(p, "image", 1)); err != nil {
		t.Fatal(err)
	}
	if got := mediaTransportWait(t, requests); got != queryAddress {
		t.Fatal("closing submission pool prevented independent query connection reuse")
	}
	if _, err := p.SubmitPrepared(ctx, mediaTransportSubmission(t, p, true)); err != nil {
		t.Fatal(err)
	}
	editAddress := mediaTransportWait(t, requests)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{mediaTransportWait(t, closed): true, mediaTransportWait(t, closed): true}
	if !got[queryAddress] || !got[editAddress] {
		t.Fatalf("provider close did not close both idle pools: %+v", got)
	}
	if _, err := p.assets.root.Stat("."); err == nil {
		t.Fatal("provider close left the private asset root open")
	}
}

func TestMediaTransportLanesRefuseRedirectAndNeverRepeatPost(t *testing.T) {
	var redirected, posts atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); mediaTransportResponse(w, r) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		w.Header().Set("Location", target.URL+"/sink")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	p := mediaTransportProvider(t, server, 1, 1)
	for _, edit := range []bool{false, true} {
		_, err := p.SubmitPrepared(context.Background(), mediaTransportSubmission(t, p, edit))
		if mediaErr := requireMediaError(t, err); !mediaErr.SubmissionUnknown || !mediaErr.ReconcileRequired {
			t.Fatalf("redirect did not preserve uncertain submission: %+v", mediaErr)
		}
	}
	if _, err := p.Query(context.Background(), mediaFixtureHandle(p, "image", 1)); requireMediaError(t, err).Code != "QUERY_REQUIRES_RECONCILIATION" {
		t.Fatal("query followed a redirect")
	}
	if redirected.Load() != 0 || posts.Load() != 2 {
		t.Fatalf("requests followed redirects or retried submissions: redirects=%d posts=%d", redirected.Load(), posts.Load())
	}

	// Reuse a previously successful connection, then lose the response after the
	// upstream has read a nonempty POST. Idempotency-Key must not enable replay.
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted-response-lost-edit-%t", edit), func(t *testing.T) {
			var count atomic.Int32
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if count.Add(1) == 1 {
					mediaTransportResponse(w, r)
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			defer fixture.Close()
			p := mediaTransportProvider(t, fixture, 1, 1)
			if _, err := p.SubmitPrepared(context.Background(), mediaTransportSubmission(t, p, edit)); err != nil {
				t.Fatal(err)
			}
			_, err := p.SubmitPrepared(context.Background(), mediaTransportSubmission(t, p, edit))
			if !requireMediaError(t, err).SubmissionUnknown || count.Load() != 2 {
				t.Fatalf("response loss retried or became safe failure: calls=%d err=%v", count.Load(), err)
			}
		})
	}
}
