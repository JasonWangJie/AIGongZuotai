package studio

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reviewPermitHTTPRequest struct {
	metadata mediaModerationHTTPMetadata
	release  chan struct{}
	exited   chan struct{}
}

type reviewPermitHTTPFixture struct {
	engine   *mediaModerationEngine
	requests chan reviewPermitHTTPRequest
	calls    atomic.Int32
	active   atomic.Int32
	peak     atomic.Int32
}

func newReviewPermitHTTPFixture(t *testing.T) *reviewPermitHTTPFixture {
	t.Helper()
	f := &reviewPermitHTTPFixture{requests: make(chan reviewPermitHTTPRequest, 16)}
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		active := f.active.Add(1)
		defer f.active.Add(-1)
		for previous := f.peak.Load(); active > previous; previous = f.peak.Load() {
			if f.peak.CompareAndSwap(previous, active) {
				break
			}
		}
		meta, body := readMediaMultipartFixture(t, r)
		if meta.SHA256 != moderationSHA(body) || meta.ByteSize != int64(len(body)) {
			t.Error("review did not send the complete bound original")
		}
		request := reviewPermitHTTPRequest{metadata: meta, release: make(chan struct{}), exited: make(chan struct{})}
		defer close(request.exited)
		f.requests <- request
		select {
		case <-request.release:
			writeMediaResponse(w, meta, "allow")
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	f.engine = httpMediaEngine(t, server.URL)
	t.Cleanup(func() {
		close(stop)
		server.Close()
		f.engine.http.client.CloseIdleConnections()
	})
	return f
}

func reviewPermitWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("review permit test did not make progress")
		var zero T
		return zero
	}
}

func mustReviewPermit(t *testing.T, e *mediaModerationEngine) *mediaModerationPermit {
	t.Helper()
	permit, ok := e.tryAcquireReviewPermit()
	if !ok || permit == nil {
		t.Fatal("expected an available engine permit")
	}
	t.Cleanup(permit.Close)
	return permit
}

func TestMediaReviewPermitSharesInputAndOutputHTTPBudget(t *testing.T) {
	f := newReviewPermitHTTPFixture(t)
	e := f.engine
	raw, content := mediaBytesFixture(t, "allow.png")
	results := make(chan ModerationDecision, 3)
	for range 2 {
		go func() { results <- e.Check(context.Background(), e.Snapshot(), "input", content, bytes.NewReader(raw)) }()
	}
	first := reviewPermitWait(t, f.requests)
	second := reviewPermitWait(t, f.requests)
	if first.metadata.Phase != "input" || second.metadata.Phase != "input" {
		t.Fatal("expected two real reference checks")
	}
	if permit, ok := e.tryAcquireReviewPermit(); ok || permit != nil {
		t.Fatal("review acquired a third permit while references filled the shared pool")
	}
	close(first.release)
	if got := reviewPermitWait(t, results); got.State != "ALLOWED" {
		t.Fatal(got)
	}
	permit := mustReviewPermit(t, e)
	go func() {
		results <- permit.Check(context.Background(), e.Snapshot(), "output", content, bytes.NewReader(raw))
	}()
	output := reviewPermitWait(t, f.requests)
	if output.metadata.Phase != "output" {
		t.Fatal("output check was not routed through the same HTTP adapter")
	}
	if got := e.Check(context.Background(), e.Snapshot(), "input", content, bytes.NewReader(raw)); got.ReasonCode != "check_busy" {
		t.Fatal("new reference bypassed the shared output permit", got)
	}
	if f.calls.Load() != 3 || f.peak.Load() != 2 {
		t.Fatal("shared checks exceeded two actual HTTP requests", f.calls.Load(), f.peak.Load())
	}
	close(second.release)
	close(output.release)
	for range 2 {
		if got := reviewPermitWait(t, results); got.State != "ALLOWED" {
			t.Fatal(got)
		}
	}
	permit.Close()
	_ = mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
}

