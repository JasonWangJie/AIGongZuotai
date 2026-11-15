package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// LedgerAdmin is deliberately an offline operator surface, never an HTTP route.
// It does not load environment files, migrate, start a worker or contact a provider.
type LedgerAdminConfig struct {
	DatabaseURL string
	Action      string
	WorkspaceID string
	Credit      LedgerCreditRequest
	Refund      LedgerRefundRequest
	Resolve     LedgerResolveRequest
	Apply       bool
	PlanDigest  string
}

type LedgerAdminReport struct {
	Action         string               `json:"action"`
	SchemaVersion  int                  `json:"schema_version"`
	Before         LedgerWallet         `json:"before"`
	Result         *LedgerMutation      `json:"result,omitempty"`
	Reconciliation LedgerReconciliation `json:"reconciliation"`
	PlanDigest     string               `json:"plan_digest,omitempty"`
	Applied        bool                 `json:"applied"`
}

func AdministerLedger(ctx context.Context, c LedgerAdminConfig) (LedgerAdminReport, error) {
	r := LedgerAdminReport{Action: c.Action, SchemaVersion: 20}
	if c.DatabaseURL == "" {
		return r, errors.New("ledger administration requires DATABASE_URL")
	}
	if c.Action != "inspect" && c.Action != "grant" && c.Action != "refund" && c.Action != "resolve" {
		return r, errors.New("action must be inspect, grant, refund or resolve")
	}
	if c.Action == "inspect" && c.Apply {
		return r, errors.New("inspect cannot apply a mutation")
	}
	if c.Apply && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.PlanDigest) {
		return r, errors.New("apply requires a dry-run plan digest")
	}
	conn, err := pgx.Connect(ctx, c.DatabaseURL)
	if err != nil {
		return r, errors.New("ledger database connection unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
	}()
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(0x53545544494F)).Scan(&locked); err != nil || !locked {
		return r, errors.New("stop the workbench before ledger administration; writer lock unavailable")
	}
	var versions []int32
	if err = conn.QueryRow(ctx, "SELECT array_agg(version ORDER BY version) FROM studio_schema_versions").Scan(&versions); err != nil || len(versions) != 20 {
		return r, errors.New("ledger administration requires exactly schema versions 001..020; it does not migrate")
	}
	for i, v := range versions {
		if int(v) != i+1 {
			return r, errors.New("ledger database schema is incompatible")
		}
	}
	identity, err := storageOwnerID(ctx, conn)
	if err != nil {
		return r, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return r, errors.New("ledger transaction unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	workspace := c.WorkspaceID
	var request any
	var reservation *ledgerReservationRow
	switch c.Action {
	case "inspect":
		if workspace == "" {
			return r, errors.New("inspect requires workspace")
		}
	case "grant":
		workspace = c.Credit.WorkspaceID
		request = c.Credit
	case "refund", "resolve":
		id := c.Refund.ReservationID
		request = c.Refund
		if c.Action == "resolve" {
			id = c.Resolve.ReservationID
			request = c.Resolve
		}
		item, e := readLedgerReservationSQL(ctx, tx, id, true)
		if e != nil {
			return r, errors.New("ledger reservation unavailable")
		}
		reservation = &item
		workspace = item.WorkspaceID
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_workspaces WHERE id=$1)", workspace).Scan(&exists); err != nil || !exists {
		return r, errors.New("ledger workspace unavailable")
	}
	r.Before, err = readLedgerWalletSQL(ctx, tx, workspace)
	if err != nil {
		return r, errors.New("ledger wallet unavailable")
	}
	if c.Action != "inspect" {
		// Generated journal IDs and timestamps are intentionally not part of this
		// digest. Database identity, the exact request and mutable input versions are.
		plan, _ := json.Marshal(struct {
			Identity, Action string
			Request          any
			Wallet           LedgerWallet
			Reservation      *ledgerReservationRow
		}{identity, c.Action, request, r.Before, reservation})
		sum := sha256.Sum256(plan)
		r.PlanDigest = hex.EncodeToString(sum[:])
		var result LedgerMutation
		switch c.Action {
		case "grant":
			result, err = creditLedgerSQL(ctx, tx, c.Credit)
		case "refund":
			result, err = refundLedgerSQL(ctx, tx, c.Refund)
		case "resolve":
			result, err = resolveLedgerSQL(ctx, tx, c.Resolve)
		}
		if err != nil {
			return r, err
		}
		r.Result = &result
		// An exact original operation replay is read-only and returns its stored
		// receipt. Later wallet changes must not prevent recovery of that receipt.
		if c.Apply && !result.Replayed && c.PlanDigest != r.PlanDigest {
			return r, errors.New("ledger plan changed; inspect a new dry-run before apply")
		}
		if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return r, errors.New("ledger integrity checks rejected the operation")
		}
	}
	r.Reconciliation, err = reconcileLedgerSQL(ctx, tx, workspace)
	if err != nil {
		return r, errors.New("ledger reconciliation unavailable")
	}
	if !r.Reconciliation.Balanced || !r.Reconciliation.AccountBalancesMatch || !r.Reconciliation.HoldsMatch || !r.Reconciliation.TotalsMatch {
		return r, errors.New("ledger reconciliation failed; operation rolled back")
	}
	if c.Apply {
		if err = tx.Commit(ctx); err != nil {
			return r, errors.New("ledger commit outcome is unknown; replay the exact operation key and request")
		}
		r.Applied = true
	}
	return r, nil
}
