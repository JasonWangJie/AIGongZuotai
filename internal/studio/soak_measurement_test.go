package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type soakProfile256 struct {
	Windows int
	Width   time.Duration
	Pairs   int
}

func soakProfileFor256(mode string) (soakProfile256, bool) {
	switch mode {
	case "pilot":
		return soakProfile256{6, 18 * time.Second, 2}, true
	case "24h":
		return soakProfile256{96, 15 * time.Minute, 30}, true
	default:
		return soakProfile256{}, false
	}
}

func (p soakProfile256) slots(i int) int {
	if i == 1 {
		return 2 * p.Pairs
	}
	return p.Pairs
}
func (p soakProfile256) total() int { return (p.Windows + 1) * p.Pairs }

type soakCounts256 struct {
	Planned, Offered, Missed, MissedDuringStop                             int // pairs; an offered pair has one run and one task
	ChatAccepted, MediaAccepted, ChatFirstDelta, ChatComplete, MediaFailed int
	ChatReplays, MediaReplays, CursorReplays                               int
	UnobservedMediaPeak                                                    int
}

type soakWindow256 struct {
	soakCounts256
	latency map[string][]time.Duration
	misses  []soakMiss256
}

func (w *soakWindow256) add(name string, d time.Duration) {
	if w.latency == nil {
		w.latency = make(map[string][]time.Duration)
	}
	w.latency[name] = append(w.latency[name], d)
}
func (w *soakWindow256) distribution() map[string]capacityDistribution {
	result := make(map[string]capacityDistribution, len(w.latency))
	for k, s := range w.latency {
		result[k] = capacityDistributionOf(s)
	}
	return result
}

// Keep no arbitrary child log lines. gctrace emits the two stop-the-world
// phases on either side of the concurrent mark phase, in clock milliseconds.
type soakGC256 struct {
	mu             sync.Mutex
	line           []byte
	discard        bool
	cycles, missed int
	pauseMS        float64
}

func (g *soakGC256) Write(b []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range b {
		if c == '\n' {
			if !g.discard && len(g.line) > 0 {
				g.readLine()
			}
			g.line = g.line[:0]
			g.discard = false
		} else if !g.discard {
			if len(g.line) == 256 || len(g.line) < 3 && c != "gc "[len(g.line)] {
				if len(g.line) == 256 {
					g.missed++
				}
				g.discard = true
				g.line = g.line[:0]
				continue
			}
			g.line = append(g.line, c)
			if c == ',' && bytes.HasSuffix(g.line, []byte(" ms clock,")) {
				g.readLine()
				g.discard = true
				g.line = g.line[:0]
			}
		}
	}
	return len(b), nil
}
func (g *soakGC256) readLine() {
	_, tail, ok := strings.Cut(string(g.line), "%: ")
	clocks, _, end := strings.Cut(tail, " ms clock,")
	parts := strings.Split(clocks, "+")
	if !ok || !end || len(parts) != 3 {
		g.missed++
		return
	}
	a, e := strconv.ParseFloat(parts[0], 64)
	b, e2 := strconv.ParseFloat(parts[2], 64)
	if e != nil || e2 != nil || a < 0 || b < 0 || math.IsNaN(a+b) || math.IsInf(a+b, 0) {
		g.missed++
		return
	}
	g.cycles++
	g.pauseMS += a + b
}

type soakObs256 struct {
	AtMS                                                  int64
	Generation                                            int
	Resources                                             soakResourcePair256
	ServiceGCCycles, ServiceGCUnparsed                    int
	ServiceGCPauseMS                                      float64
	DriverGCCycles                                        uint32
	DriverGCPauseNS                                       uint64
	PGClientConnections, PGActive, PGWaitingOnLock        int
	ActiveChatRuns, ActiveMediaTasks, UncertainMediaTasks int
	OutboxPending, OutboxRetained                         int
}

