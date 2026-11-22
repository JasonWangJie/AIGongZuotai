package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Maintenance never purges an asset or reference record. Only old files not
// reachable from any record (including trash and terminal tasks) are eligible.
type StorageMaintenanceConfig struct {
	DatabaseURL, ReferenceDir, AssetDir, WebDir string
	MinAge                                      time.Duration
	Apply                                       bool
	PlanDigest                                  string
}

type MaintenanceFile struct {
	Store      string    `json:"store"`
	Name       string    `json:"name"`
	ByteSize   int64     `json:"byte_size"`
	SHA256     string    `json:"sha256"`
	ModifiedAt time.Time `json:"modified_at"`
}

type StorageMaintenanceReport struct {
	SchemaVersion  int               `json:"schema_version"`
	ReferenceDir   string            `json:"reference_dir"`
	AssetDir       string            `json:"asset_dir"`
	MinAgeHours    float64           `json:"min_age_hours"`
	ScannedFiles   int               `json:"scanned_files"`
	ProtectedFiles int               `json:"protected_files"`
	RecentFiles    int               `json:"recent_files"`
	IgnoredFiles   int               `json:"ignored_files"`
	Candidates     []MaintenanceFile `json:"candidates"`
	CandidateBytes int64             `json:"candidate_bytes"`
	PlanDigest     string            `json:"plan_digest"`
	Applied        bool              `json:"applied"`
	RemovedFiles   int               `json:"removed_files"`
	RemovedBytes   int64             `json:"removed_bytes"`
}

var referenceGCFile = regexp.MustCompile(`^(blob_[a-f0-9]{64}\.bin|reference_part_[a-f0-9]{32}\.tmp)$`)
var assetGCFile = regexp.MustCompile(`^(asset_[a-f0-9]{64}\.(bin|json|thumb\.png)|(part|meta|asset_thumb|asset_manifest)_[a-f0-9]{32}\.tmp)$`)

const maintenanceEntryLimit = 100000
const maintenanceSchemaVersion = 38

