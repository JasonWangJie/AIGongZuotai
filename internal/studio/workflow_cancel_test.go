package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresWorkflowCancelDuringInputReview(t *testing.T) {
	a, s, c, id, database := newModeratedBillingFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	reviewer, _ := httpModerationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var input moderationHTTPRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixtureModerationReply(input))
	})
	a.moderation = reviewer
	provider := &moderatedExecutionChat{text: "unused"}
	a.chat = provider
	w := startWorkflowStepTest(t, a, s, c, id)
	q := httpBillingQuote(t, c, s, w.project, "run", w.input("script").Run)
	done := make(chan int, 1)
	go func() {
		code, _, _ := authRequest(t, c, s.URL, "POST", w.path+"/nodes/script/execute", "slow-input-review", w.project, map[string]any{"expected_version": w.run.Version, "quote_id": q.ID})
		done <- code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("reviewer did not receive the pending input")
	}
	w.request(200, "POST", w.path+"/cancel", "cancel-before-admission", map[string]any{"expected_version": w.run.Version})
	unblock.Do(func() { close(release) })
	select {
	case code := <-done:
		if code != 409 {
			t.Fatal("late allowed input bypassed cancel", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original execute did not return")
	}
	w.request(404, "GET", w.path+"/nodes/script/receipt", "slow-input-review", nil)
	db := deliveryAdmin(t, database)
	if provider.calls.Load() != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_runs") != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", q.ID) != 1 {
		t.Fatal("cancelled input created a generation or a credit hold")
	}
}

func TestPostgresWorkflowCancelTextRollbackAndReceipt(t *testing.T) {
	a, s, c, id, database := newModeratedBillingFixture(t)
	p := &moderatedExecutionChat{text: "private candidate held during cancellation", emitted: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(p.release)
	a.chat = p
	w := startWorkflowStepTest(t, a, s, c, id)
	q := httpBillingQuote(t, c, s, w.project, "run", w.input("script").Run)
	w.execute(w.run.Version, "script", "start-text-then-cancel", q.ID, 200)
	awaitModeratedEmission(t, p)
	d := w.read()
	if d.Status != "TEXT_ACTIVE" || d.TextRun.Status != "RUNNING" {
		t.Fatal("missing active text")
	}
	db := deliveryAdmin(t, database)
	ctx := context.Background()
	_, err := db.Exec(ctx, `CREATE FUNCTION refuse_workflow_cancel_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation='cancel' THEN UPDATE studio_outbox_limits SET pending=10000 WHERE singleton; END IF; RETURN NEW; END $$;
	CREATE TRIGGER refuse_workflow_cancel_fixture BEFORE INSERT ON studio_workflow_receipts FOR EACH ROW EXECUTE FUNCTION refuse_workflow_cancel_fixture()`)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"expected_version": d.Version}
	const key = "original-workflow-cancel"
	_, rejection, _ := w.request(503, "POST", w.path+"/cancel", key, body)
	if !strings.Contains(string(rejection), "EVENT_BACKLOG_FULL") {
		t.Fatal("wrong failure before commit", string(rejection))
	}
	w.request(404, "GET", w.path+"/cancel/receipt", key, nil)
	if now := w.read(); now.Status != "TEXT_ACTIVE" || now.TextRun.Status != "RUNNING" || now.Version != d.Version || now.TextRun.Billing.State != "HELD" {
		t.Fatal("partial cancellation escaped rollback", now)
	}
	if _, err = db.Exec(ctx, "DROP TRIGGER refuse_workflow_cancel_fixture ON studio_workflow_receipts"); err != nil {
		t.Fatal(err)
	}
	_, data, _ := w.request(200, "POST", w.path+"/cancel", key, body)
	cancel := decode[workflowDetail](t, data)
	if cancel.Status != "CANCELLED" || cancel.TextRun.Status != "CANCELLED" || cancel.TextRun.Output != "" || cancel.TextRun.Billing.State != "REVIEW" || cancel.TextRun.Billing.ChargedMicros != 0 {
		t.Fatal("cancelled text leaked or settled", cancel)
	}
	w.request(200, "GET", w.path+"/cancel/receipt", key, nil)
	_, _, h := w.request(200, "POST", w.path+"/cancel", key, body)
	if h.Get("Idempotency-Replayed") != "true" {
		t.Fatal("original cancellation not replayed")
	}
	w.request(409, "POST", w.path+"/cancel", key, map[string]any{"expected_version": cancel.Version})
	w.request(409, "POST", w.path+"/cancel", "different-cancel-operation", map[string]any{"expected_version": cancel.Version})
	w.execute(cancel.Version, "picture", "attempt-after-cancel", "", 409)
	if outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 0 || outboxCount(t, db, "SELECT count(*) FROM studio_workflow_receipts WHERE operation='cancel'") != 1 || outboxCount(t, db, "SELECT count(*) FROM studio_run_streams WHERE run_id=$1 AND lease_owner='' AND closed", d.TextRunID) != 1 {
		t.Fatal("cancellation did not fence child or was duplicated")
	}
}

