package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type sourceTransportTrace struct {
	mu                                         sync.Mutex
	quotes, posts, receipts, dropped, replayed int
	key, task, reservation, quote              string
	body                                       []byte
}

func (x *sourceTransportTrace) handler(t *testing.T, a *App, scope, asset string) http.Handler {
	t.Helper()
	app := a.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		post := r.Method == "POST" && r.URL.Path == "/api/v1/tasks"
		quote := r.Method == "POST" && r.URL.Path == "/api/v1/billing/quotes"
		receipt := r.Method == "GET" && r.URL.Path == sourceVideoReceiptPath
		probe := r.Method == "GET" && r.URL.Path == "/__source-transport/counts"
		if !post && !quote && !receipt && !probe {
			app.ServeHTTP(w, r)
			return
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		if probe {
			writeJSON(w, 200, map[string]int{"quotes": x.quotes, "posts": x.posts, "receiptGets": x.receipts, "replayed": x.replayed})
			return
		}
		key := r.Header.Get("Idempotency-Key")
		var raw []byte
		if post {
			var err error
			raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if err != nil {
				t.Error("could not read bounded task input")
				w.WriteHeader(400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		// Every POST, including a TCP-level resend, executes the regular
		// authenticated handler. Only the first confirmed response is withheld.
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, r)
		switch {
		case quote:
			x.quotes++
			var q BillingQuote
			if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &q) != nil || q.ID == "" || q.ProjectID != scope || q.ReserveMicros != 350000 {
				t.Error("unexpected transport fixture quote")
			}
			x.quote = q.ID
		case post:
			x.posts++
			var in TaskInput
			var task Task
			if rec.Code != 202 || json.Unmarshal(raw, &in) != nil || in.QuoteID != x.quote || in.ProjectID != scope || in.Operation != "video_extend" ||
				in.Inputs.SourceVideo == nil || *in.Inputs.SourceVideo != (SourceVideoRef{asset, 1}) || in.Inputs.ExtensionDirection != "forward" ||
				json.Unmarshal(rec.Body.Bytes(), &task) != nil || task.ID == "" || task.ProjectID != scope || task.Billing == nil || task.Billing.ReservationID == "" || task.Billing.QuoteID != x.quote || key == "" {
				t.Errorf("transport fixture task response failed validation (HTTP %d)", rec.Code)
				w.WriteHeader(500)
				return
			}
			if x.posts == 1 {
				if rec.Header().Get("Idempotency-Replayed") != "" {
					t.Error("first submission unexpectedly replayed")
				}
				x.body, x.key, x.task, x.reservation = raw, key, task.ID, task.Billing.ReservationID
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error("could not disconnect after acceptance")
					return
				}
				x.dropped++
				_ = conn.Close() // no HTTP status, header, or body on the wire
				return
			}
			if x.posts != 2 || key != x.key || !bytes.Equal(raw, x.body) || task.ID != x.task || task.Billing.ReservationID != x.reservation || rec.Header().Get("Idempotency-Replayed") != "true" {
				t.Error("resubmitted HTTP bytes did not replay the original task and reservation")
			} else {
				x.replayed++
			}
		case receipt:
			x.receipts++
			var task Task
			if key != x.key || r.Header.Get("X-Project-ID") != scope || r.URL.RawQuery != "" || rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &task) != nil ||
				task.ID != x.task || task.Billing == nil || task.Billing.ReservationID != x.reservation {
				t.Error("manual receipt lookup did not recover the accepted task")
			}
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func TestPostgresSourceVideoTransportBrowser039(t *testing.T) {
	if os.Getenv("SOURCE_VIDEO_TRANSPORT_BROWSER_TEST") != "1" {
		t.Skip("set SOURCE_VIDEO_TRANSPORT_BROWSER_TEST=1 for the headerless response test")
	}
	ff := os.Getenv("TEST_VIDEO_FFMPEG")
	if !mixedLoopbackDatabase(os.Getenv("TEST_DATABASE_URL")) || !filepath.IsAbs(ff) {
		t.Fatal("requires isolated loopback PostgreSQL and an absolute TEST_VIDEO_FFMPEG")
	}
	source := sourceVideoFootage039(t, ff, "red", 440)
	tc := &sourceVideoCase039{operation: "video_extend", direction: "forward", endpoint: "/v1/videos/extensions_async", price: 350000, output: sourceVideoFootage039(t, ff, "green", 660)}
	f := newWorkflowIntegrationFixture(t)
	wire := watchSourceVideoWire039(t, f, source, []*sourceVideoCase039{tc})
	f.media.mu.Lock()
	f.media.video = source
	f.media.mu.Unlock()
	a, s := f.open(t)
	client := authClient(t)
	const email = "source039-transport@example.test"
	owner := registerFixture(t, client, s.URL, email)
	original, asset := timelineTestSource(t, a, s, client, owner)
	s.Close()
	a.Close()

	catalog := defaultCatalogConfig(Config{Mode: "demo"})
	for i := range catalog.Models {
		if catalog.Models[i].Kind == "video" {
			catalog.Models[i].Capabilities.SourceVideo = &SourceVideoCapabilities{ExtendPresets: []string{"preset-1"}, Directions: []string{"forward"}}
		}
	}
	f.cfg.ModelCatalogFile = writeCatalogFixture(t, catalog)
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	const pricing = `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-video","kind":"video","operation":"video_extend","preset_id":"preset-1","artifact_micros":"350000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(pricing), 0600); err != nil {
		t.Fatal(err)
	}
	f.media.mu.Lock()
	f.media.video = tc.output
	f.media.mu.Unlock()
	a, s = f.open(t)
	httpCredit(t, a, owner.Workspace.ID, 1000000)
	x := &sourceTransportTrace{}
	edge := httptest.NewServer(x.handler(t, a, owner.Project.ID, asset))
	defer edge.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/source-video-transport-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	env := map[string]string{"SOURCE_PROBE_ORIGIN": edge.URL, "SOURCE_PROBE_EMAIL": email, "SOURCE_PROBE_PASSWORD": "fixture-password-123", "SOURCE_PROBE_ASSET": asset}
	for _, k := range []string{"HOME", "TMPDIR", "XDG_RUNTIME_DIR", "LOCALAPPDATA", "APPDATA", "WORKFLOW_CHROME"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	cmd.Env = childFixtureEnvironment(env)
	raw, err := cmd.CombinedOutput()
	x.mu.Lock()
	defer x.mu.Unlock()
	if err != nil {
		for _, stage := range []string{"login", "source", "quote", "submit", "outcome", "receipt", "delivery", "counts"} {
			if bytes.Contains(raw, []byte("transport step "+stage+" failed")) {
				t.Fatalf("Chromium failed at %s (quotes=%d post=%d replay=%d GET=%d)", stage, x.quotes, x.posts, x.replayed, x.receipts)
			}
		}
		t.Fatalf("Chromium transport run failed (quotes=%d post=%d replay=%d GET=%d)", x.quotes, x.posts, x.replayed, x.receipts)
	}
	var ui struct {
		PageTaskPosts, PageReceiptGets int
		Outcome                        string
	}
	if json.Unmarshal(raw, &ui) != nil || ui.PageTaskPosts != 1 || ui.PageReceiptGets != x.receipts || x.dropped != 1 || x.quotes != 1 {
		t.Fatal("actual page invocations disagree with the single explicit submit")
	}
	replay := x.posts == 2 && x.replayed == 1 && x.receipts == 0 && ui.Outcome == "transport_replay"
	lookup := x.posts == 1 && x.replayed == 0 && x.receipts == 1 && ui.Outcome == "manual_receipt"
	if !replay && !lookup {
		t.Fatal("did not witness a valid transparent replay or manual lookup")
	}
	status, raw, _ := authRequest(t, client, s.URL, "GET", sourceVideoReceiptPath, x.key, owner.Project.ID, nil)
	task := decode[Task](t, raw)
	if status != 200 || task.ID != x.task || task.Status != "SUCCEEDED" || task.Moderation == nil || task.Moderation.State != "ALLOWED" || task.Billing == nil || task.Billing.ReservationID != x.reservation || len(task.Artifacts) != 1 {
		t.Fatal("independent original-key lookup failed to recover the delivered task")
	}
	status, raw, _ = authRequest(t, client, s.URL, "GET", task.Artifacts[0].URL, "", owner.Project.ID, nil)
	if status != 200 || !bytes.Equal(raw, tc.output) {
		t.Fatal("delivered original-key result has unexpected video bytes")
	}
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	f.media.mu.Lock()
	totalSupplierPosts := f.media.count
	f.media.mu.Unlock()
	if totalSupplierPosts != 2 || len(wire()[tc.operation]) != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 2 ||
		outboxCount(t, db, "SELECT count(*) FROM studio_task_video_sources WHERE task_id=$1 AND source_task_id=$2 AND asset_id=$3 AND asset_revision=1 AND content_version=$4", x.task, original.ID, asset, moderationSHA(source)) != 1 ||
		outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 1 ||
		outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations WHERE id=$1 AND resource_id=$2 AND quote_id=$3 AND state='SETTLED' AND charged_micros=$4", x.reservation, x.task, x.quote, tc.price) != 1 {
		t.Fatal("original source, one supplier POST, or unique settled charge did not match")
	}
	report := map[string]any{"outcome": ui.Outcome, "page_task_fetch_calls": ui.PageTaskPosts, "task_http_posts": x.posts, "idempotency_replayed_responses": x.replayed, "browser_receipt_gets": x.receipts, "independent_original_receipt_gets": 1, "accepted_responses_dropped_before_headers": x.dropped, "supplier_extension_posts": 1, "extension_tasks": 1, "settled_reservations": 1, "charged_credit_micros": tc.price}
	public, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("SOURCE_VIDEO_TRANSPORT_REPORT"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("choose a fresh absolute transport report path")
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal("cannot create new transport report")
		}
		_, writeErr := out.Write(append(public, '\n'))
		closeErr := out.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot finish transport report")
		}
	}
	t.Log(string(public))
}
