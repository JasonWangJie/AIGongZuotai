package studio

import (
	"fmt"
	"strings"
)

// Fixed vocabularies: execution metadata and user input never become labels.
var durableEventActions = [...]string{"projected", "reset"}
var taskLeaseActions = [...]string{"claimed", "renewed", "lost", "rejected"}

func (o *observability) recordDurable(action string, count uint64) {
	for i, label := range durableEventActions {
		if action == label {
			o.durableEvents[i].Add(count)
			return
		}
	}
}

func (o *observability) recordLease(action string, count uint64) {
	for i, label := range taskLeaseActions {
		if action == label {
			o.taskLeases[i].Add(count)
			return
		}
	}
}

func (o *observability) writeExecutionMetrics(out *strings.Builder) {
	metricHeader(out, "studio_durable_events_total", "This-process committed feed projections and stream resets; not browser acknowledgments or global backlog.", "counter")
	for i, label := range durableEventActions {
		fmt.Fprintf(out, "studio_durable_events_total{action=%q} %d\n", label, o.durableEvents[i].Load())
	}
	metricHeader(out, "studio_task_leases_total", "This-process media task lease operations; not a cluster-wide ownership gauge.", "counter")
	for i, label := range taskLeaseActions {
		fmt.Fprintf(out, "studio_task_leases_total{action=%q} %d\n", label, o.taskLeases[i].Load())
	}
}
