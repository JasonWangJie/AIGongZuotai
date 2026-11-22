package studio

import (
	"context"
	"net"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type mixedGCWindow struct {
	Cycles        uint32 `json:"completed_cycles"`
	PauseTotalNS  uint64 `json:"pause_total_ns"`
	PauseSamples  uint32 `json:"pause_samples"`
	MaxObservedNS uint64 `json:"max_observed_pause_ns"`
	RingTruncated bool   `json:"pause_ring_truncated"`
}

type mixedGCObserver struct {
	started bool
	cycles  uint32
	total   uint64
}

func (g *mixedGCObserver) next(m *runtime.MemStats) mixedGCWindow {
	var out mixedGCWindow
	if g.started {
		out.Cycles = m.NumGC - g.cycles
		out.PauseTotalNS = m.PauseTotalNs - g.total
		out.PauseSamples = min(out.Cycles, uint32(len(m.PauseNs)))
		out.RingTruncated = out.Cycles > out.PauseSamples
		for offset := range out.PauseSamples {
			i := (m.NumGC - 1 - offset) % uint32(len(m.PauseNs))
			out.MaxObservedNS = max(out.MaxObservedNS, m.PauseNs[i])
		}
	}
	g.started, g.cycles, g.total = true, m.NumGC, m.PauseTotalNs
	return out
}

type mixedPGActivity struct {
	Registered int            `json:"registered"`
	Observed   int            `json:"observed"`
	States     map[string]int `json:"state_counts"`
	WaitTypes  map[string]int `json:"wait_event_type_counts"`
}

// The maps only contain nonzero counts from these closed vocabularies. A
// future PG version or an unavailable field cannot add an arbitrary label.
func mixedPGState(raw string) string {
	switch raw {
	case "active", "idle", "idle in transaction", "idle in transaction (aborted)", "fastpath function call", "disabled":
		return raw
	case "":
		return "unreported"
	default:
		return "other"
	}
}

func mixedPGWaitType(raw string) string {
	switch raw {
	case "Activity", "BufferPin", "Client", "Extension", "IO", "IPC", "LWLock", "Lock", "Timeout", "InjectionPoint":
		return raw
	case "":
		return "none"
	default:
		return "other"
	}
}

type mixedPGRegistry struct {
	tag    string
	pids   []int32
	starts []time.Time
}

func mixedLoopbackDatabase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return false
	}
	// pgx allows query parameters to override the authority or select a
	// service; preserve the loopback guarantee for the direct go test too.
	for name := range u.Query() {
		switch strings.ToLower(name) {
		case "host", "hostaddr", "service", "servicefile":
			return false
		}
	}
	host := u.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func mixedTaggedDSN(t *testing.T, original, tag string) string {
	t.Helper()
	u, err := url.Parse(original)
	if err != nil || u.Host == "" {
		t.Fatal("cannot tag fixture database configuration")
	}
	q := u.Query()
	q.Set("application_name", tag)
	u.RawQuery = q.Encode()
	return u.String()
}

const mixedOwnedSession = `a.datname=current_database() AND a.usename=current_user
 AND a.backend_type='client backend' AND a.application_name=$1`

const mixedOwnedActivitySQL = `SELECT coalesce(a.state,''),coalesce(a.wait_event_type,'')
 FROM pg_stat_activity a JOIN unnest($2::int[],$3::timestamptz[]) AS own(pid,started)
 ON own.pid=a.pid AND own.started=a.backend_start WHERE ` + mixedOwnedSession

