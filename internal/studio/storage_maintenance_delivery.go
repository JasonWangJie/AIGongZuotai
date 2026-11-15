package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

// Planned identities protect files even when import has not written a receipt.
// Required identities also include every pre-existing artifact/candidate, so a
// plan can never weaken the complete-file requirement of an older reference.
type maintenanceReachability struct {
	References, Assets, Required map[string]bool
	Receipts                     map[string]AssetInfo
	Deliveries                   []maintenanceDeliveryReference
	ReviewDispatches             []maintenanceReviewDispatchReference
}

type maintenanceDeliveryReference struct {
	ID, TaskID, State, ManifestDigest string
	Version                           int64
	Position                          int
	AssetID                           string
	Receipt                           *AssetInfo
}

// Dispatches add no new file identities. Their full phase identity and the
// task version still belong in the offline plan, so a cancellation or review
// transition cannot leave an earlier deletion plan executable.
type maintenanceReviewDispatchReference struct {
	TaskID, State, CandidateDigest, PolicyDigest, ResultStatus, AttemptID string
	Version, TaskVersion                                                  int64
	StartedAt, FinishedAt                                                 *time.Time
}

// Preserve the all-identities view used for first owner adoption and B2 tests.
func maintenanceReachable(ctx context.Context, conn *pgx.Conn) (map[string]bool, map[string]bool, error) {
	r, err := maintenanceReachableDetails(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	return r.References, r.Assets, nil
}

func maintenanceReachableDetails(ctx context.Context, conn *pgx.Conn) (*maintenanceReachability, error) {
	refs, assets, err := maintenanceBaseReachable(ctx, conn)
	if err != nil {
		return nil, err
	}
	r := &maintenanceReachability{References: refs, Assets: assets, Required: make(map[string]bool), Receipts: make(map[string]AssetInfo), Deliveries: []maintenanceDeliveryReference{}, ReviewDispatches: []maintenanceReviewDispatchReference{}}
	for id := range assets {
		r.Required[id] = true
	}
	rows, err := conn.Query(ctx, `SELECT d.id,d.task_id,d.state,d.version,d.manifest_digest,d.item_count,i.position,i.asset_id,i.receipt FROM studio_media_deliveries d LEFT JOIN studio_media_delivery_items i ON i.task_id=d.task_id ORDER BY d.id,i.position`)
	if err != nil {
		return nil, errors.New("delivery reachability could not be read")
	}
	defer rows.Close()
	lastID, seen, expected := "", 0, 0
	for rows.Next() {
		var item maintenanceDeliveryReference
		var position *int
		var assetID *string
		var count int
		var raw []byte
		if err = rows.Scan(&item.ID, &item.TaskID, &item.State, &item.Version, &item.ManifestDigest, &count, &position, &assetID, &raw); err != nil {
			return nil, errors.New("delivery reachability could not be decoded")
		}
		if item.ID != lastID {
			if lastID != "" && seen != expected {
				return nil, errors.New("incomplete delivery plan; maintenance refused")
			}
			lastID, seen, expected = item.ID, 0, count
		}
		if count < 1 || count > 4 || position == nil || assetID == nil || *position != seen || !mediaDeliveryID.MatchString(item.ID) || !localAssetID.MatchString(*assetID) || mediaDeliveryAssetID(item.ID, *position) != *assetID || item.Version < 1 || (item.State != "READY" && item.State != "COMPLETE" && item.State != "PARKED") {
			return nil, errors.New("invalid delivery plan; maintenance refused")
		}
		seen++
		item.Position, item.AssetID = *position, *assetID
		r.Assets[item.AssetID] = true
		if len(raw) > 0 {
			var receipt AssetInfo
			if json.Unmarshal(raw, &receipt) != nil || !validAssetInfo(receipt, item.AssetID) || receipt.Delivery == nil || !validDeliveryMarker(receipt.Delivery, item.AssetID) || receipt.Delivery.DeliveryID != item.ID || receipt.Delivery.Slot != item.Position {
				return nil, errors.New("invalid delivery receipt; maintenance refused")
			}
			item.Receipt = &receipt
			r.Receipts[item.AssetID] = receipt
			r.Required[item.AssetID] = true
		}
		if item.State == "COMPLETE" && item.Receipt == nil {
			return nil, errors.New("complete delivery lacks receipt; maintenance refused")
		}
		r.Deliveries = append(r.Deliveries, item)
		if len(r.References)+len(r.Assets) > maintenanceEntryLimit || len(r.Deliveries) > maintenanceEntryLimit {
			return nil, errors.New("maintenance delivery reachability limit exceeded")
		}
	}
	if rows.Err() != nil {
		return nil, errors.New("delivery reachability query interrupted")
	}
	if lastID != "" && seen != expected {
		return nil, errors.New("incomplete delivery plan; maintenance refused")
	}
	rows.Close()
	reviews, err := conn.Query(ctx, `SELECT d.task_id,d.state,d.version,d.candidate_digest,d.policy_digest,d.result_status,d.attempt_id,d.started_at,d.finished_at,(t.document->>'version')::bigint FROM studio_media_review_dispatch d JOIN studio_tasks t ON t.id=d.task_id ORDER BY d.task_id`)
	if err != nil {
		return nil, errors.New("review dispatch reachability could not be read")
	}
	defer reviews.Close()
	for reviews.Next() {
		var item maintenanceReviewDispatchReference
		if err = reviews.Scan(&item.TaskID, &item.State, &item.Version, &item.CandidateDigest, &item.PolicyDigest, &item.ResultStatus, &item.AttemptID, &item.StartedAt, &item.FinishedAt, &item.TaskVersion); err != nil {
			return nil, errors.New("review dispatch reachability could not be decoded")
		}
		if item.Version < 1 || item.Version > 9007199254740991 || item.TaskVersion < 1 || item.TaskVersion > 9007199254740991 || !moderationDigestPattern.MatchString(item.CandidateDigest) || !moderationDigestPattern.MatchString(item.PolicyDigest) || (item.State != "READY" && item.State != "STARTED" && item.State != "FINISHED") || (item.ResultStatus != "SUCCEEDED" && item.ResultStatus != "PARTIAL") {
			return nil, errors.New("invalid review dispatch; maintenance refused")
		}
		r.ReviewDispatches = append(r.ReviewDispatches, item)
		if len(r.ReviewDispatches) > maintenanceEntryLimit {
			return nil, errors.New("maintenance review dispatch reachability limit exceeded")
		}
	}
	if reviews.Err() != nil {
		return nil, errors.New("review dispatch reachability query interrupted")
	}
	return r, nil
}

// Offline maintenance verifies retained local bytes, including S3 backups. It
// does not claim that remote objects or their availability have been checked.
func maintenanceCheckDeliveryReceipt(ctx context.Context, root *os.Root, expected AssetInfo) error {
	if err := maintenanceRequireFile(root, expected.ID+".json"); err != nil {
		return err
	}
	actual, err := readAssetManifest(root, expected.ID)
	want, wantErr := json.Marshal(expected)
	got, gotErr := json.Marshal(actual)
	if err != nil || wantErr != nil || gotErr != nil || !bytes.Equal(want, got) {
		return errors.New("delivery manifest differs from its immutable receipt; maintenance refused")
	}
	checks := map[string]AssetInfo{expected.ID + ".bin": expected}
	if expected.Thumbnail != nil {
		checks[expected.ID+".thumb.png"] = *expected.Thumbnail
	}
	for name, info := range checks {
		if err = maintenanceRequireFile(root, name); err != nil {
			return err
		}
		file, openErr := root.Open(name)
		if openErr != nil {
			return errors.New("delivery bytes unavailable; maintenance refused")
		}
		err = verifyMediaModerationFile(ctx, file, info)
		_ = file.Close()
		if err != nil {
			return errors.New("delivery bytes differ from immutable receipt; maintenance refused")
		}
	}
	return nil
}
