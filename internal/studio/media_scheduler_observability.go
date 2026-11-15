package studio

import (
	"fmt"
	"strings"
)

// These are process-local execution observations, not a scan of persisted
// tasks. Labels are a closed set and never contain tenant/provider identities.
func writeMediaFairnessMetrics(out *strings.Builder, snapshot MediaSchedulerSnapshot, permits MediaPermitSnapshot) {
	lanes := [...]string{"submit", "query", "delivery", "review"}
	metricHeader(out, "studio_media_scope_reserved", "Current process reservations covering workspace and frozen-channel budgets.", "gauge")
	for i, lane := range lanes {
		fmt.Fprintf(out, "studio_media_scope_reserved{lane=%q} %d\n", lane, snapshot.Lanes[i].ScopeInFlight)
	}
	metricHeader(out, "studio_media_schedule_deferrals_total", "Local scheduling deferrals; repeated visits may count the same task, not queue length.", "counter")
	for i, lane := range lanes {
		fmt.Fprintf(out, "studio_media_schedule_deferrals_total{lane=%q,reason=\"capacity\"} %d\n", lane, snapshot.Lanes[i].CapacityDeferrals)
		fmt.Fprintf(out, "studio_media_schedule_deferrals_total{lane=%q,reason=\"cooldown\"} %d\n", lane, snapshot.Lanes[i].CooldownDeferrals)
	}
	metricHeader(out, "studio_media_schedule_examined_total", "Candidates inspected by this process; not accepted jobs or upstream requests.", "counter")
	for i, lane := range lanes {
		fmt.Fprintf(out, "studio_media_schedule_examined_total{lane=%q} %d\n", lane, snapshot.Lanes[i].ExaminedCandidates)
	}
	metricHeader(out, "studio_media_schedule_claims_total", "Confirmed strict scheduling claims in this process lifetime.", "counter")
	for i, lane := range lanes {
		fmt.Fprintf(out, "studio_media_schedule_claims_total{lane=%q} %d\n", lane, snapshot.Lanes[i].Claims)
	}
	metricHeader(out, "studio_media_review_waiting", "Bounded moderation permit waiters in this process, without workspace labels.", "gauge")
	fmt.Fprintf(out, "studio_media_review_waiting{phase=\"input\"} %d\nstudio_media_review_waiting{phase=\"output\"} %d\n", permits.InputWaiting, permits.OutputWaiting)
	metricHeader(out, "studio_media_review_permits", "Shared physical moderation permits held until actual work exits.", "gauge")
	fmt.Fprintf(out, "studio_media_review_permits %d\n", permits.InFlight)
}
