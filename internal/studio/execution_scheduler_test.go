package studio

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChatSchedulingConfigBoundsAndLegacyDefaults(t *testing.T) {
	var limits ChatSchedulingConfig
	if err := limits.normalize(4, 32); err != nil || limits != (ChatSchedulingConfig{Workers: 4, QueueSize: 32, WorkspaceConcurrency: 2, ModelConcurrency: 4, WorkspaceQueueSize: 8}) {
		t.Fatalf("defaults %+v %v", limits, err)
	}
	legacy := ChatSchedulingConfig{}
	if err := legacy.normalize(1, 1); err != nil || legacy.Workers != 1 || legacy.WorkspaceConcurrency != 1 || legacy.WorkspaceQueueSize != 1 {
		t.Fatal("small legacy fixture defaults changed", legacy, err)
	}
	for _, value := range []ChatSchedulingConfig{{Workers: -1}, {Workers: 65}, {QueueSize: -1}, {QueueSize: 4097}, {WorkspaceConcurrency: -1}, {WorkspaceConcurrency: 65}, {ModelConcurrency: -1}, {ModelConcurrency: 65}, {WorkspaceQueueSize: -1}, {WorkspaceQueueSize: 4097}} {
		if value.normalize(4, 32) == nil {
			t.Fatalf("invalid scheduling config accepted %+v", value)
		}
	}
}
func schedulerFixture(t *testing.T, limits ChatSchedulingConfig) *executionScheduler {
	t.Helper()
	if err := limits.normalize(4, 32); err != nil {
		t.Fatal(err)
	}
	return newExecutionScheduler(limits)
}
func schedulerPut(t *testing.T, s *executionScheduler, id, workspace, model string) {
	t.Helper()
	if err := s.checkLocked(workspace); err != nil {
		t.Fatal(err)
	}
	s.enqueueLocked(executionJob{ID: id, WorkspaceID: workspace, ModelKey: model, EnqueuedAt: time.Unix(100, 0)})
}
func schedulerPick(t *testing.T, s *executionScheduler, want string) {
	t.Helper()
	job, ok := s.pickLocked(time.Unix(102, 0))
	if !ok || job.ID != want {
		t.Fatalf("picked %+v/%v, want %s", job, ok, want)
	}
}

