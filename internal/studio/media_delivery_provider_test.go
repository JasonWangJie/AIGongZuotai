package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func deliveryTestProvider(t *testing.T, gateway *httptest.Server, directory string, storage AssetStorageConfig, workers int) *AsyncMediaProvider {
	t.Helper()
	p, err := NewAsyncMediaProvider(AsyncMediaConfig{BaseURL: gateway.URL, APIKey: fixtureMediaKey, CredentialID: "fixture-credential",
		ImageModel: "fixture-image-model", AssetDir: directory, AssetStorage: storage, DeliveryConcurrency: workers, allowLocalAssets: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func deliveryTestItem(server *httptest.Server, path string) MediaDeliveryItem {
	return MediaDeliveryItem{Kind: "image", SourceURL: server.URL + path}
}

func TestMediaDeliveryQueryFreezesAbsoluteItemsWithoutDownload(t *testing.T) {
	var downloads atomic.Int32
	var data atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, mediaQueryPrefix) {
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "asyncimg_fixture", "media_type": "image", "stage": "completed", "status": "succeeded", "data": data.Load()})
			return
		}
		downloads.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	p := deliveryTestProvider(t, server, t.TempDir(), AssetStorageConfig{}, 2)
	data.Store([]map[string]any{{"url": "/image?signature=private-source", "content_type": "image/png", "byte_size": 123}})
	got, err := p.QueryStatus(context.Background(), mediaFixtureHandle(p, "image", 2))
	if err != nil || got.Result.Status != "PARTIAL" || len(got.Result.Artifacts) != 0 || len(got.Items) != 1 || got.Items[0].Slot != 0 || got.Items[0].SourceURL != server.URL+"/image?signature=private-source" {
		t.Fatalf("status-only result did not preserve private ordered items: %v %+v", err, got.Result)
	}
	if downloads.Load() != 0 {
		t.Fatal("status query downloaded an artifact")
	}
	public, _ := json.Marshal(got.Result)
	if bytes.Contains(public, []byte("signature")) || bytes.Contains(public, []byte(server.URL)) {
		t.Fatal("public result carries delivery source")
	}
	for _, source := range []string{"http://169.254.169.254/latest/meta-data", "https://unapproved.invalid/result", server.URL + "/" + strings.Repeat("x", 8193), "data:image/png;base64,AAAA"} {
		data.Store([]map[string]any{{"url": source}})
		_, err := p.QueryStatus(context.Background(), mediaFixtureHandle(p, "image", 1))
		if requireMediaError(t, err).Code != "ASSET_SOURCE_NOT_ALLOWED" || downloads.Load() != 0 {
			t.Fatal("invalid source was downloaded or insufficiently classified")
		}
	}
}

func TestMediaDeliveryReceiptReplayAndBatchIdentity(t *testing.T) {
	var downloads atomic.Int32
	image := fixturePNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("private gateway credential reached the result source")
		}
		_, _ = w.Write(image)
	}))
	defer server.Close()
	directory := t.TempDir()
	p := deliveryTestProvider(t, server, directory, AssetStorageConfig{}, 2)
	id, item := newID("delivery"), deliveryTestItem(server, "/original?signed=private")
	if _, found, err := p.LoadDeliveryReceipt(context.Background(), id, item); err != nil || found {
		t.Fatal("unpublished delivery already had a receipt", err)
	}
	first, err := p.ImportDeliveryItem(context.Background(), id, item)
	if err != nil || first.StorageInfo == nil || first.StorageInfo.Delivery == nil || first.StorageInfo.Thumbnail == nil || first.ID != mediaDeliveryAssetID(id, 0) {
		t.Fatal("complete private receipt missing", err)
	}
	metadata, err := os.ReadFile(filepath.Join(directory, first.ID+".json"))
	if err != nil || bytes.Contains(metadata, []byte("signed")) || bytes.Contains(metadata, []byte(server.URL)) {
		t.Fatal("source URL was written to disk receipt", err)
	}
	_ = p.Close()
	restarted := deliveryTestProvider(t, server, directory, AssetStorageConfig{}, 2)
	loaded, found, err := restarted.LoadDeliveryReceipt(context.Background(), id, item)
	if err != nil || !found || !reflect.DeepEqual(first.StorageInfo, loaded.StorageInfo) {
		t.Fatal("restart did not reuse full receipt", err)
	}
	replayed, err := restarted.ImportDeliveryItem(context.Background(), id, item)
	if err != nil || replayed.ID != first.ID || downloads.Load() != 1 {
		t.Fatal("receipt recovery redownloaded or changed artifact", err)
	}
	changed := item
	changed.SourceURL = server.URL + "/different-source"
	if _, err = restarted.ImportDeliveryItem(context.Background(), id, changed); requireMediaError(t, err).Code != "DELIVERY_IDENTITY_CHANGED" || downloads.Load() != 1 {
		t.Fatal("same slot accepted different descriptor")
	}
	next, err := restarted.ImportDeliveryItem(context.Background(), newID("delivery"), changed)
	if err != nil || next.ID == first.ID || downloads.Load() != 2 {
		t.Fatal("different deliveries reused old handle/index identity", err)
	}
	changedInfo := *first.StorageInfo
	changedInfo.Delivery = nil
	if err = writeAssetManifest(restarted.assets.root, changedInfo); requireMediaError(t, err).Code != "DELIVERY_IDENTITY_CHANGED" {
		t.Fatal("completed marker was mutable")
	}
}

