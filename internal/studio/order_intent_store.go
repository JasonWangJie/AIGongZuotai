package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type OrderIntentCreate struct {
	ProductID     string `json:"product_id"`
	CatalogDigest string `json:"catalog_digest"`
	ProductDigest string `json:"product_digest"`
}
type OrderIntent struct {
	ID          string             `json:"id"`
	Product     OrderIntentProduct `json:"product"`
	State       string             `json:"state"`
	Version     uint64             `json:"version"`
	CreatedAt   time.Time          `json:"created_at"`
	CancelledAt *time.Time         `json:"cancelled_at,omitempty"`
}
type OrderIntentPage struct {
	Items      []OrderIntent `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}
type orderIntentCursor struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Scope   string    `json:"scope"`
}

const orderIntentFields = `id,product_id,product_name,catalog_version,catalog_digest,product_digest,currency,currency_exponent,amount_minor::text,credit_micros::text,state,version,created_at,cancelled_at`

func orderIntentMissing() error {
	return failure(404, "ORDER_INTENT_NOT_FOUND", "未找到本人订单意向或操作收据")
}
func orderIntentReadErr(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return orderIntentMissing()
	}
	return e
}

func scanOrderIntent(row pgx.Row) (o OrderIntent, e error) {
	p := &o.Product
	e = row.Scan(&o.ID, &p.ID, &p.Name, &p.CatalogVersion, &p.CatalogDigest, &p.ProductDigest, &p.Currency, &p.CurrencyExponent, &p.AmountMinor, &p.CreditMicros,
		&o.State, &o.Version, &o.CreatedAt, &o.CancelledAt)
	return
}

func (a *App) orderIntentTx(p *principal, fn func(context.Context, pgx.Tx) error) error {
	if a.store == nil || a.config.AuthMode != "required" {
		return failure(501, "ORDER_INTENT_STORAGE_REQUIRED", "订单意向需要持久账户模式")
	}
	if p == nil || p.User == nil {
		return failure(401, "AUTH_REQUIRED", "请先登录")
	}
	return a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			// The real current account is locked before receipt lookup, capacity
			// accounting and the business write, including the very first intent.
			if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
				return e
			}
			return fn(ctx, tx)
		})
	})
}
func readOrderIntentReceiptSQL(ctx context.Context, tx pgx.Tx, p *principal, op, key string) (o OrderIntent, digest []byte, e error) {
	k := sha256.Sum256([]byte(key))
	e = tx.QueryRow(ctx, `SELECT snapshot,request_hash FROM studio_order_intent_receipts WHERE owner_user_id=$1 AND workspace_id=$2 AND operation=$3 AND key_hash=$4`, p.User.ID, p.Workspace.ID, op, k[:]).Scan(&o, &digest)
	return
}

func (a *App) orderIntentWrite(p *principal, op, key string, hash [32]byte, apply func(context.Context, pgx.Tx) (OrderIntent, error)) (out OrderIntent, replay bool, err error) {
	if !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效的订单操作原键")
	}
	err = a.orderIntentTx(p, func(ctx context.Context, tx pgx.Tx) error {
		original, digest, e := readOrderIntentReceiptSQL(ctx, tx, p, op, key)
		if e == nil {
			if !bytes.Equal(digest, hash[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原键对应另一份订单操作")
			}
			out = original
			replay = true
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		out, e = apply(ctx, tx)
		if e != nil {
			return e
		}
		k := sha256.Sum256([]byte(key))
		_, e = tx.Exec(ctx, `INSERT INTO studio_order_intent_receipts(owner_user_id,workspace_id,operation,key_hash,request_hash,order_id,snapshot)
		 VALUES($1,$2,$3,$4,$5,$6,$7)`, p.User.ID, p.Workspace.ID, op, k[:], hash[:], out.ID, out)
		return e
	})
	if err != nil {
		return OrderIntent{}, false, err
	}
	return
}

func (a *App) createOrderIntent(p *principal, in OrderIntentCreate, key string) (OrderIntent, bool, error) {
	return a.orderIntentWrite(p, "create", key, payloadHash(in), func(ctx context.Context, tx pgx.Tx) (OrderIntent, error) {
		product, e := a.lookupOrderIntentProduct(in.ProductID, in.CatalogDigest, in.ProductDigest)
		if e != nil {
			return OrderIntent{}, e
		}
		amount, e := strconv.ParseInt(product.AmountMinor, 10, 64)
		if e != nil || amount < 1 || amount > 1_000_000_000_000 {
			return OrderIntent{}, failure(503, "INVALID_ORDER_CATALOG", "订单目录金额无效")
		}
		credits, e := strconv.ParseInt(product.CreditMicros, 10, 64)
		if e != nil || credits < 1 || credits > 1_000_000_000_000_000 {
			return OrderIntent{}, failure(503, "INVALID_ORDER_CATALOG", "订单目录权益数量无效")
		}
		var n int
		e = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM studio_order_pending_slots WHERE owner_user_id=$1 LIMIT 32) s`, p.User.ID).Scan(&n)
		if e != nil {
			return OrderIntent{}, e
		}
		if n >= 32 {
			return OrderIntent{}, failure(429, "ORDER_INTENT_PENDING_LIMIT", "本人最多可保留 32 笔待处理订单意向")
		}
		return scanOrderIntent(tx.QueryRow(ctx, `INSERT INTO studio_order_intents(id,workspace_id,owner_user_id,product_id,product_name,catalog_version,
		 catalog_digest,product_digest,currency,currency_exponent,amount_minor,credit_micros,state,version)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'PENDING',1) RETURNING `+orderIntentFields,
			newID("ord"), p.Workspace.ID, p.User.ID, product.ID, product.Name, product.CatalogVersion, product.CatalogDigest, product.ProductDigest, product.Currency, product.CurrencyExponent, amount, credits))
	})
}
func (a *App) cancelOrderIntent(p *principal, id string, expected uint64, key string) (OrderIntent, bool, error) {
	type request struct {
		OrderID  string `json:"order_id"`
		Expected uint64 `json:"expected_version"`
	}
	return a.orderIntentWrite(p, "cancel", key, payloadHash(request{id, expected}), func(ctx context.Context, tx pgx.Tx) (OrderIntent, error) {
		if !validOperationsID(id, "ord") {
			return OrderIntent{}, orderIntentMissing()
		}
		if expected < 1 || expected > 2 {
			return OrderIntent{}, failure(422, "INVALID_ORDER_VERSION", "请提供本人所见订单版本")
		}
		var v uint64
		e := tx.QueryRow(ctx, `SELECT version FROM studio_order_intents WHERE id=$1 AND workspace_id=$2 AND owner_user_id=$3 FOR UPDATE`, id, p.Workspace.ID, p.User.ID).Scan(&v)
		if e != nil {
			return OrderIntent{}, orderIntentReadErr(e)
		}
		if v != expected || v != 1 {
			return OrderIntent{}, failure(409, "ORDER_INTENT_VERSION_CONFLICT", "订单意向已变化，请读取当前状态")
		}
		var recorded bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_order_payment_events WHERE order_id=$1)
		 OR EXISTS(SELECT 1 FROM studio_order_payment_reviews WHERE order_id=$1)`, id).Scan(&recorded)
		if e != nil {
			return OrderIntent{}, e
		}
		if recorded {
			return OrderIntent{}, failure(409, "ORDER_PAYMENT_RECORDED", "订单已有收退款记录，请核对资金与权益状态")
		}
		return scanOrderIntent(tx.QueryRow(ctx, `UPDATE studio_order_intents SET state='CANCELLED',version=2,cancelled_at=GREATEST(created_at,clock_timestamp())
		 WHERE id=$1 AND workspace_id=$2 AND owner_user_id=$3 AND version=$4 AND state='PENDING' RETURNING `+orderIntentFields, id, p.Workspace.ID, p.User.ID, expected))
	})
}
func (a *App) orderIntentDetail(p *principal, id string) (out OrderIntent, err error) {
	if !validOperationsID(id, "ord") {
		return out, orderIntentMissing()
	}
	err = a.orderIntentTx(p, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = scanOrderIntent(tx.QueryRow(ctx, `SELECT `+orderIntentFields+` FROM studio_order_intents WHERE id=$1 AND workspace_id=$2 AND owner_user_id=$3`, id, p.Workspace.ID, p.User.ID))
		return orderIntentReadErr(e)
	})
	if err != nil {
		return OrderIntent{}, err
	}
	return
}
func (a *App) orderIntentReceipt(p *principal, op, key string) (out OrderIntent, err error) {
	if !workflowKey.MatchString(key) || op != "create" && op != "cancel" {
		return out, orderIntentMissing()
	}
	err = a.orderIntentTx(p, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, _, e = readOrderIntentReceiptSQL(ctx, tx, p, op, key)
		return orderIntentReadErr(e)
	})
	if err != nil {
		return OrderIntent{}, err
	}
	return
}

func (a *App) listOrderIntents(p *principal, cursor string, limit int) (out OrderIntentPage, err error) {
	limit, err = presentationPageLimit(limit)
	if err != nil {
		return out, err
	}
	err = a.orderIntentTx(p, func(ctx context.Context, tx pgx.Tx) error {
		digest := sha256.Sum256([]byte(p.User.ID + "\x00" + p.Workspace.ID))
		scope := hex.EncodeToString(digest[:])
		var c orderIntentCursor
		if cursor != "" {
			if len(cursor) > 640 {
				return failure(400, "INVALID_CURSOR", "订单分页游标无效")
			}
			raw, e := base64.RawURLEncoding.DecodeString(cursor)
			if e != nil || json.Unmarshal(raw, &c) != nil || c.Scope != scope || !validOperationsID(c.ID, "ord") || c.Created.Year() < 2000 || c.Created.Year() > 9999 {
				return failure(400, "INVALID_CURSOR", "订单分页游标无效")
			}
		}
		where := ` FROM studio_order_intents WHERE owner_user_id=$1 AND workspace_id=$2`
		args := []any{p.User.ID, p.Workspace.ID, limit + 1}
		if cursor != "" {
			where += " AND (created_at,id)<($4,$5)"
			args = append(args, c.Created, c.ID)
		}
		rows, e := tx.Query(ctx, "SELECT "+orderIntentFields+where+" ORDER BY created_at DESC,id DESC LIMIT $3", args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		out.Items = make([]OrderIntent, 0, limit+1)
		for rows.Next() {
			o, e := scanOrderIntent(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, o)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > limit {
			last := out.Items[limit-1]
			out.Items = out.Items[:limit]
			raw, e := json.Marshal(orderIntentCursor{ID: last.ID, Created: last.CreatedAt, Scope: scope})
			if e != nil {
				return e
			}
			next := base64.RawURLEncoding.EncodeToString(raw)
			out.NextCursor = &next
		}
		return nil
	})
	return
}
