package studio

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	controlWriterLock       = int64(0x53545544494F)
	maxRunReplayConnections = 4
	runReplayPageTimeout    = 5 * time.Second
)

// A slot is either an idle connection or permission to reconnect one lost reader.
// A pool belongs to exactly one read workload. No control operation borrows its slots.
type runReplayPool struct {
	config                                  *pgx.ConnConfig
	slots                                   chan *pgx.Conn
	ctx                                     context.Context
	cancel                                  context.CancelFunc
	closeOnce                               sync.Once
	database, schema, storageID, generation string
	writerPID                               int32
}

func (s *postgresStore) openRunReplayPool(parent context.Context, size int) error {
	var err error
	s.replay, err = s.newVerifiedReadPool(parent, size)
	return err
}

func (s *postgresStore) newVerifiedReadPool(parent context.Context, size int) (*runReplayPool, error) {
	ctx, cancel := context.WithCancel(parent)
	p := &runReplayPool{slots: make(chan *pgx.Conn, size), ctx: ctx, cancel: cancel, generation: s.runGeneration}
	for range size {
		p.slots <- nil
	}
	err := s.do(func(ctx context.Context, c *pgx.Conn) error {
		p.config = c.Config()
		// The pgx copy includes the control connection's notification callback.
		// Let each reader initialize its own callback.
		p.config.OnNotification = nil
		// The original DATABASE_URL can require target_session_attrs=read-write.
		// Validate that primary first, then make this session read-only before
		// pgx exposes it to callers.
		afterConnect := p.config.AfterConnect
		p.config.AfterConnect = func(ctx context.Context, c *pgconn.PgConn) error {
			if afterConnect != nil {
				if err := afterConnect(ctx, c); err != nil {
					return err
				}
			}
			_, err := c.Exec(ctx, "SET default_transaction_read_only = on").ReadAll()
			return err
		}
		p.writerPID = int32(c.PgConn().PID())
		return c.QueryRow(ctx, `SELECT current_database(),current_schema(),storage_id::text
 FROM studio_storage_identity WHERE singleton`).Scan(&p.database, &p.schema, &p.storageID)
	})
	if err == nil {
		// Test one physical read-only connection before exposing the App. Other
		// slots connect lazily, with the same verification on every new snapshot.
		err = p.borrow(ctx, func(ctx context.Context, conn *pgx.Conn) error {
			return pgx.BeginTxFunc(ctx, conn, replayTxOptions(), func(tx pgx.Tx) error {
				return p.verify(ctx, tx)
			})
		})
	}
	if err != nil {
		p.close()
		return nil, errors.New("PostgreSQL reader could not verify the control database")
	}
	return p, nil
}

func replayTxOptions() pgx.TxOptions {
	return pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
}

// A restored clone can have the original storage UUID. Also require the active
// generation, database/schema and the original writer's database-local session lock.
func (p *runReplayPool) verify(ctx context.Context, tx pgx.Tx) error {
	var matches bool
	err := tx.QueryRow(ctx, `SELECT current_database()=$1 AND current_schema()=$2
 AND NOT pg_catalog.pg_is_in_recovery() AND current_setting('transaction_read_only')='on'
 AND EXISTS (SELECT 1 FROM studio_storage_identity WHERE singleton AND storage_id::text=$3)
 AND EXISTS (SELECT 1 FROM studio_run_executor WHERE singleton AND generation=$4)
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_locks l
   WHERE l.locktype='advisory' AND l.granted AND l.mode='ExclusiveLock' AND l.pid=$5
   AND l.database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())
   AND l.classid::bigint=$6 AND l.objid::bigint=$7 AND l.objsubid=1)`,
		p.database, p.schema, p.storageID, p.generation, p.writerPID,
		controlWriterLock>>32, controlWriterLock&0xffffffff).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("Run replay snapshot does not match its control writer")
	}
	return nil
}

func (s *postgresStore) withRunReplaySnapshot(requestCtx context.Context, read func(context.Context, pgx.Tx) error) error {
	return s.withVerifiedSnapshot(requestCtx, s.replay, read)
}

func (s *postgresStore) withVerifiedSnapshot(requestCtx context.Context, pool *runReplayPool, read func(context.Context, pgx.Tx) error) error {
	snapshot := func(ctx context.Context, conn *pgx.Conn) error {
		return pgx.BeginTxFunc(ctx, conn, replayTxOptions(), func(tx pgx.Tx) error {
			if pool != nil {
				if err := pool.verify(ctx, tx); err != nil {
					return err
				}
			}
			return read(ctx, tx)
		})
	}
	if pool == nil {
		return s.do(snapshot)
	}
	if s.failed.Load() {
		return s.stopOnDatabaseFailure()
	}
	err := pool.borrow(requestCtx, snapshot)
	var op *operationError
	switch {
	case s.failed.Load():
		return s.stopOnDatabaseFailure()
	case errors.As(err, &op), err == nil:
		return err
	case transientReplayError(err):
		return replayUnavailable()
	default:
		// In particular: scope/identity mismatch, broken watermark, gap or
		// invalid stored document. Preserve the controller's fail-closed rule.
		return s.stopOnDatabaseFailure()
	}
}

func replayUnavailable() error {
	return failure(503, "RUN_REPLAY_UNAVAILABLE", "运行回放暂时不可用，可保留原游标后重新读取")
}

func transientReplayError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		pgconn.Timeout(err) || pgconn.SafeToRetry(err) {
		return true
	}
	var network *net.OpError
	if errors.As(err, &network) {
		return true
	}
	var database *pgconn.PgError
	if errors.As(err, &database) {
		return strings.HasPrefix(database.Code, "08") || strings.HasPrefix(database.Code, "53") ||
			database.Code == "57P01" || database.Code == "57P02" || database.Code == "57P03" ||
			database.Code == "57014" || database.Code == "55P03" || database.Code == "40001" || database.Code == "40P01"
	}
	return false
}

func (p *runReplayPool) borrow(requestCtx context.Context, use func(context.Context, *pgx.Conn) error) error {
	ctx, cancel := context.WithTimeout(requestCtx, runReplayPageTimeout)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	var conn *pgx.Conn
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return p.ctx.Err()
	case conn = <-p.slots:
	}
	defer func() {
		if conn != nil && (conn.IsClosed() || conn.PgConn().TxStatus() != 'I') {
			// pgx can return from a canceled query before the underlying socket
			// closes. The caller can leave immediately, but the physical slot
			// stays occupied until cleanup completes (including during shutdown).
			go func() {
				closeReplayConnection(conn)
				p.slots <- nil
			}()
			return
		}
		p.slots <- conn
	}()
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn == nil {
		var err error
		conn, err = pgx.ConnectConfig(ctx, p.config)
		if err != nil {
			// A reader that never connected cannot attest corruption of the writer.
			return replayUnavailable()
		}
	}
	return use(ctx, conn)
}

func closeReplayConnection(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = conn.Close(ctx)
	<-conn.PgConn().CleanupDone()
}

func (p *runReplayPool) close() {
	p.closeOnce.Do(func() {
		p.cancel()
		for range cap(p.slots) {
			if conn := <-p.slots; conn != nil {
				closeReplayConnection(conn)
			}
		}
	})
}
