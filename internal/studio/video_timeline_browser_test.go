package studio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aigongzuotai/internal/videoexport"
)

func timelineFixtureFootage(t *testing.T, executable, color string, pitch int) []byte {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "source.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "color="+color+":size=320x180:rate=24:duration=3",
		"-f", "lavfi", "-i", "color=white:size=32x32:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency="+strconv.Itoa(pitch)+":sample_rate=48000:duration=3",
		"-filter_complex", "[0:v][1:v]overlay=x=10+3*n:y=16[v]", "-map", "[v]", "-map", "2:a",
		"-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-g", "96", "-c:a", "aac",
		"-movflags", "+faststart", filename)
	if err := cmd.Run(); err != nil {
		t.Fatalf("encode local fixture: %v", err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPostgresVideoTimelineBrowserExport(t *testing.T) {
	if os.Getenv("VIDEO_EDIT_BROWSER_TEST") != "1" {
		t.Skip("requires Chromium and local media tools")
	}
	f := newWorkflowIntegrationFixture(t)
	f.cfg.VideoExport = videoexport.Config{
		FFmpegPath: os.Getenv("TEST_VIDEO_FFMPEG"), FFprobePath: os.Getenv("TEST_VIDEO_FFPROBE"), TempDir: t.TempDir(),
	}
	app, server := f.open(t)
	ownerClient := authClient(t)
	const email = "video-edit-browser@example.test"
	owner := registerFixture(t, ownerClient, server.URL, email)
	output := os.Getenv("VIDEO_EDIT_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("VIDEO_EDIT_BROWSER_OUTPUT must be absolute")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, color := range []string{"red", "blue"} {
		data := timelineFixtureFootage(t, f.cfg.VideoExport.FFmpegPath, color, 440*(i+1))
		if err := os.WriteFile(filepath.Join(output, "source-"+color+".mp4"), data, 0600); err != nil {
			t.Fatal(err)
		}
		f.media.mu.Lock()
		f.media.video = data
		f.media.mu.Unlock()
		task, _ := acceptMediaExecution(t, app, server, ownerClient, owner, "video", "Isolated "+color+" source", "timeline-original-"+color, 1)
		result := laneTaskStatus(t, app, task.ID, "SUCCEEDED")
		if result.Moderation == nil || result.Moderation.State != "ALLOWED" || len(result.Artifacts) != 1 {
			t.Fatal("an approved, original video must exist before editing")
		}
		ids = append(ids, libraryID("generated", result.ID, result.Artifacts[0].ID))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/video-edit-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "VIDEO_EDIT_BROWSER_URL="+server.URL, "VIDEO_EDIT_BROWSER_OUTPUT="+output,
		"VIDEO_EDIT_EMAIL="+email, "VIDEO_EDIT_PASSWORD=fixture-password-123",
		"VIDEO_EDIT_PROJECT_ID="+owner.Project.ID, "VIDEO_EDIT_RED_ASSET_ID="+ids[0], "VIDEO_EDIT_BLUE_ASSET_ID="+ids[1])
	raw, err := cmd.CombinedOutput()
	if bytes.Contains(raw, []byte("fixture-password-123")) {
		t.Fatal("browser disclosed a fixture credential")
	}
	if err != nil {
		t.Fatalf("video browser: %v\n%s", err, raw)
	}
	t.Log(strings.TrimSpace(string(raw)))
	for _, name := range []string{"timeline-v1.mp4", "timeline-v2.mp4", "video-edit-expected.json"} {
		info, err := os.Stat(filepath.Join(output, name))
		if err != nil || info.Size() < 1 {
			t.Fatalf("missing browser download: %s", name)
		}
	}
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	f.media.mu.Lock()
	submits := f.media.count
	f.media.mu.Unlock()
	if submits != 2 || outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 2 ||
		outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("editing or exporting caused an extra generation or charge")
	}
}
