package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Only the deliberately stalled subscriber uses this loopback listener. Writes
// and deadlines are handled by net/http and the kernel; record actual errors.
type sseSocketListener struct {
	*net.TCPListener
	timedOut chan time.Duration
}

type sseSocketConn struct {
	*net.TCPConn
	timedOut chan<- time.Duration
}

func (l *sseSocketListener) Accept() (net.Conn, error) {
	c, err := l.AcceptTCP()
	if err != nil {
		return nil, err
	}
	if err = c.SetWriteBuffer(1024); err != nil {
		c.Close()
		return nil, err
	}
	return &sseSocketConn{c, l.timedOut}, nil
}

func (c *sseSocketConn) Write(b []byte) (int, error) {
	start := time.Now()
	n, err := c.TCPConn.Write(b)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		select {
		case c.timedOut <- time.Since(start):
		default:
		}
	}
	return n, err
}

func sseSocketDatabase(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("TEST_DATABASE_URL")
	if uri == "" {
		t.Skip("isolated loopback PostgreSQL test database required")
	}
	if !mixedLoopbackDatabase(uri) {
		t.Fatal("requires isolated loopback PostgreSQL")
	}
	return testDatabase(t)
}

func sseSocketWait(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("SSE occupancy did not reach its expected value")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sseSocketScope(t *testing.T, s *httptest.Server, account int, authenticated bool) capacityScope {
	t.Helper()
	c := authClient(t)
	c.Transport = s.Client().Transport // owned local listener; no proxy
	c.Timeout = 0
	var id testIdentity
	if authenticated {
		id = registerFixture(t, c, s.URL, fmt.Sprintf("socket-%d@example.test", account))
	} else {
		id.Project.ID = "prj_demo"
		id.Workspace.ID = "ws_demo"
	}
	return capacityScope{c, id}
}

func sseSocketOpen(t *testing.T, ctx context.Context, s *httptest.Server, scope capacityScope, path, after string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+path, nil)
	if err != nil {
		t.Fatal("could not prepare stream")
	}
	req.Header.Set("X-Project-ID", scope.identity.Project.ID)
	req.Header.Set("Accept", "text/event-stream")
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	res, err := scope.client.Do(req)
	if err != nil {
		t.Fatal("could not open the local stream")
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		res.Body.Close()
		t.Fatal("unexpected stream response", res.StatusCode)
	}
	return res
}

func sseSocketPausedReader(t *testing.T, app *App, scope capacityScope, runID string) (*bufio.Reader, <-chan time.Duration) {
	t.Helper()
	s := httptest.NewUnstartedServer(app.Handler())
	tcp := s.Listener.(*net.TCPListener)
	if !tcp.Addr().(*net.TCPAddr).IP.IsLoopback() {
		tcp.Close()
		t.Fatal("slow reader requires an owned loopback listener")
	}
	listener := &sseSocketListener{tcp, make(chan time.Duration, 2)}
	s.Listener = listener
	s.Start()
	t.Cleanup(s.Close)
	c, err := net.DialTCP("tcp", nil, tcp.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal("could not connect the local slow socket")
	}
	t.Cleanup(func() { c.Close() })
	if c.SetReadBuffer(1024) != nil || c.SetReadDeadline(time.Now().Add(18*time.Second)) != nil {
		t.Fatal("could not bound the local receive window")
	}
	req, err := http.NewRequest(http.MethodGet, s.URL+"/api/v1/runs/"+runID+"/events?after=0", nil)
	if err != nil {
		t.Fatal("invalid local stream request")
	}
	req.Header.Set("X-Project-ID", scope.identity.Project.ID)
	req.Header.Set("Accept", "text/event-stream")
	for _, cookie := range scope.client.Jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	if err := req.Write(c); err != nil {
		t.Fatal("could not send the slow reader request")
	}
	res, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil || res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("slow reader did not establish the production SSE route")
	}
	// Close the underlying socket directly on cleanup. Response.Body.Close on
	// a raw http.ReadResponse can drain the unread chunked stream.
	return bufio.NewReader(res.Body), listener.timedOut
}

const socketFloodDeltas = 12

func sseSocketDelta(seq int) string {
	if seq == 1 {
		return "first"
	}
	// The actual public output stays below the 128 KiB Run limit. JSON
	// escaping of '<' readily fills the two deliberately small kernel buffers.
	return strconv.Itoa(seq) + strings.Repeat("<", 8<<10)
}

type sseSocketGateway struct {
	first, flood, finish chan struct{}
	posts                atomic.Int32
}

func (g *sseSocketGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	g.posts.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.(http.Flusher).Flush()
	emit := func(seq int) {
		content, _ := json.Marshal(sseSocketDelta(seq))
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", content)
		w.(http.Flusher).Flush()
	}
	wait := func(c <-chan struct{}) bool {
		select {
		case <-c:
			return true
		case <-r.Context().Done():
			return false
		}
	}
	if !wait(g.first) {
		return
	}
	emit(1)
	if !wait(g.flood) {
		return
	}
	for seq := 2; seq <= socketFloodDeltas+1; seq++ {
		emit(seq)
	}
	if wait(g.finish) {
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func sseSocketExpect(t *testing.T, r *bufio.Reader, runID string, seq int) string {
	t.Helper()
	name, id, body := readSSEFixture(t, r)
	if id != strconv.Itoa(seq) || string(body["seq"]) != id || string(body["run_id"]) != strconv.Quote(runID) {
		t.Fatal("incorrect or duplicate event identity", seq)
	}
	if seq == socketFloodDeltas+2 {
		if name != "run.completed" || string(body["status"]) != "\"COMPLETED\"" {
			t.Fatal("incorrect terminal event")
		}
	} else {
		var value string
		if name != "message.delta" || json.Unmarshal(body["delta"], &value) != nil || value != sseSocketDelta(seq) {
			t.Fatal("changed live or replayed delta", seq)
		}
	}
	return id
}
