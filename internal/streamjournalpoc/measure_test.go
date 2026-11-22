package streamjournalpoc

import (
	"math/bits"
	"testing"
	"time"
)

type profile struct {
	Runs    int `json:"logical_runs"`
	Shards  int `json:"writer_shards"`
	Readers int `json:"replay_readers"`
	Rate    int `json:"target_events_per_second"`
	Warm    int `json:"warmup_seconds"`
	Seconds int `json:"measurement_seconds"`
}

func configured(full bool) profile {
	if full {
		return profile{10000, 8, 4, 100000, 2, 12}
	}
	return profile{128, 2, 2, 1280, 1, 3}
}

const tick = 5 * time.Millisecond
const flush = 8 * time.Millisecond

// Integer arithmetic also generates non-divisible (pilot) rates exactly.
func slots(i, rate int) int { return (i+1)*rate/200 - i*rate/200 }

type histogram struct {
	cells   [2048]uint64
	n       uint64
	maximum int64
}

func bucket(us int64) int {
	if us < 32 {
		return int(max(0, us))
	}
	power := bits.Len64(uint64(us)) - 1
	return (power-4)*32 + int((uint64(us)>>uint(power-5))&31)
}
func upper(i int) int64 {
	if i < 32 {
		return int64(i)
	}
	power := i/32 + 4
	return (int64(32+i%32+1) << uint(power-5)) - 1
}
func (h *histogram) add(d time.Duration) {
	v := max(int64(0), d.Microseconds())
	h.cells[bucket(v)]++
	h.n++
	h.maximum = max(h.maximum, v)
}
func (h *histogram) join(o *histogram) {
	for i, n := range o.cells {
		h.cells[i] += n
	}
	h.n += o.n
	h.maximum = max(h.maximum, o.maximum)
}

type distribution struct {
	N   uint64 `json:"n"`
	P50 int64  `json:"p50_us_upper"`
	P95 int64  `json:"p95_us_upper"`
	P99 int64  `json:"p99_us_upper"`
	Max int64  `json:"max_us"`
}

func (h *histogram) result() distribution {
	q := func(p uint64) int64 {
		if h.n == 0 {
			return 0
		}
		need := (h.n*p + 99) / 100
		var n uint64
		for i, v := range h.cells {
			n += v
			if n >= need {
				return min(upper(i), h.maximum)
			}
		}
		return h.maximum
	}
	return distribution{h.n, q(50), q(95), q(99), h.maximum}
}

type epochWindow struct {
	origin    time.Time
	warm, end time.Duration
	seconds   int
}

func (w epochWindow) bin(ns int64) int {
	d := time.Duration(ns) - w.warm
	if d < 0 || time.Duration(ns) >= w.end {
		return -1
	}
	return int(d / time.Second)
}

type counts struct {
	total    int64
	bySecond []int64
}

func (w epochWindow) counts() counts { return counts{bySecond: make([]int64, w.seconds)} }
func (m *counts) add(w epochWindow, ns int64, n int) {
	m.total += int64(n)
	if i := w.bin(ns); i >= 0 {
		m.bySecond[i] += int64(n)
	}
}
func (m *counts) join(o counts) {
	m.total += o.total
	for i, n := range o.bySecond {
		m.bySecond[i] += n
	}
}
func sum(a []int64) int64 {
	var s int64
	for _, v := range a {
		s += v
	}
	return s
}

func TestScheduleAndJournalFormat(t *testing.T) {
	for _, p := range []profile{configured(false), configured(true)} {
		var n int
		for i := 0; i < 200*p.Seconds; i++ {
			n += slots(i, p.Rate)
		}
		if n != p.Seconds*p.Rate {
			t.Fatal(n)
		}
	}
	g := layout{10000, 8}
	batch := []delta{newDelta(1, 1, 3, 0), newDelta(9, 2, 1, 33), newDelta(1, 1, 4, 66)}
	u, e := g.prepare(0, batch)
	if e != nil || len(u.ids) != 2 || u.previous[0] != 2 || u.last[0] != 4 {
		t.Fatal("incorrect grouped heads")
	}
	if _, e = g.prepare(1, batch); e == nil {
		t.Fatal("wrong shard accepted")
	}
	for _, bad := range []delta{newDelta(1, 2, 4, 0), newDelta(1, 1, 7, 0), newDelta(1, 1, 3, -1)} {
		if _, e = g.prepare(0, []delta{batch[0], bad}); e == nil {
			t.Fatal("bad epoch or sequence")
		}
	}
	raw := encode(batch)
	n := 0
	if e = decode(raw, 3, func(d delta) error {
		if d != batch[n] {
			t.Fatal("body changed")
		}
		n++
		return nil
	}); e != nil || n != 3 {
		t.Fatal(e)
	}
	raw[len(raw)-1] ^= 1
	if e = decode(raw, 3, func(delta) error { return nil }); e == nil {
		t.Fatal("altered text accepted")
	}
	if e = decode(raw[:len(raw)-1], 3, func(delta) error { return nil }); e == nil {
		t.Fatal("short body accepted")
	}
	var h histogram
	for i := int64(0); i < 200000; i += 17 {
		if upper(bucket(i)) < i {
			t.Fatal("understated latency")
		}
		h.add(time.Duration(i) * time.Microsecond)
	}
	r := h.result()
	if r.N == 0 || r.P50 < 99000 || r.P95 < 189000 || r.P99 < 197000 || r.P99 > r.Max {
		t.Fatal(r)
	}
}
