package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type CollaborationEntry struct {
	ProjectID    string     `json:"project_id"`
	ProjectName  string     `json:"project_name"`
	TargetUserID string     `json:"target_user_id"`
	InviterName  string     `json:"inviter_name"`
	InvitationID string     `json:"invitation_id"`
	Role         string     `json:"role"`
	State        string     `json:"state"`
	Round        uint64     `json:"round"`
	Version      uint64     `json:"version"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}
type CollaborationReceipt struct {
	CollaborationEntry
	Operation string `json:"operation"`
}
type CollaborationPage struct {
	Items []CollaborationEntry `json:"items"`
}

type collaborationMutation struct {
	Project, Target, Invitation, Role string
	ExpectedVersion                   uint64
}

func collaborationMissing() error {
	return failure(404, "COLLABORATION_NOT_FOUND", "未找到可访问的项目、成员、邀请或操作收据")
}
func collaborationConflict() error {
	return failure(409, "COLLABORATION_VERSION_CONFLICT", "成员关系或邀请轮次已变化，请读取最新状态")
}
func collaborationCapacity() error {
	return failure(429, "COLLABORATION_LIMIT", "邀请关系或轮次已达上限，已有成员仍可撤权")
}
func collaborationReadError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return collaborationMissing()
	}
	return e
}
func collaborationOperation(op string) bool {
	switch op {
	case "invite", "accept", "withdraw", "downgrade", "revoke":
		return true
	}
	return false
}

func (a *App) collaborationTx(p *principal, fn func(context.Context, pgx.Tx) error) error {
	if a.store == nil || a.config.AuthMode != "required" {
		return failure(501, "COLLABORATION_STORAGE_REQUIRED", "项目协作需要持久账户模式")
	}
	if p == nil || p.User == nil {
		return failure(401, "AUTH_REQUIRED", "请先登录")
	}
	return a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error { return fn(ctx, tx) })
	})
}

// Only the collaboration draft endpoint uses this scope. The personal session,
// its project list and all ordinary workspace APIs keep their original scope.
func collaborationProjectAccessSQL(ctx context.Context, tx pgx.Tx, p *principal, project string, write bool) (workspace, role string, version uint64, err error) {
	if !validOperationsID(project, "prj") {
		err = collaborationMissing()
		return
	}
	if err = checkOrdinaryAccountSQL(ctx, tx, p); err != nil {
		return
	}
	var owner string
	err = tx.QueryRow(ctx, `SELECT q.workspace_id,COALESCE(w.owner_id,'') FROM studio_projects q JOIN studio_workspaces w ON w.id=q.workspace_id
	 WHERE q.id=$1 AND ((w.owner_id=$2 AND q.workspace_id=$3) OR EXISTS(SELECT 1 FROM studio_collaboration_members m WHERE m.project_id=q.id AND m.target_user_id=$2 AND m.state='ACTIVE')) FOR NO KEY UPDATE OF q`,
		project, p.User.ID, p.Workspace.ID).Scan(&workspace, &owner)
	if err != nil {
		err = collaborationReadError(err)
		return
	}
	if owner == p.User.ID && workspace == p.Workspace.ID {
		role = "owner"
		return
	}
	// Separate statement after acquiring the project lock: READ COMMITTED now
	// observes any demotion/revocation which committed while this caller waited.
	err = tx.QueryRow(ctx, `SELECT role,version FROM studio_collaboration_members WHERE project_id=$1 AND target_user_id=$2 AND state='ACTIVE'`,
		project, p.User.ID).Scan(&role, &version)
	if err != nil {
		err = collaborationReadError(err)
		return
	}
	if write && role != "editor" {
		err = failure(403, "COLLABORATION_READ_ONLY", "项目成员只具备查看权限")
	}
	return
}

func collaborationOwnerSQL(ctx context.Context, tx pgx.Tx, p *principal, project string) (string, error) {
	var workspace string
	err := tx.QueryRow(ctx, `SELECT q.workspace_id FROM studio_projects q JOIN studio_workspaces w ON w.id=q.workspace_id
	 WHERE q.id=$1 AND q.workspace_id=$2 AND w.owner_id=$3 FOR NO KEY UPDATE OF q`, project, p.Workspace.ID, p.User.ID).Scan(&workspace)
	return workspace, collaborationReadError(err)
}

const collaborationEntrySelect = `SELECT m.project_id,p.name,m.target_user_id,u.name,m.invitation_id,m.role,
 CASE WHEN m.state='PENDING' AND m.expires_at<=statement_timestamp() THEN 'EXPIRED' ELSE m.state END,
 m.round,m.version,CASE WHEN m.state='PENDING' THEN m.expires_at END
 FROM studio_collaboration_members m JOIN studio_projects p ON p.id=m.project_id JOIN studio_users u ON u.id=m.inviter_id `

func collaborationEntrySQL(ctx context.Context, tx pgx.Tx, project, target string) (r CollaborationEntry, err error) {
	err = tx.QueryRow(ctx, collaborationEntrySelect+`WHERE m.project_id=$1 AND m.target_user_id=$2`, project, target).Scan(&r.ProjectID, &r.ProjectName, &r.TargetUserID, &r.InviterName, &r.InvitationID, &r.Role, &r.State, &r.Round, &r.Version, &r.ExpiresAt)
	return
}

func collaborationReceiptSQL(ctx context.Context, tx pgx.Tx, actor, operation, key string) (r CollaborationReceipt, hash []byte, err error) {
	k := sha256.Sum256([]byte(key))
	err = tx.QueryRow(ctx, `SELECT response,request_hash FROM studio_collaboration_receipts WHERE actor_id=$1 AND operation=$2 AND key_hash=$3`, actor, operation, k[:]).Scan(&r, &hash)
	return
}

func (a *App) collaborationReceipt(p *principal, operation, key string) (r CollaborationReceipt, err error) {
	if !workflowKey.MatchString(key) || !collaborationOperation(operation) {
		return r, collaborationMissing()
	}
	err = a.collaborationTx(p, func(ctx context.Context, tx pgx.Tx) error {
		if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
			return e
		}
		var e error
		r, _, e = collaborationReceiptSQL(ctx, tx, p.User.ID, operation, key)
		return collaborationReadError(e)
	})
	if err != nil {
		return CollaborationReceipt{}, err
	}
	return
}
func (a *App) collaborationList(p *principal, project, kind, target string) (out CollaborationPage, err error) {
	err = a.collaborationTx(p, func(ctx context.Context, tx pgx.Tx) error {
		if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
			return e
		}
		arg := p.User.ID
		var where string
		switch kind {
		case "members":
			if _, e := collaborationOwnerSQL(ctx, tx, p, project); e != nil {
				return e
			}
			if target != "" {
				if !validOperationsID(target, "usr") {
					return failure(422, "INVALID_COLLABORATION", "请提供有效的目标账户 ID")
				}
				entry, e := collaborationEntrySQL(ctx, tx, project, target)
				out.Items = []CollaborationEntry{}
				if errors.Is(e, pgx.ErrNoRows) {
					return nil
				}
				if e != nil {
					return e
				}
				out.Items = append(out.Items, entry)
				return nil
			}
			// At most 64 live relations. The recent-history index only passes
			// those live rows before locating the 16 most recent closed rows.
			rows, e := tx.Query(ctx, collaborationEntrySelect+`JOIN (
			 (SELECT target_user_id,0 AS priority FROM studio_collaboration_members WHERE project_id=$1 AND state='ACTIVE' LIMIT 64)
			 UNION ALL (SELECT target_user_id,0 FROM studio_collaboration_members WHERE project_id=$1 AND state='PENDING' AND expires_at>statement_timestamp() LIMIT 64)
			 UNION ALL (SELECT target_user_id,1 FROM studio_collaboration_members WHERE project_id=$1 AND (state IN('WITHDRAWN','REVOKED') OR (state='PENDING' AND expires_at<=statement_timestamp()))
			   ORDER BY updated_at DESC,target_user_id LIMIT 16)
			 ) found ON found.target_user_id=m.target_user_id WHERE m.project_id=$1
			 ORDER BY found.priority,m.updated_at DESC,m.target_user_id`, project)
			if e != nil {
				return e
			}
			out.Items, e = pgx.CollectRows(rows, pgx.RowToStructByPos[CollaborationEntry])
			return e
		case "invitations":
			where = "m.target_user_id=$1 AND m.state='PENDING' AND m.expires_at>statement_timestamp()"
		case "shared-projects":
			where = "m.target_user_id=$1 AND m.state='ACTIVE'"
		default:
			return collaborationMissing()
		}
		rows, e := tx.Query(ctx, collaborationEntrySelect+"WHERE "+where+" ORDER BY m.updated_at DESC,m.project_id,m.target_user_id LIMIT 128", arg)
		if e != nil {
			return e
		}
		out.Items, e = pgx.CollectRows(rows, pgx.RowToStructByPos[CollaborationEntry])
		return e
	})
	return
}

func (a *App) mutateCollaboration(p *principal, op, key string, in collaborationMutation) (out CollaborationReceipt, replay bool, err error) {
	if !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效的操作原键")
	}
	if !collaborationOperation(op) || in.ExpectedVersion > 512 ||
		op != "accept" && (!validOperationsID(in.Project, "prj") || !validOperationsID(in.Target, "usr")) ||
		op != "invite" && (!validOperationsID(in.Invitation, "pci") || in.ExpectedVersion == 0) ||
		op == "invite" && in.Role != "viewer" && in.Role != "editor" {
		return out, false, failure(422, "INVALID_COLLABORATION", "请核对用户、邀请、角色和预期版本")
	}
	hash := payloadHash(in)
	err = a.collaborationTx(p, func(ctx context.Context, tx pgx.Tx) error {
		// Reciprocal invitations and operations on the same recipient lock
		// users in the same order, before either takes a project lock.
		if op == "invite" {
			_, e := tx.Exec(ctx, "SELECT id FROM studio_users WHERE id=ANY($1::text[]) ORDER BY id FOR UPDATE", []string{p.User.ID, in.Target})
			if e != nil {
				return e
			}
		}
		if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
			return e
		}
		project, target := in.Project, in.Target
		var workspace string
		var e error
		if op != "accept" {
			workspace, e = collaborationOwnerSQL(ctx, tx, p, project)
			if e != nil {
				return e
			}
		}
		ret, saved, e := collaborationReceiptSQL(ctx, tx, p.User.ID, op, key)
		if e == nil {
			if !bytes.Equal(saved, hash[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原键已用于不同的协作操作")
			}
			out = ret
			replay = true
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if op == "accept" {
			target = p.User.ID
			e = tx.QueryRow(ctx, `SELECT q.id FROM studio_projects q JOIN studio_collaboration_members m ON m.project_id=q.id
				WHERE m.invitation_id=$1 AND m.target_user_id=$2 FOR NO KEY UPDATE OF q`, in.Invitation, target).Scan(&project)
			if e != nil {
				return collaborationReadError(e)
			}
		}
		old, e := collaborationEntrySQL(ctx, tx, project, target)
		exists := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if op != "invite" && !exists {
			return collaborationMissing()
		}
		if old.Version != in.ExpectedVersion || op != "invite" && old.InvitationID != in.Invitation {
			return collaborationConflict()
		}
		if op == "invite" {
			if target == p.User.ID {
				return failure(422, "INVALID_COLLABORATION", "项目所有者无需邀请自己")
			}
			var known bool
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_users WHERE id=$1)", target).Scan(&known); e != nil {
				return e
			}
			if !known {
				return collaborationMissing()
			}
			e = issueCollaborationSQL(ctx, tx, project, workspace, target, p.User.ID, in.Role, old, exists)
		} else {
			e = changeCollaborationSQL(ctx, tx, project, target, op, old)
		}
		if e != nil {
			return e
		}
		entry, e := collaborationEntrySQL(ctx, tx, project, target)
		if e != nil {
			return e
		}
		out = CollaborationReceipt{CollaborationEntry: entry, Operation: op}
		k := sha256.Sum256([]byte(key))
		_, e = tx.Exec(ctx, `INSERT INTO studio_collaboration_receipts(actor_id,operation,key_hash,request_hash,project_id,target_user_id,version,response)
		  VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.User.ID, op, k[:], hash[:], project, target, entry.Version, out)
		return e
	})
	if err != nil {
		return CollaborationReceipt{}, false, err
	}
	return
}

