package studio

import (
	"strings"
	"sync"
	"testing"
)

func TestExecutionCountersConcurrentAndPrivate(t *testing.T) {
	var obs observability
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				obs.recordDurable("projected", 1)
				obs.recordLease("renewed", 1)
			}
		}()
	}
	workers.Wait()
	obs.recordDurable("prompt-secret-project-123", 100)
	obs.recordLease("task-secret-123", 100)
	var out strings.Builder
	obs.writeExecutionMetrics(&out)
	text := out.String()
	if !strings.Contains(text, "studio_durable_events_total{action=\"projected\"} 800\n") || !strings.Contains(text, "studio_task_leases_total{action=\"renewed\"} 800\n") {
		t.Fatal("concurrent execution observations lost")
	}
	if strings.Contains(text, "secret") || strings.Count(text, "{action=") != 6 {
		t.Fatal("execution labels are not bounded and private")
	}
}
