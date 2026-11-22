package studio

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	maxRunDeltaBatchItems = 32
	maxRunDeltaBatchBytes = 256 << 10
)

type runDeltaAppend struct {
	Lease    runLease
	Expected uint64
	Delta    string
	At       time.Time
}

type runDeltaBatchStats struct {
	CommittedBatches, AppendedEvents, SoloAttempts uint64
}

// Every retained index gets either a result from a confirmed batch or the
// original one-Run transaction. Never call the latter after a COMMIT error.
func (s *postgresStore) appendRunDeltaBatch(items []runDeltaAppend) ([]error, runDeltaBatchStats) {
	results := make([]error, len(items))
	var stats runDeltaBatchStats
	candidates := batchDeltaCandidates(items, results)
	if len(candidates) == 0 {
		return results, stats
	}
	solo := candidates
	if len(candidates) > 1 {
		var fast []batchDeltaRow
		var rolledBack bool
		err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			if err := s.lockRunWriterSQL(ctx, tx, ""); err != nil {
				return err
			}
			heads, err := lockBatchDeltaHeads(ctx, tx, items, candidates)
			if err != nil {
				return err
			}
			solo = nil
			for _, ix := range candidates {
				head, ok := heads[items[ix].Lease.RunID]
				if !ok {
					// Missing and currently locked rows use the original adjudication.
					solo = append(solo, ix)
					continue
				}
				trim, fallback, err := head.classify(s.runGeneration, items[ix])
				switch {
				case err != nil:
					results[ix] = err
				case fallback:
					solo = append(solo, ix)
				default:
					fast = append(fast, batchDeltaRow{ix, trim})
				}
			}
			if len(fast) == 0 {
				return tx.Rollback(ctx)
			}
			if err := writeBatchDeltaRows(ctx, tx, items, fast); err != nil {
				// A server-reported transaction/lock conflict is distinct from
				// a broken connection. Reuse the original path only after the
				// server acknowledges rollback; never retry the COMMIT below.
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03") {
					if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
						return rollbackErr
					}
					rolledBack = true
					for _, row := range fast {
						solo = append(solo, row.index)
					}
					return nil
				}
				return err
			}
			expired, err := batchDeltaExpired(ctx, tx, items, fast)
			if err != nil {
				return err
			}
			if len(expired) > 0 {
				// The deadline is checked after all potentially blocking writes.
				// Only a successful, explicit rollback permits a healthy sibling
				// to use its original append below.
				if err := tx.Rollback(ctx); err != nil {
					return err
				}
				rolledBack = true
				for _, row := range fast {
					if expired[items[row.index].Lease.RunID] {
						results[row.index] = runLeaseLost()
					} else {
						solo = append(solo, row.index)
					}
				}
				return nil
			}
			return tx.Commit(ctx)
		})
		if err != nil {
			for _, ix := range candidates {
				if results[ix] == nil {
					results[ix] = err
				}
			}
			return results, stats
		}
		if len(fast) > 0 && !rolledBack {
			stats.CommittedBatches = 1
			stats.AppendedEvents = uint64(len(fast))
		}
	}
	for _, ix := range solo {
		if s.failed.Load() {
			results[ix] = failure(503, "DATABASE_UNAVAILABLE", "数据库不可用，服务已停止执行")
			continue
		}
		item := items[ix]
		stats.SoloAttempts++
		results[ix] = s.appendLeasedRunDelta(item.Lease, item.Expected, item.Delta, item.At)
	}
	return results, stats
}

func batchDeltaCandidates(items []runDeltaAppend, results []error) []int {
	counts := make(map[string]int)
	var total int
	if len(items) <= maxRunDeltaBatchItems {
		for _, item := range items {
			counts[item.Lease.RunID]++
			if len(item.Delta) > maxRunDeltaBatchBytes-total {
				total = maxRunDeltaBatchBytes + 1
				break
			}
			total += len(item.Delta)
		}
	}
	var candidates []int
	for ix, item := range items {
		switch {
		case len(items) > maxRunDeltaBatchItems || total > maxRunDeltaBatchBytes:
			results[ix] = failure(422, "RUN_DELTA_BATCH_LIMIT", "运行增量批次超过上限")
		case counts[item.Lease.RunID] > 1:
			results[ix] = runStreamConflict()
		case item.Delta == "" || len(item.Delta) > maxRunOutputBytes || !utf8.ValidString(item.Delta) || strings.IndexByte(item.Delta, 0) >= 0:
			results[ix] = failure(422, "INVALID_STREAM_DELTA", "运行增量无效或超过输出上限")
		case item.Lease.RunID == "" || item.Lease.Owner == "" || item.Lease.ExecutionID == "" || item.Lease.Generation == "" || item.Lease.Epoch == 0 || item.Lease.Epoch > 1<<63-1:
			results[ix] = runLeaseLost()
		default:
			candidates = append(candidates, ix)
		}
	}
	return candidates
}

type batchDeltaHead struct {
	runStreamHead
	owner, generation string
	epoch             uint64
	live              bool
}