func TestMediaReviewPermitWholeBatchDoesNotReacquire(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		metadata, body := readMediaMultipartFixture(t, r)
		if metadata.SHA256 != moderationSHA(body) {
			t.Error("batch item bytes changed")
		}
		writeMediaResponse(w, metadata, "allow")
	}))
	t.Cleanup(server.Close)
	e := httpMediaEngine(t, server.URL)
	defer e.http.client.CloseIdleConnections()
	permit := mustReviewPermit(t, e)
	other := mustReviewPermit(t, e)
	for _, name := range []string{"allow.png", "allow.mp4", "block.png", "review.png"} {
		raw, content := mediaBytesFixture(t, name)
		if got := permit.Check(context.Background(), e.Snapshot(), "output", content, bytes.NewReader(raw)); got.State != "ALLOWED" {
			t.Fatal("held permit tried to reacquire or substituted fixture rules", name, got)
		}
		if extra, ok := e.tryAcquireReviewPermit(); ok || extra != nil {
			t.Fatal("batch released its slot between items")
		}
	}
	if calls.Load() != 4 {
		t.Fatal("batch did not check each complete original exactly once", calls.Load())
	}
	permit.Close()
	_ = mustReviewPermit(t, e)
	if extra, ok := e.tryAcquireReviewPermit(); ok || extra != nil {
		t.Fatal("closing one permit released the other batch's slot")
	}
	other.Close()
}

func TestMediaReviewPermitRejectsConcurrentUseWithoutExtraIO(t *testing.T) {
	f := newReviewPermitHTTPFixture(t)
	e := f.engine
	permit := mustReviewPermit(t, e)
	raw, content := mediaBytesFixture(t, "allow.mp4")
	result := make(chan ModerationDecision, 1)
	go func() {
		result <- permit.Check(context.Background(), e.Snapshot(), "output", content, bytes.NewReader(raw))
	}()
	request := reviewPermitWait(t, f.requests)
	if got := permit.Check(context.Background(), e.Snapshot(), "output", content, bytes.NewReader(raw)); got.ReasonCode != "check_busy" {
		t.Fatal("one permit launched concurrent checks", got)
	}
	other := mustReviewPermit(t, e)
	if f.calls.Load() != 1 {
		t.Fatal("rejected concurrent use performed HTTP I/O")
	}
	close(request.release)
	if got := reviewPermitWait(t, result); got.State != "ALLOWED" {
		t.Fatal(got)
	}
	go func() {
		result <- permit.Check(context.Background(), e.Snapshot(), "output", content, bytes.NewReader(raw))
	}()
	request = reviewPermitWait(t, f.requests)
	close(request.release)
	if got := reviewPermitWait(t, result); got.State != "ALLOWED" || f.calls.Load() != 2 {
		t.Fatal("sequential batch use failed after concurrent misuse was rejected", got)
	}
	other.Close()
}

type blockedReviewReader struct {
	*bytes.Reader
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *blockedReviewReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	return r.Reader.Read(p)
}

func TestMediaReviewPermitCloseWaitsForActualReadExit(t *testing.T) {
	e := mediaPolicyFixture(t)
	permit := mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	raw, content := mediaBytesFixture(t, "allow.png")
	reader := &blockedReviewReader{Reader: bytes.NewReader(raw), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(reader.release) }) })
	result := make(chan ModerationDecision, 1)
	go func() { result <- permit.Check(context.Background(), e.Snapshot(), "output", content, reader) }()
	reviewPermitWait(t, reader.entered)
	permit.Close()
	permit.Close()
	if extra, ok := e.tryAcquireReviewPermit(); ok || extra != nil {
		t.Fatal("Close released capacity while a context-insensitive reader still worked")
	}
	select {
	case got := <-result:
		t.Fatal("check returned before its reader actually exited", got)
	default:
	}
	releaseOnce.Do(func() { close(reader.release) })
	if got := reviewPermitWait(t, result); got.State != "REVIEW" || got.ReasonCode != "check_timeout" {
		t.Fatal("closed active permit returned an allow decision", got)
	}
	_ = mustReviewPermit(t, e)
	if got := permit.Check(context.Background(), e.Snapshot(), "output", content, bytes.NewReader(raw)); got.ReasonCode != "policy_unavailable" {
		t.Fatal("closed permit was reused", got)
	}
}

