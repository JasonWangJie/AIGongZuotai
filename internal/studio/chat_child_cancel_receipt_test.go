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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the same synchronous emit boundary that waits for a final database
// result. The model I/O uses real HTTP, a single OS child and two busy lanes.
func TestChatChildCancelReceiptWhileParentSavePending(t *testing.T) {
	t.Parallel()
	storageEntered := make(chan struct{})
	storageDone := make(chan struct{})
	modelClosed := make(chan struct{})
	finishPeer := make(chan struct{})
	releaseStorage := sync.OnceFunc(func() { close(storageDone) })
	releasePeer := sync.OnceFunc(func() { close(finishPeer) })

	var requests atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct{ Messages []ChatMessage }
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 3 {
			t.Error("unexpected model request")
			return
		}
		lane := request.Messages[2].Content
		w.Header().Set("Content-Type", "text/event-stream")
		delta := func(value string) {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", value)
			w.(http.Flusher).Flush()
		}
		delta(lane)
		switch lane {
		case "A":
			<-r.Context().Done()
			close(modelClosed)
		case "B":
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					delta("tick")
				case <-finishPeer:
					delta("after")
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
			}
		default:
			t.Error("unknown model request")
		}
	}))
	defer gateway.Close()
	pool := childTestPool(t, childTestConfig(gateway.URL, 1, 2))
	defer pool.close()
	defer releaseStorage()
	defer releasePeer()
	a, b := childTestTake(t, pool), childTestTake(t, pool)
	if a.proc != b.proc {
		t.Fatal("expected two tickets in the same real child")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resultA, resultB := make(chan error, 1), make(chan error, 1)
	peerDeltas := make(chan string, 3)
	var peerUpdates atomic.Int32
	go func() {
		_, err := a.stream(ctx, childTestIdentity(a, "save"), "frozen-model", childTestRequest("A"), false, func(delta string) error {
			if delta != "A" {
				t.Error("incorrect first ticket delta")
			}
			close(storageEntered)
			<-storageDone
			return nil
		})
		resultA <- err
	}()
	go func() {
		_, err := b.stream(t.Context(), childTestIdentity(b, "peer"), "frozen-model", childTestRequest("B"), false, func(delta string) error {
			if delta == "tick" {
				peerUpdates.Add(1)
			} else {
				peerDeltas <- delta
			}
			return nil
		})
		resultB <- err
	}()
	childTestWait(t, storageEntered)
	if childTestWait(t, peerDeltas) != "B" {
		t.Fatal("second ticket did not start streaming")
	}
	a.release()
	cancel()
	childTestWait(t, modelClosed)
	childTestWait(t, a.cancelReceipt)
	// Exceed the production watchdog while the first callback cannot return.
	select {
	case <-a.proc.done:
		t.Fatal("a completed model cancellation killed the peer")
	case err := <-resultA:
		t.Fatal("first stream abandoned its unfinished persistence callback", err)
	case <-time.After(childPipeTimeout + 150*time.Millisecond):
	}
	pool.mu.Lock()
	waiting := a.running && a.releasePending && a.wireEnded && a.wireSeq == 1 && a.ackSeq == 0
	pool.mu.Unlock()
	if !waiting || !pool.ready() || pool.available() || pool.liveSlots() != 2 {
		t.Fatal("completion receipt released a ticket, acknowledged a delta or reduced healthy capacity")
	}
	releasePeer()
	if childTestWait(t, peerDeltas) != "after" || childTestWait(t, resultB) != nil || peerUpdates.Load() < 2 {
		t.Fatal("healthy ticket lost its original stream")
	}
	if pool.available() || requests.Load() != 2 {
		t.Fatal("outstanding persistence freed a lane or resubmitted a request")
	}
	releaseStorage()
	if !errors.Is(childTestWait(t, resultA), context.Canceled) {
		t.Fatal("first stream lost its cancellation")
	}
	next := childTestTake(t, pool)
	if next.index != a.index || next.ticket <= a.ticket {
		t.Fatal("wrong ticket became available after persistence returned")
	}
	next.release()
	b.release()
}

