package studio

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func lockPaymentIdentitiesSQL(ctx context.Context, tx pgx.Tx, e authenticatedOrderEvent) error {
	prefix := "order-payment:" + e.Scope.Source + ":" + e.Scope.Merchant + ":" + e.Scope.Environment + ":"
	keys := []string{"event:" + e.EventID, "transaction:" + e.TransactionID}
	if e.Kind == "refund" {
		keys = append(keys, "refund:"+e.RefundID)
	}
	for _, key := range keys {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", prefix+key); err != nil {
			return err
		}
	}
	return nil
}

func claimPaymentIdentitySQL(ctx context.Context, tx pgx.Tx, e authenticatedOrderEvent, kind, id string) error {
	// Identity is global within the adapter/merchant/environment namespace.
	// Even a refund claims its parent transaction, including when it arrives first.
	_, err := tx.Exec(ctx, `INSERT INTO studio_order_payment_identities(source,merchant,environment,kind,external_id,order_id,transaction_id)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(source,merchant,environment,kind,external_id) DO UPDATE
 SET disputed=true WHERE NOT studio_order_payment_identities.disputed AND
 (studio_order_payment_identities.order_id,studio_order_payment_identities.transaction_id)<>(excluded.order_id,excluded.transaction_id)`,
		e.Scope.Source, e.Scope.Merchant, e.Scope.Environment, kind, id, e.OrderID, e.TransactionID)
	return err
}

func savePaymentFactSQL(ctx context.Context, tx pgx.Tx, e authenticatedOrderEvent) (string, error) {
	if err := claimPaymentIdentitySQL(ctx, tx, e, "transaction", e.TransactionID); err != nil {
		return "", err
	}
	identityKind := "transaction"
	if e.Kind == "refund" {
		identityKind = "refund"
		if err := claimPaymentIdentitySQL(ctx, tx, e, identityKind, e.RefundID); err != nil {
			return "", err
		}
	}
	fact := e
	fact.EventID = "" // The delivery ID may change while the economic event stays the same.
	digest := payloadHash(fact)
	tag, err := tx.Exec(ctx, `INSERT INTO studio_order_payment_facts(order_id,kind,external_id,transaction_id,currency,amount_minor,occurred_at,nano_remainder,fact_hash)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, e.OrderID, e.Kind, e.economicID(), e.TransactionID, e.Currency, e.AmountMinor, e.OccurredAt.Truncate(time.Microsecond), e.OccurredAt.Nanosecond()%1000, digest[:])
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return "RECORDED", nil
	}
	var original []byte
	err = tx.QueryRow(ctx, `SELECT fact_hash FROM studio_order_payment_facts WHERE order_id=$1 AND kind=$2 AND external_id=$3`, e.OrderID, e.Kind, e.economicID()).Scan(&original)
	if err != nil {
		return "", err
	}
	if bytes.Equal(original, digest[:]) {
		return "SAME_FACT", nil
	}
	_, err = tx.Exec(ctx, `UPDATE studio_order_payment_identities SET disputed=true WHERE source=$1 AND merchant=$2 AND environment=$3 AND kind=$4 AND external_id=$5 AND NOT disputed`, e.Scope.Source, e.Scope.Merchant, e.Scope.Environment, identityKind, e.economicID())
	return "DISPUTED", err
}

func quarantinePaymentConflictSQL(ctx context.Context, tx pgx.Tx, e authenticatedOrderEvent, original orderPaymentReceipt, digest [32]byte) error {
	// Preserve both authenticated accounts of the event without treating their
	// different amounts as a second receipt of money. No balance moves here.
	// A later notification cannot use this conflicting transaction or refund
	// for a third purchase just because the first delivery ID was occupied.
	if err := claimPaymentIdentitySQL(ctx, tx, e, "transaction", e.TransactionID); err != nil {
		return err
	}
	if e.Kind == "refund" {
		if err := claimPaymentIdentitySQL(ctx, tx, e, "refund", e.RefundID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE studio_order_payment_identities SET disputed=true WHERE
	 (source,merchant,environment)=($1,$2,$3) AND NOT disputed
	 AND ((kind='transaction' AND external_id=$4) OR (kind='refund' AND external_id=$5))`,
		e.Scope.Source, e.Scope.Merchant, e.Scope.Environment, e.TransactionID, e.RefundID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_order_payment_conflicts(source,merchant,environment,event_id,claimed_order_id,request_hash,observation)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, e.Scope.Source, e.Scope.Merchant, e.Scope.Environment, e.EventID, e.OrderID, digest[:], e)
	if err != nil {
		return err
	}
	reasons := []string{"event_conflict"}
	if e.Kind == "refund" || original.Kind == "refund" {
		reasons = append(reasons, "refund_conflict")
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_order_payment_reviews(order_id,reason)
 SELECT DISTINCT id,reason FROM unnest(ARRAY[$1::text,$2::text]) AS t(id)
 CROSS JOIN unnest($3::text[]) AS r(reason) ORDER BY id,reason ON CONFLICT DO NOTHING`, original.OrderID, e.OrderID, reasons)
	return err
}

func issueOrderCreditSQL(ctx context.Context, tx pgx.Tx, e authenticatedOrderEvent) error {
	var amount int64
	if err := tx.QueryRow(ctx, "SELECT credit_micros FROM studio_order_intents WHERE id=$1", e.OrderID).Scan(&amount); err != nil {
		return err
	}
	// Capacity failure must preserve the actual payment and leave a review flag.
	// The savepoint also undoes any intermediate journal, posting or account edit.
	err := pgx.BeginFunc(ctx, tx, func(creditTx pgx.Tx) error {
		if err := ensureLedgerWalletSQL(ctx, creditTx, e.WorkspaceID); err != nil {
			return err
		}
		purchase := ledgerReservationRow{WorkspaceID: e.WorkspaceID, LedgerReservation: LedgerReservation{ResourceType: "order", ResourceID: e.OrderID}}
		journal, err := postLedgerTransferSQL(ctx, creditTx, purchase, "CREDIT", amount, "system:issuance", "available:"+e.WorkspaceID, "")
		if err != nil {
			return err
		}
		_, err = creditTx.Exec(ctx, "INSERT INTO studio_order_payment_credits(order_id,journal_id) VALUES($1,$2)", e.OrderID, journal)
		return err
	})
	var problem *operationError
	if errors.As(err, &problem) && problem.code == "LEDGER_CAPACITY" {
		_, err = tx.Exec(ctx, "INSERT INTO studio_order_payment_reviews(order_id,reason) VALUES($1,'credit_capacity') ON CONFLICT DO NOTHING", e.OrderID)
	}
	return err
}
