package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamSessionMonitorDeduplicatesAndRevokesOriginalSession(t *testing.T) {
	hash := sha256.Sum256([]byte("first"))
	p := &principal{User: &User{ID: "owner"}, SessionHash: hash[:], SessionID: "first", CredentialVersion: 2}
	secondHash := sha256.Sum256([]byte("second"))
	q := *p
	q.SessionHash, q.SessionID = secondHash[:], "second"
	m := newStreamSessionMonitor(t.Context(), time.Second, func(_ context.Context, keys []streamSessionKey) ([]bool, error) {
		if len(keys) != 2 {
			t.Fatalf("expected two unique sessions, got %d", len(keys))
		}
		allowed := make([]bool, len(keys))
		for i, k := range keys {
			allowed[i] = k.id == q.SessionID
		}
		return allowed, nil
	})
	first, release1 := m.subscribe(t.Context(), p)
	firstOther, release2 := m.subscribe(t.Context(), p)
	second, release3 := m.subscribe(t.Context(), &q)
	m.check()
	for _, ctx := range []context.Context{first, firstOther} {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("revoked reader stayed active")
		}
	}
	if second.Err() != nil || m.rounds.Load() != 1 || m.checked.Load() != 2 {
		t.Fatal("shared check affected another session or did not record one round")
	}
	late, releaseLate := m.subscribe(t.Context(), p)
	if late.Err() == nil {
		t.Fatal("late registration reused revoked authority")
	}
	release1()
	release2()
	release3()
	releaseLate()
	releaseLate()
	if len(m.sessions) != 0 {
		t.Fatal("final readers retained shared session")
	}
}

func TestStreamSessionMonitorLateFailureCannotRevokeReplacement(t *testing.T) {
	hash := sha256.Sum256([]byte("original"))
	p := &principal{User: &User{ID: "owner"}, SessionID: "session", SessionHash: hash[:]}
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m := newStreamSessionMonitor(t.Context(), time.Second, func(context.Context, []streamSessionKey) ([]bool, error) {
		close(entered)
		<-finish
		return nil, errors.New("old read failed")
	})
	_, drop := m.subscribe(t.Context(), p)
	go func() { defer close(done); m.check() }()
	<-entered
	drop()
	next, release := m.subscribe(t.Context(), p)
	defer release()
	close(finish)
	<-done
	if next.Err() != nil {
		t.Fatal("stale read revoked a newly authenticated registration")
	}
}

func TestStreamSessionMonitorChecksDeadlineAndStops(t *testing.T) {
	life, stop := context.WithCancel(t.Context())
	defer stop()
	var calls atomic.Int32
	m := newStreamSessionMonitor(life, 20*time.Millisecond, func(ctx context.Context, keys []streamSessionKey) ([]bool, error) {
		calls.Add(1)
		<-ctx.Done()
		return []bool{true}, nil // A result arriving beyond the deadline is unusable.
	})
	loop := make(chan struct{})
	go func() { defer close(loop); m.run() }()
	hash := sha256.Sum256([]byte("deadline"))
	reader, release := m.subscribe(life, &principal{User: &User{ID: "owner"}, SessionID: "session", SessionHash: hash[:]})
	select {
	case <-reader.Done():
	case <-time.After(time.Second):
		t.Fatal("stale authority remained open")
	}
	release()
	stop()
	<-loop
	if m.rounds.Load() == 0 || calls.Load() == 0 {
		t.Fatal("failed to exercise session query")
	}
}

func TestExpandedStreamAdmissionConfig(t *testing.T) {
	for _, c := range []Config{
		{Admission: AdmissionLimits{GlobalStreams: 129}},
		{Admission: AdmissionLimits{GlobalStreams: 192}, AuthMode: "required", DatabaseURL: "unused"},
		{Admission: AdmissionLimits{GlobalStreams: 256}, AuthMode: "disabled", DatabaseURL: "unused", RunReplayReadConnections: 1},
	} {
		_, err := New(c)
		if err == nil || err.Error() != "expanded SSE admission requires authentication, PostgreSQL and Run replay readers" {
			t.Fatal("configuration did not fail before connecting")
		}
	}
	for _, n := range []int{1, 128, 192, 256, 257, 10_000, MaxGlobalStreams, MaxGlobalStreams + 1, -1} {
		limits := AdmissionLimits{GlobalStreams: n}
		limits.defaults()
		if (limits.validate() == nil) != (n >= 1 && n <= MaxGlobalStreams) {
			t.Fatalf("unexpected global admission for %d", n)
		}
	}
}

