package studio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChatChildFrameBoundsAndPrivateRequestShape(t *testing.T) {
	for _, kind := range []childFrameKind{childHello, childReady, childStart, childDelta, childAck, childEnd, 255} {
		var header [5]byte
		header[0] = byte(kind)
		binary.BigEndian.PutUint32(header[1:], uint32(childFrameLimit(kind)+1))
		if _, _, err := readChildFrame(bytes.NewReader(header[:])); !errors.Is(err, errChildProtocol) {
			t.Fatalf("frame %d read beyond invalid header", kind)
		}
	}
	if _, _, err := readChildFrame(bytes.NewReader([]byte{byte(childReady), 0, 0, 0, 2})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("truncated payload treated as a clean shutdown")
	}
	for _, body := range []string{`{"database_url":"private"}`, `{}{}`, `{"Text":"private"}`, `null`} {
		var wire bytes.Buffer
		wire.WriteByte(byte(childReady))
		binary.Write(&wire, binary.BigEndian, uint32(len(body)))
		wire.WriteString(body)
		if _, _, err := readChildFrame(&wire); err != errChildProtocol {
			t.Fatal("unexpected field or trailing document accepted")
		}
	}
	tag := childTag{Lane: 0, Ticket: 1, Identity: chatChildIdentity{RunID: "run", Owner: "owner", ExecutionID: "exec", Generation: "gen", Epoch: 1}}
	maxRequest := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: strings.Repeat("<", MaxChatContentBytes)}}, MaxOutputTokens: 512, OutputLimitField: "max_tokens"}
	start := childFrame{Tag: &tag, Start: &childRequestData{Model: "frozen", Request: maxRequest}}
	var wire bytes.Buffer
	writer := childFrameWriter{out: &wire}
	if !validChildStart(start.Start) || writer.send(childStart, start) != nil {
		t.Fatal("maximum escaped valid frozen request was rejected")
	}
	if _, f, err := readChildFrame(&wire); err != nil || f.Start.Request.Messages[0].Content != maxRequest.Messages[0].Content {
		t.Fatal("maximum valid request did not round trip")
	}
	start.Start.Request.Messages[0].Content += ">"
	if validChildStart(start.Start) {
		t.Fatal("accepted over-budget context")
	}
	finish := childFrame{Tag: &tag, Usage: &ChatUsage{Status: "reported", Source: "gateway", Complete: true, PromptTokens: 8, TotalTokens: 2}}
	if writer.send(childEnd, finish) != errChildProtocol {
		t.Fatal("accepted contradictory completed usage")
	}
}

// The program receiving the pipe is the real child implementation. A duplicate
// ticket or a cross-lane replay after POST must reject without resubmission;
// EOF must abort the in-flight round trip even without a Windows job object.
func TestChatChildDuplicateDispatchAndParentPipeEOF(t *testing.T) {
	for _, op := range []string{"repeated-ticket", "same-attempt-second-lane", "eof"} {
		t.Run(op, func(t *testing.T) {
			entered, exited := make(chan struct{}, 3), make(chan struct{}, 3)
			var posts atomic.Int32
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				entered <- struct{}{}
				<-r.Context().Done()
				exited <- struct{}{}
			}))
			defer gateway.Close()
			input, toChild := io.Pipe()
			fromChild, output := io.Pipe()
			defer input.Close()
			defer toChild.Close()
			defer fromChild.Close()
			defer output.Close()
			done := make(chan error, 1)
			go func() { done <- RunChatExecutionChild(input, output); output.Close() }()
			w := childFrameWriter{out: toChild}
			h := childHelloData{Protocol: childProtocolVersion, Owner: "owner", Mode: "live", Slots: 2, BaseURL: gateway.URL, APIKey: "fixture"}
			if err := w.send(childHello, childFrame{Hello: &h}); err != nil {
				t.Fatal(err)
			}
			if k, _, err := readChildFrame(fromChild); k != childReady || err != nil {
				t.Fatal("missing child readiness")
			}
			// A separate drain permits END to be sent after the parent's input
			// disappears. On actual OS parent death both pipe directions close.
			go io.Copy(io.Discard, fromChild)
			tag := childTag{Lane: 0, Ticket: 1, Identity: chatChildIdentity{RunID: "one", Owner: "owner", ExecutionID: "exec", Generation: "gen", Epoch: 1}}
			start := childFrame{Tag: &tag, Start: &childRequestData{Model: "frozen", Request: childTestRequest("private direct fixture")}}
			if err := w.send(childStart, start); err != nil {
				t.Fatal(err)
			}
			childTestWait(t, entered)
			switch op {
			case "eof":
				toChild.Close()
			case "same-attempt-second-lane":
				tag.Lane = 1
				fallthrough
			default:
				if err := w.send(childStart, start); err != nil {
					t.Fatal(err)
				}
			}
			if err := childTestWait(t, done); op == "eof" && err != nil || op != "eof" && !errors.Is(err, errChildProtocol) {
				t.Fatal("wrong child termination reason", err)
			}
			childTestWait(t, exited)
			if posts.Load() != 1 {
				t.Fatal("ambiguous dispatch was retried")
			}
		})
	}
}
