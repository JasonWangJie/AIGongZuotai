package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const generationMaxVersion uint64 = 9007199254740991

// These are public projections. Private evidence and operator identities never
// enter a user's status, appeal, or project notification feed.
type GenerationRestriction struct {
	ID           string    `json:"id"`
	ReasonCode   string    `json:"reason_code"`
	PublicNotice string    `json:"public_notice"`
	CreatedAt    time.Time `json:"created_at"`
}
type GenerationAccess struct {
	WorkspaceID string                 `json:"workspace_id"`
	State       string                 `json:"state"`
	Version     uint64                 `json:"version"`
	Restriction *GenerationRestriction `json:"restriction,omitempty"`
	CanAppeal   bool                   `json:"can_appeal"`
}
type GenerationAppealInput struct {
	ReasonCode string `json:"reason_code"`
	Statement  string `json:"statement"`
}
type GenerationAppeal struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	RestrictionID string    `json:"restriction_id"`
	State         string    `json:"state"`
	Outcome       string    `json:"outcome,omitempty"`
	Version       uint64    `json:"version"`
	ReasonCode    string    `json:"reason_code"`
	Statement     string    `json:"statement,omitempty"`
	PublicReply   string    `json:"public_reply,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type GenerationAppealPage struct {
	Items      []GenerationAppeal `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

func generationNotFound() error {
	return failure(404, "NOT_FOUND", "生成限制或复核申请不存在")
}
func validGenerationText(s string, required bool) bool {
	if !utf8.ValidString(s) || len(s) > 1024 || required && strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) && r != '\n' {
			return false
		}
	}
	return true
}
func validGenerationAppealInput(in GenerationAppealInput) bool {
	return (in.ReasonCode == "request_review" || in.ReasonCode == "restriction_error" || in.ReasonCode == "issue_resolved") && validGenerationText(in.Statement, false)
}