func TestMediaDeliveryCompletedCorruptionNeverRedownloads(t *testing.T) {
	for _, damage := range []string{"original-changed", "original-missing", "thumbnail-changed", "thumbnail-missing", "manifest-invalid", "marker-changed"} {
		t.Run(damage, func(t *testing.T) {
			var downloads atomic.Int32
			blob := fixturePNG(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { downloads.Add(1); _, _ = w.Write(blob) }))
			defer server.Close()
			dir := t.TempDir()
			p := deliveryTestProvider(t, server, dir, AssetStorageConfig{}, 1)
			id, item := newID("delivery"), deliveryTestItem(server, "/image")
			artifact, err := p.ImportDeliveryItem(context.Background(), id, item)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, artifact.ID+".bin")
			if strings.HasPrefix(damage, "thumbnail") {
				name = filepath.Join(dir, artifact.ID+".thumb.png")
			}
			switch {
			case strings.HasSuffix(damage, "missing"):
				err = os.Remove(name)
			case damage == "manifest-invalid":
				err = os.WriteFile(filepath.Join(dir, artifact.ID+".json"), []byte("{"), 0600)
			case damage == "marker-changed":
				info := *artifact.StorageInfo
				marker := *info.Delivery
				marker.ItemSHA256 = strings.Repeat("0", 64)
				info.Delivery = &marker
				body, _ := json.Marshal(info)
				err = os.WriteFile(filepath.Join(dir, artifact.ID+".json"), body, 0600)
			default:
				var body []byte
				body, err = os.ReadFile(name)
				if err == nil {
					body[len(body)-1] ^= 1
					err = os.WriteFile(name, body, 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := p.LoadDeliveryReceipt(context.Background(), id, item); err == nil || found || !requireMediaError(t, err).ReconcileRequired {
				t.Fatal("damaged receipt accepted", err)
			}
			if _, err := p.ImportDeliveryItem(context.Background(), id, item); err == nil || downloads.Load() != 1 {
				t.Fatal("completed receipt repaired by redownload", err)
			}
		})
	}
}

func TestMediaDeliveryIncompleteImportResumesOnlyVerifiedOriginal(t *testing.T) {
	var downloads atomic.Int32
	blob := fixturePNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { downloads.Add(1); _, _ = w.Write(blob) }))
	defer server.Close()
	dir := t.TempDir()
	p := deliveryTestProvider(t, server, dir, AssetStorageConfig{}, 1)
	item := deliveryTestItem(server, "/image")
	for _, corrupt := range []bool{false, true} {
		id := newID("delivery")
		info, err := p.assets.importLocal(context.Background(), mediaDeliveryIdentity(id, item.Slot), item.SourceURL, item.Kind)
		if err != nil || info.Thumbnail != nil || info.Delivery != nil {
			t.Fatal("incomplete import fixture failed", err)
		}
		if corrupt {
			bad := append([]byte(nil), blob...)
			bad[len(bad)-1] ^= 1
			if err = os.WriteFile(filepath.Join(dir, info.ID+".bin"), bad, 0600); err != nil {
				t.Fatal(err)
			}
		}
		before := downloads.Load()
		artifact, err := p.ImportDeliveryItem(context.Background(), id, item)
		if corrupt && err == nil || !corrupt && (err != nil || artifact.StorageInfo.Thumbnail == nil || artifact.StorageInfo.Delivery == nil) || downloads.Load() != before {
			t.Fatal("partial original not verified or redownloaded", err)
		}
	}
	orphan := newID("delivery")
	if err := os.WriteFile(filepath.Join(dir, mediaDeliveryAssetID(orphan, 0)+".bin"), blob, 0600); err != nil {
		t.Fatal(err)
	}
	before := downloads.Load()
	if _, err := p.ImportDeliveryItem(context.Background(), orphan, item); requireMediaError(t, err).Code != "DELIVERY_RECEIPT_UNAVAILABLE" || downloads.Load() != before {
		t.Fatal("unidentified orphan overwritten")
	}
}

