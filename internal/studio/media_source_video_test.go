package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// The fixture provides valid private MP4 bytes; neither the recorded request
// nor the synthetic access key is included in assertion messages.
func sourceVideoFixture(t *testing.T) ([]byte, MediaEditSource) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("testdata", "moderation", "allow.mp4"))
	if err != nil {
		t.Fatal("missing MP4 fixture")
	}
	source := mediaFixtureReference(blob)
	source.ContentType = "video/mp4"
	return blob, source
}

func TestSourceVideoFrozenWireAndOriginalHandle(t *testing.T) {
	for _, op := range []string{"video_edit", "video_extend"} {
		t.Run(op, func(t *testing.T) {
			blob, source := sourceVideoFixture(t)
			input := mediaFixtureInput("video", 1)
			input.Operation = op
			input.Inputs.Prompt = "cut \"lights\" and keep\nmotion"
			input.Parameters.AspectRatio = ""
			dir, endpoint := "", "/v1/videos/edits_async"
			expected := map[string]any{"model": "licensed-fixture", "prompt": input.Inputs.Prompt, "video": "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(blob)}
			if op == "video_extend" {
				dir, endpoint = "backward", "/v1/videos/extensions_async"
				input.Parameters.Seconds = 6
				input.Parameters.Resolution = "720p"
				expected["seconds"], expected["resolution"], expected["extension_direction"] = float64(6), "720p", "backward"
			}
			var saved MediaSubmission
			var posts, gets atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
					t.Error("credential changed")
				}
				if r.Method == http.MethodGet {
					gets.Add(1)
					if r.URL.Path != "/v1/media/tasks_async/video-source-fixture" {
						t.Error("original query handle lost")
					}
					io.WriteString(w, `{"task_id":"video-source-fixture","media_type":"video","stage":"generating","status":"processing"}`)
					return
				}
				posts.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != endpoint || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Idempotency-Key") != saved.IdempotencyKey {
					t.Error("frozen request metadata changed")
				}
				raw, err := io.ReadAll(r.Body)
				var sent map[string]any
				digest := sha256.Sum256(raw)
				if err != nil || int64(len(raw)) != saved.BodySize || r.ContentLength != saved.BodySize || hex.EncodeToString(digest[:]) != saved.BodySHA256 ||
					json.Unmarshal(raw, &sent) != nil || !reflect.DeepEqual(sent, expected) {
					t.Error("frozen wire bytes or protocol fields differ")
				}
				w.WriteHeader(http.StatusAccepted)
				io.WriteString(w, `{"task_id":"video-source-fixture","query_url":"/v1/media/tasks_async/video-source-fixture"}`)
			}))
			defer server.Close()
			provider := mediaFixtureProvider(t, server)
			store, directory := newReferenceStoreForTest(t)
			provider.bodies = store
			opens := 0
			origOpen := source.Open
			source.Open = func() (io.ReadCloser, error) { opens++; return origOpen() }
			prepared, err := provider.PrepareSourceVideoModel(t.Context(), input, "licensed-fixture", dir, source)
			if err != nil || opens != 1 || posts.Load() != 0 || len(prepared.Body) != 0 || prepared.InputType != op || prepared.Endpoint != endpoint {
				t.Fatal("could not freeze original video", err)
			}
			metadata, err := json.Marshal(prepared)
			if err != nil || bytes.Contains(metadata, []byte("data:video")) || bytes.Contains(metadata, []byte(fixtureMediaKey)) || bytes.Contains(metadata, []byte("licensed-fixture")) || len(metadata) > 1500 || json.Unmarshal(metadata, &saved) != nil {
				t.Fatal("invalid persisted submission envelope")
			}
			reopened, err := OpenReferenceStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			provider.bodies = reopened
			h, err := provider.SubmitPrepared(t.Context(), saved)
			if err != nil || h.ID != "video-source-fixture" {
				t.Fatal("original submission failed", err)
			}
			for i := 0; i < 2; i++ {
				q, err := provider.QueryStatus(t.Context(), h)
				if err != nil || q.Result.Status != "RUNNING" {
					t.Fatal("original query failed", err)
				}
			}
			if posts.Load() != 1 || gets.Load() != 2 || opens != 1 {
				t.Fatal("unexpected source reopen, submission or query")
			}
		})
	}
}

