package studio

import (
	"runtime"
	"testing"
)

func TestMixedObservationGCWindows(t *testing.T) {
	var observer mixedGCObserver
	// The baseline deliberately contains older and much longer pauses.
	m := runtime.MemStats{NumGC: 255, PauseTotalNs: 1_000_000}
	m.PauseNs[254] = 900_000
	if baseline := observer.next(&m); baseline != (mixedGCWindow{}) {
		t.Fatal("history before baseline entered the measured window", baseline)
	}
	m.NumGC += 2
	m.PauseTotalNs += 73
	m.PauseNs[255], m.PauseNs[0] = 70, 3
	want := mixedGCWindow{Cycles: 2, PauseTotalNS: 73, PauseSamples: 2, MaxObservedNS: 70}
	if got := observer.next(&m); got != want {
		t.Fatal("incorrect interval across pause ring boundary", got)
	}
	if zero := observer.next(&m); zero != (mixedGCWindow{}) {
		t.Fatal("idle interval repeated an old collection")
	}
	m.NumGC += 257
	m.PauseTotalNs += 1027
	for i := range m.PauseNs {
		m.PauseNs[i] = 2
	}
	want = mixedGCWindow{Cycles: 257, PauseTotalNS: 1027, PauseSamples: 256, MaxObservedNS: 2, RingTruncated: true}
	if got := observer.next(&m); got != want {
		t.Fatal("incomplete ring masked full cycle or duration totals", got)
	}
}

func TestMixedObservationClosedLabelsAndLocalTarget(t *testing.T) {
	if mixedPGState("private-value") != "other" || mixedPGWaitType("private-value") != "other" ||
		mixedPGState("") != "unreported" || mixedPGWaitType("") != "none" ||
		mixedPGWaitType("Lock") != "Lock" || mixedPGState("idle in transaction") != "idle in transaction" {
		t.Fatal("unbounded or ambiguous report categories")
	}
	for _, input := range []string{"postgres://localhost/test", "postgresql://[::1]:5432/test", "postgres://127.0.0.3/test?sslmode=disable"} {
		if !mixedLoopbackDatabase(input) {
			t.Fatal("rejected valid loopback database")
		}
	}
	for _, input := range []string{"", "http://localhost/", "postgres://remote.invalid/test", "postgres://127.0.0.1/test?host=remote.invalid", "postgres://localhost/test?service=name"} {
		if mixedLoopbackDatabase(input) {
			t.Fatal("accepted an unbounded database target")
		}
	}
}

func TestMixedObservationStalledAndNormalHotReaders(t *testing.T) {
	app := observerFixture(t, Config{SubscriberBuffer: 4, ReplaySize: 2})
	out := mixedControlledFanout(t, app)
	if out.Capacity != 4 || out.Published != 6 || out.NormalDelivered != 6 || out.SlowBuffered != 4 || out.SlowEvictions != 1 {
		t.Fatal("hot reader backpressure or recovery scope changed", out)
	}
}