func TestMediaDeliveryConcurrentItemHasOneImmutableReceipt(t *testing.T) {
	var downloads atomic.Int32
	entered, release := make(chan struct{}, 2), make(chan struct{})
	blob := fixturePNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write(blob)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	var once sync.Once
	defer once.Do(func() { close(release) })
	p := deliveryTestProvider(t, server, t.TempDir(), AssetStorageConfig{}, 2)
	id, item := newID("delivery"), deliveryTestItem(server, "/image")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		artifact Artifact
		err      error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() { artifact, err := p.ImportDeliveryItem(ctx, id, item); done <- result{artifact, err} }()
	}
	mediaTransportWait(t, entered)
	once.Do(func() { close(release) })
	first, second := mediaTransportWait(t, done), mediaTransportWait(t, done)
	if first.err != nil || second.err != nil || downloads.Load() != 1 || !reflect.DeepEqual(first.artifact.StorageInfo, second.artifact.StorageInfo) {
		t.Fatal("same slot published twice", first.err, second.err)
	}
}

func TestMediaDeliveryS3ReceiptVerifiesOriginalAndThumbnailBytes(t *testing.T) {
	for _, damage := range []string{"original-changed", "thumbnail-changed", "remote-missing", "storage-changed"} {
		t.Run(damage, func(t *testing.T) {
			fixture := &s3Fixture{objects: map[string]s3StoredFixture{}}
			remote := httptest.NewServer(fixture)
			defer remote.Close()
			config := AssetStorageConfig{Backend: "s3", Endpoint: remote.URL, Region: "test", Bucket: "private-bucket", Prefix: "delivery", AccessKeyID: "fixture", SecretAccessKey: "not-real", ForcePathStyle: true}
			var downloads atomic.Int32
			blob := fixturePNG(t)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { downloads.Add(1); _, _ = w.Write(blob) }))
			defer gateway.Close()
			p := deliveryTestProvider(t, gateway, t.TempDir(), config, 1)
			id, item := newID("delivery"), deliveryTestItem(gateway, "/image")
			artifact, err := p.ImportDeliveryItem(context.Background(), id, item)
			if err != nil || artifact.StorageInfo.StorageBackend != "s3" || artifact.StorageInfo.Thumbnail == nil {
				t.Fatal("S3 receipt missing", err)
			}
			if _, found, err := p.LoadDeliveryReceipt(context.Background(), id, item); err != nil || !found {
				t.Fatal("S3 receipt failed", err)
			}
			fixture.mu.Lock()
			puts := fixture.puts
			key := "/" + config.Bucket + "/" + artifact.StorageInfo.ObjectKey
			if damage == "thumbnail-changed" {
				key = "/" + config.Bucket + "/" + artifact.StorageInfo.Thumbnail.ObjectKey
			}
			switch damage {
			case "remote-missing":
				delete(fixture.objects, key)
			case "storage-changed":
				p.assets.destination.identity = "changed"
			default:
				object := fixture.objects[key]
				object.data = append([]byte(nil), object.data...)
				object.data[len(object.data)-1] ^= 1 // HEAD still matches.
				fixture.objects[key] = object
			}
			fixture.mu.Unlock()
			if _, err := p.ImportDeliveryItem(context.Background(), id, item); err == nil || !requireMediaError(t, err).ReconcileRequired {
				t.Fatal("S3 damage fell back to local bytes", err)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if downloads.Load() != 1 || fixture.puts != puts {
				t.Fatal("completed remote receipt was overwritten or redownloaded")
			}
		})
	}
}

