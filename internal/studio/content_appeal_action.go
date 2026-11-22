package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ContentAppealPlanInput struct {
	WorkspaceID     string `json:"workspace_id"`
	Action          string `json:"action"`
	ExpectedVersion uint64 `json:"expected_version"`
	EvidenceRef     string `json:"evidence_ref"`
	PublicReply     string `json:"public_reply"`
}
type ContentAppealPlan struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspace_id"`
	AppealID        string    `json:"appeal_id"`
	Action          string    `json:"action"`
	Outcome         string    `json:"outcome"`
	ExpectedVersion uint64    `json:"expected_version"`
	PlanDigest      string    `json:"plan_digest"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type ContentAppealReceipt struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	AppealID    string    `json:"appeal_id"`
	PlanID      string    `json:"plan_id"`
	Action      string    `json:"action"`
	Outcome     string    `json:"outcome"`
	Version     uint64    `json:"version"`
	ExecutedAt  time.Time `json:"executed_at"`
}
type contentAppealSnapshot struct {
	Plan                                          ContentAppealPlan
	Input                                         ContentAppealPlanInput
	ReceiptID, Actor, Session, Database, Evidence string
	Credential                                    int64
	Grant                                         [32]byte
	Hold                                          appealReservation
}

func (v contentAppealSnapshot) digest() string {
	v.Plan.PlanDigest = ""
	d := payloadHash(v)
	return hex.EncodeToString(d[:])
}
func contentAppealDecision(action string) (outcome, audit string) {
	switch action {
	case "uphold":
		return "UPHELD", "content_appeal.upheld"
	case "record_opinion":
		return "OPINION_RECORDED", "content_appeal.opinion"
	case "refer_finance":
		return "FINANCE_REFERRED", "content_appeal.referred"
	}
	return "", ""
}
func validContentPlan(in ContentAppealPlanInput) bool {
	outcome, _ := contentAppealDecision(in.Action)
	return validOperationsID(in.WorkspaceID, "ws") && in.ExpectedVersion == 1 && outcome != "" && ledgerOpaquePattern.MatchString(in.EvidenceRef) && !strings.Contains(in.EvidenceRef, "://") && validGenerationText(in.PublicReply, true)
}
func contentReviewHold(ctx context.Context, tx pgx.Tx, p *principal, x ContentAppeal, in ContentAppealPlanInput) (appealReservation, error) {
	var hold appealReservation
	if !validContentPlan(in) {
		return hold, failure(422, "INVALID_CONTENT_APPEAL_PLAN", "请核对原版本、复核决定、证据引用与公开答复")
	}
	if x.applicant == p.User.ID {
		return hold, failure(403, "CONTENT_APPEAL_INDEPENDENT_REVIEW_REQUIRED", "本人的申诉须由另一名有权限的审核员处理")
	}
	if x.State != "OPEN" || x.Version != in.ExpectedVersion {
		return hold, contentAppealChanged()
	}
	current, err := sourceContentAppealSQL(ctx, tx, x.WorkspaceID, x.applicant, x.ContentAppealInput)
	if err != nil {
		return hold, err
	}
	if current.evidence != x.evidence {
		return hold, contentAppealChanged()
	}
	if in.Action == "refer_finance" {
		hold, err = contentAppealHoldSQL(ctx, tx, x)
		if err != nil {
			return hold, err
		}
		if hold.ID == "" || hold.Remaining <= 0 {
			return hold, failure(409, "CONTENT_APPEAL_FINANCE_UNAVAILABLE", "原资源没有可交财务核实的剩余 REVIEW 冻结")
		}
	}
	return hold, nil
}
func contentDecisionAudit(ctx context.Context, tx pgx.Tx, p *principal, x ContentAppeal, in ContentAppealPlanInput, plan, action string, version uint64) error {
	// Shared audit readers receive an opaque digest. The reviewer-supplied
	// reference and statements remain in their respective private records.
	d := sha256.Sum256([]byte(in.EvidenceRef))
	return insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: x.WorkspaceID, Action: action, ResourceType: "appeal", ResourceID: x.ID, OperationID: plan, ReasonCode: in.Action, EvidenceRef: hex.EncodeToString(d[:]), Version: version})
}
func applyContentDecisionSQL(ctx context.Context, tx pgx.Tx, p *principal, x ContentAppeal, z contentAppealSnapshot) error {
	outcome, action := contentAppealDecision(z.Input.Action)
	var tagCount int64
	if outcome == "FINANCE_REFERRED" {
		tag, err := tx.Exec(ctx, `UPDATE studio_content_appeals SET state='RESOLVED',outcome=$2,version=version+1,public_reply=$3,updated_at=clock_timestamp(),reservation_id=$4,reservation_version=$5,remaining_micros=$6 WHERE id=$1 AND version=$7 AND state='OPEN'`, x.ID, outcome, z.Input.PublicReply, z.Hold.ID, z.Hold.Version, z.Hold.Remaining, z.Input.ExpectedVersion)
		if err != nil {
			return err
		}
		tagCount = tag.RowsAffected()
	} else {
		tag, err := tx.Exec(ctx, `UPDATE studio_content_appeals SET state='RESOLVED',outcome=$2,version=version+1,public_reply=$3,updated_at=clock_timestamp() WHERE id=$1 AND version=$4 AND state='OPEN'`, x.ID, outcome, z.Input.PublicReply, z.Input.ExpectedVersion)
		if err != nil {
			return err
		}
		tagCount = tag.RowsAffected()
	}
	if tagCount != 1 {
		return contentAppealChanged()
	}
	return contentDecisionAudit(ctx, tx, p, x, z.Input, z.Plan.ID, action, x.Version+1)
}
func (a *App) previewContentAppeal(p *principal, id string, in ContentAppealPlanInput) (ContentAppealPlan, error) {
	var result ContentAppealPlan
	if !validOperationsID(id, "capl") || !validContentPlan(in) {
		return result, failure(422, "INVALID_CONTENT_APPEAL_PLAN", "请核对原版本、复核决定、证据引用与公开答复")
	}
	err := a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		grant, e := authorizeOperationsSQL(ctx, tx, p, in.WorkspaceID, "content_appeal_review")
		if e != nil {
			return e
		}
		x, e := contentAppealSQL(ctx, tx, in.WorkspaceID, "", id, "", true)
		if e != nil {
			return e
		}
		hold, e := contentReviewHold(ctx, tx, p, x, in)
		if e != nil {
			return e
		}
		var now time.Time
		var database string
		if e = tx.QueryRow(ctx, "SELECT clock_timestamp(),storage_id::text FROM studio_storage_identity WHERE singleton").Scan(&now, &database); e != nil {
			return e
		}
		outcome, _ := contentAppealDecision(in.Action)
		result = ContentAppealPlan{ID: newID("caplan"), WorkspaceID: in.WorkspaceID, AppealID: id, Action: in.Action, Outcome: outcome, ExpectedVersion: x.Version, ExpiresAt: now.Add(5 * time.Minute).UTC()}
		z := contentAppealSnapshot{Plan: result, Input: in, ReceiptID: newID("carec"), Actor: p.User.ID, Session: p.SessionID, Database: database, Evidence: x.evidence, Credential: p.CredentialVersion, Grant: payloadHash(grant), Hold: hold}
		sim, e := tx.Begin(ctx)
		if e != nil {
			return e
		}
		e = applyContentDecisionSQL(ctx, sim, p, x, z)
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
		result.PlanDigest = z.digest()
		z.Plan = result
		// Only one outstanding preview per appeal. Consumed plans and their
		// immutable receipts are never removed.
		if _, e = tx.Exec(ctx, "DELETE FROM studio_content_appeal_plans WHERE appeal_id=$1 AND consumed_at IS NULL", id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO studio_content_appeal_plans(id,appeal_id,workspace_id,actor_id,snapshot,digest,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", result.ID, id, in.WorkspaceID, p.User.ID, z, result.PlanDigest, result.ExpiresAt); e != nil {
			return e
		}
		return contentDecisionAudit(ctx, tx, p, x, in, result.ID, "content_appeal.previewed", x.Version)
	})
	if err != nil {
		return ContentAppealPlan{}, err
	}
	return result, nil
}
func (a *App) executeContentAppeal(p *principal, workspace, id, planID, digest string, proof *operationsPasswordProof) (ContentAppealReceipt, error) {
	var result ContentAppealReceipt
	if !validOperationsID(id, "capl") || !validOperationsID(planID, "caplan") || !mediaContentHash.MatchString(digest) {
		return result, failure(422, "INVALID_CONTENT_APPEAL_PLAN", "预演身份或摘要无效")
	}
	err := a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		if e := checkOperationsPermissionSQL(ctx, tx, p, workspace, "content_appeal_review", proof); e != nil {
			return e
		}
		grant, e := operationsGrantSQL(ctx, tx, p, workspace, "content_appeal_review")
		if e != nil {
			return e
		}
		x, e := contentAppealSQL(ctx, tx, workspace, "", id, "", true)
		if e != nil {
			return e
		}
		var z contentAppealSnapshot
		var saved, db string
		var consumed *time.Time
		var fresh bool
		e = tx.QueryRow(ctx, `SELECT snapshot,digest,consumed_at,expires_at>clock_timestamp(),(SELECT storage_id::text FROM studio_storage_identity WHERE singleton) FROM studio_content_appeal_plans WHERE id=$1 AND appeal_id=$2 AND workspace_id=$3 AND actor_id=$4 FOR UPDATE`, planID, id, workspace, p.User.ID).Scan(&z, &saved, &consumed, &fresh, &db)
		if e != nil {
			return appealReadError(e)
		}
		if consumed != nil {
			return failure(409, "CONTENT_APPEAL_PLAN_CONSUMED", "此预演已执行，请查询原回执")
		}
		if !fresh {
			return failure(409, "CONTENT_APPEAL_PLAN_EXPIRED", "预演已过期，请重新预演")
		}
		if saved != digest || saved != z.digest() || z.Plan.PlanDigest != saved || z.Plan.AppealID != id || z.Plan.ID != planID || z.Input.WorkspaceID != workspace || z.Actor != p.User.ID || z.Session != p.SessionID || z.Credential != p.CredentialVersion || z.Database != db || z.Grant != payloadHash(grant) || z.Evidence != x.evidence {
			return contentAppealChanged()
		}
		hold, e := contentReviewHold(ctx, tx, p, x, z.Input)
		if e != nil {
			return e
		}
		if z.Hold != hold {
			return contentAppealChanged()
		}
		if e = applyContentDecisionSQL(ctx, tx, p, x, z); e != nil {
			return e
		}
		result = ContentAppealReceipt{ID: z.ReceiptID, AppealID: id, WorkspaceID: workspace, PlanID: planID, Action: z.Plan.Action, Outcome: z.Plan.Outcome, Version: x.Version + 1}
		// A wait on an appeal, credit reservation or audit lock can cross the
		// plan's deadline. Fence the final effect with the live database clock.
		e = tx.QueryRow(ctx, "UPDATE studio_content_appeal_plans SET consumed_at=clock_timestamp() WHERE id=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp() AND $2::timestamptz>clock_timestamp() RETURNING consumed_at", planID, z.Plan.ExpiresAt).Scan(&result.ExecutedAt)
		if e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return failure(409, "CONTENT_APPEAL_PLAN_EXPIRED", "预演已过期，请重新预演")
			}
			return e
		}
		_, e = tx.Exec(ctx, "INSERT INTO studio_content_appeal_receipts(id,workspace_id,appeal_id,plan_id,document) VALUES($1,$2,$3,$4,$5)", result.ID, workspace, id, planID, result)
		return e
	})
	if err != nil {
		return ContentAppealReceipt{}, err
	}
	return result, nil
}
func (a *App) contentAppealPlanReceipt(p *principal, workspace, id, plan string) (out ContentAppealReceipt, err error) {
	err = a.contentAppealTx(p, true, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := authorizeOperationsSQL(ctx, tx, p, workspace, "content_appeal_review"); e != nil {
			return e
		}
		return appealReadError(tx.QueryRow(ctx, "SELECT document FROM studio_content_appeal_receipts WHERE workspace_id=$1 AND appeal_id=$2 AND plan_id=$3", workspace, id, plan).Scan(&out))
	})
	return
}
