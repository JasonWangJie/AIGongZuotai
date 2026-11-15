package studio

import (
	"fmt"
	"strings"
)

// Only four fixed lane labels. Reserved slots include lease/connection work,
// downstream I/O, review and terminal commit; they are not gateway connections.
func writeMediaSchedulingMetrics(out *strings.Builder, snapshot MediaExecutionSnapshot) {
	metricHeader(out, "studio_media_lane_reserved", "Reserved execution budgets in this process, including claim, connection, review and persistence work.", "gauge")
	fmt.Fprintf(out, "studio_media_lane_reserved{lane=\"submit\"} %d\nstudio_media_lane_reserved{lane=\"query\"} %d\nstudio_media_lane_reserved{lane=\"delivery\"} %d\nstudio_media_lane_reserved{lane=\"review\"} %d\n", snapshot.SubmitInFlight, snapshot.QueryInFlight, snapshot.DeliveryInFlight, snapshot.ReviewInFlight)
	metricHeader(out, "studio_media_lane_limit", "Configured process-local execution ceilings; review shares the bounded moderation engine with reference input checks.", "gauge")
	fmt.Fprintf(out, "studio_media_lane_limit{lane=\"submit\"} %d\nstudio_media_lane_limit{lane=\"query\"} %d\nstudio_media_lane_limit{lane=\"delivery\"} %d\nstudio_media_lane_limit{lane=\"review\"} %d\n", snapshot.SubmitLimit, snapshot.QueryLimit, snapshot.DeliveryLimit, snapshot.ReviewLimit)
	metricHeader(out, "studio_media_lane_steps_total", "Claimed execution steps in this process lifetime; not upstream request counts or delivered assets.", "counter")
	fmt.Fprintf(out, "studio_media_lane_steps_total{lane=\"submit\"} %d\nstudio_media_lane_steps_total{lane=\"query\"} %d\nstudio_media_lane_steps_total{lane=\"delivery\"} %d\nstudio_media_lane_steps_total{lane=\"review\"} %d\n", snapshot.SubmitSteps, snapshot.QuerySteps, snapshot.DeliverySteps, snapshot.ReviewSteps)
}
