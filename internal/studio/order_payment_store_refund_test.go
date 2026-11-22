package studio

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func refundOf(e authenticatedOrderEvent, amount int64) authenticatedOrderEvent {
	e.EventID = newID("notice")
	e.Kind = "refund"
	e.RefundID = newID("refund")
	e.AmountMinor = amount
	return e
}

func TestPostgresOrderPaymentRefundOrderAndMismatch(t *testing.T) {
	f := newPaymentFixture(t)
	payment := f.order(t, true)
	refund := refundOf(payment, 1200)
	before := f.record(t, refund)
	if p := before.Projection; p.PaymentState != "AWAITING" || p.RefundState != "PENDING_MATCH" || p.RefundedMinor != "1200" || p.CreditState != "REVIEW" {
		t.Fatal(p)
	}
	caughtUp := f.record(t, payment).Projection
	if caughtUp.PaymentState != "RECEIVED" || caughtUp.RefundState != "FULL" || caughtUp.GrantedCreditMicros != "0" || caughtUp.CreditState != "REVIEW" {
		t.Fatal(caughtUp)
	}
	if got := f.record(t, refund); got != before {
		t.Fatal("original refund acknowledgement was rewritten")
	}
	for _, tc := range []string{"currency", "amount", "future", "predates_binding"} {
		e := f.order(t, true)
		switch tc {
		case "currency":
			e.Currency = "JPY"
		case "amount":
			e.AmountMinor = 1199
		case "future":
			e.OccurredAt = e.OccurredAt.Add(1500 * time.Millisecond)
		case "predates_binding":
			e.OccurredAt = e.OccurredAt.Add(-48 * time.Hour)
		}
		got := f.record(t, e).Projection
		if got.PaymentState != "REVIEW" || got.GrantedCreditMicros != "0" || got.CreditState != "REVIEW" {
			t.Fatal(tc, got)
		}
		if tc == "currency" && got.PaidMinor != "0" {
			t.Fatal("mixed currencies", got)
		}
		if tc == "future" {
			// Wait on the same database clock that accepted the first receipt.
			// Both an owner read and an authenticated re-notification must keep
			// the original time disagreement once that future instant has passed.
			_, err := f.admin.Exec(context.Background(), "SELECT pg_sleep(GREATEST(0,extract(epoch FROM $1::timestamptz-clock_timestamp()))::double precision + 0.002)", e.OccurredAt)
			if err != nil {
				t.Fatal(err)
			}
			if latest := f.current(t, e); latest != got {
				t.Fatal("wall clock silently resolved future-dated cash", latest)
			}
			if replay := f.record(t, e); replay.Projection != got {
				t.Fatal("receipt replay ignored original acceptance time", replay)
			}
			e.EventID = newID("notice")
			if duplicate := f.record(t, e); duplicate.Projection != got {
				t.Fatal("new delivery ID released quarantined CREDIT", duplicate)
			}
		}
	}
	// The refund was reported first and actually preceded its alleged payment.
	// Even after both facts arrive, temporal inconsistency cannot be upgraded.
	p := f.order(t, true)
	r := refundOf(p, 300)
	f.record(t, r)
	if err := f.admin.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&p.OccurredAt); err != nil {
		t.Fatal(err)
	}
	if got := f.record(t, p).Projection; got.RefundState != "REVIEW" || got.GrantedCreditMicros != "0" {
		t.Fatal(got)
	}
	p = f.order(t, true)
	f.record(t, p)
	r = refundOf(p, 1201)
	if got := f.record(t, r).Projection; got.RefundState != "REVIEW" || got.RefundedMinor != "1201" {
		t.Fatal("over-refund not preserved", got)
	}
	// A refund ID cannot migrate to a new purchase even when its purported
	// parent transaction is also changed.
	other := f.order(t, true)
	repeated := refundOf(other, 1201)
	repeated.RefundID = r.RefundID
	if got := f.record(t, repeated).Projection; got.RefundState != "REVIEW" || got.RefundedMinor != "0" {
		t.Fatal("ambiguous refund was counted", got)
	}
	if got := f.current(t, p); got.RefundState != "REVIEW" || got.RefundedMinor != "0" {
		t.Fatal("first claimed refund ignored ambiguity", got)
	}
}

