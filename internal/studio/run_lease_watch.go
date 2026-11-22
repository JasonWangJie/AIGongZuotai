package studio

import (
	"context"
	"errors"
	"sync"
	"time"
)

// One monitor per admitted worker. Its lifetime includes provider I/O,
// moderation and retries of the original terminal transaction.
type runLeaseWatch struct {
	lease            runLease
	cancel           context.CancelFunc
	lost, stop, done chan struct{}
	markLost         sync.Once
}

func (w *runLeaseWatch) reject(err error) {
	var op *operationError
	if errors.As(err, &op) && op.code == "RUN_LEASE_LOST" {
		w.markLost.Do(func() {
			w.cancel()
			close(w.lost)
		})
	}
}

func (a *App) watchRunLease(r *runRecord, lease runLease, cancel context.CancelFunc) *runLeaseWatch {
	w := &runLeaseWatch{lease: lease, cancel: cancel, lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		tick := time.NewTicker(a.config.RunLeaseDuration / 3)
		defer tick.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-w.lost:
				a.interruptLostRun(r, w)
				return
			case <-w.stop:
				// A fenced terminal save may have signalled loss immediately
				// before the provider exited and the worker stopped its watch.
				select {
				case <-w.lost:
					a.interruptLostRun(r, w)
				default:
				}
				return
			case <-tick.C:
				_, err := a.store.renewRunLease(w.lease, a.config.RunLeaseDuration)
				if err != nil {
					w.reject(err)
					// A database failure stops the control writer. Recovery
					// will read its actual committed state on next startup.
					if a.store.failed.Load() {
						w.cancel()
						return
					}
				}
			}
		}
	}()
	return w
}

func (a *App) stopRunLeaseWatch(r *runRecord, w *runLeaseWatch) {
	close(w.stop)
	<-w.done
	a.mu.Lock()
	if r.leaseWatch == w {
		r.leaseWatch = nil
		r.Cancel = nil
	}
	a.mu.Unlock()
}

// A confirmed lease loss cancels the model context before waiting for this
// control transaction. Only the database may supply the interrupted prefix.
func (a *App) interruptLostRun(r *runRecord, w *runLeaseWatch) {
	for a.ctx.Err() == nil {
		a.mu.Lock()
		a.waitRunStreamIdleLocked(r)
		if r.leaseWatch != w || terminal(r.Run.Status) || a.ctx.Err() != nil {
			a.mu.Unlock()
			return
		}
		r.streamWrite = make(chan struct{})
		a.mu.Unlock()
		saved, err := a.store.interruptExpiredRun(w.lease)
		a.mu.Lock()
		close(r.streamWrite)
		r.streamWrite = nil
		if err == nil && saved != nil && r.leaseWatch == w && !terminal(r.Run.Status) {
			r.Run = *saved
			r.CheckpointSeq = saved.Seq
			r.Output.Reset()
			r.Output.WriteString(saved.Output)
			a.publishFinishedRunLocked(r)
		}
		a.mu.Unlock()
		if !isEventBacklog(err) || !waitTaskPoll(a.ctx, 250*time.Millisecond) {
			return
		}
	}
}
