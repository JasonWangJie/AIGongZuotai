package studio

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// These values are private to an adapter that has authenticated a merchant fact
// and independently resolved its checkout binding. No HTTP or CLI accepts them.
type orderPaymentScope struct {
	Source      string
	Merchant    string
	Environment string
}

type authenticatedOrderEvent struct {
	Scope         orderPaymentScope
	WorkspaceID   string
	OwnerID       string
	OrderID       string
	EventID       string
	Kind          string
	TransactionID string
	RefundID      string
	Currency      string
	AmountMinor   int64
	OccurredAt    time.Time
}

type orderPaymentReceipt struct {
	EventID     string                 `json:"event_id"`
	OrderID     string                 `json:"order_id"`
	Kind        string                 `json:"kind"`
	Disposition string                 `json:"disposition"`
	Projection  OrderPaymentProjection `json:"projection"`
}

var orderPaymentCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

func (s orderPaymentScope) valid() bool {
	return evidenceAlias.MatchString(s.Source) && evidenceAlias.MatchString(s.Merchant) && (s.Environment == "test" || s.Environment == "live")
}

func (e authenticatedOrderEvent) valid() bool {
	if !e.Scope.valid() || !validOperationsID(e.WorkspaceID, "ws") || !validOperationsID(e.OwnerID, "usr") || !validOperationsID(e.OrderID, "ord") {
		return false
	}
	if !evidenceAlias.MatchString(e.EventID) || !evidenceAlias.MatchString(e.TransactionID) || !orderPaymentCurrency.MatchString(e.Currency) || e.AmountMinor < 1 || e.AmountMinor > 1_000_000_000_000 || e.OccurredAt.Year() < 2000 || e.OccurredAt.Year() > 9999 {
		return false
	}
	return e.Kind == "payment" && e.RefundID == "" || e.Kind == "refund" && evidenceAlias.MatchString(e.RefundID)
}

func (e authenticatedOrderEvent) economicID() string {
	if e.Kind == "refund" {
		return e.RefundID
	}
	return e.TransactionID
}

func paymentBindingMismatch() error {
	return failure(409, "ORDER_PAYMENT_SCOPE_MISMATCH", "支付事实与原订单的收款绑定不匹配")
}

