package studio

import (
	"testing"
	"time"
)

func TestManagedChatConfiguration(t *testing.T) {
	good := Config{Mode: "live", AuthMode: "required", DatabaseURL: "fixture", BaseURL: "fixture", APIKey: "fixture"}
	if (ChatChildConfig{}).validate(Config{}) != nil || (ChatChildConfig{2, 40}).validate(good) != nil {
		t.Fatal("disabled default or configured live fixture rejected")
	}
	for _, bad := range []ChatChildConfig{{1, 0}, {0, 1}, {5, 1}, {2, 65}, {-1, 40}} {
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