// A stable workspace lock serializes the first restriction against new
// acceptance even when no control row exists. Readers never create rows.
func lockGenerationWorkspaceSQL(ctx context.Context, tx pgx.Tx, workspace string, exclusive bool) error {
	if workspace != "ws_demo" && !validOperationsID(workspace, "ws") {
		return generationNotFound()
	}
	clause := " FOR SHARE"
	if exclusive {
		clause = " FOR UPDATE"
	}
	var id string
	err := tx.QueryRow(ctx, "SELECT id FROM studio_workspaces WHERE id=$1"+clause, workspace).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return generationNotFound()
	}
	return err
}
func readGenerationAccessSQL(ctx context.Context, q sqlQueryRower, workspace string) (GenerationAccess, error) {
	v := GenerationAccess{WorkspaceID: workspace, State: "OPEN", Version: 1}
	var restrictionID string
	err := q.QueryRow(ctx, `SELECT state,version,COALESCE(active_restriction_id,'') FROM studio_generation_controls WHERE workspace_id=$1`, workspace).Scan(&v.State, &v.Version, &restrictionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if restrictionID != "" {
		r := &GenerationRestriction{}
		err = q.QueryRow(ctx, `SELECT id,reason_code,public_notice,created_at FROM studio_generation_restrictions WHERE workspace_id=$1 AND id=$2 AND released_at IS NULL`, workspace, restrictionID).Scan(&r.ID, &r.ReasonCode, &r.PublicNotice, &r.CreatedAt)
		if err != nil {
			return v, err
		}
		v.Restriction = r
	}
	return v, nil
}
func checkWorkspaceGenerationSQL(ctx context.Context, tx pgx.Tx, workspace string) error {
	if err := lockGenerationWorkspaceSQL(ctx, tx, workspace, false); err != nil {
		return err
	}
	v, err := readGenerationAccessSQL(ctx, tx, workspace)
	if err != nil {
		return err
	}
	if v.State == "RESTRICTED" {
		return failure(403, "GENERATION_RESTRICTED", "工作区暂不能发起新生成，可查看限制说明并申请复核；已受理请求可按原键查询")
	}
	return nil
}

func (a *App) generationPrincipal(p *principal, operations bool) error {
	if a.store == nil || a.config.AuthMode != "required" {
		return failure(503, "GENERATION_CONTROL_UNAVAILABLE", "生成限制与复核需要持久账户模式")
	}
	if p == nil || p.User == nil || !validOperationsID(p.User.ID, "usr") || !validOperationsID(p.Workspace.ID, "ws") {
		return failure(401, "AUTH_REQUIRED", "请先登录")
	}
	if operations && !a.config.OperationsEnabled {
		return operationsDenied()
	}
	return nil
}
func generationUserSQL(ctx context.Context, tx pgx.Tx, p *principal, workspace string) error {
	if p == nil || p.User == nil || p.Workspace.ID != workspace {
		return generationNotFound()
	}
	_, err := operationsSessionSQL(ctx, tx, p)
	return err
}
func (a *App) generationAccess(p *principal) (GenerationAccess, error) {
	var result GenerationAccess
	if err := a.generationPrincipal(p, false); err != nil {
		return result, err
	}
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if e = generationUserSQL(ctx, tx, p, p.Workspace.ID); e != nil {
			return e
		}
		if e = lockGenerationWorkspaceSQL(ctx, tx, p.Workspace.ID, false); e != nil {
			return e
		}
		result, e = readGenerationAccessSQL(ctx, tx, p.Workspace.ID)
		if e != nil {
			return e
		}
		if result.Restriction != nil {
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_generation_appeals WHERE workspace_id=$1 AND restriction_id=$2 AND applicant_id=$3)`, p.Workspace.ID, result.Restriction.ID, p.User.ID).Scan(&exists); e != nil {
				return e
			}
			result.CanAppeal = !exists
		}
		return tx.Commit(ctx)
	})
	return result, err
}
func (a *App) operationsGenerationAccess(p *principal, workspace string) (GenerationAccess, error) {
	var result GenerationAccess
	if err := a.generationPrincipal(p, true); err != nil {
		return result, err
	}
	if !validOperationsID(workspace, "ws") {
		return result, generationNotFound()
	}
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "generation_restrict"); e != nil {
			return e
		}
		if e = lockGenerationWorkspaceSQL(ctx, tx, workspace, false); e != nil {
			return e
		}
		result, e = readGenerationAccessSQL(ctx, tx, workspace)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return result, err
}

const generationAppealColumns = `id,workspace_id,restriction_id,state,outcome,version,reason_code,created_at,updated_at`

func scanGenerationAppeal(row pgx.Row, detail bool) (GenerationAppeal, error) {
	var v GenerationAppeal
	args := []any{&v.ID, &v.WorkspaceID, &v.RestrictionID, &v.State, &v.Outcome, &v.Version, &v.ReasonCode, &v.CreatedAt, &v.UpdatedAt}
	if detail {
		args = append(args, &v.Statement, &v.PublicReply)
	}
	err := row.Scan(args...)
	if errors.Is(err, pgx.ErrNoRows) {
		err = generationNotFound()
	}
	return v, err
}
func readGenerationAppealSQL(ctx context.Context, q sqlQueryRower, workspace, id, applicant string) (GenerationAppeal, error) {
	return scanGenerationAppeal(q.QueryRow(ctx, `SELECT `+generationAppealColumns+`,statement,public_reply FROM studio_generation_appeals WHERE workspace_id=$1 AND id=$2 AND ($3='' OR applicant_id=$3)`, workspace, id, applicant), true)
}
func (a *App) createGenerationAppeal(p *principal, restrictionID string, in GenerationAppealInput, key string) (GenerationAppeal, bool, error) {
	var result GenerationAppeal
	var replayed bool
	if err := a.generationPrincipal(p, false); err != nil {
		return result, false, err
	}
	if !validOperationsID(restrictionID, "grst") || !validGenerationAppealInput(in) || len(key) < 8 || len(key) > 128 || !ledgerOpaquePattern.MatchString(key) || strings.ContainsRune(key, '/') {
		return result, false, failure(422, "INVALID_GENERATION_APPEAL", "需要有效限制标识、复核原因及有界说明")
	}
	hash := payloadHash(struct {
		Restriction string
		Input       GenerationAppealInput
	}{restrictionID, in})
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if e = generationUserSQL(ctx, tx, p, p.Workspace.ID); e != nil {
			return e
		}
		if e = lockGenerationWorkspaceSQL(ctx, tx, p.Workspace.ID, false); e != nil {
			return e
		}
		var oldHash []byte
		var oldID string
		e = tx.QueryRow(ctx, `SELECT request_hash,appeal_id FROM studio_generation_appeal_idempotency WHERE workspace_id=$1 AND applicant_id=$2 AND idempotency_key=$3`, p.Workspace.ID, p.User.ID, key).Scan(&oldHash, &oldID)
		if e == nil {
			if string(oldHash) != string(hash[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "此幂等键已用于其他复核请求")
			}
			result, e = readGenerationAppealSQL(ctx, tx, p.Workspace.ID, oldID, p.User.ID)
			if e != nil {
				return e
			}
			replayed = true
			return tx.Commit(ctx)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		access, e := readGenerationAccessSQL(ctx, tx, p.Workspace.ID)
		if e != nil {
			return e
		}
		if access.State != "RESTRICTED" || access.Restriction == nil || access.Restriction.ID != restrictionID {
			return failure(409, "GENERATION_RESTRICTION_CHANGED", "当前限制已变化，请刷新状态")
		}
		var existing string
		e = tx.QueryRow(ctx, `SELECT id FROM studio_generation_appeals WHERE workspace_id=$1 AND restriction_id=$2 AND applicant_id=$3`, p.Workspace.ID, restrictionID, p.User.ID).Scan(&existing)
		if e == nil {
			return failure(409, "GENERATION_APPEAL_EXISTS", "这一轮限制已经提交复核，请读取原申请")
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		// Session authentication already serializes submissions by this user.
		// A bounded global create lock makes capacity checks safe across users.
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x47454e415050)); e != nil {
			return e
		}
		var own, total, episode int
		if e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE workspace_id=$1),count(*),count(*) FILTER(WHERE restriction_id=$2) FROM (SELECT workspace_id,restriction_id FROM studio_generation_appeals LIMIT 100001) a`, p.Workspace.ID, restrictionID).Scan(&own, &total, &episode); e != nil {
			return e
		}
		if own >= 10000 || total >= 100000 || episode >= 128 {
			return failure(429, "GENERATION_APPEAL_LIMIT", "复核记录容量已达上限，请联系运营；解除限制仍可执行")
		}
		id := newID("gapl")
		result, e = scanGenerationAppeal(tx.QueryRow(ctx, `INSERT INTO studio_generation_appeals(id,workspace_id,restriction_id,applicant_id,reason_code,statement) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+generationAppealColumns+`,statement,public_reply`, id, p.Workspace.ID, restrictionID, p.User.ID, in.ReasonCode, in.Statement), true)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO studio_generation_appeal_idempotency(workspace_id,applicant_id,idempotency_key,request_hash,appeal_id) VALUES($1,$2,$3,$4,$5)`, p.Workspace.ID, p.User.ID, key, hash[:], id); e != nil {
			return e
		}
		if e = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: p.Workspace.ID, Action: "appeal.created", ResourceType: "appeal", ResourceID: id, ReasonCode: in.ReasonCode, Version: 1}); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return GenerationAppeal{}, false, err
	}
	return result, replayed, nil
}
func (a *App) readGenerationAppeal(p *principal, workspace, id string, operations bool) (GenerationAppeal, error) {
	var result GenerationAppeal
	if err := a.generationPrincipal(p, operations); err != nil {
		return result, err
	}
	if !validOperationsID(workspace, "ws") || !validOperationsID(id, "gapl") {
		return result, generationNotFound()
	}
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		applicant := p.User.ID
		if operations {
			_, e = authorizeOperationsSQL(ctx, tx, p, workspace, "appeal_resolve")
			applicant = ""
		} else {
			e = generationUserSQL(ctx, tx, p, workspace)
		}
		if e != nil {
			return e
		}
		result, e = readGenerationAppealSQL(ctx, tx, workspace, id, applicant)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return result, err
}

