package studio

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type AccountPlanInput struct {
	Action                string `json:"action"`
	ExpectedVersion       uint64 `json:"expected_version"`
	RestrictionID         string `json:"restriction_id,omitempty"`
	AppealID              string `json:"appeal_id,omitempty"`
	ExpectedAppealVersion uint64 `json:"expected_appeal_version,omitempty"`
	ReasonCode            string `json:"reason_code"`
	EvidenceRef           string `json:"evidence_ref"`
	PublicReply           string `json:"public_reply"`
}
type AccountPlan struct {
	ID              string           `json:"id"`
	TargetUserID    string           `json:"target_user_id"`
	Action          string           `json:"action"`
	ExpectedVersion uint64           `json:"expected_version"`
	RestrictionID   string           `json:"restriction_id"`
	AppealID        string           `json:"appeal_id,omitempty"`
	PlanDigest      string           `json:"plan_digest"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Effect          GenerationEffect `json:"effect"`
}
type AccountReceipt struct {
	ID           string           `json:"id"`
	PlanID       string           `json:"plan_id"`
	TargetUserID string           `json:"target_user_id"`
	Action       string           `json:"action"`
	Effect       GenerationEffect `json:"effect"`
	ExecutedAt   time.Time        `json:"executed_at"`
}
type accountControlPlanSnapshot struct {
	Plan                              AccountPlan
	Input                             AccountPlanInput
	Grant                             accountTargetGrant
	Database, Actor, Session, Receipt string
	Credential, TargetCredential      int64
	OpenAppeal                        generationOpenAppeal
}

func (z accountControlPlanSnapshot) digest() string {
	z.Plan.PlanDigest = ""
	h := payloadHash(z)
	return hex.EncodeToString(h[:])
}
func validAccountPlan(in AccountPlanInput) bool {
	if in.ExpectedVersion < 1 || in.ExpectedVersion > generationMaxVersion || !validOperationsOpaque(in.EvidenceRef) || !validGenerationText(in.PublicReply, true) {
		return false
	}
	switch in.Action {
	case "suspend", "uphold":
		if in.ReasonCode != "manual_review" && in.ReasonCode != "policy_concern" && in.ReasonCode != "abuse_concern" {
			return false
		}
	case "lift":
		if in.ReasonCode != "review_complete" && in.ReasonCode != "restriction_error" {
			return false
		}
	default:
		return false
	}
	if in.Action == "suspend" {
		return in.RestrictionID == "" && in.AppealID == "" && in.ExpectedAppealVersion == 0
	}
	if !validOperationsID(in.RestrictionID, "arst") {
		return false
	}
	if in.Action == "uphold" {
		return validOperationsID(in.AppealID, "aapl") && in.ExpectedAppealVersion == 1
	}
	return in.AppealID == "" && in.ExpectedAppealVersion == 0
}

// The schema binds the applicant to the target and permits one application
// per round. A new appeal between preview and execution changes this snapshot.
func accountControlEffectSQL(ctx context.Context, tx pgx.Tx, target, rid string, in AccountPlanInput) (effect GenerationEffect, open generationOpenAppeal, err error) {
	access, e := readAccountAccessSQL(ctx, tx, target)
	if e != nil {
		return effect, open, e
	}
	if access.Version != in.ExpectedVersion {
		return effect, open, accountChanged()
	}
	effect = GenerationEffect{FromState: access.State, ToState: access.State, Version: access.Version, RestrictionID: rid}
	if in.Action == "suspend" {
		if access.State != "OPEN" || access.Version >= generationMaxVersion-1 {
			return effect, open, accountChanged()
		}
		effect.ToState = "SUSPENDED"
		effect.Version++
		return
	}
	if access.State != "SUSPENDED" || access.Restriction == nil || access.Restriction.ID != in.RestrictionID || rid != in.RestrictionID {
		return effect, open, accountChanged()
	}
	e = tx.QueryRow(ctx, `SELECT id,version FROM studio_account_appeals WHERE target_user_id=$1 AND restriction_id=$2 AND state='OPEN' FOR UPDATE`, target, rid).Scan(&open.ID, &open.Version)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return effect, open, e
	}
	if open.ID != "" {
		effect.AffectedAppeals = 1
	}
	if in.Action == "uphold" {
		if open.ID != in.AppealID || open.Version != in.ExpectedAppealVersion {
			return effect, open, accountChanged()
		}
	} else {
		effect.ToState = "OPEN"
		effect.Version++
	}
	return
}
func applyAccountControlSQL(ctx context.Context, tx pgx.Tx, z accountControlPlanSnapshot) error {
	target, in, effect := z.Plan.TargetUserID, z.Input, z.Plan.Effect
	audit := "appeal.upheld"
	resource, version := in.AppealID, uint64(2)
	switch in.Action {
	case "suspend":
		if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x414343525354)); e != nil {
			return e
		}
		var own, total int
		if e := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE target_user_id=$1),count(*) FROM (SELECT target_user_id FROM studio_account_restrictions LIMIT 100001) r`, target).Scan(&own, &total); e != nil {
			return e
		}
		if own >= 1024 || total >= 100000 {
			return failure(429, "ACCOUNT_RESTRICTION_LIMIT", "账户限制记录容量已满；已有轮次仍可解除")
		}
		if _, e := tx.Exec(ctx, `INSERT INTO studio_account_restrictions(id,target_user_id,reason_code,public_notice,evidence_ref,created_by) VALUES($1,$2,$3,$4,$5,$6)`, effect.RestrictionID, target, in.ReasonCode, in.PublicReply, in.EvidenceRef, z.Actor); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `INSERT INTO studio_account_controls(user_id,state,version,active_restriction_id) VALUES($1,'SUSPENDED',2,$2) ON CONFLICT(user_id) DO UPDATE SET state='SUSPENDED',version=studio_account_controls.version+1,active_restriction_id=EXCLUDED.active_restriction_id,updated_at=clock_timestamp() WHERE studio_account_controls.state='OPEN' AND studio_account_controls.version=$3`, target, effect.RestrictionID, in.ExpectedVersion)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return accountChanged()
		}
		audit, resource, version = "account.suspended", effect.RestrictionID, effect.Version
	case "lift":
		tag, e := tx.Exec(ctx, `UPDATE studio_account_restrictions SET released_at=clock_timestamp() WHERE id=$1 AND target_user_id=$2 AND released_at IS NULL`, in.RestrictionID, target)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return accountChanged()
		}
		tag, e = tx.Exec(ctx, `UPDATE studio_account_controls SET state='OPEN',version=version+1,active_restriction_id=NULL,updated_at=clock_timestamp() WHERE user_id=$1 AND state='SUSPENDED' AND active_restriction_id=$2 AND version=$3`, target, in.RestrictionID, in.ExpectedVersion)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return accountChanged()
		}
		audit, resource, version = "account.lifted", effect.RestrictionID, effect.Version
	}
	if z.OpenAppeal.ID != "" {
		outcome := "UPHELD"
		if in.Action == "lift" {
			outcome = "LIFTED"
		}
		tag, e := tx.Exec(ctx, `UPDATE studio_account_appeals SET state='RESOLVED',version=version+1,outcome=$4,public_reply=$5,updated_at=clock_timestamp() WHERE target_user_id=$1 AND restriction_id=$2 AND id=$3 AND state='OPEN' AND version=$6`, target, effect.RestrictionID, z.OpenAppeal.ID, outcome, in.PublicReply, z.OpenAppeal.Version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return accountChanged()
		}
	}
	if in.Action == "lift" || in.Action == "suspend" {
		if _, e := tx.Exec(ctx, `DELETE FROM studio_sessions WHERE user_id=$1`, target); e != nil {
			return e
		}
	}
	return accountAuditSQL(ctx, tx, z.Actor, target, audit, resource, version)
}
func (a *App) previewAccountControl(p *principal, target string, in AccountPlanInput) (result AccountPlan, err error) {
	if !validAccountPlan(in) {
		return result, failure(422, "INVALID_ACCOUNT_PLAN", "请核对动作、轮次、证据与公开答复")
	}
	err = a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		grant, _, e := authorizeAccountTargetSQL(ctx, tx, p, target, true)
		if e != nil {
			return e
		}
		rid := in.RestrictionID
		if in.Action == "suspend" {
			rid = newID("arst")
		}
		effect, open, e := accountControlEffectSQL(ctx, tx, target, rid, in)
		if e != nil {
			return e
		}
		z := accountControlPlanSnapshot{Actor: p.User.ID, Session: p.SessionID, Credential: p.CredentialVersion, Grant: grant, Input: in, OpenAppeal: open, Receipt: newID("arec")}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp(),(SELECT storage_id::text FROM studio_storage_identity WHERE singleton),credential_version FROM studio_users WHERE id=$1`, target).Scan(&now, &z.Database, &z.TargetCredential); e != nil {
			return e
		}
		z.Plan = AccountPlan{ID: newID("aplan"), TargetUserID: target, Action: in.Action, ExpectedVersion: in.ExpectedVersion, RestrictionID: rid, AppealID: in.AppealID, ExpiresAt: now.Add(5 * time.Minute).UTC(), Effect: effect}
		simulated, e := tx.Begin(ctx)
		if e != nil {
			return e
		}
		e = applyAccountControlSQL(ctx, simulated, z)
		if e == nil {
			_, e = simulated.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
		}
		rollback := simulated.Rollback(ctx)
		if e != nil {
			return e
		}
		if rollback != nil {
			return rollback
		}
		// Keep one pending preview per intent and target; an unconsumed lift
		// is always replaceable. Each consumed plan maps to one bounded transition.
		if _, e = tx.Exec(ctx, `DELETE FROM studio_account_plans WHERE target_user_id=$1 AND action=$2 AND consumed_at IS NULL`, target, in.Action); e != nil {
			return e
		}
		z.Plan.PlanDigest = z.digest()
		if _, e = tx.Exec(ctx, `INSERT INTO studio_account_plans(id,target_user_id,actor_id,action,snapshot,digest,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, z.Plan.ID, target, z.Actor, in.Action, z, z.Plan.PlanDigest, z.Plan.ExpiresAt); e != nil {
			return e
		}
		result = z.Plan
		return accountAuditSQL(ctx, tx, p.User.ID, target, "plan.previewed", z.Plan.ID, in.ExpectedVersion)
	})
	if err != nil {
		return AccountPlan{}, err
	}
	return
}
func checkAccountProof(p *principal, current passwordSnapshot, proof *operationsPasswordProof) error {
	if proof == nil || proof.snapshot.UserID != p.User.ID || proof.snapshot.Version != current.Version || !bytes.Equal(proof.snapshot.Hash, current.Hash) || proof.sessionID != p.SessionID || !bytes.Equal(proof.sessionHash, p.SessionHash) || time.Since(proof.verifiedAt) < 0 || time.Since(proof.verifiedAt) > 5*time.Minute {
		return failure(403, "OPERATIONS_REAUTH_REQUIRED", "需要重新核验当前密码")
	}
	return nil
}
func (a *App) executeAccountControl(p *principal, target, plan, digest string, proof *operationsPasswordProof) (v AccountReceipt, err error) {
	if !validOperationsID(plan, "aplan") || !mediaContentHash.MatchString(digest) {
		return v, failure(422, "INVALID_ACCOUNT_PLAN", "预演身份或摘要无效")
	}
	err = a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		g, password, e := authorizeAccountTargetSQL(ctx, tx, p, target, true)
		if e != nil {
			return e
		}
		if e = checkAccountProof(p, password, proof); e != nil {
			return e
		}
		var z accountControlPlanSnapshot
		var used *time.Time
		var fresh bool
		var saved, database string
		var targetCredential int64
		e = tx.QueryRow(ctx, `SELECT p.snapshot,p.digest,p.consumed_at,p.expires_at>clock_timestamp(),u.credential_version,(SELECT storage_id::text FROM studio_storage_identity WHERE singleton) FROM studio_account_plans p JOIN studio_users u ON u.id=p.target_user_id WHERE p.id=$1 AND p.target_user_id=$2 AND p.actor_id=$3 FOR UPDATE OF p`, plan, target, p.User.ID).Scan(&z, &saved, &used, &fresh, &targetCredential, &database)
		if e != nil {
			return accountReadError(e)
		}
		if used != nil {
			return failure(409, "ACCOUNT_PLAN_CONSUMED", "此计划已执行，可读取原收据")
		}
		if !fresh {
			return failure(409, "ACCOUNT_PLAN_EXPIRED", "预演已过期，请重新预演")
		}
		if z.digest() != saved || saved != digest || saved != z.Plan.PlanDigest || z.Plan.ID != plan || z.Plan.TargetUserID != target || z.Actor != p.User.ID || z.Credential != p.CredentialVersion || z.Session != p.SessionID || z.Grant != g || z.TargetCredential != targetCredential || z.Database != database {
			return accountChanged()
		}
		effect, open, e := accountControlEffectSQL(ctx, tx, target, z.Plan.RestrictionID, z.Input)
		if e != nil {
			return e
		}
		if open != z.OpenAppeal || effect != z.Plan.Effect {
			return accountChanged()
		}
		if e = applyAccountControlSQL(ctx, tx, z); e != nil {
			return e
		}
		v = AccountReceipt{ID: z.Receipt, PlanID: plan, TargetUserID: target, Action: z.Input.Action, Effect: effect}
		if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&v.ExecutedAt); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO studio_account_receipts(id,target_user_id,plan_id,document) VALUES($1,$2,$3,$4)`, v.ID, target, plan, v); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
			return e
		}
		// A session deletion, receipt insertion or deferred consistency check
		// may wait. Check the live authorization after those writes, while their
		// effects are still reversible, with plan consumption as the final write.
		password, e = operationsSessionSQL(ctx, tx, p)
		if e != nil {
			return e
		}
		if e = checkAccountProof(p, password, proof); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE studio_account_plans SET consumed_at=$3 WHERE id=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp() AND $2::timestamptz>clock_timestamp()`, plan, z.Plan.ExpiresAt, v.ExecutedAt)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return failure(409, "ACCOUNT_PLAN_EXPIRED", "预演已过期，请重新预演")
		}
		return nil
	})
	if err != nil {
		return AccountReceipt{}, err
	}
	return
}
func (a *App) accountControlReceipt(p *principal, target, plan string) (v AccountReceipt, err error) {
	err = a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, e := authorizeAccountTargetSQL(ctx, tx, p, target, true); e != nil {
			return e
		}
		return accountReadError(tx.QueryRow(ctx, `SELECT r.document FROM studio_account_receipts r JOIN studio_account_plans p ON p.id=r.plan_id WHERE r.target_user_id=$1 AND r.plan_id=$2 AND p.actor_id=$3`, target, plan, p.User.ID).Scan(&v))
	})
	return
}
