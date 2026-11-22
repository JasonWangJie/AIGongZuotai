package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"aigongzuotai/internal/videoexport"
)

func TestPostgresVideoTimelinePrivateRangeAndRevokedExport(t *testing.T) {
	ffmpeg, ffprobe := os.Getenv("TEST_VIDEO_FFMPEG"), os.Getenv("TEST_VIDEO_FFPROBE")
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("set TEST_VIDEO_FFMPEG and TEST_VIDEO_FFPROBE to real local media tools")
	}
	f := newWorkflowIntegrationFixture(t)
	f.cfg.VideoExport = videoexport.Config{FFmpegPath: ffmpeg, FFprobePath: ffprobe, TempDir: t.TempDir()}
	a, s := f.open(t)
	client := authClient(t)
	owner := registerFixture(t, client, s.URL, "timeline-private@example.test")
	project := owner.Project.ID
	var assets []string
	var originals [][]byte
	for i, color := range []string{"blue", "red"} {
		original := timelineFixtureFootage(t, ffmpeg, color, 440*(1+i))
		f.media.mu.Lock()
		f.media.video = original
		f.media.mu.Unlock()
		_, asset := timelineTestSource(t, a, s, client, owner)
		assets = append(assets, asset)
		originals = append(originals, original)
	}
	f.media.mu.Lock()
	submitted := f.media.count
	f.media.mu.Unlock()
	api := "/api/v1/projects/" + project + "/video-timelines"
	call := func(method, p, key string, body any, want int) ([]byte, http.Header) {
		return presentationRequest(t, client, s.URL, project, method, p, key, body, want)
	}
	in := timelineTestInput(assets...)
	saved, _ := call("POST", api, "timeline-original-v1", in, 201)
	v := decode[VideoTimelineVersion](t, saved)
	old := api + "/" + v.ID + "/versions/1"
	preview := old + "/clips/shot1/source"
	export := old + "/export"
	if v.Version != 1 {
		t.Fatal("initial version")
	}
	prior := v.Version
	in.ExpectedVersion = &prior
	in.TimelineID = v.ID
	in.Document.Title = "新剪辑"
	in.Document.Clips = in.Document.Clips[1:]
	call("POST", api, "timeline-edit-v2", in, 201)
	raw, h := call("GET", preview, "", nil, 200)
	if !bytes.Equal(raw, originals[0]) || h.Get("Content-Type") != "video/mp4" || h.Get("Cache-Control") != "private, no-store" {
		t.Fatal("original full preview differs or permits caching")
	}
	rangeRequest := func(c *http.Client, projectID, p string, want int) ([]byte, http.Header) {
		req, err := http.NewRequest("GET", s.URL+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Range", "bytes=11-30")
		if projectID != "" {
			req.Header.Set("X-Project-ID", projectID)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("private range status %d want %d: %v", resp.StatusCode, want, err)
		}
		return body, resp.Header
	}
	part, rh := rangeRequest(client, project, preview, 206)
	if !bytes.Equal(part, originals[0][11:31]) || rh.Get("Content-Range") != fmt.Sprintf("bytes 11-30/%d", len(originals[0])) {
		t.Fatal("range did not read the original frozen bytes")
	}
	mp4, headers := call("POST", export, "", nil, 200)
	sum := sha256.Sum256(mp4)
	if len(mp4) < 12 || string(mp4[4:8]) != "ftyp" || headers.Get("X-Content-SHA256") != hex.EncodeToString(sum[:]) ||
		headers.Get("X-Video-Duration-Ms") != "500" || headers.Get("Content-Disposition") != "attachment; filename=\"timeline-v1.mp4\"" {
		t.Fatal("old-version export digest, duration or identity differs")
	}
	blocked := func(b []byte, h http.Header) {
		t.Helper()
		if h.Get("Content-Type") == "video/mp4" || h.Get("Content-Disposition") != "" || h.Get("X-Content-SHA256") != "" || bytes.Contains(b, originals[0][11:31]) {
			t.Fatal("original media or receipt headers escaped")
		}
	}
	outsider := authClient(t)
	b, bh := rangeRequest(outsider, "", preview, 401)
	blocked(b, bh)
	b, bh = presentationRequest(t, outsider, s.URL, "", "POST", export, "", nil, 401)
	blocked(b, bh)
	other := registerFixture(t, outsider, s.URL, "timeline-stranger@example.test")
	for _, projectHeader := range []string{project, other.Project.ID} {
		b, bh = rangeRequest(outsider, projectHeader, preview, 404)
		blocked(b, bh)
		b, bh = presentationRequest(t, outsider, s.URL, projectHeader, "POST", export, "", nil, 404)
		blocked(b, bh)
	}
	call("POST", "/api/v1/library/"+assets[0]+"/trash", "timeline-trash-v1-source", stateInput(1), 200)
	b, bh = rangeRequest(client, project, preview, 409)
	blocked(b, bh)
	b, bh = call("POST", export, "", nil, 409)
	blocked(b, bh)
	sibling, _ := rangeRequest(client, project, old+"/clips/shot2/source", 206)
	if !bytes.Equal(sibling, originals[1][11:31]) {
		t.Fatal("unaffected sibling preview differs")
	}
	raw, _ = call("GET", old, "", nil, 200)
	history := decode[VideoTimelineVersion](t, raw)
	if history.Version != 1 || history.Document.Clips[0].AssetAvailable == nil || *history.Document.Clips[0].AssetAvailable {
		t.Fatal("old version lost its unavailable original")
	}
	call("POST", api+"/"+v.ID+"/versions/2/export", "", nil, 200)
	// Reading Content-Length bytes can complete before the server returns
	// from ServeContent and runs its deferred file and directory cleanup.
	s.Close()
	if err := videoTempClean(f.cfg.VideoExport.TempDir); err != nil {
		t.Fatal("export retained an operation spool", err)
	}
	f.media.mu.Lock()
	again := f.media.count
	f.media.mu.Unlock()
	if again != submitted || submitted != 2 {
		t.Fatal("draft export resubmitted generation")
	}
}