func (g *soakGC256) observe(t *testing.T, ctx context.Context, start time.Time, gen int, service *childAcceptanceServer, db *pgx.Conn, tag string) soakObs256 {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var o soakObs256
	var err error
	o.Resources, err = soakResourceSample256(service)
	if err != nil {
		t.Fatal("owned process resource sampling failed", err)
	}
	g.mu.Lock()
	o.ServiceGCCycles, o.ServiceGCPauseMS, o.ServiceGCUnparsed = g.cycles, g.pauseMS, g.missed
	g.mu.Unlock()
	var driver runtime.MemStats
	runtime.ReadMemStats(&driver)
	o.DriverGCCycles, o.DriverGCPauseNS = driver.NumGC, driver.PauseTotalNs
	// A fresh application_name is unique to each owned controller generation; this
	// independent observer uses the untagged original schema DSN.
	err = db.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE state='active'),count(*) FILTER(WHERE wait_event_type='Lock') FROM pg_stat_activity a WHERE `+mixedOwnedSession, tag).Scan(&o.PGClientConnections, &o.PGActive, &o.PGWaitingOnLock)
	if err != nil || o.PGClientConnections < 1 {
		t.Fatal("could not observe this controller's PostgreSQL connections")
	}
	err = db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM studio_runs WHERE document->>'status' IN ('QUEUED','RUNNING')),
		count(*) FILTER (WHERE document->>'status' NOT IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED')),
		count(*) FILTER (WHERE document->>'status' IN ('SUBMIT_UNKNOWN','RECONCILE_REQUIRED')) FROM studio_tasks`).Scan(&o.ActiveChatRuns, &o.ActiveMediaTasks, &o.UncertainMediaTasks)
	if err != nil {
		t.Fatal("owned schema workload observation failed")
	}
	err = db.QueryRow(ctx, "SELECT pending,retained FROM studio_outbox_limits WHERE singleton").Scan(&o.OutboxPending, &o.OutboxRetained)
	if err != nil {
		t.Fatal("owned notification queue observation failed")
	}
	o.Generation = gen
	o.AtMS = time.Since(start).Milliseconds()
	return o
}

func soakFileDigest256(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal("owned executable unavailable")
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		t.Fatal("could not hash service executable")
	}
	return hex.EncodeToString(h.Sum(nil))
}

type soakSink256 struct{ file *os.File }

func openSoakSink256(t *testing.T, path string) *soakSink256 {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatal("an absolute new report path is required")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("report destination must be a new writable file")
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error("report close failed")
		}
	})
	return &soakSink256{file: f}
}
func (s *soakSink256) write(t *testing.T, value any) {
	t.Helper()
	if err := json.NewEncoder(s.file).Encode(value); err != nil {
		t.Fatal("aggregate report write failed")
	}
	if err := s.file.Sync(); err != nil {
		t.Fatal("aggregate report sync failed")
	}
}

func TestSoakProfilesAndBoundedDiagnostics256(t *testing.T) {
	for _, profile := range []string{"pilot", "24h"} {
		p, ok := soakProfileFor256(profile)
		if !ok {
			t.Fatal("missing profile")
		}
		n := 0
		for i := range p.Windows {
			n += p.slots(i)
		}
		if n != p.total() || n > 3000 || p.slots(1) != 2*p.Pairs || p.Width/time.Duration(p.slots(1)) < time.Second {
			t.Fatal("profile has an invalid schedule")
		}
		if profile == "24h" && p.Width*time.Duration(p.Windows) != 24*time.Hour {
			t.Fatal("incorrect duration")
		}
	}
	if _, ok := soakProfileFor256("other"); ok {
		t.Fatal("unexpected profile")
	}
	var gc soakGC256
	synthetic := []byte("gc 1 @1.2s 1%: 0.25+1.2+0.75 ms clock, rest\n")
	for _, c := range append(bytes.Repeat([]byte("private log fragment "), 120), '\n') {
		_, _ = gc.Write([]byte{c})
	}
	synthetic = append(synthetic[:len(synthetic)-1], bytes.Repeat([]byte(" long CPU details"), 24)...)
	synthetic = append(synthetic, '\n')
	for _, c := range synthetic {
		_, _ = gc.Write([]byte{c})
	}
	if gc.cycles != 1 || gc.missed != 0 || gc.pauseMS != 1 || len(gc.line) != 0 || cap(gc.line) > 256 {
		t.Fatal("unexpected bounded GC parsing")
	}
	var stats soakWindow256
	stats.add("fixture", time.Millisecond)
	if got := stats.distribution()["fixture"]; got.Samples != 1 || got.P95 != 1 {
		t.Fatal(fmt.Sprint("bad duration sample ", got))
	}
}
