package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

const streamSessionPageSize = 256

// A session may have several authorized Run and project subscriptions. Only its
// continuing account/session check is shared. Resource access and replay remain
// the responsibility of the original HTTP request.
type streamSessionKey struct {
	id, user string
	hash     [sha256.Size]byte
	version  int64
}

type watchedStreamSession struct {
	key     streamSessionKey
	revoked bool
	readers map[*streamSessionReader]struct{}
}

type streamSessionReader struct {
	cancel context.CancelFunc
}

// Called with the monitor lock held. Cancel invalidates each derived request
// context synchronously; it does not wait for a handler or its network I/O.
func (s *watchedStreamSession) revokeLocked() {
	if s.revoked {
		return
	}
	s.revoked = true
	for reader := range s.readers {
		reader.cancel()
	}
}

type streamSessionMonitor struct {
	ctx             context.Context
	interval        time.Duration
	validate        func(context.Context, []streamSessionKey) ([]bool, error)
	mu              sync.Mutex
	sessions        map[streamSessionKey]*watchedStreamSession
	stopReaders     func()
	validationSlot  chan struct{}
	rounds, checked atomic.Uint64
}

func newStreamSessionMonitor(ctx context.Context, interval time.Duration, validate func(context.Context, []streamSessionKey) ([]bool, error)) *streamSessionMonitor {
	m := &streamSessionMonitor{ctx: ctx, interval: interval, validate: validate, validationSlot: make(chan struct{}, 1), sessions: make(map[streamSessionKey]*watchedStreamSession)}
	m.stopReaders = sync.OnceFunc(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, entry := range m.sessions {
			entry.revokeLocked()
		}
	})
	// Shutdown can proceed even while the monitor is waiting for SQL. run
	// joins the same cancellation before it leaves the App's wait group.
	context.AfterFunc(ctx, m.stopReaders)
	return m
}

func (m *streamSessionMonitor) subscribe(parent context.Context, p *principal) (context.Context, func()) {
	var ctx context.Context
	var cancel context.CancelFunc
	if p != nil && !p.sessionDeadline.IsZero() {
		ctx, cancel = context.WithDeadline(parent, p.sessionDeadline)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	if ctx.Err() != nil || m.ctx.Err() != nil || p == nil || p.User == nil || p.User.ID == "" || p.SessionID == "" || len(p.SessionHash) != sha256.Size {
		cancel()
		return ctx, func() {}
	}
	key := streamSessionKey{id: p.SessionID, user: p.User.ID, hash: [sha256.Size]byte(p.SessionHash), version: p.CredentialVersion}
	m.mu.Lock()
	entry := m.sessions[key]
	if ctx.Err() != nil || m.ctx.Err() != nil || (entry != nil && entry.revoked) {
		m.mu.Unlock()
		cancel()
		return ctx, func() {}
	}
	if entry == nil {
		entry = &watchedStreamSession{key: key, readers: make(map[*streamSessionReader]struct{})}
		m.sessions[key] = entry
	}
	reader := &streamSessionReader{cancel: cancel}
	entry.readers[reader] = struct{}{}
	m.mu.Unlock()
	return ctx, sync.OnceFunc(func() {
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(entry.readers, reader)
		if len(entry.readers) == 0 && m.sessions[key] == entry {
			delete(m.sessions, key)
		}
	})
}

func (m *streamSessionMonitor) run() {
	defer func() {
		m.stopReaders()
		// No new checks start after the loop exits. Join any failed page
		// still unwinding before the App closes its database connections.
		m.validationSlot <- struct{}{}
		<-m.validationSlot
	}()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.check()
		}
	}
}

// Keep one physical validation operation in flight. If the driver is still
// unwinding a timed-out page, the monitor can expire this and subsequent
// rounds on schedule without starting another lookup or leaking goroutines.
func (m *streamSessionMonitor) validatePage(ctx context.Context, keys []streamSessionKey) ([]bool, bool, error) {
	select {
	case m.validationSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-m.validationSlot
		return nil, false, err
	}
	type result struct {
		valid []bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-m.validationSlot }()
		valid, err := m.validate(ctx, keys)
		done <- result{valid, err}
	}()
	select {
	case response := <-done:
		return response.valid, true, response.err
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}
}

func (m *streamSessionMonitor) check() {
	// Every page, including time waiting for the single SQL slot, shares
	// this deadline. Adding sessions does not extend the revocation window.
	ctx, cancel := context.WithTimeout(m.ctx, min(m.interval, 2*time.Second))
	defer cancel()
	m.mu.Lock()
	entries := make([]*watchedStreamSession, 0, len(m.sessions))
	for _, entry := range m.sessions {
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	if len(entries) == 0 {
		return
	}
	// A replacement registered after the snapshot has its own authentication.
	revoke := func(entry *watchedStreamSession) {
		if m.sessions[entry.key] == entry {
			entry.revokeLocked()
		}
	}
	completed := 0
	keys := make([]streamSessionKey, 0, min(len(entries), streamSessionPageSize))
	submitted := 0
	for start := 0; start < len(entries) && ctx.Err() == nil; start += streamSessionPageSize {
		page := entries[start:min(start+streamSessionPageSize, len(entries))]
		keys = keys[:0]
		for _, entry := range page {
			keys = append(keys, entry.key)
		}
		valid, attempted, err := m.validatePage(ctx, keys)
		if attempted {
			submitted += len(keys)
		}
		m.mu.Lock()
		if err != nil || len(valid) != len(page) || ctx.Err() != nil {
			m.mu.Unlock()
			break
		}
		for i, entry := range page {
			if !valid[i] {
				revoke(entry)
			}
		}
		completed += len(page)
		m.mu.Unlock()
	}
	m.mu.Lock()
	for _, entry := range entries[completed:] {
		revoke(entry)
	}
	m.checked.Add(uint64(submitted))
	m.rounds.Add(1)
	m.mu.Unlock()
}

func (s *postgresStore) validateStreamSessions(ctx context.Context, keys []streamSessionKey) ([]bool, error) {
	if len(keys) == 0 || len(keys) > streamSessionPageSize || s.streamAuth == nil {
		return nil, errors.New("invalid session validation batch")
	}
	hashes := make([][]byte, len(keys))
	ids := make([]string, len(keys))
	users := make([]string, len(keys))
	versions := make([]int64, len(keys))
	for i := range keys {
		hashes[i], ids[i], users[i], versions[i] = keys[i].hash[:], keys[i].id, keys[i].user, keys[i].version
	}
	valid := make([]bool, len(keys))
	err := s.withVerifiedSnapshot(ctx, s.streamAuth, func(txCtx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(txCtx, `SELECT k.pos FROM unnest($1::bytea[], $2::text[], $3::text[], $4::bigint[])
 WITH ORDINALITY AS k(hash,id,uid,version,pos)
 JOIN studio_sessions s ON s.token_hash=k.hash AND s.id=k.id AND s.user_id=k.uid AND s.credential_version=k.version
 JOIN studio_users u ON u.id=s.user_id AND u.credential_version=s.credential_version
 LEFT JOIN studio_account_controls g ON g.user_id=s.user_id
 WHERE s.expires_at>clock_timestamp() AND COALESCE(g.state,'OPEN')='OPEN'`, hashes, ids, users, versions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pos int
			if err := rows.Scan(&pos); err != nil {
				return err
			}
			if pos < 1 || pos > len(valid) || valid[pos-1] {
				return errors.New("invalid session validation result")
			}
			valid[pos-1] = true
		}
		return rows.Err()
	})
	return valid, err
}