func (h batchDeltaHead) classify(generation string, item runDeltaAppend) (trim uint64, solo bool, err error) {
	l := item.Lease
	if !h.live || !h.leaseRequired || h.closed || h.status != "RUNNING" || h.owner != l.Owner || h.epoch != l.Epoch ||
		h.execution != l.ExecutionID || h.generation != l.Generation || l.Generation != generation {
		return 0, false, runLeaseLost()
	}
	if h.moderated || item.Expected < h.floor || item.Expected > h.head {
		return 0, false, runStreamConflict()
	}
	if item.Expected < h.head {
		return 0, true, nil
	} // Run the original exact-payload replay check.
	if h.output+len(item.Delta) > maxRunOutputBytes {
		return 0, false, failure(422, "RUN_OUTPUT_LIMIT", "运行输出超出上限")
	}
	next := h.head + 1
	if next > 9007199254740991 {
		return 0, false, runStreamConflict()
	}
	if h.retained+len(item.Delta) > runStreamByteLimit {
		return 0, true, nil
	}
	if next <= runStreamRetention || next-runStreamRetention <= h.floor {
		return 0, false, nil
	}
	floor := next - runStreamRetention
	if floor > h.snapshot || floor != h.floor+1 {
		return 0, true, nil
	} // Needs materializing the previous prefix.
	return floor, false, nil
}

// All paths lock the business row before its stream head. C collation imposes
// a common order across runs; SKIP LOCKED keeps one busy run out of this write
// group. Its original single-run operation starts only after these locks exit.
func lockBatchDeltaHeads(ctx context.Context, tx pgx.Tx, items []runDeltaAppend, indices []int) (map[string]batchDeltaHead, error) {
	ids := make([]string, 0, len(indices))
	for _, ix := range indices {
		ids = append(ids, items[ix].Lease.RunID)
	}
	rows, err := tx.Query(ctx, `SELECT id FROM studio_runs WHERE id=ANY($1::text[]) ORDER BY id COLLATE "C" FOR UPDATE SKIP LOCKED`, ids)
	if err != nil {
		return nil, err
	}
	ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	heads := make(map[string]batchDeltaHead, len(ids))
	if len(ids) == 0 {
		return heads, nil
	}
	rows, err = tx.Query(ctx, `SELECT h.run_id,r.document->>'status',h.execution_id,h.lease_owner,h.lease_epoch,h.lease_generation,
		COALESCE(h.lease_until>clock_timestamp(),false),h.head_seq,h.floor_seq,h.snapshot_seq,h.retained_bytes,h.output_bytes,
		h.closed,h.moderated,h.lease_required FROM studio_run_streams h JOIN studio_runs r ON r.id=h.run_id
		WHERE h.run_id=ANY($1::text[]) ORDER BY h.run_id COLLATE "C" FOR UPDATE OF h SKIP LOCKED`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var h batchDeltaHead
		if err := rows.Scan(&id, &h.status, &h.execution, &h.owner, &h.epoch, &h.generation, &h.live, &h.head, &h.floor, &h.snapshot,
			&h.retained, &h.output, &h.closed, &h.moderated, &h.leaseRequired); err != nil {
			return nil, err
		}
		heads[id] = h
	}
	return heads, rows.Err()
}

type batchDeltaRow struct {
	index int
	trim  uint64
}

func writeBatchDeltaRows(ctx context.Context, tx pgx.Tx, items []runDeltaAppend, fast []batchDeltaRow) error {
	var ids, executions, deltas []string
	var seqs, trims []int64
	var times []time.Time
	for _, row := range fast {
		item := items[row.index]
		ids = append(ids, item.Lease.RunID)
		executions = append(executions, item.Lease.ExecutionID)
		deltas = append(deltas, item.Delta)
		seqs = append(seqs, int64(item.Expected+1))
		trims = append(trims, int64(row.trim))
		times = append(times, item.At)
	}
	tag, err := tx.Exec(ctx, `WITH incoming AS MATERIALIZED (
		SELECT * FROM unnest($1::text[],$2::bigint[],$3::text[],$4::text[],$5::timestamptz[],$6::bigint[])
			AS i(id,seq,execution,delta,emitted,trim_seq)
	), trimmed AS (
		DELETE FROM studio_run_stream_events e USING incoming i WHERE i.trim_seq>0 AND e.run_id=i.id AND e.seq=i.trim_seq
			RETURNING e.run_id,e.byte_size
	), added AS (
		INSERT INTO studio_run_stream_events(run_id,seq,execution_id,name,delta,byte_size,emitted_at)
			SELECT id,seq,execution,'message.delta',delta,octet_length(delta),emitted FROM incoming ORDER BY id COLLATE "C"
			RETURNING run_id,seq,byte_size
	)
	UPDATE studio_run_streams h SET head_seq=a.seq,floor_seq=GREATEST(h.floor_seq,i.trim_seq),
		retained_bytes=h.retained_bytes+a.byte_size-COALESCE(t.byte_size,0),output_bytes=h.output_bytes+a.byte_size
	FROM incoming i JOIN added a ON a.run_id=i.id LEFT JOIN trimmed t ON t.run_id=i.id
	WHERE h.run_id=i.id AND (i.trim_seq=0 OR t.run_id IS NOT NULL)`, ids, seqs, executions, deltas, times, trims)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(fast)) {
		return errors.New("run delta batch watermark mismatch")
	}
	return nil
}

// Both control generation and every selected business/head row are still
// locked. Only the database clock can invalidate these authorized leases now.
func batchDeltaExpired(ctx context.Context, tx pgx.Tx, items []runDeltaAppend, fast []batchDeltaRow) (map[string]bool, error) {
	ids := make([]string, 0, len(fast))
	for _, row := range fast {
		ids = append(ids, items[row.index].Lease.RunID)
	}
	rows, err := tx.Query(ctx, `WITH cutoff AS MATERIALIZED (SELECT clock_timestamp() AS at)
		SELECT run_id FROM studio_run_streams,cutoff WHERE run_id=ANY($1::text[]) AND (lease_until IS NULL OR lease_until<=cutoff.at)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var invalid map[string]bool
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		if invalid == nil {
			invalid = make(map[string]bool)
		}
		invalid[id] = true
	}
	return invalid, rows.Err()
}
