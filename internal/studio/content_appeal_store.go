package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ContentAppealInput struct {
	ProjectID         string `json:"project_id"`
	ResourceType      string `json:"resource_type"`
	ResourceID        string `json:"resource_id"`
	ModerationVersion uint64 `json:"moderation_version"`
	ReasonCode        string `json:"reason_code"`
	Statement         string `json:"statement,omitempty"`
}
type ContentAppeal struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ContentAppealInput
	ResourceVersion                   uint64                      `json:"resource_version"`
	Decision                          string                      `json:"decision"`
	DecisionReason                    string                      `json:"decision_reason"`
	PolicyID                          string                      `json:"policy_id"`
	PolicyVersion                     string                      `json:"policy_version"`
	State                             string                      `json:"state"`
	Outcome                           string                      `json:"outcome,omitempty"`
	Version                           uint64                      `json:"version"`
	PublicReply                       string                      `json:"public_reply,omitempty"`
	CreatedAt                         time.Time                   `json:"created_at"`
	UpdatedAt                         time.Time                   `json:"updated_at"`
	Finance                           *ContentAppealFinanceStatus `json:"finance,omitempty"`
	applicant, evidence, policyDigest string
}
type ContentAppealPage struct {
	Items      []ContentAppeal `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

func contentAppealMissing() error {
	return failure(404, "CONTENT_APPEAL_NOT_FOUND", "没有可访问的申请或已执行输出审核证据")
}
func contentAppealChanged() error {
	return failure(409, "CONTENT_APPEAL_CHANGED", "原决定、申诉或冻结状态已变化，请刷新核对")
}
func appealReadError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return contentAppealMissing()
	}
	return e
}

const contentAppealColumns = `id,workspace_id,project_id,applicant_id,resource_type,resource_id,resource_version,moderation_version,decision,decision_reason,policy_id,policy_version,policy_digest,evidence_digest,reason_code,state,version,outcome,created_at,updated_at`

func scanContentAppeal(row libraryScanner, detail bool) (ContentAppeal, error) {
	var v ContentAppeal
	dst := []any{&v.ID, &v.WorkspaceID, &v.ProjectID, &v.applicant, &v.ResourceType, &v.ResourceID, &v.ResourceVersion, &v.ModerationVersion, &v.Decision, &v.DecisionReason, &v.PolicyID, &v.PolicyVersion, &v.policyDigest, &v.evidence, &v.ReasonCode, &v.State, &v.Version, &v.Outcome, &v.CreatedAt, &v.UpdatedAt}
	if detail {
		dst = append(dst, &v.Statement, &v.PublicReply)
	}
	err := row.Scan(dst...)
	return v, appealReadError(err)
}
func contentAppealSQL(ctx context.Context, tx pgx.Tx, workspace, project, id, owner string, lock bool) (ContentAppeal, error) {
	q := "SELECT " + contentAppealColumns + ",statement,public_reply FROM studio_content_appeals WHERE workspace_id=$1 AND id=$2 AND ($3='' OR project_id=$3) AND ($4='' OR applicant_id=$4)"
	if lock {
		q += " FOR UPDATE"
	}
	return scanContentAppeal(tx.QueryRow(ctx, q, workspace, id, project, owner), true)
}
func validContentAppeal(in ContentAppealInput) bool {
	return validOperationsID(in.ProjectID, "prj") && (in.ResourceType == "run" || in.ResourceType == "task") && validOperationsID(in.ResourceID, in.ResourceType) && in.ModerationVersion > 1 && in.ModerationVersion <= generationMaxVersion &&
		(in.ReasonCode == "decision_error" || in.ReasonCode == "request_review" || in.ReasonCode == "billing_concern") && validGenerationText(in.Statement, false)
}

// The only admissible owner is the actor on the ordinary atomic acceptance
// audit. Neither a project membership nor possession of a resource id proves it.
func sourceContentAppealSQL(ctx context.Context, tx pgx.Tx, workspace, user string, in ContentAppealInput) (ContentAppeal, error) {
	x := ContentAppeal{WorkspaceID: workspace, ContentAppealInput: in, applicant: user}
	var source string
	q := `SELECT (r.document->>'seq')::bigint,m.version,m.state,m.reason_code,m.policy_id,m.policy_version,m.policy_digest,
	  COALESCE(s.execution_id,'legacy-output-decision')
	 FROM studio_runs r JOIN studio_run_moderation m ON m.run_id=r.id JOIN studio_moderation_decisions d ON d.run_id=r.id AND d.version=m.version AND d.state=m.state AND d.reason_code=m.reason_code
	 LEFT JOIN studio_run_streams s ON s.run_id=r.id WHERE ((s.closed AND s.execution_id<>'') OR
	  (s.run_id IS NULL AND d.created_at < (SELECT applied_at FROM studio_schema_versions WHERE version=21)
	   AND r.document->'error'->>'code' IN ('MODERATION_OUTPUT_BLOCKED','MODERATION_UNAVAILABLE'))) `
	if in.ResourceType == "task" {
		q = `SELECT (r.document->>'version')::bigint,m.version,m.state,m.reason_code,m.policy_id,m.policy_version,m.policy_digest,encode(sha256(convert_to(c.candidates::text,'UTF8')),'hex')
		 FROM studio_tasks r JOIN studio_task_media_moderation m ON m.task_id=r.id JOIN studio_media_moderation_decisions d ON d.task_id=r.id AND d.version=m.version AND d.state=m.state AND d.reason_code=m.reason_code
		 JOIN studio_task_media_candidates c ON c.task_id=r.id WHERE true `
	}
	q += ` AND r.id=$1 AND r.workspace_id=$2 AND r.project_id=$3 AND r.document->>'status'='FAILED'
	 AND m.state IN ('BLOCKED','REVIEW') AND m.reason_code IN('policy_block','policy_review','check_unavailable','check_protocol','check_timeout','check_busy','invalid_content','content_too_large','policy_unavailable')
	 AND EXISTS(SELECT 1 FROM studio_audit_events a WHERE a.workspace_id=r.workspace_id AND a.project_id=r.project_id AND a.resource_type=$4 AND a.resource_id=r.id AND a.actor_type='user' AND a.actor_id=$5 AND a.action IN('run.accepted','task.accepted') AND a.action=($4||'.accepted'))`
	err := tx.QueryRow(ctx, q, in.ResourceID, workspace, in.ProjectID, in.ResourceType, user).Scan(&x.ResourceVersion, &x.ModerationVersion, &x.Decision, &x.DecisionReason, &x.PolicyID, &x.PolicyVersion, &x.policyDigest, &source)
	if err != nil {
		return x, appealReadError(err)
	}
	if x.ModerationVersion != in.ModerationVersion {
		return x, contentAppealChanged()
	}
	identity := payloadHash([]any{workspace, in.ProjectID, user, in.ResourceType, in.ResourceID, x.ModerationVersion, x.Decision, x.DecisionReason, x.PolicyID, x.PolicyVersion, x.policyDigest, source})
	x.evidence = hex.EncodeToString(identity[:])
	return x, nil
}

type appealReservation struct {
	ID        string
	Version   uint64
	Remaining int64
}

func contentAppealHoldSQL(ctx context.Context, tx pgx.Tx, x ContentAppeal) (appealReservation, error) {
	var v appealReservation
	err := tx.QueryRow(ctx, `SELECT id,version,reserved_micros-charged_micros-released_micros FROM studio_ledger_reservations WHERE workspace_id=$1 AND project_id=$2 AND resource_type=$3 AND resource_id=$4 AND state='REVIEW' AND reserved_micros>charged_micros+released_micros FOR SHARE`, x.WorkspaceID, x.ProjectID, x.ResourceType, x.ResourceID).Scan(&v.ID, &v.Version, &v.Remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	return v, err
}
func (a *App) contentAppealTx(p *principal, ops bool, fn func(context.Context, pgx.Tx) error) error {
	if err := a.generationPrincipal(p, ops); err != nil {
		return err
	}
	return a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error { return fn(ctx, tx) })
	})
}
func contentAppealUser(ctx context.Context, tx pgx.Tx, p *principal, workspace, project string) error {
	if p.Workspace.ID != workspace || !validOperationsID(project, "prj") {
		return contentAppealMissing()
	}
	if _, err := operationsSessionSQL(ctx, tx, p); err != nil {
		return err
	}
	var id string
	return appealReadError(tx.QueryRow(ctx, "SELECT id FROM studio_projects WHERE workspace_id=$1 AND id=$2", workspace, project).Scan(&id))
}
func contentAppealKeySQL(ctx context.Context, tx pgx.Tx, p *principal, project, key string) (string, []byte, error) {
	var id string
	var hash []byte
	d := sha256.Sum256([]byte(key))
	err := tx.QueryRow(ctx, "SELECT appeal_id,request_hash FROM studio_content_appeal_keys WHERE applicant_id=$1 AND workspace_id=$2 AND project_id=$3 AND key_hash=$4", p.User.ID, p.Workspace.ID, project, d[:]).Scan(&id, &hash)
	return id, hash, err
}
func (a *App) createContentAppeal(p *principal, in ContentAppealInput, key string) (ContentAppeal, bool, error) {
	var out ContentAppeal
	var replay bool
	if !validContentAppeal(in) || !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_CONTENT_APPEAL", "原资源类型、审核版本、理由或陈述无效")
	}
	err := a.contentAppealTx(p, false, func(ctx context.Context, tx pgx.Tx) error {
		ws := p.Workspace.ID
		if err := contentAppealUser(ctx, tx, p, ws, in.ProjectID); err != nil {
			return err
		}
		hash := payloadHash(in)
		id, original, err := contentAppealKeySQL(ctx, tx, p, in.ProjectID, key)
		if err == nil {
			if !bytes.Equal(hash[:], original) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原申诉键对应不同内容")
			}
			out, err = contentAppealSQL(ctx, tx, ws, in.ProjectID, id, p.User.ID, false)
			replay = true
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		x, err := sourceContentAppealSQL(ctx, tx, ws, p.User.ID, in)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x4341505045414c)); err != nil {
			return err
		}
		var existing bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_content_appeals WHERE applicant_id=$1 AND workspace_id=$2 AND project_id=$3 AND resource_type=$4 AND resource_id=$5 AND moderation_version=$6)", p.User.ID, ws, in.ProjectID, in.ResourceType, in.ResourceID, in.ModerationVersion).Scan(&existing); err != nil {
			return err
		}
		if existing {
			return failure(409, "CONTENT_APPEAL_EXISTS", "这项原决定已有申诉，请查看原申请")
		}
		var own, total int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE workspace_id=$1),count(*) FROM (SELECT workspace_id FROM studio_content_appeals LIMIT 100001) s`, ws).Scan(&own, &total); err != nil {
			return err
		}
		if own >= 1024 || total >= 100000 {
			return failure(429, "CONTENT_APPEAL_LIMIT", "申诉容量已满，已登记申请可继续处理")
		}
		v, err := contentAppealHoldSQL(ctx, tx, x)
		if err != nil {
			return err
		}
		id = newID("capl")
		_, err = tx.Exec(ctx, `INSERT INTO studio_content_appeals(id,workspace_id,project_id,applicant_id,resource_type,resource_id,resource_version,moderation_version,decision,decision_reason,policy_id,policy_version,policy_digest,evidence_digest,reason_code,statement,reservation_id,reservation_version,remaining_micros) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NULLIF($17,''),$18,$19)`, id, ws, in.ProjectID, p.User.ID, in.ResourceType, in.ResourceID, x.ResourceVersion, x.ModerationVersion, x.Decision, x.DecisionReason, x.PolicyID, x.PolicyVersion, x.policyDigest, x.evidence, in.ReasonCode, in.Statement, v.ID, v.Version, v.Remaining)
		if err != nil {
			return err
		}
		k := sha256.Sum256([]byte(key))
		if _, err = tx.Exec(ctx, "INSERT INTO studio_content_appeal_keys(applicant_id,workspace_id,project_id,key_hash,request_hash,appeal_id) VALUES($1,$2,$3,$4,$5,$6)", p.User.ID, ws, in.ProjectID, k[:], hash[:], id); err != nil {
			return err
		}
		if err = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: ws, Action: "content_appeal.created", ResourceType: "appeal", ResourceID: id, Version: 1, ReasonCode: in.ReasonCode}); err != nil {
			return err
		}
		out, err = contentAppealSQL(ctx, tx, ws, in.ProjectID, id, p.User.ID, false)
		return err
	})
	if err != nil {
		return ContentAppeal{}, false, err
	}
	return out, replay, nil
}
func (a *App) readContentAppeal(p *principal, workspace, project, id string, ops bool) (out ContentAppeal, err error) {
	err = a.contentAppealTx(p, ops, func(ctx context.Context, tx pgx.Tx) error {
		owner := p.User.ID
		if ops {
			if _, e := authorizeOperationsSQL(ctx, tx, p, workspace, "content_appeal_review"); e != nil {
				return e
			}
			owner = ""
		} else if e := contentAppealUser(ctx, tx, p, workspace, project); e != nil {
			return e
		}
		var e error
		out, e = contentAppealSQL(ctx, tx, workspace, project, id, owner, false)
		if e != nil {
			return e
		}
		out.Finance, e = readContentAppealFinanceStatusSQL(ctx, tx, workspace, id)
		return e
	})
	return
}
func (a *App) contentAppealReceipt(p *principal, project, key string) (out ContentAppeal, err error) {
	err = a.contentAppealTx(p, false, func(ctx context.Context, tx pgx.Tx) error {
		if e := contentAppealUser(ctx, tx, p, p.Workspace.ID, project); e != nil {
			return e
		}
		id, _, e := contentAppealKeySQL(ctx, tx, p, project, key)
		if e != nil {
			return appealReadError(e)
		}
		out, e = contentAppealSQL(ctx, tx, p.Workspace.ID, project, id, p.User.ID, false)
		if e != nil {
			return e
		}
		out.Finance, e = readContentAppealFinanceStatusSQL(ctx, tx, p.Workspace.ID, id)
		return e
	})
	return
}

