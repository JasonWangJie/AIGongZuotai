package studio

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

func insertLedgerQuoteSQL(ctx context.Context, tx pgx.Tx, v ledgerQuoteRow) error {
	// The control writer is single-instance. This also serializes direct SQL
	// fixtures and prevents capacity check/insert races on independent sessions.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x4c454447455251)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM studio_ledger_quotes WHERE id IN(SELECT q.id FROM studio_ledger_quotes q WHERE q.reservation_id IS NULL AND q.expires_at<clock_timestamp()-interval '1 day' AND NOT EXISTS(SELECT 1 FROM studio_credit_risk_reviews r WHERE r.quote_id=q.id) ORDER BY q.expires_at,q.id LIMIT 1000 FOR UPDATE OF q SKIP LOCKED)`); err != nil {
		return err
	}
	var own, total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE workspace_id=$1),count(*) FROM (SELECT workspace_id FROM studio_ledger_quotes WHERE reservation_id IS NULL AND expires_at>clock_timestamp() LIMIT 10001) q`, v.WorkspaceID).Scan(&own, &total); err != nil {
		return err
	}
	if own >= 64 || total >= 10000 {
		return failure(429, "QUOTE_LIMIT", "有效报价数量已达上限，请等待旧报价过期")
	}
	if v.ReserveMicros < 1 || v.ReserveMicros > MaxLedgerMicros {
		return failure(422, "INVALID_LEDGER_AMOUNT", "报价冻结额度超出范围")
	}
	price, err := json.Marshal(v.Price)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_ledger_quotes(id,workspace_id,project_id,resource_type,conversation_id,model_id,model_version,price_version,request_hash,price,reserve_micros,expires_at,created_at,owner_user_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''))`, v.ID, v.WorkspaceID, v.ProjectID, v.ResourceType, v.ConversationID, v.ModelID, v.ModelVersion, v.PriceVersion, v.RequestHash[:], price, v.ReserveMicros, v.ExpiresAt, v.CreatedAt, v.OwnerUserID)
	return err
}

func ensureLedgerWalletSQL(ctx context.Context, tx pgx.Tx, workspace string) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_workspaces WHERE id=$1)", workspace).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return failure(404, "NOT_FOUND", "工作区不存在")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO studio_ledger_wallets(workspace_id) VALUES($1) ON CONFLICT DO NOTHING", workspace); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO studio_ledger_accounts(id,workspace_id,kind) VALUES('available:'||$1,$1,'available'),('reserved:'||$1,$1,'reserved') ON CONFLICT DO NOTHING`, workspace); err != nil {
		return err
	}
	var id string
	return tx.QueryRow(ctx, "SELECT workspace_id FROM studio_ledger_wallets WHERE workspace_id=$1 FOR UPDATE", workspace).Scan(&id)
}

func reserveLedgerSQL(ctx context.Context, tx pgx.Tx, request LedgerReservationRequest) (*BillingSummary, error) {
	if err := lockGenerationWorkspaceSQL(ctx, tx, request.WorkspaceID, false); err != nil {
		return nil, err
	}
	q, err := readLedgerQuoteSQL(ctx, tx, request.QuoteID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, failure(409, "QUOTE_INVALID", "报价不存在或不可用于该请求")
	}
	if err != nil {
		return nil, err
	}
	if request.RequireOwnedQuote && (q.OwnerUserID == "" || q.OwnerUserID != request.ActorUserID) {
		return nil, failure(409, "QUOTE_MISMATCH", "此原视频报价仅供原申请人提交")
	}
	if q.WorkspaceID != request.WorkspaceID || q.ProjectID != request.ProjectID || q.ResourceType != request.ResourceType || q.ConversationID != request.ConversationID || q.ModelID != request.ModelID || q.ModelVersion != request.ModelVersion || q.RequestHash != request.RequestHash || q.Price.ProviderMode != request.ProviderMode {
		return nil, failure(409, "QUOTE_MISMATCH", "报价与原请求或归属不匹配")
	}
	if q.ReservationID != "" {
		return nil, failure(409, "QUOTE_USED", "报价已经用于其他受理，请核对原幂等请求")
	}
	held, admittedAt, err := lockedSpendableQuoteSQL(ctx, tx, q)
	if err != nil {
		return nil, err
	}
	permit, err := creditRiskAdmissionSQL(ctx, tx, q, request.ActorUserID, held, admittedAt)
	if err != nil {
		return nil, err
	}
	id := newID("res")
	price, _ := json.Marshal(q.Price)
	_, err = tx.Exec(ctx, `INSERT INTO studio_ledger_reservations(id,quote_id,workspace_id,project_id,resource_type,resource_id,model_id,price_version,price,reserved_micros,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, q.ID, q.WorkspaceID, q.ProjectID, q.ResourceType, request.ResourceID, q.ModelID, q.PriceVersion, price, q.ReserveMicros, admittedAt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE studio_ledger_quotes SET reservation_id=$2 WHERE id=$1", q.ID, id); err != nil {
		return nil, err
	}
	if permit != "" {
		tag, e := tx.Exec(ctx, `UPDATE studio_credit_risk_permits SET reservation_id=$2 WHERE review_id=$1 AND reservation_id IS NULL AND expires_at>clock_timestamp()`, permit, id)
		if e != nil {
			return nil, e
		}
		if tag.RowsAffected() != 1 {
			return nil, failure(409, "CREDIT_RISK_PERMIT_STALE", "原报价的单次复核许可已变化")
		}
	}
	r := ledgerReservationRow{LedgerReservation: LedgerReservation{BillingSummary: BillingSummary{QuoteID: q.ID, ReservationID: id, PriceVersion: q.PriceVersion, State: "HELD", ReservedMicros: q.ReserveMicros, Unit: LedgerUnit, Scale: LedgerScale, Version: 1}, ProjectID: q.ProjectID, ResourceType: q.ResourceType, ResourceID: request.ResourceID, ModelID: q.ModelID}, WorkspaceID: q.WorkspaceID, Price: q.Price}
	if _, err = postLedgerTransferSQL(ctx, tx, r, "RESERVE", q.ReserveMicros, "available:"+q.WorkspaceID, "reserved:"+q.WorkspaceID, ""); err != nil {
		return nil, err
	}
	return copyBilling(&r.BillingSummary), nil
}

