package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The provider contract is tested independently of the application's reference
// store, project ACLs and upload decoding. Those layers have their own tests.
type mediaFixtureBodyStore struct {
	directory string
	published int
}

func (s *mediaFixtureBodyStore) WriteBody(ctx context.Context, contentType string, write func(io.Writer) error) (MediaBodyInfo, error) {
	file, err := os.CreateTemp(s.directory, "prepare-")
	if err != nil {
		return MediaBodyInfo{}, err
	}
	name := file.Name()
	defer os.Remove(name)
	hash := sha256.New()
	if err = write(io.MultiWriter(file, hash)); err != nil {
		file.Close()
		return MediaBodyInfo{}, err
	}
	if err = ctx.Err(); err != nil {
		file.Close()
		return MediaBodyInfo{}, err
	}
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return MediaBodyInfo{}, err
	}
	if err = file.Close(); err != nil {
		return MediaBodyInfo{}, err
	}
	if stat.Size() > maxMediaEditBodyBytes {
		return MediaBodyInfo{}, errors.New("fixture request exceeds cap")
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	id := "body_" + sum
	if err = os.Rename(name, filepath.Join(s.directory, id)); err != nil {
		return MediaBodyInfo{}, err
	}
	s.published++
	return MediaBodyInfo{ID: id, ContentType: contentType, SHA256: sum, ByteSize: stat.Size()}, nil
}
func (s *mediaFixtureBodyStore) OpenBody(id string) (*os.File, error) {
	if strings.ContainsAny(id, "/\\") {
		return nil, os.ErrNotExist
	}
	return os.Open(filepath.Join(s.directory, id))
}
func mediaFixtureReference(body []byte) MediaEditSource {
	sum := sha256.Sum256(body)
	return MediaEditSource{ContentType: "image/png", ByteSize: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }}
}

