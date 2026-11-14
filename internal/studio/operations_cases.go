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

type OperationsException struct {
	ID              string          `json:"id"`
	WorkspaceID     string          `json:"workspace_id"`
	ProjectID       string          `json:"project_id"`
	ResourceType    string          `json:"resource_type"`
	ModelID         string          `json:"model_id"`
	ProviderMode    string          `json:"provider_mode"`
	Status          string          `json:"status"`
	ResourceVersion uint64          `json:"resource_version"`
	Billing         *BillingSummary `json:"billing,omitempty"`
	CanReconcile    bool            `json:"can_reconcile"`
	OpenCaseID      string          `json:"open_case_id,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
}
type OperationsCase struct {
	ID              string               `json:"id"`
	WorkspaceID     string               `json:"workspace_id"`
	ProjectID       string               `json:"project_id"`
	ResourceType    string               `json:"resource_type"`
	ResourceID      string               `json:"resource_id"`
	ResourceVersion uint64               `json:"resource_version"`
	State           string               `json:"state"`
	Version         uint64               `json:"version"`
	Reason          string               `json:"reason"`
	EvidenceRef     string               `json:"evidence_ref"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
	Receipt         *OperationsReceipt   `json:"receipt"`
	Resource        *OperationsException `json:"resource,omitempty"`
}
type OperationsReceipt struct {
	ID              string    `json:"id"`
	PlanID          string    `json:"plan_id"`
	Action          string    `json:"action"`
	CaseVersion     uint64    `json:"case_version"`
	ResourceVersion uint64    `json:"resource_version"`
	BillingVersion  uint64    `json:"billing_version"`
	ExecutedAt      time.Time `json:"executed_at"`
}
type OperationsCaseInput struct {
	WorkspaceID     string `json:"workspace_id"`
	ResourceType    string `json:"resource_type"`
	ResourceID      string `json:"resource_id"`
	ResourceVersion uint64 `json:"resource_version"`
	Reason          string `json:"reason"`
	EvidenceRef     string `json:"evidence_ref"`
}
type OperationsPlanInput struct {
	WorkspaceID     string     `json:"workspace_id"`
	ExpectedVersion uint64     `json:"expected_version"`
	Action          string     `json:"action"`
	Reason          string     `json:"reason"`
	EvidenceRef     string     `json:"evidence_ref"`
	Usage           *ChatUsage `json:"usage,omitempty"`
	DeliveredUnits  int        `json:"delivered_units,omitempty"`
}
type OperationsEffect struct {
	ChargedMicros  int64  `json:"charged_micros,string"`
	ReleasedMicros int64  `json:"released_micros,string"`
	Unit           string `json:"unit"`
	Scale          int    `json:"scale"`
}
type OperationsPlan struct {
	ID              string           `json:"id"`
	CaseID          string           `json:"case_id"`
	CaseVersion     uint64           `json:"case_version"`
	ResourceVersion uint64           `json:"resource_version"`
	BillingVersion  uint64           `json:"billing_version"`
	Action          string           `json:"action"`
	PlanDigest      string           `json:"plan_digest"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Effect          OperationsEffect `json:"effect"`
}
type operationsPlanSnapshot struct {
	Plan              OperationsPlan
	Input             OperationsPlanInput
	DatabaseID        string
	ActorID           string
	SessionID         string
	CredentialVersion int64
	GrantHash         [32]byte
	ResourceID        string
	ResourceType      string
	ResourceStatus    string
}
type operationsResource struct {
	Summary OperationsException
	Run     *Run
	Task    *taskRecord
}

func operationsNotFound() error { return failure(404, "NOT_FOUND", "运营工单或资源不存在") }
func validOperationsReason(reason, evidence string) bool {
	switch reason {
	case "provider_unknown", "delivery_issue", "usage_missing", "billing_review", "manual_review":
	default:
		return false
	}
	return ledgerOpaquePattern.MatchString(evidence) && !strings.Contains(evidence, "://")
}
func operationsPermission(action string) string {
	switch action {
	case "reconcile", "no_action":
		return "investigate"
	case "charge", "release":
		return "finance"
	default:
		return ""
	}
}
func operationsBillingVersion(v *BillingSummary) uint64 {
	if v == nil {
		return 0
	}
	return v.Version
}
func isOperationsException(v OperationsException) bool {
	if v.Billing != nil && v.Billing.State == "REVIEW" {
		return true
	}
	if v.ResourceType == "run" {
		return v.Status == "FAILED" || v.Status == "INTERRUPTED"
	}
	return v.Status == "FAILED" || v.Status == "SUBMIT_UNKNOWN" || v.Status == "RECONCILE_REQUIRED" || v.Status == "PROVIDER_STATE_UNKNOWN"
}

func (a *App) readOperationsResourceSQL(ctx context.Context, tx pgx.Tx, workspace, kind, id string, lock bool) (operationsResource, error) {
	var out operationsResource
	if kind != "run" && kind != "task" || !validOperationsID(id, kind) {
		return out, operationsNotFound()
	}
	table := "studio_runs"
	columns := "document"
	if kind == "task" {
		table = "studio_tasks"
		columns = "document,input,handle,submission,next_poll"
	}
	query := "SELECT " + columns + " FROM " + table + " WHERE id=$1 AND workspace_id=$2"
	if lock {
		query += " FOR UPDATE"
	}
	if kind == "run" {
		var blob []byte
		var run Run
		if err := tx.QueryRow(ctx, query, id, workspace).Scan(&blob); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, operationsNotFound()
			}
			return out, err
		}
		if err := json.Unmarshal(blob, &run); err != nil {
			return out, err
		}
		out.Run = &run
		out.Summary = OperationsException{ID: run.ID, WorkspaceID: run.WorkspaceID, ProjectID: run.ProjectID, ResourceType: "run", ModelID: run.ModelID, ProviderMode: run.ProviderMode, Status: run.Status, ResourceVersion: run.Seq, Billing: copyBilling(run.Billing), UpdatedAt: run.UpdatedAt}
	} else {
		var doc, input, handle, submission []byte
		t := &taskRecord{}
		if err := tx.QueryRow(ctx, query, id, workspace).Scan(&doc, &input, &handle, &submission, &t.NextPoll); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, operationsNotFound()
			}
			return out, err
		}
		for _, pair := range []struct {
			b []byte
			v any
		}{{doc, &t.Task}, {input, &t.Input}, {handle, &t.Handle}, {submission, &t.Submission}} {
			if len(pair.b) > 0 {
				if err := json.Unmarshal(pair.b, pair.v); err != nil {
					return out, err
				}
			}
		}
		out.Task = t
		v := t.Task
		out.Summary = OperationsException{ID: v.ID, WorkspaceID: v.WorkspaceID, ProjectID: v.ProjectID, ResourceType: "task", ModelID: v.ModelID, ProviderMode: v.ProviderMode, Status: v.Status, ResourceVersion: v.Version, Billing: copyBilling(v.Billing), CanReconcile: a.canReconcileTask(t) && (v.Status == "SUBMIT_UNKNOWN" || v.Status == "RECONCILE_REQUIRED"), UpdatedAt: v.UpdatedAt}
	}
	return out, nil
}
func readOperationsCaseSQL(ctx context.Context, tx pgx.Tx, workspace, id string, lock bool) (OperationsCase, error) {
	var v OperationsCase
	if !validOperationsID(id, "ocase") {
		return v, operationsNotFound()
	}
	query := `SELECT id,workspace_id,project_id,resource_type,resource_id,resource_version,state,version,reason,evidence_ref,created_at,updated_at FROM studio_operations_cases WHERE id=$1 AND workspace_id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, query, id, workspace).Scan(&v.ID, &v.WorkspaceID, &v.ProjectID, &v.ResourceType, &v.ResourceID, &v.ResourceVersion, &v.State, &v.Version, &v.Reason, &v.EvidenceRef, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, operationsNotFound()
	}
	if err != nil {
		return v, err
	}
	var blob []byte
	err = tx.QueryRow(ctx, "SELECT document FROM studio_operations_receipts WHERE case_id=$1 AND workspace_id=$2", id, workspace).Scan(&blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(blob, &v.Receipt)
	return v, err
}

