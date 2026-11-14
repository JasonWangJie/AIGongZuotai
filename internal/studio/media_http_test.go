package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureMediaKey = "fixture-key-not-real"

func mediaFixtureInput(kind string, count int) TaskInput {
	var input TaskInput
	input.Kind, input.ModelID, input.ProjectID = kind, "studio-"+kind, "prj_fixture"
	input.Inputs.Prompt = "合成协议测试，不调用模型"
	input.Parameters.AspectRatio, input.Parameters.Count = "16:9", count
	return input
}

func mediaFixtureProvider(t *testing.T, server *httptest.Server) *AsyncMediaProvider {
	t.Helper()
	provider, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: server.URL, APIKey: fixtureMediaKey,
		CredentialID: "fixture-credential", ImageModel: "fixture-image-model", VideoModel: "fixture-video-model",
		VideoSeconds: 8, VideoResolution: "720p", AssetDir: t.TempDir(), allowLocalAssets: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	return provider
}

func mediaFixtureHandle(provider *AsyncMediaProvider, kind string, count int) MediaHandle {
	id := "asyncimg_fixture"
	if kind == "video" {
		id = "video_fixture"
	}
	return MediaHandle{ID: id, Kind: kind, Count: count, SubmittedAt: time.Now(), CredentialID: provider.credentialID, ProviderIdentity: provider.providerIdentity}
}

func mediaFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "media", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	canvas := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.NRGBA{R: 20, G: 80, B: 160, A: 255})
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func requireMediaError(t *testing.T, err error) *MediaProviderError {
	t.Helper()
	var mediaErr *MediaProviderError
	if !errors.As(err, &mediaErr) {
		t.Fatalf("expected classified media error, got %v", err)
	}
	return mediaErr
}

func TestAsyncMediaPreparedBytesAndImageVideoPaths(t *testing.T) {
	var requests atomic.Int32
	var firstBody []byte
	var firstKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			t.Error("incorrect submit method or credentials")
		}
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == imageGenerationEndpoint {
			if firstBody == nil {
				firstBody = body
				firstKey = r.Header.Get("Idempotency-Key")
			} else if !bytes.Equal(firstBody, body) || firstKey != r.Header.Get("Idempotency-Key") {
				t.Error("persisted submit bytes/key changed")
			}
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(202)
			w.Write(mediaFixture(t, "accepted-image.json"))
			return
		}
		if r.URL.Path != videoGenerationEndpoint {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var parsed map[string]any
		json.Unmarshal(body, &parsed)
		if parsed["model"] != "fixture-video-model" || parsed["seconds"] != float64(8) || parsed["resolution"] != "720p" {
			t.Errorf("incorrect video body: %s", body)
		}
		if _, hasN := parsed["n"]; hasN {
			t.Error("image n must not be added to video request")
		}
		w.WriteHeader(202)
		w.Write(mediaFixture(t, "accepted-video.json"))
	}))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	prepared, err := provider.Prepare(mediaFixtureInput("image", 2))
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(prepared)
	if strings.Contains(string(saved), fixtureMediaKey) {
		t.Fatal("raw API key leaked into submission record")
	}
	var restored MediaSubmission
	if err := json.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	provider.imageModel = "changed-config-model"
	for range 2 {
		handle, err := provider.SubmitPrepared(context.Background(), restored)
		if err != nil {
			t.Fatal(err)
		}
		if handle.Count != 2 || handle.RetryAfter != 7*time.Second || handle.ProviderIdentity != provider.providerIdentity {
			t.Fatalf("incorrect handle: %+v", handle)
		}
	}
	if !strings.Contains(string(firstBody), "fixture-image-model") {
		t.Fatal("reserialized stored request using new model configuration")
	}
	video, err := provider.Prepare(mediaFixtureInput("video", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.SubmitPrepared(context.Background(), video); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatal("unexpected automatic generation retries")
	}
}

func TestAsyncMediaRejectsChangedSavedIdentity(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	prepared, _ := provider.Prepare(mediaFixtureInput("image", 1))
	mutations := []func(*MediaSubmission){func(s *MediaSubmission) { s.Body = append([]byte(" "), s.Body...) }, func(s *MediaSubmission) { s.BaseURL = "https://other.example" }, func(s *MediaSubmission) { s.CredentialID = "rotated" }, func(s *MediaSubmission) { s.ProviderIdentity = "different-key" }, func(s *MediaSubmission) { s.Endpoint = "/unrelated" }}
	for _, mutate := range mutations {
		copy := prepared
		mutate(&copy)
		_, err := provider.SubmitPrepared(context.Background(), copy)
		if !requireMediaError(t, err).ReconcileRequired {
			t.Fatal("changed identity must be reconciled")
		}
	}
	handle := mediaFixtureHandle(provider, "image", 1)
	handle.ProviderIdentity = "prior-gateway-key"
	_, err := provider.Query(context.Background(), handle)
	if !requireMediaError(t, err).ReconcileRequired {
		t.Fatal("query identity mismatch must not execute")
	}
	if calls.Load() != 0 {
		t.Fatal("identity mismatch made an HTTP request")
	}
}

