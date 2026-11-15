package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
)

func loadMediaDeliverySQL(ctx context.Context, q sqlQueryRower, taskID string) (*MediaDeliveryRecord, error) {
	r := &MediaDeliveryRecord{}
	err := q.QueryRow(ctx, `SELECT task_id,id,workspace_id,project_id,state,version,key_id,manifest_digest,handle_digest,ciphertext,item_count,result_status FROM studio_media_deliveries WHERE task_id=$1`, taskID).Scan(&r.TaskID, &r.ID, &r.WorkspaceID, &r.ProjectID, &r.State, &r.Version, &r.KeyID, &r.ManifestDigest, &r.HandleDigest, &r.Ciphertext, &r.ItemCount, &r.ResultStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = q.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('Position',position,'AssetID',asset_id,'Receipt',receipt) ORDER BY position),'[]'::jsonb) FROM studio_media_delivery_items WHERE task_id=$1`, taskID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if len(raw) > 128<<10 || json.Unmarshal(raw, &r.Items) != nil || !validMediaDeliveryRecord(r) {
		return nil, errors.New("invalid private delivery record")
	}
	return r, nil
}

func validMediaDeliveryRecord(r *MediaDeliveryRecord) bool {
	if r == nil || !mediaDeliveryID.MatchString(r.ID) || r.ItemCount < 1 || r.ItemCount > 4 || len(r.Items) != r.ItemCount || len(r.Ciphertext) < 29 || len(r.Ciphertext) > maxMediaDeliveryPlaintext+64 || len(r.KeyID) != 32 || !moderationDigestPattern.MatchString(r.ManifestDigest) || !moderationDigestPattern.MatchString(r.HandleDigest) || r.ResultStatus != "SUCCEEDED" && r.ResultStatus != "PARTIAL" || r.State != "READY" && r.State != "COMPLETE" && r.State != "PARKED" {
		return false
	}
	for i, item := range r.Items {
		if item.Position != i || item.AssetID != mediaDeliveryAssetID(r.ID, i) {
			return false
		}
		if info := item.Receipt; info != nil && (!validAssetInfo(*info, item.AssetID) || info.Delivery == nil || !validDeliveryMarker(info.Delivery, item.AssetID) || info.Delivery.DeliveryID != r.ID || info.Delivery.Slot != i) {
			return false
		}
	}
	return true
}

func mediaDeliveryConflict() error {
	return failure(409, "MEDIA_DELIVERY_STATE_CHANGED", "私有交付状态已变化，原结果保持隔离")
}

func sameMediaDeliveryHeader(a, b *MediaDeliveryRecord) bool {
	return a.TaskID == b.TaskID && a.ID == b.ID && a.WorkspaceID == b.WorkspaceID && a.ProjectID == b.ProjectID && a.KeyID == b.KeyID && a.ManifestDigest == b.ManifestDigest && a.HandleDigest == b.HandleDigest && a.ItemCount == b.ItemCount && a.ResultStatus == b.ResultStatus && bytes.Equal(a.Ciphertext, b.Ciphertext)
}

// This hook runs after the task lease fence and before review/library/ledger.
// Nil private state means preserve the DB record, never erase or re-query it.
func saveTaskMediaDeliverySQL(ctx context.Context, tx pgx.Tx, t *taskRecord) error {
	old, err := loadMediaDeliverySQL(ctx, tx, t.Task.ID)
	if err != nil {
		return err
	}
	r := cloneMediaDelivery(t.Delivery)
	if r == nil {
		r = cloneMediaDelivery(old)
	}
	if r == nil {
		return nil
	}
	if !validMediaDeliveryRecord(r) || r.TaskID != t.Task.ID || r.WorkspaceID != t.Task.WorkspaceID || r.ProjectID != t.Task.ProjectID || r.HandleDigest != mediaDeliveryDigest(t.Handle) {
		return mediaDeliveryConflict()
	}
	if old != nil && (!sameMediaDeliveryHeader(r, old) || r.Version != old.Version) {
		return mediaDeliveryConflict()
	}
	all := true
	for _, item := range r.Items {
		all = all && item.Receipt != nil
	}
	if t.Task.Status == "SUCCEEDED" || t.Task.Status == "PARTIAL" {
		if !all || t.Task.Status != r.ResultStatus || len(t.Task.Artifacts) != len(r.Items) {
			return mediaDeliveryConflict()
		}
		for i, a := range t.Task.Artifacts {
			if a.ID != r.Items[i].AssetID || a.URL != "/api/v1/assets/"+a.ID || a.Kind != t.Task.Kind || a.StorageInfo != nil && !sameMediaDeliveryReceipt(a.StorageInfo, r.Items[i].Receipt) {
				return mediaDeliveryConflict()
			}
		}
		r.State = "COMPLETE"
	} else {
		if len(t.Task.Artifacts) != 0 {
			return mediaDeliveryConflict()
		}
		if terminal(t.Task.Status) || t.Task.Status == "RECONCILE_REQUIRED" || t.Task.Status == "SUBMIT_UNKNOWN" {
			r.State = "PARKED"
		} else if t.Task.Status != "INGESTING" && t.Task.Status != "MODERATING" {
			return mediaDeliveryConflict()
		}
	}
	if old != nil && old.State != "READY" && (r.State != old.State || !reflect.DeepEqual(r.Items, old.Items)) {
		return mediaDeliveryConflict()
	}
	if old == nil {
		if r.Version != 0 || r.State != "READY" {
			return mediaDeliveryConflict()
		}
		r.Version = 1
		_, err = tx.Exec(ctx, `INSERT INTO studio_media_deliveries(task_id,id,workspace_id,project_id,format_version,state,version,key_id,manifest_digest,handle_digest,ciphertext,item_count,result_status) VALUES($1,$2,$3,$4,1,$5,1,$6,$7,$8,$9,$10,$11)`, r.TaskID, r.ID, r.WorkspaceID, r.ProjectID, r.State, r.KeyID, r.ManifestDigest, r.HandleDigest, r.Ciphertext, r.ItemCount, r.ResultStatus)
		if err != nil {
			return err
		}
		for _, item := range r.Items {
			if item.Receipt != nil {
				return mediaDeliveryConflict()
			}
			if _, err = tx.Exec(ctx, `INSERT INTO studio_media_delivery_items(task_id,position,asset_id) VALUES($1,$2,$3)`, r.TaskID, item.Position, item.AssetID); err != nil {
				return err
			}
		}
	} else {
		changed := r.State != old.State
		for i, item := range r.Items {
			prior := old.Items[i]
			if item.Position != prior.Position || item.AssetID != prior.AssetID || prior.Receipt != nil && !sameMediaDeliveryReceipt(item.Receipt, prior.Receipt) {
				return mediaDeliveryConflict()
			}
			if prior.Receipt == nil && item.Receipt != nil {
				raw, e := json.Marshal(item.Receipt)
				if e != nil || len(raw) > 16384 {
					return mediaDeliveryConflict()
				}
				if _, err = tx.Exec(ctx, `UPDATE studio_media_delivery_items SET receipt=$3,receipted_at=clock_timestamp() WHERE task_id=$1 AND position=$2 AND receipt IS NULL`, r.TaskID, i, raw); err != nil {
					return err
				}
				changed = true
			}
		}
		if changed {
			if old.Version >= 9007199254740991 {
				return mediaDeliveryConflict()
			}
			r.Version = old.Version + 1
			if _, err = tx.Exec(ctx, `UPDATE studio_media_deliveries SET state=$2,version=$3,updated_at=clock_timestamp() WHERE task_id=$1 AND version=$4`, r.TaskID, r.State, r.Version, old.Version); err != nil {
				return err
			}
		}
	}
	t.Delivery = r
	t.Task.CanReconcile = false
	doc, _ := json.Marshal(t.Task)
	_, err = tx.Exec(ctx, `UPDATE studio_tasks SET document=$2 WHERE id=$1 AND workspace_id=$3`, t.Task.ID, doc, t.Task.WorkspaceID)
	return err
}

func validateMediaDeliveryStore(ctx context.Context, q sqlQueryRower, c *mediaDeliveryCipher, phased bool) error {
	var count, wrong, ready int
	key := ""
	if c != nil {
		key = c.keyID
	}
	if err := q.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE key_id<>$1),count(*) FILTER(WHERE state='READY' AND NOT EXISTS(SELECT 1 FROM studio_media_review_dispatch r WHERE r.task_id=d.task_id AND r.state IN('READY','STARTED') AND (SELECT count(*) FROM studio_media_delivery_items i WHERE i.task_id=d.task_id AND i.receipt IS NOT NULL)=d.item_count)) FROM studio_media_deliveries d`, key).Scan(&count, &wrong, &ready); err != nil {
		return err
	}
	if count > 0 && (c == nil || wrong > 0) {
		return errors.New("registered media deliveries require their original independent encryption key")
	}
	if ready > 0 && !phased {
		return errors.New("pending media deliveries require a compatible delivery provider")
	}
	return nil
}
