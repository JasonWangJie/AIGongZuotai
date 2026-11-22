package studio

import (
	"errors"
	"time"
)

type ChatChildConfig struct {
	Processes, SlotsPerProcess int
}

func (c ChatChildConfig) validate(app Config) error {
	if c == (ChatChildConfig{}) {
		return nil
	}
	if c.Processes < 1 || c.Processes > 4 || c.SlotsPerProcess < 1 || c.SlotsPerProcess > 64 {
		return errors.New("managed chat requires 1 to 4 children and 1 to 64 slots per child")
	}
	if app.DatabaseURL == "" || app.AuthMode != "required" || app.Mode != "live" || app.BaseURL == "" || app.APIKey == "" {
		return errors.New("managed chat requires PostgreSQL, authenticated accounts and a configured live chat provider")
	}
	return nil
}

// Only the parent assigns work. The local limit applies to every local
// request, including an unclaimed conversation after loss of all children.
// A running execution never returns to the queue.
func chatRoute(childEligible, localFree bool, liveChildSlots, occupiedChildSlots int, reserve func() bool) chatDispatchRoute {
	if childEligible && liveChildSlots > 0 {
		if reserve() {
			return chatChild
		}
		return chatUnavailable
	}
	if childEligible && occupiedChildSlots > 0 {
		// A dead child's pending executions still own their slots until the
		// model I/O and the original terminal transaction have both returned.
		return chatUnavailable
	}
	if localFree {
		return chatLocal
	}
	return chatUnavailable
}

type chatChildIdentity struct {
	RunID, Owner, ExecutionID, Generation string
	Epoch                                 uint64
}

func (a *App) runManagedChatScheduler() {
	defer a.wg.Done()
	s := a.execution
	for {
		a.mu.Lock()
		if a.ctx.Err() != nil {
			a.mu.Unlock()
			return
		}
		// Subscribe before inspecting the pool. A process can die while the
		// parent is choosing work; reserve performs the definitive check.
		changed := a.chatChildren.changes()
		live := a.chatChildren.liveSlots()
		available := a.chatChildren.available()
		var slot *chatChildSlot
		job, ok := s.pickRunnableLocked(time.Now(), func(job executionJob) chatDispatchRoute {
			return chatRoute(job.childEligible, s.localRunning < s.limits.Workers, live, len(s.running)-s.localRunning, func() bool {
				if !available {
					return false
				}
				slot, available = a.chatChildren.reserve()
				return available
			})
		})
		record := a.runs[job.ID]
		wake := s.wake
		a.mu.Unlock()
		if ok {
			a.wg.Add(1)
			go func() {
				defer a.wg.Done()
				a.executeScheduledRun(job, record, slot)
			}()
			continue
		}
		select {
		case <-a.ctx.Done():
			return
		case <-wake:
		case <-changed:
		}
	}
}
