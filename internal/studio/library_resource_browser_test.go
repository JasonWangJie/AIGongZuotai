package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The data-only load fixture lives in the fresh schema made by testDatabase.
// Browser reads and project changes go through the running app without mocks.
func libraryLoadSeedHistory(t *testing.T, dsn string, owner testIdentity, source Task) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn := deliveryAdmin(t, dsn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var schema string
	var existing int
	err = tx.QueryRow(ctx, "SELECT current_schema(),count(*) FROM studio_library_entries WHERE project_id=$1 AND workspace_id=$2", owner.Project.ID, owner.Workspace.ID).Scan(&schema, &existing)
	if err != nil || !strings.HasPrefix(schema, "studio_test_") || existing != 25 {
		t.Fatalf("history fixture scope/count mismatch (%d)", existing)
	}
	// Historical one-artifact tasks use the pre-moderation storage shape.
	// Each task has its own primary key, library identity and revision. The
	// per-task private download routes share one immutable, real image file.
	// Nothing creates fake reviews, receipts, usage or public assets.
	statements := []string{
		`CREATE TEMP TABLE library_load_seed ON COMMIT DROP AS
		 SELECT 'task_'||md5($2::text||':'||n) id,
		  '2000-01-01T00:00:00Z'::timestamptz + n * interval '1 second' AS historical_at,
		  '馆藏图像 '||n title FROM generate_series(1,$1::int) n`,
		`INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll,updated_at)
		 SELECT b.id,t.workspace_id,t.project_id,(t.document-'moderation')||jsonb_build_object(
		    'id',b.id,'created_at',b.historical_at,'updated_at',b.historical_at,'artifacts',
		    jsonb_build_array((t.document->'artifacts'->0)||jsonb_build_object('title',b.title))),
		    t.input,'{}',b.historical_at,b.historical_at FROM library_load_seed b CROSS JOIN studio_tasks t WHERE t.id=$1`,
		`INSERT INTO studio_library_entries(id,workspace_id,project_id,source,task_id,artifact_id,provider_mode,kind,name,created_at,updated_at,content_type,byte_size,has_thumbnail,original_url,storage_info)
		  SELECT 'lib_'||encode(sha256(convert_to('generated:'||b.id||':'||e.artifact_id,'UTF8')),'hex'),
		    e.workspace_id,e.project_id,e.source,b.id,e.artifact_id,e.provider_mode,e.kind,b.title,b.historical_at,b.historical_at,
		    e.content_type,e.byte_size,e.has_thumbnail,e.original_url,e.storage_info
		  FROM library_load_seed b CROSS JOIN studio_library_entries e WHERE e.task_id=$1`,
		`INSERT INTO studio_library_revisions(asset_id,project_id,workspace_id,revision,name,tags,state,updated_at)
		  SELECT e.id,e.project_id,e.workspace_id,e.revision,e.name,e.tags,e.state,e.updated_at
		  FROM studio_library_entries e JOIN library_load_seed b ON b.id=e.task_id`,
	}
	for index, sql := range statements {
		var args []any
		switch index {
		case 0:
			args = []any{10000 - existing, owner.Project.ID}
		case 1, 2:
			args = []any{source.ID}
		}
		result, e := tx.Exec(ctx, sql, args...)
		if e != nil {
			t.Fatalf("history fixture step %d: %v", index, e)
		}
		if result.RowsAffected() != int64(10000-existing) {
			t.Fatalf("history fixture step %d inserted %d", index, result.RowsAffected())
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func libraryLoadLocalDB(raw string) bool {
	cfg, err := pgx.ParseConfig(raw)
	if raw == "" || err != nil {
		return false
	}
	local := func(host string) bool {
		ip := net.ParseIP(host)
		return host == "localhost" || ip != nil && ip.IsLoopback()
	}
	if !local(cfg.Host) {
		return false
	}
	for _, f := range cfg.Fallbacks {
		if !local(f.Host) {
			return false
		}
	}
	return true
}

func TestLibraryLoadLocalDatabaseOnly(t *testing.T) {
	for _, s := range []string{"postgres://u@127.0.0.1/db?sslmode=disable", "postgres://u@[::1]/db?sslmode=disable"} {
		if !libraryLoadLocalDB(s) {
			t.Fatal("rejected loopback")
		}
	}
	for _, s := range []string{"", "postgres://u@192.0.2.1/db?sslmode=disable", "postgres://u@localhost/db?host=192.0.2.1"} {
		if libraryLoadLocalDB(s) {
			t.Fatal("accepted non-loopback")
		}
	}
}

// Count real handler exits separately from JavaScript EventSource.close().
// This wrapper forwards the original ResponseWriter (including Flusher).
type libraryLoadStreams struct {
	mu                 sync.Mutex
	started            map[string]int
	open, closed, peak int
}

func (s *libraryLoadStreams) monitor(next http.Handler, workspace string, projects []string) http.Handler {
	s.started = map[string]int{}
	for _, p := range projects {
		s.started[p] = 0
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/workspaces/"+workspace+"/events" {
			p := r.URL.Query().Get("project_id")
			s.mu.Lock()
			_, tracked := s.started[p]
			if tracked {
				s.started[p]++
				s.open++
				s.peak = max(s.peak, s.open)
			}
			s.mu.Unlock()
			if tracked {
				defer func() { s.mu.Lock(); s.open--; s.closed++; s.mu.Unlock() }()
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *libraryLoadStreams) stats() (int, int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unique := 0
	for _, n := range s.started {
		if n > 0 {
			unique++
		}
	}
	return unique, s.open, s.closed, s.peak
}

func TestLibraryLoadTracksActualHandlerExit(t *testing.T) {
	var streams libraryLoadStreams
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := streams.monitor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }), "ws", []string{"first", "second"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/workspaces/ws/events?project_id=first", nil))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler failed to enter")
	}
	unique, open, closed, peak := streams.stats()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler failed to exit")
	}
	if unique != 1 || open != 1 || closed != 0 || peak != 1 {
		t.Fatalf("blocked handler %d %d %d %d", unique, open, closed, peak)
	}
	unique, open, closed, peak = streams.stats()
	if unique != 1 || open != 0 || closed != 1 || peak != 1 {
		t.Fatalf("completed handler %d %d %d %d", unique, open, closed, peak)
	}
}

func libraryLoadHTTP(t *testing.T, c *http.Client, base, project, method, path, key string, body any, want int) ([]byte, http.Header) {
	t.Helper()
	status, data, headers := authRequest(t, c, base, method, path, key, project, body)
	if status != want {
		t.Fatalf("library fixture %s returned %d, expected %d", method, status, want)
	}
	return data, headers
}

func TestPostgresLibraryTenThousandBrowserResources(t *testing.T) {
	if os.Getenv("LIBRARY_PERF_TEST") != "1" {
		t.Skip("opt-in PostgreSQL / Chromium private library fixture")
	}
	if !libraryLoadLocalDB(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("a loopback test database is required")
	}
	ffmpeg := os.Getenv("TEST_VIDEO_FFMPEG")
	if !filepath.IsAbs(ffmpeg) {
		t.Fatal("TEST_VIDEO_FFMPEG must point to the local binary")
	}
	reportPath := os.Getenv("LIBRARY_PERF_REPORT")
	if reportPath != "" && !filepath.IsAbs(reportPath) {
		t.Fatal("report destination must be absolute")
	}
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	client := authClient(t)
	const email = "library-resource-owner@example.test"
	owner := registerFixture(t, client, s.URL, email)
	gallery := owner.Project.ID
	projects := []string{}
	thumbs := []string{}
	upload := func(project string, shade byte) string {
		code, body, _ := uploadReferenceFixture(t, client, s.URL, project, newID("upload"), fmt.Sprintf("色卡-%03d.png", shade), referencePNG(t, shade), false)
		if code != 201 {
			t.Fatalf("private reference fixture upload returned %d", code)
		}
		return libraryID("reference", decode[ReferenceAsset](t, body).ID, "")
	}
	for i := range 30 {
		b, _ := libraryLoadHTTP(t, client, s.URL, gallery, "POST", "/api/v1/projects", "", map[string]string{"name": fmt.Sprintf("馆藏分区 %02d", i+1)}, 201)
		id := decode[Project](t, b).ID
		projects = append(projects, id)
		thumbs = append(thumbs, upload(id, byte(i+1)))
	}
	for i := range 22 {
		upload(gallery, byte(100+i))
	}
	original, _ := acceptMediaExecution(t, a, s, client, owner, "image", "Static private library fixture", newID("seed"), 1)
	original = laneTaskStatus(t, a, original.ID, "SUCCEEDED")
	if len(original.Artifacts) != 1 || original.Moderation == nil || original.Moderation.State != "ALLOWED" {
		t.Fatal("source image did not pass fixture review")
	}
	videoIDs := []string{}
	for i, color := range []string{"red", "blue"} {
		raw := timelineFixtureFootage(t, ffmpeg, color, 440*(i+1))
		f.media.mu.Lock()
		f.media.video = raw
		f.media.mu.Unlock()
		_, id := timelineTestSource(t, a, s, client, owner)
		videoIDs = append(videoIDs, id)
	}
	libraryLoadSeedHistory(t, f.cfg.DatabaseURL, owner, original)
	outsider := authClient(t)
	probe := "/api/v1/library/" + thumbs[0] + "/thumbnail?project_id=" + url.QueryEscape(projects[0])
	for _, path := range []string{probe, "/api/v1/library/" + videoIDs[0] + "/content?project_id=" + url.QueryEscape(gallery)} {
		libraryLoadHTTP(t, outsider, s.URL, "", "GET", path, "", nil, 401)
	}
	stranger := registerFixture(t, outsider, s.URL, "library-resource-stranger@example.test")
	for _, spec := range []struct{ asset, part string }{{thumbs[0], "thumbnail"}, {videoIDs[0], "content"}} {
		for _, scope := range []struct {
			c       *http.Client
			project string
		}{{outsider, stranger.Project.ID}, {client, projects[1]}} {
			libraryLoadHTTP(t, scope.c, s.URL, scope.project, "GET", "/api/v1/library/"+spec.asset+"/"+spec.part+"?project_id="+url.QueryEscape(scope.project), "", nil, 404)
		}
	}
	// Independent of the DOM and separate from timing the first browser page,
	// visit every registered identity once through the real scoped keyset API.
	seen := make(map[string]bool, 10000)
	cursor := ""
	var pages int
	var oldest LibraryEntry
	for pages < 100 {
		b, _ := libraryLoadHTTP(t, client, s.URL, gallery, "GET", "/api/v1/library?limit=100&cursor="+url.QueryEscape(cursor), "", nil, 200)
		var page struct {
			Items []LibraryEntry
			Next  string `json:"next_cursor"`
			More  bool   `json:"has_more"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Items) != 100 || page.More != (pages < 100) || (page.Next != "") != page.More {
			t.Fatalf("keyset shape at page %d", pages)
		}
		for _, asset := range page.Items {
			if !libraryIDPattern.MatchString(asset.ID) || asset.ProjectID != gallery || seen[asset.ID] || asset.ContentURL == nil {
				t.Fatalf("keyset scope/identity collision at page %d", pages)
			}
			seen[asset.ID] = true
			oldest = asset
		}
		cursor = page.Next
		if !page.More {
			break
		}
	}
	if len(seen) != 10000 {
		t.Fatal("keyset did not reach every index record")
	}
	if oldest.Source != "generated" || oldest.TaskID == original.ID || oldest.ArtifactID != original.Artifacts[0].ID || oldest.ThumbnailURL == nil {
		t.Fatal("historical page lost its original private image identity")
	}
	full, h := libraryLoadHTTP(t, client, s.URL, gallery, "GET", *oldest.ContentURL, "", nil, 200)
	if !strings.HasPrefix(h.Get("Content-Type"), "image/") || !bytes.Equal(full, f.media.image) {
		t.Fatal("historical entry did not read the original private bytes")
	}
	thumb, _ := libraryLoadHTTP(t, client, s.URL, gallery, "GET", *oldest.ThumbnailURL, "", nil, 200)
	if size, _, err := image.DecodeConfig(bytes.NewReader(thumb)); err != nil || size.Width <= 0 || size.Height <= 0 {
		t.Fatal("historical thumbnail is not decodable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var streams libraryLoadStreams
	watched := httptest.NewServer(streams.monitor(s.Config.Handler, owner.Workspace.ID, append([]string{gallery}, projects...)))
	defer watched.Close()
	settings, _ := json.Marshal(map[string]any{"url": watched.URL, "email": email, "password": "fixture-password-123", "gallery": gallery, "projects": projects, "thumbs": thumbs, "videos": videoIDs})
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/library-resource-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Stdin = bytes.NewReader(settings)
	// No database password, gateway or email configuration reaches Chromium.
	for _, k := range []string{"PATH", "SystemRoot", "USERPROFILE", "LOCALAPPDATA", "HOME", "TMPDIR", "TEMP", "TMP", "WORKFLOW_CHROME", "LD_LIBRARY_PATH", "XDG_RUNTIME_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// The JS entrypoint emits only its fixed phase in error cases.
		var failure struct {
			Phase          string
			Step, Observed int
		}
		_ = json.Unmarshal(stderr.Bytes(), &failure)
		switch failure.Phase {
		case "auth", "deep-pages", "first-screen", "handoff", "media", "project-cycle", "report":
		default:
			failure.Phase = "child-exit"
		}
		t.Fatalf("resource browser failed at %s, step %d, observed %d (%v)", failure.Phase, failure.Step, failure.Observed, err)
	}
	var measurements map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &measurements); err != nil {
		t.Fatal("browser returned unreadable measurements")
	}
	measurements["schema_version"] = 1
	measurements["indexed_assets"] = len(seen)
	measurements["verified_keyset_pages"] = pages
	measurements["private_scope_negative_checks"] = 6
	measurements["historical_private_original_and_thumbnail_verified"] = true
	deadline := time.Now().Add(5 * time.Second)
	for {
		unique, open, closed, peak := streams.stats()
		if unique == 31 && open == 0 && closed >= 31 {
			measurements["server_project_sse"] = map[string]int{"distinct_projects": unique, "completed_handlers": closed, "active_after_browser_exit": open, "max_concurrent_including_transitions": peak}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream handlers: %d scopes, %d open, %d closed", unique, open, closed)
		}
		time.Sleep(15 * time.Millisecond)
	}
	b, err := json.MarshalIndent(measurements, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if reportPath != "" {
		if err = os.WriteFile(reportPath, append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("verified %d scoped index records in %d authenticated pages; Chromium completed 30 distinct project switches", len(seen), pages)
}
