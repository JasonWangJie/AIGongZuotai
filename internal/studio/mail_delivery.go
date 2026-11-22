package studio

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxMailPending = 10000
const maxMailAttempts = 5

type mailDelivery struct {
	config MailConfig
	store  *postgresStore
	cipher *mailCipher
	sender mailSender
}

func newMailDelivery(c MailConfig, s *postgresStore) (*mailDelivery, error) {
	if err := validateMailConfig(c); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, nil
	}
	if s == nil {
		return nil, errors.New("mail delivery requires PostgreSQL")
	}
	c = c.normalized()
	crypt, err := newMailCipher(c.OutboxKey)
	if err != nil {
		return nil, err
	}
	return &mailDelivery{config: c, store: s, cipher: crypt, sender: newSMTPMailSender(c)}, nil
}

type mailQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func checkMailCapacitySQL(ctx context.Context, q mailQuerier) error {
	var count int
	err := q.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM studio_mail_outbox WHERE state IN('PENDING','SENDING','RETRY','SUBMIT_UNKNOWN') AND expires_at>clock_timestamp() LIMIT 10000) pending`).Scan(&count)
	if err != nil {
		return err
	}
	if count >= maxMailPending {
		return failure(503, "MAIL_BACKLOG_FULL", "安全邮件队列暂不可用，请稍后重试")
	}
	return nil
}

func enqueueMailSQL(ctx context.Context, tx pgx.Tx, item encryptedMail) error {
	if !validMailIdentity(item) || len(item.Ciphertext) == 0 || len(item.Ciphertext) > maxMailPayload+64 || len(item.KeyID) != 32 {
		return failure(500, "MAIL_ENVELOPE_INVALID", "安全邮件无法排队")
	}
	if err := checkMailCapacitySQL(ctx, tx); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO studio_mail_outbox(id,purpose,key_id,ciphertext,expires_at,state) VALUES($1,$2,$3,$4,$5,'PENDING')`, item.ID, item.Purpose, item.KeyID, item.Ciphertext, item.ExpiresAt)
	return err
}

func (m *mailDelivery) checkPendingKey(ctx context.Context, q mailQuerier) error {
	var mismatch bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_mail_outbox WHERE state IN('PENDING','RETRY') AND expires_at>clock_timestamp() AND key_id<>$1)`, m.cipher.keyID).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch {
		return failure(503, "MAIL_KEY_MISMATCH", "待发送邮件使用其他加密密钥；请恢复原密钥后启动")
	}
	return nil
}

// The existing process-wide PostgreSQL writer lock prevents another process
// delivering concurrently. SENDING from an earlier process is never re-sent.
func (m *mailDelivery) prepare() error {
	return m.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = m.checkPendingKey(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE studio_mail_outbox SET state='SUBMIT_UNKNOWN',last_error_code='SMTP_ACCEPTANCE_UNKNOWN',updated_at=clock_timestamp() WHERE state='SENDING'`); err != nil {
			return err
		}
		if err = maintainMailSQL(ctx, tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

func maintainMailSQL(ctx context.Context, tx sqlExecer) error {
	_, err := tx.Exec(ctx, `UPDATE studio_mail_outbox SET state='EXPIRED',ciphertext=''::bytea,last_error_code='MAIL_EXPIRED',updated_at=clock_timestamp() WHERE id IN (SELECT id FROM studio_mail_outbox WHERE expires_at<=clock_timestamp() AND state IN('PENDING','RETRY','SUBMIT_UNKNOWN') ORDER BY expires_at,id LIMIT 1000)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM studio_mail_outbox WHERE id IN (SELECT id FROM studio_mail_outbox WHERE state IN('SENT','FAILED','EXPIRED') AND updated_at<clock_timestamp()-interval '7 days' ORDER BY updated_at,id LIMIT 1000)`)
	return err
}

func (m *mailDelivery) run(ctx context.Context) {
	var workers sync.WaitGroup
	for i := 0; i < m.config.Workers; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); m.worker(ctx) }()
	}
	workers.Wait()
}

