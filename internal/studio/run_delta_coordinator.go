package studio

import (
	"context"
	"slices"
	"sync"
	"time"
)

const (
	runDeltaGroupDelay   = 2 * time.Millisecond
	runDeltaQueueTimeout = 5 * time.Second
	// Execution admission already limits callers to at most 64 local and
	// 4*64 child executions. These limits include items already in storage.
	runDeltaPendingItems = 320
	runDeltaPendingBytes = 8 << 20
)

type queuedRunDelta struct {
	item         runDeltaAppend
	caller, wait context.Context
	at           time.Time
	sent         bool
	done         chan struct{}
	err          error
}

type runDeltaCoordinator struct {
	parent                   context.Context
	save                     func([]runDeltaAppend) ([]error, runDeltaBatchStats)
	groupDelay, queueTimeout time.Duration
	mu                       sync.Mutex
	queue                    []*queuedRunDelta
	active                   map[string]*queuedRunDelta
	bytes                    int
	wake                     chan struct{} // There is one storage worker.
	space                    chan struct{} // Broadcast when capacity is reclaimed.
	stats                    runDeltaCoordinatorStats
}

func newRunDeltaCoordinator(ctx context.Context, save func([]runDeltaAppend) ([]error, runDeltaBatchStats)) *runDeltaCoordinator {
	return &runDeltaCoordinator{parent: ctx, save: save, groupDelay: runDeltaGroupDelay, queueTimeout: runDeltaQueueTimeout,
		active: make(map[string]*queuedRunDelta), wake: make(chan struct{}, 1), space: make(chan struct{})}
}

func (c *runDeltaCoordinator) append(ctx context.Context, item runDeltaAppend) error {
	if item.Lease.RunID == "" || len(item.Delta) == 0 || len(item.Delta) > maxRunOutputBytes {
		return runStreamConflict()
	}
	started := time.Now()
	wait, cancel := context.WithTimeout(ctx, c.queueTimeout)
	defer cancel()
	next := &queuedRunDelta{item: item, caller: ctx, wait: wait, at: started, done: make(chan struct{})}
	for {
		c.mu.Lock()
		if err := c.waitError(ctx, wait); err != nil {
			c.countWithdrawal(err)
			c.mu.Unlock()
			return err
		}
		if c.active[item.Lease.RunID] != nil {
			c.stats.Duplicates++
			c.mu.Unlock()
			return runStreamConflict()
		}
		if len(c.active) < runDeltaPendingItems && len(item.Delta) <= runDeltaPendingBytes-c.bytes {
			c.active[item.Lease.RunID] = next
			c.queue = append(c.queue, next)
			c.bytes += len(item.Delta)
			c.mu.Unlock()
			select {
			case c.wake <- struct{}{}:
			default:
			}
			break
		}
		space := c.space
		c.mu.Unlock()
		select {
		case <-space:
		case <-wait.Done():
		case <-c.parent.Done():
		}
	}
	select {
	case <-next.done:
		return next.err
	case <-wait.Done():
	case <-c.parent.Done():
	}
	// Withdrawal and the handoff to save are serialized. Once sent, even a
	// cancelled caller must obtain the actual result. Its record's streamWrite
	// pin prevents a terminal update from overtaking a confirmed prefix.
	c.mu.Lock()
	if !next.sent && c.active[item.Lease.RunID] == next {
		index := slices.Index(c.queue, next)
		c.queue = slices.Delete(c.queue, index, index+1)
		err := c.waitError(ctx, wait)
		c.countWithdrawal(err)
		c.finishLocked(next, err)
		c.signalSpaceLocked()
	}
	c.mu.Unlock()
	<-next.done
	return next.err
}

func (c *runDeltaCoordinator) waitError(ctx, wait context.Context) error {
	if err := c.parent.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if wait.Err() != nil {
		return errRunDeltaQueueTimeout
	}
	return nil
}

var errRunDeltaQueueTimeout = failure(503, "RUN_DELTA_QUEUE_TIMEOUT", "聊天增量等待保存超时")

func (c *runDeltaCoordinator) countWithdrawal(err error) {
	if err == errRunDeltaQueueTimeout {
		c.stats.QueueTimeouts++
	} else {
		c.stats.Cancelled++
	}
}

func (c *runDeltaCoordinator) finishLocked(q *queuedRunDelta, err error) {
	delete(c.active, q.item.Lease.RunID)
	c.bytes -= len(q.item.Delta)
	q.err = err
	close(q.done)
}
func (c *runDeltaCoordinator) signalSpaceLocked() {
	close(c.space)
	c.space = make(chan struct{})
}

func (c *runDeltaCoordinator) run() {
	for {
		c.mu.Lock()
		if err := c.parent.Err(); err != nil {
			for _, q := range c.queue {
				c.countWithdrawal(err)
				c.finishLocked(q, err)
			}
			c.queue = nil
			c.signalSpaceLocked()
			c.mu.Unlock()
			return
		}
		if len(c.queue) == 0 {
			c.mu.Unlock()
			select {
			case <-c.parent.Done():
			case <-c.wake:
			}
			continue
		}
		n, size := 0, 0
		for _, q := range c.queue {
			if n == maxRunDeltaBatchItems || len(q.item.Delta) > maxRunDeltaBatchBytes-size {
				break
			}
			n++
			size += len(q.item.Delta)
		}
		until := time.Until(c.queue[0].at.Add(c.groupDelay))
		if n == len(c.queue) && n < maxRunDeltaBatchItems && size < maxRunDeltaBatchBytes && until > 0 {
			c.mu.Unlock()
			timer := time.NewTimer(until)
			select {
			case <-timer.C:
			case <-c.wake:
			case <-c.parent.Done():
			}
			timer.Stop()
			continue
		}
		group := slices.Clone(c.queue[:n])
		c.queue = slices.Delete(c.queue, 0, n)
		// A timer or Run cancellation can fire while its goroutine is waiting
		// for this mutex. Inspect its context as part of transferring ownership.
		sent := group[:0]
		entries := make([]runDeltaAppend, 0, n)
		now := time.Now()
		for _, q := range group {
			if err := c.waitError(q.caller, q.wait); err != nil {
				c.countWithdrawal(err)
				c.finishLocked(q, err)
				continue
			}
			q.sent = true
			sent = append(sent, q)
			entries = append(entries, q.item)
			c.stats.observeWait(now.Sub(q.at))
		}
		if len(sent) != len(group) {
			c.signalSpaceLocked()
		}
		c.mu.Unlock()
		if len(sent) == 0 {
			continue
		}
		errs, stats := c.save(entries)
		c.mu.Lock()
		c.stats.CommittedBatches += stats.CommittedBatches
		c.stats.AppendedEvents += stats.AppendedEvents
		c.stats.SoloAttempts += stats.SoloAttempts
		for i, q := range sent {
			if errs[i] == nil {
				c.stats.ConfirmedEvents++
			} else {
				c.stats.StoreErrors++
			}
			c.finishLocked(q, errs[i])
		}
		c.signalSpaceLocked()
		c.mu.Unlock()
	}
}