func TestSourceVideoRejectsInvalidSourcesAndTamperedSpools(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	p := mediaFixtureProvider(t, server)
	store, directory := newReferenceStoreForTest(t)
	p.bodies = store
	in := mediaFixtureInput("video", 1)
	in.Operation = "video_edit"
	blob, source := sourceVideoFixture(t)
	prepare := func(s MediaEditSource, direction string) (MediaSubmission, error) {
		return p.PrepareSourceVideoModel(t.Context(), in, "fixture", direction, s)
	}
	if _, err := p.PrepareModel(in, "fixture"); err == nil {
		t.Fatal("ordinary JSON prepare bypassed source")
	}
	if _, err := prepare(source, "backward"); err == nil {
		t.Fatal("edit accepted extension direction")
	}
	for _, mode := range []string{"wrong-length", "extra-byte", "checksum", "mime", "non-mp4", "private-open-error", "storage-busy"} {
		t.Run(mode, func(t *testing.T) {
			v := source
			code := "SOURCE_VIDEO_UNAVAILABLE"
			switch mode {
			case "wrong-length":
				v.ByteSize++
			case "extra-byte":
				v.ByteSize--
			case "checksum":
				v.SHA256 = strings.Repeat("0", 64)
			case "mime":
				v.ContentType = "video/webm"
			case "non-mp4":
				v = mediaFixtureReference(bytes.Repeat([]byte("x"), len(blob)))
				v.ContentType = "video/mp4"
			case "private-open-error":
				v.Open = func() (io.ReadCloser, error) { return nil, errors.New("private-file-path") }
			case "storage-busy":
				code = "REFERENCE_PREPARATION_BUSY"
				v.Open = func() (io.ReadCloser, error) { return nil, &MediaProviderError{Code: "ASSET_STORAGE_BUSY"} }
			}
			_, err := prepare(v, "")
			var pe *MediaProviderError
			if !errors.As(err, &pe) || pe.Code != code || strings.Contains(err.Error(), "private-file-path") {
				t.Fatal("wrong private preparation failure", mode)
			}
		})
	}
	if store.usedFiles != 0 || store.reservedBytes != 0 {
		t.Fatal("failed source published files or leaked reservation")
	}
	valid, err := prepare(source, "")
	if err != nil {
		t.Fatal(err)
	}
	fail := func(s MediaSubmission) {
		t.Helper()
		_, err := p.SubmitPrepared(t.Context(), s)
		var pe *MediaProviderError
		if !errors.As(err, &pe) || !pe.ReconcileRequired || pe.Code != "SUBMISSION_IDENTITY_CHANGED" {
			t.Fatal("unsafe prepared submission accepted")
		}
	}
	for _, change := range []func(*MediaSubmission){
		func(v *MediaSubmission) { v.Endpoint = videoExtendEndpoint }, func(v *MediaSubmission) { v.InputType = "first_frame" },
		func(v *MediaSubmission) { v.BodyFileID = "" }, func(v *MediaSubmission) { v.Body = []byte("{}") },
		func(v *MediaSubmission) { v.BodySize++ }, func(v *MediaSubmission) { v.BodySHA256 = strings.Repeat("0", 64) },
		func(v *MediaSubmission) { v.ContentType = "application/json; charset=utf-8" },
		func(v *MediaSubmission) { v.ProviderIdentity = "another" }, func(v *MediaSubmission) { v.Kind = "image" },
	} {
		v := valid
		change(&v)
		fail(v)
	}
	name := filepath.Join(directory, valid.BodyFileID+".bin")
	f, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("x"), valid.BodySize-1)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fail(valid)
	if err = os.Remove(name); err != nil {
		t.Fatal(err)
	}
	fail(valid)
	if posts.Load() != 0 {
		t.Fatal("invalid source or changed spool dispatched")
	}
}

