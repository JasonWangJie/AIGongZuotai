package streamjournalpoc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type producerStats struct {
	offered, accepted counts
	peaks             []int
	blocked           int64
}

func produce(ctx context.Context, p profile, w epochWindow, qs []chan delta) producerStats {
	s := producerStats{offered: w.counts(), accepted: w.counts(), peaks: make([]int, p.Shards)}
	defer func() {
		for _, q := range qs {
			close(q)
		}
	}()
	var pending delta
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for tickNo := 0; tickNo < 200*(p.Warm+p.Seconds); tickNo++ {
		start := w.origin.Add(time.Duration(tickNo) * tick)
		if wait := time.Until(start); wait > 0 {
			timer.Reset(wait)
			select {
			case <-ctx.Done():
				return s
			case <-timer.C:
			}
		}
		for n := 0; n < slots(tickNo, p.Rate); n++ {
			now := time.Now()
			if ctx.Err() != nil {
				return s
			}
			// Expired slots are never made up by bursting into a later window.
			if !now.Before(start.Add(tick)) {
				break
			}
			ns := now.Sub(w.origin).Nanoseconds()
			if pending.run == 0 {
				i := s.accepted.total
				pending = newDelta(uint32(i%int64(p.Runs))+1, 1, uint32(i/int64(p.Runs))+1, ns)
				s.offered.add(w, ns, 1)
			}
			shard := int(pending.run-1) % p.Shards
			select {
			case qs[shard] <- pending:
				s.peaks[shard] = max(s.peaks[shard], len(qs[shard]))
				s.accepted.add(w, ns, 1)
				pending = delta{}
			default:
				// Keep this exact, never-admitted event for the next source tick. No
				// journal submission or sequence advancement has happened for it.
				s.blocked++
				s.peaks[shard] = cap(qs[shard])
				n = slots(tickNo, p.Rate)
			}
		}
	}
	return s
}

type writerStats struct {
	lastAckNS, lastMeasuredAckNS int64
	commits, acks                counts
	queued, database, delay      histogram
	largest                      int
}

func writeShard(ctx context.Context, j journal, w epochWindow, c *pgx.Conn, shard int, q <-chan delta, ack *atomic.Int64, s *writerStats) error {
	s.commits = w.counts()
	s.acks = w.counts()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	records := make([]delta, 0, maxBatch)
	for {
		var d delta
		var more bool
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, more = <-q:
			if !more {
				return nil
			}
		}
		records = append(records[:0], d)
		deadline := time.Now().Add(flush)
		timer.Reset(flush)
	collect:
		for len(records) < maxBatch && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				break collect
			case d, more = <-q:
				if !more {
					break collect
				}
				records = append(records, d)
			}
		}
		timer.Stop()
		start := time.Now()
		pos, e := j.append(ctx, c, shard, records)
		if e != nil {
			return e
		}
		at := time.Now()
		ns := at.Sub(w.origin).Nanoseconds()
		if pos != ack.Load()+1 {
			return errors.New("unexpected durable position")
		}
		s.acks.add(w, ns, len(records))
		s.lastAckNS = ns
		s.commits.add(w, ns, 1)
		s.largest = max(s.largest, len(records))
		if w.bin(ns) >= 0 {
			s.lastMeasuredAckNS = ns
			s.database.add(at.Sub(start))
		}
		for _, r := range records {
			if w.bin(r.offeredNS) >= 0 {
				s.queued.add(start.Sub(w.origin) - time.Duration(r.offeredNS))
				s.delay.add(at.Sub(w.origin) - time.Duration(r.offeredNS))
			}
		}
		// Readers never receive a position that has not returned a successful COMMIT.
		ack.Store(pos)
		if !more {
			return nil
		}
	}
}

type readerStats struct {
	firstVerifiedNS, firstMeasuredVerifiedNS int64
	reads, pages                             counts
	delay                                    histogram
}

