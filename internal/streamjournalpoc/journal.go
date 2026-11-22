// Package streamjournalpoc is an isolated measurement of synthetic, already-public
// deltas. The production server has no dependency on it.
package streamjournalpoc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const maxBatch = 256
const maxPage = 4
const recordBytes = 36
const queueSlots = 1024

var errFence = errors.New("stream epoch or sequence conflict")

// The payload includes sixteen actual ASCII text bytes, deterministically
// derived from the logical Run and sequence. Timestamp is workload metadata.
type delta struct {
	run, epoch, seq uint32
	offeredNS       int64
	text            [16]byte
}

func newDelta(run, epoch, seq uint32, ns int64) delta {
	d := delta{run: run, epoch: epoch, seq: seq, offeredNS: ns}
	var b [8]byte
	binary.BigEndian.PutUint32(b[:4], run)
	binary.BigEndian.PutUint32(b[4:], seq)
	hex.Encode(d.text[:], b[:])
	return d
}
func encode(records []delta) []byte {
	raw := make([]byte, len(records)*recordBytes)
	for i, r := range records {
		b := raw[i*recordBytes:]
		binary.BigEndian.PutUint32(b, r.run)
		binary.BigEndian.PutUint32(b[4:], r.epoch)
		binary.BigEndian.PutUint32(b[8:], r.seq)
		binary.BigEndian.PutUint64(b[12:], uint64(r.offeredNS))
		copy(b[20:36], r.text[:])
	}
	return raw
}
func decode(raw []byte, n int, visit func(delta) error) error {
	if n < 1 || n > maxBatch || len(raw) != n*recordBytes {
		return errors.New("invalid journal frame")
	}
	for len(raw) > 0 {
		d := newDelta(binary.BigEndian.Uint32(raw), binary.BigEndian.Uint32(raw[4:]), binary.BigEndian.Uint32(raw[8:]), int64(binary.BigEndian.Uint64(raw[12:])))
		if d.run == 0 || d.epoch == 0 || d.seq == 0 || d.offeredNS < 0 || string(d.text[:]) != string(raw[20:36]) {
			return errors.New("invalid synthetic text or identity")
		}
		if e := visit(d); e != nil {
			return e
		}
		raw = raw[recordBytes:]
	}
	return nil
}

type layout struct{ runs, shards int }

func (g layout) shard(run uint32) int { return (int(run) - 1) % g.shards }

type updates struct{ ids, epochs, previous, last []int32 }

func (g layout) prepare(shard int, batch []delta) (updates, error) {
	var u updates
	if len(batch) < 1 || len(batch) > maxBatch || shard < 0 || shard >= g.shards {
		return u, errFence
	}
	slots := make(map[uint32]int, len(batch))
	for _, d := range batch {
		if d.run < 1 || int(d.run) > g.runs || g.shard(d.run) != shard || d.epoch == 0 || d.epoch > 1<<31-1 || d.seq == 0 || d.seq > 1<<31-1 || d != newDelta(d.run, d.epoch, d.seq, d.offeredNS) || d.offeredNS < 0 {
			return updates{}, errFence
		}
		i, ok := slots[d.run]
		if ok {
			if u.epochs[i] != int32(d.epoch) || u.last[i]+1 != int32(d.seq) {
				return updates{}, errFence
			}
			u.last[i] = int32(d.seq)
		} else {
			slots[d.run] = len(u.ids)
			u.ids = append(u.ids, int32(d.run))
			u.epochs = append(u.epochs, int32(d.epoch))
			u.previous = append(u.previous, int32(d.seq-1))
			u.last = append(u.last, int32(d.seq))
		}
	}
	return u, nil
}

