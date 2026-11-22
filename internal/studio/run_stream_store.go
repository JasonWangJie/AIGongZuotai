package studio

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	runStreamRetention = 256
	runStreamByteLimit = 1 << 20
)

type runStreamHead struct {
	execution, status                string
	head, floor, snapshot            uint64
	retained, output                 int
	closed, moderated, leaseRequired bool
}

func runStreamConflict() error {
	return failure(409, "RUN_STREAM_CONFLICT", "原运行的执行状态或流序号已变化")
}

func getRunStreamHead(ctx context.Context, tx pgx.Tx, id string, lock bool) (*runStreamHead, error) {
	const columns = "SELECT execution_id,head_seq,floor_seq,snapshot_seq,retained_bytes,output_bytes,closed,moderated,lease_required,r.status FROM "
	query := columns + "(SELECT id,document->>'status' AS status FROM studio_runs WHERE id=$1) r JOIN studio_run_streams h ON h.run_id=r.id"
	if lock {
		// Ledger operations also lock the business row before the stream row.
		query = columns + "(SELECT id,document->>'status' AS status FROM studio_runs WHERE id=$1 FOR UPDATE) r JOIN studio_run_streams h ON h.run_id=r.id FOR UPDATE OF h"
	}
	var h runStreamHead
	err := tx.QueryRow(ctx, query, id).Scan(&h.execution, &h.head, &h.floor, &h.snapshot, &h.retained, &h.output, &h.closed, &h.moderated, &h.leaseRequired, &h.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &h, err
}

func readRunDocumentSQL(ctx context.Context, tx pgx.Tx, id string) (Run, error) {
	var r Run
	var data []byte
	if err := tx.QueryRow(ctx, "SELECT document FROM studio_runs WHERE id=$1", id).Scan(&data); err != nil {
		return r, err
	}
	err := json.Unmarshal(data, &r)
	return r, err
}

// The document and the bounded suffix must be read from one transaction
// snapshot (or under the stream writer lock).
func loadRunStreamPrefixSQL(ctx context.Context, tx pgx.Tx, r *Run) (bool, error) {
	h, err := getRunStreamHead(ctx, tx, r.ID, false)
	if err != nil || h == nil {
		return false, err
	}
	return true, applyRunStreamPrefix(ctx, tx, r, h)
}

func applyRunStreamPrefix(ctx context.Context, tx pgx.Tx, r *Run, h *runStreamHead) error {
	if r.Seq != h.snapshot || r.Seq > h.head || h.head-r.Seq > runStreamRetention {
		return errors.New("invalid run stream checkpoint")
	}
	if r.Seq < h.head {
		rows, err := tx.Query(ctx, "SELECT seq,name,delta,emitted_at FROM studio_run_stream_events WHERE run_id=$1 AND seq>$2 ORDER BY seq LIMIT $3", r.ID, r.Seq, runStreamRetention)
		if err != nil {
			return err
		}
		defer rows.Close()
		var b strings.Builder
		b.WriteString(r.Output)
		for rows.Next() {
			var seq uint64
			var name, delta string
			if err := rows.Scan(&seq, &name, &delta, &r.UpdatedAt); err != nil {
				return err
			}
			if h.moderated || name != "message.delta" || seq != r.Seq+1 || b.Len()+len(delta) > maxRunOutputBytes {
				return errors.New("invalid run stream suffix")
			}
			b.WriteString(delta)
			r.Seq = seq
		}
		if err = rows.Err(); err != nil {
			return err
		}
		r.Output = b.String()
	}
	if r.Seq != h.head || len(r.Output) != h.output {
		return errors.New("run stream watermark differs from public output")
	}
	return nil
}

func (s *postgresStore) appendLeasedRunDelta(l runLease, expected uint64, delta string, at time.Time) error {
	if delta == "" || !utf8.ValidString(delta) || strings.IndexByte(delta, 0) >= 0 || len(delta) > maxRunOutputBytes {
		return failure(422, "INVALID_STREAM_DELTA", "运行增量无效或超过输出上限")
	}
	id, executionID := l.RunID, l.ExecutionID
	return s.runTx("", func(ctx context.Context, tx pgx.Tx) error {
		h, err := s.lockActiveRunLease(ctx, tx, l)
		if err != nil {
			return err
		}
		if h.moderated || expected > h.head || expected < h.floor {
			return runStreamConflict()
		}
		if expected < h.head {
			var matches bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_run_stream_events WHERE run_id=$1 AND seq=$2 AND execution_id=$3 AND name='message.delta' AND delta=$4)", id, expected+1, executionID, delta).Scan(&matches); err != nil {
				return err
			}
			if !matches {
				return runStreamConflict()
			}
			return l.checkDeadline(ctx, tx)
		}
		if h.output+len(delta) > maxRunOutputBytes {
			return failure(422, "RUN_OUTPUT_LIMIT", "运行输出超出上限")
		}
		if err = pruneRunStreamSQL(ctx, tx, id, h, h.head+1, len(delta), false); err != nil {
			return err
		}
		h.head++
		h.output += len(delta)
		h.retained += len(delta)
		if _, err = tx.Exec(ctx, "INSERT INTO studio_run_stream_events(run_id,seq,execution_id,name,delta,byte_size,emitted_at) VALUES($1,$2,$3,'message.delta',$4,$5,$6)", id, h.head, h.execution, delta, len(delta), at); err != nil {
			return err
		}
		if err = updateRunStreamHead(ctx, tx, id, h); err != nil {
			return err
		}
		return l.checkDeadline(ctx, tx)
	})
}