func TestMediaReviewPermitHTTPCancellationKeepsBatchOwnership(t *testing.T) {
	f := newReviewPermitHTTPFixture(t)
	e := f.engine
	permit := mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	raw, content := mediaBytesFixture(t, "allow.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan ModerationDecision, 1)
	go func() { result <- permit.Check(ctx, e.Snapshot(), "output", content, bytes.NewReader(raw)) }()
	request := reviewPermitWait(t, f.requests)
	cancel()
	if got := reviewPermitWait(t, result); got.State != "REVIEW" || got.ReasonCode != "check_timeout" {
		t.Fatal("HTTP cancellation was not isolated", got)
	}
	reviewPermitWait(t, request.exited)
	if extra, ok := e.tryAcquireReviewPermit(); ok || extra != nil {
		t.Fatal("cancelled item silently surrendered batch ownership")
	}
	permit.Close()
	_ = mustReviewPermit(t, e)
	if f.calls.Load() != 1 {
		t.Fatal("cancellation automatically retried HTTP")
	}
}

func TestMediaReviewPermitCopiesAndConcurrentCloseReleaseOnce(t *testing.T) {
	e := mediaPolicyFixture(t)
	permit := mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	copy := *permit
	var closed sync.WaitGroup
	for i := range 32 {
		closed.Add(1)
		go func() {
			defer closed.Done()
			if i%2 == 0 {
				permit.Close()
			} else {
				copy.Close()
			}
		}()
	}
	done := make(chan struct{})
	go func() { closed.Wait(); close(done) }()
	reviewPermitWait(t, done)
	_ = mustReviewPermit(t, e)
	if extra, ok := e.tryAcquireReviewPermit(); ok || extra != nil {
		t.Fatal("copied / repeatedly closed permit released more than one slot")
	}
	var nilPermit *mediaModerationPermit
	nilPermit.Close()
	new(mediaModerationPermit).Close()
}

type countedReviewReader struct {
	*bytes.Reader
	reads int
	seeks int
}

func (r *countedReviewReader) Read(p []byte) (int, error) { r.reads++; return r.Reader.Read(p) }
func (r *countedReviewReader) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	return r.Reader.Seek(offset, whence)
}

func TestMediaReviewPermitRejectsWrongEngineAndInvalidPolicyBeforeIO(t *testing.T) {
	e := mediaPolicyFixture(t)
	other := mediaPolicyFixture(t)
	permit := mustReviewPermit(t, e)
	raw, content := mediaBytesFixture(t, "allow.png")
	reader := &countedReviewReader{Reader: bytes.NewReader(raw)}
	if got := other.checkWithReviewPermit(context.Background(), permit, other.Snapshot(), "output", content, reader); got.ReasonCode != "policy_unavailable" {
		t.Fatal("same policy allowed borrowing a different engine's permit", got)
	}
	policy := e.Snapshot()
	policy.MaxImageBytes++
	if got := permit.Check(context.Background(), policy, "output", content, reader); got.ReasonCode != "policy_unavailable" {
		t.Fatal("invalid frozen digest accepted", got)
	}
	policy = e.Snapshot()
	policy.AdapterIdentity = moderationSHA([]byte("another-runtime-adapter"))
	policy.Digest = mediaModerationPolicyDigest(*policy)
	if e.compatiblePolicy(policy) {
		t.Fatal("changed adapter identity considered compatible")
	}
	if reader.reads != 0 || reader.seeks != 0 {
		t.Fatal("rejected permit/policy touched private bytes")
	}
	if got := permit.Check(context.Background(), e.Snapshot(), "output", content, reader); got.State != "ALLOWED" {
		t.Fatal("rejection corrupted the rightful engine's permit", got)
	}
}

