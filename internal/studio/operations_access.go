package studio

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const maxOperationsUserGrants = 128
const maxOperationsWorkspaceGrants = 1000

var operationsIDPattern = regexp.MustCompile(`^[a-z]+_[a-f0-9]{32}$`)

func validOperationsID(id, prefix string) bool {
	return strings.HasPrefix(id, prefix+"_") && operationsIDPattern.MatchString(id)
}

func operationsRoles(permission string) []string {
	switch permission {
	case "read":
		return []string{"observer", "operator", "finance", "moderator"}
	case "case_open":
		return []string{"operator", "finance"}
	case "investigate":
		return []string{"operator"}
	case "finance":
		return []string{"finance"}
	case "risk_review":
		return []string{"risk_reviewer"}
	case "support_ticket":
		return []string{"support"}
	case "generation_restrict", "appeal_resolve", "content_appeal_review":
		return []string{"moderator"}
	default:
		return nil
	}
}

func operationsRolePermissions(role string) []string {
	switch role {
	case "observer":
		return []string{"read"}
	case "operator":
		return []string{"read", "case_open", "investigate"}
	case "finance":
		return []string{"read", "case_open", "finance"}
	case "risk_reviewer":
		return []string{"risk_review"}
	case "support":
		return []string{"support_ticket"}
	case "moderator":
		return []string{"read", "generation_restrict", "appeal_resolve", "content_appeal_review"}
	default:
		return nil
	}
}

type OperationsAccess struct {
	WorkspaceID string   `json:"workspace_id"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	Version     uint64   `json:"version"`
}

type OperationsCapabilities struct {
	Enabled bool               `json:"enabled"`
	Access  []OperationsAccess `json:"access,omitempty"`
	Reason  string             `json:"reason,omitempty"`
}

// Identity only advertises the surface. It never caches or asserts a grant.
// The access endpoint and every protected request read current grants from PG.
func (a *App) operationsCapabilities(p *principal) OperationsCapabilities {
	enabled := a.config.OperationsEnabled && a.config.AuthMode == "required" && a.store != nil
	c := OperationsCapabilities{Enabled: enabled}
	if !enabled {
		c.Reason = "operations_disabled"
	} else if p == nil || p.User == nil {
		c.Enabled = false
		c.Reason = "authentication_required"
	}
	return c
}

type OperationsGrantSnapshot struct {
	GrantID           string `json:"grant_id"`
	Version           uint64 `json:"version"`
	Role              string `json:"role"`
	WorkspaceID       string `json:"workspace_id"`
	UserID            string `json:"user_id"`
	SessionID         string `json:"session_id"`
	CredentialVersion int64  `json:"credential_version"`
}

func operationsDenied() error {
	return failure(404, "OPERATIONS_NOT_FOUND", "运营范围不存在或当前账户无权访问")
}

func (a *App) requireOperationsAccount(r *http.Request) (*principal, error) {
	if !a.config.OperationsEnabled || a.config.AuthMode != "required" || a.store == nil {
		return nil, failure(409, "OPERATIONS_DISABLED", "当前环境未启用运营操作")
	}
	return a.requireAccount(r)
}

func (a *App) requireOperations(r *http.Request, workspaceID, permission string) (*principal, error) {
	p, err := a.requireOperationsAccount(r)
	if err != nil {
		return nil, err
	}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspaceID, permission); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return p, err
}

// Lock order is user -> session -> grant -> business resource. Password/session
// revocation and role revocation cannot pass the in-transaction authorization.
func operationsSessionSQL(ctx context.Context, tx pgx.Tx, p *principal) (passwordSnapshot, error) {
	if p == nil || p.User == nil || !validOperationsID(p.User.ID, "usr") || !validOperationsID(p.Workspace.ID, "ws") || !validSecurityID(p.SessionID, "ses") || len(p.SessionHash) != 32 || p.CredentialVersion < 1 {
		return passwordSnapshot{}, failure(401, "AUTH_REQUIRED", "会话已失效，请重新登录")
	}
	snapshot, err := readPasswordSnapshot(ctx, tx, p.User.ID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, failure(401, "AUTH_REQUIRED", "会话已失效，请重新登录")
	}
	if err != nil {
		return snapshot, err
	}
	if snapshot.Version != p.CredentialVersion {
		return snapshot, failure(401, "AUTH_REQUIRED", "会话已失效，请重新登录")
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM studio_sessions WHERE token_hash=$1 AND user_id=$2 AND workspace_id=$3 AND id=$4 AND credential_version=$5 AND expires_at>clock_timestamp() FOR SHARE`, p.SessionHash, p.User.ID, p.Workspace.ID, p.SessionID, snapshot.Version).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, failure(401, "AUTH_REQUIRED", "会话已失效，请重新登录")
	}
	return snapshot, err
}

