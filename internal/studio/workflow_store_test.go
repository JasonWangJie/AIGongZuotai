package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func workflowStoreGraph(t *testing.T) (WorkflowGraph, map[string]Model) {
	t.Helper()
	c, err := loadModelCatalog(Config{Mode: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]Model{"text": c.Entries["studio-chat"].Model, "image": c.Entries["studio-image"].Model}
	return WorkflowGraph{Name: "镜头构思", TemplateID: "text-image-approval", TemplateVersion: "1",
		Nodes: []WorkflowNode{
			{ID: "write", Kind: "text", Title: "构思", Prompt: "写一段描述", ModelID: models["text"].ID, ModelVersion: models["text"].Version},
			{ID: "draw", Kind: "image", Title: "生成", ModelID: models["image"].ID, ModelVersion: models["image"].Version, Parameters: &TaskParameters{Count: 2, AspectRatio: "1:1"}},
			{ID: "approve", Kind: "approval", Title: "选片"}},
		Edges: []WorkflowEdge{{"write", "text", "draw", "prompt"}, {"draw", "images", "approve", "images"}}}, models
}
func TestWorkflowGraphValidatesPortsBoundsAndDAG(t *testing.T) {
	g, _ := workflowStoreGraph(t)
	if err := validateWorkflowGraph(g); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*WorkflowGraph){
		"duplicate node":      func(g *WorkflowGraph) { g.Nodes[1].ID = g.Nodes[0].ID },
		"unsupported node":    func(g *WorkflowGraph) { g.Nodes[1].Kind = "video" },
		"two writers":         func(g *WorkflowGraph) { g.Nodes[1].Kind = "text" },
		"cycle":               func(g *WorkflowGraph) { g.Edges[0].From = g.Nodes[2].ID },
		"dangling":            func(g *WorkflowGraph) { g.Edges[0].To = "gone" },
		"wrong port":          func(g *WorkflowGraph) { g.Edges[0].ToPort = "images" },
		"two edges to input":  func(g *WorkflowGraph) { g.Edges[1] = g.Edges[0] },
		"extra node":          func(g *WorkflowGraph) { g.Nodes = append(g.Nodes, g.Nodes[0]) },
		"extra edge":          func(g *WorkflowGraph) { g.Edges = append(g.Edges, g.Edges[0]) },
		"unbounded expansion": func(g *WorkflowGraph) { g.Nodes[1].Parameters.Count = 5 },
		"wrong version":       func(g *WorkflowGraph) { g.TemplateVersion = "2" },
		"oversized prompt":    func(g *WorkflowGraph) { g.Nodes[0].Prompt = strings.Repeat("甲", 1366) },
		"encoded budget": func(g *WorkflowGraph) {
			g.Nodes[0].Prompt = strings.Repeat("\x01", 4096)
			g.Nodes[1].Prompt = strings.Repeat("\x01", 4096)
		},
		"approval injection": func(g *WorkflowGraph) { g.Nodes[2].ModelID = "studio-image" },
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := workflowStoreGraph(t)
			edit(&g)
			requireOperation(t, validateWorkflowGraph(g), "INVALID_WORKFLOW_GRAPH")
		})
	}
}

