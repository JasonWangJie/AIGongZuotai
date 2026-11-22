package studio

import (
	"fmt"
	"strings"
)

// Only fixed series are emitted. Private model aliases, credentials, workspace
// identifiers and request content never become a metrics label or HELP value.
func writeChatSchedulingMetrics(out *strings.Builder, snapshot ExecutionSchedulerSnapshot) {
	for _, metric := range []struct {
		name, help string
		value      int
	}{
		{"studio_chat_scheduler_queued", "Accepted chat operations waiting in this process.", snapshot.Queued},
		{"studio_chat_scheduler_running", "Chat operations occupying execution slots, including output review and terminal persistence.", snapshot.Running},
		{"studio_chat_scheduler_workspaces", "Workspaces with queued or running chat operations in this process.", snapshot.WorkspaceCount},
		{"studio_chat_scheduler_workers_limit", "Configured maximum number of in-process chat executions.", snapshot.Limits.Workers},
		{"studio_chat_scheduler_queue_limit", "Configured total waiting chat queue capacity.", snapshot.Limits.QueueSize},
		{"studio_chat_scheduler_workspace_concurrency_limit", "Configured per-workspace chat execution ceiling.", snapshot.Limits.WorkspaceConcurrency},
		{"studio_chat_scheduler_model_concurrency_limit", "Configured per-frozen-upstream-model chat execution ceiling.", snapshot.Limits.ModelConcurrency},
		{"studio_chat_scheduler_workspace_queue_limit", "Configured per-workspace waiting chat capacity.", snapshot.Limits.WorkspaceQueueSize},
	} {
		metricHeader(out, metric.name, metric.help, "gauge")
		fmt.Fprintf(out, "%s %d\n", metric.name, metric.value)
	}
	metricHeader(out, "studio_chat_scheduler_dispatched_total", "Chat operations dispatched in this process lifetime.", "counter")
	fmt.Fprintf(out, "studio_chat_scheduler_dispatched_total %d\n", snapshot.Dispatched)
	metricHeader(out, "studio_chat_scheduler_wait_seconds_total", "Total waiting time before chat execution in this process lifetime.", "counter")
	fmt.Fprintf(out, "studio_chat_scheduler_wait_seconds_total %g\n", snapshot.WaitSecondsTotal)
	metricHeader(out, "studio_chat_scheduler_wait_seconds_max", "Largest observed chat queue wait in this process lifetime; not a percentile.", "gauge")
	fmt.Fprintf(out, "studio_chat_scheduler_wait_seconds_max %g\n", snapshot.WaitSecondsMax)
}

// These aggregates are emitted only on the operator metrics endpoint. A
// tenant's capacity projection does not expose other tenants' activity.
func (a *App) writeManagedChatMetricsLocked(out *strings.Builder) {
	if a.chatChildren == nil {
		return
	}
	metricHeader(out, "studio_chat_child_ready_slots", "Capacity of live managed chat children after handshake, including busy slots.", "gauge")
	fmt.Fprintf(out, "studio_chat_child_ready_slots %d\n", a.chatChildren.liveSlots())
	metricHeader(out, "studio_chat_child_running", "Managed chat executions occupying slots until terminal processing returns.", "gauge")
	fmt.Fprintf(out, "studio_chat_child_running %d\n", len(a.execution.running)-a.execution.localRunning)
	metricHeader(out, "studio_chat_child_dispatched_total", "Dispatches to a reserved managed child slot.", "counter")
	fmt.Fprintf(out, "studio_chat_child_dispatched_total %d\n", a.execution.childDispatches)
	metricHeader(out, "studio_chat_child_local_fallback_total", "First local dispatch of unclaimed child-eligible jobs after loss of all children.", "counter")
	fmt.Fprintf(out, "studio_chat_child_local_fallback_total %d\n", a.execution.localFallbacks)
}
