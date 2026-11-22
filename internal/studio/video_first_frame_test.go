package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func firstFrameFixtureInput() TaskInput {
	in := mediaFixtureInput("video", 1)
	in.Operation = "first_frame"
	in.Parameters.Seconds = 5
	in.Parameters.Size = "1280x720"
	in.Parameters.Resolution = "720p"
	in.Inputs.WorkflowID = "workflow-private-fixture"
	in.Inputs.FirstFrame = &WorkflowCandidate{AssetID: "private-library-id", ArtifactID: "private-artifact-id", TaskID: "private-task-id", ContentVersion: "private-version", Revision: 3}
	return in
}

func TestAsyncVideoFirstFrameMultipartBytesAndOriginalHandle(t *testing.T) {
	in := firstFrameFixtureInput()
	images := [][]byte{referencePNG(t, 21), referencePNG(t, 201)}
	type received struct {
		key, hash string
		image     []byte
	}
	var mu sync.Mutex
	var posts []received
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			t.Error("wrong fixture credential")
		}
		if r.Method == "GET" {
			mu.Lock()
			queries = append(queries, r.URL.Path)
			mu.Unlock()
			fmt.Fprintf(w, `{"task_id":%q,"media_type":"video","stage":"generating","status":"processing","progress":20}`, strings.TrimPrefix(r.URL.Path, mediaQueryPrefix))
			return
		}
		if r.Method != "POST" || r.URL.Path != videoGenerationEndpoint {
			t.Error("wrong video endpoint")
			w.WriteHeader(404)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if bytes.Contains(raw, []byte("private-library-id")) || bytes.Contains(raw, []byte("workflow-private-fixture")) {
			t.Error("internal provenance escaped to gateway")
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		parts, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		fields := map[string]string{}
		var names []string
		var receivedImage []byte
		for {
			part, err := parts.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			b, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
				return
			}
			names = append(names, part.FormName())
			if part.FormName() == "input_reference" {
				if part.FileName() != "first-frame.png" || part.Header.Get("Content-Type") != "image/png" {
					t.Error("unexpected first-frame part")
				}
				receivedImage = b
			} else {
				fields[part.FormName()] = string(b)
			}
		}
		if !reflect.DeepEqual(names, []string{"model", "prompt", "seconds", "aspect_ratio", "size", "resolution", "input_reference"}) ||
			!reflect.DeepEqual(fields, map[string]string{"model": "video-v1", "prompt": in.Inputs.Prompt, "seconds": "5", "aspect_ratio": "16:9", "size": "1280x720", "resolution": "720p"}) {
			t.Error("wrong multipart fields", names, fields)
		}
		hash := sha256.Sum256(raw)
		mu.Lock()
		posts = append(posts, received{r.Header.Get("Idempotency-Key"), hex.EncodeToString(hash[:]), receivedImage})
		id := len(posts)
		mu.Unlock()
		w.WriteHeader(202)
		fmt.Fprintf(w, `{"task_id":"video-%d","query_url":"https://untrusted.invalid/query"}`, id)
	}))
	defer srv.Close()
	provider := mediaFixtureProvider(t, srv)
	store := &mediaFixtureBodyStore{directory: t.TempDir()}
	provider.bodies = store
	restarted, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: srv.URL, APIKey: fixtureMediaKey, CredentialID: "fixture-credential", AssetDir: t.TempDir(), BodyStore: store, AllowNoDefaultModel: true, allowLocalAssets: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	for i, original := range images {
		frozen, err := provider.PrepareFirstFrameModel(context.Background(), in, "video-v1", mediaFixtureReference(original))
		if err != nil {
			t.Fatal(err)
		}
		serialized, err := json.Marshal(frozen)
		if err != nil || len(frozen.Body) != 0 || frozen.BodyFileID == "" || bytes.Contains(serialized, original) || bytes.Contains(serialized, []byte(fixtureMediaKey)) {
			t.Fatal("invalid frozen metadata", err)
		}
		var restored MediaSubmission
		if err = json.Unmarshal(serialized, &restored); err != nil {
			t.Fatal(err)
		}
		handle, err := restarted.SubmitPrepared(context.Background(), restored)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = restarted.QueryStatus(context.Background(), handle); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		valid := len(posts) == i+1 && len(queries) == i+1 && posts[i].key == frozen.IdempotencyKey && posts[i].hash == frozen.BodySHA256 && bytes.Equal(posts[i].image, original) && queries[i] == mediaQueryPrefix+"video-"+strconv.Itoa(i+1)
		mu.Unlock()
		if !valid {
			t.Fatal("gateway received a different image or handle after restart")
		}
	}
}

