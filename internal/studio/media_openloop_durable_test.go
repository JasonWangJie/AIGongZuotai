package studio

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type openLoopOriginal259 struct {
	Handle, Key, SHA       string
	AcceptedAt, TerminalAt time.Time
	GETs, RateLimits       int
}
type openLoopPersisted259 struct {
	ID               string
	Created, Updated time.Time
}

// One bounded final scan of the fresh schema, outside the arrival and drain
// measurements. Public receipt scope and frozen wire identity must both agree.
func openLoopVerifyRows259(ctx context.Context, db *pgx.Conn, accounts []admissionCapacityAccount, records []openLoopAdmission259, expected func(int) openLoopOriginal259) ([]openLoopPersisted259, error) {
	byID := make(map[string]int)
	for i, r := range records {
		if r.outcome != "accepted" {
			continue
		}
		if _, duplicate := byID[r.id]; duplicate {
			return nil, errors.New("two first receipts share an operation")
		}
		byID[r.id] = i
	}
	rows, err := db.Query(ctx, `SELECT t.id,t.workspace_id,t.project_id,t.document->>'model_id',t.document->>'status',COALESCE(t.document->'error'->>'code',''),
		COALESCE(t.handle->>'ID',''),COALESCE(t.submission->>'idempotency_key',''),COALESCE(t.submission->>'body_sha256',''),k.scope_key,
		COALESCE(t.handle->>'ProviderIdentity'=t.submission->>'provider_identity' AND length(t.handle->>'ProviderIdentity')=64 AND s.channel_key=t.handle->>'ProviderIdentity'
		AND t.handle->>'CredentialID'=t.submission->>'credential_id' AND t.handle->>'Kind'=t.submission->>'kind'
		AND t.handle->>'Count'=t.submission->>'count' AND t.handle->>'SubmittedAt'=t.submission->>'prepared_at',false),
		(s.workspace_id=t.workspace_id AND s.provider_mode='live' AND s.lane='query' AND NOT s.active AND t.lease_owner=''),
		(t.document->>'created_at')::timestamptz,(t.document->>'updated_at')::timestamptz
		FROM studio_tasks t JOIN studio_idempotency k ON k.resource_id=t.id AND k.resource_type='task'
		JOIN studio_task_schedule s ON s.task_id=t.id`)
	if err != nil {
		return nil, errors.New("could not read final persisted workload")
	}
	defer rows.Close()
	keys := make(map[string]bool, len(byID))
	out := make([]openLoopPersisted259, 0, len(byID))
	for rows.Next() {
		var row openLoopPersisted259
		var workspace, project, model, status, code, handle, key, hash, scope string
		var frozen, released bool
		if rows.Scan(&row.ID, &workspace, &project, &model, &status, &code, &handle, &key, &hash, &scope, &frozen, &released, &row.Created, &row.Updated) != nil {
			return nil, errors.New("unreadable final media row")
		}
		idx, ok := byID[row.ID]
		if !ok {
			return nil, errors.New("unexpected persisted operation or duplicate receipt")
		}
		delete(byID, row.ID)
		record := records[idx]
		wanted := expected(idx)
		if key == "" || keys[key] || wanted.Key != key || wanted.Handle != handle || wanted.SHA != hash || !frozen || !released ||
			model != record.input.ModelID || workspace != accounts[record.owner].identity.Workspace.ID || project != record.input.ProjectID || scope != workspace+":tasks:"+record.key ||
			status != "FAILED" || code != "UPSTREAM_GENERATION_FAILED" || row.Updated.Before(row.Created) || row.Created.Sub(record.rowCreated).Abs() > time.Microsecond {
			return nil, errors.New("persisted scope, frozen request, wire, lease or controlled terminal disagrees")
		}
		keys[key] = true
		out = append(out, row)
	}
	if rows.Err() != nil || len(byID) != 0 {
		return nil, errors.New("incomplete final durable evidence")
	}
	return out, nil
}