func readLedgerReservationSQL(ctx context.Context, q ledgerQuerier, id string, lock bool) (ledgerReservationRow, error) {
	var r ledgerReservationRow
	var price, usage []byte
	sql := `SELECT id,quote_id,workspace_id,project_id,resource_type,resource_id,model_id,price_version,price,state,reserved_micros,charged_micros,released_micros,refunded_micros,started,review_reason,usage,COALESCE(charge_journal_id,''),version,created_at,updated_at FROM studio_ledger_reservations WHERE id=$1`
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, id).Scan(&r.ReservationID, &r.QuoteID, &r.WorkspaceID, &r.ProjectID, &r.ResourceType, &r.ResourceID, &r.ModelID, &r.PriceVersion, &price, &r.State, &r.ReservedMicros, &r.ChargedMicros, &r.ReleasedMicros, &r.RefundedMicros, &r.Started, &r.ReviewReason, &usage, &r.ChargeJournalID, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	r.Unit = LedgerUnit
	r.Scale = LedgerScale
	if err = json.Unmarshal(price, &r.Price); err != nil {
		return r, err
	}
	if len(usage) > 0 && string(usage) != "null" {
		err = json.Unmarshal(usage, &r.Usage)
	}
	return r, err
}

func postLedgerTransferSQL(ctx context.Context, tx pgx.Tx, r ledgerReservationRow, kind string, amount int64, from, to, reversal string) (string, error) {
	if amount < 1 || amount > MaxLedgerMicros || from == to {
		return "", failure(422, "INVALID_LEDGER_AMOUNT", "账本分录金额或账户无效")
	}
	ids := []string{from, to}
	sort.Strings(ids)
	balances := map[string]int64{}
	for _, id := range ids {
		var balance int64
		if err := tx.QueryRow(ctx, "SELECT balance_micros FROM studio_ledger_accounts WHERE id=$1 FOR UPDATE", id).Scan(&balance); err != nil {
			return "", err
		}
		balances[id] = balance
	}
	if balances[from] < -MaxLedgerMicros+amount || balances[to] > MaxLedgerMicros-amount {
		return "", failure(409, "LEDGER_CAPACITY", "账本账户已达整数额度上限")
	}
	if from == "available:"+r.WorkspaceID && balances[from] < amount {
		return "", failure(402, "INSUFFICIENT_CREDITS", "可用额度不足")
	}
	if from == "reserved:"+r.WorkspaceID && balances[from] < amount {
		return "", errors.New("ledger reserved projection mismatch")
	}
	available, held := int64(0), int64(0)
	if from == "available:"+r.WorkspaceID {
		available -= amount
	}
	if to == "available:"+r.WorkspaceID {
		available += amount
	}
	if from == "reserved:"+r.WorkspaceID {
		held -= amount
	}
	if to == "reserved:"+r.WorkspaceID {
		held += amount
	}
	id := newID("jrn")
	_, err := tx.Exec(ctx, `INSERT INTO studio_ledger_journals(id,workspace_id,project_id,kind,resource_type,resource_id,reservation_id,quote_id,reversal_of,amount_micros,available_delta_micros,reserved_delta_micros) VALUES($1,$2,nullif($3,''),$4,nullif($5,''),nullif($6,''),nullif($7,''),nullif($8,''),nullif($9,''),$10,$11,$12)`, id, r.WorkspaceID, r.ProjectID, kind, r.ResourceType, r.ResourceID, r.ReservationID, r.QuoteID, reversal, amount, available, held)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO studio_ledger_postings(journal_id,account_id,delta_micros) VALUES($1,$2,$4::bigint),($1,$3,-$4::bigint)", id, to, from, amount); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "UPDATE studio_ledger_accounts SET balance_micros=balance_micros+$2 WHERE id=$1", from, -amount); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "UPDATE studio_ledger_accounts SET balance_micros=balance_micros+$2 WHERE id=$1", to, amount); err != nil {
		return "", err
	}
	charged, refunded := int64(0), int64(0)
	if kind == "CHARGE" {
		charged = amount
	}
	if kind == "REFUND" {
		refunded = amount
	}
	tag, err := tx.Exec(ctx, `UPDATE studio_ledger_wallets SET charged_micros=charged_micros+$2,refunded_micros=refunded_micros+$3,version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND charged_micros<=$4::bigint-$2::bigint AND refunded_micros<=$4::bigint-$3::bigint`, r.WorkspaceID, charged, refunded, MaxLedgerMicros)
	if err == nil && tag.RowsAffected() != 1 {
		return "", failure(409, "LEDGER_CAPACITY", "账本累计额度已达上限")
	}
	return id, err
}

