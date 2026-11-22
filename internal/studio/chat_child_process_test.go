package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Match the standard binary's early subcommand in go test executables. This
// also lets authenticated integration tests exercise the real process pool.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "chat-execution-child":
			if os.Getenv("STUDIO_TEST_PARENT_PRIVATE_SENTINEL") != "" || RunChatExecutionChild(os.Stdin, os.Stdout) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "chat-child-test-wrong-attempt", "chat-child-test-gap":
			os.Exit(runChatChildBadPeer(os.Args[1]))
		case "chat-child-test-bare-end", "chat-child-test-wrong-cancel", "chat-child-test-wrong-ticket", "chat-child-test-early-receipt":
			os.Exit(runChatChildCancelPeer(os.Args[1]))
		case "chat-child-test-holder":
			os.Exit(runChatChildTestHolder())
		}
	}
	os.Exit(m.Run())
}

func runChatChildTestHolder() int {
	k, f, err := readChildFrame(os.Stdin)
	if err != nil || k != childHello {
		return 2
	}
	ctx := context.Background()
	pool, err := startChatChildPool(ctx, childTestConfig(f.Hello.BaseURL, 1, 1))
	if err != nil {
		return 2
	}
	defer pool.close()
	slot, ok := pool.reserve()
	if !ok {
		return 2
	}
	_, err = slot.stream(ctx, childTestIdentity(slot, "held"), "frozen", childTestRequest("private"), false, func(string) error { return nil })
	if err != nil {
		return 2
	}
	return 0
}

func TestChatChildAbortedParentClosesActualUpstream(t *testing.T) {
	entered, exited := make(chan struct{}, 2), make(chan struct{}, 2)
	release := make(chan struct{})
	var posts atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			exited <- struct{}{}
		case <-release:
		}
	}))
	defer gateway.Close()
	defer close(release)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	holder := exec.Command(exe, "chat-child-test-holder")
	holder.Env = []string{}
	input, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	waited := false
	defer func() {
		if !waited {
			holder.Process.Kill()
			holder.Wait()
		}
	}()
	writer := childFrameWriter{out: input}
	if err = writer.send(childHello, childFrame{Hello: &childHelloData{BaseURL: gateway.URL}}); err != nil {
		t.Fatal(err)
	}
	childTestWait(t, entered)
	if err = holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = holder.Wait()
	waited = true
	if err == nil {
		t.Fatal("holder unexpectedly exited cleanly")
	}
	childTestWait(t, exited)
	if posts.Load() != 1 {
		t.Fatal("parent loss caused an additional request")
	}
}

func runChatChildBadPeer(mode string) int {
	k, _, err := readChildFrame(os.Stdin)
	w := childFrameWriter{out: os.Stdout}
	if err != nil || k != childHello || w.send(childReady, childFrame{}) != nil {
		return 2
	}
	k, f, err := readChildFrame(os.Stdin)
	if err != nil || k != childStart || w.send(childStarted, childFrame{Tag: f.Tag}) != nil {
		return 2
	}
	seq := uint64(1)
	if mode == "chat-child-test-wrong-attempt" {
		f.Tag.Identity.Epoch++
	} else {
		seq++
	}
	if w.send(childDelta, childFrame{Tag: f.Tag, Seq: seq, Text: "private fixture text"}) != nil {
		return 2
	}
	// Keep the process alive. The parent must reject the bad output itself.
	io.Copy(io.Discard, os.Stdin)
	return 0
}

