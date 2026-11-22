package studio

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Monetary and entitlement quantities are strings in different units. Ownership
// ambiguity excludes an amount from the total; its REVIEW flag remains visible.
type OrderPaymentProjection struct {
	OrderID              string `json:"order_id"`
	Environment          string `json:"environment"`
	Currency             string `json:"currency"`
	CurrencyExponent     int    `json:"currency_exponent"`
	PaymentState         string `json:"payment_state"`
	RefundState          string `json:"refund_state"`
	CreditState          string `json:"credit_state"`
	PaidMinor            string `json:"paid_minor"`
	RefundedMinor        string `json:"refunded_minor"`
	GrantedCreditMicros  string `json:"granted_credit_micros"`
	ReversedCreditMicros string `json:"reversed_credit_micros"`
}

func orderPaymentProjectionSQL(ctx context.Context, tx pgx.Tx, workspace, owner, orderID string) (OrderPaymentProjection, error) {
	// A single statement gives the owner a consistent view at READ COMMITTED.
	// Repeated delivery events are never part of the economic sums.
	const query = `WITH original AS (
   SELECT o.*,b.source,b.merchant,b.environment,b.bound_at FROM studio_order_intents o
   LEFT JOIN studio_order_payment_bindings b ON b.order_id=o.id
   WHERE o.id=$1 AND o.workspace_id=$2 AND o.owner_user_id=$3
 ), facts AS (
   SELECT f.*,t.disputed OR COALESCE(r.disputed,false) AS disputed,
     f.currency=o.currency AS same_currency,
     f.occurred_at<o.bound_at OR (f.occurred_at,f.nano_remainder)>(f.first_received_at,0) AS invalid_time,
     p.amount_minor AS captured,p.currency AS capture_currency,
     (f.occurred_at,f.nano_remainder)<(p.occurred_at,p.nano_remainder) AS precedes_capture,
     sum(f.amount_minor) FILTER(WHERE f.kind='refund') OVER (PARTITION BY f.transaction_id) AS transaction_refunds
   FROM original o JOIN studio_order_payment_facts f ON f.order_id=o.id
   JOIN studio_order_payment_identities t ON (t.source,t.merchant,t.environment)=(o.source,o.merchant,o.environment)
     AND t.kind='transaction' AND t.external_id=f.transaction_id
   LEFT JOIN studio_order_payment_identities r ON (r.source,r.merchant,r.environment)=(o.source,o.merchant,o.environment)
     AND f.kind='refund' AND r.kind='refund' AND r.external_id=f.external_id
   LEFT JOIN studio_order_payment_facts p ON p.order_id=f.order_id AND p.kind='payment' AND p.external_id=f.transaction_id
 ), amounts AS (
   SELECT count(*) FILTER(WHERE kind='payment') AS payments, count(*) FILTER(WHERE kind='refund') AS refunds,
     COALESCE(sum(amount_minor) FILTER(WHERE kind='payment' AND same_currency AND NOT disputed),0) AS paid,
     COALESCE(sum(amount_minor) FILTER(WHERE kind='refund' AND same_currency AND NOT disputed),0) AS refunded,
     bool_or(kind='payment' AND (invalid_time OR disputed OR NOT same_currency)) AS bad_payment,
     bool_or(kind='refund' AND (invalid_time OR disputed OR NOT same_currency OR precedes_capture OR
       currency<>capture_currency OR transaction_refunds>captured)) AS bad_refund,
     bool_or(kind='refund' AND captured IS NULL) AS unmatched
   FROM facts
 ), states AS (
   SELECT o.*,a.*,c.order_id IS NOT NULL AS issued,
     EXISTS(SELECT 1 FROM studio_order_payment_reviews v WHERE v.order_id=o.id) AS review,
     CASE WHEN o.environment IS NULL THEN 'UNBOUND'
          WHEN EXISTS(SELECT 1 FROM studio_order_payment_reviews v WHERE v.order_id=o.id AND v.reason='event_conflict') THEN 'REVIEW'
          WHEN a.payments=0 THEN 'AWAITING'
          WHEN a.payments<>1 OR a.bad_payment OR o.state='CANCELLED' OR a.paid<>o.amount_minor THEN 'REVIEW'
          ELSE 'RECEIVED' END AS payment_state,
     CASE WHEN EXISTS(SELECT 1 FROM studio_order_payment_reviews v WHERE v.order_id=o.id AND v.reason='refund_conflict') THEN 'REVIEW'
          WHEN a.refunds=0 THEN 'NONE' WHEN a.bad_refund THEN 'REVIEW'
          WHEN a.unmatched THEN 'PENDING_MATCH' WHEN a.refunded=a.paid THEN 'FULL'
          ELSE 'PARTIAL' END AS refund_state
   FROM original o CROSS JOIN amounts a LEFT JOIN studio_order_payment_credits c ON c.order_id=o.id
 )
 SELECT id,COALESCE(environment,''),currency,currency_exponent,payment_state,refund_state,
   CASE WHEN review OR payment_state='REVIEW' OR refunds>0 THEN 'REVIEW'
        WHEN issued THEN 'ISSUED' ELSE 'NOT_ISSUED' END,
   paid::text,refunded::text,CASE WHEN issued THEN credit_micros::text ELSE '0' END,'0'
 FROM states`
	var p OrderPaymentProjection
	err := tx.QueryRow(ctx, query, orderID, workspace, owner).Scan(
		&p.OrderID, &p.Environment, &p.Currency, &p.CurrencyExponent, &p.PaymentState, &p.RefundState, &p.CreditState,
		&p.PaidMinor, &p.RefundedMinor, &p.GrantedCreditMicros, &p.ReversedCreditMicros,
	)
	return p, orderIntentReadErr(err)
}