func TestAsyncMediaSubmissionUncertaintyAndRejection(t *testing.T) {
	tests := []struct {
		name               string
		status             int
		body               string
		unknown, retryable bool
	}{
		{"bad accepted", 202, `{"task_id":""}`, true, false},
		{"malformed accepted", 202, `not-json`, true, false},
		{"server failure", 503, `{"error":{"message":"secret fixture-key-not-real"}}`, true, false},
		{"conflict", 409, `{}`, true, false},
		{"unexpected success", 200, `{}`, true, false},
		{"invalid input", 400, `{"error":{"message":"secret fixture-key-not-real"}}`, false, false},
		{"rate limit", 429, `{}`, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "9")
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			provider := mediaFixtureProvider(t, server)
			prepared, _ := provider.Prepare(mediaFixtureInput("image", 1))
			_, err := provider.SubmitPrepared(context.Background(), prepared)
			classified := requireMediaError(t, err)
			if classified.SubmissionUnknown != test.unknown || classified.Retryable != test.retryable {
				t.Fatalf("wrong classification: %+v", classified)
			}
			if strings.Contains(classified.Error(), fixtureMediaKey) {
				t.Fatal("upstream error exposed a secret")
			}
			if calls.Load() != 1 {
				t.Fatal("automatic retry occurred")
			}
		})
	}
	t.Run("connection lost after acceptance", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			io.Copy(io.Discard, r.Body)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
		}))
		defer server.Close()
		provider := mediaFixtureProvider(t, server)
		_, err := provider.Submit(context.Background(), mediaFixtureInput("image", 1))
		if !requireMediaError(t, err).SubmissionUnknown || calls.Load() != 1 {
			t.Fatal("lost response must retain uncertainty without retry")
		}
	})
}

func TestAsyncMediaQueryStateAndErrorSemantics(t *testing.T) {
	tests := []struct {
		name                 string
		status               int
		body                 string
		wantStatus, code     string
		retryable, reconcile bool
	}{
		{"queued", 200, `{"task_id":"asyncimg_fixture","media_type":"image","stage":"queued","status":"queued","progress":0}`, "QUEUED", "", false, false},
		{"generating", 200, string(mediaFixture(t, "generating.json")), "RUNNING", "", false, false},
		{"saving", 200, `{"task_id":"asyncimg_fixture","media_type":"image","stage":"saving","status":"saving"}`, "INGESTING", "", false, false},
		{"unknown execution", 200, string(mediaFixture(t, "execution-unknown.json")), "", "EXECUTION_UNKNOWN", false, true},
		{"postprocess failure", 200, string(mediaFixture(t, "postprocess-failed.json")), "", "POSTPROCESS_REQUIRES_RECONCILIATION", false, true},
		{"expired result", 200, string(mediaFixture(t, "result-expired.json")), "", "RESULT_EXPIRED", false, true},
		{"known generation failure", 200, `{"task_id":"asyncimg_fixture","media_type":"image","stage":"failed","status":"failed","error_code":601}`, "FAILED", "", false, false},
		{"wrong task", 200, `{"task_id":"someone_else","media_type":"image","stage":"queued"}`, "", "QUERY_IDENTITY_CHANGED", false, true},
		{"unknown stage", 200, `{"task_id":"asyncimg_fixture","media_type":"image","stage":"new_stage"}`, "", "UNKNOWN_TASK_STATE", false, true},
		{"malformed", 200, `{`, "", "QUERY_PROTOCOL_ERROR", true, false},
		{"query missing", 404, `{}`, "", "QUERY_REQUIRES_RECONCILIATION", false, true},
		{"query rate limited", 429, `{}`, "", "QUERY_TEMPORARY", true, false},
		{"query server failure", 503, `{}`, "", "QUERY_TEMPORARY", true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != mediaQueryPrefix+"asyncimg_fixture" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
					t.Error("incorrect query contract")
				}
				w.Header().Set("Retry-After", "6")
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			provider := mediaFixtureProvider(t, server)
			result, err := provider.Query(context.Background(), mediaFixtureHandle(provider, "image", 1))
			if test.code != "" {
				classified := requireMediaError(t, err)
				if classified.Code != test.code || classified.Retryable != test.retryable || classified.ReconcileRequired != test.reconcile {
					t.Fatalf("wrong classification: %+v", classified)
				}
				return
			}
			if err != nil || result.Status != test.wantStatus {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Status != "FAILED" && result.RetryAfter != 6*time.Second {
				t.Fatal("normal query Retry-After ignored")
			}
		})
	}
}

