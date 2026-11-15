package studio

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestChatSchedulerMetricsReflectRealQueueWithoutPrivateLabels(t *testing.T) {
	app := observerFixture(t, Config{MetricsToken: metricsFixtureToken, ChatScheduling: ChatSchedulingConfig{Workers: 1, QueueSize: 3, WorkspaceQueueSize: 2}})
	started := make(chan struct{})
	app.mu.Lock()
	app.chat = chatFunc(func(ctx context.Context, _ string, _ func(string) error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	app.mu.Unlock()
	input := runInput()
	input.Input = "PRIVATE_EXECUTION_PROMPT_DO_NOT_EXPOSE"
	first, _, err := app.createRun(input, "metrics-running-key")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider not entered")
	}
	second, _, err := app.createRun(input, "metrics-waiting-key")
	if err != nil {
		t.Fatal(err)
	}
	got := observedRequest(app.Handler(), "GET", "/metrics", "Bearer "+metricsFixtureToken)
	text := got.Body.String()
	if got.Code != 200 {
		t.Fatal("operator metrics unavailable")
	}
	validatePrometheusFixture(t, text)
	for _, sample := range []string{"studio_chat_scheduler_queued 1\n", "studio_chat_scheduler_running 1\n", "studio_chat_scheduler_workspaces 1\n", "studio_chat_scheduler_workers_limit 1\n", "studio_chat_scheduler_queue_limit 3\n", "studio_chat_scheduler_workspace_queue_limit 2\n", "studio_chat_scheduler_dispatched_total 1\n"} {
		if !strings.Contains(text, sample) {
			t.Fatalf("missing actual scheduler sample %s", sample)
		}
	}
	for _, secret := range []string{first.ID, second.ID, input.Input, "metrics-running-key", "metrics-waiting-key", "ws_demo", metricsFixtureToken} {
		if strings.Contains(text, secret) {
			t.Fatal("private execution value entered metrics")
		}
	}
	if _, err = app.cancelRun(second.ID); err != nil {
		t.Fatal(err)
	}
	text = observedRequest(app.Handler(), "GET", "/metrics", "Bearer "+metricsFixtureToken).Body.String()
	if !strings.Contains(text, "studio_chat_scheduler_queued 0\n") || !strings.Contains(text, "studio_chat_scheduler_running 1\n") {
		t.Fatal("queue cancellation did not free only the waiting slot")
	}
}