func TestAsyncVideoFirstFrameRejectsMissingChangedOrNonfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid frame made an upstream request") }))
	defer srv.Close()
	p := mediaFixtureProvider(t, srv)
	store := &mediaFixtureBodyStore{directory: t.TempDir()}
	p.bodies = store
	in := firstFrameFixtureInput()
	source := mediaFixtureReference(fixturePNG(t))
	if _, err := p.PrepareModel(in, "video-v1"); err == nil {
		t.Fatal("first-frame request fell through to text-only JSON")
	}
	frozen, err := p.PrepareFirstFrameModel(context.Background(), in, "video-v1", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*MediaSubmission){
		func(f *MediaSubmission) { f.InputType = "" },
		func(f *MediaSubmission) { f.Endpoint = imageEditEndpoint; f.Kind = "image" },
		func(f *MediaSubmission) { f.BodyFileID = ""; f.Body = []byte("{}") },
	} {
		changed := frozen
		mutate(&changed)
		if _, err = p.SubmitPrepared(context.Background(), changed); err == nil || !requireMediaError(t, err).ReconcileRequired {
			t.Fatal("incompatible request identity accepted", err)
		}
	}
	path := filepath.Join(store.directory, frozen.BodyFileID)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 1
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.SubmitPrepared(context.Background(), frozen); err == nil || !requireMediaError(t, err).ReconcileRequired {
		t.Fatal("modified bytes submitted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = p.SubmitPrepared(context.Background(), frozen); err == nil || !requireMediaError(t, err).ReconcileRequired {
		t.Fatal("missing file submitted")
	}
	source.ByteSize++
	if _, err = p.PrepareFirstFrameModel(context.Background(), in, "video-v1", source); err == nil {
		t.Fatal("wrong source size accepted")
	}
}

