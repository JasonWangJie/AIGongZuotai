package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scheduleLaneHTTPGate struct {
	entered chan string
	release chan struct{}
	once    sync.Once
	aCalls  atomic.Int32
	bCalls  atomic.Int32
}

func (g *scheduleLaneHTTPGate) unblock() { g.once.Do(func() { close(g.release) }) }
func (g *scheduleLaneHTTPGate) enter(ctx context.Context, kind string) bool {
	if kind == "image" {
		g.aCalls.Add(1)
		g.entered <- "A"
		select {
		case <-g.release:
			return true
		case <-ctx.Done():
			return false
		}
	}
	g.bCalls.Add(1)
	g.entered <- "B"
	return true
}

// This exercises each native stage through the same unqualified production
// selector. Targeted calls are used only to finish predecessor stages, never
// to choose either workspace for the fairness assertion.
func TestPostgresMediaSchedulerQueryDeliveryReviewWorkspaceFairness(t *testing.T) {
	for _, lane := range []mediaExecutionLane{mediaQueryLane, mediaDeliveryLane, mediaReviewLane} {
		t.Run(mediaLaneName(lane), func(t *testing.T) {
			gate := &scheduleLaneHTTPGate{entered: make(chan string, 16), release: make(chan struct{})}
			defer gate.unblock()
			media := newDeliveryHTTPFixture(t, -1)
			originalHandler := media.server.Config.Handler
			media.server.Close()
			media.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var handle string
				if lane == mediaQueryLane && strings.HasPrefix(r.URL.Path, mediaQueryPrefix) {
					handle = strings.TrimPrefix(r.URL.Path, mediaQueryPrefix)
				} else if lane == mediaDeliveryLane && strings.HasPrefix(r.URL.Path, "/private-download/") {
					handle = strings.Split(strings.TrimPrefix(r.URL.Path, "/private-download/"), "/")[0]
				}
				if handle != "" {
					media.mu.Lock()
					kind := media.tasks[handle].kind
					media.mu.Unlock()
					if !gate.enter(r.Context(), kind) {
						return
					}
				}
				originalHandler.ServeHTTP(w, r)
			}))
			cfg := deliveryTestConfig(t, media, true)
			if lane == mediaReviewLane {
				review := newReviewDispatchHTTPFixture(t, "no-block")
				// Use the real media-v1 body and response identities. Different
				// kinds distinguish the two workspaces without leaking scope to
				// the reviewer or putting a tenant identifier in result bytes.
				review.server.Close()
				review.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					meta, body := readMediaMultipartFixture(t, r)
					if r.Header.Get("Authorization") != "Bearer review-dispatch-fixture-secret" || meta.SHA256 != moderationSHA(body) || meta.ByteSize != int64(len(body)) || len(body) == 0 {
						t.Error("review did not send complete authenticated original bytes")
						w.WriteHeader(400)
						return
					}
					if meta.Phase == "output" && !gate.enter(r.Context(), meta.Kind) {
						return
					}
					writeMediaResponse(w, meta, "allow")
				}))
				cfg.Moderation, cfg.MediaModeration = executionModerationConfig(t, nil), review.config(t)
			}
			// The existing fixture defaults to one HTTP connection per lane;
			// construct the native adapter with the same two-worker budgets so
			// its transport cannot falsely serialize the fairness experiment.
			_ = cfg.mediaProvider.(*AsyncMediaProvider).Close()
			provider, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: media.server.URL, APIKey: fixtureMediaKey, CredentialID: "delivery-integration", ImageModel: "fixture-image", VideoModel: "fixture-video", VideoSeconds: 8,
				AssetDir: cfg.AssetDir, allowLocalAssets: true, SubmitConcurrency: 2, QueryConcurrency: 2, DeliveryConcurrency: 2})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			cfg.mediaProvider = provider
			cfg.MediaScheduling = MediaSchedulingConfig{SubmitWorkers: 2, QueryWorkers: 2, DeliveryWorkers: 2, ReviewWorkers: 2, WorkspaceConcurrency: 1, ChannelConcurrency: 2}
			a, server := testApp(t, cfg)
			clients := []*http.Client{authClient(t), authClient(t)}
			identities := []testIdentity{registerFixture(t, clients[0], server.URL, "lane-fair-a@example.test"), registerFixture(t, clients[1], server.URL, "lane-fair-b@example.test")}
			// Random IDs do not decide which tenant this test calls A. No
			// target-lane cursor has run yet, so the lexicographically first
			// workspace is the actual first member of the production ring.
			if identities[0].Workspace.ID > identities[1].Workspace.ID {
				identities[0], identities[1] = identities[1], identities[0]
				clients[0], clients[1] = clients[1], clients[0]
			}
			var tasks []Task
			for range 3 {
				task, _ := acceptMediaExecution(t, a, server, clients[0], identities[0], "image", "workspace A backlog", newID("key"), 1)
				tasks = append(tasks, task)
			}
			bTask, _ := acceptMediaExecution(t, a, server, clients[1], identities[1], "video", "workspace B single item", newID("key"), 1)
			tasks = append(tasks, bTask)
			for _, task := range tasks {
				for phase := mediaSubmitLane; phase < lane; phase++ {
					deliveryStep(t, a, task.ID)
				}
				advanceMediaScheduleDue(t, a, task.ID)
			}
			first := schedulerStepAsync(a, lane, "")
			select {
			case tenant := <-gate.entered:
				if tenant != "A" {
					t.Fatal("production ring did not begin with the first workspace")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("first workspace did not enter its real HTTP stage")
			}
			second := schedulerStepAsync(a, lane, "")
			select {
			case tenant := <-gate.entered:
				if tenant != "B" {
					t.Fatal("blocked workspace exceeded its limit instead of rotating")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("blocked A backlog prevented the B workspace from entering")
			}
			schedulerStepDone(t, second, true)
			bResult := deliveryRead(t, a, bTask.ID)
			if lane == mediaQueryLane && bResult.Status != "INGESTING" || lane != mediaQueryLane && bResult.Status != "SUCCEEDED" || lane == mediaReviewLane && (bResult.Moderation == nil || bResult.Moderation.State != "ALLOWED") {
				t.Fatal("B did not commit its native stage while A remained blocked", bResult.Status)
			}
			if gate.aCalls.Load() != 1 || gate.bCalls.Load() != 1 || a.mediaScheduler.snapshot().Lanes[lane].ScopeInFlight != 1 {
				t.Fatal("fair stage did not retain only the blocked A reservation")
			}
			gate.unblock()
			schedulerStepDone(t, first, true)
			if a.mediaScheduler.snapshot().Lanes[lane].ScopeInFlight != 0 {
				t.Fatal("completed lane leaked scoped execution capacity")
			}
		})
	}
}