func (a *App) createOperationsCase(p *principal, in OperationsCaseInput, key string) (OperationsCase, bool, error) {
	var result OperationsCase
	replayed := false
	if !validOperationsReason(in.Reason, in.EvidenceRef) || in.ResourceVersion > 9007199254740991 || in.ResourceType != "run" && in.ResourceType != "task" || !validOperationsID(in.ResourceID, in.ResourceType) {
		return result, false, failure(422, "INVALID_OPERATIONS_INPUT", "需要有效资源、版本、原因代码和短证据标识")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = authorizeOperationsSQL(ctx, tx, p, in.WorkspaceID, "case_open"); err != nil {
			return err
		}
		hash := payloadHash(in)
		var oldHash []byte
		var oldID string
		err = tx.QueryRow(ctx, "SELECT request_hash,case_id FROM studio_operations_case_idempotency WHERE actor_id=$1 AND workspace_id=$2 AND idempotency_key=$3", p.User.ID, in.WorkspaceID, key).Scan(&oldHash, &oldID)
		if err == nil {
			if string(oldHash) != string(hash[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "工单幂等键已用于不同请求")
			}
			result, err = readOperationsCaseSQL(ctx, tx, in.WorkspaceID, oldID, false)
			if err == nil {
				var resource operationsResource
				resource, err = a.readOperationsResourceSQL(ctx, tx, in.WorkspaceID, result.ResourceType, result.ResourceID, false)
				if err == nil {
					result.Resource = &resource.Summary
				}
			}
			replayed = true
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		resource, err := a.readOperationsResourceSQL(ctx, tx, in.WorkspaceID, in.ResourceType, in.ResourceID, true)
		if err != nil {
			return err
		}
		if resource.Summary.ResourceVersion != in.ResourceVersion {
			return failure(409, "OPERATIONS_RESOURCE_CHANGED", "资源版本已变化，请重新读取后建单")
		}
		if !isOperationsException(resource.Summary) {
			return failure(409, "OPERATIONS_NOT_EXCEPTION", "资源当前不在异常或待核实状态")
		}
		var open bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_operations_cases WHERE workspace_id=$1 AND resource_type=$2 AND resource_id=$3 AND state='OPEN')", in.WorkspaceID, in.ResourceType, in.ResourceID).Scan(&open); err != nil {
			return err
		}
		if open {
			return failure(409, "OPERATIONS_CASE_EXISTS", "该资源已有未关闭工单")
		}
		var own, total int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE workspace_id=$1),count(*) FROM (SELECT workspace_id FROM studio_operations_cases LIMIT 100001) c`, in.WorkspaceID).Scan(&own, &total); err != nil {
			return err
		}
		if own >= 10000 || total >= 100000 {
			return failure(429, "OPERATIONS_CASE_LIMIT", "运营工单存储已达上限")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		result = OperationsCase{ID: newID("ocase"), WorkspaceID: in.WorkspaceID, ProjectID: resource.Summary.ProjectID, ResourceType: in.ResourceType, ResourceID: in.ResourceID, ResourceVersion: in.ResourceVersion, State: "OPEN", Version: 1, Reason: in.Reason, EvidenceRef: in.EvidenceRef, CreatedAt: now, UpdatedAt: now, Resource: &resource.Summary}
		if _, err = tx.Exec(ctx, `INSERT INTO studio_operations_cases(id,workspace_id,project_id,resource_type,resource_id,resource_version,reason,evidence_ref,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, result.ID, result.WorkspaceID, result.ProjectID, result.ResourceType, result.ResourceID, result.ResourceVersion, result.Reason, result.EvidenceRef, p.User.ID, now); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_operations_case_idempotency(actor_id,workspace_id,idempotency_key,request_hash,case_id) VALUES($1,$2,$3,$4,$5)", p.User.ID, in.WorkspaceID, key, hash[:], result.ID); err != nil {
			return err
		}
		if err = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: in.WorkspaceID, Action: "case.created", CaseID: result.ID, ResourceType: in.ResourceType, ResourceID: in.ResourceID, ReasonCode: in.Reason, EvidenceRef: in.EvidenceRef, Version: 1}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return result, replayed, err
}

