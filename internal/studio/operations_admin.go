package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// OperationsAdminConfig is an offline-only surface. It does not migrate, load
// .env, start workers or grant access through any HTTP endpoint.
type OperationsAdminConfig struct {
	DatabaseURL  string
	Action       string
	WorkspaceID  string
	UserID       string
	Role         string
	OperationKey string
	Reason       string
	EvidenceRef  string
	Apply        bool
	PlanDigest   string
}

type OperationsRoleGrant struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
	Version     uint64 `json:"version"`
}

type OperationsRoleMutation struct {
	OperationID string              `json:"operation_id"`
	Grant       OperationsRoleGrant `json:"grant"`
	Changed     bool                `json:"changed"`
	Replayed    bool                `json:"replayed"`
	PlanDigest  string              `json:"plan_digest"`
}

type OperationsAdminReport struct {
	Action        string                  `json:"action"`
	SchemaVersion int                     `json:"schema_version"`
	Grants        []OperationsRoleGrant   `json:"grants"`
	Result        *OperationsRoleMutation `json:"result,omitempty"`
	PlanDigest    string                  `json:"plan_digest,omitempty"`
	Applied       bool                    `json:"applied"`
}

type operationsRoleRequest struct {
	Action       string `json:"action"`
	WorkspaceID  string `json:"workspace_id"`
	UserID       string `json:"user_id"`
	Role         string `json:"role"`
	OperationKey string `json:"operation_key"`
	Reason       string `json:"reason"`
	EvidenceRef  string `json:"evidence_ref"`
}

var operationsPlanDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validOperationsOpaque(s string) bool {
	return ledgerOpaquePattern.MatchString(s) && !strings.Contains(s, "://")
}

func validateOperationsAdmin(c OperationsAdminConfig) error {
	if c.Action != "inspect" && c.Action != "grant" && c.Action != "revoke" {
		return errors.New("action must be inspect, grant or revoke")
	}
	if !validOperationsID(c.WorkspaceID, "ws") || !validOperationsID(c.UserID, "usr") {
		return errors.New("operations administration requires canonical workspace and user IDs")
	}
	if c.Action == "inspect" {
		if c.Apply || c.Role != "" || c.OperationKey != "" || c.Reason != "" || c.EvidenceRef != "" || c.PlanDigest != "" {
			return errors.New("inspect accepts only workspace and user")
		}
		return nil
	}
	if len(operationsRolePermissions(c.Role)) == 0 {
		return errors.New("role must be observer, operator or finance")
	}
	if !validOperationsOpaque(c.OperationKey) || !validOperationsOpaque(c.Reason) || !validOperationsOpaque(c.EvidenceRef) {
		return errors.New("role changes require bounded opaque operation-key, reason and evidence; no URLs or private content")
	}
	if c.Apply && !operationsPlanDigestPattern.MatchString(c.PlanDigest) {
		return errors.New("apply requires the exact dry-run plan digest")
	}
	if !c.Apply && c.PlanDigest != "" {
		return errors.New("plan-digest is only valid with apply")
	}
	return nil
}

