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
	rounds, checked atomic.Uint64
}

func newStreamSessionMonitor(ctx context.Context, interval time.Duration, validate func(context.Context, []streamSessionKey) ([]bool, error)) *streamSessionMonitor {
	m := &streamSessionMonitor{ctx: ctx, interval: interval, validate: validate, sessions: make(map[streamSessionKey]*watchedStreamSession)}
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
	ctx, cancel := context.WithCancel(parent)
	if parent.Err() != nil || m.ctx.Err() != nil || p == nil || p.User == nil || p.User.ID == "" || p.SessionID == "" || len(p.SessionHash) != sha256.Size {
		cancel()
		return ctx, func() {}
	}
	key := streamSessionKey{id: p.SessionID, user: p.User.ID, hash: [sha256.Size]byte(p.SessionHash), version: p.CredentialVersion}
	m.mu.Lock()
	entry := m.sessions[key]
	if parent.Err() != nil || m.ctx.Err() != nil || (entry != nil && entry.revoked) {
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
	defer m.stopReaders()
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

func (m *streamSessionMonitor) check() {
	m.mu.Lock()
	entries := make([]*watchedStreamSession, 0, len(m.sessions))
	keys := make([]streamSessionKey, 0, len(m.sessions))
	for key, entry := range m.sessions {
		entries = append(entries, entry)
		keys = append(keys, key)
	}
	m.mu.Unlock()
	if len(keys) == 0 {
		return
	}

	// Waiting for the dedicated connection and the entire read transaction
	// share one deadline. An unavailable database invalidates these streams.
	ctx, cancel := context.WithTimeout(m.ctx, min(m.interval, 2*time.Second))
	valid, err := m.validate(ctx, keys)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, entry := range entries {
		if err == nil && len(valid) == len(entries) && valid[i] {
			continue
		}
		// The last reader may have left during SQL and a new, independently
		// authenticated reader may have registered the same session again.
		if m.sessions[entry.key] == entry {
			entry.revokeLocked()
			// Keep the revoked entry until its final handler releases the slot.
			// Even a just-admitted, stale principal must see this cancellation.
		}
	}
	m.checked.Add(uint64(len(keys)))
	m.rounds.Add(1)
}

func (s *postgresStore) validateStreamSessions(ctx context.Context, keys []streamSessionKey) ([]bool, error) {
	if len(keys) == 0 || len(keys) > 256 || s.streamAuth == nil {
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
 WITH ORDINALITY AS k(hash,id,uid,version)
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