type generationAppealCursor struct {
	Version                          int
	User, Workspace, Mode, State, ID string
	Before                           time.Time
}

func parseGenerationAppealCursor(raw string, scope generationAppealCursor) (generationAppealCursor, error) {
	if raw == "" {
		return scope, nil
	}
	if len(raw) > 2048 {
		return scope, failure(400, "INVALID_CURSOR", "复核分页游标无效")
	}
	blob, e := base64.RawURLEncoding.DecodeString(raw)
	var got generationAppealCursor
	if e != nil || json.Unmarshal(blob, &got) != nil || got.Version != scope.Version || got.User != scope.User || got.Workspace != scope.Workspace || got.Mode != scope.Mode || got.State != scope.State || !validOperationsID(got.ID, "gapl") || got.Before.Year() < 2000 || got.Before.Year() > 9999 {
		return scope, failure(400, "INVALID_CURSOR", "复核游标与身份、范围或筛选不匹配")
	}
	return got, nil
}
func (a *App) listGenerationAppeals(p *principal, workspace string, operations bool, state, cursor string, limit int) (GenerationAppealPage, error) {
	result := GenerationAppealPage{Items: []GenerationAppeal{}}
	if err := a.generationPrincipal(p, operations); err != nil {
		return result, err
	}
	if !validOperationsID(workspace, "ws") {
		return result, generationNotFound()
	}
	if state == "" {
		state = "all"
	}
	if limit == 0 {
		limit = 20
	}
	if state != "all" && state != "OPEN" && state != "RESOLVED" || limit < 1 || limit > 50 {
		return result, failure(400, "INVALID_FILTER", "复核筛选或分页大小无效")
	}
	mode := "user"
	if operations {
		mode = "operations"
	}
	scope, err := parseGenerationAppealCursor(cursor, generationAppealCursor{Version: 1, User: p.User.ID, Workspace: workspace, Mode: mode, State: state})
	if err != nil {
		return result, err
	}
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		applicant := p.User.ID
		if operations {
			_, e = authorizeOperationsSQL(ctx, tx, p, workspace, "appeal_resolve")
			applicant = ""
		} else {
			e = generationUserSQL(ctx, tx, p, workspace)
		}
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+generationAppealColumns+` FROM studio_generation_appeals WHERE workspace_id=$1 AND ($2='' OR applicant_id=$2) AND ($3='all' OR state=$3) AND ($4::timestamptz='0001-01-01T00:00:00Z' OR (created_at,id)<($4,$5)) ORDER BY created_at DESC,id DESC LIMIT $6`, workspace, applicant, state, scope.Before, scope.ID, limit+1)
		if e != nil {
			return e
		}
		for rows.Next() {
			v, scanErr := scanGenerationAppeal(rows, false)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			result.Items = append(result.Items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		return GenerationAppealPage{}, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		last := result.Items[len(result.Items)-1]
		scope.ID = last.ID
		scope.Before = last.CreatedAt
		b, _ := json.Marshal(scope)
		s := base64.RawURLEncoding.EncodeToString(b)
		result.NextCursor = &s
	}
	return result, nil
}
