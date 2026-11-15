package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMediaModerationLocalVerifiedBytesAndBounds(t *testing.T) {
	dir := t.TempDir()
	info := seedStorageAsset(t, dir)
	store, err := OpenLocalAssetReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want, err := os.ReadFile(filepath.Join(dir, info.ID+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	readers := []io.ReadSeekCloser{}
	defer func() {
		for _, r := range readers {
			_ = r.Close()
		}
	}()
	for i := 0; i < cap(mediaModerationAssetSlots); i++ {
		r, got, err := OpenMediaModerationAsset(context.Background(), store, info.ID)
		if err != nil || got.SHA256 != info.SHA256 {
			t.Fatalf("verified open failed: %v", err)
		}
		readers = append(readers, r)
		body, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(body, want) {
			t.Fatal("review did not receive the verified original bytes at offset zero")
		}
		if _, err = r.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
	}
	if r, _, err := OpenMediaModerationAsset(context.Background(), store, info.ID); err == nil {
		r.Close()
		t.Fatal("review descriptors exceeded their bound")
	}
	if err = readers[0].Close(); err != nil {
		t.Fatal(err)
	}
	r, _, err := OpenMediaModerationAsset(context.Background(), store, info.ID)
	if err != nil {
		t.Fatal("closed review descriptor did not release its slot")
	}
	r.Close()
	if _, err = os.Stat(filepath.Join(dir, info.ID+".bin")); err != nil {
		t.Fatal("review Close deleted the accepted local original")
	}
}

func TestMediaModerationLocalRefusesChangedMissingAndWrongType(t *testing.T) {
	for _, mode := range []string{"tamper", "missing", "length", "directory", "type", "oversize", "remote_receipt"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			info := seedStorageAsset(t, dir)
			name := filepath.Join(dir, info.ID+".bin")
			switch mode {
			case "tamper":
				body, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				body[len(body)-1] ^= 1
				if err = os.WriteFile(name, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing", "directory":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if mode == "directory" {
					if err := os.Mkdir(name, 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "length":
				info.ByteSize++
			case "type":
				info.ContentType = "image/jpeg"
			case "oversize":
				info.ByteSize = (256 << 20) + 1
			case "remote_receipt":
				info.StorageBackend = "s3"
			}
			manifest, _ := json.Marshal(info)
			if err := os.WriteFile(filepath.Join(dir, info.ID+".json"), manifest, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenLocalAssetReader(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			r, _, err := OpenMediaModerationAsset(context.Background(), store, info.ID)
			if r != nil || err == nil {
				if r != nil {
					r.Close()
				}
				t.Fatal("invalid private candidate was opened for moderation")
			}
			var provider *MediaProviderError
			if !errors.As(err, &provider) || !provider.ReconcileRequired || strings.Contains(err.Error(), dir) {
				t.Fatalf("error lost safe reconciliation classification: %v", err)
			}
		})
	}
}

type mediaModerationS3Fixture struct {
	mu            sync.Mutex
	body          []byte
	info          AssetInfo
	mode          string
	gets, heads   int
	conditionOK   bool
	streamStarted chan struct{}
}

func (f *mediaModerationS3Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if r.Method == "HEAD" {
		f.heads++
	} else if r.Method == "GET" {
		f.gets++
	}
	f.conditionOK = r.Header.Get("Authorization") != "" && r.Header.Get("If-Match") == f.info.ObjectETag && r.URL.Query().Get("versionId") == f.info.ObjectVersion
	mode, info, body, started := f.mode, f.info, append([]byte(nil), f.body...), f.streamStarted
	f.mu.Unlock()
	if r.Method != "GET" || mode == "missing" {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(404)
		_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
		return
	}
	if mode == "tamper" {
		body[len(body)-1] ^= 1
	}
	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Amz-Meta-Sha256", info.SHA256)
	w.Header().Set("ETag", info.ObjectETag)
	w.Header().Set("X-Amz-Version-Id", info.ObjectVersion)
	if mode == "type" {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	if mode == "cancel" {
		_, _ = w.Write(body[:8])
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		return
	}
	_, _ = w.Write(body)
}

func newMediaModerationS3Fixture(t *testing.T, mode string) (*S3AssetStorage, *mediaModerationS3Fixture, string) {
	t.Helper()
	dir := t.TempDir()
	info := seedStorageAsset(t, dir)
	body, err := os.ReadFile(filepath.Join(dir, info.ID+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &mediaModerationS3Fixture{body: body, mode: mode, streamStarted: make(chan struct{})}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	store, err := newS3AssetStorage(AssetStorageConfig{Backend: "s3", Endpoint: server.URL, Region: "test", Bucket: "review-private", Prefix: "fixture", AccessKeyID: "fixture", SecretAccessKey: "not-real", ForcePathStyle: true}, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	info.StorageBackend, info.StorageIdentity = "s3", store.identity
	info.ObjectKey, info.ObjectETag, info.ObjectVersion = store.objectKey(info.ID, false), `"accepted-etag"`, "original-version"
	if err = writeAssetManifest(store.local.root, info); err != nil {
		t.Fatal(err)
	}
	fixture.info = info
	return store, fixture, dir
}

func assertMediaModerationNoTemps(t *testing.T, dir string) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil || len(names) != 0 {
		t.Fatalf("private review temporary files leaked: count=%d, error=%v", len(names), err)
	}
}

func TestMediaModerationS3ReadsVersionedBytesAndCleansTemporaryCopy(t *testing.T) {
	store, fixture, dir := newMediaModerationS3Fixture(t, "ok")
	r, info, err := OpenMediaModerationAsset(context.Background(), store, fixture.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(body, fixture.body) || info.ObjectVersion != fixture.info.ObjectVersion {
		t.Fatal("review did not receive exact accepted remote bytes")
	}
	fixture.mu.Lock()
	gets, heads, conditions := fixture.gets, fixture.heads, fixture.conditionOK
	fixture.mu.Unlock()
	if gets != 1 || heads != 0 || !conditions {
		t.Fatalf("review did not use the private versioned GET: gets=%d heads=%d conditions=%v", gets, heads, conditions)
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil || len(names) != 1 || !assetGCFile.MatchString(filepath.Base(names[0])) {
		t.Fatal("review spool does not use the existing private GC namespace")
	}
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	assertMediaModerationNoTemps(t, dir)
	if _, err = os.Stat(filepath.Join(dir, info.ID+".bin")); err != nil {
		t.Fatal("review Close deleted the retained source")
	}
}

func TestMediaModerationS3RefusesCorruptionWithoutLocalFallback(t *testing.T) {
	for _, mode := range []string{"tamper", "missing", "type"} {
		t.Run(mode, func(t *testing.T) {
			store, fixture, dir := newMediaModerationS3Fixture(t, mode)
			r, _, err := OpenMediaModerationAsset(context.Background(), store, fixture.info.ID)
			if err == nil || r != nil {
				if r != nil {
					r.Close()
				}
				t.Fatal("invalid remote object used its valid retained local backup")
			}
			assertMediaModerationNoTemps(t, dir)
			var provider *MediaProviderError
			if !errors.As(err, &provider) || !provider.ReconcileRequired || strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), fixture.info.ObjectKey) {
				t.Fatal("remote failure leaked storage details or lost reconciliation classification")
			}
		})
	}
}

func TestMediaModerationS3CancellationCleansSpool(t *testing.T) {
	store, fixture, dir := newMediaModerationS3Fixture(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		r, _, err := OpenMediaModerationAsset(ctx, store, fixture.info.ID)
		if r != nil {
			r.Close()
		}
		result <- err
	}()
	select {
	case <-fixture.streamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("private GET did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was not preserved: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled review did not stop")
	}
	assertMediaModerationNoTemps(t, dir)
}

func TestMediaModerationReturnedReaderCancellationCleansSpool(t *testing.T) {
	store, fixture, dir := newMediaModerationS3Fixture(t, "ok")
	ctx, cancel := context.WithCancel(context.Background())
	r, _, err := OpenMediaModerationAsset(ctx, store, fixture.info.ID)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if _, err = r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal("returned descriptor continued after cancellation")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	assertMediaModerationNoTemps(t, dir)
}