func replay(ctx context.Context, j journal, w epochWindow, c *pgx.Conn, reader, readers int, ack []atomic.Int64, done *atomic.Bool, last []uint32, s *readerStats) error {
	s.reads = w.counts()
	s.pages = w.counts()
	cursors := make([]cursor, j.shards)
	for i := reader; i < j.shards; i += readers {
		cursors[i] = cursor{dataset: j.dataset, shard: i}
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for ctx.Err() == nil {
		progress := false
		// Load completion before positions. No writer can publish later once done.
		finished := done.Load()
		for i := reader; i < j.shards; i += readers {
			high := ack[i].Load()
			if high == cursors[i].after {
				continue
			}
			finished = false
			progress = true
			old := cursors[i]
			next, e := j.read(ctx, c, old, i, 0, high, func(r delta) error {
				// All events of a Run go to one reader. Payload and shard are checked
				// separately by the decoder before any record is counted.
				if r.epoch != 1 || r.seq != last[r.run]+1 {
					return errors.New("read sequence or epoch mismatch")
				}
				last[r.run] = r.seq
				ns := time.Since(w.origin).Nanoseconds()
				s.reads.add(w, ns, 1)
				if s.firstVerifiedNS == 0 {
					s.firstVerifiedNS = ns
				}
				if s.firstMeasuredVerifiedNS == 0 && w.bin(ns) >= 0 {
					s.firstMeasuredVerifiedNS = ns
				}
				if w.bin(r.offeredNS) >= 0 {
					s.delay.add(time.Duration(ns - r.offeredNS))
				}
				return nil
			})
			if e != nil {
				return e
			}
			if next.after <= old.after || next.after-old.after > maxPage {
				return errors.New("invalid replay page boundary")
			}
			cursors[i] = next
			s.pages.add(w, time.Since(w.origin).Nanoseconds(), 1)
		}
		if finished {
			return nil
		}
		if !progress {
			timer.Reset(time.Millisecond)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return ctx.Err()
}

type second struct {
	Second   int   `json:"second"`
	Target   int   `json:"target"`
	Offered  int64 `json:"offered_unique"`
	Admitted int64 `json:"admitted"`
	Ack      int64 `json:"acknowledged"`
	Verified int64 `json:"verified_replayed"`
	Commits  int64 `json:"commits"`
	Pages    int64 `json:"replay_pages"`
}
type inventory struct{ RunHeads, NonemptyRuns, HeadSum, JournalRecords, Batches, Positions int64 }

// Count a reader only when it verified an event inside the measurement
// window before a later in-window commit on one of its assigned shards.
func readersOverlappingMeasurement(writers []writerStats, readers []readerStats) int64 {
	var overlapping int64
	for id, reader := range readers {
		for shard := id; shard < len(writers); shard += len(readers) {
			if reader.firstMeasuredVerifiedNS > 0 && reader.firstMeasuredVerifiedNS < writers[shard].lastMeasuredAckNS {
				overlapping++
				break
			}
		}
	}
	return overlapping
}

func (v inventory) coversRuns(runs int) bool {
	return v.RunHeads == int64(runs) && v.NonemptyRuns == v.RunHeads
}

func (f *fixture) inventory(ctx context.Context) (out inventory, err error) {
	err = f.root.QueryRow(ctx, `SELECT (SELECT count(*) FROM heads),(SELECT count(*) FROM heads WHERE seq>0),
 (SELECT coalesce(sum(seq),0) FROM heads),(SELECT coalesce(sum(n),0) FROM batches),(SELECT count(*) FROM batches),(SELECT sum(pos) FROM shards)`).Scan(&out.RunHeads, &out.NonemptyRuns, &out.HeadSum, &out.JournalRecords, &out.Batches, &out.Positions)
	return
}

type environment struct {
	OS, Arch, Go, CPUDescription string
	LogicalCPUs, GoMaxProcs      int
	HeapBefore, HeapAfter        uint64
	GCs                          uint32
	Postgres                     map[string]string
}
type report struct {
	Format                string                  `json:"format"`
	Profile               profile                 `json:"profile"`
	Started               time.Time               `json:"started_utc"`
	Environment           environment             `json:"environment"`
	Limits                map[string]int          `json:"limits"`
	Samples               []second                `json:"one_second_samples"`
	Total                 map[string]int64        `json:"warmup_window_and_drain_totals"`
	Rates                 map[string]float64      `json:"measurement_events_per_second"`
	WarmAndMeasuredTarget int64                   `json:"warmup_and_measurement_target_slots"`
	PeakQueues            []int                   `json:"observed_peak_buffered_per_shard"`
	LargestBatch          int                     `json:"largest_persisted_batch"`
	Latency               map[string]distribution `json:"latency"`
	Inventory             inventory               `json:"committed_inventory"`
	DrainMs               int64                   `json:"post_window_drain_ms"`
	Correct               bool                    `json:"integrity_pass"`
	Target                bool                    `json:"measured_target_met"`
	Failure               string                  `json:"failure,omitempty"`
}

func safeError(e error) string {
	var pg *pgconn.PgError
	if errors.As(e, &pg) {
		return "postgres_" + pg.Code
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(e, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(e, errFence) {
		return "epoch_or_sequence_conflict"
	}
	return "journal_or_replay_failure"
}
func saveReport(path string, v report) error {
	data, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	// Never overwrite an earlier measurement. Even a failed run is evidence.
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	out, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = out.Write(append(data, '\n'))
	closeErr := out.Close()
	return errors.Join(e, closeErr)
}

func TestPostgresStreamJournalCapacity(t *testing.T) {
	p := configured(os.Getenv("STREAM_JOURNAL_PROFILE") == "full")
	f := isolate(t, layout{p.Runs, p.Shards})
	path := os.Getenv("STREAM_JOURNAL_REPORT")
	if !filepath.IsAbs(path) {
		t.Fatal("STREAM_JOURNAL_REPORT must be a new absolute JSON file")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("report destination must be fresh")
	}
	writers := make([]*pgx.Conn, p.Shards)
	readers := make([]*pgx.Conn, p.Readers)
	for i := range writers {
		writers[i] = f.conn(t)
	}
	for i := range readers {
		readers[i] = f.conn(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.Warm+p.Seconds+80)*time.Second)
	defer cancel()
	var failures sync.Once
	var failure string
	fail := func(e error) {
		if e != nil {
			failures.Do(func() { failure = safeError(e); cancel() })
		}
	}
	var mem0, mem1 runtime.MemStats
	runtime.ReadMemStats(&mem0)
	w := epochWindow{time.Now().Add(80 * time.Millisecond), time.Duration(p.Warm) * time.Second, time.Duration(p.Warm+p.Seconds) * time.Second, p.Seconds}
	qs := make([]chan delta, p.Shards)
	ack := make([]atomic.Int64, p.Shards)
	ws := make([]writerStats, p.Shards)
	rs := make([]readerStats, p.Readers)
	var writersWG, readersWG sync.WaitGroup
	var done atomic.Bool
	for i := range writers {
		qs[i] = make(chan delta, queueSlots)
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			fail(writeShard(ctx, f.journal, w, writers[i], i, qs[i], &ack[i], &ws[i]))
		}()
	}
	last := make([]uint32, p.Runs+1)
	for i := range readers {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			fail(replay(ctx, f.journal, w, readers[i], i, p.Readers, ack, &done, last, &rs[i]))
		}()
	}
	src := produce(ctx, p, w, qs)
	writersWG.Wait()
	done.Store(true)
	readersWG.Wait()
	end := time.Now()
	runtime.ReadMemStats(&mem1)
	audit, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	inv, e := f.inventory(audit)
	if e != nil {
		fail(e)
	}
	acks, commits, reads, pages := w.counts(), w.counts(), w.counts(), w.counts()
	var queue, db, delay, visible histogram
	var largest int
	var lastAckNS, overlapReaders int64
	for i := range ws {
		a := &ws[i]
		acks.join(a.acks)
		lastAckNS = max(lastAckNS, a.lastAckNS)
		commits.join(a.commits)
		queue.join(&a.queued)
		db.join(&a.database)
		delay.join(&a.delay)
		largest = max(largest, a.largest)
	}
	for i := range rs {
		a := &rs[i]
		if a.firstVerifiedNS > 0 && a.firstVerifiedNS < lastAckNS {
			overlapReaders++
		}
		reads.join(a.reads)
		pages.join(a.pages)
		visible.join(&a.delay)
	}
	measuredReaders := readersOverlappingMeasurement(ws, rs)
	valid := failure == "" && measuredReaders == int64(p.Readers) && src.accepted.total > 0 && inv.coversRuns(p.Runs) && inv.HeadSum == src.accepted.total && inv.JournalRecords == inv.HeadSum && inv.HeadSum == reads.total && reads.total == acks.total && inv.Batches == commits.total && inv.Positions == inv.Batches
	for i := 1; i <= p.Runs; i++ {
		expected := src.accepted.total / int64(p.Runs)
		if int64(i) <= src.accepted.total%int64(p.Runs) {
			expected++
		}
		if int64(last[i]) != expected {
			valid = false
		}
	}
	seconds := make([]second, p.Seconds)
	for i := range seconds {
		seconds[i] = second{i + 1, p.Rate, src.offered.bySecond[i], src.accepted.bySecond[i], acks.bySecond[i], reads.bySecond[i], commits.bySecond[i], pages.bySecond[i]}
	}
	rates := map[string]float64{"offered_unique": float64(sum(src.offered.bySecond)) / float64(p.Seconds), "admitted": float64(sum(src.accepted.bySecond)) / float64(p.Seconds), "acknowledged": float64(sum(acks.bySecond)) / float64(p.Seconds), "verified_replayed": float64(sum(reads.bySecond)) / float64(p.Seconds)}
	r := report{Format: "studio.stream-journal-poc.v1", Profile: p, Started: w.origin.UTC(), Environment: environment{runtime.GOOS, runtime.GOARCH, runtime.Version(), os.Getenv("PROCESSOR_IDENTIFIER"), runtime.NumCPU(), runtime.GOMAXPROCS(0), mem0.HeapAlloc, mem1.HeapAlloc, mem1.NumGC - mem0.NumGC, f.settings},
		Limits:  map[string]int{"batch_events": maxBatch, "flush_ms": int(flush / time.Millisecond), "source_tick_ms": int(tick / time.Millisecond), "queue_slots_per_shard": queueSlots, "batches_per_page": maxPage, "packed_record_bytes": recordBytes, "delta_text_bytes": 16},
		Samples: seconds, Total: map[string]int64{"offered_unique": src.offered.total, "enqueue_attempts": src.accepted.total + src.blocked, "admitted": src.accepted.total, "acknowledged": acks.total, "verified_replayed": reads.total, "readers_verified_before_last_commit": overlapReaders, "readers_overlapping_commits_in_measurement": measuredReaders, "queue_full_ticks": src.blocked, "unissued_or_unadmitted_slots": int64(p.Rate*(p.Warm+p.Seconds)) - src.accepted.total},
		Rates: rates, WarmAndMeasuredTarget: int64(p.Rate * (p.Warm + p.Seconds)), PeakQueues: src.peaks, LargestBatch: largest, Latency: map[string]distribution{"offered_to_transaction_start": queue.result(), "batch_transaction_to_commit_ack": db.result(), "offered_to_commit_ack": delay.result(), "offered_to_confirmed_replay": visible.result()}, Inventory: inv, DrainMs: max(int64(0), end.Sub(w.origin.Add(w.end)).Milliseconds()), Correct: valid,
		Target: valid && rates["admitted"] >= float64(p.Rate) && rates["acknowledged"] >= float64(p.Rate) && rates["verified_replayed"] >= float64(p.Rate), Failure: failure}
	if e = saveReport(path, r); e != nil {
		t.Fatal("could not write report:", e)
	}
	t.Logf("logical_runs=%d window_s=%d admitted/s=%.1f ack/s=%.1f verified/s=%.1f ack_p95_us=%d target_met=%t", p.Runs, p.Seconds, rates["admitted"], rates["acknowledged"], rates["verified_replayed"], delay.result().P95, r.Target)
	if !valid {
		t.Fatalf("integrity or execution failure (%s); inspect aggregate report", failure)
	}
}
