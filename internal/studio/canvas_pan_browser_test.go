package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type canvasBenchCase struct {
	Project         string `json:"project"`
	Prefix          string `json:"prefix"`
	Nodes           int    `json:"nodes"`
	ThumbnailDigest string `json:"thumbnail_digest"`
}

type canvasBenchInput struct {
	URL      string            `json:"url"`
	Email    string            `json:"email"`
	Password string            `json:"password"`
	Mode     string            `json:"mode"`
	GPU      string            `json:"gpu"`
	Cases    []canvasBenchCase `json:"cases"`
}

func canvasBenchDocument(count int, prefix, asset, run string) CanvasDocument {
	cols := 25
	if count == 2000 {
		cols = 50
	}
	id := func(i int) string { return fmt.Sprintf("%s-%04d", prefix, i) }
	doc := CanvasDocument{Title: "画布验证 " + prefix, Groups: []CanvasGroup{{ID: "layer", Title: "第一组"}}}
	for i := 0; i < count; i++ {
		node := CanvasNode{ID: id(i), Title: fmt.Sprintf("画布节点 %d", i), X: (i % cols) * 220, Y: (i / cols) * 135}
		// The stress viewport contains a dense cluster as well as the image
		// and running text node. Its level-of-detail fallback is exercised
		// during the timed drag, without hiding either real source.
		if count == 2000 && i >= 100 && i < 300 {
			node.X = (i - 100) % 20 * 42
			node.Y = 245 + (i-100)/20*14
		}
		switch {
		case i == 0:
			node.AssetID, node.AssetRevision = asset, 1
		case i == 1:
			node.RunID = run
		case i >= cols && i < cols+10:
			node.GroupID = "layer"
		}
		doc.Nodes = append(doc.Nodes, node)
		for j, offset := range []int{1, cols} {
			doc.Edges = append(doc.Edges, CanvasEdge{ID: fmt.Sprintf("e%d-%d", i, j), From: id(i), To: id((i + offset) % count)})
		}
	}
	return doc
}

