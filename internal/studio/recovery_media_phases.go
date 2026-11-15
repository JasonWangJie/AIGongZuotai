package studio

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

const recoveryMediaPageRecords = 128
const recoveryMediaPageBytes int64 = 16 << 20

var errRecoveryMediaPageBudget = errors.New("recovery media page exceeds encoded metadata budget")

// loadRecoveryMediaPhasesSQL reads at most one page in a repeatable-read
// snapshot. The size preflight and the four reads observe the same rows,
// including when an old executor still has an auxiliary connection open.
// No caller-owned record is changed before every row validates and COMMIT is
// confirmed. This only loads private metadata; existing execution gates still
// verify ciphertext, receipts and actual local/S3 bytes before external I/O.
func loadRecoveryMediaPhasesSQL(ctx context.Context, conn *pgx.Conn, records []*taskRecord, byteBudget int64) (int64, error) {
	if len(records) == 0 {
		return 0, nil
	}
	if len(records) > recoveryMediaPageRecords || byteBudget <= 0 || byteBudget > recoveryMediaPageBytes {
		return 0, errors.New("invalid recovery media page bounds")
	}
	ids := make([]string, 0, len(records))
	byID := make(map[string]*taskRecord, len(records))
	for _, record := range records {
		if record == nil || record.Task.ID == "" || byID[record.Task.ID] != nil {
			return 0, errors.New("invalid recovery media page identity")
		}
		ids = append(ids, record.Task.ID)
		byID[record.Task.ID] = record
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Row JSON sizes are a conservative encoded metadata budget: bytea is hex
	// encoded and unused timestamps/columns are counted too. No JSON arrays or
	// private row bodies cross the connection before the budget passes.
	var used, count int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(sum(bytes),0)::bigint,COALESCE(sum(n),0)::bigint FROM (
	 SELECT sum(octet_length(to_jsonb(d)::text)) AS bytes,count(*) AS n FROM studio_media_deliveries d WHERE task_id=ANY($1::text[])
	 UNION ALL SELECT sum(octet_length(to_jsonb(i)::text)),count(*) FROM studio_media_delivery_items i WHERE task_id=ANY($1::text[])
	 UNION ALL SELECT sum(octet_length(to_jsonb(r)::text)),count(*) FROM studio_media_review_dispatch r WHERE task_id=ANY($1::text[])
	 UNION ALL SELECT sum(octet_length(to_jsonb(m)::text)),count(*) FROM studio_task_media_moderation m WHERE task_id=ANY($1::text[])
	 UNION ALL SELECT sum(octet_length(to_jsonb(c)::text)),count(*) FROM studio_task_media_candidates c WHERE task_id=ANY($1::text[])
	) bounded`, ids).Scan(&used, &count)
	if err != nil {
		return 0, err
	}
	if count > int64(len(records)*8) {
		return 0, errors.New("invalid recovery media page row count")
	}
	if used > byteBudget {
		return 0, errRecoveryMediaPageBudget
	}
	deliveries := make(map[string]*MediaDeliveryRecord)
	dispatches := make(map[string]*MediaReviewDispatch)
	moderation := make(map[string]*MediaModerationRecord)
	read := func(query string, scan func(pgx.Rows) error) error {
		rows, err := tx.Query(ctx, query, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err = scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err = read(`SELECT task_id,id,workspace_id,project_id,state,version,key_id,manifest_digest,handle_digest,ciphertext,item_count,result_status FROM studio_media_deliveries WHERE task_id=ANY($1::text[]) ORDER BY task_id`, func(rows pgx.Rows) error {
		r := &MediaDeliveryRecord{}
		if err := rows.Scan(&r.TaskID, &r.ID, &r.WorkspaceID, &r.ProjectID, &r.State, &r.Version, &r.KeyID, &r.ManifestDigest, &r.HandleDigest, &r.Ciphertext, &r.ItemCount, &r.ResultStatus); err != nil {
			return err
		}
		t := byID[r.TaskID]
		if t == nil || t.Task.WorkspaceID != r.WorkspaceID || t.Task.ProjectID != r.ProjectID || r.ItemCount < 1 || r.ItemCount > 4 {
			return mediaDeliveryConflict()
		}
		deliveries[r.TaskID] = r
		return nil
	}); err != nil {
		return 0, err
	}
	if err = read(`SELECT task_id,position,asset_id,receipt FROM studio_media_delivery_items WHERE task_id=ANY($1::text[]) ORDER BY task_id,position`, func(rows pgx.Rows) error {
		var id string
		var item MediaDeliveryPlannedItem
		var raw []byte
		if err := rows.Scan(&id, &item.Position, &item.AssetID, &raw); err != nil {
			return err
		}
		d := deliveries[id]
		if d == nil || len(d.Items) >= d.ItemCount || len(raw) > 16384 {
			return mediaDeliveryConflict()
		}
		if len(raw) > 0 && json.Unmarshal(raw, &item.Receipt) != nil {
			return mediaDeliveryConflict()
		}
		d.Items = append(d.Items, item)
		return nil
	}); err != nil {
		return 0, err
	}
	for _, d := range deliveries {
		if !validMediaDeliveryRecord(d) {
			return 0, mediaDeliveryConflict()
		}
	}
	if err = read(`SELECT task_id,workspace_id,project_id,candidate_digest,policy_digest,result_status,state,version,attempt_id,started_at,finished_at FROM studio_media_review_dispatch WHERE task_id=ANY($1::text[]) ORDER BY task_id`, func(rows pgx.Rows) error {
		r := &MediaReviewDispatch{}
		if err := rows.Scan(&r.TaskID, &r.WorkspaceID, &r.ProjectID, &r.CandidateDigest, &r.PolicyDigest, &r.ResultStatus, &r.State, &r.Version, &r.AttemptID, &r.StartedAt, &r.FinishedAt); err != nil {
			return err
		}
		t := byID[r.TaskID]
		if t == nil || t.Task.WorkspaceID != r.WorkspaceID || t.Task.ProjectID != r.ProjectID {
			return mediaReviewStateError()
		}
		dispatches[r.TaskID] = r
		return nil
	}); err != nil {
		return 0, err
	}
	if err = read(`SELECT m.task_id,m.workspace_id,m.project_id,m.policy,m.text_policy,m.policy_id,m.policy_version,m.state,m.version,m.reason_code,c.candidates FROM studio_task_media_moderation m LEFT JOIN studio_task_media_candidates c ON c.task_id=m.task_id WHERE m.task_id=ANY($1::text[]) ORDER BY m.task_id`, func(rows pgx.Rows) error {
		var id, workspace, project string
		var policy, textPolicy, candidates []byte
		s := ModerationSummary{Phase: "output"}
		if err := rows.Scan(&id, &workspace, &project, &policy, &textPolicy, &s.PolicyID, &s.PolicyVersion, &s.State, &s.Version, &s.ReasonCode, &candidates); err != nil {
			return err
		}
		t := byID[id]
		if t == nil || t.Task.WorkspaceID != workspace || t.Task.ProjectID != project || t.Task.Moderation == nil {
			return mediaModerationStateError()
		}
		state, err := decodeTaskMediaModeration(policy, textPolicy, candidates, s)
		if err != nil {
			return err
		}
		moderation[id] = state
		return nil
	}); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	for _, record := range records {
		record.Delivery = deliveries[record.Task.ID]
		record.ReviewDispatch = dispatches[record.Task.ID]
		record.MediaModeration = moderation[record.Task.ID]
	}
	return used, nil
}
