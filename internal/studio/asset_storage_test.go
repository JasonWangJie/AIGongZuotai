package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func seedStorageAsset(t *testing.T, directory string) AssetInfo {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, 1024, 640))
	canvas.Set(9, 9, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, canvas); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	identity := sha256.Sum256([]byte(t.Name()))
	info := AssetInfo{ID: "asset_" + hex.EncodeToString(identity[:]), SHA256: hex.EncodeToString(sum[:]), ContentType: "image/png", ByteSize: int64(b.Len()), CreatedAt: time.Now().UTC().Truncate(time.Second)}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = os.WriteFile(filepath.Join(directory, info.ID+".bin"), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err = writeAssetManifest(root, info); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestLocalAssetStorageThumbnailAndBackendBoundary(t *testing.T) {
	dir := t.TempDir()
	info := seedStorageAsset(t, dir)
	store, err := NewAssetStorage(AssetStorageConfig{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	local := store.(*LocalAssetStore)
	info, err = ensureAssetThumbnail(context.Background(), local.root, info)
	if err != nil {
		t.Fatal(err)
	}
	if info.Thumbnail == nil {
		t.Fatal("thumbnail metadata missing")
	}
	recorder := httptest.NewRecorder()
	store.ServeThumbnail(recorder, httptest.NewRequest("GET", "/private", nil), info.ID)
	decoded, err := png.Decode(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 512 || decoded.Bounds().Dy() != 320 || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("thumbnail size or privacy headers differ")
	}
	info.StorageBackend = "s3"
	if err = writeAssetManifest(local.root, info); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	store.ServeAsset(recorder, httptest.NewRequest("GET", "/private", nil), info.ID)
	if recorder.Code != 404 {
		t.Fatal("local reader bypassed remote receipt")
	}
	if err = store.Delete(context.Background(), info.ID); err == nil {
		t.Fatal("local backend deleted an S3 receipt")
	}
}

func TestAssetStorageConfigurationRejectsCredentialRedirection(t *testing.T) {
	base := AssetStorageConfig{Backend: "s3", Endpoint: "https://storage.example", Region: "test", Bucket: "private-bucket", Prefix: "studio", AccessKeyID: "fixture", SecretAccessKey: "not-real", ForcePathStyle: true}
	for _, endpoint := range []string{"http://storage.example", "https://user:pass@storage.example", "https://storage.example/?secret=x", "https://storage.example/a", "https://storage.example/#x"} {
		c := base
		c.Endpoint = endpoint
		if s, err := NewAssetStorage(c, t.TempDir()); err == nil {
			s.Close()
			t.Fatalf("accepted unsafe endpoint %s", endpoint)
		}
	}
	c := base
	c.Prefix = "../other"
	if s, err := NewAssetStorage(c, t.TempDir()); err == nil {
		s.Close()
		t.Fatal("accepted traversing prefix")
	}
	c = base
	c.SecretAccessKey = ""
	if s, err := NewAssetStorage(c, t.TempDir()); err == nil {
		s.Close()
		t.Fatal("accepted implicit credentials")
	}
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; w.WriteHeader(200) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	c = base
	c.Endpoint = server.URL
	store, err := NewAssetStorage(c, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.(*S3AssetStorage).client.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String(c.Bucket)})
	if err == nil || redirected != 0 {
		t.Fatal("S3 authorization followed redirect")
	}
}

func TestS3RealPrivateImportMigrationAndRanges(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TEST_S3_* to an isolated real loopback S3-compatible service")
	}
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
		t.Fatal("real storage verification is restricted to loopback fixtures")
	}
	config := AssetStorageConfig{Backend: "s3", Endpoint: endpoint, Region: "us-east-1", Bucket: os.Getenv("TEST_S3_BUCKET"), Prefix: "tests/" + newID("run"), AccessKeyID: os.Getenv("TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"), ForcePathStyle: true}
	directory := t.TempDir()
	legacy := seedStorageAsset(t, directory)
	storage, err := NewAssetStorage(config, directory)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.(*S3AssetStorage)
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err = store.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(config.Bucket)})
	if err != nil {
		if _, headErr := store.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(config.Bucket)}); headErr != nil {
			t.Fatal("isolated S3 bucket could not be created or accessed")
		}
	}
	info, err := store.promote(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if info.StorageBackend != "s3" || info.Thumbnail == nil || info.Thumbnail.StorageBackend != "s3" {
		t.Fatal("object/thumbnail receipt was not committed")
	}
	defer func() { _ = store.Delete(context.Background(), info.ID) }()
	if _, err = os.Stat(filepath.Join(directory, info.ID+".bin")); err != nil {
		t.Fatal("migration deleted original local copy")
	}
	if _, err = os.Stat(filepath.Join(directory, info.ID+".thumb.png")); err != nil {
		t.Fatal("migration deleted local thumbnail")
	}
	remigrated, err := store.promote(ctx, legacy)
	if err != nil || remigrated.ObjectETag != info.ObjectETag {
		t.Fatal("reentrant migration changed object generation")
	}
	unauth, err := http.Get(endpoint + "/" + config.Bucket + "/" + info.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != 403 {
		t.Fatalf("direct unauthenticated object request status=%d", unauth.StatusCode)
	}
	// Exercise the unchanged provider query/import boundary against this real
	// object service; the upstream itself is a free, local protocol fixture.
	var gateway *httptest.Server
	gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, mediaQueryPrefix) {
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "asyncimg_fixture", "media_type": "image", "stage": "completed", "status": "succeeded", "data": []map[string]string{{"url": gateway.URL + "/result"}}})
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("result import leaked credentials")
		}
		_, _ = w.Write(fixturePNG(t))
	}))
	defer gateway.Close()
	provider, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: gateway.URL, APIKey: fixtureMediaKey, CredentialID: "s3-fixture", ImageModel: "fixture", AssetDir: t.TempDir(), AssetStorage: config, allowLocalAssets: true})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	result, err := provider.Query(ctx, mediaFixtureHandle(provider, "image", 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].StorageInfo == nil || result.Artifacts[0].StorageInfo.StorageBackend != "s3" {
		t.Fatal("provider did not attach private object metadata")
	}
	artifact := result.Artifacts[0]
	encoded, _ := json.Marshal(artifact)
	if bytes.Contains(encoded, []byte(config.Prefix)) || bytes.Contains(encoded, []byte("storage_info")) {
		t.Fatal("private storage identity leaked to public artifact")
	}
	served := httptest.NewRecorder()
	provider.ServeAsset(served, httptest.NewRequest("GET", "/private", nil), artifact.ID)
	if served.Code != 200 {
		t.Fatal("provider-independent S3 object was not served")
	}
	if err = provider.assets.destination.Delete(ctx, artifact.ID); err != nil {
		t.Fatal(err)
	}
	full := httptest.NewRecorder()
	store.ServeAsset(full, httptest.NewRequest("GET", "/api/v1/private", nil), info.ID)
	if full.Code != 200 || int64(full.Body.Len()) != info.ByteSize {
		t.Fatalf("private full download status=%d length=%d", full.Code, full.Body.Len())
	}
	hash := sha256.Sum256(full.Body.Bytes())
	if hex.EncodeToString(hash[:]) != info.SHA256 {
		t.Fatal("proxy bytes differ")
	}
	ranged := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/private", nil)
	request.Header.Set("Range", "bytes=5-18")
	store.ServeAsset(ranged, request, info.ID)
	if ranged.Code != 206 || !bytes.Equal(ranged.Body.Bytes(), full.Body.Bytes()[5:19]) {
		t.Fatalf("range proxy failed: %d", ranged.Code)
	}
	head := httptest.NewRecorder()
	store.ServeAsset(head, httptest.NewRequest("HEAD", "/private", nil), info.ID)
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatal("HEAD sent a body")
	}
	multi := httptest.NewRecorder()
	request = httptest.NewRequest("GET", "/private", nil)
	request.Header.Set("Range", "bytes=0-1,4-5")
	store.ServeAsset(multi, request, info.ID)
	if multi.Code != 206 || !strings.HasPrefix(multi.Header().Get("Content-Type"), "multipart/byteranges") {
		t.Fatal("multi-range response missing")
	}
	thumb := httptest.NewRecorder()
	store.ServeThumbnail(thumb, httptest.NewRequest("GET", "/private", nil), info.ID)
	decoded, err := png.Decode(bytes.NewReader(thumb.Body.Bytes()))
	if err != nil || decoded.Bounds().Dx() != 512 {
		t.Fatal("private S3 thumbnail failed")
	}
	badConfig := config
	badConfig.Prefix = "different"
	badStorage, err := NewAssetStorage(badConfig, directory)
	if err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewRecorder()
	badStorage.ServeAsset(bad, httptest.NewRequest("GET", "/private", nil), info.ID)
	badStorage.Close()
	if bad.Code != 404 {
		t.Fatal("changed storage identity was accepted")
	}
	// Actual storage loss must be unavailable even with a retained local copy.
	_, err = store.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(info.ObjectKey)})
	if err != nil {
		t.Fatal("fixture object deletion failed")
	}
	missing := httptest.NewRecorder()
	store.ServeAsset(missing, httptest.NewRequest("GET", "/private", nil), info.ID)
	if missing.Code != 404 {
		t.Fatalf("missing S3 object fell back to local: %d", missing.Code)
	}
	if _, err = store.promote(ctx, legacy); err == nil {
		t.Fatal("lost object was silently reconstructed")
	}
	if err = store.Delete(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(directory, info.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("explicit deletion retained receipt")
	}
}

func TestAssetStorageImportKeepsSSRFAndEncodingBoundary(t *testing.T) {
	pngData := fixturePNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("import leaked credentials")
		}
		_, _ = w.Write(pngData)
	}))
	defer server.Close()
	importer, err := newLocalAssetStore(t.TempDir(), server.URL, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer importer.Close()
	info, err := importer.Import(context.Background(), "storage-safety", server.URL, "image")
	if err != nil {
		t.Fatal(err)
	}
	if info.Thumbnail == nil {
		t.Fatal("new import has no private preview")
	}
	if _, err = importer.Import(context.Background(), "ssrf", "http://169.254.169.254/metadata", "image"); err == nil {
		t.Fatal("source SSRF policy bypassed")
	}
	file, _, err := importer.OpenAsset(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	actual, _ := io.ReadAll(file)
	if !bytes.Equal(actual, pngData) {
		t.Fatal("original bytes changed")
	}
}