func TestMediaDeliveryS3LostPutAcknowledgementPublishesOnlyAfterVerification(t *testing.T) {
	fixture := &s3Fixture{objects: map[string]s3StoredFixture{}, failAfterWrite: true}
	readback := make(chan string, 2)
	originalRelease, thumbnailRelease := make(chan struct{}), make(chan struct{})
	var originalOnce, thumbnailOnce sync.Once
	var readMu sync.Mutex
	seen := map[string]bool{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			readMu.Lock()
			first := !seen[r.URL.Path]
			seen[r.URL.Path] = true
			readMu.Unlock()
			if first {
				permit := originalRelease
				kind := "original"
				if strings.HasSuffix(r.URL.Path, ".thumb.png") {
					permit, kind = thumbnailRelease, "thumbnail"
				}
				readback <- kind
				select {
				case <-permit:
				case <-r.Context().Done():
					return
				}
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer remote.Close()
	defer originalOnce.Do(func() { close(originalRelease) })
	defer thumbnailOnce.Do(func() { close(thumbnailRelease) })
	config := AssetStorageConfig{Backend: "s3", Endpoint: remote.URL, Region: "test", Bucket: "private-bucket",
		Prefix: "lost-delivery-put", AccessKeyID: "fixture", SecretAccessKey: "not-real", ForcePathStyle: true}
	blob := fixturePNG(t)
	var downloads atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(blob)
	}))
	defer gateway.Close()
	directory := t.TempDir()
	p := deliveryTestProvider(t, gateway, directory, config, 1)
	id, item := newID("delivery"), deliveryTestItem(gateway, "/original?private=signed")
	assetID := mediaDeliveryAssetID(id, item.Slot)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type result struct {
		artifact Artifact
		err      error
	}
	done := make(chan result, 1)
	go func() {
		artifact, err := p.ImportDeliveryItem(ctx, id, item)
		done <- result{artifact, err}
	}()
	for _, want := range []string{"original", "thumbnail"} {
		if got := mediaTransportWait(t, readback); got != want {
			t.Fatalf("unexpected lost-ACK recovery stage: got=%s want=%s", got, want)
		}
		// The fixture has stored the object and returned HTTP 500 from PUT,
		// but the first complete GET has not yet delivered verification bytes.
		info, err := readAssetManifest(p.assets.root, assetID)
		if err != nil || info.Delivery != nil || info.StorageBackend == "s3" {
			t.Fatalf("completion marker/storage switch preceded %s readback: %v", want, err)
		}
		if _, found, err := p.LoadDeliveryReceipt(ctx, id, item); err != nil || found {
			t.Fatalf("unverified %s was exposed as a complete delivery: %v", want, err)
		}
		if want == "original" {
			originalOnce.Do(func() { close(originalRelease) })
		} else {
			thumbnailOnce.Do(func() { close(thumbnailRelease) })
		}
	}
	got := mediaTransportWait(t, done)
	if got.err != nil || got.artifact.StorageInfo == nil {
		t.Fatal("lost PUT acknowledgement did not recover the delivery", got.err)
	}
	info := got.artifact.StorageInfo
	if info.Delivery == nil || info.StorageBackend != "s3" || info.Thumbnail == nil || info.Thumbnail.StorageBackend != "s3" {
		t.Fatal("verified originals and thumbnail were not captured by the completed receipt")
	}
	fixture.mu.Lock()
	puts := fixture.puts
	original := fixture.objects["/"+config.Bucket+"/"+info.ObjectKey]
	thumbnail := fixture.objects["/"+config.Bucket+"/"+info.Thumbnail.ObjectKey]
	fixture.mu.Unlock()
	if puts < 2 {
		t.Fatal("fixture never exercised both uncertain object writes")
	}
	for _, pair := range []struct {
		stored s3StoredFixture
		info   AssetInfo
		suffix string
	}{{original, *info, ".bin"}, {thumbnail, *info.Thumbnail, ".thumb.png"}} {
		local, err := os.ReadFile(filepath.Join(directory, assetID+pair.suffix))
		if err != nil || !bytes.Equal(local, pair.stored.data) || pair.stored.sha != pair.info.SHA256 ||
			pair.stored.mime != pair.info.ContentType || int64(len(local)) != pair.info.ByteSize || pair.stored.etag != pair.info.ObjectETag {
			t.Fatal("local bytes, remote object and full receipt disagree", err)
		}
	}
	loaded, found, err := p.LoadDeliveryReceipt(ctx, id, item)
	if err != nil || !found || !reflect.DeepEqual(info, loaded.StorageInfo) {
		t.Fatal("complete receipt could not be verified after uncertain PUT", err)
	}
	reentered, err := p.ImportDeliveryItem(ctx, id, item)
	if err != nil || !reflect.DeepEqual(info, reentered.StorageInfo) {
		t.Fatal("reentry changed the immutable receipt", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.puts != puts || downloads.Load() != 1 {
		t.Fatal("completed delivery reentry repeated PUT or source download")
	}
}

func TestMediaDeliveryLimitsAndIndependentDownloadPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not-an-image") }))
	defer server.Close()
	for _, workers := range []int{0, 1, 16} {
		p := deliveryTestProvider(t, server, t.TempDir(), AssetStorageConfig{}, workers)
		want := workers
		if want == 0 {
			want = 2
		}
		transport := p.assets.client.Transport.(*http.Transport)
		if cap(p.assets.slots) != want || transport.MaxConnsPerHost != want || transport.MaxIdleConnsPerHost != want || transport == p.queryClient.Transport || transport == p.client.Transport {
			t.Fatal("download budget is not independent or configured")
		}
	}
	for _, workers := range []int{-1, 17} {
		if p, err := NewAsyncMediaProvider(AsyncMediaConfig{DeliveryConcurrency: workers}); err == nil {
			_ = p.Close()
			t.Fatal("invalid delivery budget accepted")
		}
	}
}