func updateRunStreamHead(ctx context.Context, tx pgx.Tx, id string, h *runStreamHead) error {
	_, err := tx.Exec(ctx, "UPDATE studio_run_streams SET floor_seq=$2,head_seq=$3,snapshot_seq=$4,retained_bytes=$5,output_bytes=$6,closed=$7 WHERE run_id=$1", id, h.floor, h.head, h.snapshot, h.retained, h.output, h.closed)
	return err
}

// Materialize the previously committed prefix only when pruning would remove
// unapplied deltas. Most appends touch one narrow event and its fixed-size head.
func pruneRunStreamSQL(ctx context.Context, tx pgx.Tx, id string, h *runStreamHead, seq uint64, size int, savingSnapshot bool) error {
	floor := h.floor
	if seq > runStreamRetention {
		floor = max(floor, seq-runStreamRetention)
	}
	if h.retained+size > runStreamByteLimit || seq > h.head+1 {
		floor = seq - 1
	}
	if floor == h.floor {
		return nil
	}
	if floor > h.snapshot && !savingSnapshot {
		r, err := readRunDocumentSQL(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = applyRunStreamPrefix(ctx, tx, &r, h); err != nil {
			return err
		}
		if err = writeRunDocumentSQL(ctx, tx, r); err != nil {
			return err
		}
		h.snapshot = h.head
	}
	var removed int
	if err := tx.QueryRow(ctx, `WITH removed AS (DELETE FROM studio_run_stream_events WHERE run_id=$1 AND seq<=$2 RETURNING byte_size) SELECT COALESCE(SUM(byte_size),0) FROM removed`, id, floor).Scan(&removed); err != nil {
		return err
	}
	h.floor = floor
	h.retained -= removed
	return nil
}

// Called in the business save transaction, including offline/operations ledger
// snapshots. A jump without intermediate events establishes a new replay floor.
func saveRunStreamSnapshotSQL(ctx context.Context, tx pgx.Tx, run Run, h *runStreamHead) error {
	if h == nil {
		return nil
	}
	if run.Seq < h.head || h.closed && !terminal(run.Status) || h.moderated != (run.Moderation != nil) {
		return runStreamConflict()
	}
	if h.execution != "" && run.Seq > h.head+1 {
		return runStreamConflict()
	}
	newTerminal := terminal(run.Status) && !h.closed
	if h.execution != "" || h.closed {
		if newTerminal && run.Seq != h.head+1 || !terminal(run.Status) && run.Seq != h.head {
			return runStreamConflict()
		}
		prior, err := readRunDocumentSQL(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		if err = applyRunStreamPrefix(ctx, tx, &prior, h); err != nil {
			return err
		}
		if (!h.moderated || h.closed) && prior.Output != run.Output || h.closed && prior.Status != run.Status {
			return runStreamConflict()
		}
		// A billing correction to a terminal Run may advance its cursor, but
		// neither it nor a same-sequence checkpoint can approve new content.
		if h.moderated && !newTerminal && (prior.Moderation == nil || *prior.Moderation != *run.Moderation) {
			return runStreamConflict()
		}
	}
	if run.Seq > h.head {
		name := "run.snapshot"
		var payload any = copyRun(run)
		if newTerminal && !h.moderated && run.Seq == h.head+1 && len(run.Output) == h.output {
			name = map[string]string{"COMPLETED": "run.completed", "CANCELLED": "run.cancelled", "FAILED": "run.failed", "INTERRUPTED": "run.interrupted"}[run.Status]
			payload = map[string]any{"run_id": run.ID, "seq": run.Seq, "status": run.Status}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if len(data) > runStreamByteLimit {
			return failure(422, "RUN_SNAPSHOT_LIMIT", "运行快照超出上限")
		}
		if err = pruneRunStreamSQL(ctx, tx, run.ID, h, run.Seq, len(data), true); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_run_stream_events(run_id,seq,execution_id,name,data,byte_size,emitted_at) VALUES($1,$2,$3,$4,$5,$6,$7)", run.ID, run.Seq, h.execution, name, data, len(data), run.UpdatedAt); err != nil {
			return err
		}
		h.retained += len(data)
	}
	h.head, h.snapshot, h.output, h.closed = run.Seq, run.Seq, len(run.Output), terminal(run.Status)
	return updateRunStreamHead(ctx, tx, run.ID, h)
}
