package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type s3StoredFixture struct {
	data            []byte
	mime, sha, etag string
}
type s3Fixture struct {
	mu             sync.Mutex
	objects        map[string]s3StoredFixture
	failPut        bool
	failAfterWrite bool
	puts           int
}

func (f *s3Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(403)
		return
	}
	object, ok := f.objects[r.URL.Path]
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", code)
	}
	switch r.Method {
	case "PUT":
		f.puts++
		if f.failPut {
			fail(503, "ServiceUnavailable")
			return
		}
		if ok && r.Header.Get("If-None-Match") == "*" {
			fail(412, "PreconditionFailed")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			fail(400, "IncompleteBody")
			return
		}
		sum := sha256.Sum256(body)
		if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			fail(400, "BadDigest")
			return
		}
		object = s3StoredFixture{data: body, mime: r.Header.Get("Content-Type"), sha: r.Header.Get("X-Amz-Meta-Sha256"), etag: `"` + hex.EncodeToString(sum[:]) + `"`}
		f.objects[r.URL.Path] = object
		if f.failAfterWrite {
			fail(500, "InternalError")
			return
		}
		w.Header().Set("ETag", object.etag)
		w.WriteHeader(200)
	case "GET", "HEAD":
		if !ok {
			fail(404, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Type", object.mime)
		w.Header().Set("X-Amz-Meta-Sha256", object.sha)
		w.Header().Set("ETag", object.etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(object.data)))
		http.ServeContent(w, r, "object", time.Time{}, bytes.NewReader(object.data))
	case "DELETE":
		delete(f.objects, r.URL.Path)
		w.WriteHeader(204)
	default:
		fail(405, "MethodNotAllowed")
	}
}

func TestS3FailureRecoveryPreservesReceiptAndSource(t *testing.T) {
	fixture := &s3Fixture{objects: map[string]s3StoredFixture{}, failPut: true}
	server := httptest.NewServer(fixture)
	defer server.Close()
	directory := t.TempDir()
	legacy := seedStorageAsset(t, directory)
	config := AssetStorageConfig{Backend: "s3", Endpoint: server.URL, Region: "test", Bucket: "fixture-private", Prefix: "unit", AccessKeyID: "fixture", SecretAccessKey: "not-real", ForcePathStyle: true}
	storage, err := NewAssetStorage(config, directory)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.(*S3AssetStorage)
	defer store.Close()
	ctx := context.Background()
	_, err = store.promote(ctx, legacy)
	var providerErr *MediaProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable {
		t.Fatal("temporary storage failure lost retry classification")
	}
	manifest, err := readAssetManifest(store.local.root, legacy.ID)
	if err != nil || manifest.StorageBackend == "s3" {
		t.Fatal("receipt committed before verified object")
	}
	fixture.mu.Lock()
	fixture.failPut = false
	fixture.failAfterWrite = true
	fixture.mu.Unlock()
	info, err := store.promote(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	before := fixture.puts
	fixture.mu.Unlock()
	if _, err = store.promote(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	if fixture.puts != before {
		t.Error("reentry rewrote remote object")
	}
	object := fixture.objects["/"+config.Bucket+"/"+info.ObjectKey]
	object.sha = "changed"
	fixture.objects["/"+config.Bucket+"/"+info.ObjectKey] = object
	fixture.mu.Unlock()
	if _, err = store.Stat(ctx, info.ID); !errors.As(err, &providerErr) || !providerErr.ReconcileRequired {
		t.Fatal("changed remote metadata accepted")
	}
	recorder := httptest.NewRecorder()
	store.ServeAsset(recorder, httptest.NewRequest("GET", "/private", nil), info.ID)
	if recorder.Code != 404 {
		t.Fatal("changed object fell back to retained local")
	}
	if _, err = os.Stat(filepath.Join(directory, info.ID+".bin")); err != nil {
		t.Fatal("failed promotion removed local source")
	}
}

func TestS3MigrationRejectsLocalCorruptionAndPagesWithoutIO(t *testing.T) {
	fixture := &s3Fixture{objects: map[string]s3StoredFixture{}}
	server := httptest.NewServer(fixture)
	defer server.Close()
	directory := t.TempDir()
	info := seedStorageAsset(t, directory)
	config := AssetStorageConfig{Backend: "s3", Endpoint: server.URL, Region: "test", Bucket: "fixture-private", AccessKeyID: "fixture", SecretAccessKey: "not-real", ForcePathStyle: true}
	data, err := os.ReadFile(filepath.Join(directory, info.ID+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err = os.WriteFile(filepath.Join(directory, info.ID+".bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := MigrateAssets(context.Background(), AssetStorageConfig{}, directory, false, 1, "")
	if err != nil || len(plan.Items) != 1 || plan.Applied {
		t.Fatal("offline plan required S3 or changed storage")
	}
	_, err = MigrateAssets(context.Background(), config, directory, true, 1, "")
	var p *MediaProviderError
	if !errors.As(err, &p) || !p.ReconcileRequired {
		t.Fatalf("corrupt source was accepted: %v", err)
	}
	fixture.mu.Lock()
	puts := fixture.puts
	fixture.mu.Unlock()
	if puts != 0 {
		t.Fatal("corrupt bytes reached remote storage")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := 0; i < 3; i++ {
		clone := info
		sum := sha256.Sum256([]byte(strconv.Itoa(i)))
		clone.ID = "asset_" + hex.EncodeToString(sum[:])
		if err = writeAssetManifest(root, clone); err != nil {
			t.Fatal(err)
		}
	}
	first, err := MigrateAssets(context.Background(), AssetStorageConfig{}, directory, false, 2, "")
	if err != nil || len(first.Items) != 2 || !first.HasMore {
		t.Fatal("first migration page differs")
	}
	second, err := MigrateAssets(context.Background(), AssetStorageConfig{}, directory, false, 2, first.NextAfter)
	if err != nil || len(second.Items) != 2 || second.HasMore {
		t.Fatal("second migration page differs")
	}
	if first.Items[1].ID >= second.Items[0].ID {
		t.Fatal("migration cursor duplicates or reorders IDs")
	}
}