func AdministerOperations(ctx context.Context, c OperationsAdminConfig) (OperationsAdminReport, error) {
	report := OperationsAdminReport{Action: c.Action, SchemaVersion: 15, Grants: []OperationsRoleGrant{}}
	if err := validateOperationsAdmin(c); err != nil {
		return report, err
	}
	if c.DatabaseURL == "" {
		return report, errors.New("operations administration requires DATABASE_URL")
	}
	conn, err := pgx.Connect(ctx, c.DatabaseURL)
	if err != nil {
		return report, errors.New("operations database connection unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
	}()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(0x53545544494F)).Scan(&locked); err != nil || !locked {
		return report, errors.New("stop the workbench before operations administration; writer lock unavailable")
	}
	var versions []int32
	if err = conn.QueryRow(ctx, "SELECT array_agg(version ORDER BY version) FROM studio_schema_versions").Scan(&versions); err != nil || len(versions) != 15 {
		return report, errors.New("operations administration requires exactly schema versions 001..015; it does not migrate")
	}
	for i, v := range versions {
		if int(v) != i+1 {
			return report, errors.New("operations database schema is incompatible")
		}
	}
	identity, err := storageOwnerID(ctx, conn)
	if err != nil {
		return report, errors.New("operations database identity unavailable")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return report, errors.New("operations transaction unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	request := operationsRoleRequest{c.Action, c.WorkspaceID, c.UserID, c.Role, c.OperationKey, c.Reason, c.EvidenceRef}
	raw, _ := json.Marshal(request)
	requestHash := sha256.Sum256(raw)
	if c.Action != "inspect" {
		var priorHash, priorReceipt []byte
		err = tx.QueryRow(ctx, "SELECT request_hash,response FROM studio_operations_role_operations WHERE operation_key=$1", c.OperationKey).Scan(&priorHash, &priorReceipt)
		if err == nil {
			if !bytes.Equal(priorHash, requestHash[:]) {
				return report, errors.New("operation key already belongs to a different role request")
			}
			var prior OperationsRoleMutation
			if json.Unmarshal(priorReceipt, &prior) != nil {
				return report, errors.New("operations receipt unavailable")
			}
			prior.Replayed = true
			report.Result, report.PlanDigest = &prior, prior.PlanDigest
			// An original receipt is read-only. Do not regrant a revoked role or apply
			// present-day mutable state to recovery of an unknown commit.
			report.Applied = c.Apply
			return report, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return report, errors.New("operations receipt lookup unavailable")
		}
	}
	var userVersion int64
	if err = tx.QueryRow(ctx, "SELECT credential_version FROM studio_users WHERE id=$1 FOR UPDATE", c.UserID).Scan(&userVersion); err != nil {
		return report, errors.New("operations target user unavailable")
	}
	var workspace string
	if err = tx.QueryRow(ctx, "SELECT id FROM studio_workspaces WHERE id=$1 FOR UPDATE", c.WorkspaceID).Scan(&workspace); err != nil {
		return report, errors.New("operations target workspace unavailable")
	}
	rows, err := tx.Query(ctx, `SELECT id,user_id,workspace_id,role,active,version FROM studio_operations_grants WHERE user_id=$1 AND workspace_id=$2 ORDER BY role FOR UPDATE`, c.UserID, c.WorkspaceID)
	if err != nil {
		return report, errors.New("operations grants unavailable")
	}
	var existing *OperationsRoleGrant
	for rows.Next() {
		var g OperationsRoleGrant
		if err = rows.Scan(&g.ID, &g.UserID, &g.WorkspaceID, &g.Role, &g.Active, &g.Version); err != nil {
			rows.Close()
			return report, errors.New("operations grants unavailable")
		}
		report.Grants = append(report.Grants, g)
		if g.Role == c.Role {
			copy := g
			existing = &copy
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, errors.New("operations grants unavailable")
	}
	if c.Action == "inspect" {
		return report, nil
	}
	var userCount, workspaceCount int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM studio_operations_grants WHERE user_id=$1 AND active),(SELECT count(*) FROM studio_operations_grants WHERE workspace_id=$2 AND active)`, c.UserID, c.WorkspaceID).Scan(&userCount, &workspaceCount); err != nil {
		return report, errors.New("operations grant capacity unavailable")
	}
	plan, _ := json.Marshal(struct {
		DatabaseID                string
		Request                   operationsRoleRequest
		Grant                     *OperationsRoleGrant
		CredentialVersion         int64
		UserCount, WorkspaceCount int
	}{identity, request, existing, userVersion, userCount, workspaceCount})
	sum := sha256.Sum256(plan)
	report.PlanDigest = hex.EncodeToString(sum[:])
	if c.Apply && c.PlanDigest != report.PlanDigest {
		return report, errors.New("operations plan changed; inspect a new dry-run before apply")
	}
	var result OperationsRoleMutation
	result.OperationID, result.PlanDigest = newID("oro"), report.PlanDigest
	if existing != nil {
		result.Grant = *existing
	} else {
		result.Grant = OperationsRoleGrant{ID: newID("opg"), UserID: c.UserID, WorkspaceID: c.WorkspaceID, Role: c.Role, Version: 1}
	}
	if c.Action == "revoke" && existing == nil {
		return report, errors.New("operations grant does not exist")
	}
	desired := c.Action == "grant"
	result.Changed = existing == nil || existing.Active != desired
	if result.Changed {
		if desired && (userCount >= maxOperationsUserGrants || workspaceCount >= maxOperationsWorkspaceGrants) {
			return report, errors.New("operations active grant capacity exceeded")
		}
		if existing != nil {
			// Every active grant must retain a representable revocation revision.
			// Re-enabling the last inactive revision would make revocation impossible.
			limit := uint64(9007199254740991)
			if desired {
				limit--
			}
			if existing.Version >= limit {
				return report, errors.New("operations grant revision capacity exhausted")
			}
			result.Grant.Version++
		}
		result.Grant.Active = desired
		if existing == nil {
			_, err = tx.Exec(ctx, `INSERT INTO studio_operations_grants(id,user_id,workspace_id,role,active,version) VALUES($1,$2,$3,$4,$5,$6)`, result.Grant.ID, c.UserID, c.WorkspaceID, c.Role, desired, result.Grant.Version)
		} else {
			_, err = tx.Exec(ctx, `UPDATE studio_operations_grants SET active=$2,version=$3,updated_at=clock_timestamp() WHERE id=$1`, result.Grant.ID, desired, result.Grant.Version)
		}
		if err != nil {
			return report, errors.New("operations grant mutation rejected")
		}
	}
	action := "role.granted"
	if c.Action == "revoke" {
		action = "role.revoked"
	}
	if err = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{WorkspaceID: c.WorkspaceID, Action: action, ResourceType: "grant", ResourceID: result.Grant.ID, OperationID: result.OperationID, ReasonCode: c.Reason, EvidenceRef: c.EvidenceRef, Version: result.Grant.Version}); err != nil {
		return report, errors.New("operations audit unavailable")
	}
	receipt, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `INSERT INTO studio_operations_role_operations(operation_key,request_hash,operation_id,response) VALUES($1,$2,$3,$4)`, c.OperationKey, requestHash[:], result.OperationID, receipt); err != nil {
		return report, errors.New("operations receipt unavailable")
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return report, errors.New("operations integrity checks rejected the operation")
	}
	report.Result = &result
	if c.Apply {
		if err = tx.Commit(ctx); err != nil {
			return report, errors.New("operations commit outcome is unknown; replay the exact operation key and request with the original plan digest")
		}
		report.Applied = true
	}
	return report, nil
}
