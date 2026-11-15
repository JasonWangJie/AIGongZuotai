package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresMediaReviewDispatchRejectsReadyReceiptTampering(t *testing.T) {
	for _, damage := range []string{"marker-removed", "manifest-created-at", "thumbnail-identity", "thumbnail-bytes", "thumbnail-missing"} {
		t.Run(damage, func(t *testing.T) {
			f := newDeliveryHTTPFixture(t, -1)
			cfg := deliveryTestConfig(t, f, true)
			cfg.Moderation = executionModerationConfig(t, nil)
			cfg.MediaModeration = MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}
			a, s := testApp(t, cfg)
			client, identity := deliveryIdentity(t, s, true)
			var checks atomic.Int32
			reviewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				meta, _ := readMediaMultipartFixture(t, r)
				checks.Add(1)
				writeMediaResponse(w, meta, "allow")
			}))
			defer reviewer.Close()
			a.mediaModeration = httpMediaEngine(t, reviewer.URL)
			task, _ := acceptMediaExecution(t, a, s, client, identity, "image", "frozen review receipt", newID("key"), 1)
			for range 3 {
				deliveryStep(t, a, task.ID)
			}
			record, err := a.store.loadTask(task.ID)
			if err != nil || record.ReviewDispatch == nil || record.ReviewDispatch.State != "READY" {
				t.Fatal("private batch did not reach READY", err)
			}
			info := *record.MediaModeration.Candidates[0].StorageInfo
			if info.Delivery == nil || info.Thumbnail == nil {
				t.Fatal("fixture did not produce complete frozen receipt")
			}
			manifest := filepath.Join(cfg.AssetDir, info.ID+".json")
			switch damage {
			case "marker-removed":
				info.Delivery = nil
			case "manifest-created-at":
				info.CreatedAt = info.CreatedAt.Add(time.Second)
			case "thumbnail-identity":
				thumb := *info.Thumbnail
				thumb.CreatedAt = thumb.CreatedAt.Add(time.Second)
				info.Thumbnail = &thumb
			case "thumbnail-bytes":
				path := filepath.Join(cfg.AssetDir, info.ID+".thumb.png")
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				raw[len(raw)-1] ^= 1
				if e = os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "thumbnail-missing":
				if e := os.Remove(filepath.Join(cfg.AssetDir, info.ID+".thumb.png")); e != nil {
					t.Fatal(e)
				}
			}
			if damage == "marker-removed" || damage == "manifest-created-at" || damage == "thumbnail-identity" {
				raw, _ := json.Marshal(info)
				if err = os.WriteFile(manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Independent review uses private storage only, even if generation
			// has been disabled after all receipts and READY were committed.
			original := a.media
			defer func() {
				if closer, ok := original.(interface{ Close() error }); ok {
					_ = closer.Close()
				}
			}()
			a.media = nil
			if !a.executePersistentTaskStep(task.ID) {
				t.Fatal("review was blocked by missing generation provider")
			}
			final, err := a.store.loadTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if checks.Load() != 0 || final.Task.Moderation.State != "REVIEW" || final.Task.Status != "FAILED" || len(final.Task.Artifacts) != 0 || final.ReviewDispatch.State != "FINISHED" {
				t.Fatal("changed completed receipt was reviewed or published")
			}
			code, _, _ := authRequest(t, client, s.URL, "GET", record.MediaModeration.Candidates[0].URL, "", identity.Project.ID, nil)
			if code != 404 {
				t.Fatal("changed private receipt became downloadable", code)
			}
			var count int
			if err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
				return conn.QueryRow(ctx, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1", task.ID).Scan(&count)
			}); err != nil || count != 0 {
				t.Fatal("invalid receipt was registered", err)
			}
		})
	}
}