func operationsGrantSQL(ctx context.Context, tx pgx.Tx, p *principal, workspaceID, permission string) (OperationsGrantSnapshot, error) {
	var grant OperationsGrantSnapshot
	roles := operationsRoles(permission)
	if len(roles) == 0 {
		return grant, failure(422, "INVALID_OPERATIONS_PERMISSION", "运营权限类型无效")
	}
	if !validOperationsID(workspaceID, "ws") {
		return grant, operationsDenied()
	}
	err := tx.QueryRow(ctx, `SELECT id,role,version FROM studio_operations_grants WHERE user_id=$1 AND workspace_id=$2 AND active AND role=ANY($3::text[]) ORDER BY role,id LIMIT 1 FOR SHARE`, p.User.ID, workspaceID, roles).Scan(&grant.GrantID, &grant.Role, &grant.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return grant, operationsDenied()
	}
	grant.WorkspaceID, grant.UserID, grant.SessionID, grant.CredentialVersion = workspaceID, p.User.ID, p.SessionID, p.CredentialVersion
	return grant, err
}

func authorizeOperationsSQL(ctx context.Context, tx pgx.Tx, p *principal, workspaceID, permission string) (OperationsGrantSnapshot, error) {
	if _, err := operationsSessionSQL(ctx, tx, p); err != nil {
		return OperationsGrantSnapshot{}, err
	}
	return operationsGrantSQL(ctx, tx, p, workspaceID, permission)
}

// This proof is never serialized, persisted, returned to the browser or logged.
// It is only passed from bcrypt validation into the same request's transaction.
type operationsPasswordProof struct {
	snapshot    passwordSnapshot
	sessionID   string
	sessionHash []byte
	verifiedAt  time.Time
}

func (a *App) verifyOperationsPassword(r *http.Request, password string) (*operationsPasswordProof, error) {
	p, err := a.requireOperationsAccount(r)
	if err != nil {
		return nil, err
	}
	if !validAccountPassword(password) {
		return nil, failure(422, "INVALID_OPERATIONS_PASSWORD", "密码需要10至72字节有效文本")
	}
	if !a.authAdmission(r, "operations-password", p.User.ID) {
		return nil, failure(429, "OPERATIONS_RATE_LIMIT", "密码核验过于频繁，请稍后重试")
	}
	select {
	case a.bcryptSlots <- struct{}{}:
		defer func() { <-a.bcryptSlots }()
	default:
		return nil, failure(429, "AUTH_BUSY", "认证处理繁忙，请稍后重试")
	}
	var snapshot passwordSnapshot
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		snapshot, e = operationsSessionSQL(ctx, tx, p)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword(snapshot.Hash, []byte(password)) != nil {
		return nil, failure(403, "OPERATIONS_PASSWORD_INVALID", "当前密码核验失败")
	}
	if err := r.Context().Err(); err != nil {
		return nil, failure(408, "OPERATIONS_REQUEST_CANCELLED", "请求已取消，请重新确认操作")
	}
	return &operationsPasswordProof{snapshot: snapshot, sessionID: p.SessionID, sessionHash: append([]byte(nil), p.SessionHash...), verifiedAt: time.Now()}, nil
}

func checkOperationsPermissionSQL(ctx context.Context, tx pgx.Tx, p *principal, workspaceID, permission string, proof *operationsPasswordProof) error {
	if proof == nil || time.Since(proof.verifiedAt) < 0 || time.Since(proof.verifiedAt) > 5*time.Minute || p == nil || p.User == nil || proof.snapshot.UserID != p.User.ID || proof.sessionID != p.SessionID || !bytes.Equal(proof.sessionHash, p.SessionHash) {
		return failure(403, "OPERATIONS_REAUTH_REQUIRED", "敏感操作需要重新核验当前密码")
	}
	current, err := operationsSessionSQL(ctx, tx, p)
	if err != nil {
		return err
	}
	if current.Version != proof.snapshot.Version || !bytes.Equal(current.Hash, proof.snapshot.Hash) {
		return failure(403, "OPERATIONS_REAUTH_REQUIRED", "凭据已经变化，请重新核验密码")
	}
	_, err = operationsGrantSQL(ctx, tx, p, workspaceID, permission)
	return err
}

