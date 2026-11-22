package studio

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type CreditRiskPolicyLimits struct {
	MaxHeldMicros int64 `json:"max_held_micros,string"`
	MaxPerMinute  int   `json:"max_per_minute"`
}

type CreditRiskPolicyMutation struct {
	ID         string           `json:"operation_id"`
	PlanDigest string           `json:"plan_digest"`
	Before     CreditRiskPolicy `json:"before"`
	After      CreditRiskPolicy `json:"after"`
	Reason     string           `json:"reason_code"`
	Evidence   string           `json:"evidence_ref"`
	Replayed   bool             `json:"replayed"`
}

func validateCreditRiskPolicyChange(c OperationsAdminConfig) error {
	if !validOperationsID(c.WorkspaceID, "ws") || c.UserID != "" || c.TargetUserID != "" || c.Role != "" {
		return errors.New("risk policy requires a single canonical workspace")
	}
	limits := *c.RiskPolicy
	switch c.Action {
	case "inspect":
		if limits != (CreditRiskPolicyLimits{}) || c.Apply || c.PlanDigest != "" || c.OperationKey != "" || c.Reason != "" || c.EvidenceRef != "" {
			return errors.New("inspect accepts only the workspace")
		}
		return nil
	case "disable":
		if limits != (CreditRiskPolicyLimits{}) {
			return errors.New("disable accepts no thresholds")
		}
	case "set":
		if limits.MaxHeldMicros < 1 || limits.MaxHeldMicros > MaxLedgerMicros || limits.MaxPerMinute < 1 || limits.MaxPerMinute > 1000 {
			return errors.New("set requires positive bounded hold and frequency limits")
		}
	default:
		return errors.New("risk policy action must be inspect, set or disable")
	}
	if !validOperationsOpaque(c.OperationKey) || !validOperationsOpaque(c.Reason) || !validOperationsOpaque(c.EvidenceRef) {
		return errors.New("risk policy changes require an opaque operation key, reason and evidence")
	}
	if c.Apply && !operationsPlanDigestPattern.MatchString(c.PlanDigest) || !c.Apply && c.PlanDigest != "" {
		return errors.New("supply the exact dry-run digest only when applying")
	}
	return nil
}

// AdministerOperations holds the sole control-writer lock and has checked the
// complete schema and storage identity. Its deferred rollback covers dry-runs.
func administerCreditRiskPolicy(ctx context.Context, tx pgx.Tx, identity string, c OperationsAdminConfig, report OperationsAdminReport) (OperationsAdminReport, error) {
	request := struct {
		Workspace, Action, Key, Reason, Evidence string
		Limits                                   CreditRiskPolicyLimits
	}{c.WorkspaceID, c.Action, c.OperationKey, c.Reason, c.EvidenceRef, *c.RiskPolicy}
	hash := payloadHash(request)
	if c.Action != "inspect" {
		var stored, document []byte
		err := tx.QueryRow(ctx, "SELECT request_hash,response FROM studio_credit_risk_policy_operations WHERE operation_key=$1", c.OperationKey).Scan(&stored, &document)
		if err == nil {
			var original CreditRiskPolicyMutation
			if !bytes.Equal(stored, hash[:]) || json.Unmarshal(document, &original) != nil || c.Apply && original.PlanDigest != c.PlanDigest {
				return report, errors.New("original risk policy receipt differs from the requested operation or digest")
			}
			original.Replayed = true
			report.PolicyChange, report.PlanDigest, report.Applied = &original, original.PlanDigest, c.Apply
			return report, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return report, errors.New("cannot read original risk policy receipt")
		}
	}
	var workspace string
	if err := tx.QueryRow(ctx, "SELECT id FROM studio_workspaces WHERE id=$1 FOR UPDATE", c.WorkspaceID).Scan(&workspace); err != nil {
		return report, errors.New("risk policy workspace unavailable")
	}
	before, err := readCreditRiskPolicySQL(ctx, tx, workspace)
	if err != nil {
		return report, errors.New("cannot read current risk policy")
	}
	report.Policy = &before
	if c.Action == "inspect" {
		return report, nil
	}
	digest := payloadHash(struct {
		Identity string
		Request  [32]byte
		Before   CreditRiskPolicy
	}{identity, hash, before})
	report.PlanDigest = hex.EncodeToString(digest[:])
	if c.Apply && report.PlanDigest != c.PlanDigest {
		return report, errors.New("risk policy changed; review a new dry-run")
	}
	after := before
	after.Enabled = c.Action == "set"
	after.MaxHeldMicros, after.MaxPerMinute = c.RiskPolicy.MaxHeldMicros, c.RiskPolicy.MaxPerMinute
	if after != before {
		// Reserve the last representable revision to disable an active policy.
		if before.Version >= 9007199254740991 || after.Enabled && before.Version >= 9007199254740990 {
			return report, errors.New("risk policy revision limit reached")
		}
		after.Version++
		if _, err := tx.Exec(ctx, "INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES($1,$2,$3,$4,$5)", c.WorkspaceID, after.Version, after.Enabled, after.MaxHeldMicros, after.MaxPerMinute); err != nil {
			return report, errors.New("risk policy revision rejected")
		}
	}
	result := CreditRiskPolicyMutation{ID: newID("rpol"), PlanDigest: report.PlanDigest, Before: before, After: after, Reason: c.Reason, Evidence: c.EvidenceRef}
	if _, err := tx.Exec(ctx, "INSERT INTO studio_credit_risk_policy_operations(operation_key,request_hash,operation_id,response) VALUES($1,$2,$3,$4)", c.OperationKey, hash[:], result.ID, result); err != nil {
		return report, errors.New("risk policy receipt rejected")
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return report, errors.New("risk policy integrity check failed")
	}
	if c.Apply {
		if err := tx.Commit(ctx); err != nil {
			return report, errors.New("risk policy commit outcome unknown; check the original operation key and digest")
		}
		report.Applied = true
	}
	report.PolicyChange = &result
	return report, nil
}