func writeLedgerReservationSQL(ctx context.Context, tx pgx.Tx, r *ledgerReservationRow) error {
	usage, _ := json.Marshal(r.Usage)
	return tx.QueryRow(ctx, `UPDATE studio_ledger_reservations SET state=$2,charged_micros=$3,released_micros=$4,refunded_micros=$5,started=$6,review_reason=$7,usage=$8,charge_journal_id=nullif($9,''),version=version+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING version,updated_at`, r.ReservationID, r.State, r.ChargedMicros, r.ReleasedMicros, r.RefundedMicros, r.Started, r.ReviewReason, usage, r.ChargeJournalID).Scan(&r.Version, &r.UpdatedAt)
}

func settleLedgerReservationSQL(ctx context.Context, tx pgx.Tx, r *ledgerReservationRow, charge int64) error {
	remaining := r.ReservedMicros - r.ChargedMicros - r.ReleasedMicros
	if charge < 0 || charge > remaining {
		return failure(409, "LEDGER_HOLD_EXCEEDED", "结算额度不能超过尚未处理的冻结额度")
	}
	if err := ensureLedgerWalletSQL(ctx, tx, r.WorkspaceID); err != nil {
		return err
	}
	if charge > 0 {
		id, err := postLedgerTransferSQL(ctx, tx, *r, "CHARGE", charge, "reserved:"+r.WorkspaceID, "system:revenue", "")
		if err != nil {
			return err
		}
		r.ChargeJournalID = id
		r.ChargedMicros += charge
	}
	released := remaining - charge
	if released > 0 {
		if _, err := postLedgerTransferSQL(ctx, tx, *r, "RELEASE", released, "reserved:"+r.WorkspaceID, "available:"+r.WorkspaceID, ""); err != nil {
			return err
		}
		r.ReleasedMicros += released
	}
	r.State = "SETTLED"
	if r.ChargedMicros == 0 {
		r.State = "RELEASED"
	}
	r.ReviewReason = ""
	return writeLedgerReservationSQL(ctx, tx, r)
}