func TestStreamSessionMonitorRevokesEveryReaderBeforeRoundReturns(t *testing.T) {
	// An asynchronous fan-out must not pass just because cancellation
	// goroutines happened to run first on another CPU.
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	hash := sha256.Sum256([]byte("revocation fixture"))
	p := &principal{User: &User{ID: "owner"}, SessionHash: hash[:], SessionID: "session", CredentialVersion: 1}
	m := newStreamSessionMonitor(t.Context(), time.Second, func(_ context.Context, keys []streamSessionKey) ([]bool, error) {
		answer := make([]bool, len(keys))
		for i, k := range keys {
			answer[i] = k.version != p.CredentialVersion
		}
		return answer, nil
	})
	var readers []context.Context
	for range 64 {
		reader, release := m.subscribe(t.Context(), p)
		t.Cleanup(release)
		readers = append(readers, reader)
	}
	other := *p
	other.CredentialVersion++
	unaffected, releaseOther := m.subscribe(t.Context(), &other)
	defer releaseOther()
	m.check()
	for i, reader := range readers {
		if reader.Err() != context.Canceled {
			t.Fatalf("reader %d still authorized after completed negative round", i)
		}
	}
	if m.rounds.Load() != 1 || unaffected.Err() != nil {
		t.Fatal("round accounting or scope differs")
	}
}

func TestStreamSessionMonitorShutdownCancelsReadersWhileValidationIsBusy(t *testing.T) {
	life, stop := context.WithCancel(t.Context())
	defer stop()
	entered, finish, done := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	m := newStreamSessionMonitor(life, 5*time.Millisecond, func(context.Context, []streamSessionKey) ([]bool, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-finish
		return []bool{true}, nil
	})
	hash := sha256.Sum256([]byte("shutdown fixture"))
	p := &principal{User: &User{ID: "owner"}, SessionHash: hash[:], SessionID: "original"}
	request, endRequest := context.WithCancel(t.Context())
	defer endRequest()
	independent, releaseIndependent := m.subscribe(request, p)
	defer releaseIndependent()
	reader, release := m.subscribe(t.Context(), p)
	defer release()
	endRequest()
	if independent.Err() != context.Canceled || reader.Err() != nil {
		t.Fatal("original request cancellation propagated to a sibling")
	}
	go func() { defer close(done); m.run() }()
	defer func() { stop(); close(finish); <-done }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("validation did not start")
	}
	stop()
	select {
	case <-reader.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for the outstanding validation result")
	}
	late, releaseLate := m.subscribe(t.Context(), p)
	defer releaseLate()
	if late.Err() != context.Canceled {
		t.Fatal("admitted a reader after shutdown")
	}
}

func syntheticStreamSession(n int) *principal {
	id := strconv.Itoa(n)
	hash := sha256.Sum256([]byte(id))
	return &principal{User: &User{ID: "owner-" + id}, SessionHash: hash[:], SessionID: id, CredentialVersion: 1}
}

func TestStreamSessionMonitorFullAdmissionPagedPositions(t *testing.T) {
	const total = 10_000
	readers := make(map[string]context.Context, total)
	visited := make(map[string]bool, total)
	pages := 0
	var firstDeadline time.Time
	m := newStreamSessionMonitor(t.Context(), 5*time.Second, func(ctx context.Context, keys []streamSessionKey) ([]bool, error) {
		n := min(total-len(visited), streamSessionPageSize)
		if n == 0 || len(keys) != n {
			t.Fatalf("invalid SQL batch size %d; expected %d", len(keys), n)
		}
		deadline, ok := ctx.Deadline()
		if !ok || pages > 0 && !deadline.Equal(firstDeadline) {
			t.Fatal("page received a fresh deadline")
		}
		firstDeadline = deadline
		pages++
		valid := make([]bool, n)
		for i, k := range keys {
			if _, duplicate := visited[k.id]; duplicate || k.user != "owner-"+k.id || k.version != 1 {
				t.Fatal("duplicated or cross-account reader")
			}
			valid[i] = i != 0 && i != n-1
			visited[k.id] = valid[i]
		}
		return valid, nil
	})
	for i := range total {
		p := syntheticStreamSession(i)
		reader, release := m.subscribe(t.Context(), p)
		readers[p.SessionID] = reader
		t.Cleanup(release)
	}
	m.check()
	if len(visited) != total || pages != 40 || m.checked.Load() != total || m.rounds.Load() != 1 {
		t.Fatal("incomplete ten-thousand-session round")
	}
	for id, reader := range readers {
		if (reader.Err() == nil) != visited[id] {
			t.Fatal("reader authorization applied to the wrong page or position")
		}
	}
}

