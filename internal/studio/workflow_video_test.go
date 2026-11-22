package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func workflowStoreVideoGraph(t *testing.T) (*App, WorkflowGraph, map[string]Model) {
	t.Helper()
	cfg := Config{Mode: "demo"}
	file := defaultCatalogConfig(cfg)
	for i := range file.Models {
		if file.Models[i].Kind == "video" {
			file.Models[i].Capabilities.VideoFirstFrameSupported = true
		}
	}
	cfg.ModelCatalogFile = writeCatalogFixture(t, file)
	c, err := loadModelCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g, models := workflowStoreGraph(t)
	v := c.Entries["studio-video"].Model
	models["video"] = v
	g.TemplateID, g.TemplateVersion = "text-image-approval-video", "2"
	g.Nodes = append(g.Nodes, WorkflowNode{ID: "animate", Kind: "video", Title: "视频", Prompt: "镜头前移", ModelID: v.ID, ModelVersion: v.Version, Parameters: &TaskParameters{AspectRatio: "16:9", Seconds: 5, Count: 1}})
	g.Edges = append(g.Edges, WorkflowEdge{"approve", "selected_image", "animate", "first_frame"}, WorkflowEdge{"write", "text", "animate", "prompt"})
	return &App{config: cfg, catalog: c}, g, models
}

func TestWorkflowVideoGraphRequiresSelectionAndPublicText(t *testing.T) {
	_, g, _ := workflowStoreVideoGraph(t)
	if err := validateWorkflowGraph(g); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*WorkflowGraph){
		"image directly to video": func(g *WorkflowGraph) { g.Edges[2].From = "draw"; g.Edges[2].FromPort = "images" },
		"missing selection": func(g *WorkflowGraph) {
			g.Edges = g.Edges[:3]
			g.Edges[2] = WorkflowEdge{"write", "text", "animate", "prompt"}
		},
		"missing public text": func(g *WorkflowGraph) { g.Edges = g.Edges[:3] },
		"legacy template":     func(g *WorkflowGraph) { g.TemplateID = "text-image-approval"; g.TemplateVersion = "1" },
		"two videos":          func(g *WorkflowGraph) { g.Nodes[3].Parameters.Count = 2 },
		"missing duration":    func(g *WorkflowGraph) { g.Nodes[3].Parameters.Seconds = 0 },
		"excessive duration":  func(g *WorkflowGraph) { g.Nodes[3].Parameters.Seconds = 601 },
	} {
		t.Run(name, func(t *testing.T) {
			_, g, _ := workflowStoreVideoGraph(t)
			edit(&g)
			requireOperation(t, validateWorkflowGraph(g), "INVALID_WORKFLOW_GRAPH")
		})
	}
}

func TestWorkflowVideoInputBindsChosenOriginalAndScript(t *testing.T) {
	a, g, models := workflowStoreVideoGraph(t)
	c := WorkflowCandidate{"asset_chosen", "art_chosen", "task_stills", 2, "original-content"}
	other := WorkflowCandidate{"asset_other", "art_other", "task_stills", 3, "other-content"}
	d := workflowDetail{WorkflowExecution: WorkflowExecution{ID: "wfr_example", ProjectID: "prj_demo", Graph: g, Input: "original request", FrozenModels: models, ProviderMode: a.config.Mode, Status: "VIDEO_READY", Candidates: []WorkflowCandidate{other, c}, Selected: &c}, TextRun: &Run{Status: "COMPLETED", Output: "published script"}}
	n := g.Nodes[3]
	// The user may rename the original after approval. Their previous selection
	// still identifies those exact bytes and the revision they approved.
	d.Candidates[1].Revision++
	input, err := a.workflowNodeInput(d, n)
	if err != nil || input.Task == nil || input.Task.Operation != "first_frame" || input.Task.Inputs.WorkflowID != d.ID || input.Task.Inputs.FirstFrame == nil || *input.Task.Inputs.FirstFrame != c || input.Task.Inputs.Prompt != "镜头前移\n\npublished script" {
		t.Fatal("input did not bind chosen bytes, workflow, and actual published text", err)
	}
	d.Status = "WAITING_APPROVAL"
	_, err = a.workflowNodeInput(d, n)
	requireOperation(t, err, "WORKFLOW_STATE_CHANGED")
	d.Status = "VIDEO_READY"
	d.Candidates[1].ContentVersion = "replacement"
	_, err = a.workflowNodeInput(d, n)
	requireOperation(t, err, "WORKFLOW_FIRST_FRAME_UNAVAILABLE")
	d.Candidates = []WorkflowCandidate{other}
	_, err = a.workflowNodeInput(d, n)
	requireOperation(t, err, "WORKFLOW_FIRST_FRAME_UNAVAILABLE")
	d.Candidates = []WorkflowCandidate{c}
	d.TextRun.Status = "INTERRUPTED"
	_, err = a.workflowNodeInput(d, n)
	requireOperation(t, err, "WORKFLOW_STATE_CHANGED")
}

