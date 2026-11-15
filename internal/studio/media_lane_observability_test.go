package studio

import (
	"context"
	"strings"
	"testing"
	"time"
)

type laneMetricsProvider struct{ submitted, queried chan struct{} }

func (p *laneMetricsProvider) Submit(ctx context.Context, input TaskInput) (MediaHandle, error) {
	if input.Inputs.Prompt == "private-submit-wait" {
		close(p.submitted)
		<-ctx.Done()
		return MediaHandle{}, ctx.Err()
	}
	return MediaHandle{ID: "private-upstream-handle", Kind: input.Kind, Count: 1, SubmittedAt: time.Now()}, nil
}
func (p *laneMetricsProvider) Query(ctx context.Context, _ MediaHandle) (MediaResult, error) {
	close(p.queried)
	<-ctx.Done()
	return MediaResult{}, ctx.Err()
}
func (p *laneMetricsProvider) Cancel(context.Context, MediaHandle) error { return nil }

func TestMediaLaneMetricsReflectIndependentReservedBudgets(t *testing.T) {
	provider := &laneMetricsProvider{submitted: make(chan struct{}), queried: make(chan struct{})}
	app := observerFixture(t, Config{MetricsToken: metricsFixtureToken, mediaProvider: provider, PollInterval: time.Millisecond,
		MediaScheduling: MediaSchedulingConfig{SubmitWorkers: 1, QueryWorkers: 1}})
	input := taskInput("image")
	input.Inputs.Prompt = "private-query-wait"
	queryTask, _, err := app.createTask(input, "private-query-key")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.queried:
	case <-time.After(3 * time.Second):
		t.Fatal("query did not enter")
	}
	input.Inputs.Prompt = "private-submit-wait"
	submitTask, _, err := app.createTask(input, "private-submit-key")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.submitted:
	case <-time.After(3 * time.Second):
		t.Fatal("submit blocked by independent query")
	}
	response := observedRequest(app.Handler(), "GET", "/metrics", "Bearer "+metricsFixtureToken)
	if response.Code != 200 {
		t.Fatal("metrics unavailable")
	}
	body := response.Body.String()
	validatePrometheusFixture(t, body)
	for _, sample := range []string{
		"studio_media_lane_reserved{lane=\"submit\"} 1\n", "studio_media_lane_reserved{lane=\"query\"} 1\n",
		"studio_media_lane_limit{lane=\"submit\"} 1\n", "studio_media_lane_limit{lane=\"query\"} 1\n",
		"studio_media_lane_steps_total{lane=\"submit\"} 2\n", "studio_media_lane_steps_total{lane=\"query\"} 1\n",
		"studio_media_scope_reserved{lane=\"submit\"} 1\n", "studio_media_scope_reserved{lane=\"query\"} 1\n",
	} {
		if !strings.Contains(body, sample) {
			t.Fatalf("missing observed media metric %s", sample)
		}
	}
	for _, private := range []string{queryTask.ID, submitTask.ID, "private-submit-wait", "private-query-wait", "private-upstream-handle", "private-submit-key", "private-query-key", metricsFixtureToken} {
		if strings.Contains(body, private) {
			t.Fatal("private media identity leaked to metrics")
		}
	}
}
