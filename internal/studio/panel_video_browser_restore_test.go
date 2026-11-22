package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"aigongzuotai/internal/videoexport"
)

// The browser receives a new batch of encoded footage from the real local
// HTTP media fixture. Keep the approved source and both exports for a second
// decoder to check the original shot positions, colour and audio.
func TestPostgresPanelVideoBrowserAndRestore038(t *testing.T) {
	if os.Getenv("PANEL_VIDEO_BROWSER_TEST") != "1" {
		t.Skip("requires Chromium, PostgreSQL tools and offline media codecs")
	}
	for _, name := range []string{"TEST_DATABASE_URL", "TEST_POSTGRES_BIN_DIR", "TEST_VIDEO_FFMPEG", "TEST_VIDEO_FFPROBE"} {
		if os.Getenv(name) == "" {
			t.Fatalf("explicit panel video acceptance requires %s", name)
		}
	}
	f := newWorkflowIntegrationFixture(t)
	f.cfg.VideoExport = videoexport.Config{FFmpegPath: os.Getenv("TEST_VIDEO_FFMPEG"), FFprobePath: os.Getenv("TEST_VIDEO_FFPROBE"), TempDir: t.TempDir()}
	out := os.Getenv("PANEL_VIDEO_BROWSER_OUTPUT")
	if out == "" {
		out = t.TempDir()
	}
	if !filepath.IsAbs(out) {
		t.Fatal("choose an absolute owned output directory")
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	red := timelineFixtureFootage(t, f.cfg.VideoExport.FFmpegPath, "red", 440)
	blue := timelineFixtureFootage(t, f.cfg.VideoExport.FFmpegPath, "blue", 880)
	for name, data := range map[string][]byte{"red": red, "blue": blue} {
		if err := os.WriteFile(filepath.Join(out, "source-"+name+".mp4"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.media.mu.Lock()
	f.media.video = red
	f.media.mu.Unlock()
	a, s := f.open(t)
	client := authClient(t)
	const email = "panel-video-browser@example.test"
	owner := registerFixture(t, client, s.URL, email)
	project := owner.Project.ID
	_, redAsset := timelineTestSource(t, a, s, client, owner)
	comicAPI := "/api/v1/projects/" + project + "/comic"
	draft := ComicDocument{Title: "归航", Story: "海港迎来清晨。",
		Characters: []ComicCard{{ID: "keeper", Name: "守灯人", Description: "穿着黄色雨衣。"}},
		Scenes:     []ComicCard{{ID: "harbor", Name: "旧港", Description: "有白色灯塔的码头。"}},
		Panels: []ComicPanel{{ID: "wide", Title: "码头全景", SceneID: "harbor", CharacterIDs: []string{"keeper"}, Action: "沿着码头行走。"},
			{ID: "arrival", Title: "归船靠岸", SceneID: "harbor", CharacterIDs: []string{"keeper"}, Action: "提灯招手。", Dialogue: "靠岸吧。"}}}
	var zero uint64
	presentationRequest(t, client, s.URL, project, "POST", comicAPI, "browser-panel-fixed-source", ComicSaveInput{ExpectedVersion: &zero, Document: draft}, 201)
	presentationRequest(t, client, s.URL, project, "POST", comicAPI+"/versions/1/confirm", "browser-panel-author-confirms", nil, 200)
	one := uint64(1)
	draft.Panels[1].Action = "新草案中的动作"
	presentationRequest(t, client, s.URL, project, "POST", comicAPI, "browser-panel-later-unconfirmed", ComicSaveInput{ExpectedVersion: &one, Document: draft}, 201)
	timelines := "/api/v1/projects/" + project + "/video-timelines"
	in := timelineTestInput(redAsset, redAsset)
	in.Document.Title = "归航初剪"
	in.Document.Clips[0].Title = "保留的码头全景"
	in.Document.Clips[0].InMS = 250
	in.Document.Clips[0].OutMS = 1250
	in.Document.Clips[1].Title = "替换的归船镜头"
	in.Document.Clips[1].InMS = 500
	in.Document.Clips[1].OutMS = 1750
	raw, _ := presentationRequest(t, client, s.URL, project, "POST", timelines, "browser-original-two-shot-timeline", in, 201)
	original := decode[VideoTimelineVersion](t, raw)
	originalView, _ := presentationRequest(t, client, s.URL, project, "GET", timelines+"/"+original.ID+"/versions/1", "", nil, 200)
	s.Close()
	a.Close()
	// Seed footage is historical. Only the explicit, ordinary panel generation
	// in the browser should reserve and settle a newly priced output.
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	price := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-video","kind":"video","operation":"generate","preset_id":"preset-1","artifact_micros":"200000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(price), 0600); err != nil {
		t.Fatal(err)
	}
	f.media.mu.Lock()
	f.media.video = blue
	f.media.mu.Unlock()
	a, s = f.open(t)
	httpCredit(t, a, owner.Workspace.ID, 1_000_000)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	receiptFile := filepath.Join(t.TempDir(), "original-operations.json")
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/panel-video-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	settings := map[string]string{
		"PANEL_PROBE_ORIGIN": s.URL, "PANEL_PROBE_EMAIL": email, "PANEL_PROBE_PASSWORD": "fixture-password-123",
		"PANEL_PROBE_PROJECT": project, "PANEL_PROBE_TIMELINE": original.ID, "PANEL_PROBE_OUTPUT": out, "PANEL_PROBE_RECEIPTS": receiptFile,
	}
	for _, key := range []string{"HOME", "TMPDIR", "XDG_RUNTIME_DIR", "LOCALAPPDATA", "APPDATA", "WORKFLOW_CHROME"} {
		if value, ok := os.LookupEnv(key); ok {
			settings[key] = value
		}
	}
	cmd.Env = childFixtureEnvironment(settings)
	stdout, err := cmd.CombinedOutput()
	if bytes.Contains(stdout, []byte("fixture-password-123")) {
		t.Fatal("browser output included a fixture credential")
	}
	if err != nil {
		t.Fatalf("local browser did not complete: %s", stdout)
	}
	var keys struct{ Generate, Replace string }
	keyBytes, err := os.ReadFile(receiptFile)
	if err != nil || json.Unmarshal(keyBytes, &keys) != nil || keys.Generate == "" || keys.Replace == "" || keys.Generate == keys.Replace {
		t.Fatal("browser did not retain two distinct original operation keys")
	}
	gen := comicAPI + "/versions/1/panels/arrival/videos/receipt"
	taskReceipt, _ := presentationRequest(t, client, s.URL, project, "GET", gen, keys.Generate, nil, 200)
	var accepted struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(taskReceipt, &accepted) != nil || accepted.TaskID == "" {
		t.Fatal("missing original panel task")
	}
	v2raw, _ := presentationRequest(t, client, s.URL, project, "GET", timelines+"/receipt", keys.Replace, nil, 200)
	v2 := decode[VideoTimelineVersion](t, v2raw)
	if v2.ID != original.ID || v2.Version != 2 || len(v2.Document.Clips) != 2 || v2.Document.Clips[0].AssetID != redAsset || v2.Document.Clips[0].ContentVersion != original.Document.Clips[0].ContentVersion || v2.Document.Clips[1].AssetID == redAsset || v2.Document.Clips[1].ID != original.Document.Clips[1].ID {
		t.Fatal("replacement did not preserve original shot and timeline identities")
	}
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	if outboxCount(t, db, "SELECT count(*) FROM studio_comic_panel_videos WHERE project_id=$1 AND task_id=$2", project, accepted.TaskID) != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations WHERE resource_id=$1 AND state='SETTLED' AND charged_micros=200000", accepted.TaskID) != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 2 {
		t.Fatal("unexpected number of panel generation tasks, bindings or settlements")
	}
	s.Close()
	a.Close()
	restoreWorkflowFixture(t, f, "studio_comic_panel_videos", "studio_comic_versions", "studio_comic_receipts", "studio_video_timeline_versions", "studio_video_timeline_keys", "studio_tasks", "studio_ledger_reservations", "studio_media_delivery_items")
	_, restored := f.open(t)
	client = authClient(t)
	presentationRequest(t, client, restored.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	gotTask, _ := presentationRequest(t, client, restored.URL, project, "GET", gen, keys.Generate, nil, 200)
	gotV2, _ := presentationRequest(t, client, restored.URL, project, "GET", timelines+"/receipt", keys.Replace, nil, 200)
	gotV1, _ := presentationRequest(t, client, restored.URL, project, "GET", timelines+"/"+original.ID+"/versions/1", "", nil, 200)
	if !bytes.Equal(taskReceipt, gotTask) || !bytes.Equal(v2raw, gotV2) || !bytes.Equal(originalView, gotV1) {
		t.Fatal("restored original records differ")
	}
	for _, test := range []struct {
		path  string
		bytes []byte
	}{{"/versions/1/clips/shot2/source", red}, {"/versions/2/clips/shot1/source", red}, {"/versions/2/clips/shot2/source", blue}} {
		status, raw, _ := authRequest(t, client, restored.URL, "GET", timelines+"/"+original.ID+test.path, "", project, nil)
		if status != 200 || !bytes.Equal(raw, test.bytes) {
			t.Fatal("restored authorized footage differs")
		}
	}
	f.media.mu.Lock()
	posts := f.media.count
	f.media.mu.Unlock()
	if posts != 2 {
		t.Fatal("unexpected model POST after browser, export or restore", posts)
	}
	decodeCmd := exec.CommandContext(ctx, "python", "scripts/verify-video-edit.py", out, "--ffmpeg", f.cfg.VideoExport.FFmpegPath, "--ffprobe", f.cfg.VideoExport.FFprobePath, "--panel-replacement")
	decodeCmd.Dir = filepath.Join("..", "..")
	result, err := decodeCmd.Output()
	if err != nil {
		t.Fatal("independent picture and audio decoding failed", err)
	}
	if !json.Valid(result) {
		t.Fatal("decoder returned invalid evidence")
	}
	if err := os.WriteFile(filepath.Join(out, "panel-decoded.json"), result, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("two authorized MP4 revisions decoded; original receipts restored; one panel generation and one 200000 micro-CREDIT settlement")
}
