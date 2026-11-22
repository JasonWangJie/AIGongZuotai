package studio

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// Install the backlog when the initial empty startup drain commits. The
// recovery transaction then sees a full queue on its first business save.
// Sequences survive rollbacks and count actual attempted status transitions.
func testRecoveryPressure(t *testing.T, pendingRows int, resourceTable string) {
	t.Helper()
	s, database, conn := outboxStore(t)
	s.close()
	runs := 0
	if resourceTable == "studio_runs" {
		runs = 1
	}
	recoverySeedRows(t, conn, runs, 1, 0, "live")
	ctx := context.Background()
	_, err := conn.Exec(ctx, fmt.Sprintf(`CREATE SEQUENCE pressure_activations;
CREATE SEQUENCE pressure_save_attempts MAXVALUE 2;
CREATE FUNCTION pressure_fill() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF nextval('pressure_activations') = 1 THEN
  INSERT INTO studio_outbox_events(event_id,event_key,workspace_id,project_id,project_seq,kind,resource_type,resource_id,resource_version,status)
  SELECT 'evt_pressure_'||i,'pressure:'||i,'ws_demo','prj_recovery_'||lpad((i%%32)::text,2,'0'),
   row_number() OVER(PARTITION BY i%%32 ORDER BY i),'task.updated','task','task_recovery_000001',6,'QUEUED'
  FROM generate_series(1,%d) i ORDER BY i;
  INSERT INTO studio_event_heads(workspace_id,project_id,last_seq)
   SELECT workspace_id,project_id,max(project_seq) FROM studio_outbox_events GROUP BY workspace_id,project_id;
  NEW.pending := %d;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pressure_at_initial_drain BEFORE UPDATE OF retained ON studio_outbox_limits
 FOR EACH ROW WHEN (OLD.pending=0 AND NEW.pending=0) EXECUTE FUNCTION pressure_fill();
CREATE FUNCTION pressure_count_save() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM nextval('pressure_save_attempts');
 RETURN NEW;
END $$;
CREATE TRIGGER pressure_on_save BEFORE UPDATE OF document ON %s FOR EACH ROW
 WHEN ((OLD.document->>'status') IS DISTINCT FROM (NEW.document->>'status')) EXECUTE FUNCTION pressure_count_save();`, pendingRows, outboxPendingLimit, resourceTable))
	if err != nil {
		t.Fatal(err)
	}
	cfg, media, chatCalls := recoveryFixtureConfig(t, database)
	app, err := New(cfg)
	if pendingRows == 0 {
		if app != nil {
			app.Close()
			t.Fatal("startup accepted a full counter without pending events")
		}
		if err == nil || err.Error() != "recovery outbox reported full with no pending events to project" {
			t.Fatal("startup did not stop at its first unsuccessful drain", err)
		}
		if outboxCount(t, conn, "SELECT last_value FROM pressure_save_attempts") != 1 ||
			outboxCount(t, conn, `SELECT count(*) FROM studio_tasks WHERE document->>'status'='SUBMITTING' AND document->>'version'='7'`) != 1 ||
			outboxCount(t, conn, "SELECT count(*) FROM studio_outbox_events") != 0 {
			t.Fatal("rejected recovery retried without progress or committed partial state")
		}
		// Correct the artificial counter, then recover the original record.
		if _, err = conn.Exec(ctx, "UPDATE studio_outbox_limits SET pending=0 WHERE singleton"); err != nil {
			t.Fatal(err)
		}
		app, err = New(cfg)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	recoveryAssertRows(t, conn, runs, 1)
	if outboxCount(t, conn, "SELECT last_value FROM pressure_save_attempts") != 2 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_outbox_events WHERE event_key LIKE 'pressure:%' AND projected_at IS NOT NULL") != pendingRows ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_project_event_feed") != pendingRows+runs+1 ||
		media.calls.Load() != 0 || chatCalls.Load() != 0 {
		t.Fatal("recovery did not project the original queue and commit the retried save exactly once")
	}
}

func TestPostgresRecoveryOutboxRejectionRequiresProgress(t *testing.T) {
	testRecoveryPressure(t, 0, "studio_tasks")
}

func TestPostgresRecoveryFullOutboxDrainsAndRetries(t *testing.T) {
	testRecoveryPressure(t, outboxPendingLimit, "studio_runs")
}

func TestPostgresRecoveryAcrossPagesProviderModeChange(t *testing.T) {
	s, database, conn := outboxStore(t)
	s.close()
	// These tasks were all accepted in demo mode. Only the new process
	// switches to live; a persisted task's original provider mode is frozen.
	recoverySeedRows(t, conn, 0, recoveryMediaPageRecords+1, 0, "demo")
	id := fmt.Sprintf("task_recovery_%06d", recoveryMediaPageRecords+1)
	_, err := conn.Exec(context.Background(), `UPDATE studio_tasks SET
 document=jsonb_set(document,'{status}','"RUNNING"'),
 handle='{"ID":"original-mode-handle","Kind":"image","Count":1}',
 submission='{"kind":"image","count":1,"body":"b3JpZ2luYWw=","idempotency_key":"original-key"}'
 WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	before := recoveryFaultReadTask(t, conn, id)
	cfg, media, chatCalls := recoveryFixtureConfig(t, database)
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	after := recoveryFaultReadTask(t, conn, id)
	projection, err := loadMediaScheduleSQL(context.Background(), conn, id)
	if err != nil || projection == nil || projection.Active || projection.ProviderMode != "demo" {
		t.Fatal("parked task lost its original scheduling scope", err)
	}
	if app.recoveryStats.TaskPages < 2 || after.Task.Status != "RECONCILE_REQUIRED" ||
		after.Task.Error == nil || after.Task.Error.Code != "PROVIDER_MODE_CHANGED" ||
		after.Task.ProviderMode != "demo" || after.Task.Version != before.Task.Version+1 ||
		!reflect.DeepEqual(before.Handle, after.Handle) || !reflect.DeepEqual(before.Submission, after.Submission) ||
		!reflect.DeepEqual(before.Input, after.Input) || media.calls.Load() != 0 || chatCalls.Load() != 0 {
		t.Fatal("second page failed to park the old provider mode with its original request and handle")
	}
}