func TestPostgresWorkflowFrozenVersionsAndNodeIntent(t *testing.T) {
	s, _, admin := outboxStore(t)
	scope := subscriptionScope{"ws_demo", "prj_demo"}
	g, models := workflowStoreGraph(t)
	key := newID("save")
	d, replay, err := s.saveWorkflowDefinition(scope, "", 0, g, key)
	if err != nil || replay || d.Version != 1 {
		t.Fatal("first definition", err)
	}
	g2, _ := workflowStoreGraph(t)
	g2.Nodes[0].Prompt = "新版本指令"
	second, _, err := s.saveWorkflowDefinition(scope, d.ID, 1, g2, newID("save"))
	if err != nil || second.Version != 2 {
		t.Fatal("second version", err)
	}
	d, replay, err = s.saveWorkflowDefinition(scope, "", 0, g, key)
	if err != nil || !replay || d.Version != 1 || d.Graph.Nodes[0].Prompt != g.Nodes[0].Prompt {
		t.Fatal("old save did not return immutable original", err)
	}
	_, _, err = s.saveWorkflowDefinition(scope, d.ID, 1, g2, newID("save"))
	requireOperation(t, err, "WORKFLOW_CONFLICT")
	startKey := newID("start")
	x, replay, err := s.startWorkflow(scope, d.ID, 1, "原输入", startKey, models, "demo")
	if err != nil || replay || x.DefinitionVersion != 1 || x.Graph.Nodes[0].Prompt != g.Nodes[0].Prompt {
		t.Fatal("start failed to bind v1", err)
	}
	replayed, found, err := s.startWorkflow(scope, d.ID, 1, x.Input, startKey, nil, "live")
	if err != nil || !found || replayed.ID != x.ID || replayed.ProviderMode != "demo" {
		t.Fatal("new catalog changed original operation", err)
	}
	_, _, err = s.startWorkflow(scope, d.ID, 1, "其他输入", startKey, models, "demo")
	requireOperation(t, err, "IDEMPOTENCY_CONFLICT")
	foreign := subscriptionScope{scope.WorkspaceID, newID("prj")}
	if _, err = admin.Exec(context.Background(), "INSERT INTO studio_projects(id,workspace_id,name) VALUES($1,$2,'other')", foreign.ProjectID, foreign.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	_, err = s.workflowExecution(foreign, x.ID)
	requireOperation(t, err, "WORKFLOW_NOT_FOUND")
	_, err = s.workflowDefinitionByKey(foreign, key)
	requireOperation(t, err, "WORKFLOW_NOT_FOUND")
	input := RunInput{ProjectID: scope.ProjectID, ModelVersion: g.Nodes[0].ModelVersion, ModelID: g.Nodes[0].ModelID, Input: workflowPrompt(g.Nodes[0].Prompt, x.Input)}
	action := newID("action")
	i, err := s.prepareWorkflowIntent(scope, x.ID, x.Version, "text", action, &input, nil)
	if err != nil || i.Key == "" {
		t.Fatal("intent was not frozen", err)
	}
	_, err = s.workflowActionReceipt(scope, x.ID, "text", action)
	requireOperation(t, err, "WORKFLOW_NOT_FOUND")
	if err = s.releaseWorkflowIntent(scope, x.ID, "text", i); err != nil {
		t.Fatal(err)
	}
	action = newID("action")
	i2, err := s.prepareWorkflowIntent(scope, x.ID, x.Version, "text", action, &input, nil)
	if err != nil || i2.Key != i.Key {
		t.Fatal("rejected attempt lost permanent child key", err)
	}
	requireOperation(t, s.releaseWorkflowIntent(scope, x.ID, "text", i), "WORKFLOW_CONFLICT")
	oldActor := &principal{Workspace: Workspace{ID: scope.WorkspaceID}}
	guard := &workflowChildGuard{scope: scope, id: x.ID, kind: "text", key: i.Key, actionKey: action, version: x.Version, actor: oldActor}
	r := outboxFixtureRun(newID("run"))
	r.Output, r.Prompt, r.ModelVersion = "", input.Input, input.ModelVersion
	if err = s.createGuardedRun(&r, scope.WorkspaceID+":runs:"+i.Key, payloadHash(input), guard, nil); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.workflowActionReceipt(scope, x.ID, "text", action)
	if err != nil || receipt.TextRunID != r.ID || receipt.Status != "TEXT_ACTIVE" {
		t.Fatal("child commit did not attach original parent", err)
	}
	requireOperation(t, s.releaseWorkflowIntent(scope, x.ID, "text", i2), "WORKFLOW_CONFLICT")
	l := claimFixtureRun(t, s, &r)
	r.Status = "FAILED"
	r.Seq++
	if err = s.saveLeasedRun(&r, l, r.Seq-1); err != nil {
		t.Fatal(err)
	}
	saved, err := s.workflowExecution(scope, x.ID)
	if err != nil || saved.Status != "FAILED" || saved.TextRunID != r.ID || saved.ImageTaskID != "" {
		t.Fatal("failed child advanced another node", err)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_workflow_intents WHERE execution_id=$1", x.ID); n != 2 {
		t.Fatal("unexpected number of child attempts", n)
	}
}

func TestPostgresWorkflowOriginalCandidatesAndConcurrentConfirmation(t *testing.T) {
	s, dsn, admin := outboxStore(t)
	scope := subscriptionScope{"ws_demo", "prj_demo"}
	actor := &principal{Workspace: Workspace{ID: scope.WorkspaceID}}
	g, models := workflowStoreGraph(t)
	d, _, err := s.saveWorkflowDefinition(scope, "", 0, g, newID("save"))
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := s.startWorkflow(scope, d.ID, d.Version, "原输入", newID("start"), models, "demo")
	if err != nil {
		t.Fatal(err)
	}
	input := RunInput{ProjectID: scope.ProjectID, ModelID: g.Nodes[0].ModelID, ModelVersion: g.Nodes[0].ModelVersion, Input: workflowPrompt(g.Nodes[0].Prompt, x.Input)}
	action := newID("action")
	i, err := s.prepareWorkflowIntent(scope, x.ID, x.Version, "text", action, &input, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := outboxFixtureRun(newID("run"))
	r.Prompt, r.Output, r.ModelVersion = input.Input, "", input.ModelVersion
	guard := &workflowChildGuard{scope: scope, id: x.ID, kind: "text", key: i.Key, actionKey: action, version: x.Version, actor: actor}
	if err = s.createGuardedRun(&r, scope.WorkspaceID+":runs:"+i.Key, payloadHash(input), guard, nil); err != nil {
		t.Fatal(err)
	}
	l := claimFixtureRun(t, s, &r)
	if err = s.appendLeasedRunDelta(l, 0, "公开图像描述", time.Now()); err != nil {
		t.Fatal(err)
	}
	r.Output, r.Status, r.Seq = "公开图像描述", "COMPLETED", 2
	if err = s.saveLeasedRun(&r, l, 1); err != nil {
		t.Fatal(err)
	}
	x, err = s.workflowExecution(scope, x.ID)
	if err != nil || x.Status != "IMAGE_READY" {
		t.Fatal("text terminal did not enable the image node", err)
	}
	n := g.Nodes[1]
	taskInput := TaskInput{ProjectID: x.ProjectID, Kind: "image", Operation: "generate", ModelID: n.ModelID, ModelVersion: n.ModelVersion, Parameters: *n.Parameters}
	taskInput.Inputs.Prompt = workflowPrompt(n.Prompt, r.Output)
	action = newID("action")
	i, err = s.prepareWorkflowIntent(scope, x.ID, x.Version, "image", action, nil, &taskInput)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: scope.WorkspaceID, ProjectID: x.ProjectID, Kind: "image", ModelID: n.ModelID, ModelVersion: n.ModelVersion, ProviderMode: "demo", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now}, Input: taskInput, NextPoll: now}
	guard = &workflowChildGuard{scope: scope, id: x.ID, kind: "image", key: i.Key, actionKey: action, version: x.Version, actor: actor}
	if err = s.createGuardedTask(task, scope.WorkspaceID+":tasks:"+i.Key, payloadHash(taskInput), guard, nil); err != nil {
		t.Fatal(err)
	}
	task.Task.Status = "SUCCEEDED"
	task.Task.Version++
	task.Task.Artifacts = []Artifact{{ID: newID("art"), Kind: "image", URL: "/demo-assets/cloud.svg"}, {ID: newID("art"), Kind: "image", URL: "/demo-assets/sunset.svg"}}
	for j := range task.Task.Artifacts {
		png := referencePNG(t, byte(j+1))
		digest := sha256.Sum256(png)
		a := &task.Task.Artifacts[j]
		a.StorageInfo = &AssetInfo{ID: a.ID, ByteSize: int64(len(png)), ContentType: "image/png", SHA256: hex.EncodeToString(digest[:])}
	}
	if err = s.saveTask(task); err != nil {
		t.Fatal(err)
	}
	x, err = s.workflowExecution(scope, x.ID)
	if err != nil || x.Status != "WAITING_APPROVAL" || len(x.Candidates) != 2 {
		t.Fatal("image transaction did not freeze two candidates", err)
	}
	first, second := x.Candidates[0], x.Candidates[1]
	ctx := context.Background()
	if _, err = admin.Exec(ctx, "UPDATE studio_library_entries SET name='renamed original',revision=revision+1 WHERE id=$1", first.AssetID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.confirmWorkflow(scope, x.ID, x.Version, first.AssetID, first.Revision, newID("confirm"))
	requireOperation(t, err, "WORKFLOW_CANDIDATE_CHANGED")
	if _, err = admin.Exec(ctx, "UPDATE studio_library_entries SET state='trashed',deleted_at=now(),revision=revision+1 WHERE id=$1", second.AssetID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.confirmWorkflow(scope, x.ID, x.Version, second.AssetID, second.Revision+1, newID("confirm"))
	requireOperation(t, err, "WORKFLOW_CANDIDATE_CHANGED")
	// Restore the visible entry with a digest from different encoded bytes.
	// Even with its current metadata revision it is no longer the handed-off image.
	replacement := sha256.Sum256(referencePNG(t, 99))
	if _, err = admin.Exec(ctx, `UPDATE studio_library_entries SET state='active',deleted_at=NULL,revision=revision+1,
	 storage_info=jsonb_set(storage_info,'{sha256}',to_jsonb($2::text)) WHERE id=$1`, second.AssetID, hex.EncodeToString(replacement[:])); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.confirmWorkflow(scope, x.ID, x.Version, second.AssetID, second.Revision+2, newID("confirm"))
	requireOperation(t, err, "WORKFLOW_CANDIDATE_CHANGED")
	_, _, err = s.confirmWorkflow(scope, x.ID, x.Version, newID("unrelated"), second.Revision, newID("confirm"))
	requireOperation(t, err, "WORKFLOW_CANDIDATE_CHANGED")
	detail, err := (&App{store: s}).workflowDetail(scope, x)
	if err != nil || len(detail.Candidates) != 1 || detail.Candidates[0].AssetID != first.AssetID ||
		detail.Candidates[0].Revision != first.Revision+1 || detail.Candidates[0].ContentVersion != first.ContentVersion {
		t.Fatal("refresh did not show only the renamed original bytes", err)
	}
	chosen := detail.Candidates[0]
	// Hold the project row until both confirmation transactions are really
	// waiting in independent backend sessions. Only one original key can win.
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM studio_projects WHERE id=$1 FOR UPDATE", scope.ProjectID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		key string
		err error
	}
	done := make(chan result, 2)
	var pids []int32
	for j := 0; j < 2; j++ {
		client := &postgresStore{conn: deliveryAdmin(t, dsn)}
		pids = append(pids, int32(client.conn.PgConn().PID()))
		go func() {
			key := newID("confirm")
			_, _, e := client.confirmWorkflow(scope, x.ID, x.Version, chosen.AssetID, chosen.Revision, key)
			done <- result{key, e}
		}()
	}
	awaitRunLeaseWaiters(t, s.conn, pids...)
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	win, lose := <-done, <-done
	if win.err != nil {
		win, lose = lose, win
	}
	if win.err != nil {
		t.Fatal(win.err)
	}
	requireOperation(t, lose.err, "WORKFLOW_CONFLICT")
	final, replayed, err := s.confirmWorkflow(scope, x.ID, x.Version, chosen.AssetID, chosen.Revision, win.key)
	if err != nil || !replayed || final.Status != "COMPLETED" || final.Selected == nil || *final.Selected != chosen || final.Version != x.Version+1 {
		t.Fatal("confirmed selection not recovered from original receipt", err)
	}
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_workflow_receipts WHERE operation='confirm' AND resource_id=$1", x.ID); n != 1 {
		t.Fatal("more than one choice was committed", n)
	}
}