func TestVideoFirstFrameReadsRegisteredS3Version(t *testing.T) {
	for _, mode := range []string{"ok", "tamper"} {
		t.Run(mode, func(t *testing.T) {
			store, fixture, dir := newMediaModerationS3Fixture(t, mode)
			app := App{assets: store}
			r, err := app.firstFrameSource(context.Background(), fixture.info).Open()
			if mode != "ok" {
				if err == nil {
					r.Close()
					t.Fatal("modified remote frame was accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				b, e := io.ReadAll(r)
				r.Close()
				if e != nil || !bytes.Equal(b, fixture.body) {
					t.Fatal("selected original bytes changed", e)
				}
			}
			fixture.mu.Lock()
			verified := fixture.conditionOK && fixture.gets == 1 && fixture.heads == 0
			fixture.mu.Unlock()
			if !verified {
				t.Fatal("source did not read the original private object version")
			}
			assertMediaModerationNoTemps(t, dir)
		})
	}
}

func TestPostgresWorkflowVideoFirstFrameQuoteAndOriginalReceipt(t *testing.T) {
	gw := newWorkflowVideoGateway(t)
	f := gw.fixture
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	prices := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"4000000"},{"model_id":"studio-image","kind":"image","operation":"generate","preset_id":"preset-3","artifact_micros":"10000"},{"model_id":"studio-video","kind":"video","operation":"generate","preset_id":"preset-1","artifact_micros":"50000"},{"model_id":"studio-video","kind":"video","operation":"first_frame","preset_id":"preset-1","artifact_micros":"300000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	a, s := gw.open(t)
	a.chat = &moderatedExecutionChat{text: workflowTestText}
	c := authClient(t)
	id := registerFixture(t, c, s.URL, "first-frame-quote@example.test")
	httpCredit(t, a, id.Workspace.ID, 1_000_000)
	w := workflowStepTest{t: t, c: c, server: s, project: id.Project.ID}
	root := "/api/v1/projects/" + w.project
	_, body, _ := w.request(201, "POST", root+"/workflow-definitions", newID("define"), map[string]any{"expected_version": 0, "graph": workflowVideoGraph(a)})
	def := decode[WorkflowDefinition](t, body)
	_, body, _ = w.request(201, "POST", root+"/workflow-runs", newID("start"), workflowStartInput{DefinitionID: def.ID, DefinitionVersion: def.Version, Input: "a coastal scene"})
	w.run = decode[WorkflowExecution](t, body)
	w.path = root + "/workflow-runs/" + w.run.ID
	q := httpBillingQuote(t, c, s, w.project, "run", w.input("script").Run)
	body, _ = w.execute(w.run.Version, "script", newID("step"), q.ID, 200)
	awaitAuthRun(t, c, s.URL, decode[workflowDetail](t, body).TextRunID)
	d := w.read()
	q = httpBillingQuote(t, c, s, w.project, "task", w.input("picture").Task)
	body, _ = w.execute(d.Version, "picture", newID("step"), q.ID, 200)
	laneTaskStatus(t, a, decode[workflowDetail](t, body).ImageTaskID, "SUCCEEDED")
	d = w.read()
	if d.Status != "WAITING_APPROVAL" || len(d.Candidates) != 2 {
		t.Fatal("missing approved image selection", d)
	}
	selected := d.Candidates[0]
	w.request(200, "POST", w.path+"/nodes/pick/confirm", newID("select"), map[string]any{"expected_version": d.Version, "asset_id": selected.AssetID, "revision": selected.Revision})
	d = w.read()
	input := *w.input("movie").Task
	if d.Status != "VIDEO_READY" || input.Inputs.FirstFrame == nil || *input.Inputs.FirstFrame != selected {
		t.Fatal("wrong frozen video selection")
	}
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	quotesBefore := outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes")
	for _, wrongInput := range []func(*TaskInput){func(v *TaskInput) { v.Inputs.Prompt += " edited" }, func(v *TaskInput) { v.Parameters.Seconds++ }} {
		mutated := input
		wrongInput(&mutated)
		w.request(409, "POST", "/api/v1/billing/quotes", "", map[string]any{"resource_type": "task", "task_input": mutated})
	}
	if outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes") != quotesBefore {
		t.Fatal("changed source contract received a quote")
	}
	w.execute(d.Version, "movie", "missing-video-quote", "", 422)
	textOnly := input
	textOnly.Operation = "generate"
	textOnly.Inputs.WorkflowID = ""
	textOnly.Inputs.FirstFrame = nil
	wrong := httpBillingQuote(t, c, s, w.project, "task", textOnly)
	w.execute(d.Version, "movie", "wrong-video-quote", wrong.ID, 409)
	if outboxCount(t, db, "SELECT count(*) FROM studio_tasks WHERE document->>'kind'='video'") != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", wrong.ID) != 1 {
		t.Fatal("text-only quote paid for a first-frame video")
	}
	q = httpBillingQuote(t, c, s, w.project, "task", input)
	if q.ReserveMicros != 300_000 {
		t.Fatal("first-frame rate substituted", q)
	}
	body, _ = w.execute(d.Version, "movie", "accept-video-quote", q.ID, 200)
	accepted := decode[workflowDetail](t, body)
	task := laneTaskStatus(t, a, accepted.VideoTaskID, "SUCCEEDED")
	if task.FirstFrame == nil || *task.FirstFrame != selected || task.Billing == nil || task.Billing.State != "SETTLED" || task.Billing.ChargedMicros != q.ReserveMicros {
		t.Fatal("wrong source or settlement", task)
	}
	body, h := w.execute(d.Version, "movie", "accept-video-quote", q.ID, 200)
	if h.Get("Idempotency-Replayed") != "true" || decode[workflowDetail](t, body).VideoTaskID != task.ID {
		t.Fatal("video did not replay original resource")
	}
	w.request(200, "GET", w.path+"/nodes/movie/receipt", "accept-video-quote", nil)
	gw.mu.Lock()
	posts := len(gw.frames)
	gw.mu.Unlock()
	if posts != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_journals WHERE resource_id=$1 AND kind IN ('RESERVE','CHARGE')", task.ID) != 2 {
		t.Fatal("workflow video submitted or charged more than once")
	}
}