func TestMediaReviewPermitDisabledAndCancelledPathsDoNotHang(t *testing.T) {
	var disabled *mediaModerationEngine
	permit, ok := disabled.tryAcquireReviewPermit()
	if !ok || permit != nil || disabled.compatiblePolicy(nil) {
		t.Fatal("disabled engine made the review dispatcher wait for capacity")
	}
	if got := permit.Check(context.Background(), nil, "output", MediaModerationContent{}, nil); got.ReasonCode != "policy_unavailable" {
		t.Fatal(got)
	}
	permit.Close()
	if p, ok := new(mediaModerationEngine).tryAcquireReviewPermit(); !ok || p != nil {
		t.Fatal("uninitialized engine waited forever")
	}
	f := newReviewPermitHTTPFixture(t)
	e := f.engine
	active := mustReviewPermit(t, e)
	raw, content := mediaBytesFixture(t, "allow.png")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := active.Check(ctx, e.Snapshot(), "output", content, bytes.NewReader(raw)); got.ReasonCode != "check_timeout" {
		t.Fatal("cancelled context started a check", got)
	}
	if f.calls.Load() != 0 {
		t.Fatal("already cancelled context made an HTTP request")
	}
	active.Close()
	_ = mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
}

func TestMediaReviewPermitReadPanicStillReleasesOnClose(t *testing.T) {
	e := mediaPolicyFixture(t)
	permit := mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	_, content := mediaBytesFixture(t, "allow.png")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected reader panic fixture")
			}
		}()
		permit.Check(context.Background(), e.Snapshot(), "output", content, panicReviewReader{})
	}()
	permit.Close()
	_ = mustReviewPermit(t, e)
}

// Exercise the real transport's body writer after validation already finished.
// Cancellation must not return the shared slot while it still reads private
// bytes; testing only the earlier synchronous hash pass would miss this path.
func TestMediaReviewPermitWaitsForCancelledHTTPBodyReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(server.Close)
	e := httpMediaEngine(t, server.URL)
	transport := e.http.client.Transport.(*http.Transport)
	t.Cleanup(transport.CloseIdleConnections)
	roundTripDone := make(chan struct{})
	e.http.client.Transport = mediaRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		close(roundTripDone)
		return response, err
	})
	permit := mustReviewPermit(t, e)
	_ = mustReviewPermit(t, e)
	raw, content := mediaBytesFixture(t, "allow.mp4")
	reader := &uploadBlockedReviewReader{Reader: bytes.NewReader(raw), entered: make(chan struct{}), release: make(chan struct{})}
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(reader.release) }) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan ModerationDecision, 1)
	go func() { result <- permit.Check(ctx, e.Snapshot(), "output", content, reader) }()
	reviewPermitWait(t, reader.entered)
	cancel()
	permit.Close()
	select {
	case got := <-result:
		t.Fatal("cancelled HTTP check returned while the transport still read private bytes", got)
	case <-time.After(30 * time.Millisecond):
	}
	if extra, ok := e.tryAcquireReviewPermit(); ok || extra != nil {
		t.Fatal("cancelled HTTP write surrendered its real I/O budget")
	}
	released.Do(func() { close(reader.release) })
	if got := reviewPermitWait(t, result); got.State != "REVIEW" {
		t.Fatal(got)
	}
	reviewPermitWait(t, roundTripDone)
	_ = mustReviewPermit(t, e)
}

type uploadBlockedReviewReader struct {
	*bytes.Reader
	seeks   int
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *uploadBlockedReviewReader) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	return r.Reader.Seek(offset, whence)
}
func (r *uploadBlockedReviewReader) Read(p []byte) (int, error) {
	// MP4: initial full hash, rewind for the header, then rewind for upload.
	if r.seeks >= 3 {
		r.once.Do(func() { close(r.entered); <-r.release })
	}
	return r.Reader.Read(p)
}

type panicReviewReader struct{}

func (panicReviewReader) Read([]byte) (int, error)       { panic("private reader fixture") }
func (panicReviewReader) Seek(int64, int) (int64, error) { return 0, nil }

var _ io.ReadSeeker = (*blockedReviewReader)(nil)
