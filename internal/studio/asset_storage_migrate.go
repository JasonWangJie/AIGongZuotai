package studio

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	if directory == "" {
		directory = ".runtime/assets"
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return AssetMigrationReport{}, errors.New("private asset directory unavailable")
	}
	_, ownerErr := root.Lstat(storageOwnerName)
	_ = root.Close()
	if !errors.Is(ownerErr, os.ErrNotExist) {
		return AssetMigrationReport{}, errors.New("owned storage requires database-aware migration; no objects changed")
	}
	return migrateAssets(ctx, config, directory, apply, limit, after, nil)
}

type AssetMigrationConfig struct {
	Storage                AssetStorageConfig
	Directory, DatabaseURL string
	Apply                  bool
	Limit                  int
	After                  string
}

// The production CLI owns both the private directory and database writer locks.
// Its schema/owner check never upgrades a database or creates owner markers.
func MigrateOwnedAssets(ctx context.Context, c AssetMigrationConfig) (AssetMigrationReport, error) {
	var report AssetMigrationReport
	if c.Directory == "" {
		c.Directory = ".runtime/assets"
	}
	lease, err := AcquireStorageLease(c.Directory)
	if err != nil {
		return report, err
	}
	defer lease.Close()
	root, err := os.OpenRoot(c.Directory)
	if err != nil {
		return report, errors.New("private asset directory unavailable")
	}
	defer root.Close()
	owner, ownerErr := readStorageOwner(root)
	if errors.Is(ownerErr, os.ErrNotExist) && c.DatabaseURL == "" {
		// Only explicit unowned local/demo fixtures retain the legacy behavior.
		return MigrateAssets(ctx, c.Storage, c.Directory, c.Apply, c.Limit, c.After)
	}
	if ownerErr != nil || owner.Role != "assets" || c.DatabaseURL == "" {
		return report, errors.New("owned storage requires its matching database and assets owner")
	}
	conn, err := pgx.Connect(ctx, c.DatabaseURL)
	if err != nil {
		return report, errors.New("storage migration database unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
	}()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(0x53545544494F)).Scan(&locked); err != nil || !locked {
		return report, errors.New("storage migration requires the workbench to be stopped; writer lock unavailable")
	}
	identity, err := storageOwnerID(ctx, conn)
	if err != nil || owner.ID != identity {
		return report, errors.New("storage migration owner does not match the database")
	}
	reachable, err := maintenanceReachableDetails(ctx, conn)
	if err != nil {
		return report, err
	}
	protected := make(map[string]bool, len(reachable.Deliveries))
	for _, receipt := range reachable.Receipts {
		if err = maintenanceCheckDeliveryReceipt(ctx, root, receipt); err != nil {
			return report, err
		}
	}
	for _, item := range reachable.Deliveries {
		protected[item.AssetID] = true
	}
	return migrateAssets(ctx, c.Storage, c.Directory, c.Apply, c.Limit, c.After, protected)
}

func migrateAssets(ctx context.Context, config AssetStorageConfig, directory string, apply bool, limit int, after string, deliveryAssets map[string]bool) (AssetMigrationReport, error) {
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
	// Validate the entire selected batch before the first remote write. Even a
	// terminal delivery keeps its immutable original storage locator/receipt.
	for _, id := range ids {
		info, readErr := readAssetManifest(root, id)
		if readErr != nil {
			return report, errors.New("asset manifest cannot be safely migrated")
		}
		if deliveryAssets[id] || info.Delivery != nil {
			return report, errors.New("delivery assets cannot be migrated without an append-only locator protocol; no objects changed")
		}
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
