package studio

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type CreditRiskPolicy struct {
	WorkspaceID   string `json:"workspace_id"`
	Version       uint64 `json:"version"`
	Enabled       bool   `json:"enabled"`
	MaxHeldMicros int64  `json:"max_held_micros,string"`
	MaxPerMinute  int    `json:"max_per_minute"`
}
type RiskEvidence struct {
	QuoteID       string
	UserID        string
	WorkspaceID   string
	ProjectID     string
	RequestDigest string
	PriceVersion  string
	PolicyVersion uint64
	AmountMicros  int64
	ExpiresAt     time.Time
	Rule          string
	Reasons       []string
}

func riskActorID(p *principal) string {
	if p == nil || p.User == nil {
		return ""
	}
	return p.User.ID
}
func readCreditRiskPolicySQL(ctx context.Context, tx pgx.Tx, workspace string) (v CreditRiskPolicy, err error) {
	v.WorkspaceID = workspace
	err = tx.QueryRow(ctx, `SELECT version,enabled,max_held_micros,max_per_minute FROM studio_credit_risk_policies WHERE workspace_id=$1 ORDER BY version DESC LIMIT 1`, workspace).Scan(&v.Version, &v.Enabled, &v.MaxHeldMicros, &v.MaxPerMinute)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}

// Called with the workspace and wallet locked. The balance includes unsettled
// REVIEW amounts. Count real reservations, even if they have settled or been
// released since acceptance. The index probe stops at the configured threshold.
func evaluateCreditRiskSQL(ctx context.Context, tx pgx.Tx, workspace string, held, requested int64, at time.Time) (CreditRiskPolicy, []string, error) {
	reasons := []string{}
	policy, e := readCreditRiskPolicySQL(ctx, tx, workspace)
	if e != nil || !policy.Enabled {
		return policy, reasons, e
	}
	if requested > policy.MaxHeldMicros-held {
		reasons = append(reasons, "HELD_LIMIT")
	}
	window := at.UTC().Truncate(time.Minute)
	var atLimit bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_ledger_reservations WHERE workspace_id=$1 AND created_at>=$2 AND created_at<$3 LIMIT 1 OFFSET $4)`, workspace, window, window.Add(time.Minute), policy.MaxPerMinute-1).Scan(&atLimit)
	if e != nil {
		return policy, nil, e
	}
	if atLimit {
		reasons = append(reasons, "RATE_LIMIT")
	}
	return policy, reasons, nil
}
func creditRiskAdmissionSQL(ctx context.Context, tx pgx.Tx, q ledgerQuoteRow, actor string, held int64, at time.Time) (string, error) {
	policy, reasons, e := evaluateCreditRiskSQL(ctx, tx, q.WorkspaceID, held, q.ReserveMicros, at)
	if e != nil || !policy.Enabled {
		return "", e
	}
	// Only this authenticated applicant may consume the permission. A reviewed
	// quote admitted under an unchanged policy also records its true reservation
	// if the original limit has subsequently cleared.
	if actor != "" && q.OwnerUserID == actor {
		var id string
		e = tx.QueryRow(ctx, `SELECT p.review_id FROM studio_credit_risk_permits p JOIN studio_credit_risk_reviews r ON r.id=p.review_id WHERE p.quote_id=$1 AND p.policy_version=$2 AND p.applicant_id=$3 AND p.workspace_id=$4 AND p.project_id=$5 AND p.request_hash=$6 AND p.price_version=$7 AND p.reserve_micros=$8 AND p.expires_at=$9 AND p.reservation_id IS NULL AND r.state='APPROVED' AND $10::text[] <@ r.reason_codes FOR UPDATE OF p`, q.ID, policy.Version, actor, q.WorkspaceID, q.ProjectID, q.RequestHash[:], q.PriceVersion, q.ReserveMicros, q.ExpiresAt, reasons).Scan(&id)
		if e == nil {
			return id, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return "", e
		}
	}
	if len(reasons) == 0 {
		return "", nil
	}
	if reasons[0] == "HELD_LIMIT" {
		return "", failure(429, "CREDIT_RISK_HELD_LIMIT", "本次报价将超过工作区冻结上限，尚未受理，可按原报价申请复核")
	}
	return "", failure(429, "CREDIT_RISK_RATE_LIMIT", "工作区本分钟的收费受理次数已达上限，尚未受理，可按原报价申请复核")
}

// Authentication and reviewer authorization are validated by the caller.
// Owner, quote, price and amount are read from the same rows as admission.
func riskQuoteEvidenceSQL(ctx context.Context, tx pgx.Tx, owner, workspace, project, quoteID string) (v RiskEvidence, err error) {
	if err = lockGenerationWorkspaceSQL(ctx, tx, workspace, false); err != nil {
		return
	}
	q, e := scanLedgerQuote(tx.QueryRow(ctx, ledgerQuoteByID+` AND owner_user_id=$2 AND workspace_id=$3 AND project_id=$4 FOR UPDATE`, quoteID, owner, workspace, project))
	if errors.Is(e, pgx.ErrNoRows) {
		return v, failure(404, "NOT_FOUND", "未找到本人在此项目的原报价")
	}
	if e != nil {
		return v, e
	}
	if q.ReservationID != "" {
		return v, failure(409, "QUOTE_USED", "原报价已被受理，请核对原业务收据")
	}
	held, at, e := lockedSpendableQuoteSQL(ctx, tx, q)
	if e != nil {
		return v, e
	}
	policy, reasons, e := evaluateCreditRiskSQL(ctx, tx, workspace, held, q.ReserveMicros, at)
	if e != nil {
		return v, e
	}
	v = RiskEvidence{QuoteID: q.ID, UserID: owner, WorkspaceID: workspace, ProjectID: project, RequestDigest: hex.EncodeToString(q.RequestHash[:]), PriceVersion: q.PriceVersion, PolicyVersion: policy.Version, AmountMicros: q.ReserveMicros, ExpiresAt: q.ExpiresAt.UTC(), Reasons: reasons}
	if len(reasons) > 0 {
		v.Rule = reasons[0]
	}
	return
}

// The wallet row serializes every reserve and settlement. Read the clock and
// balances in a separate statement after acquiring it; creating an empty
// wallet would turn the owner's eligibility GET into a mutation.
func lockedSpendableQuoteSQL(ctx context.Context, tx pgx.Tx, q ledgerQuoteRow) (held int64, at time.Time, err error) {
	var workspace string
	err = tx.QueryRow(ctx, `SELECT workspace_id FROM studio_ledger_wallets WHERE workspace_id=$1 FOR UPDATE`, q.WorkspaceID).Scan(&workspace)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return
	}
	var available, charged int64
	if workspace == "" {
		err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at)
	} else {
		err = tx.QueryRow(ctx, `SELECT a.balance_micros,h.balance_micros,w.charged_micros,clock_timestamp() FROM studio_ledger_wallets w JOIN studio_ledger_accounts a ON a.id='available:'||w.workspace_id JOIN studio_ledger_accounts h ON h.id='reserved:'||w.workspace_id WHERE w.workspace_id=$1`, workspace).Scan(&available, &held, &charged, &at)
	}
	if err != nil {
		return
	}
	if !at.Before(q.ExpiresAt) {
		return held, at, failure(409, "QUOTE_EXPIRED", "报价已过期，请重新获取报价")
	}
	if available < q.ReserveMicros {
		return held, at, failure(402, "INSUFFICIENT_CREDITS", "可用额度不足，尚未受理生成请求")
	}
	// Refunds restore spendable credits without rewriting cumulative charges.
	if held > MaxLedgerMicros-charged || q.ReserveMicros > MaxLedgerMicros-charged-held {
		return held, at, failure(409, "LEDGER_CAPACITY", "累计结算额度及现有冻结已占满账本容量，尚未受理生成请求")
	}
	return
}
func issueCreditRiskPermitSQL(ctx context.Context, tx pgx.Tx, reviewID string, ev RiskEvidence) error {
	tag, e := tx.Exec(ctx, `INSERT INTO studio_credit_risk_permits(review_id,workspace_id,project_id,applicant_id,quote_id,policy_version,request_hash,price_version,reserve_micros,expires_at)
 SELECT $1,$2,$3,$4,$5,$6,decode($7,'hex'),$8,$9,$10::timestamptz WHERE $10::timestamptz>clock_timestamp()`, reviewID, ev.WorkspaceID, ev.ProjectID, ev.UserID, ev.QuoteID, ev.PolicyVersion, ev.RequestDigest, ev.PriceVersion, ev.AmountMicros, ev.ExpiresAt)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return failure(409, "QUOTE_EXPIRED", "原报价已过期，未授予许可")
	}
	return nil
}