// Every appender and epoch rotation takes the same shard row before the Run
// heads. A failed CAS rolls back all heads, the batch and the position.
func (g layout) stage(ctx context.Context, tx pgx.Tx, shard int, batch []delta) (int64, error) {
	u, e := g.prepare(shard, batch)
	if e != nil {
		return 0, e
	}
	var pos int64
	e = tx.QueryRow(ctx, "UPDATE shards SET pos=pos+1 WHERE id=$1 RETURNING pos", shard).Scan(&pos)
	if e != nil {
		return 0, e
	}
	tag, e := tx.Exec(ctx, `UPDATE heads h SET seq=s.last FROM unnest($1::int[],$2::int[],$3::int[],$4::int[]) AS s(id,epoch,previous,last)
 WHERE h.id=s.id AND h.shard=$5 AND h.epoch=s.epoch AND h.seq=s.previous`, u.ids, u.epochs, u.previous, u.last, shard)
	if e != nil {
		return 0, e
	}
	if tag.RowsAffected() != int64(len(u.ids)) {
		return 0, errFence
	}
	_, e = tx.Exec(ctx, "INSERT INTO batches(shard,pos,n,body) VALUES($1,$2,$3,$4)", shard, pos, len(batch), encode(batch))
	return pos, e
}
func (g layout) append(ctx context.Context, c *pgx.Conn, shard int, batch []delta) (pos int64, err error) {
	err = pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error { var e error; pos, e = g.stage(ctx, tx, shard, batch); return e })
	if err != nil {
		return 0, err
	}
	return pos, nil
}

// Resolve a lost application ACK by reading the exact original bytes at the
// expected position. A missing or different record supplies no confirmation.
func (g layout) confirmed(ctx context.Context, c *pgx.Conn, shard int, pos int64, records []delta) (bool, error) {
	if _, err := g.prepare(shard, records); err != nil || pos < 1 {
		return false, errFence
	}
	var raw []byte
	err := c.QueryRow(ctx, "SELECT body FROM batches WHERE shard=$1 AND pos=$2", shard, pos).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && bytes.Equal(raw, encode(records)), err
}

func (g layout) rotate(ctx context.Context, c *pgx.Conn, run int, epoch int) error {
	return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SELECT id FROM shards WHERE id=$1 FOR UPDATE", g.shard(uint32(run))); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, "UPDATE heads SET epoch=epoch+1 WHERE id=$1 AND epoch=$2", run, epoch)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return errFence
		}
		return nil
	})
}

// Internal readers audit all Runs of one shard. A Run cursor has a nonzero run
// and can deliver only that Run; positions never expose other Runs' payloads.
type cursor struct {
	dataset string
	shard   int
	run     uint32
	after   int64
}
type journal struct {
	layout
	dataset string
}

func (j journal) read(ctx context.Context, c *pgx.Conn, from cursor, shard int, run uint32, acknowledged int64, visit func(delta) error) (cursor, error) {
	if from.dataset != j.dataset || from.shard != shard || from.run != run || shard < 0 || shard >= j.shards || int(run) > j.runs || run != 0 && j.shard(run) != shard || from.after < 0 || from.after > acknowledged {
		return from, errors.New("cursor scope or watermark mismatch")
	}
	if from.after == acknowledged {
		return from, nil
	}
	rows, e := c.Query(ctx, "SELECT pos,n,body FROM batches WHERE shard=$1 AND pos>$2 AND pos<=$3 ORDER BY pos LIMIT $4", shard, from.after, acknowledged, maxPage)
	if e != nil {
		return from, e
	}
	defer rows.Close()
	next := from
	for rows.Next() {
		var pos int64
		var n int
		var body []byte
		if e = rows.Scan(&pos, &n, &body); e != nil {
			return from, e
		}
		if pos != next.after+1 {
			return from, fmt.Errorf("missing committed position in shard %d", shard)
		}
		e = decode(body, n, func(d delta) error {
			if int(d.run) > j.runs || j.shard(d.run) != shard {
				return errors.New("record is outside replay scope")
			}
			if run == 0 || d.run == run {
				return visit(d)
			}
			return nil
		})
		if e != nil {
			return from, e
		}
		next.after = pos
	}
	if e = rows.Err(); e != nil {
		return from, e
	}
	if next == from {
		return from, errors.New("acknowledged page missing")
	}
	return next, nil
}
