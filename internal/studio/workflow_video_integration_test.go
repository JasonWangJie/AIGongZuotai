package studio

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The application calls the real asynchronous HTTP adapter. Parse the video
// upload at the gateway before issuing a handle for the existing delivery fixture.
type workflowVideoGateway struct {
	fixture *workflowIntegrationFixture
	mu      sync.Mutex
	frames  [][]byte
}

func newWorkflowVideoGateway(t *testing.T) *workflowVideoGateway {
	t.Helper()
	f := newWorkflowIntegrationFixture(t)
	video := &workflowVideoGateway{fixture: f}
	baseHandler := f.gateway.Config.Handler
	f.media.blockedImage = referencePNG(t, 191) // The delivery fixture uses this alternate PNG for slot 1.
	if bytes.Equal(f.media.image, f.media.blockedImage) {
		t.Fatal("two distinct source images required")
	}
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == videoGenerationEndpoint {
			r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error("video did not send valid multipart", err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			files := r.MultipartForm.File["input_reference"]
			if len(r.MultipartForm.File) != 1 || len(files) != 1 || files[0].Header.Get("Content-Type") != "image/png" ||
				r.FormValue("model") != "demo-video" || r.FormValue("seconds") != "5" || !strings.Contains(r.FormValue("prompt"), workflowTestText) {
				t.Error("missing or incorrect model, prompt, duration or selected input_reference")
				w.WriteHeader(400)
				return
			}
			part, err := files[0].Open()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			frame, err := io.ReadAll(part)
			part.Close()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			video.mu.Lock()
			video.frames = append(video.frames, frame)
			video.mu.Unlock()
			r.Body = io.NopCloser(strings.NewReader("{}"))
		}
		baseHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.gateway.Close)
	catalog := defaultCatalogConfig(Config{Mode: "demo"})
	for i := range catalog.Models {
		if catalog.Models[i].Kind == "video" {
			catalog.Models[i].Capabilities.VideoFirstFrameSupported = true
		}
	}
	f.cfg.ModelCatalogFile = writeCatalogFixture(t, catalog)
	return video
}

func (v *workflowVideoGateway) open(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	f := v.fixture
	p, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: f.gateway.URL, APIKey: fixtureMediaKey, CredentialID: "workflow-video-fixture", ImageModel: "demo-image", VideoModel: "demo-video", VideoSeconds: 5, AssetDir: f.cfg.AssetDir, allowLocalAssets: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.mediaProvider = p
	a, s := testApp(t, cfg)
	a.chat = f.chat
	return a, s
}

func workflowVideoGraph(a *App) WorkflowGraph {
	g := workflowFixtureGraph(a)
	g.TemplateID, g.TemplateVersion = "text-image-approval-video", "2"
	model := a.catalog.Entries["studio-video"].Model
	g.Nodes = append(g.Nodes, WorkflowNode{ID: "movie", Kind: "video", Title: "Animate", Prompt: "Animate the scene", ModelID: model.ID, ModelVersion: model.Version, Parameters: &TaskParameters{AspectRatio: "16:9", Seconds: 5, Count: 1}})
	g.Edges = append(g.Edges, WorkflowEdge{From: "pick", FromPort: "selected_image", To: "movie", ToPort: "first_frame"}, WorkflowEdge{From: "script", FromPort: "text", To: "movie", ToPort: "prompt"})
	return g
}

