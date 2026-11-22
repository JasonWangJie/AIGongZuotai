package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type SupportTicketCreate struct {
	Category string `json:"category"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
}
type SupportTicketAction struct {
	ExpectedVersion uint64 `json:"expected_version"`
	Body            string `json:"body"`
}
type SupportTicketCard struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Category    string    `json:"category"`
	Subject     string    `json:"subject"`
	State       string    `json:"state"`
	Version     uint64    `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type SupportTicketMessage struct {
	Version   uint64    `json:"version"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}
type SupportTicketDetail struct {
	SupportTicketCard
	Messages []SupportTicketMessage `json:"messages"`
}
type SupportTicketPage struct {
	Items      []SupportTicketCard `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

func validSupportText(text string, chars, byteLimit int, multiline bool) bool {
	if len(text) > byteLimit || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > chars {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}
func validSupportCreate(in SupportTicketCreate) bool {
	switch in.Category {
	case "account", "billing", "generation", "other":
		return validSupportText(in.Subject, 120, 480, false) && validSupportText(in.Body, 2000, 6000, true)
	}
	return false
}

func supportMissing() error {
	return failure(404, "SUPPORT_TICKET_NOT_FOUND", "未找到可读的客服工单或操作收据")
}
func supportReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return supportMissing()
	}
	return err
}

func (a *App) supportTicketTx(p *principal, workspace string, staff bool, fn func(context.Context, pgx.Tx) error, proofs ...*operationsPasswordProof) error {
	if a.store == nil || a.config.AuthMode != "required" {
		return failure(501, "SUPPORT_STORAGE_REQUIRED", "客服工单需要持久账户模式")
	}
	if p == nil || p.User == nil {
		return failure(401, "AUTH_REQUIRED", "请先登录")
	}
	if !validOperationsID(workspace, "ws") || (!staff && workspace != p.Workspace.ID) {
		return supportMissing()
	}
	if staff && !a.config.OperationsEnabled {
		return operationsDenied()
	}
	return a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			// User and live session first. A staff grant is then kept locked
			// through the final read/write. Ticket locks always come last.
			if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
				return e
			}
			if len(proofs) == 1 {
				if e := checkOperationsPermissionSQL(ctx, tx, p, workspace, "support_ticket", proofs[0]); e != nil {
					return e
				}
			} else if staff {
				if _, e := operationsGrantSQL(ctx, tx, p, workspace, "support_ticket"); e != nil {
					return e
				}
			}
			if e := fn(ctx, tx); e != nil {
				return e
			}
			return validateCurrentSessionSQL(ctx, tx, p, p.CredentialVersion)
		})
	})
}

const supportCardFields = "id,workspace_id,category,subject,state,version,created_at,updated_at"

