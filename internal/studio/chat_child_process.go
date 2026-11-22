package studio

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"time"
)

const childPipeTimeout = 10 * time.Second

type chatChildProcess struct {
	pool              *chatChildPool
	owner             string
	cmd               *exec.Cmd
	input, output     *os.File // parent endpoints
	writer            childFrameWriter
	lanes             []childProcessLane
	healthy, stopping bool // pool.mu
	stopOnce          sync.Once
	done              chan struct{}
}

func launchChatChildProcess(ctx context.Context, pool *chatChildPool, cmd *exec.Cmd, h childHelloData) (*chatChildProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	toChild, input, err := os.Pipe()
	if err != nil {
		return nil, errChatChildInterrupted
	}
	output, fromChild, err := os.Pipe()
	if err != nil {
		toChild.Close()
		input.Close()
		return nil, errChatChildInterrupted
	}
	defer toChild.Close()
	defer fromChild.Close()
	proc := &chatChildProcess{pool: pool, owner: h.Owner, cmd: cmd, input: input, output: output, writer: childFrameWriter{out: input}, lanes: make([]childProcessLane, h.Slots), done: make(chan struct{})}
	// exec treats nil Env as inherit. An explicit empty slice leaves only OS
	// essentials supplied by the runtime (SystemRoot on Windows).
	cmd.Env = []string{}
	cmd.Dir = os.TempDir()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = toChild, fromChild, nil
	attach, release, err := chatChildGuard(cmd)
	if err != nil {
		input.Close()
		output.Close()
		return nil, errChatChildInterrupted
	}
	if err = cmd.Start(); err != nil {
		release()
		input.Close()
		output.Close()
		return nil, errChatChildInterrupted
	}
	toChild.Close()
	fromChild.Close()
	// The child waits for HELLO. Before attachment it cannot dispatch even
	// when its parent is terminated between Start and attach.
	if err = attach(); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		release()
		input.Close()
		output.Close()
		return nil, errChatChildInterrupted
	}
	stopContext := context.AfterFunc(ctx, proc.stop)
	go func() {
		_ = cmd.Wait()
		stopContext()
		proc.stop()
		release()
		close(proc.done)
	}()
	// Stop a wedged version/ready handshake even if it never reads its pipe.
	timer := time.AfterFunc(childPipeTimeout, proc.stop)
	err = proc.writer.send(childHello, childFrame{Hello: &h})
	if err == nil {
		var k childFrameKind
		k, _, err = readChildFrame(output)
		if k != childReady {
			err = errChildProtocol
		}
	}
	if !timer.Stop() || err != nil {
		proc.stop()
		<-proc.done
		return nil, errChatChildInterrupted
	}
	pool.mu.Lock()
	if proc.stopping {
		pool.mu.Unlock()
		<-proc.done
		return nil, errChatChildInterrupted
	}
	proc.healthy = true
	pool.mu.Unlock()
	go proc.read()
	return proc, nil
}

func (p *chatChildProcess) stop() {
	p.stopOnce.Do(func() {
		p.pool.mu.Lock()
		p.stopping = true
		p.healthy = false
		p.pool.signalLocked()
		p.pool.mu.Unlock()
		// Kill first so a blocked pipe read/write wakes on both supported OSes.
		_ = p.cmd.Process.Kill()
		_ = p.input.Close()
		_ = p.output.Close()
	})
}

func (p *chatChildProcess) send(k childFrameKind, f childFrame) error {
	timer := time.AfterFunc(childPipeTimeout, p.stop)
	err := p.writer.send(k, f)
	if !timer.Stop() || err != nil {
		p.stop()
		return errChatChildInterrupted
	}
	return nil
}

func (p *chatChildProcess) read() {
	defer p.stop()
	for {
		kind, f, err := readChildFrame(p.output)
		if err != nil || f.Tag == nil || f.Tag.Lane >= len(p.lanes) {
			return
		}
		p.pool.mu.Lock()
		slot := p.lanes[f.Tag.Lane].slot
		match := slot != nil && slot.running && *f.Tag == slot.tag && slot.acceptReplyLocked(kind, f)
		p.pool.mu.Unlock()
		if !match {
			return
		}
		if kind == childCancelReceipt {
			continue
		}
		// At most one DELTA can be awaiting its ACK per lane. A peer that
		// exceeds the control+delta+terminal mailbox has broken the protocol.
		select {
		case slot.inbox <- childReply{kind, f}:
		default:
			return
		}
	}
}

func (s *chatChildSlot) acceptReplyLocked(kind childFrameKind, f childFrame) bool {
	switch kind {
	case childStarted:
		if s.wireStarted {
			return false
		}
		s.wireStarted = true
	case childDelta:
		if !s.wireStarted || s.wireEnded || s.ackSeq != s.wireSeq || f.Seq != s.wireSeq+1 {
			return false
		}
		s.wireSeq = f.Seq
	case childEnd:
		if !s.wireStarted || s.wireEnded || f.Seq != s.wireSeq || s.wantUsage != (f.Usage != nil) {
			return false
		}
		if s.ackSeq != s.wireSeq && !(f.Failed && s.cancelID != "") {
			return false
		}
		s.wireEnded = true
	case childCancelReceipt:
		if !s.wireEnded || s.cancelID == "" || f.CancelID != s.cancelID || f.Seq != s.wireSeq {
			return false
		}
		s.cancelID = ""
		close(s.cancelReceipt)
	default:
		return false
	}
	return true
}