func TestAsyncMediaSafeImportPartialVideoAndAssetServing(t *testing.T) {
	pngData := fixturePNG(t)
	mp4Data := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	var server *httptest.Server
	var assetGets atomic.Int32
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case mediaQueryPrefix + "asyncimg_fixture":
			w.Write(bytes.ReplaceAll(mediaFixture(t, "completed-image.json"), []byte("{{RESULT_URL}}"), []byte(server.URL+"/redirect?signature=fixture-only")))
		case mediaQueryPrefix + "video_fixture":
			json.NewEncoder(w).Encode(map[string]any{"task_id": "video_fixture", "media_type": "video", "stage": "completed", "status": "succeeded", "data": []map[string]string{{"url": server.URL + "/video"}}})
		case "/redirect":
			if r.Header.Get("Authorization") != "" {
				t.Error("API key leaked to result redirect")
			}
			http.Redirect(w, r, server.URL+"/image?signature=preserved", http.StatusFound)
		case "/image", "/video":
			assetGets.Add(1)
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
				t.Error("credentials or referrer leaked into media download")
			}
			if r.URL.Path == "/image" {
				if r.URL.Query().Get("signature") != "preserved" {
					t.Error("signed query parameters dropped")
				}
				w.Write(pngData)
			} else {
				w.Write(mp4Data)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := mediaFixtureProvider(t, server)
	imageResult, err := provider.Query(context.Background(), mediaFixtureHandle(provider, "image", 2))
	if err != nil {
		t.Fatal(err)
	}
	if imageResult.Status != "PARTIAL" || len(imageResult.Artifacts) != 1 || imageResult.Error == nil {
		t.Fatalf("partial result not represented: %+v", imageResult)
	}
	artifact := imageResult.Artifacts[0]
	if !strings.HasPrefix(artifact.URL, "/api/v1/assets/asset_") || strings.Contains(artifact.URL, server.URL) {
		t.Fatal("upstream URL exposed to browser")
	}
	response := httptest.NewRecorder()
	provider.ServeAsset(response, httptest.NewRequest("GET", artifact.URL, nil), artifact.ID)
	if response.Code != 200 || response.Header().Get("Content-Type") != "image/png" || !bytes.Equal(response.Body.Bytes(), pngData) || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("stored image delivery failed")
	}
	if _, err := provider.Query(context.Background(), mediaFixtureHandle(provider, "image", 2)); err != nil {
		t.Fatal(err)
	}
	if assetGets.Load() != 1 {
		t.Fatal("already imported result downloaded again")
	}
	videoResult, err := provider.Query(context.Background(), mediaFixtureHandle(provider, "video", 1))
	if err != nil {
		t.Fatal(err)
	}
	if videoResult.Status != "SUCCEEDED" || videoResult.Artifacts[0].Kind != "video" {
		t.Fatal("video path did not produce a video asset")
	}
	request := httptest.NewRequest("GET", videoResult.Artifacts[0].URL, nil)
	request.Header.Set("Range", "bytes=0-7")
	rangeResponse := httptest.NewRecorder()
	provider.ServeAsset(rangeResponse, request, videoResult.Artifacts[0].ID)
	if rangeResponse.Code != 206 || rangeResponse.Body.Len() != 8 {
		t.Fatal("asset byte-range delivery failed")
	}
	if !errors.Is(provider.Cancel(context.Background(), mediaFixtureHandle(provider, "image", 1)), ErrMediaCancelUnsupported) {
		t.Fatal("cancel must not pretend to cancel unsupported upstream")
	}
	if _, _, err := provider.assets.OpenAsset("../../secret"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("asset path traversal was accepted")
	}
}

