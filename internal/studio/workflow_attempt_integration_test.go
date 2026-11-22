package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresWorkflowAttemptBrowser(t *testing.T) {
	if os.Getenv("WORKFLOW_ATTEMPT_BROWSER_TEST") != "1" {
		t.Skip("set WORKFLOW_ATTEMPT_BROWSER_TEST=1 with local Chromium and built assets")
	}
	f := newWorkflowIntegrationFixture(t)
	_, s := f.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/workflow-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "WORKFLOW_BROWSER_ATTEMPTS=1", "WORKFLOW_BROWSER_URL="+s.URL)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow attempts browser failed: %v\n%s", err, raw)
	}
	t.Log(strings.TrimSpace(string(raw)))
	f.media.mu.Lock()
	defer f.media.mu.Unlock()
	if f.chatCalls.Load() != 1 || f.media.count != 2 {
		t.Fatal("partial rerun repeated inherited text or duplicated a media request")
	}
}

func TestPostgresWorkflowControlReceiptLostAck(t *testing.T) {
	for _, operation := range []string{"cancel", "rerun"} {
		t.Run(operation, func(t *testing.T) {
			f := newWorkflowIntegrationFixture(t)
			direct := f.cfg.DatabaseURL
			admin := deliveryAdmin(t, direct)
			proxy, armed, dropped := recoveryControlAckProxy(t, direct)
			f.cfg.DatabaseURL = proxy
			app, s := f.open(t)
			client := authClient(t)
			owner := registerFixture(t, client, s.URL, "flow-ack@example.test")
			project := owner.Project.ID
			addr := s.URL
			request := func(method, path, key string, body any, want int) []byte {
				t.Helper()
				code, raw, _ := authRequest(t, client, addr, method, path, key, project, body)
				if code != want {
					t.Fatalf("%s %s returned %d, wanted %d: %s", method, path, code, want, raw)
				}
				return raw
			}
			root := "/api/v1/projects/" + project
			def := decode[WorkflowDefinition](t, request("POST", root+"/workflow-definitions", newID("def"), map[string]any{"expected_version": 0, "graph": workflowFixtureGraph(app)}, 201))
			parent := decode[WorkflowExecution](t, request("POST", root+"/workflow-runs", newID("flow"), map[string]any{"definition_id": def.ID, "definition_version": 1, "input": "original scene"}, 201))
			path := root + "/workflow-runs/" + parent.ID
			body := map[string]any{"expected_version": parent.Version}
			if operation == "rerun" {
				stopped := decode[workflowDetail](t, request("POST", path+"/cancel", newID("stop"), body, 200))
				body = map[string]any{"expected_version": stopped.Version, "from_node": "script", "input": "new scene"}
			}
			ctx := context.Background()
			// The two literals are closed test cases. The trigger takes this lock only
			// after the intended action reached its receipt insert in the real transaction.
			_, err := admin.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION hold_control_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN PERFORM pg_advisory_xact_lock(912327,25); RETURN NEW; END $$;
		 CREATE TRIGGER hold_control_receipt BEFORE INSERT ON studio_workflow_receipts
		 FOR EACH ROW WHEN (NEW.operation='%s') EXECUTE FUNCTION hold_control_receipt()`, operation))
			if err != nil {
				t.Fatal(err)
			}
			barrier, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(ctx)
			if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912327,25)"); err != nil {
				t.Fatal(err)
			}
			key := newID("action")
			result := make(chan int, 1)
			go func() {
				code, _, _ := authRequest(t, client, addr, "POST", path+"/"+operation, key, project, body)
				result <- code
			}()
			verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, result)
			app.Close()
			s.Close()
			f.cfg.DatabaseURL = direct
			_, restored := f.open(t)
			addr = restored.URL
			read := decode[WorkflowExecution](t, request("GET", path+"/"+operation+"/receipt", key, nil, 200))
			if operation == "rerun" && (read.ID == parent.ID || read.Status != "TEXT_READY" || read.SourceWorkflowID != parent.ID || read.Input != "new scene") {
				t.Fatal("fork receipt lost exact new attempt")
			}
			if operation == "cancel" && (read.ID != parent.ID || read.Status != "CANCELLED") {
				t.Fatal("cancel receipt lost parent")
			}
			code, _, h := authRequest(t, client, addr, "POST", path+"/"+operation, key, project, body)
			want := http.StatusOK
			if operation == "rerun" {
				want = http.StatusCreated
			}
			if code != want || h.Get("Idempotency-Replayed") != "true" {
				t.Fatal("original operation did not replay", code)
			}
			if outboxCount(t, admin, "SELECT count(*) FROM studio_workflow_receipts WHERE operation_key=$1", key) != 1 ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_runs WHERE project_id=$1", project) != 0 ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_tasks WHERE project_id=$1", project) != 0 || f.chatCalls.Load() != 0 {
				t.Fatal("control receipt/replay triggered additional work")
			}
		})
	}
}

// Kill an actual server after its provider has received the image POST, before the
// response containing a handle. The new process must expose the original uncertain
// child and prevent a fresh workflow attempt even after the parent is cancelled.
func TestPostgresWorkflowKilledAfterSubmitCannotRerun(t *testing.T) {
	var chats, images atomic.Int32
	received := make(chan struct{}, 1)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		chats.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\ndata: [DONE]\n\n", workflowTestText)
	}))
	defer chat.Close()
	end := make(chan struct{})
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != imageGenerationEndpoint {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 128<<10))
		images.Add(1)
		select {
		case received <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-end:
		}
	}))
	defer media.Close()
	defer close(end)
	cfg := durableProcessConfig{DatabaseURL: testDatabase(t), ReferenceDir: t.TempDir(), AssetDir: t.TempDir(), ChatGatewayURL: chat.URL + "/v1", GatewayURL: media.URL}
	child := startDurableChild(t, cfg)
	client := authClient(t)
	id := registerFixture(t, client, child.base, "uncertain-workflow@example.test")
	call := func(method, path, key string, body any, want int) []byte {
		t.Helper()
		code, raw, _ := authRequest(t, client, child.base, method, path, key, id.Project.ID, body)
		if code != want {
			t.Fatalf("%s %s returned %d, wanted %d: %s", method, path, code, want, raw)
		}
		return raw
	}
	bootstrap := decode[struct {
		Models []Model `json:"models"`
	}](t, call("GET", "/api/v1/bootstrap", "", nil, 200))
	g, _ := workflowStoreGraph(t)
	node := map[string]string{}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		node[n.Kind] = n.ID
		for _, m := range bootstrap.Models {
			if m.ID == n.ModelID {
				n.ModelVersion = m.Version
			}
		}
	}
	prefix := "/api/v1/projects/" + id.Project.ID
	def := decode[WorkflowDefinition](t, call("POST", prefix+"/workflow-definitions", newID("def"), map[string]any{"expected_version": 0, "graph": g}, 201))
	run := decode[WorkflowExecution](t, call("POST", prefix+"/workflow-runs", newID("flow"), map[string]any{"definition_id": def.ID, "definition_version": 1, "input": "private scene"}, 201))
	path := prefix + "/workflow-runs/" + run.ID
	wait := func(want string) workflowDetail {
		t.Helper()
		var d workflowDetail
		for until := time.Now().Add(8 * time.Second); time.Now().Before(until); {
			d = decode[workflowDetail](t, call("GET", path, "", nil, 200))
			if d.Status == want {
				return d
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("workflow reached %s instead of %s", d.Status, want)
		return d
	}
	call("POST", path+"/nodes/"+node["text"]+"/execute", newID("script"), map[string]any{"expected_version": run.Version}, 200)
	d := wait("IMAGE_READY")
	imageKey := newID("image")
	call("POST", path+"/nodes/"+node["image"]+"/execute", imageKey, map[string]any{"expected_version": d.Version}, 200)
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("supplier did not receive image POST")
	}
	child.kill(t)
	child = startDurableChild(t, cfg)
	d = wait("REVIEW")
	if d.ImageTask == nil || d.ImageTask.Status != "SUBMIT_UNKNOWN" || d.ImageTaskID == "" {
		t.Fatal("original accepted image was not exposed as submission unknown")
	}
	receipt := decode[workflowDetail](t, call("GET", path+"/nodes/"+node["image"]+"/receipt", imageKey, nil, 200))
	if receipt.ImageTaskID != d.ImageTaskID {
		t.Fatal("child identity changed after process kill")
	}
	stream, closeStream := durableFixtureStream(t, client, child.base, id, "")
	cursor, name, _, ok := durableFixtureFrame(stream)
	closeStream()
	if !ok || name != "stream.reset" || cursor == "" {
		t.Fatal("project stream did not supply initial cursor")
	}
	key := newID("cancel")
	body := map[string]any{"expected_version": d.Version}
	cancelled := decode[workflowDetail](t, call("POST", path+"/cancel", key, body, 200))
	if cancelled.Status != "CANCELLED" || cancelled.ImageTask == nil || cancelled.ImageTask.Status != "SUBMIT_UNKNOWN" {
		t.Fatal("parent cancellation erased uncertain child outcome")
	}
	call("GET", path+"/cancel/receipt", key, nil, 200)
	replay, _, headers := authRequest(t, client, child.base, "POST", path+"/cancel", key, id.Project.ID, body)
	if replay != 200 || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("original cancellation did not replay", replay)
	}
	call("POST", path+"/rerun", newID("retry"), map[string]any{"from_node": node["image"], "expected_version": cancelled.Version}, 409)
	stream, closeStream = durableFixtureStream(t, client, child.base, id, cursor)
	defer closeStream()
	seen := false
	for {
		_, name, data, ok := durableFixtureFrame(stream)
		if !ok {
			break
		}
		if name != "resource.changed" {
			continue
		}
		var e DurableEvent
		if json.Unmarshal([]byte(data), &e) != nil {
			t.Fatal("invalid event frame")
		}
		if e.Kind == "workflow.updated" && e.ResourceID == run.ID && e.Status == "CANCELLED" && e.ResourceVersion == cancelled.Version {
			if strings.Contains(data, "private scene") || strings.Contains(data, imageKey) || strings.Contains(data, key) {
				t.Fatal("workflow feed leaked original input or operation keys")
			}
			seen = true
			break
		}
	}
	if !seen || chats.Load() != 1 || images.Load() != 1 {
		t.Fatal("missing authorized replay or duplicated external request", seen, chats.Load(), images.Load())
	}
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	if n := outboxCount(t, admin, "SELECT count(*) FROM studio_outbox_events WHERE kind='workflow.updated' AND resource_id=$1 AND status='CANCELLED'", run.ID); n != 1 {
		t.Fatal("cancel replay duplicated workflow event", n)
	}
}
