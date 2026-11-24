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
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

// Count exchanges where they really cross the browser-facing HTTP socket.
// Only the successful extension acknowledgement is dropped, after its writes
// have committed. All paths, including SSE and receipt reads, use the real app.
type sourceVideoBrowserTrace struct {
	mu                          sync.Mutex
	quotes, posts, receiptReads []string
	prices                      map[string]BillingQuote
	key, taskID                 map[string]string
}

func (trace *sourceVideoBrowserTrace) handler(t *testing.T, a *App, f *workflowIntegrationFixture, project, asset string, cases []*sourceVideoCase039) http.Handler {
	t.Helper()
	app := a.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Node samples these aggregate fixture counters before a human button
		// action. They prove the application has not submitted or recovered early.
		if r.Method == "GET" && r.URL.Path == "/__source-video-probe/counts" {
			trace.mu.Lock()
			defer trace.mu.Unlock()
			writeJSON(w, 200, map[string]int{"quotes": len(trace.quotes), "posts": len(trace.posts), "receipts": len(trace.receiptReads)})
			return
		}
		quote := r.Method == "POST" && r.URL.Path == "/api/v1/billing/quotes"
		post := r.Method == "POST" && r.URL.Path == "/api/v1/tasks"
		receipt := r.URL.Path == sourceVideoReceiptPath
		if !quote && !post && !receipt {
			app.ServeHTTP(w, r)
			return
		}
		trace.mu.Lock()
		defer trace.mu.Unlock()
		var in TaskInput
		key := r.Header.Get("Idempotency-Key")
		if !receipt {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if err != nil {
				t.Error("unexpected browser request size")
				w.WriteHeader(400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if quote {
				var q struct {
					ResourceType string    `json:"resource_type"`
					Input        TaskInput `json:"task_input"`
				}
				if json.Unmarshal(raw, &q) != nil || q.ResourceType != "task" || r.Header.Get("X-Project-ID") != project {
					t.Error("invalid scope or shape for browser quote")
					w.WriteHeader(400)
					return
				}
				in = q.Input
			} else if json.Unmarshal(raw, &in) != nil {
				t.Error("invalid browser task body")
				w.WriteHeader(400)
				return
			}
			var expected *sourceVideoCase039
			for _, tc := range cases {
				if tc.operation == in.Operation {
					expected = tc
					break
				}
			}
			if expected == nil || in.ProjectID != project || in.Inputs.SourceVideo == nil || *in.Inputs.SourceVideo != (SourceVideoRef{asset, 1}) ||
				in.Inputs.Prompt != in.Operation+" fixture" || in.Inputs.ExtensionDirection != expected.direction ||
				in.ModelID != "studio-video" || in.ModelVersion == "" || in.Parameters.AspectRatio != "16:9" || in.Parameters.Seconds != 5 || in.Parameters.Count != 1 {
				t.Error("browser changed the chosen source, version, direction or spec")
				w.WriteHeader(400)
				return
			}
			if quote {
				trace.quotes = append(trace.quotes, in.Operation)
				if in.QuoteID != "" {
					t.Error("browser quoted an already priced request")
				}
			} else {
				trace.posts = append(trace.posts, in.Operation)
				if key == "" || in.QuoteID == "" || trace.prices[in.Operation].ID != in.QuoteID || len(trace.posts) != len(trace.quotes) || trace.key[in.Operation] != "" {
					t.Error("task POST missed its original quote or was repeated")
					w.WriteHeader(409)
					return
				}
				trace.key[in.Operation] = key
				f.media.mu.Lock()
				f.media.video = expected.output
				f.media.mu.Unlock()
			}
		} else {
			trace.receiptReads = append(trace.receiptReads, r.Method)
			if r.Method != "GET" || r.URL.RawQuery != "" || key == "" || key != trace.key["video_extend"] || r.Header.Get("X-Project-ID") != project {
				t.Error("browser did not read the original key and project with GET")
				w.WriteHeader(400)
				return
			}
		}

		result := httptest.NewRecorder()
		app.ServeHTTP(result, r)
		if quote {
			var accepted BillingQuote
			if result.Code != 201 || json.Unmarshal(result.Body.Bytes(), &accepted) != nil || accepted.ID == "" {
				t.Errorf("browser quote returned %d", result.Code)
			}
			trace.prices[in.Operation] = accepted
		} else {
			var task Task
			if json.Unmarshal(result.Body.Bytes(), &task) != nil || task.ID == "" || task.ProjectID != project {
				t.Error("task/receipt response did not identify the current task")
			}
			if receipt {
				if result.Code != 200 || task.ID != trace.taskID["video_extend"] {
					t.Errorf("original receipt returned %d with an incorrect task", result.Code)
				}
			} else {
				if result.Code != 202 || task.Operation != in.Operation {
					t.Errorf("browser submission returned %d", result.Code)
				}
				trace.taskID[in.Operation] = task.ID
				if in.Operation == "video_extend" && result.Code == 202 {
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error("failed to withhold accepted response", err)
						return
					}
					// Once response headers have arrived, Chromium treats a short
					// body as a read failure rather than replaying a POST on a
					// pooled connection that appeared stale before the response.
					defer conn.Close()
					if _, err := rw.WriteString("HTTP/1.1 202 Accepted\r\nContent-Type: application/json\r\nContent-Length: 32\r\nConnection: close\r\n\r\n{"); err != nil {
						t.Error("failed to start the accepted response")
						return
					}
					if err := rw.Flush(); err != nil {
						t.Error("failed to flush the partial accepted response")
					}
					return
				}
			}
		}
		for k, v := range result.Header() {
			w.Header()[k] = slices.Clone(v)
		}
		w.WriteHeader(result.Code)
		_, _ = w.Write(result.Body.Bytes())
	})
}

