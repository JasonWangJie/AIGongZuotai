package studio

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

func (a *App) validateBillingAdmission(quoteID string, chat bool) error {
	if a.pricing == nil {
		if quoteID != "" {
			return failure(409, "BILLING_DISABLED", "当前服务不接受额度报价；请核对原请求状态")
		}
		return nil
	}
	if quoteID == "" {
		return failure(422, "QUOTE_REQUIRED", "请先取得与完整请求匹配的额度报价")
	}
	if len(quoteID) > 128 || !ledgerOpaquePattern.MatchString(quoteID) {
		return failure(409, "QUOTE_INVALID", "报价标识无效")
	}
	if chat {
		if _, ok := a.chat.(ChatUsageProvider); !ok {
			return failure(503, "CHAT_USAGE_UNSUPPORTED", "当前适配器不支持真实用量报告，未受理生成请求")
		}
	}
	return nil
}

func saveBilledTaskSQL(ctx context.Context, tx pgx.Tx, t *taskRecord) error {
	// Fence first. Any subsequent journal, asset, audit or outbox error rolls the
	// entire business update back; no charge can survive a stale worker result.
	if err := saveTaskSQL(ctx, tx, t); err != nil {
		return err
	}
	if err := registerTaskLibrarySQL(ctx, tx, t.Task); err != nil {
		return err
	}
	if t.Task.Billing == nil {
		return nil
	}
	delivered := 0
	if t.Task.Status == "SUCCEEDED" || t.Task.Status == "PARTIAL" {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM studio_library_entries WHERE task_id=$1 AND workspace_id=$2 AND source='generated'", t.Task.ID, t.Task.WorkspaceID).Scan(&delivered); err != nil {
			return err
		}
	}
	billing, err := applyResourceBillingSQL(ctx, tx, t.Task.WorkspaceID, "task", t.Task.ID, t.Task.Status, nil, delivered)
	if err != nil {
		return err
	}
	t.Task.Billing = billing
	blob, _ := json.Marshal(t.Task)
	_, err = tx.Exec(ctx, "UPDATE studio_tasks SET document=$2 WHERE id=$1 AND workspace_id=$3", t.Task.ID, blob, t.Task.WorkspaceID)
	return err
}

// Reconciliation is read-only. It detects projection drift; it never repairs a
// balance by inventing a compensating entry or deleting immutable evidence.
func reconcileLedgerSQL(ctx context.Context, tx pgx.Tx, workspace string) (LedgerReconciliation, error) {
	r := LedgerReconciliation{WorkspaceID: workspace}
	err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum((SELECT count(*) FROM studio_ledger_postings p WHERE p.journal_id=j.id)),0),NOT EXISTS(SELECT 1 FROM studio_ledger_journals x LEFT JOIN studio_ledger_postings p ON p.journal_id=x.id WHERE ($1='' OR x.workspace_id=$1) GROUP BY x.id HAVING count(p.account_id)<2 OR sum(p.delta_micros)<>0) FROM studio_ledger_journals j WHERE ($1='' OR j.workspace_id=$1)`, workspace).Scan(&r.JournalCount, &r.EntryCount, &r.Balanced)
	if err != nil {
		return r, err
	}
	// System accounts are included because workspace journals also affect them.
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM studio_ledger_accounts a LEFT JOIN studio_ledger_postings p ON p.account_id=a.id WHERE ($1='' OR a.workspace_id=$1 OR a.workspace_id IS NULL) GROUP BY a.id,a.balance_micros HAVING a.balance_micros<>COALESCE(sum(p.delta_micros),0)) AND NOT EXISTS(SELECT 1 FROM studio_ledger_wallets w LEFT JOIN studio_ledger_accounts a ON a.id='available:'||w.workspace_id LEFT JOIN studio_ledger_accounts h ON h.id='reserved:'||w.workspace_id WHERE ($1='' OR w.workspace_id=$1) AND (a.id IS NULL OR h.id IS NULL))`, workspace).Scan(&r.AccountBalancesMatch)
	if err != nil {
		return r, err
	}
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM studio_ledger_wallets w JOIN studio_ledger_accounts a ON a.id='reserved:'||w.workspace_id WHERE ($1='' OR w.workspace_id=$1) AND a.balance_micros<>COALESCE((SELECT sum(r.reserved_micros-r.charged_micros-r.released_micros) FROM studio_ledger_reservations r WHERE r.workspace_id=w.workspace_id),0))`, workspace).Scan(&r.HoldsMatch)
	if err != nil {
		return r, err
	}
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM studio_ledger_wallets w WHERE ($1='' OR w.workspace_id=$1) AND (w.charged_micros<>COALESCE((SELECT sum(j.amount_micros) FROM studio_ledger_journals j WHERE j.workspace_id=w.workspace_id AND j.kind='CHARGE'),0) OR w.refunded_micros<>COALESCE((SELECT sum(j.amount_micros) FROM studio_ledger_journals j WHERE j.workspace_id=w.workspace_id AND j.kind='REFUND'),0) OR w.charged_micros<>COALESCE((SELECT sum(r.charged_micros) FROM studio_ledger_reservations r WHERE r.workspace_id=w.workspace_id),0) OR w.refunded_micros<>COALESCE((SELECT sum(r.refunded_micros) FROM studio_ledger_reservations r WHERE r.workspace_id=w.workspace_id),0)))`, workspace).Scan(&r.TotalsMatch)
	return r, err
}
