package studio

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	soakSent256          = "due_to_client_send"
	soakChatAccepted256  = "chat_admission"
	soakMediaAccepted256 = "media_admission"
	soakChatFirst256     = "chat_send_to_first_observed_delta"
	soakChatComplete256  = "chat_send_to_observed_terminal"
	soakMediaFailed256   = "media_admission_to_observed_terminal"
)

// Each event has one clock reading. That reading determines both its counter
// and its observation latency, including for events in the drain bucket.
func (r *soakTrial256) record(name string, start, observed time.Time) {
	w := r.windowAt(observed)
	switch name {
	case soakSent256:
		w.Offered++
	case soakChatAccepted256:
		w.ChatAccepted++
	case soakMediaAccepted256:
		w.MediaAccepted++
	case soakChatFirst256:
		w.ChatFirstDelta++
	case soakChatComplete256:
		w.ChatComplete++
	case soakMediaFailed256:
		w.MediaFailed++
	default:
		panic("unknown soak observation")
	}
	w.add(name, observed.Sub(start))
}

func (w *soakWindow256) samplesMatch() bool {
	return len(w.latency[soakSent256]) == w.Offered &&
		len(w.latency[soakChatAccepted256]) == w.ChatAccepted &&
		len(w.latency[soakMediaAccepted256]) == w.MediaAccepted &&
		len(w.latency[soakChatFirst256]) == w.ChatFirstDelta &&
		len(w.latency[soakChatComplete256]) == w.ChatComplete &&
		len(w.latency[soakMediaFailed256]) == w.MediaFailed
}

type soakMiss256 struct {
	Index      int    `json:"index"`
	DueNS      int64  `json:"due_ns"`
	DeadlineNS int64  `json:"deadline_ns"`
	ObservedNS int64  `json:"observed_ns"`
	Reason     string `json:"reason"`
	DuringStop bool   `json:"during_stop"`
}

func (r *soakTrial256) noteMiss(index int, due, deadline, observed time.Time, reason string) {
	w := r.windowAt(due)
	// A delayed driver can discover multiple expired slots after the service
	// is ready. Only deadlines reached after the kill and no later than ready
	// were lost during unavailability. A busy driver or full client queue is
	// accountable even in the configured fault window.
	duringStop := reason == "expired" && !r.stopAt.IsZero() && !r.readyAt.IsZero() &&
		r.stopAt.Before(deadline) && !deadline.After(r.readyAt)
	w.Missed++
	if duringStop {
		w.MissedDuringStop++
	}
	w.misses = append(w.misses, soakMiss256{Index: index, DueNS: due.Sub(r.start).Nanoseconds(), DeadlineNS: deadline.Sub(r.start).Nanoseconds(), ObservedNS: observed.Sub(r.start).Nanoseconds(), Reason: reason, DuringStop: duringStop})
}

func soakMediaObservation256(admitted, observed time.Time, task Task) (bool, error) {
	if observed.Before(admitted) || observed.Sub(admitted) > 90*time.Second {
		return false, errors.New("admitted media exceeded the observation deadline")
	}
	if !terminal(task.Status) {
		return false, nil
	}
	if task.Status != "FAILED" || task.Error == nil || task.Error.Code != "UPSTREAM_GENERATION_FAILED" {
		return false, errors.New("media did not reach the controlled supplier outcome")
	}
	return true, nil
}

// Each read uses the retired generation's own database tag. Failure to read
// or retire within the caller's deadline prevents starting a new controller.
func waitSoakPGRelease256(ctx context.Context, interval time.Duration, read func(context.Context) (int, error)) error {
	for {
		n, err := read(ctx)
		if ctx.Err() != nil {
			return errors.New("retired PostgreSQL connections did not close before restart")
		}
		if err != nil || n < 0 {
			return errors.New("retired PostgreSQL connection observation failed")
		}
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("retired PostgreSQL connections did not close before restart")
		case <-time.After(interval):
		}
	}
}

