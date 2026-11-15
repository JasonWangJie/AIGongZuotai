package studio

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type GenerationPlanInput struct {
	WorkspaceID           string `json:"workspace_id"`
	Action                string `json:"action"`
	ExpectedVersion       uint64 `json:"expected_version"`
	RestrictionID         string `json:"restriction_id,omitempty"`
	AppealID              string `json:"appeal_id,omitempty"`
	ExpectedAppealVersion uint64 `json:"expected_appeal_version,omitempty"`
	ReasonCode            string `json:"reason_code"`
	EvidenceRef           string `json:"evidence_ref"`
	PublicReply           string `json:"public_reply"`
}
type GenerationEffect struct {
	FromState       string `json:"from_state"`
	ToState         string `json:"to_state"`
	Version         uint64 `json:"version"`
	RestrictionID   string `json:"restriction_id"`
	AffectedAppeals int    `json:"affected_appeals"`
}
type GenerationPlan struct {
	ID              string           `json:"id"`
	WorkspaceID     string           `json:"workspace_id"`
	Action          string           `json:"action"`
	ExpectedVersion uint64           `json:"expected_version"`
	RestrictionID   string           `json:"restriction_id,omitempty"`
	AppealID        string           `json:"appeal_id,omitempty"`
	PlanDigest      string           `json:"plan_digest"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Effect          GenerationEffect `json:"effect"`
}
type GenerationReceipt struct {
	ID          string           `json:"id"`
	PlanID      string           `json:"plan_id"`
	WorkspaceID string           `json:"workspace_id"`
	Action      string           `json:"action"`
	Effect      GenerationEffect `json:"effect"`
	ExecutedAt  time.Time        `json:"executed_at"`
}
type generationOpenAppeal struct {
	ID      string
	Version uint64
}
type generationPlanSnapshot struct {
	Plan                           GenerationPlan
	Input                          GenerationPlanInput
	DatabaseID, ActorID, SessionID string
	CredentialVersion              int64
	GrantHash                      [32]byte
	OpenAppeals                    []generationOpenAppeal
}

func generationPermission(action string) string {
	switch action {
	case "restrict_future", "lift_restriction":
		return "generation_restrict"
	case "uphold_restriction":
		return "appeal_resolve"
	}
	return ""
}
func validGenerationPlanInput(in GenerationPlanInput) bool {
	if !validOperationsID(in.WorkspaceID, "ws") || generationPermission(in.Action) == "" || in.ExpectedVersion < 1 || in.ExpectedVersion > generationMaxVersion || !validGenerationText(in.PublicReply, true) || !ledgerOpaquePattern.MatchString(in.EvidenceRef) || strings.Contains(in.EvidenceRef, "://") {
		return false
	}
	if in.Action == "lift_restriction" {
		if in.ReasonCode != "review_complete" && in.ReasonCode != "restriction_error" {
			return false
		}
	} else if in.ReasonCode != "manual_review" && in.ReasonCode != "policy_concern" && in.ReasonCode != "abuse_concern" {
		return false
	}
	if in.Action == "restrict_future" {
		return in.RestrictionID == "" && in.AppealID == "" && in.ExpectedAppealVersion == 0
	}
	if !validOperationsID(in.RestrictionID, "grst") {
		return false
	}
	if in.Action == "uphold_restriction" {
		return validOperationsID(in.AppealID, "gapl") && in.ExpectedAppealVersion == 1
	}
	return in.AppealID == "" && in.ExpectedAppealVersion == 0
}
func generationChanged() error {
	return failure(409, "GENERATION_CONTROL_CHANGED", "限制或复核申请已变化，请重新读取并预演")
}
func readGenerationOpenAppealsSQL(ctx context.Context, tx pgx.Tx, workspace, restriction string) ([]generationOpenAppeal, error) {
	items := []generationOpenAppeal{}
	if restriction == "" {
		return items, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,version FROM studio_generation_appeals WHERE workspace_id=$1 AND restriction_id=$2 AND state='OPEN' ORDER BY id LIMIT 129 FOR UPDATE`, workspace, restriction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v generationOpenAppeal
		if err = rows.Scan(&v.ID, &v.Version); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > 128 {
		return nil, failure(409, "GENERATION_APPEAL_LIMIT", "当前复核集合超出安全上限")
	}
	return items, nil
}
func generationEffect(access GenerationAccess, in GenerationPlanInput, restriction string, open []generationOpenAppeal) (GenerationEffect, error) {
	effect := GenerationEffect{FromState: access.State, ToState: access.State, Version: access.Version, RestrictionID: restriction}
	if access.Version != in.ExpectedVersion {
		return effect, generationChanged()
	}
	if in.Action == "restrict_future" {
		if access.State != "OPEN" || access.Restriction != nil {
			return effect, generationChanged()
		}
		if access.Version >= generationMaxVersion-1 {
			return effect, failure(409, "GENERATION_VERSION_LIMIT", "版本不足以为后续解除预留容量")
		}
		effect.ToState = "RESTRICTED"
		effect.Version++
		return effect, nil
	}
	if access.State != "RESTRICTED" || access.Restriction == nil || access.Restriction.ID != in.RestrictionID || restriction != in.RestrictionID {
		return effect, generationChanged()
	}
	if in.Action == "lift_restriction" {
		if access.Version >= generationMaxVersion {
			return effect, failure(409, "GENERATION_VERSION_LIMIT", "限制版本已达上限")
		}
		effect.ToState = "OPEN"
		effect.Version++
		effect.AffectedAppeals = len(open)
		return effect, nil
	}
	for _, v := range open {
		if v.ID == in.AppealID && v.Version == in.ExpectedAppealVersion {
			effect.AffectedAppeals = 1
			return effect, nil
		}
	}
	return effect, generationChanged()
}