func childTestConfig(endpoint string, processes, slots int) Config {
	return Config{Mode: "live", BaseURL: endpoint, APIKey: "local-test-token", ChatChildren: ChatChildConfig{Processes: processes, SlotsPerProcess: slots}}
}
func childTestRequest(text string) ChatRequest {
	return ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "first"}, {Role: "assistant", Content: "earlier answer"}, {Role: "user", Content: text}}, MaxOutputTokens: 512, OutputLimitField: "max_completion_tokens"}
}
func childTestIdentity(slot *chatChildSlot, run string) chatChildIdentity {
	return chatChildIdentity{RunID: run, Owner: slot.owner(), ExecutionID: "execution_" + run, Generation: "generation", Epoch: 2}
}
func childTestTake(t *testing.T, pool *chatChildPool) *chatChildSlot {
	t.Helper()
	slot, ok := pool.reserve()
	if !ok {
		t.Fatal("required a ready execution slot")
	}
	return slot
}
func childTestWait[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a child transition")
	}
	var zero T
	return zero
}
func childTestPool(t *testing.T, cfg Config) *chatChildPool {
	t.Helper()
	pool, err := startChatChildPool(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.close)
	return pool
}

func TestChatChildMultiplexFrozenRequestsAndAcknowledgedDeltas(t *testing.T) {
	t.Setenv("STUDIO_TEST_PARENT_PRIVATE_SENTINEL", "must not reach child")
	var posts atomic.Int32
	cancelled := make(chan struct{})
	const slow = "private slow"
	const fast = "private fast"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		var req struct {
			Model    string
			Messages []ChatMessage
			Output   int `json:"max_completion_tokens"`
			Options  struct {
				Usage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if r.Method != "POST" || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test-token" || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) != 3 {
			t.Error("invalid gateway method, credentials or message count")
			return
		}
		last := req.Messages[2].Content
		if !reflect.DeepEqual(req.Messages, childTestRequest(last).Messages) || req.Model != "frozen-model" || req.Output != 512 || req.Options.Usage != (last == fast) {
			t.Error("lost frozen model, budget, history or usage flag")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", last)
		w.(http.Flusher).Flush()
		if last == slow {
			<-r.Context().Done()
			close(cancelled)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer gateway.Close()
	pool := childTestPool(t, childTestConfig(gateway.URL, 1, 2))
	defer pool.close()
	first, second := childTestTake(t, pool), childTestTake(t, pool)
	if pool.available() || !pool.ready() || pool.liveSlots() != 2 || first.owner() != second.owner() {
		t.Fatal("wrong process or slot accounting")
	}
	if got := pool.processes[0].cmd; len(got.Args) != 2 || got.Args[1] != "chat-execution-child" || got.Env == nil || len(got.Env) != 0 {
		t.Fatal("child launch inherited process parameters or environment")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, allowReturn := make(chan struct{}), make(chan struct{})
	slowDone := make(chan error, 1)
	go func() {
		_, err := first.stream(ctx, childTestIdentity(first, "run1"), "frozen-model", childTestRequest(slow), false, func(text string) error {
			if text != slow {
				t.Error("slow lane received another run's text")
			}
			close(entered)
			<-allowReturn
			return nil
		})
		slowDone <- err
	}()
	childTestWait(t, entered)
	var answer strings.Builder
	usage, err := second.stream(t.Context(), childTestIdentity(second, "run2"), "frozen-model", childTestRequest(fast), true, func(text string) error { answer.WriteString(text); return nil })
	if err != nil || answer.String() != fast || usage.Status != "reported" || !usage.Complete || usage.TotalTokens != 5 {
		t.Fatal("other lane did not return its exact output and usage", err)
	}
	if _, err = second.stream(t.Context(), childTestIdentity(second, "run2"), "frozen-model", childTestRequest(fast), true, func(string) error { return nil }); !errors.Is(err, errChatChildInterrupted) {
		t.Fatal("ticket allowed a second dispatch")
	}
	first.release() // Concurrent with real persistence; cannot free it yet.
	cancel()
	childTestWait(t, cancelled)
	if pool.available() {
		t.Fatal("reserved slot released before emit returned")
	}
	close(allowReturn)
	if err = childTestWait(t, slowDone); !errors.Is(err, context.Canceled) {
		t.Fatal("wrong cancellation result", err)
	}
	next := childTestTake(t, pool)
	if next.index != first.index || next.ticket <= first.ticket || next.owner() != first.owner() {
		t.Fatal("cancelled lane did not become safely reusable")
	}
	next.release()
	second.release()
	if posts.Load() != 2 || !pool.ready() {
		t.Fatal("unexpected provider submission or lost healthy process")
	}
}

func TestChatChildExitInterruptsOnlyAffectedOwnerAndNeverResubmits(t *testing.T) {
	entered, left := make(chan struct{}, 3), make(chan struct{}, 3)
	var posts atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		entered <- struct{}{}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		left <- struct{}{}
	}))
	defer gateway.Close()
	pool := childTestPool(t, childTestConfig(gateway.URL, 2, 1))
	defer pool.close()
	a, b := childTestTake(t, pool), childTestTake(t, pool)
	if a.owner() == b.owner() || a.proc.cmd.Process.Pid == b.proc.cmd.Process.Pid {
		t.Fatal("expected separate OS processes")
	}
	run := func(s *chatChildSlot, id string) <-chan error {
		ret := make(chan error, 1)
		go func() {
			_, err := s.stream(t.Context(), childTestIdentity(s, id), "original", childTestRequest(id), false, func(string) error { return nil })
			ret <- err
		}()
		return ret
	}
	doneA, doneB := run(a, "a"), run(b, "b")
	childTestWait(t, entered)
	childTestWait(t, entered)
	if err := a.proc.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(childTestWait(t, doneA), errChatChildInterrupted) {
		t.Fatal("child death did not remain interrupted")
	}
	childTestWait(t, left)
	a.release()
	if pool.ready() || pool.available() || pool.liveSlots() != 1 || posts.Load() != 2 {
		t.Fatal("wrong degraded accounting or duplicate submission")
	}
	pool.close()
	if !errors.Is(childTestWait(t, doneB), errChatChildInterrupted) {
		t.Fatal("shutdown did not reap the remaining execution")
	}
	childTestWait(t, left)
	if pool.liveSlots() != 0 || posts.Load() != 2 {
		t.Fatal("process was resurrected after shutdown")
	}
}

func TestChatChildRejectsCrossAttemptAndNoncontiguousStream(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"chat-child-test-wrong-attempt", "chat-child-test-gap"} {
		t.Run(mode, func(t *testing.T) {
			cfg := Config{Mode: "demo", DemoTokenDelay: time.Millisecond, ChatChildren: ChatChildConfig{Processes: 1, SlotsPerProcess: 1}}
			pool, err := launchChatChildPool(t.Context(), cfg, func() *exec.Cmd { return exec.Command(exe, mode) })
			if err != nil {
				t.Fatal(err)
			}
			defer pool.close()
			slot := childTestTake(t, pool)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = slot.stream(ctx, childTestIdentity(slot, "example"), "original", childTestRequest("private input"), false, func(string) error { t.Fatal("invalid delta reached persistence"); return nil })
			if !errors.Is(err, errChatChildInterrupted) || ctx.Err() != nil || pool.liveSlots() != 0 {
				t.Fatal("parent failed to fence corrupt stream", err)
			}
		})
	}
}

func TestChatChildWholeStreamOutputBudget(t *testing.T) {
	var posts atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, size := range []int{maxRunOutputBytes / 2, maxRunOutputBytes/2 + 1} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", strings.Repeat("x", size))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer gateway.Close()
	pool := childTestPool(t, childTestConfig(gateway.URL, 1, 1))
	defer pool.close()
	slot := childTestTake(t, pool)
	defer slot.release()
	published := 0
	_, err := slot.stream(t.Context(), childTestIdentity(slot, "bounded"), "original", childTestRequest("input"), false, func(s string) error {
		published += len(s)
		return nil
	})
	if !errors.Is(err, errChildProvider) || published != maxRunOutputBytes/2 || posts.Load() != 1 || !pool.ready() {
		t.Fatal("output over the whole-run limit changed the prefix, restarted the provider or disabled the healthy process")
	}
}
