package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type AccountGovernanceGrant struct {
	ID             string `json:"id"`
	OperatorUserID string `json:"operator_user_id"`
	TargetUserID   string `json:"target_user_id"`
	Active         bool   `json:"active"`
	Version        uint64 `json:"version"`
}

type AccountGrantMutation struct {
	Action      string                 `json:"action"`
	Reason      string                 `json:"reason"`
	EvidenceRef string                 `json:"evidence_ref"`
	ID          string                 `json:"operation_id"`
	Grant       AccountGovernanceGrant `json:"grant"`
	Changed     bool                   `json:"changed"`
	Replayed    bool                   `json:"replayed"`
	PlanDigest  string                 `json:"plan_digest"`
}

// The caller owns the existing offline control-writer lock, format fence and
// serializable transaction. A dry-run performs the real writes and rolls back.
func administerAccountGrant(ctx context.Context, tx pgx.Tx, dbID string, c OperationsAdminConfig, report OperationsAdminReport) (OperationsAdminReport, error) {
	request := struct{ Action, Operator, Target, Key, Reason, Evidence string }{c.Action, c.UserID, c.TargetUserID, c.OperationKey, c.Reason, c.EvidenceRef}
	hash := payloadHash(request)
	if c.Action != "inspect" {
		var saved, blob []byte
		err := tx.QueryRow(ctx, "SELECT request_hash,response FROM studio_account_grant_operations WHERE operation_key=$1", c.OperationKey).Scan(&saved, &blob)
		if err == nil {
			if !bytes.Equal(saved, hash[:]) {
				return report, errors.New("operation key already belongs to another account governance request")
			}
			var prior AccountGrantMutation
			if json.Unmarshal(blob, &prior) != nil {
				return report, errors.New("account grant receipt unreadable")
			}
			if c.Apply && c.PlanDigest != prior.PlanDigest {
				return report, errors.New("account grant receipt digest mismatch")
			}
			prior.Replayed = true
			report.TargetResult = &prior
			report.PlanDigest = prior.PlanDigest
			report.Applied = c.Apply
			return report, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return report, errors.New("account grant receipt unavailable")
		}
	}
	var operatorVersion, targetVersion int64
	err := tx.QueryRow(ctx, `SELECT u.credential_version,t.credential_version FROM studio_users u CROSS JOIN studio_users t WHERE u.id=$1 AND t.id=$2`, c.UserID, c.TargetUserID).Scan(&operatorVersion, &targetVersion)
	if err != nil {
		return report, errors.New("account governance requires two existing users")
	}
	var grant AccountGovernanceGrant
	err = tx.QueryRow(ctx, `SELECT id,operator_user_id,target_user_id,active,version FROM studio_account_governance_grants WHERE operator_user_id=$1 AND target_user_id=$2 FOR UPDATE`, c.UserID, c.TargetUserID).Scan(&grant.ID, &grant.OperatorUserID, &grant.TargetUserID, &grant.Active, &grant.Version)
	if err == nil {
		copy := grant
		report.TargetGrant = &copy
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return report, errors.New("unable to read account governance grant")
	}
	if c.Action == "inspect" {
		return report, nil
	}
	var actorCount, targetCount int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM studio_account_governance_grants WHERE operator_user_id=$1 AND active),(SELECT count(*) FROM studio_account_governance_grants WHERE target_user_id=$2 AND active)`, c.UserID, c.TargetUserID).Scan(&actorCount, &targetCount); err != nil {
		return report, errors.New("account grant capacity unavailable")
	}
	plan, _ := json.Marshal(struct {
		DatabaseID                     string
		Request                        any
		Original                       *AccountGovernanceGrant
		OperatorVersion, TargetVersion int64
		OperatorCount, TargetCount     int
	}{dbID, request, report.TargetGrant, operatorVersion, targetVersion, actorCount, targetCount})
	sum := sha256.Sum256(plan)
	report.PlanDigest = hex.EncodeToString(sum[:])
	if c.Apply && c.PlanDigest != report.PlanDigest {
		return report, errors.New("account governance plan changed; inspect a new dry-run before applying")
	}
	desired := c.Action == "grant"
	if report.TargetGrant == nil && !desired {
		return report, errors.New("account governance grant does not exist")
	}
	result := AccountGrantMutation{Action: c.Action, Reason: c.Reason, EvidenceRef: c.EvidenceRef, ID: newID("agop"), PlanDigest: report.PlanDigest, Changed: desired != grant.Active}
	if result.Changed {
		if desired && (actorCount >= 64 || targetCount >= 64) {
			return report, errors.New("active account grant capacity exceeded")
		}
		if report.TargetGrant == nil {
			grant = AccountGovernanceGrant{ID: newID("agnt"), OperatorUserID: c.UserID, TargetUserID: c.TargetUserID, Active: true, Version: 1}
			_, err = tx.Exec(ctx, "INSERT INTO studio_account_governance_grants(id,operator_user_id,target_user_id,active,version) VALUES($1,$2,$3,true,1)", grant.ID, c.UserID, c.TargetUserID)
		} else {
			maxVersion := uint64(9007199254740991)
			if desired {
				maxVersion--
			}
			if grant.Version >= maxVersion {
				return report, errors.New("account grant revision capacity exhausted")
			}
			grant.Version++
			grant.Active = desired
			_, err = tx.Exec(ctx, "UPDATE studio_account_governance_grants SET active=$2,version=$3,updated_at=clock_timestamp() WHERE id=$1", grant.ID, desired, grant.Version)
		}
		if err != nil {
			return report, errors.New("account grant mutation rejected")
		}
	}
	result.Grant = grant
	action := "grant.created"
	if report.TargetGrant != nil && desired {
		action = "grant.granted"
	}
	if !desired {
		action = "grant.revoked"
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_account_governance_audit(id,actor_user_id,target_user_id,action,resource_id,version) VALUES($1,NULL,$2,$3,$4,$5)`, newID("aga"), c.TargetUserID, action, grant.ID, grant.Version)
	if err != nil {
		return report, errors.New("account grant audit unavailable")
	}
	data, _ := json.Marshal(result)
	_, err = tx.Exec(ctx, `INSERT INTO studio_account_grant_operations(operation_key,request_hash,operation_id,response) VALUES($1,$2,$3,$4)`, c.OperationKey, hash[:], result.ID, data)
	if err != nil {
		return report, errors.New("account grant receipt unavailable")
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return report, errors.New("account grant integrity check failed")
	}
	report.TargetResult = &result
	if c.Apply {
		if err = tx.Commit(ctx); err != nil {
			return report, errors.New("account grant commit outcome unknown; check original operation key and plan digest")
		}
		report.Applied = true
	}
	return report, nil
}