func issueCollaborationSQL(ctx context.Context, tx pgx.Tx, project, workspace, target, actor, role string, old CollaborationEntry, exists bool) error {
	if exists && old.State != "EXPIRED" && old.State != "REVOKED" && old.State != "WITHDRAWN" {
		return collaborationConflict()
	}
	if exists && (old.Round >= 128 || old.Version > 508) {
		return collaborationCapacity()
	}
	// Actor/recipient and project locks are held across admission. Separate
	// indexed branches bound both active and unexpired pending work; archived
	// or expired relationships never count against either live quota.
	var projectCount, userCount int
	e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM (
		  SELECT 1 FROM studio_collaboration_members WHERE project_id=$1 AND state='ACTIVE'
		  UNION ALL SELECT 1 FROM studio_collaboration_members WHERE project_id=$1 AND state='PENDING' AND expires_at>statement_timestamp() LIMIT 64) live_project),
		 (SELECT count(*) FROM (
		  SELECT 1 FROM studio_collaboration_members WHERE target_user_id=$2 AND state='ACTIVE'
		  UNION ALL SELECT 1 FROM studio_collaboration_members WHERE target_user_id=$2 AND state='PENDING' AND expires_at>statement_timestamp() LIMIT 128) live_recipient)`,
		project, target).Scan(&projectCount, &userCount)
	if e != nil {
		return e
	}
	if projectCount >= 64 || userCount >= 128 {
		return collaborationCapacity()
	}
	id := newID("pci")
	if !exists {
		_, e = tx.Exec(ctx, `INSERT INTO studio_collaboration_members(project_id,workspace_id,target_user_id,invitation_id,inviter_id,role,state,round,version,expires_at)
		 VALUES($1,$2,$3,$4,$5,$6,'PENDING',1,1,clock_timestamp()+interval '72 hours')`, project, workspace, target, id, actor, role)
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE studio_collaboration_members SET invitation_id=$3,inviter_id=$4,role=$5,state='PENDING',round=round+1,version=version+1,
	 expires_at=clock_timestamp()+interval '72 hours',updated_at=clock_timestamp() WHERE project_id=$1 AND target_user_id=$2`, project, target, id, actor, role)
	return e
}
func changeCollaborationSQL(ctx context.Context, tx pgx.Tx, project, target, op string, old CollaborationEntry) error {
	state, role := old.State, old.Role
	switch op {
	case "accept", "withdraw":
		if state != "PENDING" {
			return collaborationConflict()
		}
		state = "ACTIVE"
		if op == "withdraw" {
			state = "WITHDRAWN"
		}
	case "revoke":
		if state != "ACTIVE" {
			return collaborationConflict()
		}
		state = "REVOKED"
	case "downgrade":
		if state != "ACTIVE" || role != "editor" {
			return collaborationConflict()
		}
		role = "viewer"
	default:
		return collaborationMissing()
	}
	tag, e := tx.Exec(ctx, `UPDATE studio_collaboration_members SET state=$3,role=$4,version=version+1,updated_at=clock_timestamp()
	 WHERE project_id=$1 AND target_user_id=$2 AND version=$5 AND (state<>'PENDING' OR expires_at>clock_timestamp())`, project, target, state, role, old.Version)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return collaborationConflict()
	}
	return nil
}
