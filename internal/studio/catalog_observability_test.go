package studio

import (
	"strings"
	"testing"
	"time"
)

func TestCustomAndRetiredModelsUseBoundedMetricBuckets(t *testing.T) {
	app := observerFixture(t, Config{})
	now := time.Now()
	app.mu.Lock()
	app.runs["custom-run"] = &runRecord{Run: Run{ID: "custom-run", WorkspaceID: "ws_demo", ModelID: "private-custom-chat", Status: "QUEUED", CreatedAt: now.Add(-20 * time.Second)}}
	app.tasks["custom-image"] = &taskRecord{Task: Task{ModelID: "private-custom-image", Kind: "image", Status: "SUBMIT_UNKNOWN", CreatedAt: now}, InFlight: true}
	app.tasks["retired-video"] = &taskRecord{Task: Task{ModelID: "private-retired-video", Kind: "video", Status: "QUEUED", CreatedAt: now.Add(-10 * time.Second)}, InFlight: true}
	app.mu.Unlock()
	metrics := app.prometheusText()
	for _, sample := range []string{
		`studio_cached_active_resources{kind="chat",model="other"} 1`,
		`studio_cached_active_resources{kind="image",model="other"} 1`,
		`studio_cached_active_resources{kind="video",model="other"} 1`,
		`studio_cached_queued_resources{kind="chat",model="other"} 1`,
		`studio_cached_queued_resources{kind="image",model="other"} 0`,
		`studio_cached_queued_resources{kind="video",model="other"} 1`,
	} {
		if !strings.Contains(metrics, sample) {
			t.Fatalf("custom or retired occupancy omitted: %s", sample)
		}
	}
	if strings.Contains(metrics, "private-") {
		t.Fatal("configured or historical model identity leaked into metric labels")
	}
	if count := strings.Count(metrics, "\nstudio_cached_active_resources{"); count != 6 {
		t.Fatalf("expected six fixed resource series, got %d", count)
	}
}