func lockedPaymentOrderSQL(ctx context.Context, tx pgx.Tx, workspace, owner, id string) (string, error) {
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM studio_order_intents WHERE id=$1 AND owner_user_id=$2 AND workspace_id=$3 FOR NO KEY UPDATE`, id, owner, workspace).Scan(&state)
	return state, orderIntentReadErr(err)
}

func readPaymentBindingSQL(ctx context.Context, tx pgx.Tx, id string) (orderPaymentScope, error) {
	var scope orderPaymentScope
	err := tx.QueryRow(ctx, `SELECT source,merchant,environment FROM studio_order_payment_bindings WHERE order_id=$1`, id).Scan(&scope.Source, &scope.Merchant, &scope.Environment)
	return scope, err
}

func (s *postgresStore) bindOrderPayment(workspace, owner, id string, scope orderPaymentScope) error {
	if !scope.valid() || !validOperationsID(workspace, "ws") || !validOperationsID(owner, "usr") || !validOperationsID(id, "ord") {
		return paymentBindingMismatch()
	}
	return s.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			state, err := lockedPaymentOrderSQL(ctx, tx, workspace, owner, id)
			if err != nil {
				return err
			}
			existing, err := readPaymentBindingSQL(ctx, tx, id)
			if err == nil {
				if existing != scope {
					return paymentBindingMismatch()
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if state != "PENDING" {
				return paymentBindingMismatch()
			}
			if _, err = tx.Exec(ctx, `INSERT INTO studio_order_payment_environment(environment) VALUES($1) ON CONFLICT DO NOTHING`, scope.Environment); err != nil {
				return err
			}
			var environment string
			if err = tx.QueryRow(ctx, "SELECT environment FROM studio_order_payment_environment WHERE singleton").Scan(&environment); err != nil {
				return err
			}
			if scope.Environment != environment {
				return paymentBindingMismatch()
			}
			_, err = tx.Exec(ctx, `INSERT INTO studio_order_payment_bindings(order_id,owner_user_id,workspace_id,source,merchant,environment) VALUES($1,$2,$3,$4,$5,$6)`, id, owner, workspace, scope.Source, scope.Merchant, scope.Environment)
			return err
		})
	})
}

func readOrderPaymentEventReceiptSQL(ctx context.Context, tx pgx.Tx, scope orderPaymentScope, id string) (orderPaymentReceipt, []byte, error) {
	var original orderPaymentReceipt
	var digest []byte
	err := tx.QueryRow(ctx, `SELECT receipt,request_hash FROM studio_order_payment_events WHERE source=$1 AND merchant=$2 AND environment=$3 AND event_id=$4`, scope.Source, scope.Merchant, scope.Environment, id).Scan(&original, &digest)
	return original, digest, err
}

func (s *postgresStore) orderPaymentEventReceipt(scope orderPaymentScope, id string) (orderPaymentReceipt, error) {
	var out orderPaymentReceipt
	if !scope.valid() || !evidenceAlias.MatchString(id) {
		return out, orderIntentMissing()
	}
	err := s.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			var err error
			out, _, err = readOrderPaymentEventReceiptSQL(ctx, tx, scope, id)
			return orderIntentReadErr(err)
		})
	})
	if err != nil {
		return orderPaymentReceipt{}, err
	}
	return out, nil
}

func (s *postgresStore) recordAuthenticatedOrderEvent(e authenticatedOrderEvent) (orderPaymentReceipt, error) {
	e.OccurredAt = e.OccurredAt.UTC()
	if !e.valid() {
		return orderPaymentReceipt{}, failure(422, "INVALID_ORDER_PAYMENT_FACT", "支付事实的身份、金额或发生时间无效")
	}
	var out orderPaymentReceipt
	var collision bool
	err := s.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			var err error
			out, collision, err = recordOrderPaymentSQL(ctx, tx, e)
			return err
		})
	})
	if err != nil {
		return orderPaymentReceipt{}, err
	}
	if collision {
		return orderPaymentReceipt{}, failure(409, "ORDER_PAYMENT_EVENT_CONFLICT", "同一通知身份已有不同的原始记录，需要核实")
	}
	return out, nil
}

func recordOrderPaymentSQL(ctx context.Context, tx pgx.Tx, e authenticatedOrderEvent) (orderPaymentReceipt, bool, error) {
	var out orderPaymentReceipt
	if _, err := lockedPaymentOrderSQL(ctx, tx, e.WorkspaceID, e.OwnerID, e.OrderID); err != nil {
		return out, false, err
	}
	bound, err := readPaymentBindingSQL(ctx, tx, e.OrderID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && bound != e.Scope {
		return out, false, paymentBindingMismatch()
	}
	if err != nil {
		return out, false, err
	}
	// Serialize collisions across DIFFERENT order rows too. The namespaces
	// are always acquired as event, transaction, refund, in that order.
	if err = lockPaymentIdentitiesSQL(ctx, tx, e); err != nil {
		return out, false, err
	}
	original, oldHash, err := readOrderPaymentEventReceiptSQL(ctx, tx, e.Scope, e.EventID)
	requestHash := payloadHash(e)
	if err == nil {
		if bytes.Equal(oldHash, requestHash[:]) {
			return original, false, nil
		}
		err = quarantinePaymentConflictSQL(ctx, tx, e, original, requestHash)
		return out, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, false, err
	}
	disposition, err := savePaymentFactSQL(ctx, tx, e)
	if err != nil {
		return out, false, err
	}
	current, err := orderPaymentProjectionSQL(ctx, tx, e.WorkspaceID, e.OwnerID, e.OrderID)
	if err != nil {
		return out, false, err
	}
	if current.PaymentState == "RECEIVED" && current.RefundState == "NONE" && current.CreditState == "NOT_ISSUED" {
		if err = issueOrderCreditSQL(ctx, tx, e); err != nil {
			return out, false, err
		}
		current, err = orderPaymentProjectionSQL(ctx, tx, e.WorkspaceID, e.OwnerID, e.OrderID)
		if err != nil {
			return out, false, err
		}
	}
	out = orderPaymentReceipt{EventID: e.EventID, OrderID: e.OrderID, Kind: e.Kind, Disposition: disposition, Projection: current}
	_, err = tx.Exec(ctx, `INSERT INTO studio_order_payment_events(source,merchant,environment,event_id,order_id,kind,external_id,transaction_id,
 currency,amount_minor,occurred_at,nano_remainder,request_hash,receipt) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		e.Scope.Source, e.Scope.Merchant, e.Scope.Environment, e.EventID, e.OrderID, e.Kind, e.economicID(), e.TransactionID, e.Currency, e.AmountMinor, e.OccurredAt.Truncate(time.Microsecond), e.OccurredAt.Nanosecond()%1000, requestHash[:], out)
	return out, false, err
}