// All business work is local SQL. The reconcile transition only schedules a
// query of the saved handle, and never sends an upstream request here.
func (a *App) applyOperationsActionSQL(ctx context.Context, tx pgx.Tx, p *principal, c OperationsCase, planID string, in OperationsPlanInput, resource operationsResource) (OperationsEffect, error) {
	effect := OperationsEffect{Unit: LedgerUnit, Scale: LedgerScale}
	switch in.Action {
	case "no_action":
		if in.Usage != nil || in.DeliveredUnits != 0 {
			return effect, failure(422, "INVALID_OPERATIONS_INPUT", "关闭工单不能附带收费计量")
		}
		return effect, nil
	case "reconcile":
		if in.Usage != nil || in.DeliveredUnits != 0 || resource.Task == nil || !resource.Summary.CanReconcile {
			return effect, failure(409, "RECONCILE_UNAVAILABLE", "仅能核对有原受理记录的停驻媒体任务")
		}
		t := resource.Task
		if t.Task.Version >= 9007199254740991 {
			return effect, failure(409, "OPERATIONS_VERSION_LIMIT", "资源版本已达上限")
		}
		if t.Task.ReconcileAfter != nil && time.Now().Before(*t.Task.ReconcileAfter) {
			return effect, failure(429, "RECONCILE_COOLDOWN", "原任务仍在核对冷却期")
		}
		now := time.Now().UTC()
		after := now.Add(15 * time.Second)
		t.Lease = nil
		t.LeaseComplete = false
		t.LeaseStatus = ""
		t.Task.ReconcileAfter = &after
		t.Task.CanReconcile = false
		t.Task.Status = "QUEUED"
		t.Task.Error = nil
		t.Task.Version++
		t.Task.UpdatedAt = now
		t.NextPoll = now
		if err := saveBilledTaskSQL(ctx, tx, t); err != nil {
			return effect, err
		}
		if err := insertAuditSQL(ctx, tx, newAuditEvent(c.WorkspaceID, c.ProjectID, p.User.ID, "task.reconcile_requested", "task", c.ResourceID, AuditDetails{FromStatus: resource.Summary.Status, ToStatus: "QUEUED", Kind: t.Task.Kind})); err != nil {
			return effect, err
		}
		return effect, appendTaskOutboxSQL(ctx, tx, t.Task)
	case "charge", "release":
		if resource.Summary.Billing == nil || resource.Summary.Billing.State != "REVIEW" {
			return effect, failure(409, "LEDGER_REVIEW_REQUIRED", "仅支持待核实冻结的财务决定")
		}
		before := resource.Summary.Billing
		if resource.Summary.ResourceVersion >= 9007199254740991 {
			return effect, failure(409, "OPERATIONS_VERSION_LIMIT", "资源版本已达上限")
		}
		mutation, err := resolveLedgerSQL(ctx, tx, LedgerResolveRequest{ReservationID: before.ReservationID, OperationKey: "ops:" + planID, Reason: in.Reason, EvidenceRef: in.EvidenceRef, Decision: in.Action, Usage: in.Usage, DeliveredUnits: in.DeliveredUnits})
		if err != nil {
			return effect, err
		}
		if mutation.Reservation == nil {
			return effect, errors.New("operations resolution omitted reservation")
		}
		if resource.Task != nil {
			if _, err = tx.Exec(ctx, "UPDATE studio_tasks SET lease_epoch=lease_epoch+1,lease_owner='',lease_until=NULL WHERE id=$1 AND workspace_id=$2", c.ResourceID, c.WorkspaceID); err != nil {
				return effect, err
			}
		}
		effect.ChargedMicros = mutation.Reservation.ChargedMicros - before.ChargedMicros
		effect.ReleasedMicros = mutation.Reservation.ReleasedMicros - before.ReleasedMicros
		return effect, nil
	default:
		return effect, failure(422, "INVALID_OPERATIONS_INPUT", "运营动作不受支持")
	}
}

