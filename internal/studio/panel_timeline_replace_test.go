package studio

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"aigongzuotai/internal/videoexport"
)

func TestPanelClipReplacementPreservesSiblingAndRejectsInvalidRanges(t *testing.T) {
	asset := libraryID("generated", newID("task"), "source")
	v := VideoTimelineVersion{ID: newID("vtl"), Version: 1, Document: timelineTestInput(asset, asset).Document}
	original := append([]VideoTimelineClip(nil), v.Document.Clips...)
	start, end := int64(40), int64(540)
	in := panelClipReplacement{AssetID: libraryID("generated", newID("task"), "new"), AssetRevision: 2, InMS: &start, OutMS: &end}
	doc, index, err := panelReplacementDocument(v, "shot2", in)
	if err != nil || index != 1 || !reflect.DeepEqual(original, v.Document.Clips) || doc.Clips[0] != original[0] ||
		doc.Clips[1].Title != original[1].Title || doc.Clips[1].ID != original[1].ID || doc.Clips[1].AssetID != in.AssetID || doc.Clips[1].InMS != start || doc.Clips[1].OutMS != end {
		t.Fatal("replacement changed the wrong clip or the original version")
	}
	_, _, err = panelReplacementDocument(v, "missing", in)
	requireOperation(t, err, "VIDEO_TIMELINE_NOT_FOUND")
	end = start
	_, _, err = panelReplacementDocument(v, "shot2", in)
	requireOperation(t, err, "INVALID_VIDEO_TIMELINE")
}

type panelReplaceFixture struct {
	t           *testing.T
	f           *workflowIntegrationFixture
	a           *App
	s           *httptest.Server
	client      *http.Client
	owner       testIdentity
	timeline    VideoTimelineVersion
	firstAsset  string
	quote       string
	reviewer    *reviewDispatchHTTPFixture
	submitFault atomic.Int32
	faultPosts  atomic.Int32
}