func canvasBenchPNG(t *testing.T, shade byte) []byte {
	t.Helper()
	pic := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for y := 0; y < 360; y++ {
		for x := 0; x < 640; x++ {
			pic.SetRGBA(x, y, color.RGBA{R: byte(x), G: byte(y), B: shade, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, pic); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCanvasPanFixtureGraphAndDistinctPrivatePNGs(t *testing.T) {
	var zero uint64
	for _, count := range []int{500, 2000} {
		d := canvasBenchDocument(count, "case", libraryID("reference", "fixture-reference", ""), "run_0123456789abcdef0123456789abcdef")
		if len(d.Nodes) != count || len(d.Edges) != count*2 {
			t.Fatal("incorrect graph dimensions")
		}
		if err := validateCanvas(CanvasSaveInput{&zero, d}); err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Equal(canvasBenchPNG(t, 40), canvasBenchPNG(t, 190)) {
		t.Fatal("thumbnail source fixtures must differ")
	}
}

// Both authenticated Run requests remain genuinely active while the browser is
// measuring. This local HTTP fixture owns every upstream response and exits on
// either release or application cancellation.
func canvasBenchProvider(t *testing.T, f *workflowIntegrationFixture) (*atomic.Int32, func()) {
	t.Helper()
	var count atomic.Int32
	stop := make(chan struct{})
	release := sync.OnceFunc(func() { close(stop) })
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		count.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"local\"}}]}\n\n")
		w.(http.Flusher).Flush()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-stop:
				io.WriteString(w, "data: [DONE]\n\n")
				return
			case <-ticker.C:
				io.WriteString(w, ": keepalive\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	t.Cleanup(func() { release(); gateway.Close() })
	chat, err := newOpenAIChat(gateway.URL+"/v1", "owned-loopback-canvas", "fixture-chat")
	if err != nil {
		t.Fatal(err)
	}
	f.chat = chat
	return &count, release
}

func canvasBenchHTTP(t *testing.T, c *http.Client, base, project, method, path, key string, input any, want int) []byte {
	t.Helper()
	code, raw, _ := authRequest(t, c, base, method, path, key, project, input)
	if code != want {
		t.Fatalf("canvas fixture %s HTTP %d, expected %d", method, code, want)
	}
	return raw
}

func canvasCheckDenied(t *testing.T, c *http.Client, base, project, path string) {
	t.Helper()
	code, b, _ := authRequest(t, c, base, "GET", path, "", project, nil)
	if (code != 403 && code != 404) || bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("cross-project canvas or private image returned HTTP %d", code)
	}
}

func canvasBenchDistDigest(t *testing.T, dir string) string {
	t.Helper()
	hash := sha256.New()
	var files int
	var total int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files++
		total += info.Size()
		if !info.Mode().IsRegular() || files > 2048 || total > 128<<20 {
			return fmt.Errorf("unexpected workbench dist input")
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		content := sha256.New()
		n, readErr := io.Copy(content, io.LimitReader(f, info.Size()+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || n != info.Size() {
			return fmt.Errorf("unstable or unreadable workbench dist")
		}
		fmt.Fprintf(hash, "%s%c%x\n", filepath.ToSlash(name), 0, content.Sum(nil))
		return nil
	})
	if err != nil || files < 2 {
		t.Fatal("could not identify a complete production dist")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func TestPostgresCanvasPanBrowser037(t *testing.T) {
	mode := os.Getenv("CANVAS_BROWSER_TEST")
	if mode == "" {
		t.Skip("opt-in 037 PostgreSQL/browser canvas measurement")
	}
	if !libraryLoadLocalDB(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("dedicated loopback test database required")
	}
	gpu := os.Getenv("CANVAS_BROWSER_GPU")
	dist, reportPath := os.Getenv("CANVAS_BROWSER_DIST"), os.Getenv("CANVAS_BROWSER_REPORT")
	if (mode != "full" && mode != "pilot") || (gpu != "software" && gpu != "hardware") || !filepath.IsAbs(reportPath) || !filepath.IsAbs(dist) {
		t.Fatal("set full|pilot, hardware|software, absolute built dist and a fresh absolute report path")
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatal("report path must be unused")
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build the workbench first")
	}
	distDigest := canvasBenchDistDigest(t, dist)
	f := newWorkflowIntegrationFixture(t)
	f.cfg.WebDir = dist
	f.cfg.Heartbeat = 2 * time.Second
	f.cfg.PollInterval = 250 * time.Millisecond
	calls, release := canvasBenchProvider(t, f)
	_, s := f.open(t)
	defer release()
	owner, outsider := authClient(t), authClient(t)
	const email = "canvas-perf-owner@example.test"
	person := registerFixture(t, owner, s.URL, email)
	registerFixture(t, outsider, s.URL, "canvas-perf-other@example.test")
	sibling := decode[Project](t, canvasBenchHTTP(t, owner, s.URL, person.Project.ID, "POST", "/api/v1/projects", "", map[string]string{"name": "性能画布乙"}, 201))
	in := canvasBenchInput{URL: s.URL, Email: email, Password: "fixture-password-123", Mode: mode, GPU: gpu}
	var runs []string
	var originals []CanvasDocument
	for i, p := range []Project{person.Project, sibling} {
		count := []int{500, 2000}[i]
		prefix := []string{"normal", "stress"}[i]
		blob := canvasBenchPNG(t, byte(40+i*150))
		code, raw, _ := uploadReferenceFixture(t, owner, s.URL, p.ID, "canvas-upload-"+prefix, "canvas-source.png", blob, false)
		if code != 201 {
			t.Fatalf("approved canvas reference upload returned %d", code)
		}
		asset := decode[ReferenceAsset](t, raw)
		person.Project = p
		r := executionRun(t, s, owner, person, "studio-chat", "Local canvas status "+prefix, "canvas-run-"+prefix)
		runs = append(runs, r.ID)
		deadline := time.Now().Add(8 * time.Second)
		for calls.Load() != int32(i+1) {
			if time.Now().After(deadline) {
				t.Fatal("expected a real active generation at the local gateway")
			}
			time.Sleep(10 * time.Millisecond)
		}
		d := canvasBenchDocument(count, prefix, libraryID("reference", asset.ID, ""), r.ID)
		originals = append(originals, d)
		base := "/api/v1/projects/" + p.ID + "/canvas"
		var zero uint64
		saved := decode[CanvasVersion](t, canvasBenchHTTP(t, owner, s.URL, p.ID, "PUT", base, "canvas-save-"+prefix, CanvasSaveInput{&zero, d}, 200))
		img, active := saved.Sources[d.Nodes[0].ID], saved.Sources[d.Nodes[1].ID]
		if saved.Revision != 1 || !reflect.DeepEqual(saved.Document, d) || !img.Available || img.ContentVersion != fmt.Sprintf("%x", sha256.Sum256(blob)) || !active.Available || active.Status != "RUNNING" || active.Seq == nil {
			t.Fatal("saved canvas did not match the approved bytes and active Run")
		}
		raw = canvasBenchHTTP(t, owner, s.URL, p.ID, "GET", img.ThumbnailURL, "", nil, 200)
		conf, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil || conf.Width <= 0 || conf.Height <= 0 {
			t.Fatal("private approved PNG thumbnail is not decodable")
		}
		hash := sha256.Sum256(raw)
		in.Cases = append(in.Cases, canvasBenchCase{p.ID, prefix, count, hex.EncodeToString(hash[:])})
		canvasCheckDenied(t, outsider, s.URL, p.ID, base+"/revisions/1")
		canvasCheckDenied(t, outsider, s.URL, p.ID, img.ThumbnailURL)
	}
	if in.Cases[0].ThumbnailDigest == in.Cases[1].ThumbnailDigest {
		t.Fatal("fixtures need distinct private thumbnail bytes")
	}
	report := canvasBenchBrowser(t, in)
	if canvasBenchDistDigest(t, dist) != distDigest {
		t.Fatal("production artifact changed during the measurement")
	}
	report["dist_manifest_sha256"] = distDigest
	for i, c := range in.Cases {
		base := "/api/v1/projects/" + c.Project + "/canvas/revisions/"
		old := decode[CanvasVersion](t, canvasBenchHTTP(t, owner, s.URL, c.Project, "GET", base+"1", "", nil, 200))
		updated := decode[CanvasVersion](t, canvasBenchHTTP(t, owner, s.URL, c.Project, "GET", base+"2", "", nil, 200))
		want := old.Document
		want.Nodes = append([]CanvasNode(nil), old.Document.Nodes...)
		want.Nodes[0].X += 24
		want.Nodes[0].Y += 12
		want.Nodes[0].Title = "已移动的画布节点"
		if !reflect.DeepEqual(old.Document, originals[i]) || !reflect.DeepEqual(want, updated.Document) || old.Revision != 1 || updated.Revision != 2 {
			t.Fatal("revision rewind or edited canvas changed unrelated nodes, groups or edges")
		}
		active := updated.Sources[want.Nodes[1].ID]
		if !active.Available || active.Status != "RUNNING" {
			t.Fatal("run stopped before the complete browser measurement")
		}
	}
	if calls.Load() != 2 || f.chatCalls.Load() != 0 {
		t.Fatal("unexpected loopback generation count")
	}
	f.media.mu.Lock()
	mediaPosts := f.media.count
	f.media.mu.Unlock()
	if mediaPosts != 0 {
		t.Fatal("unexpected media generation during canvas use")
	}
	release()
	for _, id := range runs {
		if awaitAuthRun(t, owner, s.URL, id).Status != "COMPLETED" {
			t.Fatal("owned generation did not complete")
		}
	}
	report["verified_backend"] = map[string]any{"approved_private_pngs": 2, "persisted_and_reopened_revisions": 4, "outsider_denials": 4, "active_then_completed_runs": 2, "extra_generation_posts": 0}
	file, err := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	err = enc.Encode(report)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("could not write the complete canvas measurement")
	}
	if passed, ok := report["within_local_frame_targets"].(bool); !ok || !passed {
		t.Fatal("frame targets exceeded; report includes all raw samples")
	}
}

// A fresh Chromium profile belongs to one guarded driver. Do not hand it the
// fixture identity until the Windows job or POSIX process group is attached.
func canvasBenchBrowser(t *testing.T, in canvasBenchInput) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/canvas-pan-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	for _, k := range []string{"PATH", "SystemRoot", "USERPROFILE", "LOCALAPPDATA", "HOME", "TMPDIR", "TEMP", "TMP", "WORKFLOW_CHROME", "LD_LIBRARY_PATH", "XDG_RUNTIME_DIR", "DISPLAY", "WAYLAND_DISPLAY"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if dir := os.Getenv("CANVAS_BROWSER_CAPTURE_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			t.Fatal("visual capture requires an absolute path")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("visual capture requires a new directory")
		}
		cmd.Env = append(cmd.Env, "CANVAS_BROWSER_CAPTURE_DIR="+dir)
	}
	pipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	attach, stop, err := longReplyProcessGuard(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err = attach(); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err)
	}
	writeErr := json.NewEncoder(pipe).Encode(in)
	closeErr := pipe.Close()
	if err = cmd.Wait(); err != nil || writeErr != nil || closeErr != nil {
		var fault struct {
			Phase string
			Case  int
			Line  int
		}
		json.Unmarshal(stderr.Bytes(), &fault)
		// Only a fixed stage label is ever relayed to test output.
		if !strings.Contains(" setup login open sources pan edit fold overview switch report ", " "+fault.Phase+" ") {
			fault.Phase = "driver"
		}
		t.Fatalf("canvas browser failed phase=%s case=%d script_line=%d", fault.Phase, fault.Case, fault.Line)
	}
	var report map[string]any
	if stdout.Len() > 4<<20 || json.Unmarshal(stdout.Bytes(), &report) != nil || report["mode"] != in.Mode || report["requested_gpu"] != in.GPU {
		t.Fatal("incomplete browser evidence")
	}
	return report
}
