package studio

import (
	"testing"
	"time"
)

func TestManagedChatConfiguration(t *testing.T) {
	good := Config{Mode: "live", AuthMode: "required", DatabaseURL: "fixture", BaseURL: "fixture", APIKey: "fixture"}
	if (ChatChildConfig{}).validate(Config{}) != nil || (ChatChildConfig{2, 40}).validate(good) != nil || (ChatChildConfig{8, 64}).validate(good) != nil || (ChatChildConfig{16, 64}).validate(good) != nil {
		t.Fatal("disabled default or configured live fixture rejected")
	}
	for _, bad := range []ChatChildConfig{{1, 0}, {0, 1}, {17, 1}, {2, 65}, {-1, 40}} {
		if bad.validate(good) == nil {
			t.Fatal("invalid child budget accepted")
		}
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Mode = "demo" }, func(c *Config) { c.AuthMode = "disabled" },
		func(c *Config) { c.DatabaseURL = "" }, func(c *Config) { c.BaseURL = "" }, func(c *Config) { c.APIKey = "" },
	} {
		cfg := good
		change(&cfg)
		if (ChatChildConfig{1, 1}).validate(cfg) == nil {
			t.Fatal("ineligible runtime accepted a managed execution topology")
		}
	}
}

func TestManagedChatEightProcessSlotEnvelope(t *testing.T) {
	pool := childTestPool(t, Config{Mode: "demo", DemoTokenDelay: time.Millisecond, ChatChildren: ChatChildConfig{Processes: 8, SlotsPerProcess: 64}})
	if !pool.ready() || pool.liveSlots() != 512 {
		t.Fatal("eight actual child handshakes did not advertise 512 slots")
	}
	byPID := make(map[int]map[int]bool)
	var slots []*chatChildSlot
	for range 512 {
		s := childTestTake(t, pool)
		pid := s.proc.cmd.Process.Pid
		if byPID[pid] == nil {
			byPID[pid] = make(map[int]bool)
		}
		if byPID[pid][s.index] {
			t.Fatal("same process lane reserved twice")
		}
		byPID[pid][s.index] = true
		slots = append(slots, s)
	}
	if len(byPID) != 8 {
		t.Fatal("reservations did not span eight live OS processes")
	}
	for _, lanes := range byPID {
		if len(lanes) != 64 {
			t.Fatal("expected exactly 64 occupied slots per child")
		}
	}
	if _, ok := pool.reserve(); ok {
		t.Fatal("reserved a 513th slot")
	}
	first := slots[0]
	first.release()
	reused := childTestTake(t, pool)
	if reused.proc != first.proc || reused.index != first.index || reused.ticket <= first.ticket {
		t.Fatal("single released lane was not reused with a new ticket")
	}
	first.release()
	if pool.available() {
		t.Fatal("an old ticket released its replacement")
	}
	first.proc.stop()
	childTestWait(t, first.proc.done)
	if pool.liveSlots() != 448 || pool.ready() || pool.available() {
		t.Fatal("loss of one process did not remove exactly its 64 slots")
	}
	reused.release()
	for _, s := range slots {
		s.release()
	}
	var remaining []*chatChildSlot
	for range 448 {
		s := childTestTake(t, pool)
		if s.proc == first.proc {
			t.Fatal("dead process received another reservation")
		}
		remaining = append(remaining, s)
	}
	if pool.available() {
		t.Fatal("surviving processes exceeded their slot budgets")
	}
	for _, s := range remaining {
		s.release()
	}
	pool.close()
	if pool.liveSlots() != 0 || pool.available() {
		t.Fatal("closed pool retained execution capacity")
	}
}

func TestManagedChatSlotsAndSharedFairness(t *testing.T) {
	s := newExecutionScheduler(ChatSchedulingConfig{Workers: 1, WorkspaceConcurrency: 2, ModelConcurrency: 2, QueueSize: 20, WorkspaceQueueSize: 8})
	now := time.Unix(100, 0)
	enqueue := func(id, ws, model string, child bool) {
		s.enqueueLocked(executionJob{ID: id, WorkspaceID: ws, ModelKey: model, EnqueuedAt: now, childEligible: child})
	}
	live, free := 2, 2
	choose := func(j executionJob) chatDispatchRoute {
		return chatRoute(j.childEligible, s.localRunning < s.limits.Workers, live, len(s.running)-s.localRunning, func() bool {
			if free == 0 {
				return false
			}
			free--
			return true
		})
	}
	pick := func(id string, child bool) {
		t.Helper()
		job, ok := s.pickRunnableLocked(now, choose)
		if !ok || job.ID != id || job.dispatchedToChild != child {
			t.Fatalf("unexpected dispatch %s/%t, wanted %s/%t", job.ID, job.dispatchedToChild, id, child)
		}
	}
	enqueue("plain1", "workspace1", "shared-upstream", false)
	pick("plain1", false)
	enqueue("blocked-plain", "workspace1", "upstream2", false)
	enqueue("frozen1", "workspace1", "shared-upstream", true)
	pick("frozen1", true) // Local head is blocked; one workspace/model slot remains.
	enqueue("blocked-workspace", "workspace1", "other-upstream", true)
	enqueue("blocked-model", "workspace2", "shared-upstream", true)
	enqueue("frozen2", "workspace2", "other-upstream", true)
	pick("frozen2", true)
	if free != 0 || s.localRunning != 1 || s.modelRunning["shared-upstream"] != 2 || s.workspaceRunning["workspace1"] != 2 {
		t.Fatal("central workspace, frozen upstream or local budget diverged")
	}
	if _, ok := s.pickRunnableLocked(now, choose); ok {
		t.Fatal("ready budget exceeded")
	}
	live = 0
	enqueue("fresh-fallback", "workspace3", "upstream3", true)
	s.releaseLocked("plain1")
	pick("blocked-plain", false)
	s.releaseLocked("blocked-plain")
	if _, ok := s.pickRunnableLocked(now, choose); ok {
		t.Fatal("fallback began before lost children finished returning")
	}
	s.releaseLocked("frozen1")
	s.releaseLocked("frozen2")
	for len(s.queued) != 0 {
		job, ok := s.pickRunnableLocked(now, choose)
		if !ok || !job.childEligible || job.dispatchedToChild || s.localRunning != 1 {
			t.Fatal("unclaimed fallback did not use the sole local slot")
		}
		if _, ok := s.pickRunnableLocked(now, choose); ok {
			t.Fatal("fallback exceeded the in-process budget")
		}
		s.releaseLocked(job.ID)
	}
	if s.localRunning != 0 || s.childDispatches != 2 || s.localFallbacks != 3 {
		t.Fatal("completed executions were re-enqueued or counted more than once")
	}
}

func TestManagedChatPartialFailureDoesNotReroute(t *testing.T) {
	// One child survives and is fully occupied. Available local capacity is
	// for direct runs; eligible jobs remain queued for the existing pool.
	if route := chatRoute(true, true, 40, 40, func() bool { return false }); route != chatUnavailable {
		t.Fatal("busy surviving child caused fallback")
	}
	if route := chatRoute(false, false, 40, 0, func() bool { t.Fatal("direct job requested a child slot"); return true }); route != chatUnavailable {
		t.Fatal("direct run borrowed a child slot")
	}
}
