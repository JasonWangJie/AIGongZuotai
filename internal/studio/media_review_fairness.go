package studio

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const mediaPermitWaitLimit = 10 * time.Second

type MediaPermitSnapshot struct{ InputWaiting, OutputWaiting, InFlight int }
type mediaPermitWaiter struct {
	workspace string
	output    bool
	ctx       context.Context
	ready     chan *mediaModerationPermit
}
type mediaPermitArbiter struct {
	mu           sync.Mutex
	inputs       map[string][]*mediaPermitWaiter
	workspaces   []string
	outputs      []*mediaPermitWaiter
	inputWaiting int
	nextOutput   bool
}

func mediaPermitBusy() error {
	return failure(429, "MEDIA_MODERATION_BUSY", "媒体审核等待容量已满或等待超时，未开始检查")
}
func (e *mediaModerationEngine) newPermitLocked() *mediaModerationPermit {
	return &mediaModerationPermit{state: &mediaModerationPermitState{engine: e, slots: e.slots}}
}
func (e *mediaModerationEngine) permitSnapshot() MediaPermitSnapshot {
	if e == nil {
		return MediaPermitSnapshot{}
	}
	e.permits.mu.Lock()
	defer e.permits.mu.Unlock()
	return MediaPermitSnapshot{InputWaiting: e.permits.inputWaiting, OutputWaiting: len(e.permits.outputs), InFlight: len(e.slots)}
}
func (e *mediaModerationEngine) acquireInputPermit(ctx context.Context, workspaceID string) (*mediaModerationPermit, error) {
	if e == nil || e.slots == nil {
		return nil, failure(503, "MEDIA_MODERATION_UNAVAILABLE", "媒体审核未启用，未开始检查")
	}
	return e.acquireScopedPermit(ctx, workspaceID, false)
}
func (e *mediaModerationEngine) acquireReviewPermit(ctx context.Context, workspaceID string) (*mediaModerationPermit, error) {
	if e == nil || e.slots == nil {
		return nil, nil
	}
	return e.acquireScopedPermit(ctx, workspaceID, true)
}
func (e *mediaModerationEngine) acquireScopedPermit(ctx context.Context, workspace string, output bool) (*mediaModerationPermit, error) {
	if workspace == "" || len(workspace) > 128 || !utf8.ValidString(workspace) || strings.ContainsAny(workspace, "\x00\r\n") {
		return nil, failure(400, "INVALID_WORKSPACE", "审核作用域无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, mediaPermitWaitLimit)
	defer cancel()
	w := &mediaPermitWaiter{workspace: workspace, output: output, ctx: waitCtx, ready: make(chan *mediaModerationPermit, 1)}
	a := &e.permits
	a.mu.Lock()
	e.dispatchPermitsLocked()
	if output && len(a.outputs) >= 2 || !output && (a.inputWaiting >= 16 || len(a.inputs[workspace]) >= 2) {
		a.mu.Unlock()
		return nil, mediaPermitBusy()
	}
	if output {
		a.outputs = append(a.outputs, w)
	} else {
		if a.inputs == nil {
			a.inputs = make(map[string][]*mediaPermitWaiter)
		}
		if len(a.inputs[workspace]) == 0 {
			a.workspaces = append(a.workspaces, workspace)
		}
		a.inputs[workspace] = append(a.inputs[workspace], w)
		a.inputWaiting++
	}
	e.dispatchPermitsLocked()
	a.mu.Unlock()
	select {
	case p := <-w.ready:
		if waitCtx.Err() != nil {
			p.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, mediaPermitBusy()
		}
		return p, nil
	case <-waitCtx.Done():
		a.mu.Lock()
		e.removeWaiterLocked(w)
		e.dispatchPermitsLocked()
		a.mu.Unlock()
		// Grant and cancellation can race. The arbiter publishes while holding
		// its lock, so after removal any already granted permit is visible here.
		select {
		case p := <-w.ready:
			p.Close()
		default:
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, mediaPermitBusy()
	}
}
func (e *mediaModerationEngine) removeWaiterLocked(w *mediaPermitWaiter) {
	a := &e.permits
	if w.output {
		for i, item := range a.outputs {
			if item == w {
				a.outputs = append(a.outputs[:i], a.outputs[i+1:]...)
				return
			}
		}
		return
	}
	queue := a.inputs[w.workspace]
	for i, item := range queue {
		if item != w {
			continue
		}
		queue = append(queue[:i], queue[i+1:]...)
		a.inputWaiting--
		if len(queue) > 0 {
			a.inputs[w.workspace] = queue
			return
		}
		delete(a.inputs, w.workspace)
		for j, id := range a.workspaces {
			if id == w.workspace {
				a.workspaces = append(a.workspaces[:j], a.workspaces[j+1:]...)
				break
			}
		}
		return
	}
}
func (e *mediaModerationEngine) dispatchPermitsLocked() {
	a := &e.permits
	for len(e.slots) < cap(e.slots) && (a.inputWaiting > 0 || len(a.outputs) > 0) {
		output := len(a.outputs) > 0 && (a.inputWaiting == 0 || a.nextOutput)
		var w *mediaPermitWaiter
		if output {
			w = a.outputs[0]
			a.outputs = a.outputs[1:]
		} else {
			ws := a.workspaces[0]
			a.workspaces = a.workspaces[1:]
			q := a.inputs[ws]
			w = q[0]
			q = q[1:]
			a.inputWaiting--
			if len(q) == 0 {
				delete(a.inputs, ws)
			} else {
				a.inputs[ws] = q
				a.workspaces = append(a.workspaces, ws)
			}
		}
		if w.ctx.Err() != nil {
			continue
		}
		e.slots <- struct{}{}
		a.nextOutput = !output
		w.ready <- e.newPermitLocked()
	}
}
func (e *mediaModerationEngine) releasePermit(slots chan struct{}) {
	e.permits.mu.Lock()
	defer e.permits.mu.Unlock()
	<-slots
	e.dispatchPermitsLocked()
}