func TestAssetPolicyPublicAddressesAndURLs(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.1.2", "0.0.0.0", "224.0.0.1", "198.18.1.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2001:db8::1"} {
		if publicAssetIP(net.ParseIP(ip), false) {
			t.Errorf("private/special IP accepted: %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2001:4860:4860::8888"} {
		if !publicAssetIP(net.ParseIP(ip), false) {
			t.Errorf("public IP rejected: %s", ip)
		}
	}
	store, err := newLocalAssetStore(t.TempDir(), "https://gateway.example", []string{"cdn.example"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, raw := range []string{"http://cdn.example/image.png", "https://attacker.example/image.png", "https://user:pass@cdn.example/image.png", "https://cdn.example:8443/image.png", "https://cdn.example/image.png#fragment"} {
		u, _ := url.Parse(raw)
		if store.validateURL(u) == nil {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	u, _ := url.Parse("https://cdn.example/image.png?signature=keep")
	if err := store.validateURL(u); err != nil {
		t.Fatal(err)
	}
	store.hosts["127.0.0.1"] = true
	if _, err := store.dialPublicAddress(context.Background(), "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("default policy dialed loopback")
	}
}

func TestAssetInvalidOversizedAndRedirectedResults(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     []byte
		limit    int64
		redirect string
		code     string
	}{
		{"svg refused", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 1024, "", "ASSET_FORMAT_UNSUPPORTED"},
		{"truncated PNG", fixturePNG(t)[:40], 1024, "", "ASSET_FORMAT_UNSUPPORTED"},
		{"oversized", fixturePNG(t), 8, "", "ASSET_TOO_LARGE"},
		{"redirect SSRF", nil, 1024, "http://169.254.169.254/latest/meta-data/", "ASSET_SOURCE_NOT_ALLOWED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.redirect != "" {
					http.Redirect(w, r, test.redirect, 302)
					return
				}
				w.Write(test.body)
			}))
			defer server.Close()
			provider := mediaFixtureProvider(t, server)
			provider.assets.imageLimit = test.limit
			_, err := provider.assets.Import(context.Background(), "fixture", server.URL+"/result", "image")
			if classified := requireMediaError(t, err); classified.Code != test.code {
				t.Fatalf("unexpected error: %+v", classified)
			}
		})
	}
}

func TestAssetGIFAllocationBudget(t *testing.T) {
	for _, frames := range []int{2, 17} {
		animation := gif.GIF{}
		for range frames {
			animation.Image = append(animation.Image, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White}))
			animation.Delay = append(animation.Delay, 10)
		}
		var encoded bytes.Buffer
		if err := gif.EncodeAll(&encoded, &animation); err != nil {
			t.Fatal(err)
		}
		_, err := validateAssetEncoding(bytes.NewReader(encoded.Bytes()), "image", int64(encoded.Len()))
		if frames == 2 && err != nil {
			t.Fatal(err)
		}
		if frames == 17 && err == nil {
			t.Fatal("GIF frame budget not enforced")
		}
	}
}

func TestMediaRetryAfterAndConfiguration(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if retryAfterDuration("12", now) != 12*time.Second || retryAfterDuration(now.Add(20*time.Second).Format(http.TimeFormat), now) != 20*time.Second || retryAfterDuration("bad", now) != 3*time.Second {
		t.Fatal("Retry-After seconds/date/fallback are incorrect")
	}
	for _, base := range []string{"https://gateway.example/v1", "https://user:pass@gateway.example", "http://remote.example", "https://gateway.example?key=secret"} {
		if provider, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: base, APIKey: fixtureMediaKey, CredentialID: "fixture", ImageModel: "image", AssetDir: t.TempDir()}); err == nil {
			provider.Close()
			t.Fatalf("invalid service root accepted: %s", base)
		}
	}
}

func TestAssetReaderSurvivesProviderShutdownAndCannotImport(t *testing.T) {
	pngData := fixturePNG(t)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Write(pngData)
	}))
	directory := t.TempDir()
	writer, err := newLocalAssetStore(directory, server.URL, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := writer.Import(context.Background(), "offline-retained-asset", server.URL+"/result", "image")
	if err != nil {
		writer.Close()
		server.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server.Close()
	reader, err := OpenLocalAssetReader(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	response := httptest.NewRecorder()
	reader.ServeAsset(response, httptest.NewRequest("GET", "/api/v1/assets/"+asset.ID, nil), asset.ID)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), pngData) {
		t.Fatal("stored asset did not survive provider/network shutdown")
	}
	if downloads.Load() != 1 {
		t.Fatal("reader accessed a gateway")
	}
	_, err = reader.Import(context.Background(), "offline-retained-asset", "https://untrusted.example/result", "image")
	if requireMediaError(t, err).Code != "ASSET_STORE_READ_ONLY" {
		t.Fatal("read-only asset access allowed Import")
	}
	missing := filepath.Join(directory, "must-not-create")
	if unexpected, err := OpenLocalAssetReader(missing); err == nil {
		unexpected.Close()
		t.Fatal("reader unexpectedly created a missing directory")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reader mutated the filesystem")
	}
}
