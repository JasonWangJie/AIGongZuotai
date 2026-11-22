package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type RiskAppealInput struct {
	ProjectID  string `json:"project_id"`
	QuoteID    string `json:"quote_id"`
	ReasonCode string `json:"reason_code"`
	Statement  string `json:"statement,omitempty"`
}
type RiskEligibility struct {
	WorkspaceID     string    `json:"workspace_id"`
	ProjectID       string    `json:"project_id"`
	QuoteID         string    `json:"quote_id"`
	AllowedToAppeal bool      `json:"allowed_to_appeal"`
	ReasonCodes     []string  `json:"reason_codes"`
	PolicyVersion   uint64    `json:"policy_version"`
	AmountMicros    int64     `json:"amount_micros,string"`
	ExpiresAt       time.Time `json:"expires_at"`
	AppealID        string    `json:"appeal_id,omitempty"`
}
type RiskAppeal struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	RiskAppealInput
	ReasonCodes    []string  `json:"reason_codes"`
	PolicyVersion  uint64    `json:"policy_version"`
	AmountMicros   int64     `json:"amount_micros,string"`
	ExpiresAt      time.Time `json:"expires_at"`
	State          string    `json:"state"`
	Version        uint64    `json:"version"`
	PublicReply    string    `json:"public_reply,omitempty"`
	ExemptionState string    `json:"exemption_state,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	owner, digest  string
}

func riskMissing() error { return failure(404, "NOT_FOUND", "报价、风控复核或收据不存在") }
func riskReadError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return riskMissing()
	}
	return e
}
func riskChanged() error {
	return failure(409, "RISK_REVIEW_CHANGED", "原报价、限制策略或复核状态已变化，请刷新核对")
}
func riskEvidenceDigest(ev RiskEvidence) string {
	ev.ExpiresAt = ev.ExpiresAt.UTC()
	d := payloadHash(ev)
	return hex.EncodeToString(d[:])
}
func riskOwnerSQL(ctx context.Context, tx pgx.Tx, p *principal, project string) error {
	if p == nil || p.User == nil || !validOperationsID(project, "prj") {
		return riskMissing()
	}
	if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
		return e
	}
	var exists bool
	if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_projects WHERE workspace_id=$1 AND id=$2)", p.Workspace.ID, project).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return riskMissing()
	}
	return nil
}
func (a *App) riskEligibility(p *principal, project, quote string) (v RiskEligibility, err error) {
	if !validOperationsID(quote, "quo") {
		return v, riskMissing()
	}
	err = a.contentAppealTx(p, false, func(ctx context.Context, tx pgx.Tx) error {
		if e := riskOwnerSQL(ctx, tx, p, project); e != nil {
			return e
		}
		ev, e := riskQuoteEvidenceSQL(ctx, tx, p.User.ID, p.Workspace.ID, project, quote)
		if e != nil {
			return e
		}
		v = RiskEligibility{WorkspaceID: p.Workspace.ID, ProjectID: project, QuoteID: quote, ReasonCodes: append([]string{}, ev.Reasons...), PolicyVersion: ev.PolicyVersion, AmountMicros: ev.AmountMicros, ExpiresAt: ev.ExpiresAt}
		if e = tx.QueryRow(ctx, "SELECT id FROM studio_credit_risk_reviews WHERE applicant_id=$1 AND quote_id=$2 AND policy_version=$3", p.User.ID, quote, ev.PolicyVersion).Scan(&v.AppealID); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		v.AllowedToAppeal = ev.Rule != "" && v.AppealID == ""
		return nil
	})
	return
}

const riskReviewFields = `r.id,r.workspace_id,r.project_id,r.applicant_id,r.quote_id,r.reason_code,r.reason_codes,r.policy_version,r.reserve_micros,r.expires_at,r.state,r.version,r.evidence_digest,r.created_at,r.updated_at,
	CASE WHEN pm.reservation_id IS NOT NULL THEN 'CONSUMED' WHEN pm.review_id IS NULL THEN '' WHEN pm.expires_at<=clock_timestamp() THEN 'EXPIRED' WHEN q.reservation_id IS NOT NULL OR pm.policy_version<>(SELECT version FROM studio_credit_risk_policies WHERE workspace_id=r.workspace_id ORDER BY version DESC LIMIT 1) THEN 'INACTIVE' ELSE 'AVAILABLE' END`
const riskReviewTables = ` FROM studio_credit_risk_reviews r JOIN studio_ledger_quotes q ON q.id=r.quote_id LEFT JOIN studio_credit_risk_permits pm ON pm.review_id=r.id `

func scanRiskAppeal(row libraryScanner) (RiskAppeal, error) {
	var v RiskAppeal
	e := row.Scan(&v.ID, &v.WorkspaceID, &v.ProjectID, &v.owner, &v.QuoteID, &v.ReasonCode, &v.ReasonCodes, &v.PolicyVersion, &v.AmountMicros, &v.ExpiresAt, &v.State, &v.Version, &v.digest, &v.CreatedAt, &v.UpdatedAt, &v.ExemptionState, &v.Statement, &v.PublicReply)
	return v, riskReadError(e)
}
func riskReviewSQL(ctx context.Context, tx pgx.Tx, ws, project, id, user string, lock bool) (RiskAppeal, error) {
	if !validOperationsID(id, "rskrev") {
		return RiskAppeal{}, riskMissing()
	}
	sql := "SELECT " + riskReviewFields + ",r.statement,r.public_reply" + riskReviewTables + "WHERE r.id=$1 AND r.workspace_id=$2 AND ($3='' OR r.project_id=$3) AND ($4='' OR r.applicant_id=$4)"
	if lock {
		sql += " FOR UPDATE OF r"
	}
	return scanRiskAppeal(tx.QueryRow(ctx, sql, id, ws, project, user))
}
func riskKeySQL(ctx context.Context, tx pgx.Tx, p *principal, project, key string) (id string, hash []byte, err error) {
	k := sha256.Sum256([]byte(key))
	err = tx.QueryRow(ctx, `SELECT r.id,k.request_hash FROM studio_credit_risk_review_keys k JOIN studio_credit_risk_reviews r ON r.id=k.review_id WHERE k.applicant_id=$1 AND k.key_hash=$2 AND r.workspace_id=$3 AND r.project_id=$4`, p.User.ID, k[:], p.Workspace.ID, project).Scan(&id, &hash)
	return
}
func (a *App) createRiskAppeal(p *principal, in RiskAppealInput, key string) (RiskAppeal, bool, error) {
	var result RiskAppeal
	var replay bool
	if !validOperationsID(in.QuoteID, "quo") || !workflowKey.MatchString(key) || (in.ReasonCode != "request_review" && in.ReasonCode != "limit_error" && in.ReasonCode != "billing_concern") || !validGenerationText(in.Statement, false) {
		return result, false, failure(422, "INVALID_RISK_REVIEW", "请填写有效原报价、申请理由与最多1024字节的陈述")
	}
	err := a.contentAppealTx(p, false, func(ctx context.Context, tx pgx.Tx) error {
		if e := riskOwnerSQL(ctx, tx, p, in.ProjectID); e != nil {
			return e
		}
		hash := payloadHash(in)
		k := sha256.Sum256([]byte(key))
		// The operation key belongs to this actor even when used in a different project.
		var priorID string
		var old []byte
		e := tx.QueryRow(ctx, "SELECT review_id,request_hash FROM studio_credit_risk_review_keys WHERE applicant_id=$1 AND key_hash=$2", p.User.ID, k[:]).Scan(&priorID, &old)
		if e == nil {
			if !bytes.Equal(hash[:], old) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原键绑定其他复核申请")
			}
			result, e = riskReviewSQL(ctx, tx, p.Workspace.ID, in.ProjectID, priorID, p.User.ID, false)
			replay = true
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		ev, e := riskQuoteEvidenceSQL(ctx, tx, p.User.ID, p.Workspace.ID, in.ProjectID, in.QuoteID)
		if e != nil {
			return e
		}
		if ev.Rule == "" {
			return failure(409, "RISK_REVIEW_NOT_NEEDED", "该报价当前未触发权益风控规则")
		}
		raw, e := hex.DecodeString(ev.RequestDigest)
		if e != nil || len(raw) != 32 {
			return errors.New("invalid stored quote digest")
		}
		if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x435249534b524556)); e != nil {
			return e
		}
		var existing bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_credit_risk_reviews WHERE quote_id=$1 AND policy_version=$2)", in.QuoteID, ev.PolicyVersion).Scan(&existing); e != nil {
			return e
		}
		if existing {
			return failure(409, "RISK_REVIEW_EXISTS", "这份原报价已有复核申请，请查看原申请")
		}
		var own, total int
		if e = tx.QueryRow(ctx, "SELECT count(*) FILTER(WHERE workspace_id=$1),count(*) FROM (SELECT workspace_id FROM studio_credit_risk_reviews LIMIT 100001) b", ev.WorkspaceID).Scan(&own, &total); e != nil {
			return e
		}
		if own >= 10000 || total >= 100000 {
			return failure(429, "RISK_REVIEW_CAPACITY", "风控复核申请存储已达上限")
		}
		id := newID("rskrev")
		ct, e := tx.Exec(ctx, `INSERT INTO studio_credit_risk_reviews(id,workspace_id,project_id,applicant_id,quote_id,policy_version,request_hash,price_version,reserve_micros,expires_at,rule,reason_codes,evidence_digest,reason_code,statement)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15 WHERE $10::timestamptz>clock_timestamp() ON CONFLICT(quote_id,policy_version) DO NOTHING`, id, ev.WorkspaceID, ev.ProjectID, ev.UserID, ev.QuoteID, ev.PolicyVersion, raw, ev.PriceVersion, ev.AmountMicros, ev.ExpiresAt, ev.Rule, ev.Reasons, riskEvidenceDigest(ev), in.ReasonCode, in.Statement)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			return riskChanged()
		}
		if _, e = tx.Exec(ctx, "INSERT INTO studio_credit_risk_review_keys(applicant_id,key_hash,request_hash,review_id) VALUES($1,$2,$3,$4)", p.User.ID, k[:], hash[:], id); e != nil {
			return e
		}
		if e = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: p.Workspace.ID, Action: "risk_review.created", ResourceType: "appeal", ResourceID: id, ReasonCode: in.ReasonCode, Version: 1}); e != nil {
			return e
		}
		result, e = riskReviewSQL(ctx, tx, p.Workspace.ID, in.ProjectID, id, p.User.ID, false)
		return e
	})
	return result, replay, err
}
func (a *App) readRiskAppeal(p *principal, ws, project, id, key string, ops bool) (out RiskAppeal, err error) {
	err = a.contentAppealTx(p, ops, func(ctx context.Context, tx pgx.Tx) error {
		owner := p.User.ID
		if ops {
			if _, e := authorizeOperationsSQL(ctx, tx, p, ws, "risk_review"); e != nil {
				return e
			}
			owner = ""
		} else if e := riskOwnerSQL(ctx, tx, p, project); e != nil {
			return e
		}
		var e error
		if key != "" {
			id, _, e = riskKeySQL(ctx, tx, p, project, key)
			if e != nil {
				return riskReadError(e)
			}
		}
		out, e = riskReviewSQL(ctx, tx, ws, project, id, owner, false)
		return e
	})
	return
}