// MaintainPrivateStorage is an offline, explicitly configured admin operation.
// It acquires both OS directory locks and the application's PostgreSQL writer
// lock. It never upgrades a database and never deletes remote S3 objects.
func MaintainPrivateStorage(ctx context.Context, config StorageMaintenanceConfig) (StorageMaintenanceReport, error) {
	var report StorageMaintenanceReport
	if config.DatabaseURL == "" {
		return report, errors.New("maintenance requires the owning PostgreSQL database; memory mode cannot prove file reachability")
	}
	if config.MinAge == 0 {
		config.MinAge = 24 * time.Hour
	}
	if config.MinAge < 24*time.Hour || config.MinAge > 3650*24*time.Hour {
		return report, errors.New("maintenance minimum age must be between 24 hours and 3650 days")
	}
	if config.Apply && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(config.PlanDigest) {
		return report, errors.New("apply requires the exact digest from a reviewed dry-run plan")
	}
	if config.ReferenceDir == "" {
		config.ReferenceDir = ".runtime/references"
	}
	if config.AssetDir == "" {
		config.AssetDir = ".runtime/assets"
	}
	if config.WebDir == "" {
		config.WebDir = "apps/workbench/dist"
	}
	for _, pair := range [][2]string{{config.ReferenceDir, config.AssetDir}, {config.WebDir, config.ReferenceDir}, {config.WebDir, config.AssetDir}, {"apps/workbench/public", config.ReferenceDir}, {"apps/workbench/public", config.AssetDir}} {
		if err := validateAssetSeparation(pair[0], pair[1]); err != nil {
			return report, errors.New("maintenance private directories must be separate from each other and static content")
		}
	}
	lease, err := AcquireStorageLease(config.ReferenceDir, config.AssetDir)
	if err != nil {
		return report, err
	}
	defer lease.Close()
	referenceDir, err := filepath.EvalSymlinks(config.ReferenceDir)
	if err != nil {
		return report, errors.New("maintenance reference directory unavailable")
	}
	assetDir, err := filepath.EvalSymlinks(config.AssetDir)
	if err != nil {
		return report, errors.New("maintenance asset directory unavailable")
	}
	referenceDir, _ = filepath.Abs(referenceDir)
	assetDir, _ = filepath.Abs(assetDir)
	conn, err := pgx.Connect(ctx, config.DatabaseURL)
	if err != nil {
		return report, errors.New("maintenance database connection unavailable")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(0x53545544494F)).Scan(&locked); err != nil || !locked {
		return report, errors.New("maintenance requires the workbench to be stopped; PostgreSQL writer lock unavailable")
	}
	reachability, err := maintenanceReachableDetails(ctx, conn)
	if err != nil {
		return report, err
	}
	refs, assets := reachability.References, reachability.Assets
	referenceRoot, err := os.OpenRoot(referenceDir)
	if err != nil {
		return report, errors.New("maintenance reference directory cannot be opened")
	}
	defer referenceRoot.Close()
	assetRoot, err := os.OpenRoot(assetDir)
	if err != nil {
		return report, errors.New("maintenance asset directory cannot be opened")
	}
	defer assetRoot.Close()
	if err = requireStorageOwnership(ctx, conn, referenceRoot, assetRoot); err != nil {
		return report, err
	}
	// Detect a wrong storage path or incomplete restore before offering deletion.
	for name := range refs {
		if err = maintenanceRequireFile(referenceRoot, name); err != nil {
			return report, err
		}
	}
	for id := range reachability.Required {
		for _, suffix := range []string{".bin", ".json"} {
			if err = maintenanceRequireFile(assetRoot, id+suffix); err != nil {
				return report, err
			}
		}
		info, readErr := readAssetManifest(assetRoot, id)
		if readErr != nil {
			return report, errors.New("reachable asset manifest is invalid; repair storage before maintenance")
		}
		if info.Thumbnail != nil {
			if err = maintenanceRequireFile(assetRoot, id+".thumb.png"); err != nil {
				return report, err
			}
		}
	}
	for _, receipt := range reachability.Receipts {
		if err = maintenanceCheckDeliveryReceipt(ctx, assetRoot, receipt); err != nil {
			return report, err
		}
	}
	report = StorageMaintenanceReport{SchemaVersion: maintenanceSchemaVersion, ReferenceDir: referenceDir, AssetDir: assetDir, MinAgeHours: config.MinAge.Hours(), Candidates: []MaintenanceFile{}}
	cutoff := time.Now().Add(-config.MinAge)
	if err = maintenanceScan(ctx, referenceRoot, "references", refs, nil, cutoff, &report); err != nil {
		return report, err
	}
	if err = maintenanceScan(ctx, assetRoot, "assets", nil, assets, cutoff, &report); err != nil {
		return report, err
	}
	sort.Slice(report.Candidates, func(i, j int) bool {
		a, b := report.Candidates[i], report.Candidates[j]
		if a.Store != b.Store {
			return a.Store < b.Store
		}
		return a.Name < b.Name
	})
	reachable := make([]string, 0, len(refs)+len(assets))
	for name := range refs {
		reachable = append(reachable, "references/"+name)
	}
	for id := range assets {
		reachable = append(reachable, "assets/"+id)
	}
	sort.Strings(reachable)
	identity, _ := json.Marshal(struct {
		Schema                 int
		ReferenceDir, AssetDir string
		MinAge                 time.Duration
		Reachable              []string
		Deliveries             []maintenanceDeliveryReference
		ReviewDispatches       []maintenanceReviewDispatchReference
		Files                  []MaintenanceFile
	}{maintenanceSchemaVersion, referenceDir, assetDir, config.MinAge, reachable, reachability.Deliveries, reachability.ReviewDispatches, report.Candidates})
	digest := sha256.Sum256(identity)
	report.PlanDigest = hex.EncodeToString(digest[:])
	if !config.Apply {
		return report, nil
	}
	if config.PlanDigest != report.PlanDigest {
		return report, errors.New("maintenance plan changed; review a fresh dry run before applying")
	}
	// Recheck every candidate before the first removal. All cooperating writers
	// remain excluded by OS locks, even if the DB connection is interrupted.
	for _, candidate := range report.Candidates {
		root := assetRoot
		if candidate.Store == "references" {
			root = referenceRoot
		}
		checked, checkErr := maintenanceInspect(ctx, root, candidate.Store, candidate.Name)
		if checkErr != nil || checked != candidate {
			return report, errors.New("maintenance candidate changed; no removals started")
		}
	}
	if err = conn.Ping(ctx); err != nil {
		return report, errors.New("database ownership lost; no removals started")
	}
	for _, candidate := range report.Candidates {
		if err = ctx.Err(); err != nil {
			return report, errors.New("maintenance interrupted; inspect the reported partial removal count")
		}
		root := assetRoot
		if candidate.Store == "references" {
			root = referenceRoot
		}
		if err = root.Remove(candidate.Name); err != nil {
			return report, errors.New("maintenance removal failed; inspect the reported partial removal count")
		}
		report.RemovedFiles++
		report.RemovedBytes += candidate.ByteSize
	}
	report.Applied = true
	return report, nil
}

func maintenanceRequireFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("reachable private file is missing or unsafe; repair the matching database and storage before maintenance")
	}
	return nil
}

func maintenanceBaseReachable(ctx context.Context, conn *pgx.Conn) (map[string]bool, map[string]bool, error) {
	refs, assets := map[string]bool{}, map[string]bool{}
	var versions []int32
	if err := conn.QueryRow(ctx, "SELECT array_agg(version ORDER BY version) FROM studio_schema_versions").Scan(&versions); err != nil || len(versions) != maintenanceSchemaVersion {
		return nil, nil, fmt.Errorf("maintenance requires exactly schema versions 001..%03d; it does not run migrations", maintenanceSchemaVersion)
	}
	for i, v := range versions {
		if int(v) != i+1 {
			return nil, nil, errors.New("maintenance database schema is incompatible")
		}
	}
	rows, err := conn.Query(ctx, "SELECT blob FROM studio_references")
	if err != nil {
		return nil, nil, errors.New("reference reachability could not be read")
	}
	for rows.Next() {
		var raw []byte
		var blob ReferenceBlob
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, nil, errors.New("reference reachability could not be decoded")
		}
		if err = json.Unmarshal(raw, &blob); err != nil || !privateBlobID.MatchString(blob.ID) {
			rows.Close()
			return nil, nil, errors.New("invalid persisted reference; maintenance refused")
		}
		refs[blob.ID+".bin"] = true
		if blob.Thumbnail != nil {
			if !privateBlobID.MatchString(blob.Thumbnail.ID) {
				rows.Close()
				return nil, nil, errors.New("invalid persisted thumbnail; maintenance refused")
			}
			refs[blob.Thumbnail.ID+".bin"] = true
		}
		if len(refs) > maintenanceEntryLimit {
			rows.Close()
			return nil, nil, errors.New("maintenance reachability limit exceeded; use a reviewed partitioned maintenance procedure")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, errors.New("reference reachability query interrupted")
	}
	rows, err = conn.Query(ctx, "SELECT document->'artifacts', submission->>'body_file_id' FROM studio_tasks")
	if err != nil {
		return nil, nil, errors.New("task reachability could not be read")
	}
	for rows.Next() {
		var raw []byte
		var bodyID *string
		var artifacts []Artifact
		if err = rows.Scan(&raw, &bodyID); err != nil {
			rows.Close()
			return nil, nil, errors.New("task reachability could not be decoded")
		}
		if bodyID != nil && *bodyID != "" {
			if !privateBlobID.MatchString(*bodyID) {
				rows.Close()
				return nil, nil, errors.New("invalid persisted edit body; maintenance refused")
			}
			refs[*bodyID+".bin"] = true
		}
		if len(raw) > 0 && string(raw) != "null" {
			if err = json.Unmarshal(raw, &artifacts); err != nil {
				rows.Close()
				return nil, nil, errors.New("invalid persisted artifacts; maintenance refused")
			}
		}
		for _, artifact := range artifacts {
			if localAssetID.MatchString(artifact.ID) {
				assets[artifact.ID] = true
			} else if strings.HasPrefix(artifact.ID, "asset_") {
				rows.Close()
				return nil, nil, errors.New("unrecognized private asset identity; maintenance refused")
			}
		}
		if len(refs)+len(assets) > maintenanceEntryLimit {
			rows.Close()
			return nil, nil, errors.New("maintenance reachability limit exceeded")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, errors.New("task reachability query interrupted")
	}
	// Candidates remain durable references in every moderation/worker state.
	// An upstream terminal state or a blocked decision never makes them orphaned.
	rows, err = conn.Query(ctx, "SELECT candidates FROM studio_task_media_candidates")
	if err != nil {
		return nil, nil, errors.New("media candidate reachability could not be read")
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, nil, errors.New("media candidate reachability could not be decoded")
		}
		candidates, decodeErr := decodeMediaCandidates(raw)
		if decodeErr != nil || len(candidates) == 0 {
			rows.Close()
			return nil, nil, errors.New("invalid persisted media candidates; maintenance refused")
		}
		for _, candidate := range candidates {
			assets[candidate.ID] = true
		}
		if len(refs)+len(assets) > maintenanceEntryLimit {
			rows.Close()
			return nil, nil, errors.New("maintenance reachability limit exceeded")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, errors.New("media candidate reachability query interrupted")
	}
	return refs, assets, nil
}

func maintenanceScan(ctx context.Context, root *os.Root, store string, refs, assets map[string]bool, cutoff time.Time, report *StorageMaintenanceReport) error {
	dir, err := root.Open(".")
	if err != nil {
		return errors.New("maintenance directory enumeration failed")
	}
	defer dir.Close()
	for {
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return errors.New("maintenance scan interrupted")
			}
			report.ScannedFiles++
			if report.ScannedFiles > maintenanceEntryLimit {
				return errors.New("maintenance file limit exceeded")
			}
			name := entry.Name()
			info, err := entry.Info()
			if err != nil {
				return errors.New("maintenance file metadata unavailable")
			}
			pattern := assetGCFile
			if store == "references" {
				pattern = referenceGCFile
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !pattern.MatchString(name) {
				report.IgnoredFiles++
				continue
			}
			protected := refs[name]
			if store == "assets" && len(name) >= 70 {
				protected = assets[name[:70]]
			}
			if protected {
				report.ProtectedFiles++
				continue
			}
			if !info.ModTime().Before(cutoff) {
				report.RecentFiles++
				continue
			}
			candidate, err := maintenanceInspect(ctx, root, store, name)
			if err != nil {
				return err
			}
			report.Candidates = append(report.Candidates, candidate)
			report.CandidateBytes += candidate.ByteSize
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return errors.New("maintenance directory enumeration interrupted")
		}
	}
}

func maintenanceInspect(ctx context.Context, root *os.Root, store, name string) (MaintenanceFile, error) {
	var result MaintenanceFile
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > 256<<20 {
		return result, errors.New("maintenance candidate is unsafe or exceeds the per-file scan budget")
	}
	f, err := root.Open(name)
	if err != nil {
		return result, errors.New("maintenance candidate cannot be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) {
		return result, errors.New("maintenance candidate identity changed")
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			return result, errors.New("maintenance hashing interrupted")
		}
		n, readErr := f.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, errors.New("maintenance candidate could not be hashed")
		}
	}
	after, err := f.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return result, errors.New("maintenance candidate changed during hashing")
	}
	result = MaintenanceFile{Store: store, Name: name, ByteSize: info.Size(), SHA256: fmt.Sprintf("%x", hash.Sum(nil)), ModifiedAt: info.ModTime().UTC()}
	return result, nil
}
