package studio

import (
	"context"
	"os"
	"os/exec"
	"sync"
)

// All lane allocation is local to the one control writer. A reservation stays
// occupied until the caller releases it after model I/O, moderation and save.
type chatChildPool struct {
	mu        sync.Mutex
	processes []*chatChildProcess
	wake      chan struct{}
	next      int
	closed    bool
}
type chatChildSlot struct {
	proc                          *chatChildProcess
	index                         int
	ticket                        uint64
	running, used, releasePending bool
	tag                           childTag
	inbox                         chan childReply
	// The pipe reader validates order even while the stream goroutine is
	// inside emit. None of these fields acknowledge persistence.
	wireStarted, wireEnded, wantUsage bool
	wireSeq, ackSeq                   uint64
	cancelID                          string
	cancelReceipt                     chan struct{}
}
type childProcessLane struct {
	ticket uint64
	slot   *chatChildSlot
}
type childReply struct {
	kind  childFrameKind
	frame childFrame
}

func startChatChildPool(ctx context.Context, cfg Config) (*chatChildPool, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, errChatChildInterrupted
	}
	return launchChatChildPool(ctx, cfg, func() *exec.Cmd { return exec.Command(exe, "chat-execution-child") })
}

// The unexported factory is also used by in-package OS-process tests. The
// application always launches its own executable with the fixed argument.
func launchChatChildPool(ctx context.Context, cfg Config, command func() *exec.Cmd) (*chatChildPool, error) {
	n, k := cfg.ChatChildren.Processes, cfg.ChatChildren.SlotsPerProcess
	if n < 1 || n > 4 || k < 1 || k > 64 {
		return nil, errChildProtocol
	}
	if cfg.Mode == "live" && (len(cfg.BaseURL) > 4096 || len(cfg.APIKey) > 4096) {
		return nil, errChildProtocol
	}
	pool := &chatChildPool{wake: make(chan struct{})}
	for range n {
		h := childHelloData{Protocol: childProtocolVersion, Owner: newID("chat_child"), Mode: cfg.Mode, Slots: k}
		if h.Mode == "live" {
			h.BaseURL, h.APIKey = cfg.BaseURL, cfg.APIKey
		} else {
			h.DemoDelay = cfg.DemoTokenDelay
		}
		p, err := launchChatChildProcess(ctx, pool, command(), h)
		if err != nil {
			pool.close()
			return nil, err
		}
		pool.processes = append(pool.processes, p)
	}
	if err := ctx.Err(); err != nil {
		pool.close()
		return nil, err
	}
	return pool, nil
}

func (p *chatChildPool) signalLocked() { close(p.wake); p.wake = make(chan struct{}) }

func (p *chatChildPool) changes() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.wake
}
func (p *chatChildPool) ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.processes) == 0 {
		return false
	}
	for _, proc := range p.processes {
		if !proc.healthy {
			return false
		}
	}
	return true
}
func (p *chatChildPool) liveSlots() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0
	}
	total := 0
	for _, proc := range p.processes {
		if proc.healthy {
			total += len(proc.lanes)
		}
	}
	return total
}
func (p *chatChildPool) freeLocked() (*chatChildProcess, int, int) {
	if !p.closed {
		for i := range p.processes {
			n := (p.next + i) % len(p.processes)
			proc := p.processes[n]
			if proc.healthy {
				for j := range proc.lanes {
					if proc.lanes[j].slot == nil {
						return proc, j, n
					}
				}
			}
		}
	}
	return nil, 0, 0
}
func (p *chatChildPool) available() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	proc, _, _ := p.freeLocked()
	return proc != nil
}
func (p *chatChildPool) reserve() (*chatChildSlot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	proc, j, n := p.freeLocked()
	if proc == nil {
		return nil, false
	}
	p.next = (n + 1) % len(p.processes)
	lane := &proc.lanes[j]
	lane.ticket++
	slot := &chatChildSlot{proc: proc, index: j, ticket: lane.ticket, inbox: make(chan childReply, 3), cancelReceipt: make(chan struct{})}
	lane.slot = slot
	p.signalLocked()
	return slot, true
}
func (s *chatChildSlot) owner() string { return s.proc.owner }
func (s *chatChildSlot) release() {
	p := s.proc.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	s.releasePending = true
	s.releaseIfDoneLocked()
}
func (s *chatChildSlot) releaseIfDoneLocked() {
	lane := &s.proc.lanes[s.index]
	if s.releasePending && !s.running && lane.slot == s {
		lane.slot = nil
		s.proc.pool.signalLocked()
	}
}

func (p *chatChildPool) close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.signalLocked()
	}
	p.mu.Unlock()
	for _, proc := range p.processes {
		proc.stop()
	}
	for _, proc := range p.processes {
		<-proc.done
	}
}
