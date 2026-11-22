package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"runtime"
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
	for _, n := range []int{1, 128, 192, 256, 257, -1} {
		limits := AdmissionLimits{GlobalStreams: n}
		limits.defaults()
		if (limits.validate() == nil) != (n >= 1 && n <= 256) {
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
