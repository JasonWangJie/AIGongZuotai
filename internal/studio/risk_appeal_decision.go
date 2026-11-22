package studio

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type RiskReviewPlan = ContentAppealPlan
type RiskReviewReceipt = ContentAppealReceipt
type riskPlanSnapshot struct {
	Plan                                          RiskReviewPlan
	Input                                         ContentAppealPlanInput
	Database, Actor, Session, ReceiptID, Evidence string
	Credential                                    int64
	Grant                                         [32]byte
}

func (s riskPlanSnapshot) digest() string {
	s.Plan.PlanDigest = ""
	d := payloadHash(s)
	return hex.EncodeToString(d[:])
}
func validRiskPlan(in ContentAppealPlanInput) bool {
	return (in.Action == "approve" || in.Action == "deny") && in.ExpectedVersion == 1 && validOperationsID(in.WorkspaceID, "ws") && validGenerationText(in.PublicReply, true) && validOperationsOpaque(in.EvidenceRef) && !strings.Contains(in.EvidenceRef, "://")
}
func riskOutcome(action string) string {
	if action == "approve" {
		return "APPROVED"
	}
	return "DENIED"
}

// Read the immutable owner/quote identity before taking any review lock.
// The acceptance path uses quote -> wallet -> permit as well.
func lockRiskDecisionSQL(ctx context.Context, tx pgx.Tx, p *principal, ws, id string, in ContentAppealPlanInput) (RiskAppeal, RiskEvidence, error) {
	head, e := riskReviewSQL(ctx, tx, ws, "", id, "", false)
	var ev RiskEvidence
	if e != nil {
		return head, ev, e
	}
	if head.owner == p.User.ID {
		return head, ev, failure(403, "INDEPENDENT_RISK_REVIEW_REQUIRED", "自己的额度申请需要另一名获授权人员审议")
	}
	if head.State != "OPEN" || head.Version != in.ExpectedVersion {
		return head, ev, riskChanged()
	}
	if in.Action == "approve" {
		ev, e = riskQuoteEvidenceSQL(ctx, tx, head.owner, ws, head.ProjectID, head.QuoteID)
		if e != nil {
			return head, ev, e
		}
		if ev.Rule == "" || riskEvidenceDigest(ev) != head.digest {
			return head, ev, riskChanged()
		}
	}
	x, e := riskReviewSQL(ctx, tx, ws, head.ProjectID, id, head.owner, true)
	if e == nil && (x.State != "OPEN" || x.Version != in.ExpectedVersion || x.digest != head.digest) {
		e = riskChanged()
	}
	return x, ev, e
}
func applyRiskDecisionSQL(ctx context.Context, tx pgx.Tx, p *principal, x RiskAppeal, in ContentAppealPlanInput, plan string, ev RiskEvidence) error {
	outcome := riskOutcome(in.Action)
	ct, e := tx.Exec(ctx, `UPDATE studio_credit_risk_reviews SET state=$2,public_reply=$3,version=version+1,updated_at=clock_timestamp() WHERE id=$1 AND state='OPEN' AND version=$4`, x.ID, outcome, in.PublicReply, in.ExpectedVersion)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return riskChanged()
	}
	action := "risk_review.denied"
	if in.Action == "approve" {
		if e = issueCreditRiskPermitSQL(ctx, tx, x.ID, ev); e != nil {
			return e
		}
		action = "risk_review.approved"
	}
	return insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: x.WorkspaceID, Action: action, ResourceType: "appeal", ResourceID: x.ID, OperationID: plan, ReasonCode: in.Action, EvidenceRef: in.EvidenceRef, Version: x.Version + 1})
}
func (a *App) previewRiskReview(p *principal, id string, in ContentAppealPlanInput) (out RiskReviewPlan, err error) {
	if !validOperationsID(id, "rskrev") || !validRiskPlan(in) {
		return out, failure(422, "INVALID_RISK_REVIEW_PLAN", "请核对权限范围、决定、版本、证据引用与公开答复")
	}
	err = a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		g, e := authorizeOperationsSQL(ctx, tx, p, in.WorkspaceID, "risk_review")
		if e != nil {
			return e
		}
		x, ev, e := lockRiskDecisionSQL(ctx, tx, p, in.WorkspaceID, id, in)
		if e != nil {
			return e
		}
		var now time.Time
		var db string
		if e = tx.QueryRow(ctx, "SELECT clock_timestamp(),storage_id::text FROM studio_storage_identity WHERE singleton").Scan(&now, &db); e != nil {
			return e
		}
		expires := now.Add(5 * time.Minute)
		if in.Action == "approve" && ev.ExpiresAt.Before(expires) {
			expires = ev.ExpiresAt
		}
		out = RiskReviewPlan{ID: newID("rskpl"), WorkspaceID: x.WorkspaceID, AppealID: id, Action: in.Action, Outcome: riskOutcome(in.Action), ExpectedVersion: x.Version, ExpiresAt: expires.UTC()}
		s := riskPlanSnapshot{Plan: out, Input: in, Database: db, Actor: p.User.ID, Session: p.SessionID, Credential: p.CredentialVersion, Grant: payloadHash(g), Evidence: x.digest, ReceiptID: newID("rskrc")}
		sim, e := tx.Begin(ctx)
		if e != nil {
			return e
		}
		e = applyRiskDecisionSQL(ctx, sim, p, x, in, out.ID, ev)
		if e == nil {
			_, e = sim.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
		}
		rb := sim.Rollback(ctx)
		if e != nil {
			return e
		}
		if rb != nil {
			return rb
		}
		out.PlanDigest = s.digest()
		s.Plan = out
		if _, e = tx.Exec(ctx, "DELETE FROM studio_credit_risk_plans WHERE review_id=$1 AND consumed_at IS NULL", id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO studio_credit_risk_plans(id,review_id,workspace_id,actor_id,snapshot,digest,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", out.ID, id, x.WorkspaceID, p.User.ID, s, out.PlanDigest, out.ExpiresAt); e != nil {
			return e
		}
		return insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: x.WorkspaceID, Action: "risk_review.previewed", ResourceType: "appeal", ResourceID: id, OperationID: out.ID, ReasonCode: in.Action, EvidenceRef: in.EvidenceRef, Version: x.Version})
	})
	return
}
func (a *App) executeRiskReview(p *principal, ws, id, plan, digest string, proof *operationsPasswordProof) (out RiskReviewReceipt, err error) {
	if !validOperationsID(id, "rskrev") || !validOperationsID(plan, "rskpl") || !mediaContentHash.MatchString(digest) {
		return out, failure(422, "INVALID_RISK_REVIEW_PLAN", "原计划身份或摘要无效")
	}
	err = a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		if e := checkOperationsPermissionSQL(ctx, tx, p, ws, "risk_review", proof); e != nil {
			return e
		}
		g, e := operationsGrantSQL(ctx, tx, p, ws, "risk_review")
		if e != nil {
			return e
		}
		var s riskPlanSnapshot
		var stored, db string
		var consumed *time.Time
		var fresh bool
		// The immutable preview identifies the action before acquiring business
		// locks. A replacement preview is detected by the final conditional write.
		e = tx.QueryRow(ctx, `SELECT snapshot,digest,consumed_at,expires_at>clock_timestamp(),(SELECT storage_id::text FROM studio_storage_identity WHERE singleton)
		FROM studio_credit_risk_plans WHERE id=$1 AND workspace_id=$2 AND review_id=$3 AND actor_id=$4`, plan, ws, id, p.User.ID).Scan(&s, &stored, &consumed, &fresh, &db)
		if e != nil {
			return riskReadError(e)
		}
		if consumed != nil {
			return failure(409, "RISK_PLAN_CONSUMED", "这项计划已确认，请读取原收据")
		}
		if !fresh {
			return failure(409, "RISK_PLAN_EXPIRED", "原计划已到期，请读取原申请")
		}
		if s.digest() != stored || s.Plan.PlanDigest != stored || digest != stored || s.Plan.ID != plan || s.Plan.AppealID != id || s.Input.WorkspaceID != ws || s.Actor != p.User.ID || s.Session != p.SessionID || s.Credential != p.CredentialVersion || s.Grant != payloadHash(g) || s.Database != db {
			return riskChanged()
		}
		x, ev, e := lockRiskDecisionSQL(ctx, tx, p, ws, id, s.Input)
		if e != nil {
			return e
		}
		if x.digest != s.Evidence {
			return riskChanged()
		}
		if e = applyRiskDecisionSQL(ctx, tx, p, x, s.Input, plan, ev); e != nil {
			return e
		}
		out = RiskReviewReceipt{ID: s.ReceiptID, WorkspaceID: ws, AppealID: id, PlanID: plan, Action: s.Plan.Action, Outcome: s.Plan.Outcome, Version: x.Version + 1}
		if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&out.ExecutedAt); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO studio_credit_risk_receipts(id,review_id,workspace_id,plan_id,document) VALUES($1,$2,$3,$4,$5)", out.ID, id, ws, plan, out); e != nil {
			return e
		}
		// Fence both deadlines after business, permit, receipt and audit SQL.
		var committedAt time.Time
		e = tx.QueryRow(ctx, `UPDATE studio_credit_risk_plans SET consumed_at=clock_timestamp() WHERE id=$1 AND digest=$2 AND consumed_at IS NULL AND expires_at>clock_timestamp() AND $3::timestamptz>clock_timestamp() RETURNING consumed_at`, plan, stored, s.Plan.ExpiresAt).Scan(&committedAt)
		if errors.Is(e, pgx.ErrNoRows) {
			return riskChanged()
		}
		return e
	})
	return
}
func (a *App) riskPlanReceipt(p *principal, ws, id, plan string) (out RiskReviewReceipt, err error) {
	if !validOperationsID(id, "rskrev") || !validOperationsID(plan, "rskpl") {
		return out, riskMissing()
	}
	err = a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := authorizeOperationsSQL(ctx, tx, p, ws, "risk_review"); e != nil {
			return e
		}
		return riskReadError(tx.QueryRow(ctx, "SELECT document FROM studio_credit_risk_receipts WHERE workspace_id=$1 AND review_id=$2 AND plan_id=$3", ws, id, plan).Scan(&out))
	})
	return
}