func (a *App) operationsAccessHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireOperationsAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	access := []OperationsAccess{}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = operationsSessionSQL(ctx, tx, p); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT workspace_id,role,version FROM studio_operations_grants WHERE user_id=$1 AND active ORDER BY workspace_id,role LIMIT $2`, p.User.ID, maxOperationsUserGrants+1)
		if e != nil {
			return e
		}
		for rows.Next() {
			var item OperationsAccess
			if e = rows.Scan(&item.WorkspaceID, &item.Role, &item.Version); e != nil {
				rows.Close()
				return e
			}
			item.Permissions = operationsRolePermissions(item.Role)
			access = append(access, item)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(access) > maxOperationsUserGrants {
			return failure(409, "OPERATIONS_ACCESS_LIMIT", "运营授权数量超过当前读取上限，请联系离线管理员")
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": true, "access": access})
}

type OperationsAuditEvent struct {
	ActorUserID  string `json:"actor_user_id,omitempty"`
	WorkspaceID  string `json:"workspace_id"`
	Action       string `json:"action"`
	CaseID       string `json:"case_id,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
	OperationID  string `json:"operation_id,omitempty"`
	ReasonCode   string `json:"reason_code,omitempty"`
	EvidenceRef  string `json:"evidence_ref,omitempty"`
	Version      uint64 `json:"version"`
}

func insertOperationsAuditSQL(ctx context.Context, tx pgx.Tx, event OperationsAuditEvent) error {
	_, err := insertOperationsAuditRecordSQL(ctx, tx, event)
	return err
}

type OperationsAuditRecord struct {
	OperationsAuditEvent
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

func insertOperationsAuditRecordSQL(ctx context.Context, tx pgx.Tx, event OperationsAuditEvent) (OperationsAuditRecord, error) {
	result := OperationsAuditRecord{OperationsAuditEvent: event}
	actions := map[string]bool{"role.granted": true, "role.revoked": true, "case.created": true, "case.updated": true, "case.previewed": true, "case.executed": true, "case.reconciled": true, "case.resolved": true, "case.refunded": true, "generation.previewed": true, "generation.restricted": true, "generation.lifted": true, "appeal.created": true, "appeal.upheld": true, "appeal.lifted": true,
		"content_appeal.created": true, "content_appeal.previewed": true, "content_appeal.upheld": true,
		"content_appeal.opinion": true, "content_appeal.referred": true, "content_appeal.finance_linked": true,
		"risk_review.created": true, "risk_review.previewed": true, "risk_review.approved": true, "risk_review.denied": true,
	}
	kinds := map[string]bool{"": true, "run": true, "task": true, "reservation": true, "grant": true, "case": true, "restriction": true, "appeal": true}
	if !validOperationsID(event.WorkspaceID, "ws") || event.ActorUserID != "" && !validOperationsID(event.ActorUserID, "usr") || !actions[event.Action] || !kinds[event.ResourceType] || event.Version > 9007199254740991 {
		return result, failure(422, "INVALID_OPERATIONS_AUDIT", "运营审计字段无效")
	}
	for _, value := range []string{event.CaseID, event.ResourceID, event.OperationID, event.ReasonCode, event.EvidenceRef} {
		if value != "" && (!ledgerOpaquePattern.MatchString(value) || strings.Contains(value, "://")) {
			return result, failure(422, "INVALID_OPERATIONS_AUDIT", "运营审计只接受有界标识，不接受正文")
		}
	}
	result.ID = newID("opa")
	err := tx.QueryRow(ctx, `INSERT INTO studio_operations_audit(id,actor_user_id,workspace_id,action,case_id,resource_type,resource_id,operation_id,reason_code,evidence_ref,version) VALUES($1,nullif($2,''),$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING created_at`, result.ID, event.ActorUserID, event.WorkspaceID, event.Action, event.CaseID, event.ResourceType, event.ResourceID, event.OperationID, event.ReasonCode, event.EvidenceRef, event.Version).Scan(&result.CreatedAt)
	return result, err
}