// Applies only this domain. It never cancels work, touches billing, alters
// moderation decisions, or publishes a private statement to a project feed.
func applyGenerationControlSQL(ctx context.Context, tx pgx.Tx, p *principal, in GenerationPlanInput, effect GenerationEffect, open []generationOpenAppeal, operationID string) error {
	if in.Action == "restrict_future" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x47454e525354)); err != nil {
			return err
		}
		var own, total int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE workspace_id=$1),count(*) FROM (SELECT workspace_id FROM studio_generation_restrictions LIMIT 100001) r`, in.WorkspaceID).Scan(&own, &total); err != nil {
			return err
		}
		if own >= 10000 || total >= 100000 {
			return failure(429, "GENERATION_RESTRICTION_LIMIT", "限制历史容量已达上限；已有限制仍可解除")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO studio_generation_restrictions(id,workspace_id,reason_code,public_notice,evidence_ref,created_by) VALUES($1,$2,$3,$4,$5,$6)`, effect.RestrictionID, in.WorkspaceID, in.ReasonCode, in.PublicReply, in.EvidenceRef, p.User.ID); err != nil {
			return err
		}
		if in.ExpectedVersion == 1 {
			if _, err := tx.Exec(ctx, `INSERT INTO studio_generation_controls(workspace_id,state,version,active_restriction_id) VALUES($1,'RESTRICTED',$2,$3)`, in.WorkspaceID, effect.Version, effect.RestrictionID); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE studio_generation_controls SET state='RESTRICTED',version=$2,active_restriction_id=$3,updated_at=clock_timestamp() WHERE workspace_id=$1 AND state='OPEN' AND version=$4`, in.WorkspaceID, effect.Version, effect.RestrictionID, in.ExpectedVersion)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return generationChanged()
			}
		}
		return insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: in.WorkspaceID, Action: "generation.restricted", ResourceType: "restriction", ResourceID: effect.RestrictionID, OperationID: operationID, ReasonCode: in.ReasonCode, EvidenceRef: in.EvidenceRef, Version: effect.Version})
	}
	if in.Action == "lift_restriction" {
		tag, err := tx.Exec(ctx, `UPDATE studio_generation_controls SET state='OPEN',version=$3,active_restriction_id=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND active_restriction_id=$2 AND version=$4`, in.WorkspaceID, in.RestrictionID, effect.Version, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return generationChanged()
		}
		tag, err = tx.Exec(ctx, `UPDATE studio_generation_restrictions SET released_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 AND released_at IS NULL`, in.WorkspaceID, in.RestrictionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return generationChanged()
		}
		if err = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: in.WorkspaceID, Action: "generation.lifted", ResourceType: "restriction", ResourceID: in.RestrictionID, OperationID: operationID, ReasonCode: in.ReasonCode, EvidenceRef: in.EvidenceRef, Version: effect.Version}); err != nil {
			return err
		}
	}
	for _, v := range open {
		if in.Action == "uphold_restriction" && v.ID != in.AppealID {
			continue
		}
		outcome, action := "LIFTED", "appeal.lifted"
		if in.Action == "uphold_restriction" {
			outcome, action = "UPHELD", "appeal.upheld"
		}
		tag, err := tx.Exec(ctx, `UPDATE studio_generation_appeals SET state='RESOLVED',outcome=$4,version=2,public_reply=$5,updated_at=clock_timestamp() WHERE workspace_id=$1 AND restriction_id=$2 AND id=$3 AND state='OPEN' AND version=$6`, in.WorkspaceID, in.RestrictionID, v.ID, outcome, in.PublicReply, v.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return generationChanged()
		}
		if err = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: in.WorkspaceID, Action: action, ResourceType: "appeal", ResourceID: v.ID, OperationID: operationID, ReasonCode: in.ReasonCode, EvidenceRef: in.EvidenceRef, Version: 2}); err != nil {
			return err
		}
	}
	return nil
}

func generationPlanCapacitySQL(ctx context.Context, tx pgx.Tx, workspace, action string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x47454e504c4e)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM studio_generation_plans WHERE id IN(SELECT id FROM studio_generation_plans WHERE consumed_at IS NULL AND expires_at<clock_timestamp()-interval '1 day' ORDER BY expires_at,id LIMIT 1000)`); err != nil {
		return err
	}
	if action == "lift_restriction" {
		// One outstanding lift plan per workspace reserves a recovery lane. A new
		// lift preview supersedes the previous unexecuted preview, not a receipt.
		_, err := tx.Exec(ctx, `DELETE FROM studio_generation_plans WHERE workspace_id=$1 AND action='lift_restriction' AND consumed_at IS NULL`, workspace)
		return err
	}
	var own, active, total int
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE workspace_id=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp()),count(*) FILTER(WHERE consumed_at IS NULL AND expires_at>clock_timestamp()),count(*) FROM (SELECT workspace_id,consumed_at,expires_at FROM studio_generation_plans WHERE action<>'lift_restriction' LIMIT 100001) p`, workspace).Scan(&own, &active, &total)
	if err != nil {
		return err
	}
	if own >= 64 || active >= 10000 || total >= 100000 {
		return failure(429, "GENERATION_PLAN_LIMIT", "新限制或维持预演容量已满；解除预演仍可执行")
	}
	return nil
}
func (a *App) previewGenerationControl(p *principal, in GenerationPlanInput) (GenerationPlan, error) {
	var result GenerationPlan
	if err := a.generationPrincipal(p, true); err != nil {
		return result, err
	}
	if !validGenerationPlanInput(in) {
		return result, failure(422, "INVALID_GENERATION_PLAN", "动作、版本、原因、证据或公开答复无效")
	}
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		grant, e := authorizeOperationsSQL(ctx, tx, p, in.WorkspaceID, generationPermission(in.Action))
		if e != nil {
			return e
		}
		if e = lockGenerationWorkspaceSQL(ctx, tx, in.WorkspaceID, true); e != nil {
			return e
		}
		access, e := readGenerationAccessSQL(ctx, tx, in.WorkspaceID)
		if e != nil {
			return e
		}
		open, e := readGenerationOpenAppealsSQL(ctx, tx, in.WorkspaceID, in.RestrictionID)
		if e != nil {
			return e
		}
		restriction := in.RestrictionID
		if in.Action == "restrict_future" {
			restriction = newID("grst")
		}
		effect, e := generationEffect(access, in, restriction, open)
		if e != nil {
			return e
		}
		var now time.Time
		var databaseID string
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp(),storage_id::text FROM studio_storage_identity WHERE singleton`).Scan(&now, &databaseID); e != nil {
			return e
		}
		result = GenerationPlan{ID: newID("gplan"), WorkspaceID: in.WorkspaceID, Action: in.Action, ExpectedVersion: in.ExpectedVersion, RestrictionID: restriction, AppealID: in.AppealID, ExpiresAt: now.Add(5 * time.Minute).UTC(), Effect: effect}
		simulated, e := tx.Begin(ctx)
		if e != nil {
			return e
		}
		actionErr := applyGenerationControlSQL(ctx, simulated, p, in, effect, open, result.ID)
		if actionErr == nil {
			_, actionErr = simulated.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
		}
		rollbackErr := simulated.Rollback(ctx)
		if actionErr != nil {
			return actionErr
		}
		if rollbackErr != nil {
			return rollbackErr
		}
		if e = generationPlanCapacitySQL(ctx, tx, in.WorkspaceID, in.Action); e != nil {
			return e
		}
		snapshot := generationPlanSnapshot{Plan: result, Input: in, DatabaseID: databaseID, ActorID: p.User.ID, SessionID: p.SessionID, CredentialVersion: p.CredentialVersion, GrantHash: payloadHash(grant), OpenAppeals: open}
		digest := payloadHash(snapshot)
		result.PlanDigest = hex.EncodeToString(digest[:])
		snapshot.Plan = result
		blob, _ := json.Marshal(snapshot)
		if _, e = tx.Exec(ctx, `INSERT INTO studio_generation_plans(id,workspace_id,actor_id,action,snapshot,digest,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, result.ID, in.WorkspaceID, p.User.ID, in.Action, blob, result.PlanDigest, result.ExpiresAt); e != nil {
			return e
		}
		if e = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: in.WorkspaceID, Action: "generation.previewed", ResourceType: "restriction", ResourceID: restriction, OperationID: result.ID, ReasonCode: in.ReasonCode, EvidenceRef: in.EvidenceRef, Version: access.Version}); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return GenerationPlan{}, err
	}
	return result, nil
}
func (a *App) executeGenerationControl(p *principal, proof *operationsPasswordProof, workspace, planID, digest string) (GenerationReceipt, error) {
	var result GenerationReceipt
	if err := a.generationPrincipal(p, true); err != nil {
		return result, err
	}
	digestBytes, e := hex.DecodeString(digest)
	if !validOperationsID(workspace, "ws") || !validOperationsID(planID, "gplan") || e != nil || len(digestBytes) != 32 || hex.EncodeToString(digestBytes) != digest {
		return result, failure(422, "INVALID_GENERATION_PLAN", "预演标识或摘要无效")
	}
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "generation_restrict"); e != nil {
			return e
		}
		// Workspace is locked before a plan or appeal in every writer path.
		if e = lockGenerationWorkspaceSQL(ctx, tx, workspace, true); e != nil {
			return e
		}
		var blob []byte
		var storedDigest string
		var consumed *time.Time
		var fresh bool
		e = tx.QueryRow(ctx, `SELECT snapshot,digest,consumed_at,expires_at>clock_timestamp() FROM studio_generation_plans WHERE id=$1 AND workspace_id=$2 AND actor_id=$3 FOR UPDATE`, planID, workspace, p.User.ID).Scan(&blob, &storedDigest, &consumed, &fresh)
		if errors.Is(e, pgx.ErrNoRows) {
			return generationNotFound()
		}
		if e != nil {
			return e
		}
		if consumed != nil {
			return failure(409, "GENERATION_PLAN_CONSUMED", "此预演已执行，请读取原回执")
		}
		if !fresh {
			return failure(409, "GENERATION_PLAN_EXPIRED", "预演已过期，请重新预演")
		}
		if digest != storedDigest {
			return failure(409, "GENERATION_PLAN_CHANGED", "预演摘要不一致")
		}
		var snapshot generationPlanSnapshot
		if e = json.Unmarshal(blob, &snapshot); e != nil {
			return e
		}
		snapshot.Plan.PlanDigest = ""
		computed := payloadHash(snapshot)
		snapshot.Plan.PlanDigest = storedDigest
		if hex.EncodeToString(computed[:]) != storedDigest || snapshot.Plan.ID != planID || snapshot.Input.WorkspaceID != workspace || !validGenerationPlanInput(snapshot.Input) {
			return failure(409, "GENERATION_PLAN_CHANGED", "预演内容与摘要不一致")
		}
		permission := generationPermission(snapshot.Input.Action)
		if e = checkOperationsPermissionSQL(ctx, tx, p, workspace, permission, proof); e != nil {
			return e
		}
		grant, e := authorizeOperationsSQL(ctx, tx, p, workspace, permission)
		if e != nil {
			return e
		}
		var databaseID string
		if e = tx.QueryRow(ctx, `SELECT storage_id::text FROM studio_storage_identity WHERE singleton`).Scan(&databaseID); e != nil {
			return e
		}
		if snapshot.ActorID != p.User.ID || snapshot.SessionID != p.SessionID || snapshot.CredentialVersion != p.CredentialVersion || snapshot.DatabaseID != databaseID || snapshot.GrantHash != payloadHash(grant) {
			return failure(409, "GENERATION_PLAN_CHANGED", "账号、会话、授权或数据库身份已变化，请重新预演")
		}
		access, e := readGenerationAccessSQL(ctx, tx, workspace)
		if e != nil {
			return e
		}
		open, e := readGenerationOpenAppealsSQL(ctx, tx, workspace, snapshot.Input.RestrictionID)
		if e != nil {
			return e
		}
		if payloadHash(open) != payloadHash(snapshot.OpenAppeals) {
			return generationChanged()
		}
		effect, e := generationEffect(access, snapshot.Input, snapshot.Plan.RestrictionID, open)
		if e != nil {
			return e
		}
		if effect != snapshot.Plan.Effect {
			return failure(409, "GENERATION_PLAN_CHANGED", "当前业务效果与预演不同")
		}
		if e = applyGenerationControlSQL(ctx, tx, p, snapshot.Input, effect, open, planID); e != nil {
			return e
		}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		result = GenerationReceipt{ID: newID("grec"), PlanID: planID, WorkspaceID: workspace, Action: snapshot.Input.Action, Effect: effect, ExecutedAt: now}
		receipt, _ := json.Marshal(result)
		if _, e = tx.Exec(ctx, `UPDATE studio_generation_plans SET consumed_at=$2 WHERE id=$1`, planID, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO studio_generation_receipts(id,workspace_id,plan_id,document,created_at) VALUES($1,$2,$3,$4,$5)`, result.ID, workspace, planID, receipt, now); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return GenerationReceipt{}, err
	}
	return result, nil
}
func (a *App) readGenerationReceipt(p *principal, workspace, planID string) (GenerationReceipt, error) {
	var result GenerationReceipt
	if err := a.generationPrincipal(p, true); err != nil {
		return result, err
	}
	if !validOperationsID(workspace, "ws") || !validOperationsID(planID, "gplan") {
		return result, generationNotFound()
	}
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "generation_restrict"); e != nil {
			return e
		}
		var blob []byte
		e = tx.QueryRow(ctx, `SELECT document FROM studio_generation_receipts WHERE workspace_id=$1 AND plan_id=$2`, workspace, planID).Scan(&blob)
		if errors.Is(e, pgx.ErrNoRows) {
			return generationNotFound()
		}
		if e != nil {
			return e
		}
		if e = json.Unmarshal(blob, &result); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return result, err
}