func TestMediaDeliverySlowDownloadDoesNotOccupyStatusQuery(t *testing.T) {
	entered, exited := make(chan struct{}, 2), make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	var downloads atomic.Int32
	blob := fixturePNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, mediaQueryPrefix) {
			mediaTransportResponse(w, r)
			return
		}
		downloads.Add(1)
		entered <- struct{}{}
		defer func() { exited <- struct{}{} }()
		select {
		case <-release:
			_, _ = w.Write(blob)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer once.Do(func() { close(release) })
	p := deliveryTestProvider(t, server, t.TempDir(), AssetStorageConfig{}, 1)
	firstID, item := newID("delivery"), deliveryTestItem(server, "/image")
	secondID := newID("delivery")
	stripe := func(id string) uint64 {
		value, _ := strconv.ParseUint(mediaDeliveryAssetID(id, 0)[6:8], 16, 8)
		return value % 64
	}
	for stripe(firstID) == stripe(secondID) {
		secondID = newID("delivery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := p.ImportDeliveryItem(ctx, firstID, item); first <- err }()
	mediaTransportWait(t, entered)
	queryCtx, queryCancel := context.WithTimeout(context.Background(), time.Second)
	defer queryCancel()
	if result, err := p.QueryStatus(queryCtx, mediaFixtureHandle(p, "image", 1)); err != nil || result.Result.Status != "RUNNING" {
		t.Fatal("in-progress download occupied the query connection or worker interface", err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer waitCancel()
	if _, err := p.ImportDeliveryItem(waitCtx, secondID, item); !requireMediaError(t, err).Retryable || downloads.Load() != 1 {
		t.Fatal("second import bypassed the real download slot")
	}
	cancel()
	if err := mediaTransportWait(t, first); !requireMediaError(t, err).Retryable {
		t.Fatal("cancelled download recovery changed")
	}
	mediaTransportWait(t, exited)
	once.Do(func() { close(release) })
	if _, err := p.ImportDeliveryItem(context.Background(), secondID, item); err != nil || downloads.Load() != 2 {
		t.Fatal("cancelled import leaked its download slot", err)
	}
}
