package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestWorkflowRerunPreviewUsesPortsNotEditorOrder(t *testing.T) {
	_, g, _ := workflowStoreVideoGraph(t)
	g.Nodes[0], g.Nodes[3] = g.Nodes[3], g.Nodes[0]
	for i, node := range []string{"write", "draw", "approve", "animate"} {
		p, _, err := workflowAffected(g, node)
		want := []string{"write", "draw", "approve", "animate"}
		if err != nil || !slices.Equal(p.InheritedNodes, want[:i]) || !slices.Equal(p.AffectedNodes, want[i:]) {
			t.Fatal("wrong dependency boundaries", node, p, err)
		}
	}
}

// Register completed sources using ordinary store transactions; only the
// starting parent state is seeded. The fork tests need real FKs, immutable
// content identities and project authorization without issuing provider calls.
func workflowRerunFixture(t *testing.T, s *postgresStore, db *pgx.Conn) (subscriptionScope, WorkflowExecution) {
	t.Helper()
	scope := subscriptionScope{"ws_demo", "prj_demo"}
	_, g, models := workflowStoreVideoGraph(t)
	d, _, err := s.saveWorkflowDefinition(scope, "", 0, g, newID("save"))
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := s.startWorkflow(scope, d.ID, d.Version, "原输入", newID("start"), models, "demo")
	if err != nil {
		t.Fatal(err)
	}
	r := outboxFixtureRun(newID("run"))
	r.Status, r.Output = "COMPLETED", "原运行发布文本"
	if err = s.createRun(r, newID("seed"), payloadHash(r.ID)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := &taskRecord{Task: Task{ID: newID("task"), Kind: "image", WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Status: "SUCCEEDED", Version: 2, ProviderMode: "demo", ModelID: g.Nodes[1].ModelID, ModelVersion: g.Nodes[1].ModelVersion, CreatedAt: now, UpdatedAt: now}, NextPoll: now}
	for _, shade := range []byte{31, 97} {
		body := referencePNG(t, shade)
		digest := sha256.Sum256(body)
		id := newID("art")
		task.Task.Artifacts = append(task.Task.Artifacts, Artifact{ID: id, Kind: "image", URL: "/api/v1/assets/" + id, StorageInfo: &AssetInfo{ID: id, ContentType: "image/png", ByteSize: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}})
	}
	if err = s.createTask(task, newID("seed"), payloadHash(task.Task.ID)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		c, e := workflowCandidatesSQL(ctx, tx, scope, task.Task.ID, false)
		if e != nil {
			return e
		}
		if len(c) != 2 {
			t.Fatal("fixture has no private source pair")
		}
		_, e = tx.Exec(ctx, "UPDATE studio_workflow_executions SET version=7,status='CANCELLED',text_run_id=$2,image_task_id=$3,candidates=$4,selected=$5 WHERE id=$1", x.ID, r.ID, task.Task.ID, c, c[0])
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	x, err = s.workflowExecution(scope, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	return scope, x
}

func TestPostgresWorkflowRerunForksOnlyAffectedNodes(t *testing.T) {
	s, _, db := outboxStore(t)
	scope, x := workflowRerunFixture(t, s, db)
	ctx := context.Background()
	nodes := []string{"write", "draw", "approve", "animate"}
	statuses := []string{"TEXT_READY", "IMAGE_READY", "WAITING_APPROVAL", "VIDEO_READY"}
	intents := []int{3, 2, 1, 1}
	children := outboxCount(t, db, "SELECT (SELECT count(*) FROM studio_tasks)+(SELECT count(*) FROM studio_runs)")
	for i, node := range nodes {
		p, err := s.workflowRerunPreview(scope, x.ID, node)
		if err != nil || p.SourceVersion != x.Version || !slices.Equal(p.AffectedNodes, nodes[i:]) || !slices.Equal(p.InheritedNodes, nodes[:i]) {
			t.Fatal("incorrect preview", node, p, err)
		}
		in := workflowRerunInput{ExpectedVersion: x.Version, FromNode: node}
		prompt, input := "新起点指令", "新运行输入"
		if i != 2 {
			in.Prompt = &prompt
		}
		if i == 0 {
			in.Input = &input
		}
		key := newID("fork")
		next, replay, err := s.rerunWorkflow(scope, x.ID, key, in)
		if err != nil || replay || next.ID == x.ID || next.SourceWorkflowID != x.ID || next.SourceVersion != x.Version || next.FromNode != node || next.Attempt != 2 || next.Status != statuses[i] || next.Version != 1 || next.VideoTaskID != "" {
			t.Fatal("fork not prepared at the selected node", node, next, err)
		}
		if (next.TextRunID == x.TextRunID) != (i >= 1) || (next.ImageTaskID == x.ImageTaskID) != (i >= 2) || (next.Selected != nil) != (i == 3) {
			t.Fatal("wrong inherited data", node)
		}
		if i == 3 && *next.Selected != *x.Selected {
			t.Fatal("inherited different selected image")
		}
		if (i >= 2) != (len(next.Candidates) == 2) {
			t.Fatal("wrong candidate inheritance", node)
		}
		if in.Prompt != nil && next.Graph.Nodes[i].Prompt != prompt || i == 0 && next.Input != input || i != 0 && next.Input != x.Input {
			t.Fatal("new inputs not frozen", node)
		}
		if n := outboxCount(t, db, "SELECT count(*) FROM studio_workflow_intents WHERE execution_id=$1", next.ID); n != intents[i] {
			t.Fatal("new child keys include inherited nodes", node, n)
		}
		if n := outboxCount(t, db, "SELECT count(*) FROM studio_workflow_executions WHERE id=$1 AND text_run_id IS NULL AND image_task_id IS NULL AND video_task_id IS NULL", next.ID); n != 1 {
			t.Fatal("old business child ownership was copied", node)
		}
		again, replay, err := s.rerunWorkflow(scope, x.ID, key, in)
		if err != nil || !replay || again.ID != next.ID {
			t.Fatal("rerun replay broke", err)
		}
		receipt, err := s.workflowRerunReceipt(scope, x.ID, key)
		if err != nil || receipt.ID != next.ID {
			t.Fatal("rerun receipt lost", err)
		}
		_, err = s.workflowRerunReceipt(scope, next.ID, key)
		requireOperation(t, err, "WORKFLOW_NOT_FOUND")
		if n := outboxCount(t, db, "SELECT count(*) FROM studio_outbox_events WHERE resource_id=$1 AND kind='workflow.updated'", next.ID); n != 1 {
			t.Fatal("fork emitted duplicate notifications", n)
		}
		changed := in
		changed.ExpectedVersion++
		_, _, err = s.rerunWorkflow(scope, x.ID, key, changed)
		requireOperation(t, err, "IDEMPOTENCY_CONFLICT")
		if i == 2 {
			// A late image outcome belongs only to its old parent, never this
			// fork or another editable generation with inherited image links.
			if err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
				return workflowImageOutcomeSQL(ctx, tx, Task{ID: x.ImageTaskID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Kind: "image", Status: "FAILED"})
			}); err != nil {
				t.Fatal(err)
			}
			current, err := s.workflowExecution(scope, next.ID)
			if err != nil || current.Version != 1 || current.Status != "WAITING_APPROVAL" {
				t.Fatal("late outcome moved fork", err)
			}
			_, _, err = s.confirmWorkflow(scope, next.ID, 1, x.Candidates[1].AssetID, x.Candidates[1].Revision, newID("choose"))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	old, err := s.workflowExecution(scope, x.ID)
	if err != nil || old.Version != x.Version || old.Status != x.Status || *old.Selected != *x.Selected || old.Graph.Nodes[0].Prompt != x.Graph.Nodes[0].Prompt || old.Input != x.Input {
		t.Fatal("old attempt was mutated", err)
	}
	if n := outboxCount(t, db, "SELECT (SELECT count(*) FROM studio_tasks)+(SELECT count(*) FROM studio_runs)"); n != children {
		t.Fatal("read, fork, or select implicitly created a child")
	}
}

func TestPostgresWorkflowRerunRefusesUnresolvedChildrenAndSerializesOriginalKey(t *testing.T) {
	s, dsn, db := outboxStore(t)
	scope, x := workflowRerunFixture(t, s, db)
	ctx := context.Background()
	in := workflowRerunInput{ExpectedVersion: x.Version, FromNode: "write"}
	key := newID("fork")
	if _, e := db.Exec(ctx, "UPDATE studio_tasks SET document=jsonb_set(document,'{status}',to_jsonb('SUBMIT_UNKNOWN'::text)) WHERE id=$1", x.ImageTaskID); e != nil {
		t.Fatal(e)
	}
	_, e := s.workflowRerunPreview(scope, x.ID, "write")
	requireOperation(t, e, "WORKFLOW_SOURCE_UNRESOLVED")
	_, _, e = s.rerunWorkflow(scope, x.ID, key, in)
	requireOperation(t, e, "WORKFLOW_SOURCE_UNRESOLVED")
	if n := outboxCount(t, db, "SELECT count(*) FROM studio_workflow_executions WHERE source_workflow_id=$1", x.ID); n != 0 {
		t.Fatal("forked unresolved original")
	}
	if _, e = db.Exec(ctx, "UPDATE studio_tasks SET document=jsonb_set(document,'{status}',to_jsonb('SUCCEEDED'::text)) WHERE id=$1", x.ImageTaskID); e != nil {
		t.Fatal(e)
	}
	// Wait for both independent transactions to queue on the same project.
	hold, e := db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback(ctx)
	if _, e = hold.Exec(ctx, "SELECT id FROM studio_projects WHERE id=$1 FOR UPDATE", scope.ProjectID); e != nil {
		t.Fatal(e)
	}
	type result struct {
		id     string
		replay bool
		err    error
	}
	done := make(chan result, 2)
	pids := []int32{}
	for i := 0; i < 2; i++ {
		client := &postgresStore{conn: deliveryAdmin(t, dsn)}
		pids = append(pids, int32(client.conn.PgConn().PID()))
		go func() {
			fork, replay, e := client.rerunWorkflow(scope, x.ID, key, in)
			done <- result{fork.ID, replay, e}
		}()
	}
	awaitRunLeaseWaiters(t, s.conn, pids...)
	if e = hold.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	a, b := <-done, <-done
	if a.err != nil || b.err != nil || a.id != b.id || a.replay == b.replay {
		t.Fatal("original key forked twice or missed its receipt", a, b)
	}
	if n := outboxCount(t, db, "SELECT count(*) FROM studio_workflow_receipts WHERE operation='rerun' AND resource_id=$1", a.id); n != 1 {
		t.Fatal("concurrent requests did not share one receipt")
	}
	// Reads and same-key retries survive later changes to original eligibility.
	if _, e = db.Exec(ctx, "UPDATE studio_tasks SET document=jsonb_set(document,'{status}',to_jsonb('RECONCILE_REQUIRED'::text)) WHERE id=$1", x.ImageTaskID); e != nil {
		t.Fatal(e)
	}
	again, replayed, e := s.rerunWorkflow(scope, x.ID, key, in)
	if e != nil || !replayed || again.ID != a.id {
		t.Fatal("replay revalidated a previously accepted attempt", e)
	}
	_, _, e = s.rerunWorkflow(scope, x.ID, newID("fork"), in)
	requireOperation(t, e, "WORKFLOW_SOURCE_UNRESOLVED")
}

func TestPostgresWorkflowRerunBackpressureAndAmbiguousCancelledChildren(t *testing.T) {
	s, _, db := outboxStore(t)
	scope, x := workflowRerunFixture(t, s, db)
	ctx := context.Background()
	key := newID("retry")
	in := workflowRerunInput{ExpectedVersion: x.Version, FromNode: "write"}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// The global Outbox budget must also roll back the new fork and its
	// private child keys. Retry the exact request only after capacity returns.
	before := outboxCount(t, db, "SELECT pending FROM studio_outbox_limits WHERE singleton")
	exec("UPDATE studio_outbox_limits SET pending=10000 WHERE singleton")
	_, _, err := s.rerunWorkflow(scope, x.ID, key, in)
	requireOperation(t, err, "EVENT_BACKLOG_FULL")
	_, err = s.workflowRerunReceipt(scope, x.ID, key)
	requireOperation(t, err, "WORKFLOW_NOT_FOUND")
	if n := outboxCount(t, db, "SELECT count(*) FROM studio_workflow_executions WHERE source_workflow_id=$1", x.ID); n != 0 {
		t.Fatal("backpressure left a fork")
	}
	exec("UPDATE studio_outbox_limits SET pending=$1 WHERE singleton", before)
	// A completed local cancellation record can be preceded by a handle-based
	// reconciliation that also used QUEUED. Its unresolved upstream handle
	// takes precedence over a superficial queued-cancel audit.
	exec(`UPDATE studio_tasks SET document=jsonb_set(document,'{status}',to_jsonb('CANCELLED'::text)),handle=jsonb_build_object('ID','existing-handle') WHERE id=$1`, x.ImageTaskID)
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		return insertAuditSQL(ctx, tx, newAuditEvent(scope.WorkspaceID, scope.ProjectID, "", "task.cancelled", "task", x.ImageTaskID, AuditDetails{FromStatus: "QUEUED", ToStatus: "CANCELLED"}))
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.rerunWorkflow(scope, x.ID, key, in)
	requireOperation(t, err, "WORKFLOW_SOURCE_UNRESOLVED")
	exec(`UPDATE studio_tasks SET handle='{}' WHERE id=$1`, x.ImageTaskID)
	exec("UPDATE studio_run_streams SET execution_id='started' WHERE run_id=$1", x.TextRunID)
	exec(`UPDATE studio_runs SET document=document||'{"status":"FAILED","error":{"code":"PROVIDER_STREAM_FAILED"}}'::jsonb WHERE id=$1`, x.TextRunID)
	_, _, err = s.rerunWorkflow(scope, x.ID, key, in)
	requireOperation(t, err, "WORKFLOW_SOURCE_UNRESOLVED")
	exec(`UPDATE studio_runs SET document=document||'{"status":"COMPLETED","error":null}'::jsonb WHERE id=$1`, x.TextRunID)
	next, replay, err := s.rerunWorkflow(scope, x.ID, key, in)
	if err != nil || replay || next.SourceVersion != x.Version {
		t.Fatal("fresh retry after known outcomes", err)
	}
}

func TestPostgresWorkflowRerunRejectsRevokedSessionEvenForReplay(t *testing.T) {
	a, server, client, identity, dsn := operationsFixture(t)
	p, err := a.currentPrincipal(operationsRequest(t, client, server))
	if err != nil {
		t.Fatal(err)
	}
	g, models := workflowStoreGraph(t)
	scope := subscriptionScope{identity.Workspace.ID, identity.Project.ID}
	d, _, err := a.store.saveWorkflowDefinition(scope, "", 0, g, newID("save"), p)
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := a.store.startWorkflow(scope, d.ID, d.Version, "用户原输入", newID("start"), models, "demo", p)
	if err != nil {
		t.Fatal(err)
	}
	db := deliveryAdmin(t, dsn)
	ctx := context.Background()
	if _, err = db.Exec(ctx, "UPDATE studio_workflow_executions SET status='CANCELLED',version=2 WHERE id=$1", x.ID); err != nil {
		t.Fatal(err)
	}
	key := newID("fork")
	in := workflowRerunInput{ExpectedVersion: 2, FromNode: "write"}
	next, _, err := a.store.rerunWorkflow(scope, x.ID, key, in, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "DELETE FROM studio_sessions WHERE id=$1", p.SessionID); err != nil {
		t.Fatal(err)
	}
	_, _, err = a.store.rerunWorkflow(scope, x.ID, key, in, p)
	requireOperation(t, err, "AUTH_REQUIRED")
	_, _, err = a.store.rerunWorkflow(scope, x.ID, newID("fork"), in, p)
	requireOperation(t, err, "AUTH_REQUIRED")
	if n := outboxCount(t, db, "SELECT count(*) FROM studio_workflow_executions WHERE source_workflow_id=$1", x.ID); n != 1 || next.ID == x.ID {
		t.Fatal("revoked session changed fork history")
	}
}