// The gateway holds either the first generation POST or its original GET. It
// intentionally completes the call after cancellation to exercise late writes.
func TestPostgresWorkflowCancelQueuedUnknownAndKnownMedia(t *testing.T) {
	for _, holdQuery := range []bool{false, true} {
		name := "submit"
		if holdQuery {
			name = "query"
		}
		t.Run(name, func(t *testing.T) {
			f := newWorkflowIntegrationFixture(t)
			f.cfg.MediaScheduling.SubmitWorkers = 1
			f.cfg.BillingMode = "credits"
			f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
			prices := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"4000000"},{"model_id":"studio-image","kind":"image","operation":"generate","preset_id":"preset-3","artifact_micros":"50000"}]}`
			if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var posts atomic.Int32
			original := f.gateway.Config.Handler
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				isPost := r.Method == "POST" && r.URL.Path == imageGenerationEndpoint
				if isPost {
					posts.Add(1)
				}
				if isPost && !holdQuery || r.Method == "GET" && strings.HasPrefix(r.URL.Path, mediaQueryPrefix) && holdQuery {
					once.Do(func() { close(started) })
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				original.ServeHTTP(w, r)
			}))
			t.Cleanup(gateway.Close)
			f.gateway = gateway
			a, s := f.open(t)
			a.chat = &moderatedExecutionChat{text: workflowTestText}
			c := authClient(t)
			id := registerFixture(t, c, s.URL, "media-cancel@example.test")
			httpCredit(t, a, id.Workspace.ID, 1_000_000)
			w := startWorkflowStepTest(t, a, s, c, id)
			startImage := func(w workflowStepTest) workflowDetail {
				t.Helper()
				textQuote := httpBillingQuote(t, c, s, w.project, "run", w.input("script").Run)
				data, _ := w.execute(w.run.Version, "script", newID("text"), textQuote.ID, 200)
				awaitAuthRun(t, c, s.URL, decode[workflowDetail](t, data).TextRunID)
				d := w.read()
				imageQuote := httpBillingQuote(t, c, s, w.project, "task", w.input("picture").Task)
				w.execute(d.Version, "picture", newID("picture"), imageQuote.ID, 200)
				return w.read()
			}
			first := startImage(w)
			select {
			case <-started:
			case <-time.After(8 * time.Second):
				t.Fatal("fixture did not reach target request")
			}
			if !holdQuery {
				second := w
				collection := "/api/v1/projects/" + w.project + "/workflow-runs"
				_, b, _ := w.request(201, "POST", collection, newID("start"), workflowStartInput{DefinitionID: first.DefinitionID, DefinitionVersion: first.DefinitionVersion, Input: "a second scene"})
				second.run = decode[WorkflowExecution](t, b)
				second.path = collection + "/" + second.run.ID
				queued := startImage(second)
				if queued.ImageTask.Status != "QUEUED" {
					t.Fatal("second task was not queued", queued.ImageTask)
				}
				second.request(200, "POST", second.path+"/cancel", newID("cancel"), map[string]any{"expected_version": queued.Version})
				if q := second.read(); q.Status != "CANCELLED" || q.ImageTask.Status != "CANCELLED" || q.ImageTask.Billing == nil || q.ImageTask.Billing.State != "RELEASED" || q.ImageTask.Billing.ReleasedMicros != 100_000 {
					t.Fatal("queued media not stopped", q)
				}
			}
			first = w.read()
			_, b, _ := w.request(200, "POST", w.path+"/cancel", "stop-original-media", map[string]any{"expected_version": first.Version})
			stopped := decode[workflowDetail](t, b)
			if stopped.Status != "CANCELLED" {
				t.Fatal("parent did not stop")
			}
			if !holdQuery && (stopped.ImageTask.Status != "SUBMIT_UNKNOWN" || stopped.ImageTask.CanReconcile || stopped.ImageTask.Billing == nil || stopped.ImageTask.Billing.State != "REVIEW" || stopped.ImageTask.Billing.ReleasedMicros != 0 || stopped.ImageTask.Billing.ChargedMicros != 0) {
				t.Fatal("unconfirmed submission falsely reported stopped or retryable", stopped.ImageTask)
			}
			if holdQuery && terminal(stopped.ImageTask.Status) {
				t.Fatal("provider with known handle was declared stopped")
			}
			unblock()
			if holdQuery {
				delivered := laneTaskStatus(t, a, first.ImageTaskID, "SUCCEEDED")
				if delivered.Billing == nil || delivered.Billing.State != "SETTLED" || delivered.Billing.ChargedMicros != 100_000 {
					t.Fatal("original delivered task settled incorrectly", delivered.Billing)
				}
			}
			w.request(200, "GET", w.path+"/cancel/receipt", "stop-original-media", nil)
			last := w.read()
			if last.Status != "CANCELLED" || len(last.Candidates) != 0 || posts.Load() != 1 {
				t.Fatal("cancelled workflow advanced or posted again", last.Status, posts.Load())
			}
		})
	}
}
