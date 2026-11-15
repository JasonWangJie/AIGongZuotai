package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// These generated test patterns are explicit local fixtures, never AI output.
//
//go:embed testdata/moderation/*.png testdata/moderation/*.mp4
var mediaModerationFixtures embed.FS

type moderatedDemoMedia struct {
	demo   demoMedia
	assets AssetStorage
}

func newModeratedDemoMedia(duration time.Duration, assets AssetStorage) (MediaProvider, error) {
	switch assets.(type) {
	case *LocalAssetStore, *S3AssetStorage:
		return &moderatedDemoMedia{demo: demoMedia{duration: duration}, assets: assets}, nil
	default:
		return nil, errors.New("media fixture provider requires private storage")
	}
}

func (d *moderatedDemoMedia) Submit(ctx context.Context, in TaskInput) (MediaHandle, error) {
	handle, err := d.demo.Submit(ctx, in)
	if err != nil {
		return handle, err
	}
	// Markers choose a known fixture in demo mode. The moderation decision is
	// made from the full imported bytes, independently of this prompt.
	variant := "allow"
	if strings.Contains(in.Inputs.Prompt, "[MEDIA_FIXTURE_BLOCK]") {
		variant = "block"
	} else if strings.Contains(in.Inputs.Prompt, "[MEDIA_FIXTURE_REVIEW]") {
		variant = "review"
	}
	handle.ID += "_" + variant
	return handle, nil
}

func (d *moderatedDemoMedia) Cancel(ctx context.Context, handle MediaHandle) error {
	return d.demo.Cancel(ctx, handle)
}

func (d *moderatedDemoMedia) Query(ctx context.Context, handle MediaHandle) (MediaResult, error) {
	result, err := d.demo.Query(ctx, handle)
	if err != nil || result.Status != "SUCCEEDED" {
		return result, err
	}
	variant := "allow"
	if strings.HasSuffix(handle.ID, "_block") {
		variant = "block"
	} else if strings.HasSuffix(handle.ID, "_review") {
		variant = "review"
	}
	ext := ".png"
	if handle.Kind == "video" {
		ext = ".mp4"
	}
	data, err := mediaModerationFixtures.ReadFile("testdata/moderation/" + variant + ext)
	if err != nil {
		return MediaResult{}, assetStorageError()
	}
	result.Artifacts = nil
	for i := 0; i < handle.Count; i++ {
		info, err := importMediaModerationFixture(ctx, d.assets, fmt.Sprintf("media-fixture-v1:%s:%d", handle.ID, i), handle.Kind, data)
		if err != nil {
			return MediaResult{}, err
		}
		result.Artifacts = append(result.Artifacts, Artifact{ID: info.ID, URL: "/api/v1/assets/" + info.ID, Kind: handle.Kind, Title: "本地审核样本 · 非AI生成", StorageInfo: &info})
	}
	return result, nil
}

func importMediaModerationFixture(ctx context.Context, storage AssetStorage, identity, kind string, data []byte) (AssetInfo, error) {
	var local *LocalAssetStore
	var remote *S3AssetStorage
	switch s := storage.(type) {
	case *LocalAssetStore:
		local = s
	case *S3AssetStorage:
		local, remote = s.local, s
	default:
		return AssetInfo{}, assetStorageError()
	}
	if err := ctx.Err(); err != nil {
		return AssetInfo{}, err
	}
	sum := sha256.Sum256([]byte(identity))
	id := "asset_" + hex.EncodeToString(sum[:])
	digest := sha256.Sum256(data)
	expected := hex.EncodeToString(digest[:])
	if existing, err := storage.Stat(ctx, id); err == nil {
		if existing.SHA256 != expected || existing.ByteSize != int64(len(data)) {
			return AssetInfo{}, assetStorageError()
		}
		return existing, nil
	}
	contentType, err := validateAssetEncoding(bytes.NewReader(data), kind, int64(len(data)))
	if err != nil {
		return AssetInfo{}, err
	}
	local.publishMu.Lock()
	defer local.publishMu.Unlock()
	// Recheck after acquiring the publisher lock; do not overwrite a candidate.
	if existing, err := readAssetManifest(local.root, id); err == nil {
		if existing.SHA256 != expected || existing.ByteSize != int64(len(data)) {
			return AssetInfo{}, assetStorageError()
		}
		if remote != nil {
			return remote.promote(ctx, existing)
		}
		return existing, nil
	}
	tmp := newID("part") + ".tmp"
	f, err := local.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return AssetInfo{}, assetStorageError()
	}
	defer func() { _ = f.Close(); _ = local.root.Remove(tmp) }()
	if n, err := io.Copy(f, bytes.NewReader(data)); err != nil || n != int64(len(data)) {
		return AssetInfo{}, assetStorageError()
	}
	if err = f.Sync(); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if err = f.Close(); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if err = local.root.Rename(tmp, id+".bin"); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	info := AssetInfo{ID: id, ContentType: contentType, ByteSize: int64(len(data)), SHA256: expected, CreatedAt: time.Now().UTC()}
	if err = writeAssetManifest(local.root, info); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	info, err = ensureAssetThumbnail(ctx, local.root, info)
	if err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if remote != nil {
		return remote.promote(ctx, info)
	}
	return info, nil
}