func (m *mailDelivery) worker(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastMaintenance := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(lastMaintenance) >= time.Minute {
			if err := m.store.do(func(ctx context.Context, conn *pgx.Conn) error { return maintainMailSQL(ctx, conn) }); err != nil {
				return
			}
			lastMaintenance = time.Now()
		}
		item, attempt, found, err := m.claim()
		if err != nil {
			return
		}
		if !found {
			timer.Reset(time.Second)
			continue
		}
		p, err := m.cipher.open(item)
		result := mailSendResult{"FAILED", "MAIL_PAYLOAD_UNREADABLE"}
		if err == nil {
			if time.Now().Before(item.ExpiresAt) {
				result = m.sender.Send(ctx, item, p)
			} else {
				result = mailSendResult{"EXPIRED", "MAIL_EXPIRED"}
			}
		}
		// Persist an observed SMTP acceptance even during normal shutdown. If DB
		// confirmation is lost, fail closed; next startup parks SENDING unknown.
		if err = m.finish(item, attempt, result); err != nil {
			return
		}
		timer.Reset(0)
	}
}

func (m *mailDelivery) claim() (encryptedMail, int, bool, error) {
	var item encryptedMail
	var attempts int
	err := m.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, `WITH candidate AS (SELECT id FROM studio_mail_outbox WHERE state IN('PENDING','RETRY') AND next_attempt_at<=clock_timestamp() AND expires_at>clock_timestamp() AND attempts<5 ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE studio_mail_outbox m SET state='SENDING',attempts=attempts+1,updated_at=clock_timestamp() FROM candidate c WHERE m.id=c.id RETURNING m.id,m.purpose,m.key_id,m.ciphertext,m.expires_at,m.attempts`).Scan(&item.ID, &item.Purpose, &item.KeyID, &item.Ciphertext, &item.ExpiresAt, &attempts)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return encryptedMail{}, 0, false, nil
	}
	return item, attempts, err == nil, err
}

func mailRetryDelay(attempt int) time.Duration {
	seconds := []int{15, 60, 300, 900}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(seconds) {
		attempt = len(seconds)
	}
	var jitter [1]byte
	_, _ = rand.Read(jitter[:])
	return time.Duration(seconds[attempt-1])*time.Second + time.Duration(jitter[0]%11)*time.Second
}

func (m *mailDelivery) finish(item encryptedMail, attempt int, result mailSendResult) error {
	delay := time.Duration(0)
	if result.State == "RETRY" {
		if attempt >= maxMailAttempts {
			result = mailSendResult{"FAILED", "SMTP_RETRY_EXHAUSTED"}
		} else {
			delay = mailRetryDelay(attempt)
		}
	}
	if result.State != "SENT" && result.State != "FAILED" && result.State != "RETRY" && result.State != "SUBMIT_UNKNOWN" && result.State != "EXPIRED" {
		return failure(500, "MAIL_STATE_INVALID", "安全邮件状态无效")
	}
	return m.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		// Expired retry requests terminate immediately, without waiting for the
		// periodic cleanup. Only the claimed attempt may publish this outcome.
		_, err := conn.Exec(ctx, `UPDATE studio_mail_outbox SET state=CASE WHEN $3='RETRY' AND expires_at<=clock_timestamp()+$5*interval '1 millisecond' THEN 'EXPIRED' ELSE $3 END,last_error_code=CASE WHEN $3='RETRY' AND expires_at<=clock_timestamp()+$5*interval '1 millisecond' THEN 'MAIL_EXPIRED' ELSE $4 END,ciphertext=CASE WHEN $3 IN('SENT','FAILED','EXPIRED') OR ($3='RETRY' AND expires_at<=clock_timestamp()+$5*interval '1 millisecond') THEN ''::bytea ELSE ciphertext END,next_attempt_at=clock_timestamp()+$5*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=$1 AND state='SENDING' AND attempts=$2`, item.ID, attempt, result.State, result.Code, delay.Milliseconds())
		return err
	})
}
