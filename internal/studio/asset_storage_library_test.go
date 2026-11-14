package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// This test uses the actual App HTTP permission/catalog boundary and a real
// isolated S3 service. Only the model gateway is a free local protocol fixture.
// Memory catalog seeding in the provider-disabled second app does not assert
// PostgreSQL restart persistence; that is covered by core's separate PG tests.
func TestS3RealLibraryLifecycle(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires an isolated real loopback S3 service via TEST_S3_*")
	}
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
		t.Fatal("S3 lifecycle verification is limited to loopback")
	}
	storageConfig := AssetStorageConfig{Backend: "s3", Endpoint: endpoint, Region: "us-east-1", Bucket: os.Getenv("TEST_S3_BUCKET"), Prefix: "library/" + newID("run"), AccessKeyID: os.Getenv("TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"), ForcePathStyle: true}
	imageBytes := fixturePNG(t)
	var submits atomic.Int32
	var gateway *httptest.Server
	gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == imageGenerationEndpoint:
			submits.Add(1)
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"task_id":"library-s3-fixture"}`)
		case r.Method == "GET" && r.URL.Path == mediaQueryPrefix+"library-s3-fixture":
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "library-s3-fixture", "media_type": "image", "stage": "completed", "status": "succeeded", "data": []map[string]string{{"url": gateway.URL + "/result"}}})
		case r.Method == "GET" && r.URL.Path == "/result":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("gateway credentials leaked to result host")
			}
			_, _ = w.Write(imageBytes)
		default:
			t.Errorf("unexpected model fixture request: %s", r.Method)
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()
	config := Config{Mode: "live", AsyncImageBaseURL: gateway.URL, AsyncImageAPIKey: fixtureMediaKey, AsyncImageModel: "fixture-image", AsyncImageCredentialID: "s3-library-fixture", AssetStorage: storageConfig, AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), PollInterval: time.Hour}
	app, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { app.Close() }()
	server := httptest.NewServer(app.Handler())
	defer func() { server.Close() }()
	provider := app.media.(*AsyncMediaProvider)
	provider.assets.allowLocal = true
	store := app.assets.(*S3AssetStorage)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = store.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(storageConfig.Bucket)}); err != nil {
		if _, headErr := store.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(storageConfig.Bucket)}); headErr != nil {
			t.Fatal("isolated fixture bucket unavailable")
		}
	}
	code, body, _ := request(t, server, "POST", "/api/v1/tasks", "s3-library-task-key", taskInput("image"))
	if code != 202 {
		t.Fatalf("task not accepted: %d %s", code, body)
	}
	created := decode[Task](t, body)
	app.executeTaskStep(created.ID)
	app.executeTaskStep(created.ID)
	code, body, _ = request(t, server, "GET", "/api/v1/tasks/"+created.ID, "", nil)
	completed := decode[Task](t, body)
	if code != 200 || completed.Status != "SUCCEEDED" || len(completed.Artifacts) != 1 || submits.Load() != 1 {
		t.Fatalf("actual provider/S3 task failed: %d %s", code, body)
	}
	artifactID := completed.Artifacts[0].ID
	libraryID := libraryID("generated", created.ID, artifactID)
	entry := libraryEntryHTTP(t, server, libraryID)
	if entry.ContentURL == nil || entry.ThumbnailURL == nil || entry.ByteSize != int64(len(imageBytes)) {
		t.Fatal("catalog omitted private original or thumbnail")
	}
	assertPublic := func(data []byte) {
		t.Helper()
		for _, secret := range []string{"storage_info", "object_key", "storage_identity", storageConfig.AccessKeyID, storageConfig.SecretAccessKey, storageConfig.Prefix} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatal("private storage data leaked in public HTTP response")
			}
		}
	}
	assertPublic(body)
	code, body, _ = request(t, server, "GET", "/api/v1/library/"+libraryID, "", nil)
	assertPublic(body)
	for _, path := range []string{*entry.ContentURL, *entry.ThumbnailURL, "/api/v1/assets/" + artifactID} {
		code, _, headers := request(t, server, "GET", path, "", nil)
		if code != 200 || headers.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("private catalog download failed %d", code)
		}
	}
	rangeRequest, _ := http.NewRequest("GET", server.URL+*entry.ContentURL, nil)
	rangeRequest.Header.Set("Range", "bytes=1-9")
	ranged, err := server.Client().Do(rangeRequest)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(ranged.Body)
	ranged.Body.Close()
	if ranged.StatusCode != 206 || !bytes.Equal(part, imageBytes[1:10]) {
		t.Fatal("HTTP catalog range failed")
	}
	code, body, _ = request(t, server, "POST", "/api/v1/projects", "", map[string]string{"name": "isolated project"})
	if code != 201 {
		t.Fatal("could not create second project")
	}
	other := decode[Project](t, body)
	for _, path := range []string{*entry.ContentURL, *entry.ThumbnailURL, "/api/v1/assets/" + artifactID, "/api/v1/library/" + libraryID} {
		u, _ := url.Parse(path)
		q := u.Query()
		q.Set("project_id", other.ID)
		u.RawQuery = q.Encode()
		code, _, _ = request(t, server, "GET", u.String(), "", nil)
		if code != 404 {
			t.Fatalf("cross-project private asset accepted: %d", code)
		}
	}
	code, body, _ = request(t, server, "POST", "/api/v1/library/"+libraryID+"/trash", "s3-library-trash-key", stateInput(entry.Revision))
	if code != 200 {
		t.Fatalf("trash failed %d", code)
	}
	trashed := decode[LibraryEntry](t, body)
	if trashed.ContentURL != nil || trashed.ThumbnailURL != nil {
		t.Fatal("trash retains public download URLs")
	}
	for _, path := range []string{*entry.ContentURL, *entry.ThumbnailURL, "/api/v1/assets/" + artifactID} {
		code, _, _ = request(t, server, "GET", path, "", nil)
		if code != 404 {
			t.Fatalf("new or legacy URL bypassed trash: %d", code)
		}
	}
	if _, err = store.Stat(ctx, artifactID); err != nil {
		t.Fatal("soft deletion removed real S3 object")
	}
	code, body, _ = request(t, server, "POST", "/api/v1/library/"+libraryID+"/restore", "s3-library-restore-key", stateInput(trashed.Revision))
	if code != 200 {
		t.Fatalf("restore failed %d", code)
	}
	for _, path := range []string{*entry.ContentURL, *entry.ThumbnailURL, "/api/v1/assets/" + artifactID} {
		code, _, _ = request(t, server, "GET", path, "", nil)
		if code != 200 {
			t.Fatalf("restore did not reopen private content: %d", code)
		}
	}
	code, replay, headers := request(t, server, "POST", "/api/v1/tasks", "s3-library-task-key", taskInput("image"))
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, replay).ID != created.ID || submits.Load() != 1 {
		t.Fatal("asset lifecycle caused a generation resubmission")
	}
	// Start a new provider-disabled app over the same private storage. Restore
	// only the test's in-memory source record to exercise the independent reader.
	app.mu.Lock()
	saved := app.tasks[created.ID].Task
	app.mu.Unlock()
	server.Close()
	app.Close()
	config.Mode = "demo"
	config.AsyncImageBaseURL = ""
	config.AsyncImageAPIKey = ""
	config.AsyncImageModel = ""
	config.AsyncImageCredentialID = ""
	app, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.registerMemoryTaskLibraryLocked(saved)
	app.mu.Unlock()
	server = httptest.NewServer(app.Handler())
	for _, path := range []string{*entry.ContentURL, *entry.ThumbnailURL, "/api/v1/assets/" + artifactID} {
		code, _, _ = request(t, server, "GET", path, "", nil)
		if code != 200 {
			t.Fatalf("provider-disabled storage download failed: %d", code)
		}
	}
	if err = app.assets.Delete(ctx, artifactID); err != nil {
		t.Fatal("isolated object cleanup failed")
	}
}