func TestPostgresWorkflowVideoSendsChosenOriginals(t *testing.T) {
	gateway := newWorkflowVideoGateway(t)
	f := gateway.fixture
	app, server := gateway.open(t)
	addr := server.URL
	client := authClient(t)
	owner := registerFixture(t, client, addr, "video-workflow@example.test")
	project := owner.Project.ID
	other := authClient(t)
	registerFixture(t, other, addr, "video-other@example.test")
	root := "/api/v1/projects/" + project
	call := func(method, path, key string, body any, want int) []byte {
		t.Helper()
		code, raw, _ := authRequest(t, client, addr, method, path, key, project, body)
		if code != want {
			t.Fatalf("%s %s: %d, wanted %d, response %s", method, path, code, want, raw)
		}
		return raw
	}
	wait := func(path, status string) workflowDetail {
		t.Helper()
		var d workflowDetail
		for until := time.Now().Add(12 * time.Second); time.Now().Before(until); {
			d = decode[workflowDetail](t, call("GET", path, "", nil, 200))
			if d.Status == status {
				return d
			}
			if d.Status == "FAILED" || d.Status == "REVIEW" {
				t.Fatalf("workflow reached %s %s; wanted %s", d.Status, d.ErrorCode, status)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out at %s waiting for %s", d.Status, status)
		return d
	}
	def := decode[WorkflowDefinition](t, call("POST", root+"/workflow-definitions", newID("save"), map[string]any{"expected_version": 0, "graph": workflowVideoGraph(app)}, 201))
	for i, expected := range [][]byte{f.media.image, f.media.blockedImage} {
		flow := decode[WorkflowExecution](t, call("POST", root+"/workflow-runs", newID("start"), map[string]any{"definition_id": def.ID, "definition_version": def.Version, "input": "a winter night"}, 201))
		path := root + "/workflow-runs/" + flow.ID
		call("POST", path+"/nodes/script/execute", newID("text"), map[string]any{"expected_version": flow.Version}, 200)
		d := wait(path, "IMAGE_READY")
		call("POST", path+"/nodes/picture/execute", newID("picture"), map[string]any{"expected_version": d.Version}, 200)
		d = wait(path, "WAITING_APPROVAL")
		var chosen WorkflowCandidate
		for _, c := range d.Candidates {
			if bytes.Equal(call("GET", "/api/v1/library/"+c.AssetID+"/content", "", nil, 200), expected) {
				chosen = c
				break
			}
		}
		if len(d.Candidates) != 2 || chosen.AssetID == "" {
			t.Fatal("missing requested original PNG")
		}
		call("POST", path+"/nodes/pick/confirm", newID("pick"), map[string]any{"expected_version": d.Version, "asset_id": chosen.AssetID, "revision": chosen.Revision}, 200)
		d = wait(path, "VIDEO_READY")
		if i == 0 {
			// A restart must recover a human choice without enqueueing a video.
			app.Close()
			server.Close()
			restoreWorkflowFixture(t, f)
			app, server = gateway.open(t)
			addr = server.URL
			client = authClient(t)
			call("POST", "/api/v1/auth/login", "", map[string]string{"email": "video-workflow@example.test", "password": "fixture-password-123"}, 200)
			other = authClient(t)
			if code, _, _ := authRequest(t, other, addr, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": "video-other@example.test", "password": "fixture-password-123"}); code != 200 {
				t.Fatal("fixture login failed", code)
			}
			d = wait(path, "VIDEO_READY")
		}
		input := decode[workflowInput](t, call("GET", path+"/nodes/movie/input", "", nil, 200))
		if d.Selected == nil || *d.Selected != chosen || input.Task == nil || input.Task.Operation != "first_frame" || input.Task.Inputs.WorkflowID != flow.ID || input.Task.Inputs.FirstFrame == nil || *input.Task.Inputs.FirstFrame != chosen {
			t.Fatal("video did not bind original workflow and selected asset")
		}
		key := newID("animate")
		body := map[string]any{"expected_version": d.Version}
		if code, _, _ := authRequest(t, other, addr, "POST", path+"/nodes/movie/execute", key, project, body); code != 404 {
			t.Fatal("another project could act on chosen image", code)
		}
		gateway.mu.Lock()
		before := len(gateway.frames)
		gateway.mu.Unlock()
		if before != i {
			t.Fatal("selection, restart, quote input or invalid action submitted video")
		}
		call("POST", path+"/nodes/movie/execute", key, body, 200)
		d = wait(path, "COMPLETED")
		if d.VideoTask == nil || d.VideoTaskID != d.VideoTask.ID || len(d.VideoArtifacts) != 1 || d.VideoArtifacts[0].TaskID != d.VideoTaskID || d.VideoTask.Moderation == nil || d.VideoTask.Moderation.State != "ALLOWED" {
			t.Fatal("workflow completed without approved registered video")
		}
		download := "/api/v1/library/" + d.VideoArtifacts[0].AssetID + "/content"
		if !bytes.Equal(call("GET", download, "", nil, 200), f.media.video) {
			t.Fatal("private video bytes differ from gateway result")
		}
		if code, _, _ := authRequest(t, other, addr, "GET", download, "", project, nil); code != 404 {
			t.Fatal("other workspace could read private video", code)
		}
		call("GET", path+"/nodes/movie/receipt", key, nil, 200)
		code, _, h := authRequest(t, client, addr, "POST", path+"/nodes/movie/execute", key, project, body)
		if code != 200 || h.Get("Idempotency-Replayed") != "true" {
			t.Fatal("video action did not replay", code)
		}
		gateway.mu.Lock()
		same := len(gateway.frames) == i+1 && bytes.Equal(gateway.frames[i], expected)
		gateway.mu.Unlock()
		if !same {
			t.Fatal("actual video gateway POST did not receive the one chosen original PNG")
		}
	}
	f.media.mu.Lock()
	defer f.media.mu.Unlock()
	if f.media.count != 4 || f.chatCalls.Load() != 2 {
		t.Fatalf("expected two chats, two image posts and two video posts, got %d chats and %d media posts", f.chatCalls.Load(), f.media.count)
	}
}

func TestPostgresWorkflowVideoBrowser(t *testing.T) {
	if os.Getenv("WORKFLOW_VIDEO_BROWSER_TEST") != "1" {
		t.Skip("set WORKFLOW_VIDEO_BROWSER_TEST=1 with built assets and local Chromium")
	}
	gateway := newWorkflowVideoGateway(t)
	_, server := gateway.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/workflow-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "WORKFLOW_BROWSER_VIDEO=1", "WORKFLOW_BROWSER_URL="+server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("video workflow browser failed: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.frames) != 1 || (!bytes.Equal(gateway.frames[0], gateway.fixture.media.image) && !bytes.Equal(gateway.frames[0], gateway.fixture.media.blockedImage)) {
		t.Fatalf("browser sent %d video requests or unexpected source bytes", len(gateway.frames))
	}
}
