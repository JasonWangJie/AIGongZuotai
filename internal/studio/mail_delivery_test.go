package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func mailTestStore(t *testing.T) (*postgresStore, string) {
	t.Helper()
	database := testDatabase(t)
	s, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s, database
}

func enqueueTestMail(t *testing.T, m *mailDelivery, e EmailEnvelope) encryptedMail {
	t.Helper()
	item, err := m.seal(e)
	if err != nil {
		t.Fatal(err)
	}
	err = m.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = enqueueMailSQL(ctx, tx, item); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func mailState(t *testing.T, m *mailDelivery, id string) (string, int, int) {
	t.Helper()
	var state string
	var attempts, size int
	err := m.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT state,attempts,octet_length(ciphertext) FROM studio_mail_outbox WHERE id=$1", id).Scan(&state, &attempts, &size)
	})
	if err != nil {
		t.Fatal(err)
	}
	return state, attempts, size
}

func awaitMailState(t *testing.T, m *mailDelivery, id, want string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		state, _, _ := mailState(t, m, id)
		if state == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _, _ := mailState(t, m, id)
	t.Fatalf("mail state=%s want=%s", state, want)
}

func runMailTestWorker(t *testing.T, m *mailDelivery) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("mail worker failed to stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestPostgresMailRestartParksUnconfirmedAndKeyMismatchDoesNotPoison(t *testing.T) {
	s, database := mailTestStore(t)
	f := newMailSMTPFixture(t, mailSMTPOptions{})
	m, err := newMailDelivery(f.Config, s)
	if err != nil {
		t.Fatal(err)
	}
	m.sender = f.sender()
	item := enqueueTestMail(t, m, testMailEnvelope())
	claimed, attempt, found, err := m.claim()
	if err != nil || !found || attempt != 1 {
		t.Fatalf("claim failed found=%v attempt=%d err=%v", found, attempt, err)
	}
	if bytes.Contains(claimed.Ciphertext, []byte(testMailEnvelope().Token)) || bytes.Contains(claimed.Ciphertext, []byte(testMailEnvelope().To)) {
		t.Fatal("outbox stored plaintext")
	}
	// Simulate a process ending after durable SENDING, without any known reply.
	s.close()
	reopened, err := openPostgres(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.close)
	m, err = newMailDelivery(f.Config, reopened)
	if err != nil {
		t.Fatal(err)
	}
	m.sender = f.sender()
	if err = m.prepare(); err != nil {
		t.Fatal(err)
	}
	state, attempts, _ := mailState(t, m, item.ID)
	if state != "SUBMIT_UNKNOWN" || attempts != 1 {
		t.Fatalf("restart state=%s attempts=%d", state, attempts)
	}
	if _, _, found, err = m.claim(); err != nil || found {
		t.Fatal("restart reclaimed an uncertain SMTP submission")
	}
	if f.DataCalls.Load() != 0 {
		t.Fatal("unknown SMTP was resent")
	}
	e := testMailEnvelope()
	e.ID = "mail_pending_key"
	enqueueTestMail(t, m, e)
	badConfig := f.Config
	badConfig.OutboxKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("z", 32)))
	wrong, err := newMailDelivery(badConfig, reopened)
	if err != nil {
		t.Fatal(err)
	}
	err = wrong.prepare()
	var op *operationError
	if !errors.As(err, &op) || op.code != "MAIL_KEY_MISMATCH" || reopened.failed.Load() {
		t.Fatalf("key mismatch was not a recoverable configuration error: %T", err)
	}
	state, _, size := mailState(t, m, e.ID)
	if state != "PENDING" || size == 0 {
		t.Fatal("wrong key destroyed pending encrypted mail")
	}
}

func TestPostgresMailSMTPAcceptedUnknownAndExpiry(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "accepted"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := mailTestStore(t)
			f := newMailSMTPFixture(t, mailSMTPOptions{DropAfterData: unknown})
			m, err := newMailDelivery(f.Config, s)
			if err != nil {
				t.Fatal(err)
			}
			m.sender = f.sender()
			item := enqueueTestMail(t, m, testMailEnvelope())
			stop := runMailTestWorker(t, m)
			want := "SENT"
			if unknown {
				want = "SUBMIT_UNKNOWN"
			}
			awaitMailState(t, m, item.ID, want)
			stop()
			_, attempts, size := mailState(t, m, item.ID)
			if attempts != 1 || f.DataCalls.Load() != 1 {
				t.Fatal("worker did not deliver exactly one attempt")
			}
			if !unknown && size != 0 {
				t.Fatal("accepted mail retained encrypted token")
			}
			if unknown {
				if _, _, found, err := m.claim(); err != nil || found {
					t.Fatal("uncertain mail automatically retried")
				}
				err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
					_, err := conn.Exec(ctx, "UPDATE studio_mail_outbox SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", item.ID)
					if err != nil {
						return err
					}
					return maintainMailSQL(ctx, conn)
				})
				if err != nil {
					t.Fatal(err)
				}
				state, _, size := mailState(t, m, item.ID)
				if state != "EXPIRED" || size != 0 {
					t.Fatal("expired uncertain mail retained a usable encrypted payload")
				}
			}
		})
	}
}

