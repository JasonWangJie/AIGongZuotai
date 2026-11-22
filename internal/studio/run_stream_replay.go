package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const runStreamPageRows = 32

type runStreamReader struct {
	record *runRecord
	events chan event
}

// Caller holds App.mu. A saturated subscriber's old channel has already
// been closed and removed. Re-register before reading the durable gap.
func (a *App) armRunReaderLocked(reader *runStreamReader) {
	reader.events = nil
	if !terminal(reader.record.Run.Status) {
		reader.events = make(chan event, a.config.SubscriberBuffer)
		reader.record.Subscribers[reader.events] = true
	}
}

type runStreamPage struct {
	events   []event
	high     uint64
	terminal bool
}

func (s *postgresStore) readRunStreamPage(requestCtx context.Context, scope subscriptionScope, id string, after uint64, initialSnapshot bool) (runStreamPage, error) {
	var page runStreamPage
	err := s.withRunReplaySnapshot(requestCtx, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var floor uint64
		var journal, moderated bool
		err := tx.QueryRow(ctx, `SELECT r.document->>'status',s.run_id IS NOT NULL,
 COALESCE(s.moderated,false) OR COALESCE(jsonb_typeof(r.document->'moderation')='object',false),
 COALESCE(s.floor_seq,0),COALESCE(s.head_seq,(r.document->>'seq')::bigint,0)
 FROM studio_runs r LEFT JOIN studio_run_streams s ON s.run_id=r.id
 WHERE r.id=$1 AND r.workspace_id=$2 AND r.project_id=$3`, id, scope.WorkspaceID, scope.ProjectID).
			Scan(&status, &journal, &moderated, &floor, &page.high)
		if errors.Is(err, pgx.ErrNoRows) {
			return failure(404, "NOT_FOUND", "运行不存在")
		}
		if err != nil {
			return err
		}
		if after > page.high {
			return failure(409, "CURSOR_AHEAD", "事件游标超出当前运行，请重新获取快照")
		}
		page.terminal = terminal(status)
		if !journal || moderated || after < floor || page.terminal && after == page.high || initialSnapshot && page.high > 0 {
			var raw []byte
			if err = tx.QueryRow(ctx, "SELECT document FROM studio_runs WHERE id=$1 AND workspace_id=$2 AND project_id=$3", id, scope.WorkspaceID, scope.ProjectID).Scan(&raw); err != nil {
				return err
			}
			var run Run
			if err = json.Unmarshal(raw, &run); err != nil {
				return err
			}
			if journal {
				if _, err = loadRunStreamPrefixSQL(ctx, tx, &run); err != nil {
					return err
				}
			}
			if run.ID != id || run.WorkspaceID != scope.WorkspaceID || run.ProjectID != scope.ProjectID || run.Seq != page.high {
				return errors.New("run stream snapshot does not cover the committed head")
			}
			raw, err = json.Marshal(copyRun(run))
			if err != nil {
				return err
			}
			page.events = []event{{Name: "run.snapshot", Seq: page.high, Data: raw}}
		} else if after < page.high {
			// The complete retained window has a byte limit in addition to this
			// row bound. Ordering and payloads are read at the header snapshot.
			rows, err := tx.Query(ctx, `SELECT seq,name,delta,data FROM studio_run_stream_events
 WHERE run_id=$1 AND seq>$2 AND seq<=$3 ORDER BY seq LIMIT $4`, id, after, page.high, runStreamPageRows)
			if err != nil {
				return err
			}
			expected := after + 1
			payloadBytes := 0
			for rows.Next() {
				var e event
				var delta string
				if err = rows.Scan(&e.Seq, &e.Name, &delta, &e.Data); err != nil {
					rows.Close()
					return err
				}
				if e.Seq != expected {
					rows.Close()
					return errors.New("run stream replay sequence gap")
				}
				expected++
				if e.Name == "message.delta" {
					e.Data, err = json.Marshal(map[string]any{"run_id": id, "seq": e.Seq, "delta": delta})
					if err != nil {
						rows.Close()
						return err
					}
				}
				// Escaping the delta for JSON can increase its transmitted size.
				if payloadBytes+len(e.Data) > runStreamByteLimit {
					break
				}
				payloadBytes += len(e.Data)
				page.events = append(page.events, e)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(page.events) == 0 {
				return errors.New("run stream replay unexpectedly empty")
			}
		}
		return nil
	})
	if err != nil {
		return runStreamPage{}, err
	}
	return page, err
}

// The hot reader is pinned and subscribed before the first snapshot. After
// each bounded PG page, catch up through its committed head before switching
// to live events. Duplicate buffered notifications at the boundary are skipped.
func (a *App) serveDurableRunEvents(w http.ResponseWriter, r *http.Request, reader *runStreamReader, scope subscriptionScope, after uint64, initialSnapshot bool) {
	id := r.PathValue("id")
	started := false
	replay := true
	ticker := time.NewTicker(a.config.Heartbeat)
	defer ticker.Stop()
	stillAuthorized := func() bool { return r.Context().Err() == nil && a.ctx.Err() == nil }
	for stillAuthorized() {
		if replay {
			page, err := a.store.readRunStreamPage(r.Context(), scope, id, after, initialSnapshot && !started)
			if err != nil {
				if !started {
					writeError(w, err)
				}
				return
			}
			if !started {
				if !startSSE(w) {
					return
				}
				started = true
			}
			for _, e := range page.events {
				if !stillAuthorized() || !writeSSE(w, e) {
					return
				}
				after = e.Seq
			}
			if after < page.high {
				continue
			}
			if page.terminal {
				return
			}
			replay = false
		}
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case e, ok := <-reader.events:
			if !ok {
				a.mu.Lock()
				a.armRunReaderLocked(reader)
				a.mu.Unlock()
				replay = true
				continue
			}
			if e.Seq <= after {
				continue
			}
			if e.Seq != after+1 {
				replay = true
				continue
			}
			if !stillAuthorized() || !writeSSE(w, e) {
				return
			}
			after = e.Seq
			if e.Name != "message.delta" {
				return
			}
		case <-ticker.C:
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err := io.WriteString(w, ": heartbeat\n\n")
			if err != nil || controller.Flush() != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}
}