func TestSourceVideoEncoded64MiBBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("preparing or reading the frozen request sent an HTTP request")
	}))
	defer server.Close()
	p := mediaFixtureProvider(t, server)
	store, _ := newReferenceStoreForTest(t)
	p.bodies = store
	in := mediaFixtureInput("video", 1)
	in.Operation = "video_edit"
	in.Parameters.AspectRatio = ""
	// Measure the fixed JSON overhead using the actual request for a known source.
	blob, source := sourceVideoFixture(t)
	initial, err := p.PrepareSourceVideoModel(t.Context(), in, "fixture", "", source)
	if err != nil {
		t.Fatal(err)
	}
	overhead := initial.BodySize - int64(base64.StdEncoding.EncodedLen(len(blob)))
	in.Inputs.Prompt += strings.Repeat("p", int((maxSourceVideoBodyBytes-overhead)%4))
	overhead += int64((maxSourceVideoBodyBytes - overhead) % 4)
	maxRaw := (maxSourceVideoBodyBytes - overhead) / 4 * 3
	payload := func() io.Reader {
		return io.MultiReader(bytes.NewReader(blob), io.LimitReader(referenceZeroReader{}, maxRaw-int64(len(blob))))
	}
	hash := sha256.New()
	if n, err := io.Copy(hash, payload()); err != nil || n != maxRaw {
		t.Fatal("boundary fixture failed")
	}
	opens := 0
	source.SHA256 = hex.EncodeToString(hash.Sum(nil))
	source.ByteSize = maxRaw
	source.Open = func() (io.ReadCloser, error) { opens++; return io.NopCloser(payload()), nil }
	exact, err := p.PrepareSourceVideoModel(t.Context(), in, "fixture", "", source)
	if err != nil || exact.BodySize != maxSourceVideoBodyBytes {
		t.Fatal("exact 64 MiB JSON failed", err)
	}
	file, _, size, err := p.openSubmissionBody(t.Context(), exact)
	if err != nil || size != maxSourceVideoBodyBytes {
		t.Fatal("frozen 64 MiB body unavailable", err)
	}
	file.Close()
	source.ByteSize++
	_, err = p.PrepareSourceVideoModel(t.Context(), in, "fixture", "", source)
	var pe *MediaProviderError
	if !errors.As(err, &pe) || pe.Code != "SOURCE_VIDEO_BODY_TOO_LARGE" || opens != 1 || store.usedFiles != 2 {
		t.Fatal("encoded limit failed before source open")
	}
	t.Logf("exact %d-byte JSON frozen and verified; next source byte rejected", size)
}

func TestSourceVideoCancellationDiscardsPrivateBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled source dispatched") }))
	defer server.Close()
	p := mediaFixtureProvider(t, server)
	store, directory := newReferenceStoreForTest(t)
	p.bodies = store
	in := mediaFixtureInput("video", 1)
	in.Operation = "video_edit"
	blob, source := sourceVideoFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &mediaCancelReader{cancel: cancel}
	source.ByteSize = 1 << 20
	source.Open = func() (io.ReadCloser, error) { return io.NopCloser(io.MultiReader(bytes.NewReader(blob), reader)), nil }
	_, err := p.PrepareSourceVideoModel(ctx, in, "fixture", "", source)
	files, listErr := os.ReadDir(directory)
	if !errors.Is(err, context.Canceled) || reader.read == 0 || reader.read > 32<<10 || listErr != nil || len(files) != 0 || store.reservedBytes != 0 || store.usedFiles != 0 {
		t.Fatal("cancelled streaming left private bytes, reservations or unexpected reads")
	}
}

func TestSourceVideoAcceptedWithoutHandleRemainsUnknown(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "{}")
	}))
	defer s.Close()
	p := mediaFixtureProvider(t, s)
	p.bodies, _ = newReferenceStoreForTest(t)
	in := mediaFixtureInput("video", 1)
	in.Operation = "video_extend"
	_, src := sourceVideoFixture(t)
	frozen, err := p.PrepareSourceVideoModel(t.Context(), in, "fixture", "", src)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SubmitPrepared(t.Context(), frozen)
	var pe *MediaProviderError
	if !errors.As(err, &pe) || !pe.SubmissionUnknown || !pe.ReconcileRequired || calls.Load() != 1 {
		t.Fatal("unconfirmed acceptance did not remain unknown")
	}
}