func newPanelReplaceFixture(t *testing.T, footage bool) *panelReplaceFixture {
	t.Helper()
	x := &panelReplaceFixture{t: t, f: newWorkflowIntegrationFixture(t), client: authClient(t)}
	f := x.f
	x.reviewer = newReviewDispatchHTTPFixture(t, "")
	f.cfg.MediaModeration = x.reviewer.config(t)
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	const prices = `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-video","kind":"video","operation":"generate","preset_id":"preset-1","artifact_micros":"50000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	if footage {
		f.cfg.VideoExport = videoexport.Config{FFmpegPath: os.Getenv("TEST_VIDEO_FFMPEG"), FFprobePath: os.Getenv("TEST_VIDEO_FFPROBE"), TempDir: t.TempDir()}
		f.media.video = timelineFixtureFootage(t, f.cfg.VideoExport.FFmpegPath, "red", 440)
	}
	// Failure scenarios exercise the ordinary adapter, including a submit
	// whose HTTP connection closes before the gateway acknowledges anything.
	original := f.gateway
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			switch x.submitFault.Swap(0) {
			case 1:
				x.faultPosts.Add(1)
				http.Error(w, "invalid fixture request", http.StatusUnprocessableEntity)
				return
			case 2:
				x.faultPosts.Add(1)
				conn, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = conn.Close()
				}
				return
			}
		}
		original.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.gateway.Close)
	x.a, x.s = f.open(t)
	x.owner = registerFixture(t, x.client, x.s.URL, "panel-edit@example.test")
	httpCredit(t, x.a, x.owner.Workspace.ID, 4000000)
	x.call("POST", x.comic(), "save-storyboard", comicHTTPDraft(""), 201)
	x.call("POST", x.comic()+"/versions/1/confirm", "confirm-storyboard", nil, 200)
	task, _ := acceptMediaExecution(t, x.a, x.s, x.client, x.owner, "video", "Original footage", "old-video", 1)
	task = x.wait(task.ID, "SUCCEEDED")
	if len(task.Artifacts) != 1 {
		t.Fatal("missing original output")
	}
	x.firstAsset = libraryID("generated", task.ID, task.Artifacts[0].ID)
	raw := x.call("POST", x.timelines(), "timeline-v1", timelineTestInput(x.firstAsset, x.firstAsset), 201)
	x.timeline = decode[VideoTimelineVersion](t, raw)
	return x
}
func (x *panelReplaceFixture) comic() string {
	return "/api/v1/projects/" + x.owner.Project.ID + "/comic"
}
func (x *panelReplaceFixture) timelines() string {
	return "/api/v1/projects/" + x.owner.Project.ID + "/video-timelines"
}
func (x *panelReplaceFixture) route(v string) string {
	return x.timelines() + "/" + x.timeline.ID + "/versions/" + v
}
func (x *panelReplaceFixture) replace(v string) string {
	return x.route(v) + "/clips/shot2/replace-from-panel"
}
func (x *panelReplaceFixture) call(method, route, key string, body any, want int) []byte {
	x.t.Helper()
	b, _ := presentationRequest(x.t, x.client, x.s.URL, x.owner.Project.ID, method, route, key, body, want)
	return b
}
func (x *panelReplaceFixture) wait(id, want string) Task {
	x.t.Helper()
	db := deliveryAdmin(x.t, x.f.cfg.DatabaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var current Task
	for ctx.Err() == nil {
		if e := db.QueryRow(ctx, "SELECT document FROM studio_tasks WHERE id=$1", id).Scan(&current); e != nil {
			x.t.Fatal("could not read committed task")
		}
		if current.Status == want {
			return current
		}
		switch current.Status {
		case "FAILED", "CANCELLED", "SUCCEEDED", "PARTIAL", "SUBMIT_UNKNOWN", "RECONCILE_REQUIRED":
			x.t.Fatalf("committed unexpected terminal state %s", current.Status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	x.t.Fatalf("timeout waiting for %s, last committed status %s", want, current.Status)
	return Task{}
}

func (x *panelReplaceFixture) generate(panel, key, want string) Task {
	x.t.Helper()
	base := x.comic() + "/versions/1/panels/" + panel + "/videos"
	model := x.a.catalog.Entries["studio-video"].Model
	body := map[string]any{"model_id": model.ID, "model_version": model.Version, "parameters": TaskParameters{AspectRatio: "16:9", Count: 1, Seconds: 5}}
	var preview struct {
		Attempt int           `json:"attempt"`
		Quote   *BillingQuote `json:"quote"`
	}
	if err := json.Unmarshal(x.call("POST", base+"/quote", key, body, 200), &preview); err != nil || preview.Quote == nil || preview.Quote.ReserveMicros != 50000 || preview.Attempt < 1 {
		x.t.Fatal("missing real panel quote and attempt")
	}
	x.quote = preview.Quote.ID
	body["expected_attempt"], body["quote_id"] = preview.Attempt, x.quote
	raw := x.call("POST", base, key, body, 201)
	var accepted struct {
		TaskID  string `json:"task_id"`
		Attempt int    `json:"attempt"`
		QuoteID string `json:"quote_id"`
	}
	if e := json.Unmarshal(raw, &accepted); e != nil || !validOperationsID(accepted.TaskID, "task") || accepted.Attempt != preview.Attempt || accepted.QuoteID != x.quote {
		x.t.Fatal("wrong video generation receipt")
	}
	var receipt struct {
		TaskID string `json:"task_id"`
	}
	if e := json.Unmarshal(x.call("GET", base+"/receipt", key, nil, 200), &receipt); e != nil || receipt.TaskID != accepted.TaskID {
		x.t.Fatal("original generation key did not identify the same attempt")
	}
	return x.wait(accepted.TaskID, want)
}

func panelReplaceInput(panel string, task Task, asset string) panelClipReplacement {
	start, end := int64(100), int64(350)
	return panelClipReplacement{ComicVersion: 1, PanelID: panel, TaskID: task.ID, AssetID: asset, AssetRevision: 1, InMS: &start, OutMS: &end}
}
func (x *panelReplaceFixture) posted() int {
	x.f.media.mu.Lock()
	defer x.f.media.mu.Unlock()
	return x.f.media.count + int(x.faultPosts.Load())
}

func TestPostgresPanelClipRejectsUnknownFailedForeignAndRecycled(t *testing.T) {
	x := newPanelReplaceFixture(t, false)
	ok := x.generate("east", "video-success", "SUCCEEDED")
	asset := libraryID("generated", ok.ID, ok.Artifacts[0].ID)
	good := panelReplaceInput("east", ok, asset)
	wrongPanel := good
	wrongPanel.PanelID = "last"
	stale := good
	stale.AssetRevision++
	wrongAsset := good
	wrongAsset.AssetID = x.firstAsset
	for i, bad := range []panelClipReplacement{wrongPanel, stale, wrongAsset} {
		key := "invalid-video-" + strconv.Itoa(i)
		x.call("POST", x.replace("1"), key, bad, 409)
		x.call("GET", x.timelines()+"/receipt", key, nil, 404)
	}
	other := authClient(t)
	presentationRequest(t, other, x.s.URL, "", "POST", x.replace("1"), "foreign", good, 401)
	registerFixture(t, other, x.s.URL, "panel-stranger@example.test")
	presentationRequest(t, other, x.s.URL, x.owner.Project.ID, "POST", x.replace("1"), "foreign", good, 404)
	// The library entry alone must not mint a different approved original.
	db := deliveryAdmin(t, x.f.cfg.DatabaseURL)
	ctx := context.Background()
	for _, digest := range []string{moderationSHA([]byte("other content")), moderationSHA(x.f.media.video)} {
		if _, e := db.Exec(ctx, `UPDATE studio_library_entries SET storage_info=jsonb_set(storage_info,'{sha256}',to_jsonb($2::text)) WHERE id=$1`, asset, digest); e != nil {
			t.Fatal("could not configure isolated original identity check")
		}
		if digest != moderationSHA(x.f.media.video) {
			x.call("POST", x.replace("1"), "unconfirmed-digest", good, 409)
			x.call("GET", x.timelines()+"/receipt", "unconfirmed-digest", nil, 404)
		}
	}
	x.submitFault.Store(1)
	failed := x.generate("last", "video-fails", "FAILED")
	x.reviewer.mu.Lock()
	x.reviewer.blockSHA = moderationSHA(x.f.media.video)
	x.reviewer.mu.Unlock()
	isolated := x.generate("last", "video-blocked", "FAILED")
	if isolated.Moderation == nil || isolated.Moderation.State != "BLOCKED" || len(isolated.Artifacts) != 0 {
		t.Fatal("output review did not isolate the submitted panel footage")
	}
	x.submitFault.Store(2)
	unknown := x.generate("east", "video-unknown", "SUBMIT_UNKNOWN")
	for _, bad := range []panelClipReplacement{panelReplaceInput("last", failed, asset), panelReplaceInput("last", isolated, asset), panelReplaceInput("east", unknown, asset)} {
		x.call("POST", x.replace("1"), "no-committed-replacement", bad, 409)
		x.call("GET", x.timelines()+"/receipt", "no-committed-replacement", nil, 404)
	}
	x.call("POST", "/api/v1/library/"+asset+"/trash", "trash-panel-result", stateInput(1), 200)
	x.call("POST", x.replace("1"), "trashed-replacement", good, 409)
	x.call("GET", x.timelines()+"/receipt", "trashed-replacement", nil, 404)
	if x.posted() != 5 || !reflect.DeepEqual(decode[VideoTimelineVersion](t, x.call("GET", x.route("1"), "", nil, 200)), x.timeline) {
		t.Fatal("failed replacement changed the original or generated again")
	}
	x.call("GET", x.route("2"), "", nil, 404)
}

// The handler commits normally, then this dedicated endpoint closes only
// its own connection before transmitting a response to the client.
func (x *panelReplaceFixture) loseReplaceResponse(key string, input panelClipReplacement) {
	x.t.Helper()
	committed := make(chan int, 1)
	interrupted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := httptest.NewRecorder()
		x.s.Config.Handler.ServeHTTP(record, r)
		committed <- record.Code
		conn, _, e := w.(http.Hijacker).Hijack()
		if e == nil {
			_ = conn.Close()
		}
	}))
	defer interrupted.Close()
	data, e := json.Marshal(input)
	if e != nil {
		x.t.Fatal(e)
	}
	req, e := http.NewRequest("POST", interrupted.URL+x.replace("1"), bytes.NewReader(data))
	if e != nil {
		x.t.Fatal(e)
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Project-ID", x.owner.Project.ID)
	resp, e := x.client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if e == nil {
		x.t.Fatal("replacement response unexpectedly reached caller")
	}
	select {
	case code := <-committed:
		if code != 201 {
			x.t.Fatalf("replacement was not committed (%d)", code)
		}
	case <-time.After(15 * time.Second):
		x.t.Fatal("missing server outcome")
	}
}

func verifyPanelMovie(t *testing.T, ffmpeg string, mp4 []byte, wantBlueSecond bool) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "two-clips.mp4")
	if e := os.WriteFile(file, mp4, 0600); e != nil {
		t.Fatal(e)
	}
	sample := func(position string, args ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := []string{"-nostdin", "-v", "error", "-threads", "1", "-i", file, "-ss", position}
		raw, e := exec.CommandContext(ctx, ffmpeg, append(append(cmd, args...), "pipe:1")...).Output()
		if e != nil {
			t.Fatal("could not independently decode the exported video")
		}
		return raw
	}
	for i, at := range []string{"0.060", "0.310"} {
		pixel := sample(at, "-an", "-frames:v", "1", "-vf", "scale=1:1", "-pix_fmt", "rgb24", "-f", "rawvideo")
		blue := i == 1 && wantBlueSecond
		if len(pixel) != 3 || (blue && (pixel[2] < 150 || pixel[0] > 70)) || (!blue && (pixel[0] < 150 || pixel[2] > 70)) {
			t.Fatal("decoded picture does not match the selected clip", i+1)
		}
		pcm := sample(at, "-vn", "-t", "0.125", "-ac", "1", "-ar", "48000", "-f", "s16le")
		if len(pcm) != 12000 {
			t.Fatal("incomplete decoded audio window", i+1)
		}
		n := 0
		prev := int16(binary.LittleEndian.Uint16(pcm))
		for j := 2; j < len(pcm); j += 2 {
			s := int16(binary.LittleEndian.Uint16(pcm[j:]))
			if prev <= 0 && s > 0 {
				n++
			}
			prev = s
		}
		wanted := 440
		if blue {
			wanted = 880
		}
		if n*8 < wanted-24 || n*8 > wanted+24 {
			t.Fatalf("unexpected audio in clip %d: %d Hz", i+1, n*8)
		}
	}
}

func TestPanelClipReplacementIndependentDecoder(t *testing.T) {
	cfg := videoexport.Config{FFmpegPath: os.Getenv("TEST_VIDEO_FFMPEG"), FFprobePath: os.Getenv("TEST_VIDEO_FFPROBE"), TempDir: t.TempDir()}
	if cfg.FFmpegPath == "" || cfg.FFprobePath == "" {
		t.Skip("requires local video tools")
	}
	executor, err := videoexport.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var clips []videoexport.Clip
	for i, color := range []string{"red", "blue"} {
		raw := timelineFixtureFootage(t, cfg.FFmpegPath, color, (i+1)*440)
		file := filepath.Join(t.TempDir(), "source.mp4")
		if err = os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		clips = append(clips, videoexport.Clip{Source: source, ExpectedSHA256: moderationSHA(raw), ByteSize: int64(len(raw)), StartMS: 100, EndMS: 350})
	}
	for _, second := range []int{0, 1} {
		file, err := os.CreateTemp(t.TempDir(), "result-*.mp4")
		if err != nil {
			t.Fatal(err)
		}
		_, err = executor.Export(t.Context(), []videoexport.Clip{clips[0], clips[second]}, file)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal("local test export failed", err, closeErr)
		}
		raw, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		verifyPanelMovie(t, cfg.FFmpegPath, raw, second == 1)
	}
}

func (x *panelReplaceFixture) verifyAccounted(taskID, quote string) {
	x.t.Helper()
	db := deliveryAdmin(x.t, x.f.cfg.DatabaseURL)
	if x.posted() != 2 || outboxCount(x.t, db, "SELECT count(*) FROM studio_tasks") != 2 ||
		outboxCount(x.t, db, "SELECT count(*) FROM studio_comic_panel_videos WHERE task_id=$1 AND quote_id=$2", taskID, quote) != 1 ||
		outboxCount(x.t, db, `SELECT count(*) FROM studio_ledger_reservations WHERE resource_id=$1 AND quote_id=$2
		 AND state='SETTLED' AND reserved_micros=50000 AND charged_micros=50000 AND released_micros=0 AND refunded_micros=0`, taskID, quote) != 1 ||
		outboxCount(x.t, db, "SELECT count(*) FROM studio_ledger_journals WHERE resource_id=$1 AND kind='RESERVE' AND amount_micros=50000", taskID) != 1 ||
		outboxCount(x.t, db, "SELECT count(*) FROM studio_ledger_journals WHERE resource_id=$1 AND kind='CHARGE' AND amount_micros=50000", taskID) != 1 ||
		outboxCount(x.t, db, "SELECT count(*) FROM studio_video_timeline_versions") != 2 {
		x.t.Fatal("unexpected generation POST count, settled amount, source attempt or timeline version count")
	}
}

func TestPostgresPanelClipReplacementReceiptsDecodedExportAndRestore(t *testing.T) {
	if os.Getenv("TEST_VIDEO_FFMPEG") == "" || os.Getenv("TEST_VIDEO_FFPROBE") == "" || os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("requires local ffmpeg, ffprobe and PostgreSQL backup tools")
	}
	x := newPanelReplaceFixture(t, true)
	blue := timelineFixtureFootage(t, x.f.cfg.VideoExport.FFmpegPath, "blue", 880)
	x.f.media.mu.Lock()
	x.f.media.video = blue
	x.f.media.mu.Unlock()
	task := x.generate("east", "specific-panel-generation", "SUCCEEDED")
	if task.Moderation == nil || task.Moderation.State != "ALLOWED" || len(task.Artifacts) != 1 {
		t.Fatal("panel delivery was not approved")
	}
	input := panelReplaceInput("east", task, libraryID("generated", task.ID, task.Artifacts[0].ID))
	const key = "replace-only-shot-two"
	x.loseReplaceResponse(key, input)
	before := x.timeline
	after := decode[VideoTimelineVersion](t, x.call("GET", x.timelines()+"/receipt", key, nil, 200))
	if after.Version != 2 || after.ID != before.ID || after.Document.Title != before.Document.Title || len(after.Document.Clips) != 2 ||
		!reflect.DeepEqual(after.Document.Clips[0], before.Document.Clips[0]) || after.Document.Clips[1].ID != before.Document.Clips[1].ID ||
		after.Document.Clips[1].Title != before.Document.Clips[1].Title || after.Document.Clips[1].AssetID != input.AssetID ||
		after.Document.Clips[1].ContentVersion != moderationSHA(blue) {
		t.Fatal("the operation did not replace exactly one frozen clip")
	}
	x.call("POST", x.replace("1"), "stale-panel-edit", input, 409)
	x.call("GET", x.timelines()+"/receipt", "stale-panel-edit", nil, 404)
	mutated := input
	mutated.PanelID = "last"
	x.call("POST", x.replace("1"), key, mutated, 409)
	compare := func() {
		t.Helper()
		for i, want := range []VideoTimelineVersion{before, after} {
			base := x.route(strconv.Itoa(i + 1))
			if !reflect.DeepEqual(want, decode[VideoTimelineVersion](t, x.call("GET", base, "", nil, 200))) {
				t.Fatal("stored fixed revision changed")
			}
			mp4, h := presentationRequest(t, x.client, x.s.URL, x.owner.Project.ID, "POST", base+"/export", "", nil, 200)
			if h.Get("Content-Type") != "video/mp4" || h.Get("X-Video-Duration-Ms") != "500" || h.Get("X-Content-SHA256") != moderationSHA(mp4) {
				t.Fatal("export identity or duration changed")
			}
			verifyPanelMovie(t, x.f.cfg.VideoExport.FFmpegPath, mp4, i == 1)
		}
		x.verifyAccounted(task.ID, x.quote)
	}
	compare()
	x.s.Close()
	x.a.Close()
	restoreWorkflowFixture(t, x.f, "studio_video_timelines", "studio_video_timeline_versions", "studio_video_timeline_clips", "studio_video_timeline_keys",
		"studio_comics", "studio_comic_versions", "studio_comic_panel_videos", "studio_tasks", "studio_task_media_moderation", "studio_ledger_quotes", "studio_ledger_reservations", "studio_ledger_journals")
	x.a, x.s = x.f.open(t)
	x.client = authClient(t)
	x.call("POST", "/api/v1/auth/login", "", map[string]string{"email": "panel-edit@example.test", "password": "fixture-password-123"}, 200)
	if !reflect.DeepEqual(after, decode[VideoTimelineVersion](t, x.call("GET", x.timelines()+"/receipt", key, nil, 200))) {
		t.Fatal("recovered receipt did not identify original revision")
	}
	compare()
	x.call("POST", "/api/v1/library/"+input.AssetID+"/trash", "revoke-panel-result", stateInput(1), 200)
	raw, h := presentationRequest(t, x.client, x.s.URL, x.owner.Project.ID, "POST", x.replace("1"), key, input, 201)
	replay := decode[VideoTimelineVersion](t, raw)
	if replay.Version != 2 || h.Get("Idempotency-Replayed") != "true" || replay.Document.Clips[1].AssetAvailable == nil || *replay.Document.Clips[1].AssetAvailable {
		t.Fatal("revoked delivery did not retain its original read-only receipt and current visibility")
	}
	x.call("GET", x.route("2")+"/clips/shot2/source", "", nil, 409)
	x.call("POST", x.route("2")+"/export", "", nil, 409)
	x.verifyAccounted(task.ID, x.quote)
}