func TestPostgresSourceVideoBrowser039(t *testing.T) {
	if os.Getenv("SOURCE_VIDEO_BROWSER_TEST") != "1" {
		t.Skip("set SOURCE_VIDEO_BROWSER_TEST=1 for Chromium and PostgreSQL")
	}
	for _, k := range []string{"TEST_DATABASE_URL", "TEST_VIDEO_FFMPEG"} {
		if os.Getenv(k) == "" {
			t.Fatalf("explicit browser acceptance requires %s", k)
		}
	}
	ffmpeg := os.Getenv("TEST_VIDEO_FFMPEG")
	source := sourceVideoFootage039(t, ffmpeg, "red", 440)
	cases := []*sourceVideoCase039{
		{operation: "video_edit", endpoint: "/v1/videos/edits_async", price: 200000, output: sourceVideoFootage039(t, ffmpeg, "blue", 880)},
		{operation: "video_extend", endpoint: "/v1/videos/extensions_async", direction: "forward", price: 350000, output: sourceVideoFootage039(t, ffmpeg, "green", 660)},
	}
	f := newWorkflowIntegrationFixture(t)
	wire := watchSourceVideoWire039(t, f, source, cases)
	f.media.mu.Lock()
	f.media.video = source
	f.media.mu.Unlock()
	a, s := f.open(t)
	client := authClient(t)
	const email = "source039-browser@example.test"
	identity := registerFixture(t, client, s.URL, email)
	original, asset := timelineTestSource(t, a, s, client, identity)
	s.Close()
	a.Close()

	catalog := defaultCatalogConfig(Config{Mode: "demo"})
	for i := range catalog.Models {
		if catalog.Models[i].Kind == "video" {
			catalog.Models[i].Capabilities.SourceVideo = &SourceVideoCapabilities{EditPresets: []string{"preset-1"}, ExtendPresets: []string{"preset-1"}, Directions: []string{"forward"}}
		}
	}
	f.cfg.ModelCatalogFile = writeCatalogFixture(t, catalog)
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	const prices = `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-video","kind":"video","operation":"video_edit","preset_id":"preset-1","artifact_micros":"200000"},{"model_id":"studio-video","kind":"video","operation":"video_extend","preset_id":"preset-1","artifact_micros":"350000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	a, s = f.open(t)
	httpCredit(t, a, identity.Workspace.ID, 1000000)
	project := identity.Project.ID
	trace := &sourceVideoBrowserTrace{prices: map[string]BillingQuote{}, key: map[string]string{}, taskID: map[string]string{}}
	edge := httptest.NewServer(trace.handler(t, a, f, project, asset, cases))
	defer edge.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/source-video-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	env := map[string]string{"SOURCE_PROBE_ORIGIN": edge.URL, "SOURCE_PROBE_EMAIL": email, "SOURCE_PROBE_PASSWORD": "fixture-password-123", "SOURCE_PROBE_ASSET": asset}
	for _, k := range []string{"HOME", "TMPDIR", "XDG_RUNTIME_DIR", "LOCALAPPDATA", "APPDATA", "WORKFLOW_CHROME"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	cmd.Env = childFixtureEnvironment(env)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		// The browser script emits only the failing stage. Do not print Chromium logs.
		for _, stage := range []string{"login", "open video studio", "edit quote and confirmation", "extension quote and confirmation", "lost response and original receipt"} {
			for _, checkpoint := range []string{"navigation", "operation", "source-list", "form-ready", "request-quote", "price", "traffic", "confirm", "delivery", "unknown", "receipt"} {
				if bytes.Contains(raw, []byte("Source-video browser failed during "+stage+" / "+checkpoint)) {
					trace.mu.Lock()
					defer trace.mu.Unlock()
					t.Fatalf("Chromium failed at %s / %s (quotes=%d posts=%d receipt reads=%d)", stage, checkpoint, len(trace.quotes), len(trace.posts), len(trace.receiptReads))
				}
			}
		}
		t.Fatal("Chromium probe failed", err)
	}
	var ui struct {
		Workbench, Confirmation, LostResponse, Recovery string
		Operations                                      []string
		DeliveredCards                                  int
	}
	if json.Unmarshal(raw, &ui) != nil || ui.Workbench != "视频工作室" || ui.Confirmation != "确认额度并提交" || ui.LostResponse != "受理状态待确认" ||
		ui.Recovery != "查询原任务收据" || ui.DeliveredCards != 2 || !reflect.DeepEqual(ui.Operations, []string{"编辑原视频", "延长原视频"}) {
		t.Fatal("browser did not confirm both quotes and recover the original task")
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	want := []string{"video_edit", "video_extend"}
	if !slices.Equal(trace.quotes, want) || !slices.Equal(trace.posts, want) || !slices.Equal(trace.receiptReads, []string{"GET"}) || trace.key[want[0]] == trace.key[want[1]] {
		t.Fatal("browser quote, submit or recovery traffic differs from the explicit user actions")
	}
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	local := wire()
	for _, tc := range cases {
		id := trace.taskID[tc.operation]
		if len(local[tc.operation]) != 1 || trace.prices[tc.operation].ReserveMicros != tc.price || id == "" ||
			outboxCount(t, db, `SELECT count(*) FROM studio_task_video_sources WHERE task_id=$1 AND source_task_id=$2 AND asset_revision=1 AND asset_id=$3 AND content_version=$4`, id, original.ID, asset, moderationSHA(source)) != 1 ||
			outboxCount(t, db, `SELECT count(*) FROM studio_ledger_reservations WHERE resource_id=$1 AND quote_id=$2 AND state='SETTLED' AND charged_micros=$3`, id, trace.prices[tc.operation].ID, tc.price) != 1 {
			t.Fatal("browser task missed the original source, endpoint or its single settlement", tc.operation)
		}
		status, raw, _ := authRequest(t, client, s.URL, "GET", "/api/v1/tasks/"+id, "", project, nil)
		task := decode[Task](t, raw)
		if status != 200 || task.Status != "SUCCEEDED" || task.Moderation == nil || task.Moderation.State != "ALLOWED" || len(task.Artifacts) != 1 {
			t.Fatal("missing approved browser output", tc.operation)
		}
		status, raw, _ = authRequest(t, client, s.URL, "GET", task.Artifacts[0].URL, "", project, nil)
		if status != 200 || !bytes.Equal(raw, tc.output) {
			t.Fatal("browser task delivered different footage", tc.operation)
		}
	}
	f.media.mu.Lock()
	posts := f.media.count
	f.media.mu.Unlock()
	if posts != 3 || outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 3 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 2 {
		t.Fatal("duplicate task, supplier submission or freeze")
	}
	report := struct {
		Workbench     string   `json:"workbench"`
		Operations    []string `json:"operations"`
		Confirmation  string   `json:"confirmation"`
		LostResponse  string   `json:"lost_response"`
		Recovery      string   `json:"recovery"`
		Quotes        int      `json:"quotes"`
		TaskPosts     int      `json:"task_posts"`
		ReceiptReads  int      `json:"receipt_gets"`
		SupplierPosts int      `json:"source_operation_supplier_posts"`
		Delivered     int      `json:"delivered"`
		ChargedMicros int64    `json:"settled_credit_micros"`
	}{ui.Workbench, ui.Operations, ui.Confirmation, ui.LostResponse, ui.Recovery, len(trace.quotes), len(trace.posts), len(trace.receiptReads), len(local[want[0]]) + len(local[want[1]]), ui.DeliveredCards, trace.prices[want[0]].ReserveMicros + trace.prices[want[1]].ReserveMicros}
	public, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("SOURCE_VIDEO_BROWSER_REPORT"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("choose an absolute report path")
		}
		if err = os.WriteFile(path, append(public, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(public))
}