func TestPostgresWorkflowVideoRechecksSelectedAssetAtAdmission(t *testing.T) {
	s, _, admin := outboxStore(t)
	a, g, models := workflowStoreVideoGraph(t)
	a.store = s
	scope := subscriptionScope{"ws_demo", "prj_demo"}
	def, _, err := s.saveWorkflowDefinition(scope, "", 0, g, newID("save"))
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := s.startWorkflow(scope, def.ID, def.Version, "seed", newID("start"), models, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, admin, "SELECT count(DISTINCT child_key) FROM studio_workflow_intents WHERE execution_id=$1", x.ID); n != 3 {
		t.Fatal("four-node template should reserve exactly three child keys", n)
	}
	// Seed already delivered child resources through their ordinary storage
	// methods. This test exercises the interval between selection/prepare and
	// final admission, independently of the gateway and billing suite.
	r := outboxFixtureRun(newID("run"))
	r.Status, r.Output = "COMPLETED", "镜头脚本"
	if err = s.createRun(r, newID("seed"), payloadHash(r.ID)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	image := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Kind: "image", ModelID: g.Nodes[1].ModelID, ModelVersion: g.Nodes[1].ModelVersion, ProviderMode: "demo", Status: "SUCCEEDED", Version: 2, CreatedAt: now, UpdatedAt: now}, NextPoll: now}
	for j := 0; j < 2; j++ {
		b := referencePNG(t, byte(j+11))
		h := sha256.Sum256(b)
		id := newID("art")
		image.Task.Artifacts = append(image.Task.Artifacts, Artifact{ID: id, Kind: "image", URL: "/api/v1/assets/" + id, StorageInfo: &AssetInfo{ID: id, ContentType: "image/png", ByteSize: int64(len(b)), SHA256: hex.EncodeToString(h[:])}})
	}
	if err = s.createTask(image, newID("seed"), payloadHash(image.Task.ID)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var candidates []WorkflowCandidate
	err = pgx.BeginFunc(ctx, admin, func(tx pgx.Tx) error {
		var e error
		candidates, e = workflowCandidatesSQL(ctx, tx, scope, image.Task.ID, false)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "UPDATE studio_workflow_executions SET status='WAITING_APPROVAL',version=5,text_run_id=$2,image_task_id=$3,candidates=$4 WHERE id=$1", x.ID, r.ID, image.Task.ID, candidates)
		return e
	})
	if err != nil || len(candidates) != 2 {
		t.Fatal("two private candidates required", err, len(candidates))
	}
	chosen := candidates[1]
	x, _, err = s.confirmWorkflow(scope, x.ID, 5, chosen.AssetID, chosen.Revision, newID("confirm"))
	if err != nil || x.Status != "VIDEO_READY" || x.VideoTaskID != "" {
		t.Fatal("confirmation did not stop before video admission", err)
	}
	_, err = s.workflowFirstFrameInfo(scope, x.ID, candidates[0])
	requireOperation(t, err, "WORKFLOW_FIRST_FRAME_UNAVAILABLE")
	_, err = s.workflowFirstFrameInfo(subscriptionScope{scope.WorkspaceID, "other_project"}, x.ID, chosen)
	requireOperation(t, err, "WORKFLOW_NOT_FOUND")
	if _, err = admin.Exec(ctx, "UPDATE studio_library_entries SET revision=revision+1,name='same bytes' WHERE id=$1", chosen.AssetID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.workflowFirstFrameInfo(scope, x.ID, chosen); err != nil {
		t.Fatal("rename invalidated original content", err)
	}
	d, err := a.workflowDetail(scope, x)
	if err != nil {
		t.Fatal(err)
	}
	input, err := a.workflowNodeInput(d, g.Nodes[3])
	if err != nil {
		t.Fatal(err)
	}
	action := newID("animate")
	intent, err := s.prepareWorkflowIntent(scope, x.ID, x.Version, "video", action, nil, input.Task)
	if err != nil {
		t.Fatal(err)
	}
	actor := &principal{Workspace: Workspace{ID: scope.WorkspaceID}}
	guard := &workflowChildGuard{scope: scope, id: x.ID, kind: "video", key: intent.Key, actionKey: action, version: x.Version, actor: actor}
	video := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, Kind: "video", Operation: "first_frame", ModelID: g.Nodes[3].ModelID, ModelVersion: g.Nodes[3].ModelVersion, ProviderMode: "demo", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now}, Input: *input.Task, NextPoll: now}
	commit := func(hash [32]byte) error {
		return s.createGuardedTask(video, scope.WorkspaceID+":tasks:"+intent.Key, hash, guard, nil)
	}
	requireOperation(t, commit(payloadHash("different task body")), "WORKFLOW_STATE_CHANGED")
	if _, err = admin.Exec(ctx, "UPDATE studio_library_entries SET state='trashed',deleted_at=clock_timestamp(),revision=revision+1 WHERE id=$1", chosen.AssetID); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, commit(payloadHash(*input.Task)), "WORKFLOW_FIRST_FRAME_UNAVAILABLE")
	// Restore the row but replace its digest with different bytes. The
	// previous intent still cannot attach a different frame.
	replacement := sha256.Sum256(referencePNG(t, 99))
	if _, err = admin.Exec(ctx, "UPDATE studio_library_entries SET state='active',deleted_at=NULL,storage_info=jsonb_set(storage_info,'{sha256}',to_jsonb($2::text)) WHERE id=$1", chosen.AssetID, hex.EncodeToString(replacement[:])); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, commit(payloadHash(*input.Task)), "WORKFLOW_FIRST_FRAME_UNAVAILABLE")
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_tasks WHERE id=$1", video.Task.ID); n != 0 {
		t.Fatal("rejected video was admitted")
	}
	if child, err := s.workflowIntentChild(scope, x.ID, "video"); err != nil || child != "" {
		t.Fatal("rejected video has a business receipt", err)
	}
	saved, err := s.workflowExecution(scope, x.ID)
	if err != nil || saved.Status != "VIDEO_READY" || saved.Version != x.Version || saved.Selected == nil || *saved.Selected != chosen {
		t.Fatal("rejected operations changed original choice", err)
	}
}