type mailSequenceSender struct {
	calls      atomic.Int32
	active     atomic.Int32
	maxActive  atomic.Int32
	firstRetry bool
	mu         sync.Mutex
	tokens     map[string]string
}

func (s *mailSequenceSender) Send(ctx context.Context, item encryptedMail, p mailPayload) mailSendResult {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for old := s.maxActive.Load(); n > old; old = s.maxActive.Load() {
		if s.maxActive.CompareAndSwap(old, n) {
			break
		}
	}
	s.calls.Add(1)
	s.mu.Lock()
	prior, exists := s.tokens[item.ID]
	s.tokens[item.ID] = p.Token
	s.mu.Unlock()
	if exists && prior != p.Token {
		return mailSendResult{"FAILED", "TEST_TOKEN_CHANGED"}
	}
	select {
	case <-ctx.Done():
		return mailSendResult{"RETRY", "SMTP_CONNECTION_UNAVAILABLE"}
	case <-time.After(20 * time.Millisecond):
	}
	if s.firstRetry && !exists {
		return mailSendResult{"RETRY", "SMTP_TEMPORARY_REJECTION"}
	}
	return mailSendResult{"SENT", ""}
}

func TestPostgresMailRetryBudgetConcurrencyAndAtomicQueueCapacity(t *testing.T) {
	s, _ := mailTestStore(t)
	f := newMailSMTPFixture(t, mailSMTPOptions{})
	c := f.Config
	c.Workers = 2
	m, err := newMailDelivery(c, s)
	if err != nil {
		t.Fatal(err)
	}
	sender := &mailSequenceSender{firstRetry: true, tokens: make(map[string]string)}
	m.sender = sender
	for _, id := range []string{"mail_retry_a", "mail_retry_b"} {
		e := testMailEnvelope()
		e.ID = id
		enqueueTestMail(t, m, e)
	}
	stop := runMailTestWorker(t, m)
	for _, id := range []string{"mail_retry_a", "mail_retry_b"} {
		awaitMailState(t, m, id, "RETRY")
	}
	stop()
	if sender.maxActive.Load() > 2 || sender.maxActive.Load() < 1 {
		t.Fatal("worker concurrency was not bounded")
	}
	err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "UPDATE studio_mail_outbox SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE state='RETRY'")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stop = runMailTestWorker(t, m)
	for _, id := range []string{"mail_retry_a", "mail_retry_b"} {
		awaitMailState(t, m, id, "SENT")
		_, attempts, size := mailState(t, m, id)
		if attempts != 2 || size != 0 {
			t.Fatal("safe retry did not retain original envelope then clear it")
		}
	}
	stop()
	if sender.calls.Load() != 4 {
		t.Fatalf("unexpected network calls=%d", sender.calls.Load())
	}
	e := testMailEnvelope()
	e.ID = "mail_attempt_limit"
	item := enqueueTestMail(t, m, e)
	err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "UPDATE studio_mail_outbox SET state='SENDING',attempts=5 WHERE id=$1", item.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.finish(item, 5, mailSendResult{"RETRY", "SMTP_TEMPORARY_REJECTION"}); err != nil {
		t.Fatal(err)
	}
	state, _, size := mailState(t, m, item.ID)
	if state != "FAILED" || size != 0 {
		t.Fatal("retry attempts did not terminate")
	}
	// Queue admission is transactional and rejects without poisoning the DB.
	err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `INSERT INTO studio_mail_outbox(id,purpose,key_id,ciphertext,expires_at,state) SELECT 'mail_full_'||n,'reset_password',$1,$2,clock_timestamp()+interval '30 minutes','PENDING' FROM generate_series(1,10000)n`, item.KeyID, item.Ciphertext)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.ID = "mail_rejected"
	rejected, err := m.seal(e)
	if err != nil {
		t.Fatal(err)
	}
	err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = enqueueMailSQL(ctx, tx, rejected); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	var op *operationError
	if !errors.As(err, &op) || op.code != "MAIL_BACKLOG_FULL" || s.failed.Load() {
		t.Fatal("mail queue capacity rejection poisoned storage")
	}
	var exists bool
	err = s.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_mail_outbox WHERE id=$1)", rejected.ID).Scan(&exists)
	})
	if err != nil || exists {
		t.Fatal("rejected mail was committed")
	}
}