type contentAppealCursor struct {
	User, Workspace, Project, State, ID string
	Ops                                 bool
	Before                              time.Time
}

func (a *App) listContentAppeals(p *principal, workspace, project, state, cursor string, limit int, ops bool) (ContentAppealPage, error) {
	out := ContentAppealPage{Items: []ContentAppeal{}}
	if state == "" {
		state = "all"
	}
	if limit == 0 {
		limit = 20
	}
	if (state != "all" && state != "OPEN" && state != "RESOLVED") || limit < 1 || limit > 50 {
		return out, failure(400, "INVALID_FILTER", "申诉状态或页大小无效")
	}
	err := a.contentAppealTx(p, ops, func(ctx context.Context, tx pgx.Tx) error {
		owner := p.User.ID
		if ops {
			if _, e := authorizeOperationsSQL(ctx, tx, p, workspace, "content_appeal_review"); e != nil {
				return e
			}
			owner = ""
		} else if e := contentAppealUser(ctx, tx, p, workspace, project); e != nil {
			return e
		}
		scope := contentAppealCursor{User: p.User.ID, Workspace: workspace, Project: project, State: state, Ops: ops}
		if cursor != "" {
			var cur contentAppealCursor
			if len(cursor) > 2048 {
				return failure(400, "INVALID_CURSOR", "申诉游标无效")
			}
			b, e := base64.RawURLEncoding.DecodeString(cursor)
			if e != nil || json.Unmarshal(b, &cur) != nil || cur.User != scope.User || cur.Workspace != workspace || cur.Project != project || cur.State != state || cur.Ops != ops || !validOperationsID(cur.ID, "capl") || cur.Before.Year() < 2000 || cur.Before.Year() > 9999 {
				return failure(400, "INVALID_CURSOR", "申诉游标与身份或范围不符")
			}
			scope = cur
		}
		rows, e := tx.Query(ctx, `SELECT `+contentAppealColumns+` FROM studio_content_appeals WHERE workspace_id=$1 AND ($2='' OR project_id=$2) AND ($3='' OR applicant_id=$3) AND ($4='all' OR state=$4) AND ($5::timestamptz='0001-01-01T00:00:00Z' OR (created_at,id)<($5,$6)) ORDER BY created_at DESC,id DESC LIMIT $7`, workspace, project, owner, state, scope.Before, scope.ID, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanContentAppeal(rows, false)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			scope.Before, scope.ID = last.CreatedAt, last.ID
			b, _ := json.Marshal(scope)
			n := base64.RawURLEncoding.EncodeToString(b)
			out.NextCursor = &n
		}
		return nil
	})
	return out, err
}
