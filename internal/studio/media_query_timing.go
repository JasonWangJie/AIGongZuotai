package studio

import "time"

// In-package measurement only. The callback receives fixed phases and two
// timestamps. There is no runtime setting, background sampler or request data.
type mediaQueryTiming struct {
	record func(queryTimingPhase, time.Time, time.Time)
}
type queryTimingPhase uint8

const (
	querySelectReady queryTimingPhase = iota
	querySelectEmpty
	queryStrictClaim
	queryClaimMiss
	queryHandoff
	queryBeforeRenew
	queryFirstRenew
	queryHeartbeatRenew
	queryLeasePoolWait
	queryLeaseTransaction
	queryResultAppWait
	queryResultSave
	queryControlWait
	querySaveTransaction
	queryTimingPhaseCount
)

func (m *mediaQueryTiming) start() time.Time {
	if m == nil {
		return time.Time{}
	}
	return time.Now()
}
func (m *mediaQueryTiming) end(phase queryTimingPhase, start time.Time) {
	if m != nil {
		m.record(phase, start, time.Now())
	}
}
