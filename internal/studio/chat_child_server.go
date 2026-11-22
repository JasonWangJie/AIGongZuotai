package studio

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

type childChatProvider interface {
	ChatUsageProvider
	StreamMessages(context.Context, string, ChatRequest, func(string) error) error
}

func childProvider(h childHelloData) (childChatProvider, error) {
	if h.Protocol != childProtocolVersion || h.Slots < 1 || h.Slots > 64 || h.Owner == "" || len(h.Owner) > 128 {
		return nil, errChildProtocol
	}
	switch h.Mode {
	case "demo":
		if h.BaseURL != "" || h.APIKey != "" || h.DemoDelay <= 0 {
			return nil, errChildProtocol
		}
		return demoChat{delay: h.DemoDelay}, nil
	case "live":
		if len(h.BaseURL) > 4096 || len(h.APIKey) > 4096 || h.DemoDelay != 0 {
			return nil, errChildProtocol
		}
		return newOpenAIChatGatewayWithConcurrency(h.BaseURL, h.APIKey, h.Slots)
	}
	return nil, errChildProtocol
}

type childExecutionLane struct {
	tag             childTag
	ack             chan uint64
	cancel          context.CancelFunc
	stopped         chan struct{}
	cancelRequested bool // Input reader only. One waiter at most per ticket.
	endWriteDone    chan struct{}
	endWritten      bool
	endSeq          uint64
}

// RunChatExecutionChild serves one anonymous stdin/stdout pair only. It never
// opens a listener, reads environment settings, or constructs a studio App.
func RunChatExecutionChild(input io.Reader, output io.Writer) error {
	kind, frame, err := readChildFrame(input)
	if err != nil || kind != childHello {
		return errChildProtocol
	}
	hello := *frame.Hello
	provider, err := childProvider(hello)
	if err != nil {
		return errChildProtocol
	}
	if live, ok := provider.(*openAIChat); ok {
		defer live.client.CloseIdleConnections()
	}
	writer := &childFrameWriter{out: output}
	if err = writer.send(childReady, childFrame{}); err != nil {
		return errChatChildInterrupted
	}
	ctx, end := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	defer func() { end(); workers.Wait() }()
	lanes := make([]*childExecutionLane, hello.Slots)
	for {
		kind, frame, err = readChildFrame(input)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil || frame.Tag == nil || frame.Tag.Lane >= len(lanes) || frame.Tag.Identity.Owner != hello.Owner {
			return errChildProtocol
		}
		t := *frame.Tag
		lane := lanes[t.Lane]
		if kind == childStart {
			if !validChildStart(frame.Start) {
				return errChildProtocol
			}
			// A slot is a scheduling detail, never a second authorization to
			// dispatch an attempt already recorded on a different lane.
			for _, other := range lanes {
				if other != nil && other.tag.Identity == t.Identity {
					return errChildProtocol
				}
			}
			if lane != nil {
				select {
				case <-lane.stopped:
				default:
					return errChildProtocol
				}
				if t.Ticket <= lane.tag.Ticket {
					return errChildProtocol
				}
			}
			runCtx, cancel := context.WithCancel(ctx)
			lane = &childExecutionLane{tag: t, ack: make(chan uint64, 1), cancel: cancel, stopped: make(chan struct{}), endWriteDone: make(chan struct{})}
			lanes[t.Lane] = lane
			// Acknowledge dispatch before the one possible upstream submission.
			if writer.send(childStarted, childFrame{Tag: &t}) != nil {
				cancel()
				return errChatChildInterrupted
			}
			start := frame.Start
			workers.Go(func() { lane.execute(runCtx, writer, provider, start) })
			continue
		}
		if lane == nil || t != lane.tag {
			return errChildProtocol
		}
		switch kind {
		case childCancel:
			// Terminal output may already be in flight in the opposite pipe.
			if lane.cancelRequested {
				return errChildProtocol
			}
			lane.cancelRequested = true
			lane.cancel()
			cancelID := frame.CancelID
			workers.Go(func() {
				select {
				case <-lane.endWriteDone:
					if lane.endWritten {
						_ = writer.send(childCancelReceipt, childFrame{Tag: &lane.tag, Seq: lane.endSeq, CancelID: cancelID})
					}
				case <-ctx.Done():
				}
			})
		case childAck:
			select {
			case lane.ack <- frame.Seq:
			default:
				return errChildProtocol
			}
		default:
			return errChildProtocol
		}
	}
}

func validChildStart(s *childRequestData) bool {
	if s == nil || strings.TrimSpace(s.Model) == "" || len(s.Model) > 256 || !utf8.ValidString(s.Model) || strings.ContainsAny(s.Model, "\x00\r\n") {
		return false
	}
	_, err := validateChatWireRequest(s.Request)
	return err == nil
}

func (lane *childExecutionLane) execute(ctx context.Context, w *childFrameWriter, p childChatProvider, s *childRequestData) {
	var seq uint64
	bytes := 0
	emit := func(text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if text == "" {
			return nil
		}
		if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 || len(text) > maxRunOutputBytes-bytes {
			return errChildProvider
		}
		seq++
		bytes += len(text)
		if err := w.send(childDelta, childFrame{Tag: &lane.tag, Seq: seq, Text: text}); err != nil {
			return err
		}
		select {
		case n := <-lane.ack:
			if n != seq {
				return errChildProtocol
			}
			return ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	finish := childFrame{Tag: &lane.tag}
	var err error
	if s.Usage {
		usage, e := p.StreamMessagesUsage(ctx, s.Model, s.Request, emit)
		finish.Usage = &usage
		err = e
	} else {
		err = p.StreamMessages(ctx, s.Model, s.Request, emit)
	}
	finish.Failed = err != nil || ctx.Err() != nil
	finish.Seq = seq
	lane.cancel()
	// The provider has returned and closed its response body. Permit the next
	// ticket only after this point; the parent cannot reuse it before END.
	close(lane.stopped)
	lane.endSeq = seq
	lane.endWritten = w.send(childEnd, finish) == nil
	close(lane.endWriteDone)
}
