package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/image/draw"
)

// AssetStorage is private infrastructure. Every HTTP caller must first verify
// the current catalog entry, project ownership and non-trashed state.
type AssetStorage interface {
	Stat(context.Context, string) (AssetInfo, error)
	ServeAsset(http.ResponseWriter, *http.Request, string)
	ServeThumbnail(http.ResponseWriter, *http.Request, string)
	Delete(context.Context, string) error
	Close() error
}

type AssetStorageConfig struct {
	Backend, Endpoint, Region, Bucket, Prefix  string
	AccessKeyID, SecretAccessKey, SessionToken string
	ForcePathStyle                             bool
}

func NewAssetStorage(config AssetStorageConfig, directory string) (AssetStorage, error) {
	if strings.TrimSpace(directory) == "" {
		directory = ".runtime/assets"
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, errors.New("private asset directory unavailable")
	}
	if config.Backend == "s3" {
		return newS3AssetStorage(config, directory)
	}
	if config.Backend != "" && config.Backend != "local" {
		return nil, errors.New("ASSET_STORAGE_BACKEND must be local or s3")
	}
	return OpenLocalAssetReader(directory)
}

func (store *LocalAssetStore) Stat(ctx context.Context, id string) (AssetInfo, error) {
	if err := ctx.Err(); err != nil {
		return AssetInfo{}, err
	}
	file, info, err := store.OpenAsset(id)
	if err == nil {
		_ = file.Close()
	}
	return info, err
}

func (store *LocalAssetStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !localAssetID.MatchString(id) {
		return os.ErrNotExist
	}
	info, err := readAssetManifest(store.root, id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info.StorageBackend == "s3" {
		return errors.New("asset belongs to a different storage backend")
	}
	return deleteLocalAssetFiles(store.root, id)
}

func deleteLocalAssetFiles(root *os.Root, id string) error {
	for _, suffix := range []string{".bin", ".thumb.png", ".json"} {
		if err := root.Remove(id + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("asset deletion incomplete")
		}
	}
	return nil
}

func readAssetManifest(root *os.Root, id string) (AssetInfo, error) {
	if !localAssetID.MatchString(id) {
		return AssetInfo{}, os.ErrNotExist
	}
	f, err := root.Open(id + ".json")
	if err != nil {
		return AssetInfo{}, os.ErrNotExist
	}
	defer f.Close()
	var info AssetInfo
	if err = json.NewDecoder(io.LimitReader(f, 8192)).Decode(&info); err != nil || !validAssetInfo(info, id) {
		return AssetInfo{}, os.ErrNotExist
	}
	if thumb := info.Thumbnail; thumb != nil && (!validAssetInfo(*thumb, id) || thumb.ContentType != "image/png" || thumb.ByteSize > 2<<20 || thumb.Thumbnail != nil || thumb.Delivery != nil) {
		return AssetInfo{}, os.ErrNotExist
	}
	return info, nil
}

func validAssetInfo(info AssetInfo, id string) bool {
	if info.StorageBackend != "" && info.StorageBackend != "local" && info.StorageBackend != "s3" {
		return false
	}
	if info.ID != id || info.ByteSize < 1 || info.ByteSize > 256<<20 || len(info.SHA256) != 64 {
		return false
	}
	if !validDeliveryMarker(info.Delivery, id) {
		return false
	}
	if _, err := hex.DecodeString(info.SHA256); err != nil {
		return false
	}
	return map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "video/mp4": true}[info.ContentType]
}

func writeAssetManifest(root *os.Root, info AssetInfo) error {
	if !validAssetInfo(info, info.ID) || !localAssetID.MatchString(info.ID) {
		return errors.New("invalid asset metadata")
	}
	if current, err := readAssetManifest(root, info.ID); err == nil && current.Delivery != nil {
		before, _ := json.Marshal(current)
		after, _ := json.Marshal(info)
		if !bytes.Equal(before, after) {
			return deliveryIdentityError()
		}
		return nil
	}
	tmp := newID("asset_manifest") + ".tmp"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return assetStorageError()
	}
	defer func() { _ = f.Close(); _ = root.Remove(tmp) }()
	if err = json.NewEncoder(f).Encode(info); err != nil {
		return assetStorageError()
	}
	if err = f.Sync(); err != nil {
		return assetStorageError()
	}
	if err = f.Close(); err != nil {
		return assetStorageError()
	}
	if err = root.Rename(tmp, info.ID+".json"); err != nil {
		return assetStorageError()
	}
	return nil
}