func runChatChildCancelPeer(mode string) int {
	kind, _, err := readChildFrame(os.Stdin)
	writer := childFrameWriter{out: os.Stdout}
	if err != nil || kind != childHello || writer.send(childReady, childFrame{}) != nil {
		return 2
	}
	kind, start, err := readChildFrame(os.Stdin)
	if err != nil || kind != childStart || writer.send(childStarted, childFrame{Tag: start.Tag}) != nil {
		return 2
	}
	if writer.send(childDelta, childFrame{Tag: start.Tag, Seq: 1, Text: "pending"}) != nil {
		return 2
	}
	kind, cancellation, err := readChildFrame(os.Stdin)
	if err != nil || kind != childCancel {
		return 2
	}
	if mode != "chat-child-test-early-receipt" {
		if writer.send(childEnd, childFrame{Tag: start.Tag, Seq: 1, Failed: true}) != nil {
			return 2
		}
	}
	if mode == "chat-child-test-wrong-cancel" {
		cancellation.CancelID = newID("cancel")
	}
	if mode == "chat-child-test-wrong-ticket" {
		start.Tag.Ticket++
	}
	if mode != "chat-child-test-bare-end" {
		if writer.send(childCancelReceipt, childFrame{Tag: start.Tag, Seq: 1, CancelID: cancellation.CancelID}) != nil {
			return 2
		}
	}
	// Keep the pipe open so the parent's watchdog or protocol validation has
	// to actually terminate this process.
	io.Copy(io.Discard, os.Stdin)
	return 0
}

func TestChatChildCancelFencesMissingOrInvalidReceipt(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"chat-child-test-bare-end", "chat-child-test-wrong-cancel", "chat-child-test-wrong-ticket", "chat-child-test-early-receipt"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			cfg := Config{Mode: "demo", DemoTokenDelay: time.Millisecond, ChatChildren: ChatChildConfig{Processes: 1, SlotsPerProcess: 1}}
			pool, err := launchChatChildPool(t.Context(), cfg, func() *exec.Cmd { return exec.Command(exe, mode) })
			if err != nil {
				t.Fatal(err)
			}
			defer pool.close()
			entered, finish := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(finish) })
			defer release()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			slot := childTestTake(t, pool)
			done := make(chan error, 1)
			go func() {
				_, err := slot.stream(ctx, childTestIdentity(slot, "held"), "frozen", childTestRequest("A"), false, func(string) error {
					close(entered)
					<-finish
					return nil
				})
				done <- err
			}()
			childTestWait(t, entered)
			start := time.Now()
			cancel()
			deadline := 5 * time.Second
			if mode == "chat-child-test-bare-end" {
				deadline = childPipeTimeout + 4*time.Second
			} else {
				release()
			}
			select {
			case <-slot.proc.done:
			case <-time.After(deadline):
				t.Fatal("a stalled or invalid child bypassed fencing")
			}
			if mode == "chat-child-test-bare-end" && time.Since(start) < childPipeTimeout {
				t.Fatal("valid terminal packet was rejected before its outstanding receipt timed out")
			}
			select {
			case <-slot.cancelReceipt:
				t.Fatal("unproven cancellation was accepted")
			default:
			}
			pool.mu.Lock()
			pending := slot.running
			acknowledged := slot.ackSeq
			pool.mu.Unlock()
			if acknowledged != 0 || (mode == "chat-child-test-bare-end" && !pending) {
				t.Fatal("failed process acknowledged an unfinished save")
			}
			release()
			if err := childTestWait(t, done); !errors.Is(err, errChatChildInterrupted) {
				t.Fatal("wrong interrupted outcome", err)
			}
		})
	}
}
