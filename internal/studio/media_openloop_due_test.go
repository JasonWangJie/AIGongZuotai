package studio

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// One read-only statement for every active query projection in this disposable
// schema. The LEFT join retains observations before a channel's first claim.
// Identity disagreements must reach the caller rather than disappear in WHERE.
const openLoopDueSQL259 = `SELECT statement_timestamp(),t.id,COALESCE(t.handle->>'ID',''),
	s.version,t.lease_epoch,s.eligible_at,GREATEST(s.eligible_at,s.server_not_before,c.cooldown_until),
	COALESCE(c.parked,false),COALESCE(c.probe_required,false),
	COALESCE(s.workspace_id=t.workspace_id AND s.provider_mode='live' AND t.document->>'provider_mode'='live'
	 AND s.eligible_at=t.next_poll AND t.handle->>'ProviderIdentity'=s.channel_key
	 AND t.submission->>'provider_identity'=s.channel_key AND t.handle->>'CredentialID'=t.submission->>'credential_id',false)
	FROM studio_task_schedule s JOIN studio_tasks t ON t.id=s.task_id
	LEFT JOIN studio_media_channel_runtime c ON c.scope='gateway' AND c.lane='query' AND c.channel_key=s.channel_key
	WHERE s.active AND s.lane='query' LIMIT 4097`

type openLoopDueStamp259 struct{ begin, finish, db time.Time }
type openLoopDueGate259 struct {
	sample        int
	effective     time.Time
	parked, probe bool
}
type openLoopDueRound259 struct {
	version, firstEpoch, lastEpoch int64
	first, last                    int // inclusive indices of consecutive samples in which this version was present
	due                            time.Time
	gates                          []openLoopDueGate259 // append only when a relevant channel gate changes
}
type openLoopDueHandle259 struct {
	task   string
	rounds []openLoopDueRound259
}
type openLoopDueTracker259 struct {
	samples       []openLoopDueStamp259
	handles       map[string]*openLoopDueHandle259
	rounds, gates int
	invalid       bool
}

func newOpenLoopDueTracker259() *openLoopDueTracker259 {
	return &openLoopDueTracker259{handles: make(map[string]*openLoopDueHandle259)}
}