func ledgerUsageCharge(r ledgerReservationRow, usage *ChatUsage) (int64, bool) {
	if usage == nil || (usage.Status == "reported" && (usage.Source != "gateway" || r.Price.ProviderMode != "live")) || (usage.Status == "demo" && (usage.Source != "demo" || r.Price.ProviderMode != "demo")) {
		return 0, false
	}
	if usage == nil || !usage.Complete || (usage.Status != "reported" && usage.Status != "demo") || usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens || usage.PromptTokens > r.Price.MaxInputTokens || usage.CompletionTokens > r.Price.MaxOutputTokens {
		return 0, false
	}
	value, err := ledgerChatAmount(r.Price, usage.PromptTokens, usage.CompletionTokens)
	return value, err == nil && value <= r.ReservedMicros-r.ChargedMicros-r.ReleasedMicros
}

// This runs inside the same transaction as the fenced business state write.
// A missing reservation is a legacy resource and never creates a retroactive debit.
func applyResourceBillingSQL(ctx context.Context, tx pgx.Tx, workspace, kind, id, status string, usage *ChatUsage, delivered int) (*BillingSummary, error) {
	var reservationID string
	err := tx.QueryRow(ctx, "SELECT id FROM studio_ledger_reservations WHERE workspace_id=$1 AND resource_type=$2 AND resource_id=$3", workspace, kind, id).Scan(&reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := readLedgerReservationSQL(ctx, tx, reservationID, true)
	if err != nil {
		return nil, err
	}
	if r.State != "HELD" && r.State != "REVIEW" {
		return copyBilling(&r.BillingSummary), nil
	}
	if status == "RUNNING" || status == "SUBMITTING" || status == "QUEUED_UPSTREAM" || status == "GENERATING" || status == "SAVING" {
		if !r.Started {
			r.Started = true
			if err = writeLedgerReservationSQL(ctx, tx, &r); err != nil {
				return nil, err
			}
		}
		return copyBilling(&r.BillingSummary), nil
	}
	if !terminal(status) && status != "SUBMIT_UNKNOWN" && status != "RECONCILE_REQUIRED" && status != "PROVIDER_STATE_UNKNOWN" {
		return copyBilling(&r.BillingSummary), nil
	}
	// All accepted-but-uncertain outcomes remain held, even with no output.
	if kind == "run" && status == "COMPLETED" {
		if amount, ok := ledgerUsageCharge(r, usage); ok {
			r.Usage = usage
			err = settleLedgerReservationSQL(ctx, tx, &r, amount)
			return copyBilling(&r.BillingSummary), err
		}
	}
	if kind == "task" && (status == "SUCCEEDED" || status == "PARTIAL") && delivered > 0 && delivered <= r.Price.RequestedUnits && r.Price.ArtifactMicros > 0 && int64(delivered) <= MaxLedgerMicros/r.Price.ArtifactMicros {
		amount := int64(delivered) * r.Price.ArtifactMicros
		if amount <= r.ReservedMicros-r.ChargedMicros-r.ReleasedMicros {
			err = settleLedgerReservationSQL(ctx, tx, &r, amount)
			return copyBilling(&r.BillingSummary), err
		}
	}
	if !r.Started && status != "SUBMIT_UNKNOWN" && status != "RECONCILE_REQUIRED" && status != "PROVIDER_STATE_UNKNOWN" {
		err = settleLedgerReservationSQL(ctx, tx, &r, 0)
		return copyBilling(&r.BillingSummary), err
	}
	reason := "EXECUTION_UNCERTAIN"
	if kind == "run" && status == "COMPLETED" {
		reason = "USAGE_REQUIRES_REVIEW"
	}
	if kind == "task" && (status == "SUCCEEDED" || status == "PARTIAL") {
		reason = "DELIVERY_REQUIRES_REVIEW"
	}
	if r.State != "REVIEW" || r.ReviewReason != reason || usage != nil {
		r.State = "REVIEW"
		r.ReviewReason = reason
		if usage != nil {
			r.Usage = usage
		}
		if err = writeLedgerReservationSQL(ctx, tx, &r); err != nil {
			return nil, err
		}
	}
	return copyBilling(&r.BillingSummary), nil
}

var ledgerOpaquePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func beginLedgerOperationSQL(ctx context.Context, tx pgx.Tx, key, purpose, reason, evidence string, request any) (LedgerMutation, bool, error) {
	var result LedgerMutation
	if len(key) < 8 || !ledgerOpaquePattern.MatchString(key) || !ledgerOpaquePattern.MatchString(reason) || !ledgerOpaquePattern.MatchString(evidence) {
		return result, false, failure(422, "INVALID_LEDGER_OPERATION", "需要8至128字符操作键及不含敏感正文的原因和证据标识")
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "ledger-operation:"+key); err != nil {
		return result, false, err
	}
	hash := payloadHash(struct {
		Purpose string
		Request any
	}{purpose, request})
	var old, blob []byte
	err := tx.QueryRow(ctx, "SELECT request_hash,response FROM studio_ledger_operations WHERE operation_key=$1", key).Scan(&old, &blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if len(old) != 32 || string(old) != string(hash[:]) {
		return result, false, failure(409, "IDEMPOTENCY_CONFLICT", "操作键已经用于其他账本请求")
	}
	err = json.Unmarshal(blob, &result)
	result.Replayed = true
	return result, true, err
}
func finishLedgerOperationSQL(ctx context.Context, tx pgx.Tx, key, purpose, reason, evidence string, request any, r *ledgerReservationRow, workspace string) (LedgerMutation, error) {
	w, err := readLedgerWalletSQL(ctx, tx, workspace)
	if err != nil {
		return LedgerMutation{}, err
	}
	result := LedgerMutation{OperationID: newID("lop"), Wallet: w}
	if r != nil {
		copy := r.LedgerReservation
		result.Reservation = &copy
		if err = syncLedgerResourceDocumentSQL(ctx, tx, *r); err != nil {
			return result, err
		}
	}
	hash := payloadHash(struct {
		Purpose string
		Request any
	}{purpose, request})
	blob, _ := json.Marshal(result)
	_, err = tx.Exec(ctx, "INSERT INTO studio_ledger_operations(operation_key,request_hash,operation_id,purpose,reason,evidence_ref,response) VALUES($1,$2,$3,$4,$5,$6,$7)", key, hash[:], result.OperationID, purpose, reason, evidence, blob)
	return result, err
}
func creditLedgerSQL(ctx context.Context, tx pgx.Tx, in LedgerCreditRequest) (LedgerMutation, error) {
	old, replayed, err := beginLedgerOperationSQL(ctx, tx, in.OperationKey, "credit", in.Reason, in.EvidenceRef, in)
	if err != nil || replayed {
		return old, err
	}
	if in.AmountMicros <= 0 || in.AmountMicros > MaxLedgerMicros {
		return old, failure(422, "INVALID_LEDGER_AMOUNT", "授予额度须为允许范围内的正整数")
	}
	if err = ensureLedgerWalletSQL(ctx, tx, in.WorkspaceID); err != nil {
		return old, err
	}
	if _, err = postLedgerTransferSQL(ctx, tx, ledgerReservationRow{WorkspaceID: in.WorkspaceID}, "CREDIT", in.AmountMicros, "system:issuance", "available:"+in.WorkspaceID, ""); err != nil {
		return old, err
	}
	return finishLedgerOperationSQL(ctx, tx, in.OperationKey, "credit", in.Reason, in.EvidenceRef, in, nil, in.WorkspaceID)
}
func refundLedgerSQL(ctx context.Context, tx pgx.Tx, in LedgerRefundRequest) (LedgerMutation, error) {
	old, replayed, err := beginLedgerOperationSQL(ctx, tx, in.OperationKey, "refund", in.Reason, in.EvidenceRef, in)
	if err != nil || replayed {
		return old, err
	}
	r, err := readLedgerReservationSQL(ctx, tx, in.ReservationID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return old, failure(404, "NOT_FOUND", "结算记录不存在")
	}
	if err != nil {
		return old, err
	}
	if r.State != "SETTLED" || r.ChargedMicros <= 0 || r.RefundedMicros != 0 || r.ChargeJournalID == "" {
		return old, failure(409, "LEDGER_REFUND_NOT_ALLOWED", "仅支持已结算正数消费的一次全额冲正")
	}
	if err = ensureLedgerWalletSQL(ctx, tx, r.WorkspaceID); err != nil {
		return old, err
	}
	if _, err = postLedgerTransferSQL(ctx, tx, r, "REFUND", r.ChargedMicros, "system:revenue", "reserved:"+r.WorkspaceID, r.ChargeJournalID); err != nil {
		return old, err
	}
	if _, err = postLedgerTransferSQL(ctx, tx, r, "REFUND_RELEASE", r.ChargedMicros, "reserved:"+r.WorkspaceID, "available:"+r.WorkspaceID, ""); err != nil {
		return old, err
	}
	r.RefundedMicros = r.ChargedMicros
	r.State = "REFUNDED"
	if err = writeLedgerReservationSQL(ctx, tx, &r); err != nil {
		return old, err
	}
	return finishLedgerOperationSQL(ctx, tx, in.OperationKey, "refund", in.Reason, in.EvidenceRef, in, &r, r.WorkspaceID)
}
func resolveLedgerSQL(ctx context.Context, tx pgx.Tx, in LedgerResolveRequest) (LedgerMutation, error) {
	old, replayed, err := beginLedgerOperationSQL(ctx, tx, in.OperationKey, "resolve", in.Reason, in.EvidenceRef, in)
	if err != nil || replayed {
		return old, err
	}
	r, err := readLedgerReservationSQL(ctx, tx, in.ReservationID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return old, failure(404, "NOT_FOUND", "待核实记录不存在")
	}
	if err != nil {
		return old, err
	}
	if r.State != "REVIEW" {
		return old, failure(409, "LEDGER_REVIEW_REQUIRED", "仅可核实待核实冻结记录")
	}
	// A transient query outage still has a live recovery path. Operators must
	// not release its hold while the scheduler can silently resume delivery.
	var businessStatus string
	table := "studio_runs"
	if r.ResourceType == "task" {
		table = "studio_tasks"
	}
	if err = tx.QueryRow(ctx, "SELECT document->>'status' FROM "+table+" WHERE id=$1 AND workspace_id=$2", r.ResourceID, r.WorkspaceID).Scan(&businessStatus); err != nil {
		return old, err
	}
	if !terminal(businessStatus) && businessStatus != "SUBMIT_UNKNOWN" && businessStatus != "RECONCILE_REQUIRED" {
		return old, failure(409, "LEDGER_EXECUTION_ACTIVE", "任务仍可自动执行或查询，暂不能人工结算或释放")
	}
	amount := int64(0)
	switch in.Decision {
	case "release":
		if in.Usage != nil || in.DeliveredUnits != 0 {
			return old, failure(422, "INVALID_LEDGER_OPERATION", "释放操作不能同时提交收费计量")
		}
	case "charge":
		if r.ResourceType == "run" {
			if in.DeliveredUnits != 0 {
				return old, failure(422, "INVALID_USAGE_EVIDENCE", "LLM核实不能同时包含媒体交付数量")
			}
			var ok bool
			amount, ok = ledgerUsageCharge(r, in.Usage)
			if !ok {
				return old, failure(422, "INVALID_USAGE_EVIDENCE", "有效计量与冻结预算不匹配")
			}
			r.Usage = in.Usage
		} else {
			if in.Usage != nil || in.DeliveredUnits < 1 || in.DeliveredUnits > r.Price.RequestedUnits || r.Price.ArtifactMicros <= 0 || int64(in.DeliveredUnits) > MaxLedgerMicros/r.Price.ArtifactMicros {
				return old, failure(422, "INVALID_DELIVERY_EVIDENCE", "交付数量不在冻结规格范围内")
			}
			amount = int64(in.DeliveredUnits) * r.Price.ArtifactMicros
		}
	default:
		return old, failure(422, "INVALID_LEDGER_OPERATION", "核实操作必须是charge或release")
	}
	if err = settleLedgerReservationSQL(ctx, tx, &r, amount); err != nil {
		return old, err
	}
	return finishLedgerOperationSQL(ctx, tx, in.OperationKey, "resolve", in.Reason, in.EvidenceRef, in, &r, r.WorkspaceID)
}

func syncLedgerResourceDocumentSQL(ctx context.Context, tx pgx.Tx, r ledgerReservationRow) error {
	if r.ResourceType == "run" {
		var blob []byte
		if err := tx.QueryRow(ctx, "SELECT document FROM studio_runs WHERE id=$1 AND workspace_id=$2 FOR UPDATE", r.ResourceID, r.WorkspaceID).Scan(&blob); err != nil {
			return err
		}
		var run Run
		if err := json.Unmarshal(blob, &run); err != nil {
			return err
		}
		run.Billing = copyBilling(&r.BillingSummary)
		run.Seq++
		run.UpdatedAt = time.Now().UTC()
		if err := saveRunSQL(ctx, tx, run); err != nil {
			return err
		}
		return appendRunOutboxSQL(ctx, tx, run)
	}
	var blob []byte
	if err := tx.QueryRow(ctx, "SELECT document FROM studio_tasks WHERE id=$1 AND workspace_id=$2 FOR UPDATE", r.ResourceID, r.WorkspaceID).Scan(&blob); err != nil {
		return err
	}
	var task Task
	if err := json.Unmarshal(blob, &task); err != nil {
		return err
	}
	task.Billing = copyBilling(&r.BillingSummary)
	task.Version++
	task.UpdatedAt = time.Now().UTC()
	blob, _ = json.Marshal(task)
	if _, err := tx.Exec(ctx, "UPDATE studio_tasks SET document=$2,updated_at=$3 WHERE id=$1", task.ID, blob, task.UpdatedAt); err != nil {
		return err
	}
	return appendTaskOutboxSQL(ctx, tx, task)
}

func listLedgerEntriesSQL(ctx context.Context, q ledgerQuerier, workspace, project, kind string, limit int, before time.Time, beforeID string) ([]LedgerEntry, error) {
	items := []LedgerEntry{}
	if limit < 1 || limit > 101 {
		return nil, failure(400, "INVALID_LIMIT", "账本读取数量超出范围")
	}
	rows, err := q.Query(ctx, `SELECT id,kind,COALESCE(project_id,''),COALESCE(resource_type,''),COALESCE(resource_id,''),COALESCE(reservation_id,''),COALESCE(quote_id,''),COALESCE(reversal_of,''),amount_micros,available_delta_micros,reserved_delta_micros,created_at FROM studio_ledger_journals WHERE workspace_id=$1 AND ($2='' OR project_id=$2) AND ($3='' OR kind=$3) AND ($4::timestamptz='0001-01-01T00:00:00Z' OR (created_at,id)<($4,$5)) ORDER BY created_at DESC,id DESC LIMIT $6`, workspace, project, kind, before, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := LedgerEntry{Unit: LedgerUnit, Scale: LedgerScale}
		if err = rows.Scan(&v.ID, &v.Kind, &v.ProjectID, &v.ResourceType, &v.ResourceID, &v.ReservationID, &v.QuoteID, &v.ReversalOf, &v.AmountMicros, &v.AvailableDeltaMicros, &v.ReservedDeltaMicros, &v.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func listLedgerReservationsSQL(ctx context.Context, q ledgerQuerier, workspace, project, state string, limit int, before time.Time, beforeID string) ([]LedgerReservation, error) {
	items := []LedgerReservation{}
	if limit < 1 || limit > 101 {
		return nil, failure(400, "INVALID_LIMIT", "冻结读取数量超出范围")
	}
	rows, err := q.Query(ctx, `SELECT id,quote_id,price_version,state,reserved_micros,charged_micros,released_micros,refunded_micros,version,review_reason,project_id,resource_type,resource_id,model_id,created_at,updated_at FROM studio_ledger_reservations WHERE workspace_id=$1 AND ($2='' OR project_id=$2) AND ($3='' OR state=$3) AND ($4::timestamptz='0001-01-01T00:00:00Z' OR (created_at,id)<($4,$5)) ORDER BY created_at DESC,id DESC LIMIT $6`, workspace, project, state, before, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v := LedgerReservation{BillingSummary: BillingSummary{Unit: LedgerUnit, Scale: LedgerScale}}
		if err = rows.Scan(&v.ReservationID, &v.QuoteID, &v.PriceVersion, &v.State, &v.ReservedMicros, &v.ChargedMicros, &v.ReleasedMicros, &v.RefundedMicros, &v.Version, &v.ReviewReason, &v.ProjectID, &v.ResourceType, &v.ResourceID, &v.ModelID, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