func scanSupportCard(row pgx.Row) (c SupportTicketCard, e error) {
	e = row.Scan(&c.ID, &c.WorkspaceID, &c.Category, &c.Subject, &c.State, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	return
}
func supportCardSQL(ctx context.Context, tx pgx.Tx, workspace, id, owner string, lock bool) (SupportTicketCard, error) {
	q := "SELECT " + supportCardFields + " FROM studio_support_tickets WHERE workspace_id=$1 AND id=$2"
	args := []any{workspace, id}
	if owner != "" {
		q += " AND owner_user_id=$3"
		args = append(args, owner)
	}
	if lock {
		q += " FOR UPDATE"
	}
	c, e := scanSupportCard(tx.QueryRow(ctx, q, args...))
	return c, supportReadError(e)
}
func supportDetailSQL(ctx context.Context, tx pgx.Tx, c SupportTicketCard, version uint64) (SupportTicketDetail, error) {
	if version == 0 || version > c.Version {
		return SupportTicketDetail{}, supportMissing()
	}
	rows, e := tx.Query(ctx, `SELECT version,CASE operation WHEN 'create' THEN 'created' WHEN 'close' THEN 'closed' ELSE operation END,
		body,created_at FROM studio_support_messages WHERE workspace_id=$1 AND ticket_id=$2 AND version<=$3 ORDER BY version`, c.WorkspaceID, c.ID, version)
	if e != nil {
		return SupportTicketDetail{}, e
	}
	defer rows.Close()
	out := SupportTicketDetail{SupportTicketCard: c, Messages: make([]SupportTicketMessage, 0, version)}
	for rows.Next() {
		var m SupportTicketMessage
		if e = rows.Scan(&m.Version, &m.Kind, &m.Body, &m.CreatedAt); e != nil {
			return SupportTicketDetail{}, e
		}
		out.Messages = append(out.Messages, m)
	}
	if e = rows.Err(); e != nil {
		return SupportTicketDetail{}, e
	}
	if uint64(len(out.Messages)) != version {
		return SupportTicketDetail{}, failure(503, "SUPPORT_TICKET_INCOMPLETE", "工单记录不完整")
	}
	last := out.Messages[len(out.Messages)-1]
	out.Version, out.UpdatedAt, out.State = version, last.CreatedAt, "OPEN"
	if last.Kind == "closed" {
		out.State = "CLOSED"
	}
	return out, nil
}

func (a *App) supportTicket(p *principal, workspace, id string, staff bool) (out SupportTicketDetail, err error) {
	if !validOperationsID(id, "tkt") {
		return out, supportMissing()
	}
	err = a.supportTicketTx(p, workspace, staff, func(ctx context.Context, tx pgx.Tx) error {
		owner := ""
		if !staff {
			owner = p.User.ID
		}
		c, e := supportCardSQL(ctx, tx, workspace, id, owner, false)
		if e != nil {
			return e
		}
		out, e = supportDetailSQL(ctx, tx, c, c.Version)
		return e
	})
	if err != nil {
		return SupportTicketDetail{}, err
	}
	return
}

type supportCursor struct {
	Workspace, User, ID string
	Staff               bool
	Created             time.Time
}

func (a *App) supportTickets(p *principal, workspace string, staff bool, cursor string) (out SupportTicketPage, err error) {
	err = a.supportTicketTx(p, workspace, staff, func(ctx context.Context, tx pgx.Tx) error {
		q := "SELECT " + supportCardFields + " FROM studio_support_tickets WHERE workspace_id=$1"
		args := []any{workspace}
		c := supportCursor{Workspace: workspace, User: p.User.ID, Staff: staff}
		if cursor != "" {
			if len(cursor) > 640 {
				return failure(400, "INVALID_CURSOR", "工单分页游标无效")
			}
			b, e := base64.RawURLEncoding.DecodeString(cursor)
			c = supportCursor{}
			if e != nil || json.Unmarshal(b, &c) != nil || c.Workspace != workspace || c.User != p.User.ID || c.Staff != staff ||
				!validOperationsID(c.ID, "tkt") || c.Created.Year() < 2000 || c.Created.Year() > 9999 {
				return failure(400, "INVALID_CURSOR", "工单分页游标无效")
			}
			q += " AND (created_at,id)<($2,$3)"
			args = append(args, c.Created, c.ID)
		}
		if !staff {
			if cursor != "" {
				q += " AND owner_user_id=$4"
			} else {
				q += " AND owner_user_id=$2"
			}
			args = append(args, p.User.ID)
		}
		rows, e := tx.Query(ctx, q+" ORDER BY created_at DESC,id DESC LIMIT 21", args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		out.Items = make([]SupportTicketCard, 0, 21)
		for rows.Next() {
			v, e := scanSupportCard(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > 20 {
			out.Items = out.Items[:20]
			last := out.Items[19]
			c.Created, c.ID = last.CreatedAt, last.ID
			b, e := json.Marshal(c)
			if e != nil {
				return e
			}
			next := base64.RawURLEncoding.EncodeToString(b)
			out.NextCursor = &next
		}
		return nil
	})
	if err != nil {
		return SupportTicketPage{}, err
	}
	return
}

func supportReceiptSQL(ctx context.Context, tx pgx.Tx, workspace, actor, op string, hash []byte) (id string, version uint64, request []byte, err error) {
	err = tx.QueryRow(ctx, `SELECT ticket_id,version,request_hash FROM studio_support_receipts
		WHERE workspace_id=$1 AND actor_user_id=$2 AND operation=$3 AND key_hash=$4`, workspace, actor, op, hash).Scan(&id, &version, &request)
	return
}
func validSupportOp(op string) bool {
	return op == "create" || op == "followup" || op == "close" || op == "reply"
}
func (a *App) supportTicketReceipt(p *principal, workspace, op, key string) (out SupportTicketDetail, err error) {
	if !workflowKey.MatchString(key) || !validSupportOp(op) {
		return out, supportMissing()
	}
	err = a.supportTicketTx(p, workspace, op == "reply", func(ctx context.Context, tx pgx.Tx) error {
		h := sha256.Sum256([]byte(key))
		id, v, _, e := supportReceiptSQL(ctx, tx, workspace, p.User.ID, op, h[:])
		if e != nil {
			return supportReadError(e)
		}
		owner := p.User.ID
		if op == "reply" {
			owner = ""
		}
		c, e := supportCardSQL(ctx, tx, workspace, id, owner, false)
		if e != nil {
			return e
		}
		out, e = supportDetailSQL(ctx, tx, c, v)
		return e
	})
	if err != nil {
		return SupportTicketDetail{}, err
	}
	return
}

func (a *App) supportWrite(p *principal, workspace, op, key, body string, digest [32]byte, apply func(context.Context, pgx.Tx) (SupportTicketCard, error), proofs ...*operationsPasswordProof) (out SupportTicketDetail, replayed bool, err error) {
	if op == "reply" && (len(proofs) != 1 || proofs[0] == nil) || op != "reply" && len(proofs) != 0 {
		return out, false, failure(403, "OPERATIONS_REAUTH_REQUIRED", "客服答复需要重新核验当前密码")
	}
	if !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效的客服操作原键")
	}
	h := sha256.Sum256([]byte(key))
	err = a.supportTicketTx(p, workspace, op == "reply", func(ctx context.Context, tx pgx.Tx) error {
		id, v, original, e := supportReceiptSQL(ctx, tx, workspace, p.User.ID, op, h[:])
		if e == nil {
			if !bytes.Equal(original, digest[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原键已绑定其他工单操作")
			}
			owner := p.User.ID
			if op == "reply" {
				owner = ""
			}
			c, e := supportCardSQL(ctx, tx, workspace, id, owner, false)
			if e != nil {
				return e
			}
			out, e = supportDetailSQL(ctx, tx, c, v)
			replayed = true
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		c, e := apply(ctx, tx)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO studio_support_messages(ticket_id,workspace_id,version,actor_user_id,operation,body,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, c.ID, workspace, c.Version, p.User.ID, op, body, c.UpdatedAt); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO studio_support_receipts(workspace_id,actor_user_id,operation,key_hash,request_hash,ticket_id,version)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, workspace, p.User.ID, op, h[:], digest[:], c.ID, c.Version); e != nil {
			return e
		}
		out, e = supportDetailSQL(ctx, tx, c, c.Version)
		return e
	}, proofs...)
	if err != nil {
		return SupportTicketDetail{}, false, err
	}
	return
}

func (a *App) createSupportTicket(p *principal, key string, in SupportTicketCreate) (SupportTicketDetail, bool, error) {
	if p == nil || p.User == nil {
		return SupportTicketDetail{}, false, failure(401, "AUTH_REQUIRED", "请先登录")
	}
	return a.supportWrite(p, p.Workspace.ID, "create", key, in.Body, payloadHash(in), func(ctx context.Context, tx pgx.Tx) (SupportTicketCard, error) {
		if !validSupportCreate(in) {
			return SupportTicketCard{}, failure(422, "INVALID_SUPPORT_TICKET", "请检查工单分类、主题和正文")
		}
		var count int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM studio_support_tickets WHERE workspace_id=$1 AND owner_user_id=$2 AND state='OPEN' LIMIT 8) limited`, p.Workspace.ID, p.User.ID).Scan(&count)
		if e != nil {
			return SupportTicketCard{}, e
		}
		if count >= 8 {
			return SupportTicketCard{}, failure(429, "SUPPORT_TICKET_OPEN_LIMIT", "本人在当前工作区最多同时保留8个开放工单")
		}
		return scanSupportCard(tx.QueryRow(ctx, `WITH instant AS (SELECT clock_timestamp() AS ts)
			INSERT INTO studio_support_tickets(id,workspace_id,owner_user_id,category,subject,state,version,created_at,updated_at)
			SELECT $1,$2,$3,$4,$5,'OPEN',1,ts,ts FROM instant RETURNING `+supportCardFields, newID("tkt"), p.Workspace.ID, p.User.ID, in.Category, in.Subject))
	})
}

func (a *App) actOnSupportTicket(p *principal, workspace, id, op, key string, in SupportTicketAction, proofs ...*operationsPasswordProof) (SupportTicketDetail, bool, error) {
	if op != "followup" && op != "close" && op != "reply" {
		return SupportTicketDetail{}, false, supportMissing()
	}
	req := struct {
		ID     string
		Action SupportTicketAction
	}{id, in}
	return a.supportWrite(p, workspace, op, key, in.Body, payloadHash(req), func(ctx context.Context, tx pgx.Tx) (SupportTicketCard, error) {
		if !validOperationsID(id, "tkt") {
			return SupportTicketCard{}, supportMissing()
		}
		if in.ExpectedVersion < 1 || in.ExpectedVersion > 32 || (op == "close" && in.Body != "") || (op != "close" && !validSupportText(in.Body, 2000, 6000, true)) {
			return SupportTicketCard{}, failure(422, "INVALID_SUPPORT_ACTION", "请核对工单版本及正文")
		}
		owner := p.User.ID
		if op == "reply" {
			owner = ""
		}
		c, e := supportCardSQL(ctx, tx, workspace, id, owner, true)
		if e != nil {
			return c, e
		}
		if c.State != "OPEN" || c.Version != in.ExpectedVersion {
			return c, failure(409, "SUPPORT_TICKET_CHANGED", "工单已变化，请刷新")
		}
		state := "OPEN"
		if op == "close" {
			state = "CLOSED"
		} else if c.Version >= 31 {
			return c, failure(429, "SUPPORT_TICKET_MESSAGE_LIMIT", "已达消息上限，可关闭此工单")
		}
		return scanSupportCard(tx.QueryRow(ctx, `UPDATE studio_support_tickets SET state=$2,version=version+1,updated_at=GREATEST(updated_at,clock_timestamp())
			WHERE id=$1 RETURNING `+supportCardFields, c.ID, state))
	}, proofs...)
}