func TestChatSchedulerRoundRobinDeleteCursorAndWake(t *testing.T) {
	s := schedulerFixture(t, ChatSchedulingConfig{Workers: 1, WorkspaceQueueSize: 16})
	for _, v := range []struct{ id, ws string }{{"a1", "a"}, {"a2", "a"}, {"b1", "b"}, {"b2", "b"}, {"c1", "c"}, {"c2", "c"}} {
		schedulerPut(t, s, v.id, v.ws, "same")
	}
	schedulerPick(t, s, "a1")
	s.releaseLocked("a1") // next workspace b
	if !s.cancelLocked("a2") {
		t.Fatal("queued item not cancelled")
	} // remove before cursor
	schedulerPick(t, s, "b1")
	s.releaseLocked("b1")
	if !s.cancelLocked("c1") || !s.cancelLocked("c2") {
		t.Fatal("cancel current workspace")
	}
	schedulerPut(t, s, "d1", "d", "same")
	schedulerPick(t, s, "b2")
	s.releaseLocked("b2")
	schedulerPick(t, s, "d1")
	wake := s.wake
	s.releaseLocked("d1")
	select {
	case <-wake:
	default:
		t.Fatal("released capacity did not wake waiting workers")
	}
	if v := s.snapshotLocked(time.Now()); v.Queued != 0 || v.Running != 0 || v.WorkspaceCount != 0 || v.Dispatched != 4 || v.WaitSecondsTotal != 8 || v.WaitSecondsMax != 2 {
		t.Fatalf("retained finished group or invalid counts %+v", v)
	}
}
func TestChatSchedulerWorkspaceModelBudgetsAndHeadOfLine(t *testing.T) {
	s := schedulerFixture(t, ChatSchedulingConfig{Workers: 3, WorkspaceConcurrency: 2, ModelConcurrency: 1})
	schedulerPut(t, s, "a1", "a", "frozen-upstream-shared")
	schedulerPick(t, s, "a1")
	schedulerPut(t, s, "a2", "a", "frozen-upstream-shared")
	schedulerPut(t, s, "a3", "a", "other-model")
	schedulerPut(t, s, "b1", "b", "frozen-upstream-shared")
	schedulerPut(t, s, "b2", "b", "third-model")
	schedulerPick(t, s, "a3")
	schedulerPick(t, s, "b2")
	if _, ok := s.pickLocked(time.Now()); ok {
		t.Fatal("global/model running caps exceeded")
	}
	if s.cancelLocked("a1") {
		t.Fatal("running cancellation incorrectly released execution budget")
	}
	s.releaseLocked("a3")
	if _, ok := s.pickLocked(time.Now()); ok {
		t.Fatal("same upstream alias bypassed model cap")
	}
	s.releaseLocked("a1")
	schedulerPick(t, s, "a2")
	s.releaseLocked("a2")
	schedulerPick(t, s, "b1")
	s.releaseLocked("b1")
	s.releaseLocked("b2")
	s.releaseLocked("b2")
	if len(s.modelRunning) != 0 || len(s.workspaceRunning) != 0 {
		t.Fatal("finished budget maps retained entries")
	}
}
func TestChatSchedulerQueueCapsCancellationAndPrivateSnapshot(t *testing.T) {
	s := schedulerFixture(t, ChatSchedulingConfig{Workers: 1, QueueSize: 3, WorkspaceQueueSize: 2})
	schedulerPut(t, s, "1", "secret-ws-a", "secret-upstream")
	schedulerPut(t, s, "2", "secret-ws-a", "secret-upstream")
	if err := s.checkLocked("secret-ws-a"); err == nil {
		t.Fatal("workspace queue unbounded")
	}
	schedulerPut(t, s, "3", "secret-ws-b", "secret-upstream")
	if err := s.checkLocked("another"); err == nil {
		t.Fatal("global queue unbounded")
	}
	if !s.cancelLocked("2") || s.cancelLocked("2") {
		t.Fatal("queue cancellation not once-only")
	}
	if err := s.checkLocked("secret-ws-a"); err != nil {
		t.Fatal("cancelled queue slot retained", err)
	}
	schedulerPick(t, s, "1")
	local := s.workspaceSnapshotLocked("secret-ws-b", time.Now())
	if local.Queued != 1 || local.Running != 0 || local.WorkspaceCount != 1 || local.Dispatched != 0 {
		t.Fatal("workspace snapshot leaked global counts", local)
	}
	blob, _ := json.Marshal(s.snapshotLocked(time.Now()))
	if strings.Contains(string(blob), "secret") {
		t.Fatal("snapshot exposed private quota identities")
	}
}
func TestChatSchedulerBoundedTransitionsMaintainBudgets(t *testing.T) {
	s := schedulerFixture(t, ChatSchedulingConfig{Workers: 5, QueueSize: 64, WorkspaceConcurrency: 2, ModelConcurrency: 2, WorkspaceQueueSize: 8})
	rng := rand.New(rand.NewSource(16))
	next := 0
	for n := 0; n < 5000; n++ {
		switch rng.Intn(4) {
		case 0:
			workspace := fmt.Sprintf("w%d", rng.Intn(9))
			if s.checkLocked(workspace) == nil {
				next++
				s.enqueueLocked(executionJob{ID: fmt.Sprint(next), WorkspaceID: workspace, ModelKey: fmt.Sprint(rng.Intn(4)), EnqueuedAt: time.Now()})
			}
		case 1:
			s.pickLocked(time.Now())
		case 2:
			for id := range s.queued {
				s.cancelLocked(id)
				break
			}
		case 3:
			for id := range s.running {
				s.releaseLocked(id)
				break
			}
		}
		queued := 0
		seen := map[string]bool{}
		for _, w := range s.order {
			if seen[w] || len(s.workspaces[w]) == 0 {
				t.Fatal("ring duplicate or empty workspace")
			}
			seen[w] = true
			queued += len(s.workspaces[w])
			if len(s.workspaces[w]) > 8 {
				t.Fatal("workspace queue exceeded")
			}
		}
		if queued != len(s.queued) || len(s.queued) > 64 || len(s.running) > 5 || len(s.order) > 0 && (s.next < 0 || s.next >= len(s.order)) {
			t.Fatal("bounded queue/ring invariant violated")
		}
		actualWS, actualModel := map[string]int{}, map[string]int{}
		for _, j := range s.running {
			actualWS[j.WorkspaceID]++
			actualModel[j.ModelKey]++
			if _, present := s.queued[j.ID]; present {
				t.Fatal("job both queued and running")
			}
		}
		for w, count := range actualWS {
			if count != s.workspaceRunning[w] || count > 2 {
				t.Fatal("workspace budget drift")
			}
		}
		for m, count := range actualModel {
			if count != s.modelRunning[m] || count > 2 {
				t.Fatal("model budget drift")
			}
		}
	}
}
func TestChatExecutionHTTPPoolBoundedWithoutTotalStreamTimeout(t *testing.T) {
	p, err := newOpenAIChatGatewayWithConcurrency("https://gateway.example/v1", "test-key", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.CloseIdleConnections()
	transport, ok := p.client.Transport.(*http.Transport)
	if !ok || transport.MaxConnsPerHost != 3 || transport.MaxIdleConnsPerHost != 3 || transport.MaxIdleConns != 3 || transport.Proxy != nil || p.client.Timeout != 0 {
		t.Fatal("chat pool budget or streaming timeout isolation changed")
	}
	if _, err = newOpenAIChatGatewayWithConcurrency("https://gateway.example/v1", "test-key", 65); err == nil {
		t.Fatal("unbounded connection configuration accepted")
	}
}
