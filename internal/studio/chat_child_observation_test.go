package studio

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChatChildGatewayContract(t *testing.T) {
	g := newChildAcceptanceGateway(t.Context(), 4)
	for _, tick := range g.ticks {
		close(tick)
	}
	close(g.lanes[0].release)
	send := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12032"
		req.Header.Set("Authorization", "Bearer "+g.key)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		return w
	}
	payload := func(model, prompt string) string {
		return fmt.Sprintf(`{"model":%q,"stream":true,"max_tokens":128,"messages":[{"role":"user","content":%q}],"stream_options":{"include_usage":true}}`, model, prompt)
	}
	valid := payload(childUpstream(0), activePrompt(0))
	res := send(valid)
	next := 0
	usage, err := parseOpenAIStreamUsage(res.Body, func(delta string) error {
		if delta != activeDelta(0, next) {
			return fmt.Errorf("fixture delta differs")
		}
		next++
		return nil
	})
	if err != nil || res.Code != 200 || next != activeStreamSteps || usage.TotalTokens != 5 || !usage.Complete {
		t.Fatal("actual upstream decoder rejected fixture contract")
	}
	if send(valid).Code != 409 || send(payload(childUpstream(4), activePrompt(4))).Code != 409 || send(payload("wrong", activePrompt(1))).Code != 400 || g.invalid != 3 {
		t.Fatal("fixture accepted replay, queue dispatch, or wrong frozen model")
	}
}

// A single owned test child and loopback socket validate Windows observation
// independently of PostgreSQL, a running workbench or the capacity experiment.
func TestChatChildFilteredKernelOwnership(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows kernel ownership probe")
	}
	socket, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("could not open owned socket")
	}
	defer socket.Close()
	port, err := childFixturePort(socket.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test binary unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := exec.CommandContext(ctx, exe, "-test.run", "^TestChatChildKernelPeerHelper$")
	p.Env = childFixtureEnvironment(map[string]string{"STUDIO_CHILD_INTERNAL_KERNEL_PEER": strconv.Itoa(port)})
	if err = p.Start(); err != nil {
		t.Fatal("owned observation child could not start")
	}
	defer func() { _ = p.Process.Kill(); _ = p.Wait() }()
	_ = socket.(*net.TCPListener).SetDeadline(time.Now().Add(6 * time.Second))
	conn, err := socket.Accept()
	if err != nil {
		t.Fatal("owned test child failed to connect")
	}
	defer conn.Close()
	peer, err := childFixturePort(conn.RemoteAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal("test host unavailable")
	}
	defer parent.Release()
	ctrl := &childAcceptanceServer{proc: &exec.Cmd{Path: exe, Process: parent}}
	if owner := childOwners(t, ctx, ctrl, port, []int{peer})[peer]; owner != p.Process.Pid {
		t.Fatal("kernel observation differed from actual spawned PID")
	}
}

func TestChatChildKernelPeerHelper(t *testing.T) {
	raw := os.Getenv("STUDIO_CHILD_INTERNAL_KERNEL_PEER")
	if raw == "" {
		t.Skip("private peer helper")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("invalid port")
	}
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", raw), 3*time.Second)
	if err != nil {
		t.Fatal("test peer connect failed")
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(18 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
}
