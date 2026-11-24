package studio

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type childObservedLease258 struct {
	owner, execution string
	epoch            int64
	expires          time.Time
}

// Both snapshots cover the entire original set while upstream responses are
// held open. Each refill must come from the same fenced execution. A lease
// replaced by a new owner or a later claim cannot count as a renewal.
func verifyChildRenewal258(t *testing.T, ctx context.Context, db *pgx.Conn, turns []childConversation, generation string) int {
	t.Helper()
	ids := make([]string, len(turns))
	for i, turn := range turns {
		ids[i] = turn.run.ID
	}
	read := func() map[string]childObservedLease258 {
		t.Helper()
		rows, err := db.Query(ctx, `SELECT run_id,lease_owner,execution_id,lease_epoch,lease_until
 FROM studio_run_streams WHERE run_id=ANY($1::text[]) AND lease_required AND NOT closed
 AND lease_owner<>'' AND execution_id<>'' AND lease_epoch>0 AND lease_generation=$2 AND lease_until>clock_timestamp()`, ids, generation)
		if err != nil {
			capacityDBFailure(t, "renewal_snapshot", err)
		}
		defer rows.Close()
		result := make(map[string]childObservedLease258, len(turns))
		for rows.Next() {
			var id string
			var lease childObservedLease258
			if err := rows.Scan(&id, &lease.owner, &lease.execution, &lease.epoch, &lease.expires); err != nil {
				capacityDBFailure(t, "renewal_read", err)
			}
			result[id] = lease
		}
		if err := rows.Err(); err != nil {
			capacityDBFailure(t, "renewal_rows", err)
		}
		if len(result) != len(ids) {
			t.Fatalf("expected %d live original leases, observed %d", len(ids), len(result))
		}
		return result
	}
	original := read()
	deadline := time.NewTimer(35 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("fixture deadline while verifying original lease renewals")
		case <-deadline.C:
			t.Fatal("not all original leases advanced within one lease duration and observer allowance")
		case <-tick.C:
			renewed := 0
			for id, now := range read() {
				old := original[id]
				if old.owner != now.owner || old.execution != now.execution || old.epoch != now.epoch || now.expires.Before(old.expires) {
					t.Fatal("live execution changed its frozen lease or lost time")
				}
				if now.expires.After(old.expires) {
					renewed++
				}
			}
			if renewed == len(ids) {
				return renewed
			}
		}
	}
}

func TestChatChild1024Topology258(t *testing.T) {
	p := childAcceptanceProfile("integrated-1024")
	f := childFixture{childAcceptanceTopology: p, scopes: make([]capacityScope, p.workspaces)}
	if p.normal != 1024 || p.readers != 1 || p.streamLimit() != 1025 || p.processes != 16 || p.models != 16 || p.workspaces != 17 || !p.integrated {
		t.Fatal("integrated profile lacks distinct models, registered scopes or delayed-reader capacity")
	}
	settings := f.settings(&childAcceptanceGateway{childAcceptanceTopology: p}, "http://127.0.0.1")
	if settings["CHAT_CHILD_PROCESSES"] != "16" || settings["CHAT_CHILD_SLOTS"] != "64" || settings["CHAT_WORKERS"] != "1" ||
		settings["CHAT_WORKSPACE_CONCURRENCY"] != "61" || settings["CHAT_MODEL_CONCURRENCY"] != "64" || settings["ADMISSION_WORKSPACE_RUNS"] != "62" {
		t.Fatal("a production execution or admission budget cannot support the fixture")
	}
	workspaces := make([]int, p.workspaces)
	models := make([]int, p.models)
	pairs := map[[2]int]int{}
	for i := range p.normal {
		w, m := i%p.workspaces, i%p.models
		workspaces[w]++
		models[m]++
		pairs[[2]int{w, m}]++
	}
	if len(pairs) != p.workspaces*p.models || workspaces[0]+1 != 62 || workspaces[len(workspaces)-1] != 60 {
		t.Fatal("last workspace lacks spare execution capacity or first lacks a slow-reader slot")
	}
	for _, n := range models {
		if n != 64 {
			t.Fatal("frozen model occupancy differs")
		}
	}
	for _, n := range workspaces {
		if n != 60 && n != 61 {
			t.Fatal("workspace occupancy differs")
		}
	}
	for _, n := range pairs {
		if n != 3 && n != 4 {
			t.Fatal("workspace-model cohort cannot use the ordinary admission limits")
		}
	}
	if p := childAcceptanceProfile("integrated-512"); p.normal != 512 || p.processes != 8 || p.models != 8 || p.workspaces != 9 {
		t.Fatal("historical 512 profile changed")
	}
}

func TestChatChild1024SlotsAndReap258(t *testing.T) {
	if os.Getenv("STUDIO_CHAT_CHILD_POOL_1024") != "1" {
		t.Skip("set STUDIO_CHAT_CHILD_POOL_1024=1 for a sixteen-process IPC and teardown check")
	}
	// No job is dispatched. This isolates startup, reservation and actual
	// OS process teardown from the separately measured live model requests.
	pool := childTestPool(t, childTestConfig("http://127.0.0.1:1", 16, 64))
	if len(pool.processes) != 16 || pool.liveSlots() != 1024 {
		t.Fatal("actual pool does not expose sixteen 64-slot children")
	}
	owners := map[int]map[int]bool{}
	var slots []*chatChildSlot
	for range 1024 {
		slot := childTestTake(t, pool)
		pid := slot.proc.cmd.Process.Pid
		if owners[pid] == nil {
			owners[pid] = map[int]bool{}
		}
		if owners[pid][slot.index] || slot.index < 0 || slot.index >= 64 || cap(slot.inbox) != 3 {
			t.Fatal("duplicate physical slot or unexpected IPC mailbox budget")
		}
		owners[pid][slot.index] = true
		slots = append(slots, slot)
	}
	if _, ok := pool.reserve(); ok || pool.available() || len(owners) != 16 {
		t.Fatal("pool exceeded its configured execution slots")
	}
	for _, lanes := range owners {
		if len(lanes) != 64 {
			t.Fatal("physical process did not own its full slot range")
		}
	}
	for _, slot := range slots {
		slot.release()
	}
	if !pool.available() {
		t.Fatal("released pool cannot receive reservations")
	}
	pool.close()
	for _, child := range pool.processes {
		select {
		case <-child.done:
			if child.cmd.ProcessState == nil {
				t.Fatal("child has not been waited on")
			}
		default:
			t.Fatal("live child survived pool shutdown")
		}
	}
	if _, ok := pool.reserve(); ok {
		t.Fatal("closed pool admitted a new slot")
	}
	t.Log("16 real child processes reaped, 1024 distinct IPC slots reserved and released, zero model requests")
}