func TestPostgresOrderPaymentRefundKeepsHeldReviewAndOtherPurchase(t *testing.T) {
	f := newPaymentFixture(t)
	purchase := f.order(t, true)
	f.record(t, purchase)
	// Consume 9M and leave precisely 1M HELD and 10M REVIEW across two
	// distinct issued purchases. No AVAILABLE credits remain to return.
	other := f.order(t, true)
	f.record(t, other)
	ctx := context.Background()
	for i, amount := range []int64{9_000_000, 1_000_000, 10_000_000} {
		q := ledgerQuoteRow{ID: newID("qte"), WorkspaceID: purchase.WorkspaceID, ProjectID: f.owner.Project.ID, OwnerUserID: purchase.OwnerID, ResourceType: "task", ModelID: "test-image", ModelVersion: "v1", PriceVersion: "price1", RequestHash: payloadHash(i), ReserveMicros: amount, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour), Price: LedgerPriceSnapshot{ProviderMode: "demo", Basis: "delivered_artifact", ArtifactMicros: amount, RequestedUnits: 1}}
		err := pgx.BeginFunc(ctx, f.admin, func(tx pgx.Tx) error {
			if err := insertLedgerQuoteSQL(ctx, tx, q); err != nil {
				return err
			}
			id := newID("task")
			_, err := reserveLedgerSQL(ctx, tx, LedgerReservationRequest{ActorUserID: purchase.OwnerID, WorkspaceID: q.WorkspaceID, ProjectID: q.ProjectID, QuoteID: q.ID, ResourceType: q.ResourceType, ResourceID: id, ModelID: q.ModelID, ModelVersion: q.ModelVersion, ProviderMode: "demo", RequestHash: q.RequestHash})
			if err != nil {
				return err
			}
			switch i {
			case 0:
				_, err = applyResourceBillingSQL(ctx, tx, q.WorkspaceID, "task", id, "SUCCEEDED", nil, 1)
			case 2:
				_, err = applyResourceBillingSQL(ctx, tx, q.WorkspaceID, "task", id, "SUBMIT_UNKNOWN", nil, 0)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	wallet, err := readLedgerWalletSQL(ctx, f.admin, purchase.WorkspaceID)
	if err != nil || wallet.AvailableMicros != 0 || wallet.ReservedMicros != 11_000_000 || wallet.ReviewMicros != 10_000_000 || wallet.ChargedMicros != 9_000_000 {
		t.Fatal(wallet, err)
	}
	tables := []string{"studio_ledger_wallets", "studio_ledger_accounts", "studio_ledger_journals", "studio_ledger_postings", "studio_ledger_quotes", "studio_ledger_reservations", "studio_order_payment_credits"}
	before := schedulerOriginalSnapshot(t, f.admin, tables)
	partial := refundOf(purchase, 300)
	if got := f.record(t, partial).Projection; got.RefundState != "PARTIAL" || got.CreditState != "REVIEW" || got.GrantedCreditMicros != "10000000" || got.ReversedCreditMicros != "0" {
		t.Fatal(got)
	}
	rest := refundOf(purchase, 900)
	if got := f.record(t, rest).Projection; got.RefundState != "FULL" || got.PaymentState != "RECEIVED" || got.RefundedMinor != "1200" {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, f.admin, tables)) {
		t.Fatal("refund mutated a wallet, hold, consumption posting or original purchase")
	}
	if got := f.current(t, other); got.CreditState != "ISSUED" || got.RefundState != "NONE" {
		t.Fatal("different purchase was affected", got)
	}
}

func TestPostgresOrderPaymentCreditCapacityPreservesCashReceipt(t *testing.T) {
	f := newPaymentFixture(t)
	e := f.order(t, true)
	err := ledgerTx(f.app.store, func(ctx context.Context, tx pgx.Tx) error {
		_, err := creditLedgerSQL(ctx, tx, LedgerCreditRequest{WorkspaceID: e.WorkspaceID, OperationKey: newID("grant"), Reason: "fixture", EvidenceRef: "fixture:preexisting", AmountMicros: MaxLedgerMicros})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_ledger_journals", "studio_ledger_postings", "studio_ledger_accounts", "studio_ledger_wallets"}
	before := schedulerOriginalSnapshot(t, f.admin, tables)
	receipt := f.record(t, e)
	p := receipt.Projection
	if p.PaymentState != "RECEIVED" || p.CreditState != "REVIEW" || p.PaidMinor != "1200" || p.GrantedCreditMicros != "0" {
		t.Fatal(p)
	}
	if again := f.record(t, e); again != receipt {
		t.Fatal("capacity receipt changed")
	}
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, f.admin, tables)) {
		t.Fatal("failed issuance left partial ledger writes")
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_pending_slots WHERE order_id=$1", e.OrderID); n != 1 {
		t.Fatal("unresolved purchase ceased using pending capacity")
	}
}

func TestPostgresOrderPaymentPendingLimitReleasedByIssuedPurchase(t *testing.T) {
	f := newPaymentFixture(t)
	paid := f.order(t, true)
	unclear := f.order(t, true)
	for i := 2; i < 32; i++ {
		f.order(t, false)
	}
	_, _, err := f.app.createOrderIntent(f.owner, orderStoreInput(f.app), newID("intent"))
	requireOperation(t, err, "ORDER_INTENT_PENDING_LIMIT")
	unclear.AmountMinor--
	if got := f.record(t, unclear).Projection; got.CreditState != "REVIEW" {
		t.Fatal(got)
	}
	_, _, err = f.app.createOrderIntent(f.owner, orderStoreInput(f.app), newID("intent"))
	requireOperation(t, err, "ORDER_INTENT_PENDING_LIMIT")
	f.record(t, paid)
	key := newID("intent")
	newOrder, _, err := f.app.createOrderIntent(f.owner, orderStoreInput(f.app), key)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.app.createOrderIntent(f.owner, orderStoreInput(f.app), newID("intent"))
	requireOperation(t, err, "ORDER_INTENT_PENDING_LIMIT")
	again, replay, err := f.app.createOrderIntent(f.owner, orderStoreInput(f.app), key)
	if err != nil || !replay || newOrder != again {
		t.Fatal("full accounts lost original receipt", err)
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_intents WHERE state='PENDING' AND owner_user_id=$1", paid.OwnerID); n != 33 {
		t.Fatal("033 state/history changed", n)
	}
	if n := outboxCount(t, f.admin, "SELECT count(*) FROM studio_order_pending_slots WHERE owner_user_id=$1", paid.OwnerID); n != 32 {
		t.Fatal("unsupported pending capacity", n)
	}
}