func (a *App) previewOperationsCase(p *principal, id string, in OperationsPlanInput) (OperationsPlan, error) {
	var result OperationsPlan
	permission := operationsPermission(in.Action)
	if permission == "" || !validOperationsReason(in.Reason, in.EvidenceRef) || in.ExpectedVersion < 1 || in.ExpectedVersion >= 9007199254740991 || in.Usage != nil && in.Usage.Reason != "" {
		return result, failure(422, "INVALID_OPERATIONS_INPUT", "需要有效动作、工单版本、原因与证据标识")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		grant, err := authorizeOperationsSQL(ctx, tx, p, in.WorkspaceID, permission)
		if err != nil {
			return err
		}
		c, err := readOperationsCaseSQL(ctx, tx, in.WorkspaceID, id, true)
		if err != nil {
			return err
		}
		if c.State != "OPEN" {
			return failure(409, "OPERATIONS_CASE_RESOLVED", "工单已结束，只能读取回执")
		}
		if c.Version != in.ExpectedVersion {
			return failure(409, "OPERATIONS_CASE_CHANGED", "工单版本已变化")
		}
		resource, err := a.readOperationsResourceSQL(ctx, tx, in.WorkspaceID, c.ResourceType, c.ResourceID, true)
		if err != nil {
			return err
		}
		var now time.Time
		var databaseID string
		if err = tx.QueryRow(ctx, "SELECT clock_timestamp(),storage_id::text FROM studio_storage_identity WHERE singleton").Scan(&now, &databaseID); err != nil {
			return err
		}
		result = OperationsPlan{ID: newID("oplan"), CaseID: id, CaseVersion: c.Version, ResourceVersion: resource.Summary.ResourceVersion, BillingVersion: operationsBillingVersion(resource.Summary.Billing), Action: in.Action, ExpiresAt: now.Add(5 * time.Minute).UTC()}
		simulated, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		effect, actionErr := a.applyOperationsActionSQL(ctx, simulated, p, c, result.ID, in, resource)
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
		result.Effect = effect
		snapshot := operationsPlanSnapshot{Plan: result, Input: in, DatabaseID: databaseID, ActorID: p.User.ID, SessionID: p.SessionID, CredentialVersion: p.CredentialVersion, GrantHash: payloadHash(grant), ResourceID: c.ResourceID, ResourceType: c.ResourceType, ResourceStatus: resource.Summary.Status}
		digest := payloadHash(snapshot)
		result.PlanDigest = hex.EncodeToString(digest[:])
		snapshot.Plan = result
		blob, _ := json.Marshal(snapshot)
		if _, err = tx.Exec(ctx, "DELETE FROM studio_operations_plans WHERE id IN(SELECT id FROM studio_operations_plans WHERE consumed_at IS NULL AND expires_at<clock_timestamp()-interval '1 day' ORDER BY expires_at,id LIMIT 1000)"); err != nil {
			return err
		}
		var own, active, total int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE workspace_id=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp()),count(*) FILTER(WHERE consumed_at IS NULL AND expires_at>clock_timestamp()),count(*) FROM (SELECT workspace_id,consumed_at,expires_at FROM studio_operations_plans LIMIT 100001) p`, in.WorkspaceID).Scan(&own, &active, &total); err != nil {
			return err
		}
		if own >= 64 || active >= 10000 || total >= 100000 {
			return failure(429, "OPERATIONS_PLAN_LIMIT", "运营预演数量已达上限")
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_operations_plans(id,case_id,workspace_id,actor_id,snapshot,digest,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", result.ID, id, in.WorkspaceID, p.User.ID, blob, result.PlanDigest, result.ExpiresAt); err != nil {
			return err
		}
		if err = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: in.WorkspaceID, Action: "case.previewed", CaseID: id, ResourceType: c.ResourceType, ResourceID: c.ResourceID, OperationID: result.ID, ReasonCode: in.Reason, EvidenceRef: in.EvidenceRef, Version: c.Version}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return result, err
}

func (a *App) executeOperationsCase(p *principal, proof *operationsPasswordProof, id, workspace, planID, digest string, version uint64) (OperationsCase, error) {
	var result OperationsCase
	var committedResource operationsResource
	if !validOperationsID(id, "ocase") {
		return result, operationsNotFound()
	}
	if !validOperationsID(planID, "oplan") || len(digest) != 64 || version < 1 || version >= 9007199254740991 {
		return result, failure(422, "INVALID_OPERATIONS_INPUT", "预演标识、摘要或工单版本无效")
	}
	decoded, decodeErr := hex.DecodeString(digest)
	if decodeErr != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
		return result, failure(422, "INVALID_OPERATIONS_INPUT", "预演摘要必须是64位小写十六进制")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		// Read permission is checked before looking up a private plan; execution
		// also checks its exact action permission and password proof in this tx.
		if _, err = authorizeOperationsSQL(ctx, tx, p, workspace, "read"); err != nil {
			return err
		}
		var blob []byte
		var storedDigest string
		var consumed *time.Time
		var fresh bool
		err = tx.QueryRow(ctx, "SELECT snapshot,digest,consumed_at,expires_at>clock_timestamp() FROM studio_operations_plans WHERE id=$1 AND case_id=$2 AND workspace_id=$3 AND actor_id=$4 FOR UPDATE", planID, id, workspace, p.User.ID).Scan(&blob, &storedDigest, &consumed, &fresh)
		if errors.Is(err, pgx.ErrNoRows) {
			return operationsNotFound()
		}
		if err != nil {
			return err
		}
		if consumed != nil {
			return failure(409, "OPERATIONS_PLAN_CONSUMED", "此预演已执行，请读取工单回执")
		}
		if !fresh {
			return failure(409, "OPERATIONS_PLAN_EXPIRED", "预演已过期，请重新读取并预演")
		}
		if storedDigest != digest {
			return failure(409, "OPERATIONS_PLAN_CHANGED", "预演摘要不一致")
		}
		var snapshot operationsPlanSnapshot
		if err = json.Unmarshal(blob, &snapshot); err != nil {
			return err
		}
		originalDigest := snapshot.Plan.PlanDigest
		snapshot.Plan.PlanDigest = ""
		computed := payloadHash(snapshot)
		snapshot.Plan.PlanDigest = originalDigest
		if hex.EncodeToString(computed[:]) != storedDigest || snapshot.Plan.ID != planID || snapshot.Plan.CaseID != id || snapshot.Input.WorkspaceID != workspace {
			return failure(409, "OPERATIONS_PLAN_CHANGED", "持久预演内容与摘要不一致")
		}
		permission := operationsPermission(snapshot.Input.Action)
		if err = checkOperationsPermissionSQL(ctx, tx, p, workspace, permission, proof); err != nil {
			return err
		}
		grant, err := authorizeOperationsSQL(ctx, tx, p, workspace, permission)
		if err != nil {
			return err
		}
		var databaseID string
		if err = tx.QueryRow(ctx, "SELECT storage_id::text FROM studio_storage_identity WHERE singleton").Scan(&databaseID); err != nil {
			return err
		}
		if snapshot.ActorID != p.User.ID || snapshot.SessionID != p.SessionID || snapshot.CredentialVersion != p.CredentialVersion || snapshot.GrantHash != payloadHash(grant) || snapshot.DatabaseID != databaseID {
			return failure(409, "OPERATIONS_PLAN_CHANGED", "账号、会话、授权或数据库身份已变化，请重新预演")
		}
		c, err := readOperationsCaseSQL(ctx, tx, workspace, id, true)
		if err != nil {
			return err
		}
		if c.State != "OPEN" {
			return failure(409, "OPERATIONS_CASE_RESOLVED", "工单已结束，请读取回执")
		}
		if c.Version != version || snapshot.Plan.CaseVersion != c.Version {
			return failure(409, "OPERATIONS_CASE_CHANGED", "工单版本已变化")
		}
		resource, err := a.readOperationsResourceSQL(ctx, tx, workspace, c.ResourceType, c.ResourceID, true)
		if err != nil {
			return err
		}
		if resource.Summary.ResourceVersion != snapshot.Plan.ResourceVersion || operationsBillingVersion(resource.Summary.Billing) != snapshot.Plan.BillingVersion || resource.Summary.Status != snapshot.ResourceStatus {
			return failure(409, "OPERATIONS_RESOURCE_CHANGED", "资源或结算版本已变化，请重新预演")
		}
		effect, err := a.applyOperationsActionSQL(ctx, tx, p, c, planID, snapshot.Input, resource)
		if err != nil {
			return err
		}
		if effect != snapshot.Plan.Effect {
			return failure(409, "OPERATIONS_PLAN_CHANGED", "业务效果与预演不一致")
		}
		committedResource, err = a.readOperationsResourceSQL(ctx, tx, workspace, c.ResourceType, c.ResourceID, false)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		c.State = "RESOLVED"
		c.Version++
		c.ResourceVersion = committedResource.Summary.ResourceVersion
		c.UpdatedAt = now
		c.Resource = &committedResource.Summary
		c.Receipt = &OperationsReceipt{ID: newID("orec"), PlanID: planID, Action: snapshot.Input.Action, CaseVersion: c.Version, ResourceVersion: c.ResourceVersion, BillingVersion: operationsBillingVersion(committedResource.Summary.Billing), ExecutedAt: now}
		receipt, _ := json.Marshal(c.Receipt)
		if _, err = tx.Exec(ctx, "UPDATE studio_operations_cases SET state='RESOLVED',version=$2,resource_version=$3,updated_at=$4 WHERE id=$1", id, c.Version, c.ResourceVersion, now); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE studio_operations_plans SET consumed_at=$2 WHERE id=$1", planID, now); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_operations_receipts(id,case_id,plan_id,workspace_id,document,created_at) VALUES($1,$2,$3,$4,$5,$6)", c.Receipt.ID, id, planID, workspace, receipt, now); err != nil {
			return err
		}
		if err = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: workspace, Action: "case.executed", CaseID: id, ResourceType: c.ResourceType, ResourceID: c.ResourceID, OperationID: c.Receipt.ID, ReasonCode: snapshot.Input.Reason, EvidenceRef: snapshot.Input.EvidenceRef, Version: c.Version}); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		result = c
		return nil
	})
	if err == nil && result.Receipt.Action != "no_action" {
		a.cacheOperationsResourceLocked(committedResource)
	}
	return result, err
}
func (a *App) cacheOperationsResourceLocked(resource operationsResource) {
	if resource.Run != nil {
		if cached := a.runs[resource.Run.ID]; cached != nil {
			cached.Run = copyRun(*resource.Run)
			cached.CheckpointSeq = cached.Run.Seq
		}
	}
	if resource.Task != nil {
		t := resource.Task
		if cached := a.tasks[t.Task.ID]; cached != nil {
			cached.Task = copyTask(t.Task)
			cached.Handle = t.Handle
			cached.Submission = t.Submission
			cached.NextPoll = t.NextPoll
			cached.Lease = nil
			cached.LeaseComplete = false
			cached.LeaseStatus = ""
			cached.InFlight = false
		}
		a.publishTaskLocked(t.Task)
	}
}