func TestStreamSessionMonitorFailedPageClosesUnverifiedSuffix(t *testing.T) {
	for _, failure := range []string{"error", "short", "deadline"} {
		t.Run(failure, func(t *testing.T) {
			const count = 2*streamSessionPageSize + 1
			checked := make(map[string]bool)
			started, resume := make(chan struct{}), make(chan struct{})
			var pages int
			period := time.Second
			if failure == "deadline" {
				period = 100 * time.Millisecond
			}
			m := newStreamSessionMonitor(t.Context(), period, func(ctx context.Context, keys []streamSessionKey) ([]bool, error) {
				pages++
				valid := make([]bool, len(keys))
				for i, k := range keys {
					valid[i] = true
					if pages == 1 {
						checked[k.id] = true
					}
				}
				if pages == 1 {
					return valid, nil
				}
				if pages != 2 {
					t.Error("attempted another page after failure")
					return nil, errors.New("unexpected extra page")
				}
				close(started)
				switch failure {
				case "error":
					return nil, errors.New("test failure")
				case "short":
					return valid[1:], nil
				}
				<-ctx.Done()
				<-resume // simulate transport cleanup outliving the SQL deadline
				return valid, nil
			})
			readers := make(map[string]context.Context, count)
			for i := range count {
				p := syntheticStreamSession(i)
				reader, release := m.subscribe(t.Context(), p)
				t.Cleanup(release)
				readers[p.SessionID] = reader
			}
			done := make(chan struct{})
			go func() { defer close(done); m.check() }()
			defer func() { close(resume); <-done }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("second page never started")
			}
			end := time.After(3 * time.Second)
			for id, reader := range readers {
				if checked[id] {
					if reader.Err() != nil {
						t.Fatal("prior valid session lost")
					}
				} else {
					select {
					case <-reader.Done():
					case <-end:
						t.Fatal("unverified session remained authorized")
					}
				}
			}
		})
	}
}

func TestStreamSessionExpiryDoesNotWaitForValidation(t *testing.T) {
	m := newStreamSessionMonitor(t.Context(), 5*time.Second, func(context.Context, []streamSessionKey) ([]bool, error) {
		t.Fatal("should not need a verification tick")
		return nil, nil
	})
	p := syntheticStreamSession(1)
	p.sessionDeadline = time.Now().Add(20 * time.Millisecond)
	one, release := m.subscribe(t.Context(), p)
	defer release()
	select {
	case <-one.Done():
	case <-time.After(time.Second):
		t.Fatal("session outlived its database deadline")
	}
	two, releaseTwo := m.subscribe(t.Context(), p)
	defer releaseTwo()
	if two.Err() == nil {
		t.Fatal("accepted an expired principal")
	}
}

func TestStreamSessionMonitorStuckPageCannotStarveLaterRounds(t *testing.T) {
	life, cancel := context.WithCancel(t.Context())
	entered, unwind := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m := newStreamSessionMonitor(life, 35*time.Millisecond, func(ctx context.Context, keys []streamSessionKey) ([]bool, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-ctx.Done()
		<-unwind
		return []bool{true}, nil
	})
	first, endFirst := m.subscribe(life, syntheticStreamSession(1))
	defer endFirst()
	stopped := make(chan struct{})
	go func() { defer close(stopped); m.run() }()
	defer func() { cancel(); close(unwind); <-stopped }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no SQL lookup started")
	}
	late, endLate := m.subscribe(life, syntheticStreamSession(2))
	defer endLate()
	expire := time.After(2 * time.Second)
	for _, r := range []context.Context{first, late} {
		select {
		case <-r.Done():
		case <-expire:
			t.Fatal("a stale reader survived blocked physical cleanup")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("multiple physical lookups ran during failed cleanup")
	}
}