func registerMixedSessions(t *testing.T, admin *pgx.Conn, tag string, sessions ...*pgx.Conn) mixedPGRegistry {
	t.Helper()
	if tag == "" || len(sessions) == 0 {
		t.Fatal("fixture observation lacks a registered session")
	}
	var ids []int32
	for _, conn := range sessions {
		ids = append(ids, int32(conn.PgConn().PID()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := admin.Query(ctx, "SELECT a.pid,a.backend_start FROM pg_stat_activity a WHERE "+mixedOwnedSession+" AND a.pid=ANY($2::int[]) ORDER BY a.pid", tag, ids)
	if err != nil {
		t.Fatal("could not register fixture database sessions")
	}
	defer rows.Close()
	r := mixedPGRegistry{tag: tag}
	for rows.Next() {
		var pid int32
		var since time.Time
		if err := rows.Scan(&pid, &since); err != nil || since.IsZero() {
			t.Fatal("invalid fixture session identity")
		}
		r.pids, r.starts = append(r.pids, pid), append(r.starts, since)
	}
	if rows.Err() != nil || len(r.pids) != len(ids) {
		t.Fatal("fixture session registration did not match all owned backends")
	}
	return r
}

func (r *mixedPGRegistry) activity(t *testing.T, admin *pgx.Conn) mixedPGActivity {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := admin.Query(ctx, mixedOwnedActivitySQL, r.tag, r.pids, r.starts)
	if err != nil {
		t.Fatal("fixture PG activity read failed")
	}
	defer rows.Close()
	out := mixedPGActivity{Registered: len(r.pids), States: map[string]int{}, WaitTypes: map[string]int{}}
	for rows.Next() {
		var state, wait string
		if err := rows.Scan(&state, &wait); err != nil {
			t.Fatal("invalid fixture PG activity row")
		}
		out.Observed++
		out.States[mixedPGState(state)]++
		out.WaitTypes[mixedPGWaitType(wait)]++
	}
	if rows.Err() != nil || out.Observed != out.Registered {
		t.Fatal("fixture database session identity changed or disappeared")
	}
	return out
}

type mixedObservation struct {
	app      *App
	gc       mixedGCObserver
	registry mixedPGRegistry
}

func (ob *mixedObservation) managedActivity(t *testing.T, app *App, admin *pgx.Conn) mixedPGActivity {
	t.Helper()
	if ob.app != app {
		tag := app.store.conn.Config().RuntimeParams["application_name"]
		sessions := append([]*pgx.Conn{app.store.conn}, app.taskLeases.all...)
		ob.registry = registerMixedSessions(t, admin, tag, sessions...)
		ob.app = app
	}
	return ob.registry.activity(t, admin)
}

type mixedLockObservation struct {
	Waiting               mixedPGActivity `json:"waiter_while_blocked"`
	Released              mixedPGActivity `json:"waiter_after_release"`
	OwnBlockerVerified    bool            `json:"own_blocker_verified"`
	WaiterStatementMS     float64         `json:"waiter_statement_elapsed_ms"`
	StaleIdentityExcluded bool            `json:"stale_backend_identity_excluded"`
}

// Separate, test-only sessions, after the last workload sample. PostgreSQL
// must itself identify this blocker as the reason the waiter cannot proceed.
func mixedControlledLock(t *testing.T, admin *pgx.Conn, dsn string) mixedLockObservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	tag := newID("mixedcontrol")
	dsn = mixedTaggedDSN(t, dsn, tag)
	open := func() *pgx.Conn {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal("could not open owned lock probe")
		}
		return conn
	}
	closeConn := func(c *pgx.Conn) {
		finish, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = c.Close(finish)
	}
	blocker := open()
	defer closeConn(blocker)
	waiter := open()
	defer closeConn(waiter)
	blockerRegistry := registerMixedSessions(t, admin, tag, blocker)
	waiterRegistry := registerMixedSessions(t, admin, tag, waiter)
	// Same PID and private tag, but not the registered server incarnation.
	var out mixedLockObservation
	err := admin.QueryRow(ctx, "SELECT NOT EXISTS ("+mixedOwnedActivitySQL+")", tag, waiterRegistry.pids,
		[]time.Time{waiterRegistry.starts[0].Add(time.Microsecond)}).Scan(&out.StaleIdentityExcluded)
	if err != nil || !out.StaleIdentityExcluded {
		t.Fatal("fixture activity query accepted a stale backend identity")
	}
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal("could not start owned lock transaction")
	}
	var acquired bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(172,pg_backend_pid())").Scan(&acquired); err != nil || !acquired {
		t.Fatal("could not install fixture lock")
	}
	finished := make(chan struct{})
	var waitErr error
	var elapsed time.Duration
	go func() {
		defer close(finished)
		started := time.Now()
		_, waitErr = waiter.Exec(ctx, "SELECT pg_advisory_xact_lock(172,$1::int)", blockerRegistry.pids[0])
		elapsed = time.Since(started)
	}()
	defer func() {
		cancel()
		finish, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = tx.Rollback(finish)
		<-finished // pgx observes cancellation before its connection is closed.
	}()
	for ctx.Err() == nil {
		activity := waiterRegistry.activity(t, admin)
		if activity.States["active"] == 1 && activity.WaitTypes["Lock"] == 1 {
			blockerRegistry.activity(t, admin)
			err = admin.QueryRow(ctx, "SELECT $1::int=ANY(pg_blocking_pids($2::int))", blockerRegistry.pids[0], waiterRegistry.pids[0]).Scan(&out.OwnBlockerVerified)
			if err != nil || !out.OwnBlockerVerified {
				t.Fatal("wait was not caused by the registered fixture blocker")
			}
			out.Waiting = activity
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !out.OwnBlockerVerified || tx.Rollback(ctx) != nil {
		t.Fatal("fixture lock was not observed and released within the deadline")
	}
	select {
	case <-finished:
		if waitErr != nil {
			t.Fatal("owned lock waiter failed after release")
		}
		out.WaiterStatementMS = float64(elapsed) / float64(time.Millisecond)
	case <-ctx.Done():
		t.Fatal("owned lock waiter did not exit after release")
	}
	for ctx.Err() == nil {
		out.Released = waiterRegistry.activity(t, admin)
		if out.Released.States["idle"] == 1 && out.Released.WaitTypes["Lock"] == 0 {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned waiter did not return to idle")
	return out
}

type mixedFanoutObservation struct {
	Capacity        int    `json:"subscriber_buffer_capacity"`
	Published       int    `json:"events_published"`
	NormalDelivered int    `json:"normal_delivered_in_order"`
	SlowBuffered    int    `json:"slow_buffered_before_disconnect"`
	SlowEvictions   uint64 `json:"slow_buffer_full_evictions"`
}

// Exercise the actual bounded hot reader and publisher on an isolated record.
// The stalled reader keeps no socket open and performs no database replay.
func mixedControlledFanout(t *testing.T, a *App) mixedFanoutObservation {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	record := &runRecord{Run: Run{Status: "RUNNING"}, Subscribers: map[chan event]bool{}}
	normal, slow := &runStreamReader{record: record}, &runStreamReader{record: record}
	a.armRunReaderLocked(normal)
	a.armRunReaderLocked(slow)
	before := a.obs.slowConsumers.Load()
	result := mixedFanoutObservation{Capacity: cap(slow.events)}
	for range result.Capacity + 2 {
		record.Run.Seq++
		a.publishRunLocked(record, "message.delta", struct{}{})
		result.Published++
		select {
		case item, ok := <-normal.events:
			if !ok || item.Seq != record.Run.Seq {
				t.Fatal("healthy hot reader lost event ordering")
			}
			result.NormalDelivered++
		default:
			t.Fatal("healthy hot reader did not receive event")
		}
	}
	result.SlowEvictions = a.obs.slowConsumers.Load() - before
	if !record.Subscribers[normal.events] || record.Subscribers[slow.events] || len(record.Subscribers) != 1 || result.SlowEvictions != 1 {
		t.Fatal("stalled reader did not disconnect exactly once while healthy reader stayed attached")
	}
	for range result.Capacity + 1 {
		select {
		case item, ok := <-slow.events:
			if !ok {
				if result.SlowBuffered != result.Capacity {
					t.Fatal("slow reader disconnected before its configured capacity")
				}
				return result
			}
			result.SlowBuffered++
			if item.Seq != uint64(result.SlowBuffered) {
				t.Fatal("slow reader lost a previously buffered event")
			}
		default:
			t.Fatal("saturated reader channel remained open")
		}
	}
	t.Fatal("slow reader exceeded its configured capacity")
	return result
}
