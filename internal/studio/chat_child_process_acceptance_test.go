package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func childFixtureEnvironment(overrides map[string]string) []string {
	env := make([]string, 0, 32)
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "PATH", "PATHEXT", "USERPROFILE", "LANG", "LC_ALL", "TZ"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

type childAcceptanceServer struct {
	proc *exec.Cmd
	base string
	done chan struct{}
}

func startChildAcceptanceServer(t *testing.T, ctx context.Context, binary string, settings map[string]string) *childAcceptanceServer {
	t.Helper()
	return startObservedChildAcceptanceServer(t, ctx, binary, settings, io.Discard)
}

func startObservedChildAcceptanceServer(t *testing.T, ctx context.Context, binary string, settings map[string]string, diagnostics io.Writer) *childAcceptanceServer {
	t.Helper()
	socket, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("loopback listener allocation failed")
	}
	addr := socket.Addr().String()
	if err = socket.Close(); err != nil {
		t.Fatal("loopback allocation release failed")
	}
	settings["APP_ADDR"] = addr
	cmd := exec.Command(binary)
	cmd.Dir = t.TempDir()
	cmd.Env = childFixtureEnvironment(settings)
	// The isolated child service never inherits test runner output or an operator environment.
	cmd.Stdout, cmd.Stderr = io.Discard, diagnostics
	if err = cmd.Start(); err != nil {
		t.Fatal("standard server failed to start")
	}
	service := &childAcceptanceServer{proc: cmd, base: "http://" + addr, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(service.done) }()
	t.Cleanup(func() { service.crash(t) })
	health := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	deadline := time.NewTimer(35 * time.Second)
	defer deadline.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", service.base+"/readyz", nil)
		res, err := health.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return service
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("server startup deadline")
		case <-deadline.C:
			t.Fatal("standard controller did not become ready")
		case <-service.done:
			t.Fatal("standard controller exited before ready")
		case <-time.After(80 * time.Millisecond):
		}
	}
}

func (s *childAcceptanceServer) crash(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
		return
	default:
	}
	if err := s.proc.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Error("could not stop owned controller")
	}
	select {
	case <-s.done:
	case <-time.After(8 * time.Second):
		t.Error("owned controller failed to exit")
	}
}

// Query Windows' filtered TCP ownership table at the exact fixture endpoint.
// The only process inspection is of direct descendants of the owned parent.
const childKernelQuery = `
$ErrorActionPreference='Stop'
$owned=@(Get-CimInstance Win32_Process -Filter ("ParentProcessId = "+$env:FIXTURE_PARENT) -Property ProcessId,ParentProcessId,ExecutablePath | Where-Object { $_.ExecutablePath -eq $env:FIXTURE_BINARY })
if ($env:FIXTURE_PORT -eq '0') { [Console]::WriteLine($owned.Count); exit 0 }
$allowed=@{}
foreach ($value in $env:FIXTURE_PEERS.Split(',')) { $allowed[[int]$value]=$true }
$ids=@{}
foreach ($child in $owned) { $ids[[int]$child.ProcessId]=$true }
if ($env:FIXTURE_EXPECT_PARENT -eq '1') {
  $ids=@{}
  $self=@(Get-CimInstance Win32_Process -Filter ("ProcessId = "+$env:FIXTURE_PARENT) -Property ProcessId,ExecutablePath | Where-Object { $_.ExecutablePath -eq $env:FIXTURE_BINARY })
  if ($self.Count -eq 1) { $ids[[int]$self[0].ProcessId]=$true }
}
$rows=@(Get-NetTCPConnection -LocalAddress 127.0.0.1 -RemoteAddress 127.0.0.1 -RemotePort ([int]$env:FIXTURE_PORT) -State Established |
  Where-Object { $allowed.ContainsKey([int]$_.LocalPort) -and $ids.ContainsKey([int]$_.OwningProcess) } |
  ForEach-Object { @{peer=[int]$_.LocalPort; process=[int]$_.OwningProcess} })
[Console]::WriteLine((ConvertTo-Json -InputObject $rows -Compress))
`

func childKernelObservation(ctx context.Context, server *childAcceptanceServer, port int, peers []int, expectParent bool) ([]byte, error) {
	if runtime.GOOS != "windows" {
		return nil, errors.New("Windows filtered TCP ownership probe required")
	}
	if len(peers) > maxChatChildProcesses*maxChatChildSlots {
		return nil, errors.New("too many peers for owned kernel observation")
	}
	parts := make([]string, len(peers))
	for i, v := range peers {
		if v < 1 || v > 65535 {
			return nil, errors.New("invalid owned peer")
		}
		parts[i] = strconv.Itoa(v)
	}
	sub, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	host := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(sub, host, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", childKernelQuery)
	mode := "0"
	if expectParent {
		mode = "1"
	}
	cmd.Env = childFixtureEnvironment(map[string]string{"FIXTURE_BINARY": server.proc.Path, "FIXTURE_PARENT": strconv.Itoa(server.proc.Process.Pid), "FIXTURE_PORT": strconv.Itoa(port), "FIXTURE_PEERS": strings.Join(parts, ","), "FIXTURE_EXPECT_PARENT": mode})
	out, err := cmd.Output()
	limit := 16 << 10
	if len(peers) > 512 {
		// The fixture emits only the requested peer-port and owner PID pairs.
		limit = 64 << 10
	} else if len(peers) >= 512 {
		limit = 32 << 10
	}
	if err != nil || len(out) > limit {
		return nil, errors.New("filtered local kernel observation failed")
	}
	return out, nil
}

func childOwners(t *testing.T, ctx context.Context, controller *childAcceptanceServer, gatewayPort int, peers []int) map[int]int {
	t.Helper()
	return observedOwners(t, ctx, controller, gatewayPort, peers, false)
}

func observedOwners(t *testing.T, ctx context.Context, controller *childAcceptanceServer, gatewayPort int, peers []int, expectParent bool) map[int]int {
	t.Helper()
	out, err := childKernelObservation(ctx, controller, gatewayPort, peers, expectParent)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Peer, Process int }
	if json.Unmarshal(out, &rows) != nil || len(rows) != len(peers) {
		t.Fatalf("owned TCP coverage actual=%d expected=%d", len(rows), len(peers))
	}
	result := make(map[int]int, len(rows))
	for _, r := range rows {
		if r.Process <= 0 || (r.Process == controller.proc.Process.Pid) != expectParent || result[r.Peer] != 0 {
			t.Fatal("ambiguous child ownership")
		}
		result[r.Peer] = r.Process
	}
	for _, port := range peers {
		if result[port] == 0 {
			t.Fatal("missing connection owner")
		}
	}
	return result
}

func killObservedChild(t *testing.T, ctx context.Context, controller *childAcceptanceServer, gatewayPort, peer, pid int) {
	t.Helper()
	target, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal("could not pin observed child handle")
	}
	defer target.Release()
	// On Windows the handle pins the process object. Verify the live peer and
	// direct parent again while holding that handle; never kill by a later PID lookup.
	if childOwners(t, ctx, controller, gatewayPort, []int{peer})[peer] != pid {
		t.Fatal("owned child changed before termination")
	}
	if target.Kill() != nil {
		t.Fatal("could not terminate verified owned child")
	}
}

func waitChildExit(t *testing.T, ctx context.Context, controller *childAcceptanceServer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := childKernelObservation(ctx, controller, 0, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(raw)) == "0" {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatal("owned child processes did not exit after controller termination")
}

func childFixturePort(address string) (int, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return 0, fmt.Errorf("fixture must bind IPv4 loopback")
	}
	return strconv.Atoi(port)
}