func TestAsyncImageEditSpoolReplayPreservesMultipartAndOldQueries(t *testing.T) {
	var calls atomic.Int32
	var saved MediaSubmission
	reference := fixturePNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			t.Error("edit lost server credential boundary")
		}
		if r.Method == "GET" {
			if r.URL.Path != mediaQueryPrefix+"edit-fixture" {
				t.Error("edit did not use common query route")
			}
			io.WriteString(w, `{"task_id":"edit-fixture","media_type":"image","stage":"generating","status":"processing","progress":20}`)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		sum := sha256.Sum256(body)
		if r.URL.Path != imageEditEndpoint || r.Header.Get("Idempotency-Key") != saved.IdempotencyKey || r.Header.Get("Content-Type") != saved.ContentType || r.ContentLength != saved.BodySize || int64(len(body)) != saved.BodySize || hex.EncodeToString(sum[:]) != saved.BodySHA256 {
			t.Error("multipart replay changed request identity")
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		fields := map[string]string{}
		images := 0
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				break
			}
			blob, _ := io.ReadAll(part)
			if part.FormName() == "image[]" {
				images++
				if part.FileName() != "reference-1.png" || part.Header.Get("Content-Type") != "image/png" || !bytes.Equal(blob, reference) {
					t.Error("private reference bytes or generated filename changed")
				}
			} else {
				fields[part.FormName()] = string(blob)
			}
		}
		if images != 1 || len(fields) != 6 || fields["model"] != "edit-upstream-v1" || fields["prompt"] != "preserve subject" || fields["n"] != "2" || fields["aspect_ratio"] != "1:1" || fields["size"] != "1024x1024" || fields["resolution"] != "2K" {
			t.Error("multipart fields do not match frozen edit parameters")
		}
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"task_id":"edit-fixture","query_url":"https://untrusted.invalid/query"}`)
	}))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	if provider.editClient.Transport != provider.client.Transport || provider.editClient.Timeout != 2*time.Minute || provider.client.Timeout != 30*time.Second {
		t.Fatal("large edit submission did not share the gateway pool with a bounded upload timeout")
	}
	store := &mediaFixtureBodyStore{directory: t.TempDir()}
	provider.bodies = store
	input := mediaFixtureInput("image", 2)
	input.Inputs.Prompt = "  preserve subject  "
	input.Parameters.AspectRatio, input.Parameters.Size, input.Parameters.Resolution = "1:1", "1024x1024", "2K"
	prepared, err := provider.PrepareEditModel(context.Background(), input, "edit-upstream-v1", []MediaEditSource{mediaFixtureReference(reference)})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || store.published != 1 {
		t.Fatal("prepare performed upstream I/O or failed to publish exactly one spool")
	}
	persisted, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(`"body":`)) || bytes.Contains(persisted, []byte(fixtureMediaKey)) || bytes.Contains(persisted, reference) {
		t.Fatal("database metadata contains a binary body or credential")
	}
	if err = json.Unmarshal(persisted, &saved); err != nil {
		t.Fatal(err)
	}
	// Simulate restart after the edit model was removed from the catalog.
	restarted, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: server.URL, APIKey: fixtureMediaKey, CredentialID: "fixture-credential", AssetDir: t.TempDir(), AllowNoDefaultModel: true, BodyStore: store, allowLocalAssets: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	for range 2 {
		handle, err := restarted.SubmitPrepared(context.Background(), saved)
		if err != nil {
			t.Fatal(err)
		}
		result, err := restarted.Query(context.Background(), handle)
		if err != nil || result.Status != "RUNNING" {
			t.Fatalf("old edit handle failed to query: %v", err)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("unexpected automatic generation replay")
	}
}

func TestAsyncImageEditRejectsCorruptSavedRequestBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	store := &mediaFixtureBodyStore{directory: t.TempDir()}
	provider.bodies = store
	saved, err := provider.PrepareEditModel(context.Background(), mediaFixtureInput("image", 1), "image-edit", []MediaEditSource{mediaFixtureReference(fixturePNG(t))})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*MediaSubmission){
		"hash":             func(s *MediaSubmission) { s.BodySHA256 = strings.Repeat("0", 64) },
		"length":           func(s *MediaSubmission) { s.BodySize++ },
		"missing":          func(s *MediaSubmission) { s.BodyFileID = "body_missing" },
		"type":             func(s *MediaSubmission) { s.ContentType = "application/json" },
		"boundary":         func(s *MediaSubmission) { s.ContentType = "multipart/form-data; boundary=changed-boundary" },
		"both bodies":      func(s *MediaSubmission) { s.Body = []byte("{}") },
		"generation route": func(s *MediaSubmission) { s.Endpoint = imageGenerationEndpoint },
		"video":            func(s *MediaSubmission) { s.Kind = "video"; s.Endpoint = videoGenerationEndpoint },
		"oversized":        func(s *MediaSubmission) { s.BodySize = maxMediaEditBodyBytes + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := saved
			mutate(&copy)
			_, err := provider.SubmitPrepared(context.Background(), copy)
			classified := requireMediaError(t, err)
			if !classified.ReconcileRequired || classified.SubmissionUnknown {
				t.Fatal("local invalid body was not rejected before submission")
			}
		})
	}
	path := filepath.Join(store.directory, saved.BodyFileID)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)/2] ^= 1
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.SubmitPrepared(context.Background(), saved); !requireMediaError(t, err).ReconcileRequired {
		t.Fatal("same-size altered spool accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid spool reached the gateway")
	}
}

func TestAsyncImageEditRejectsInvalidReferenceWithoutPublication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("prepare must not access network") }))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	store := &mediaFixtureBodyStore{directory: t.TempDir()}
	provider.bodies = store
	valid := mediaFixtureReference(fixturePNG(t))
	for name, mutate := range map[string]func(*MediaEditSource){
		"gif": func(r *MediaEditSource) { r.ContentType = "image/gif" }, "svg": func(r *MediaEditSource) { r.ContentType = "image/svg+xml" },
		"large": func(r *MediaEditSource) { r.ByteSize = maxMediaReferenceBytes + 1 }, "empty": func(r *MediaEditSource) { r.ByteSize = 0 },
		"length": func(r *MediaEditSource) { r.ByteSize-- }, "hash": func(r *MediaEditSource) { r.SHA256 = strings.Repeat("0", 64) },
		"missing opener": func(r *MediaEditSource) { r.Open = nil }, "disk error": func(r *MediaEditSource) {
			r.Open = func() (io.ReadCloser, error) { return nil, errors.New("secret-private-disk-path") }
		},
	} {
		t.Run(name, func(t *testing.T) {
			ref := valid
			mutate(&ref)
			_, err := provider.PrepareEditModel(context.Background(), mediaFixtureInput("image", 1), "edit", []MediaEditSource{ref})
			if err == nil || strings.Contains(err.Error(), "secret-private") {
				t.Fatal("invalid source accepted or private error leaked")
			}
			if store.published != 0 {
				t.Fatal("failed reference published a body")
			}
		})
	}
	for _, refs := range [][]MediaEditSource{nil, {valid, valid, valid, valid, valid}} {
		if _, err := provider.PrepareEditModel(context.Background(), mediaFixtureInput("image", 1), "edit", refs); err == nil {
			t.Fatal("invalid reference count accepted")
		}
	}
	if _, err := provider.PrepareEditModel(context.Background(), mediaFixtureInput("video", 1), "edit", []MediaEditSource{valid}); err == nil {
		t.Fatal("video accepted image edit protocol")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.PrepareEditModel(ctx, mediaFixtureInput("image", 1), "edit", []MediaEditSource{valid}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled prepare performed work")
	}
	files, err := os.ReadDir(store.directory)
	if err != nil || len(files) != 0 {
		t.Fatal("failed prepare left fixture spool files")
	}
}

type mediaCancelReader struct {
	read   int
	cancel context.CancelFunc
}

func (r *mediaCancelReader) Read(p []byte) (int, error) {
	r.read += len(p)
	clear(p)
	r.cancel()
	return len(p), nil
}
func (r *mediaCancelReader) Close() error { return nil }

func TestAsyncImageEditCancelledStreamingDoesNotPublish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("prepare must not access network") }))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	store := &mediaFixtureBodyStore{directory: t.TempDir()}
	provider.bodies = store
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &mediaCancelReader{cancel: cancel}
	ref := MediaEditSource{ContentType: "image/png", ByteSize: maxMediaReferenceBytes, SHA256: strings.Repeat("0", 64), Open: func() (io.ReadCloser, error) { return reader, nil }}
	_, err := provider.PrepareEditModel(ctx, mediaFixtureInput("image", 1), "edit", []MediaEditSource{ref})
	if !errors.Is(err, context.Canceled) || reader.read > 32<<10 || store.published != 0 {
		t.Fatalf("cancelled bounded streaming failed: read=%d published=%d err=%v", reader.read, store.published, err)
	}
}

type mediaFailingBodyStore struct{ failure error }

func (s mediaFailingBodyStore) WriteBody(context.Context, string, func(io.Writer) error) (MediaBodyInfo, error) {
	return MediaBodyInfo{}, s.failure
}
func (s mediaFailingBodyStore) OpenBody(string) (*os.File, error) { return nil, s.failure }

func TestAsyncImageEditPreservesSafeStorageBackpressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("prepare accessed network") }))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	for _, tc := range []struct {
		failure error
		code    string
	}{{ErrReferenceBusy, "REFERENCE_PREPARATION_BUSY"}, {ErrReferenceStorageFull, "REFERENCE_STORAGE_FULL"}, {os.ErrPermission, "MEDIA_REFERENCE_UNAVAILABLE"}} {
		provider.bodies = mediaFailingBodyStore{failure: fmt.Errorf("secret-private-path: %w", tc.failure)}
		_, err := provider.PrepareEditModel(context.Background(), mediaFixtureInput("image", 1), "edit", []MediaEditSource{mediaFixtureReference(fixturePNG(t))})
		classified := requireMediaError(t, err)
		if classified.Code != tc.code || strings.Contains(classified.Error(), "secret-private") {
			t.Fatalf("storage admission error lost safe category: %s", classified.Code)
		}
	}
}