func TestSoakEventWindowBoundaries256(t *testing.T) {
	start := time.Unix(100, 0)
	width := time.Second
	r := soakTrial256{start: start, profile: soakProfile256{Windows: 2, Width: width}, windows: make([]soakWindow256, 3)}
	names := []string{soakSent256, soakChatAccepted256, soakMediaAccepted256, soakChatFirst256, soakChatComplete256, soakMediaFailed256}
	for _, name := range names {
		r.record(name, start, start.Add(width-time.Nanosecond))
		r.record(name, start, start.Add(width))
		r.record(name, start, start.Add(2*width))
	}
	for i, w := range r.windows {
		if !w.samplesMatch() || w.Offered != 1 || w.ChatAccepted != 1 || w.MediaAccepted != 1 || w.ChatFirstDelta != 1 || w.ChatComplete != 1 || w.MediaFailed != 1 {
			t.Fatalf("inconsistent counts in bucket %d", i)
		}
		for _, name := range names {
			want := []time.Duration{width - time.Nanosecond, width, 2 * width}[i]
			if w.latency[name][0] != want {
				t.Fatal("observation moved to another window")
			}
		}
	}
	r.windows[1].ChatAccepted++
	if r.windows[1].samplesMatch() {
		t.Fatal("a window lost its admission latency sample")
	}
}

func TestSoakMissedSlotBoundaries256(t *testing.T) {
	start := time.Unix(100, 0)
	r := soakTrial256{start: start, profile: soakProfile256{Windows: 2, Width: time.Minute}, windows: make([]soakWindow256, 3), stopAt: start.Add(56 * time.Second), readyAt: start.Add(68 * time.Second)}
	cases := []struct {
		deadline time.Duration
		reason   string
		want     bool
	}{
		{55 * time.Second, "expired", false}, {56 * time.Second, "expired", false},
		{60 * time.Second, "expired", true}, {68 * time.Second, "expired", true},
		{68*time.Second + time.Nanosecond, "expired", false}, {90 * time.Second, "expired", false},
		{65 * time.Second, "outstanding_media_limit", false},
	}
	for i, c := range cases {
		deadline := start.Add(c.deadline)
		due := deadline.Add(-time.Second)
		r.noteMiss(i, due, deadline, start.Add(2*time.Minute), c.reason)
		w := r.windowAt(due)
		if got := w.misses[len(w.misses)-1]; got.DuringStop != c.want || got.DeadlineNS != c.deadline.Nanoseconds() || got.Index != i {
			t.Fatalf("incorrect lost-slot classification for case %d", i)
		}
	}
	if r.windows[0].MissedDuringStop != 1 || r.windows[1].MissedDuringStop != 1 || r.windows[2].Missed != 0 {
		t.Fatal("missed arrivals must belong to the original schedule")
	}
}

func TestSoakMediaDeadline256(t *testing.T) {
	start := time.Unix(100, 0)
	known := Task{Status: "FAILED", Error: &APIError{Code: "UPSTREAM_GENERATION_FAILED"}}
	for _, task := range []Task{known, {Status: "QUEUED"}} {
		done, err := soakMediaObservation256(start, start.Add(90*time.Second), task)
		if err != nil || done != (task.Status == "FAILED") {
			t.Fatal("legitimate boundary observation lost")
		}
		done, err = soakMediaObservation256(start, start.Add(90*time.Second+time.Nanosecond), task)
		if err == nil || done {
			t.Fatal("late observation marked successful")
		}
	}
}

func TestSoakPGReleaseGate256(t *testing.T) {
	reads := 0
	err := waitSoakPGRelease256(t.Context(), time.Nanosecond, func(context.Context) (int, error) {
		reads++
		return 3 - reads, nil
	})
	if err != nil || reads != 3 {
		t.Fatal("release must observe zero, not just a decrease")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = waitSoakPGRelease256(ctx, time.Hour, func(context.Context) (int, error) { cancel(); return 1, nil })
	if err == nil {
		t.Fatal("old connections outlived the wait")
	}
	expired, expire := context.WithCancel(t.Context())
	defer expire()
	err = waitSoakPGRelease256(expired, time.Nanosecond, func(context.Context) (int, error) { expire(); return 0, nil })
	if err == nil {
		t.Fatal("a zero count after deadline is not timely release")
	}
	err = waitSoakPGRelease256(t.Context(), time.Nanosecond, func(context.Context) (int, error) { return 0, errors.New("private diagnostic") })
	if err == nil || err.Error() == "private diagnostic" {
		t.Fatal("a failed count is not proof of release")
	}
}
