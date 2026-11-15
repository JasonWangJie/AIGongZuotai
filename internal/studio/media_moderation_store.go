package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// Bound both verified descriptors and private temporary copies for their entire
// lifetime, including the downstream moderation request. Callers must Close.
var mediaModerationAssetSlots = make(chan struct{}, 4)

// OpenMediaModerationAsset reads an already imported private asset for review.
// It does not grant download permission, register a library entry or regenerate
// anything. The caller must bind the returned identity to its frozen candidate.
// An S3 receipt always reads the original private object, never a local backup.
func OpenMediaModerationAsset(ctx context.Context, storage AssetStorage, id string) (io.ReadSeekCloser, AssetInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, AssetInfo{}, err
	}
	select {
	case mediaModerationAssetSlots <- struct{}{}:
	default:
		return nil, AssetInfo{}, &MediaProviderError{Code: "ASSET_STORAGE_BUSY", Message: "private media review reads are busy; retain the original task", Retryable: true}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	reader := &mediaModerationAssetReader{ctx: ctx, cancel: cancel, release: func() { <-mediaModerationAssetSlots }}
	success := false
	defer func() {
		if !success {
			_ = reader.Close()
		}
	}()
	var info AssetInfo
	var err error
	switch store := storage.(type) {
	case *LocalAssetStore:
		if store == nil || store.root == nil {
			return nil, AssetInfo{}, mediaModerationAssetUnavailable()
		}
		info, err = readAssetManifest(store.root, id)
		if err == nil && info.StorageBackend == "s3" {
			return nil, AssetInfo{}, mediaModerationAssetUnavailable()
		}
		if err == nil {
			reader.file, err = store.root.Open(id + ".bin")
		}
	case *S3AssetStorage:
		if store == nil || store.local == nil || store.local.root == nil {
			return nil, AssetInfo{}, mediaModerationAssetUnavailable()
		}
		info, err = readAssetManifest(store.local.root, id)
		if err == nil {
			if info.StorageBackend == "s3" {
				err = copyMediaModerationObject(ctx, store, info, reader)
			} else {
				reader.file, err = store.local.root.Open(id + ".bin")
			}
		}
	default:
		return nil, AssetInfo{}, mediaModerationAssetUnavailable()
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, AssetInfo{}, ctx.Err()
		}
		// Helpers already return safe provider errors. Filesystem errors contain
		// private paths, so never pass them outside this storage boundary.
		if provider, ok := err.(*MediaProviderError); ok {
			return nil, AssetInfo{}, provider
		}
		return nil, AssetInfo{}, mediaModerationAssetUnavailable()
	}
	if err = verifyMediaModerationFile(ctx, reader.file, info); err != nil {
		return nil, AssetInfo{}, err
	}
	// AfterFunc also removes a returned S3 temporary copy if its caller is
	// canceled before reaching its deferred Close. Close itself is idempotent.
	context.AfterFunc(ctx, func() { _ = reader.Close() })
	if err = ctx.Err(); err != nil {
		return nil, AssetInfo{}, err
	}
	success = true
	return reader, info, nil
}

func mediaModerationAssetUnavailable() error {
	return &MediaProviderError{Code: "ASSET_UNAVAILABLE", Message: "private media review bytes are missing, invalid or inaccessible; retain the original task", ReconcileRequired: true}
}

func copyMediaModerationObject(ctx context.Context, store *S3AssetStorage, info AssetInfo, reader *mediaModerationAssetReader) error {
	if err := store.checkReceipt(info); err != nil {
		return err
	}
	select {
	case store.slots <- struct{}{}:
		defer func() { <-store.slots }()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return &MediaProviderError{Code: "ASSET_STORAGE_BUSY", Message: "private object reads are busy; retain the original task", Retryable: true}
	}
	out, err := store.client.GetObject(ctx, store.getInput(info))
	if err != nil {
		return safeS3Error(err)
	}
	defer out.Body.Close()
	if aws.ToInt64(out.ContentLength) != info.ByteSize || aws.ToString(out.ContentType) != info.ContentType || out.Metadata["sha256"] != info.SHA256 ||
		(info.ObjectETag != "" && aws.ToString(out.ETag) != info.ObjectETag) || (info.ObjectVersion != "" && aws.ToString(out.VersionId) != info.ObjectVersion) {
		return integrityError()
	}
	// Use an existing, GC-recognized asset temporary-file namespace. This is
	// only a private review spool; it never replaces a manifest or original.
	reader.root, reader.name = store.local.root, newID("asset_thumb")+".tmp"
	reader.file, err = reader.root.OpenFile(reader.name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		// A failed exclusive create does not authorize deleting an existing file.
		reader.name = ""
		return assetStorageError()
	}
	n, err := io.Copy(reader.file, &assetContextReader{ctx: ctx, r: io.LimitReader(out.Body, info.ByteSize+1)})
	if err != nil {
		return safeS3Error(err)
	}
	if n != info.ByteSize {
		return integrityError()
	}
	if _, err = reader.file.Seek(0, io.SeekStart); err != nil {
		return assetStorageError()
	}
	return nil
}

func verifyMediaModerationFile(ctx context.Context, file *os.File, info AssetInfo) error {
	if file == nil || !validAssetInfo(info, info.ID) {
		return mediaModerationAssetUnavailable()
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != info.ByteSize {
		return integrityError()
	}
	hash := sha256.New()
	n, err := io.Copy(hash, &assetContextReader{ctx: ctx, r: io.LimitReader(file, info.ByteSize+1)})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || n != info.ByteSize || hex.EncodeToString(hash.Sum(nil)) != info.SHA256 {
		return integrityError()
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return assetStorageError()
	}
	kind := "image"
	if info.ContentType == "video/mp4" {
		kind = "video"
	}
	checked := &mediaModerationReadSeeker{ctx: ctx, file: file}
	contentType, err := validateAssetEncoding(checked, kind, info.ByteSize)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || contentType != info.ContentType {
		return integrityError()
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return assetStorageError()
	}
	return nil
}

type mediaModerationReadSeeker struct {
	ctx  context.Context
	file *os.File
}

func (r *mediaModerationReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}

func (r *mediaModerationReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Seek(offset, whence)
}

type mediaModerationAssetReader struct {
	ctx     context.Context
	file    *os.File
	root    *os.Root
	name    string
	cancel  context.CancelFunc
	release func()
	once    sync.Once
	err     error
}

func (r *mediaModerationAssetReader) Read(p []byte) (int, error) {
	return (&mediaModerationReadSeeker{ctx: r.ctx, file: r.file}).Read(p)
}

func (r *mediaModerationAssetReader) Seek(offset int64, whence int) (int64, error) {
	return (&mediaModerationReadSeeker{ctx: r.ctx, file: r.file}).Seek(offset, whence)
}

func (r *mediaModerationAssetReader) Close() error {
	r.once.Do(func() {
		if r.file != nil {
			if err := r.file.Close(); err != nil {
				r.err = assetStorageError()
			}
		}
		if r.root != nil && r.name != "" {
			if err := r.root.Remove(r.name); err != nil && !os.IsNotExist(err) {
				r.err = assetStorageError()
			}
		}
		r.cancel()
		r.release()
	})
	return r.err
}