func (o *openLoopDueTracker259) sample(ctx context.Context, db *pgx.Conn) (sampleErr error) {
	defer func() {
		if sampleErr != nil {
			o.invalid = true
		}
	}()
	if len(o.samples) >= 8192 {
		return errors.New("independent schedule sample budget exhausted")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stamp := openLoopDueStamp259{begin: time.Now()}
	rows, err := db.Query(ctx, openLoopDueSQL259)
	if err != nil {
		return errors.New("independent schedule read failed")
	}
	defer rows.Close()
	i := len(o.samples)
	n := 0
	for rows.Next() {
		var task, handle string
		var version, epoch int64
		var due time.Time
		var gate openLoopDueGate259
		var identity bool
		err = rows.Scan(&stamp.db, &task, &handle, &version, &epoch, &due, &gate.effective, &gate.parked, &gate.probe, &identity)
		if err != nil || !identity || handle == "" || task == "" || version <= 0 || epoch < 0 || due.IsZero() || gate.effective.Before(due) {
			return errors.New("invalid frozen query projection")
		}
		n++
		if n > 4096 {
			return errors.New("independent schedule read exceeded local profile")
		}
		if err = o.retain(i, task, handle, version, epoch, due, gate); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return errors.New("independent schedule read was incomplete")
	}
	stamp.finish = time.Now()
	// Empty snapshots have no rows needing comparison against server time.
	o.samples = append(o.samples, stamp)
	return nil
}

func (o *openLoopDueTracker259) retain(i int, task, handle string, version, epoch int64, due time.Time, gate openLoopDueGate259) error {
	h := o.handles[handle]
	if h == nil {
		if len(o.handles) >= 4096 {
			return errors.New("too many original handles for local profile")
		}
		h = &openLoopDueHandle259{task: task}
		o.handles[handle] = h
	}
	if h.task != task {
		return errors.New("one original handle belongs to multiple tasks")
	}
	last := len(h.rounds) - 1
	if last < 0 || h.rounds[last].version != version {
		if last >= 0 && (version < h.rounds[last].version || epoch < h.rounds[last].lastEpoch || !due.After(h.rounds[last].due)) {
			return errors.New("sampled schedule or lease identity regressed")
		}
		if o.rounds >= 262144 {
			return errors.New("sampled schedule round budget exhausted")
		}
		h.rounds = append(h.rounds, openLoopDueRound259{version: version, first: i, last: i - 1, due: due, firstEpoch: epoch, lastEpoch: epoch})
		o.rounds++
		last++
	}
	r := &h.rounds[last]
	if r.last != i-1 || !r.due.Equal(due) || epoch < r.lastEpoch {
		return errors.New("sampled query version was duplicated, lost or changed")
	}
	r.last, r.lastEpoch = i, epoch
	if n := len(r.gates); n > 0 {
		old := r.gates[n-1]
		if old.parked == gate.parked && old.probe == gate.probe && old.effective.Equal(gate.effective) {
			return nil
		}
	}
	if o.gates >= 524288 {
		return errors.New("sampled schedule gate budget exhausted")
	}
	gate.sample = i
	r.gates = append(r.gates, gate)
	o.gates++
	return nil
}

// A lower bound: a sampled version is certified for a WroteRequest only
// when its unchanged projection version is read both before and after that
// callback. A missing denominator has no percentage.
type openLoopDueCoverage259 struct {
	CertifiedSameVersionWrites int
	AllSuccessfulWroteRequests int
	PercentLowerBound          *float64
}
type openLoopDueSummary259 struct {
	ProjectionObservedBeforeWrite                                                                                          openLoopDueCoverage259
	Samples, OriginalHandles, ObservedVersions, ObservedFirstVersions, ObservedRepeatVersions                              int
	WireHandlesAbsentFromAllSamples, WireWrites, BracketedWrites, UnmatchedWrites, VersionsWithoutBracketedWrite           int
	VersionsSeenRawDueWithoutPossibleWrite, VersionsSeenUnwrittenThroughNextSampleOrEndLowerBound, MaturityWindowUncertain int
	WithinVersionObservedLeaseEpochIncreases, BetweenVersionsObservedLeaseEpochIncreases                                   int64
}
type openLoopDueWindow259 struct {
	// The frontier is the last completed statement wholly inside the window.
	// A nil frontier is missing coverage, not a measured empty queue.
	Samples                                                                                   int
	Frontier                                                                                  *openLoopDueFrontier259
	ConfirmedObservedRawMaturities, ConfirmedFirstRawMaturities, ConfirmedRepeatRawMaturities int
	FirstObservedTimeGateDueVersions                                                          int
	BracketedWrites, UnmatchedWrites                                                          int
	BracketedRawDueToWriteUpperBound                                                          openLoopLatency259
}
type openLoopDueFrontier259 struct {
	SampleAtMS, SampleAgeAtWindowEndMS, QueryWallMS                                        float64
	ActiveQueryVersions, RawDue, RawDueUnwrittenLowerBound, PossiblyWrittenRawDue          int
	TimeGateDueWithoutParkOrProbe, TimeGateDueUnwrittenLowerBound, ParkedOrProbeRestricted int
	OldestRawUnwrittenMS, OldestTimeGateUnwrittenMS                                        float64
}

func dueMS259(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func firstWriteAt259(xs []time.Time, at time.Time) int {
	return sort.Search(len(xs), func(i int) bool { return !xs[i].Before(at) })
}

func (o *openLoopDueTracker259) report(start, end time.Time, width time.Duration, writes map[string][]time.Time) (openLoopDueSummary259, []openLoopDueWindow259, error) {
	var s openLoopDueSummary259
	if !end.After(start) || width <= 0 {
		return s, nil, errors.New("invalid independent schedule windows")
	}
	n := int((end.Sub(start) + width - 1) / width)
	if n > 8192 {
		return s, nil, errors.New("independent schedule window budget exceeded")
	}
	windows := make([]openLoopDueWindow259, n)
	if o.invalid || len(o.samples) == 0 {
		return s, windows, errors.New("incomplete independent schedule samples")
	}
	base := func(at time.Time) int {
		if at.Before(start) || at.After(end) {
			return -1
		}
		return min(int(at.Sub(start)/width), n-1)
	}
	frontiers := make([]int, n)
	latencies := make([][]time.Duration, n)
	for j := range frontiers {
		frontiers[j] = -1
	}
	for i, stamp := range o.samples {
		k := base(stamp.finish)
		if k < 0 || base(stamp.begin) != k {
			continue
		}
		w := &windows[k]
		w.Samples++
		frontiers[k] = i
		edge := start.Add(min(time.Duration(k+1)*width, end.Sub(start)))
		w.Frontier = &openLoopDueFrontier259{SampleAtMS: dueMS259(stamp.finish.Sub(start)), SampleAgeAtWindowEndMS: dueMS259(edge.Sub(stamp.finish)), QueryWallMS: dueMS259(stamp.finish.Sub(stamp.begin))}
	}
	s.Samples = len(o.samples)
	s.OriginalHandles = len(o.handles)
	s.ObservedVersions = o.rounds
	for handle, xs := range writes {
		if !sort.SliceIsSorted(xs, func(i, j int) bool { return xs[i].Before(xs[j]) }) {
			return s, windows, errors.New("unordered original GET writes")
		}
		h := o.handles[handle]
		if h == nil && len(xs) > 0 {
			s.WireHandlesAbsentFromAllSamples++
		}
		for _, x := range xs {
			k := base(x)
			if k < 0 {
				return s, windows, errors.New("original GET write outside measurement")
			}
			s.WireWrites++
			if h != nil {
				// Two snapshots of the same monotonically versioned projection
				// strictly surround the HTTP callback. A write touching a query's
				// uncertain execution interval is deliberately left unmatched.
				j := sort.Search(len(h.rounds), func(i int) bool { return !o.samples[h.rounds[i].first].finish.Before(x) }) - 1
				if j >= 0 {
					r := h.rounds[j]
					if x.Before(o.samples[r.last].begin) {
						first := o.samples[r.first]
						earliest := first.begin.Add(r.due.Sub(first.db))
						if x.Before(earliest) {
							return s, windows, errors.New("bracketed original GET before scheduled time")
						}
						s.BracketedWrites++
						windows[k].BracketedWrites++
						latencies[k] = append(latencies[k], x.Sub(earliest))
						continue
					}
				}
			}
			s.UnmatchedWrites++
			windows[k].UnmatchedWrites++
		}
	}
	for handle, h := range o.handles {
		xs := writes[handle]
		for j, r := range h.rounds {
			first, last := o.samples[r.first], o.samples[r.last]
			rawEarliest := first.begin.Add(r.due.Sub(first.db))
			rawLatest := first.finish.Add(r.due.Sub(first.db))
			idx := firstWriteAt259(xs, rawEarliest)
			repeat := idx > 0
			if repeat {
				s.ObservedRepeatVersions++
			} else {
				s.ObservedFirstVersions++
			}
			s.WithinVersionObservedLeaseEpochIncreases += r.lastEpoch - r.firstEpoch
			if j > 0 {
				s.BetweenVersionsObservedLeaseEpochIncreases += r.firstEpoch - h.rounds[j-1].lastEpoch
			}
			bracket := sort.Search(len(xs), func(i int) bool { return xs[i].After(first.finish) })
			if bracket == len(xs) || !xs[bracket].Before(last.begin) {
				s.VersionsWithoutBracketedWrite++
			}
			// Never turn a future timestamp from a now-retired projection into
			// an actual due. Independent evidence must retain this version
			// through a snapshot at or after the database deadline.
			if !r.due.After(last.db) {
				k := base(rawEarliest)
				if k >= 0 && base(rawLatest) == k {
					windows[k].ConfirmedObservedRawMaturities++
					if repeat {
						windows[k].ConfirmedRepeatRawMaturities++
					} else {
						windows[k].ConfirmedFirstRawMaturities++
					}
				} else {
					s.MaturityWindowUncertain++
				}
				m := r.first + sort.Search(r.last-r.first+1, func(off int) bool { return !o.samples[r.first+off].db.Before(r.due) })
				if idx == len(xs) || xs[idx].After(o.samples[m].finish) {
					s.VersionsSeenRawDueWithoutPossibleWrite++
					until := end
					if r.last+1 < len(o.samples) {
						until = o.samples[r.last+1].finish
					}
					if idx == len(xs) || xs[idx].After(until) {
						s.VersionsSeenUnwrittenThroughNextSampleOrEndLowerBound++
					}
				}
			}
			// This is the window of the first actual independent sample at which
			// the version passed its time and channel gates, not a forecast
			// from the previous response or a count of HTTP observations.
			for n, g := range r.gates {
				if g.parked || g.probe {
					continue
				}
				limit := r.last + 1
				if n+1 < len(r.gates) {
					limit = r.gates[n+1].sample
				}
				at := g.sample + sort.Search(limit-g.sample, func(off int) bool { return !o.samples[g.sample+off].db.Before(g.effective) })
				if at < limit {
					if k := base(o.samples[at].finish); k >= 0 {
						windows[k].FirstObservedTimeGateDueVersions++
					}
					break
				}
			}
			for k, i := range frontiers {
				if i < r.first || i > r.last {
					continue
				}
				w := windows[k].Frontier
				w.ActiveQueryVersions++
				stamp := o.samples[i]
				g := r.gates[sort.Search(len(r.gates), func(y int) bool { return r.gates[y].sample > i })-1]
				if g.parked || g.probe {
					w.ParkedOrProbeRestricted++
				}
				if r.due.After(stamp.db) {
					continue
				}
				w.RawDue++
				// Conservatively ignore any version for which even an unpaired
				// successful write could already have happened by this sample.
				pending := idx == len(xs) || xs[idx].After(stamp.finish)
				if pending {
					w.RawDueUnwrittenLowerBound++
					w.OldestRawUnwrittenMS = max(w.OldestRawUnwrittenMS, dueMS259(stamp.db.Sub(r.due)))
				} else {
					w.PossiblyWrittenRawDue++
				}
				if g.parked || g.probe || g.effective.After(stamp.db) {
					continue
				}
				w.TimeGateDueWithoutParkOrProbe++
				if pending {
					w.TimeGateDueUnwrittenLowerBound++
					w.OldestTimeGateUnwrittenMS = max(w.OldestTimeGateUnwrittenMS, dueMS259(stamp.db.Sub(g.effective)))
				}
			}
		}
	}
	for i := range windows {
		windows[i].BracketedRawDueToWriteUpperBound = openLoopLatencies259(latencies[i])
	}
	s.ProjectionObservedBeforeWrite = openLoopDueCoverage259{CertifiedSameVersionWrites: s.BracketedWrites, AllSuccessfulWroteRequests: s.WireWrites}
	if s.WireWrites > 0 {
		percent := 100 * float64(s.BracketedWrites) / float64(s.WireWrites)
		s.ProjectionObservedBeforeWrite.PercentLowerBound = &percent
	}
	if s.WireWrites > 0 && s.ObservedVersions == 0 {
		return s, windows, errors.New("wire activity without any independently observed query projection")
	}
	return s, windows, nil
}

func TestOpenLoopDueUnwrittenAndGates259(t *testing.T) {
	start := time.Unix(10_000, 0)
	at := func(ms int64) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
	o := newOpenLoopDueTracker259()
	for _, ms := range []int64{2100, 5100, 8100, 10100, 14100} {
		o.samples = append(o.samples, openLoopDueStamp259{begin: at(ms - 1), finish: at(ms + 1), db: at(ms)})
	}
	add := func(i int, h string, version, epoch, due, effective int64, probe bool) {
		t.Helper()
		if err := o.retain(i, h, h, version, epoch, at(due), openLoopDueGate259{effective: at(effective), probe: probe}); err != nil {
			t.Fatal(err)
		}
	}
	add(0, "a", 1, 1, 3000, 3000, false)
	add(1, "a", 1, 2, 3000, 3000, false)
	add(2, "a", 2, 2, 7000, 7000, false)
	add(3, "a", 2, 2, 7000, 7000, false)
	add(4, "a", 2, 3, 7000, 7000, false)
	add(1, "b", 8, 1, 4000, 9000, false)
	add(2, "b", 8, 1, 4000, 9000, false)
	add(3, "b", 8, 1, 4000, 9000, true)
	// A one-sample future projection retires before it can be confirmed due.
	add(0, "future", 3, 1, 7000, 7000, false)
	ss, ws, err := o.report(start, at(15500), 5*time.Second, map[string][]time.Time{"a": {at(4000)}, "unseen": {at(1000)}, "b": {at(11000)}})
	if err != nil || len(ws) != 4 || ss.OriginalHandles != 3 || ss.ObservedVersions != 4 || ss.ObservedRepeatVersions != 1 || ss.WireWrites != 3 || ss.BracketedWrites != 1 || ss.UnmatchedWrites != 2 || ss.WireHandlesAbsentFromAllSamples != 1 || ss.VersionsWithoutBracketedWrite != 3 || ss.WithinVersionObservedLeaseEpochIncreases != 2 {
		t.Fatalf("wrong sample coverage: %+v, %v", ss, err)
	}
	coverage := ss.ProjectionObservedBeforeWrite
	if coverage.CertifiedSameVersionWrites != 1 || coverage.AllSuccessfulWroteRequests != 3 || coverage.PercentLowerBound == nil || *coverage.PercentLowerBound != 100.0/3.0 {
		t.Fatal("wire coverage denominator mismatch", coverage)
	}
	if ss.VersionsSeenRawDueWithoutPossibleWrite != 2 || ss.VersionsSeenUnwrittenThroughNextSampleOrEndLowerBound != 1 {
		t.Fatal("unwritten independently observed rounds were lost", ss)
	}
	if ws[0].ConfirmedFirstRawMaturities != 2 || ws[0].BracketedWrites != 1 || ws[0].BracketedRawDueToWriteUpperBound.Samples != 1 || ws[1].ConfirmedRepeatRawMaturities != 1 || ws[1].FirstObservedTimeGateDueVersions != 2 {
		t.Fatal("raw due, gate observation and wire windows were conflated", ws)
	}
	f := ws[1].Frontier
	if f == nil || f.RawDue != 2 || f.RawDueUnwrittenLowerBound != 2 || f.TimeGateDueUnwrittenLowerBound != 1 || f.OldestRawUnwrittenMS != 4100 || f.OldestTimeGateUnwrittenMS != 1100 || f.SampleAgeAtWindowEndMS != 1899 {
		t.Fatal("incorrect sampled frontier", f)
	}
	if ws[3].Samples != 0 || ws[3].Frontier != nil || ws[3].ConfirmedObservedRawMaturities != 0 {
		t.Fatal("missing final partial-window sampling must stay explicit")
	}
}
func TestOpenLoopDueAmbiguousWrite259(t *testing.T) {
	start := time.Unix(3_000, 0)
	at := func(ms int64) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
	o := newOpenLoopDueTracker259()
	o.samples = []openLoopDueStamp259{{begin: at(999), db: at(1000), finish: at(1001)}, {begin: at(1999), db: at(2000), finish: at(2001)}}
	for i := range o.samples {
		if err := o.retain(i, "original", "handle", 2, 1, at(1000), openLoopDueGate259{effective: at(1000)}); err != nil {
			t.Fatal(err)
		}
	}
	s, ws, err := o.report(start, at(2600), time.Second, map[string][]time.Time{"handle": {at(1000)}})
	if err != nil || s.BracketedWrites != 0 || s.UnmatchedWrites != 1 || s.MaturityWindowUncertain != 1 || s.VersionsSeenRawDueWithoutPossibleWrite != 0 {
		t.Fatal("ambiguous write or boundary due treated as proven", s, err)
	}
	if ws[0].Frontier != nil || ws[1].Frontier != nil || ws[2].Frontier != nil {
		t.Fatal("snapshots spanning window boundaries cannot describe their exact trailing edge")
	}
	if err := o.retain(2, "different", "handle", 3, 2, at(3000), openLoopDueGate259{effective: at(3000)}); err == nil {
		t.Fatal("frozen handle reassigned")
	}
	if err := o.retain(2, "original", "handle", 2, 0, at(1000), openLoopDueGate259{effective: at(1000)}); err == nil {
		t.Fatal("regressed epoch accepted")
	}
	empty := newOpenLoopDueTracker259()
	empty.samples = append(empty.samples, openLoopDueStamp259{begin: at(800), finish: at(900)})
	noWire, _, noWireErr := empty.report(start, at(2600), time.Second, nil)
	if noWireErr != nil || noWire.ProjectionObservedBeforeWrite.PercentLowerBound != nil {
		t.Fatal("zero writes cannot have a coverage percentage")
	}
	missing, _, absentErr := empty.report(start, at(2600), time.Second, map[string][]time.Time{"unseen": {at(1100)}})
	if absentErr == nil || missing.WireHandlesAbsentFromAllSamples != 1 || missing.UnmatchedWrites != 1 {
		t.Fatal("wire-only run reported as covered")
	}
	o.invalid = true
	if _, _, err := o.report(start, at(2600), time.Second, nil); err == nil {
		t.Fatal("incomplete query sampling reported as complete")
	}
}
