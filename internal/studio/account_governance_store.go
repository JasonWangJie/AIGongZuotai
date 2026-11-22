package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type AccountAccess struct {
	TargetUserID string                 `json:"target_user_id"`
	State        string                 `json:"state"`
	Version      uint64                 `json:"version"`
	Restriction  *GenerationRestriction `json:"restriction,omitempty"`
	CanAppeal    bool                   `json:"can_appeal"`
}
type AccountAppeal struct {
	ID            string    `json:"id"`
	TargetUserID  string    `json:"target_user_id"`
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
type AccountAppealPage struct {
	Items      []AccountAppeal `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}
type accountTargetGrant struct {
	ID      string
	Version uint64
}

func accountChanged() error {
	return failure(409, "ACCOUNT_CONTROL_CHANGED", "账户限制轮次、申请或授权已变化，请刷新并重新预演")
}
func accountNotFound() error {
	return failure(404, "ACCOUNT_GOVERNANCE_NOT_FOUND", "未找到已授权的记录")
}
func accountReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return accountNotFound()
	}
	return err
}

// A read without an eligible grant never looks up or locks a foreign user.
// Reciprocal grants lock both identities in the same database order before
// session, restricted-account and grant checks under lock.
func authorizeAccountTargetSQL(ctx context.Context, tx pgx.Tx, p *principal, target string, ops bool) (g accountTargetGrant, password passwordSnapshot, err error) {
	if !validOperationsID(target, "usr") {
		return g, password, operationsDenied()
	}
	if !ops {
		if p.User.ID != target {
			return g, password, operationsDenied()
		}
		password, err = operationsSessionSQL(ctx, tx, p)
		return
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_account_governance_grants WHERE operator_user_id=$1 AND target_user_id=$2 AND active)`, p.User.ID, target).Scan(&eligible); err != nil {
		return
	}
	if !eligible || p.User.ID == target {
		return g, password, operationsDenied()
	}
	rows, e := tx.Query(ctx, `SELECT id FROM studio_users WHERE id IN($1,$2) ORDER BY id FOR UPDATE`, p.User.ID, target)
	if e != nil {
		return g, password, e
	}
	ids, e := pgx.CollectRows(rows, pgx.RowTo[string])
	if e != nil {
		return g, password, e
	}
	if len(ids) != 2 {
		return g, password, operationsDenied()
	}
	password, err = operationsSessionSQL(ctx, tx, p)
	if err != nil {
		return
	}
	var suspended bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_account_controls WHERE user_id=$1 AND state='SUSPENDED')`, p.User.ID).Scan(&suspended); err != nil {
		return
	}
	if suspended {
		return g, password, operationsDenied()
	}
	err = tx.QueryRow(ctx, `SELECT id,version FROM studio_account_governance_grants WHERE operator_user_id=$1 AND target_user_id=$2 AND active FOR SHARE`, p.User.ID, target).Scan(&g.ID, &g.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = operationsDenied()
	}
	return
}

func readAccountAccessSQL(ctx context.Context, tx pgx.Tx, target string) (AccountAccess, error) {
	v := AccountAccess{TargetUserID: target, State: "OPEN", Version: 1}
	var rid string
	err := tx.QueryRow(ctx, `SELECT state,version,coalesce(active_restriction_id,'') FROM studio_account_controls WHERE user_id=$1`, target).Scan(&v.State, &v.Version, &rid)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil || rid == "" {
		return v, err
	}
	r := &GenerationRestriction{}
	err = tx.QueryRow(ctx, `SELECT r.id,r.reason_code,r.public_notice,r.created_at,NOT EXISTS(SELECT 1 FROM studio_account_appeals a WHERE a.restriction_id=r.id AND a.applicant_id=r.target_user_id) FROM studio_account_restrictions r WHERE r.id=$1 AND r.target_user_id=$2 AND r.released_at IS NULL`, rid, target).Scan(&r.ID, &r.ReasonCode, &r.PublicNotice, &r.CreatedAt, &v.CanAppeal)
	v.Restriction = r
	return v, err
}
func (a *App) accountGovernanceAccess(p *principal, target string, ops bool) (v AccountAccess, err error) {
	err = a.contentAppealTx(p, ops, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, e := authorizeAccountTargetSQL(ctx, tx, p, target, ops); e != nil {
			return e
		}
		var e error
		v, e = readAccountAccessSQL(ctx, tx, target)
		return e
	})
	return
}
func accountAuditSQL(ctx context.Context, tx pgx.Tx, actor, target, action, resource string, version uint64) error {
	_, e := tx.Exec(ctx, `INSERT INTO studio_account_governance_audit(id,actor_user_id,target_user_id,action,resource_id,version) VALUES($1,$2,$3,$4,$5,$6)`, newID("aaud"), actor, target, action, resource, version)
	return e
}

const accountAppealColumns = `id,target_user_id,restriction_id,state,outcome,version,reason_code,created_at,updated_at`

func scanAccountAppeal(row libraryScanner, detail bool) (AccountAppeal, error) {
	var v AccountAppeal
	args := []any{&v.ID, &v.TargetUserID, &v.RestrictionID, &v.State, &v.Outcome, &v.Version, &v.ReasonCode, &v.CreatedAt, &v.UpdatedAt}
	if detail {
		args = append(args, &v.Statement, &v.PublicReply)
	}
	err := accountReadError(row.Scan(args...))
	return v, err
}
func accountAppealSQL(ctx context.Context, tx pgx.Tx, target, id string) (AccountAppeal, error) {
	return scanAccountAppeal(tx.QueryRow(ctx, `SELECT `+accountAppealColumns+`,statement,public_reply FROM studio_account_appeals WHERE target_user_id=$1 AND id=$2`, target, id), true)
}
func (a *App) readAccountAppeal(p *principal, target, id string, ops bool) (v AccountAppeal, err error) {
	err = a.contentAppealTx(p, ops, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, e := authorizeAccountTargetSQL(ctx, tx, p, target, ops); e != nil {
			return e
		}
		var e error
		v, e = accountAppealSQL(ctx, tx, target, id)
		return e
	})
	return
}
func accountKeySQL(ctx context.Context, tx pgx.Tx, target, key string) (id string, h []byte, err error) {
	k := sha256.Sum256([]byte(key))
	err = tx.QueryRow(ctx, `SELECT appeal_id,request_hash FROM studio_account_appeal_idempotency WHERE target_user_id=$1 AND applicant_id=$1 AND key_hash=$2`, target, k[:]).Scan(&id, &h)
	return
}
func (a *App) accountAppealReceipt(p *principal, key string) (v AccountAppeal, err error) {
	err = a.contentAppealTx(p, false, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, e := authorizeAccountTargetSQL(ctx, tx, p, p.User.ID, false); e != nil {
			return e
		}
		id, _, e := accountKeySQL(ctx, tx, p.User.ID, key)
		if e != nil {
			return accountReadError(e)
		}
		v, e = accountAppealSQL(ctx, tx, p.User.ID, id)
		return e
	})
	return
}
func (a *App) createAccountAppeal(p *principal, rid string, in GenerationAppealInput, key string) (v AccountAppeal, replayed bool, err error) {
	if !validOperationsID(rid, "arst") || !validGenerationAppealInput(in) || !validOperationsOpaque(key) || len(key) < 8 {
		return v, false, failure(422, "INVALID_ACCOUNT_APPEAL", "请核对限制轮次、申诉理由和原键")
	}
	h := payloadHash(struct {
		Restriction string
		Input       GenerationAppealInput
	}{rid, in})
	err = a.contentAppealTx(p, false, func(ctx context.Context, tx pgx.Tx) error {
		target := p.User.ID
		if _, _, e := authorizeAccountTargetSQL(ctx, tx, p, target, false); e != nil {
			return e
		}
		old, oldHash, e := accountKeySQL(ctx, tx, target, key)
		if e == nil {
			if !bytes.Equal(oldHash, h[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原键已用于另一份申请")
			}
			replayed = true
			v, e = accountAppealSQL(ctx, tx, target, old)
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		access, e := readAccountAccessSQL(ctx, tx, target)
		if e != nil {
			return e
		}
		if access.State != "SUSPENDED" || access.Restriction == nil || access.Restriction.ID != rid {
			return accountChanged()
		}
		if !access.CanAppeal {
			return failure(409, "ACCOUNT_APPEAL_EXISTS", "此轮限制已有申请，请读取原申请")
		}
		// Each bounded restriction round admits exactly one appeal.
		v, e = scanAccountAppeal(tx.QueryRow(ctx, `INSERT INTO studio_account_appeals(id,target_user_id,restriction_id,applicant_id,reason_code,statement) VALUES($1,$2,$3,$2,$4,$5) RETURNING `+accountAppealColumns+`,statement,public_reply`, newID("aapl"), target, rid, in.ReasonCode, in.Statement), true)
		if e != nil {
			return e
		}
		k := sha256.Sum256([]byte(key))
		if _, e = tx.Exec(ctx, `INSERT INTO studio_account_appeal_idempotency(target_user_id,applicant_id,key_hash,request_hash,appeal_id) VALUES($1,$1,$2,$3,$4)`, target, k[:], h[:], v.ID); e != nil {
			return e
		}
		return accountAuditSQL(ctx, tx, target, target, "appeal.created", v.ID, 1)
	})
	if err != nil {
		return AccountAppeal{}, false, err
	}
	return
}

type accountPageCursor struct {
	Actor, Target, State, ID string
	Ops                      bool
	Before                   time.Time
}

func (a *App) listAccountAppeals(p *principal, target, state, cursor string, limit int, ops bool) (out AccountAppealPage, err error) {
	out.Items = []AccountAppeal{}
	if state == "" {
		state = "all"
	}
	if limit == 0 {
		limit = 20
	}
	if (state != "all" && state != "OPEN" && state != "RESOLVED") || limit < 1 || limit > 50 {
		return out, failure(400, "INVALID_FILTER", "申诉状态或页大小无效")
	}
	err = a.contentAppealTx(p, ops, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, e := authorizeAccountTargetSQL(ctx, tx, p, target, ops); e != nil {
			return e
		}
		c := accountPageCursor{Actor: p.User.ID, Target: target, State: state, Ops: ops}
		if cursor != "" {
			var got accountPageCursor
			if len(cursor) > 2048 {
				return failure(400, "INVALID_CURSOR", "分页游标无效")
			}
			b, e := base64.RawURLEncoding.DecodeString(cursor)
			if e != nil || json.Unmarshal(b, &got) != nil || got.Actor != c.Actor || got.Target != target || got.State != state || got.Ops != ops || !validOperationsID(got.ID, "aapl") || got.Before.Year() < 2000 || got.Before.Year() > 9999 {
				return failure(400, "INVALID_CURSOR", "游标与当前账户或筛选不匹配")
			}
			c = got
		}
		rows, e := tx.Query(ctx, `SELECT `+accountAppealColumns+` FROM studio_account_appeals WHERE target_user_id=$1 AND ($2='all' OR state=$2) AND ($3::timestamptz='0001-01-01T00:00:00Z' OR (created_at,id)<($3,$4)) ORDER BY created_at DESC,id DESC LIMIT $5`, target, state, c.Before, c.ID, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			item, e := scanAccountAppeal(rows, false)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, item)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			c.ID, c.Before = last.ID, last.CreatedAt
			b, _ := json.Marshal(c)
			n := base64.RawURLEncoding.EncodeToString(b)
			out.NextCursor = &n
		}
		return nil
	})
	return
}