// Import keeps source URL validation, bounded admission, image decoding and
// credential-free downloads in the original importer. Storage promotion cannot
// resubmit a generation or silently fall back from a missing remote object.
func (store *LocalAssetStore) Import(ctx context.Context, identity, sourceURL, kind string) (AssetInfo, error) {
	sum := sha256.Sum256([]byte(identity))
	id := "asset_" + hex.EncodeToString(sum[:])
	if info, err := readAssetManifest(store.root, id); err == nil && info.StorageBackend == "s3" {
		if store.destination == nil {
			return AssetInfo{}, &MediaProviderError{Code: "ASSET_STORAGE_IDENTITY_CHANGED", Message: "asset storage configuration changed; retain the original task", ReconcileRequired: true}
		}
		return store.destination.Stat(ctx, id)
	}
	info, err := store.importLocal(ctx, identity, sourceURL, kind)
	if err != nil {
		return AssetInfo{}, err
	}
	store.publishMu.Lock()
	info, err = ensureAssetThumbnail(ctx, store.root, info)
	store.publishMu.Unlock()
	if err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if store.destination != nil {
		return store.destination.promote(ctx, info)
	}
	return info, nil
}

func ensureAssetThumbnail(ctx context.Context, root *os.Root, info AssetInfo) (AssetInfo, error) {
	if info.ContentType == "video/mp4" || info.Thumbnail != nil {
		return info, nil
	}
	file, err := root.Open(info.ID + ".bin")
	if err != nil {
		return AssetInfo{}, err
	}
	defer file.Close()
	if err = ctx.Err(); err != nil {
		return AssetInfo{}, err
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 32_000_000 {
		return AssetInfo{}, errors.New("invalid thumbnail source dimensions")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return AssetInfo{}, err
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return AssetInfo{}, err
	}
	b := decoded.Bounds()
	width, height := b.Dx(), b.Dy()
	if width > 512 || height > 512 {
		if width >= height {
			height = max(1, height*512/width)
			width = 512
		} else {
			width = max(1, width*512/height)
			height = 512
		}
	}
	thumb := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(thumb, thumb.Bounds(), decoded, b, draw.Src, nil)
	tmp := newID("asset_thumb") + ".tmp"
	out, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return AssetInfo{}, err
	}
	defer func() { _ = out.Close(); _ = root.Remove(tmp) }()
	hash := sha256.New()
	writer := &privateBlobWriter{ctx: ctx, writer: io.MultiWriter(out, hash), remaining: 2 << 20}
	if err = png.Encode(writer, thumb); err != nil {
		return AssetInfo{}, err
	}
	stat, err := out.Stat()
	if err != nil {
		return AssetInfo{}, err
	}
	if err = out.Sync(); err != nil {
		return AssetInfo{}, err
	}
	if err = out.Close(); err != nil {
		return AssetInfo{}, err
	}
	if err = root.Rename(tmp, info.ID+".thumb.png"); err != nil {
		return AssetInfo{}, err
	}
	info.Thumbnail = &AssetInfo{ID: info.ID, ContentType: "image/png", ByteSize: stat.Size(), SHA256: hex.EncodeToString(hash.Sum(nil)), CreatedAt: info.CreatedAt}
	if err = writeAssetManifest(root, info); err != nil {
		return AssetInfo{}, err
	}
	return info, nil
}

func (store *LocalAssetStore) ServeThumbnail(w http.ResponseWriter, r *http.Request, id string) {
	info, err := readAssetManifest(store.root, id)
	if err != nil || info.StorageBackend == "s3" || info.Thumbnail == nil {
		http.NotFound(w, r)
		return
	}
	f, err := store.root.Open(id + ".thumb.png")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != info.Thumbnail.ByteSize {
		http.NotFound(w, r)
		return
	}
	assetResponseHeaders(w, *info.Thumbnail)
	http.ServeContent(w, r, id+".png", info.CreatedAt, f)
}

func assetResponseHeaders(w http.ResponseWriter, info AssetInfo) {
	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "video/mp4": ".mp4"}[info.ContentType]
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s%s"`, info.ID, ext))
}
