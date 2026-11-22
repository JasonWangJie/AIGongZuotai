package studio

import (
	"context"
	"time"
)

// stream accepts exactly one frozen request for a reservation, including when
// the result of writing START is unknown. It never selects a replacement lane.
func (s *chatChildSlot) stream(ctx context.Context, id chatChildIdentity, model string, req ChatRequest, wantUsage bool, emit func(string) error) (usageResult ChatUsage, resultErr error) {
	p := s.proc
	start := &childRequestData{Model: model, Request: req, Usage: wantUsage}
	tag := childTag{Lane: s.index, Ticket: s.ticket, Identity: id}
	if !tag.valid() || id.Owner != p.owner || !validChildStart(start) || emit == nil {
		return ChatUsage{}, errChildProtocol
	}
	p.pool.mu.Lock()
	if s.used || s.releasePending || !p.healthy || p.pool.closed || p.lanes[s.index].slot != s {
		p.pool.mu.Unlock()
		return ChatUsage{}, errChatChildInterrupted
	}
	s.used, s.running, s.tag, s.wantUsage = true, true, tag, wantUsage
	p.pool.mu.Unlock()
	defer func() {
		p.pool.mu.Lock()
		s.running = false
		s.releaseIfDoneLocked()
		p.pool.mu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return ChatUsage{}, err
	}
	if err := p.send(childStart, childFrame{Tag: &tag, Start: start}); err != nil {
		<-p.done
		return ChatUsage{}, errChatChildInterrupted
	}
	// Cancellation is independent of potentially blocked persistence in emit.
	// An END plus the matching cancellation receipt proves the child closed
	// this ticket's model I/O. The storage callback still owns its slot.
	streamCtx, cancel := context.WithCancel(ctx)
	cancelled := make(chan struct{})
	stopCancel := context.AfterFunc(streamCtx, func() {
		defer close(cancelled)
		cancelID := newID("cancel")
		p.pool.mu.Lock()
		s.cancelID = cancelID
		p.pool.mu.Unlock()
		if p.send(childCancel, childFrame{Tag: &tag, CancelID: cancelID}) != nil {
			return
		}
		timer := time.NewTimer(childPipeTimeout)
		defer timer.Stop()
		select {
		case <-s.cancelReceipt:
		case <-p.done:
		case <-timer.C:
			p.stop()
		}
	})
	defer func() {
		if !stopCancel() {
			<-cancelled
			select {
			case <-s.cancelReceipt:
			default:
				// An END alone cannot complete a cancellation. A failed send,
				// broken pipe or missing receipt has already fenced the child.
				<-p.done
				usageResult, resultErr = ChatUsage{}, errChatChildInterrupted
			}
		}
		cancel()
	}()
	var emitErr error
	received := 0
	badPeer := func() (ChatUsage, error) {
		p.stop()
		<-p.done
		return ChatUsage{}, errChatChildInterrupted
	}
	for {
		select {
		case <-p.done:
			return ChatUsage{}, errChatChildInterrupted
		case reply := <-s.inbox:
			p.pool.mu.Lock()
			stopping := p.stopping
			p.pool.mu.Unlock()
			if stopping {
				<-p.done
				return ChatUsage{}, errChatChildInterrupted
			}
			f := reply.frame
			switch reply.kind {
			case childDelta:
				if len(f.Text) > maxRunOutputBytes-received {
					return badPeer()
				}
				received += len(f.Text)
				if streamCtx.Err() != nil {
					continue
				}
				if emitErr = emit(f.Text); emitErr != nil {
					cancel()
					continue
				}
				if streamCtx.Err() == nil {
					// A fast peer may already return its next packet before the
					// local Write returns. emit has confirmed this exact delta.
					p.pool.mu.Lock()
					s.ackSeq = f.Seq
					p.pool.mu.Unlock()
					if err := p.send(childAck, childFrame{Tag: &tag, Seq: f.Seq}); err != nil {
						return badPeer()
					}
				}
			case childEnd:
				usage := ChatUsage{}
				if f.Usage != nil {
					usage = *f.Usage
				}
				if emitErr != nil {
					return usage, emitErr
				}
				if err := ctx.Err(); err != nil {
					return usage, err
				}
				if f.Failed {
					return usage, errChildProvider
				}
				return usage, nil
			}
		}
	}
}
