package studio

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

type AssetMigrationItem struct {
	ID       string `json:"id"`
	ByteSize int64  `json:"byte_size"`
	Action   string `json:"action"`
}
type AssetMigrationReport struct {
	Applied             bool                 `json:"applied"`
	Items               []AssetMigrationItem `json:"items"`
	HasMore             bool                 `json:"has_more"`
	NextAfter           string               `json:"next_after,omitempty"`
	LocalCopiesRetained bool                 `json:"local_copies_retained"`
}

// MigrateAssets is deliberately offline. Its caller must own the asset
// directory's StorageLease for the entire plan/apply operation. The library
// catalog references stable IDs, so switching an object's manifest needs no
// rewrite of public URLs or regeneration. No local/remote object is deleted.
func MigrateAssets(ctx context.Context, config AssetStorageConfig, directory string, apply bool, limit int, after string) (AssetMigrationReport, error) {
	report := AssetMigrationReport{Applied: apply, Items: []AssetMigrationItem{}, LocalCopiesRetained: true}
	if limit < 1 || limit > 1000 || (after != "" && !localAssetID.MatchString(after)) {
		return report, errors.New("migration limit must be 1..1000 and after must be an asset ID")
	}
	if directory == "" {
		directory = ".runtime/assets"
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return report, errors.New("private asset directory unavailable")
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return report, errors.New("private asset directory unavailable")
	}
	defer dir.Close()
	// Scan bounded batches; retain only the smallest limit+1 identifiers.
	ids := make([]string, 0, limit+1)
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return report, err
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			if !localAssetID.MatchString(id) || id <= after {
				continue
			}
			index := sort.SearchStrings(ids, id)
			ids = append(ids, "")
			copy(ids[index+1:], ids[index:])
			ids[index] = id
			if len(ids) > limit+1 {
				ids = ids[:limit+1]
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return report, errors.New("could not enumerate private asset metadata")
		}
	}
	report.HasMore = len(ids) > limit
	if report.HasMore {
		ids = ids[:limit]
	}
	var destination *S3AssetStorage
	if apply {
		if config.Backend != "s3" {
			return report, errors.New("migration apply requires ASSET_STORAGE_BACKEND=s3")
		}
		destination, err = newS3AssetStorage(config, directory)
		if err != nil {
			return report, err
		}
		defer destination.Close()
	}
	for _, id := range ids {
		info, readErr := readAssetManifest(root, id)
		if readErr != nil {
			return report, errors.New("asset manifest cannot be safely migrated")
		}
		action := "copy_verify"
		if info.StorageBackend == "s3" {
			action = "verify_existing"
		}
		item := AssetMigrationItem{ID: id, ByteSize: info.ByteSize, Action: action}
		if apply {
			if info.StorageBackend == "s3" {
				if err = destination.checkReceipt(info); err == nil {
					err = destination.verifyObject(ctx, info)
				}
				if err == nil && info.Thumbnail != nil {
					err = destination.verifyObject(ctx, *info.Thumbnail)
				}
			} else {
				_, err = destination.promote(ctx, info)
			}
			if err != nil {
				return report, err
			}
		}
		report.Items = append(report.Items, item)
		report.NextAfter = id
	}
	return report, nil
}
