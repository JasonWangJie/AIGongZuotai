package studio

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
)

const LedgerUnit = "CREDIT"
const LedgerScale = 6
const MaxLedgerMicros int64 = 9_000_000_000_000_000

type LedgerPriceSnapshot struct {
	ProviderMode           string `json:"provider_mode"`
	Basis                  string `json:"basis"`
	PolicyVersion          string `json:"policy_version"`
	InputPerMillionMicros  int64  `json:"input_per_million_micros,string"`
	OutputPerMillionMicros int64  `json:"output_per_million_micros,string"`
	ArtifactMicros         int64  `json:"artifact_micros,string"`
	MaxInputTokens         int64  `json:"max_input_tokens"`
	MaxOutputTokens        int64  `json:"max_output_tokens"`
	RequestedUnits         int    `json:"requested_units"`
}
type ledgerQuoteRow struct {
	ID, WorkspaceID, ProjectID, ResourceType, ConversationID, ModelID, ModelVersion, PriceVersion string
	RequestHash                                                                                   [32]byte
	Price                                                                                         LedgerPriceSnapshot
	ReserveMicros                                                                                 int64
	ExpiresAt, CreatedAt                                                                          time.Time
	ReservationID                                                                                 string
}
type BillingSummary struct {
	QuoteID        string `json:"quote_id"`
	ReservationID  string `json:"reservation_id"`
	PriceVersion   string `json:"price_version"`
	State          string `json:"state"`
	ReservedMicros int64  `json:"reserved_micros,string"`
	ChargedMicros  int64  `json:"charged_micros,string"`
	ReleasedMicros int64  `json:"released_micros,string"`
	RefundedMicros int64  `json:"refunded_micros,string"`
	Unit           string `json:"unit"`
	Scale          int    `json:"scale"`
	Version        uint64 `json:"version"`
	ReviewReason   string `json:"review_reason,omitempty"`
}
type LedgerWallet struct {
	WorkspaceID     string     `json:"workspace_id"`
	Unit            string     `json:"unit"`
	Scale           int        `json:"scale"`
	AvailableMicros int64      `json:"available_micros,string"`
	ReservedMicros  int64      `json:"reserved_micros,string"`
	ReviewMicros    int64      `json:"review_micros,string"`
	ChargedMicros   int64      `json:"charged_micros,string"`
	RefundedMicros  int64      `json:"refunded_micros,string"`
	Version         uint64     `json:"version"`
	UpdatedAt       *time.Time `json:"updated_at"`
}
type LedgerEntry struct {
	ID                   string    `json:"id"`
	Kind                 string    `json:"kind"`
	ProjectID            string    `json:"project_id,omitempty"`
	ResourceType         string    `json:"resource_type,omitempty"`
	ResourceID           string    `json:"resource_id,omitempty"`
	ReservationID        string    `json:"reservation_id,omitempty"`
	QuoteID              string    `json:"quote_id,omitempty"`
	ReversalOf           string    `json:"reversal_of,omitempty"`
	AmountMicros         int64     `json:"amount_micros,string"`
	AvailableDeltaMicros int64     `json:"available_delta_micros,string"`
	ReservedDeltaMicros  int64     `json:"reserved_delta_micros,string"`
	Unit                 string    `json:"unit"`
	Scale                int       `json:"scale"`
	CreatedAt            time.Time `json:"created_at"`
}
type LedgerReservation struct {
	BillingSummary
	ProjectID    string    `json:"project_id"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	ModelID      string    `json:"model_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type ledgerReservationRow struct {
	LedgerReservation
	WorkspaceID     string
	Price           LedgerPriceSnapshot
	Started         bool
	Usage           *ChatUsage
	ChargeJournalID string
}
type LedgerReservationRequest struct {
	QuoteID, WorkspaceID, ProjectID, ResourceType, ResourceID, ConversationID, ModelID, ModelVersion string
	ProviderMode                                                                                     string
	RequestHash                                                                                      [32]byte
}
type LedgerCreditRequest struct {
	WorkspaceID, OperationKey, Reason, EvidenceRef string
	AmountMicros                                   int64
}
type LedgerRefundRequest struct{ ReservationID, OperationKey, Reason, EvidenceRef string }
type LedgerResolveRequest struct {
	ReservationID, OperationKey, Reason, EvidenceRef, Decision string
	Usage                                                      *ChatUsage
	DeliveredUnits                                             int
}
type LedgerMutation struct {
	OperationID string             `json:"operation_id"`
	Wallet      LedgerWallet       `json:"wallet"`
	Reservation *LedgerReservation `json:"reservation,omitempty"`
	Replayed    bool               `json:"replayed"`
}
type LedgerReconciliation struct {
	WorkspaceID          string `json:"workspace_id"`
	Balanced             bool   `json:"balanced"`
	AccountBalancesMatch bool   `json:"account_balances_match"`
	HoldsMatch           bool   `json:"holds_match"`
	TotalsMatch          bool   `json:"totals_match"`
	JournalCount         int64  `json:"journal_count"`
	EntryCount           int64  `json:"entry_count"`
}
type ledgerQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// One ceiling after adding both exact products. Never uses a float, character
// count, inferred provider price, or individually rounded token components.
func ledgerChatAmount(p LedgerPriceSnapshot, input, output int64) (int64, error) {
	if input < 0 || output < 0 || input > 1_000_000_000 || output > 1_000_000_000 || p.InputPerMillionMicros < 0 || p.OutputPerMillionMicros < 0 || p.InputPerMillionMicros > MaxLedgerMicros || p.OutputPerMillionMicros > MaxLedgerMicros {
		return 0, failure(422, "INVALID_LEDGER_AMOUNT", "计量或整数费率超出允许范围")
	}
	total := new(big.Int).Mul(big.NewInt(input), big.NewInt(p.InputPerMillionMicros))
	total.Add(total, new(big.Int).Mul(big.NewInt(output), big.NewInt(p.OutputPerMillionMicros)))
	total.Add(total, big.NewInt(999999))
	total.Quo(total, big.NewInt(1000000))
	if !total.IsInt64() || total.Int64() > MaxLedgerMicros {
		return 0, failure(422, "LEDGER_AMOUNT_OVERFLOW", "额度超过账本整数上限")
	}
	return total.Int64(), nil
}
func ledgerRunRequestHash(in RunInput) [32]byte   { in.QuoteID = ""; return payloadHash(in) }
func ledgerTaskRequestHash(in TaskInput) [32]byte { in.QuoteID = ""; return payloadHash(in) }
func ledgerConversationRequestHash(id string, in ConversationRunInput) [32]byte {
	in.QuoteID = ""
	return payloadHash(struct {
		ConversationID string
		Input          ConversationRunInput
	}{id, in})
}
func copyBilling(v *BillingSummary) *BillingSummary {
	if v == nil {
		return nil
	}
	next := *v
	return &next
}

func readLedgerQuoteSQL(ctx context.Context, q ledgerQuerier, id string, lock bool) (ledgerQuoteRow, error) {
	var v ledgerQuoteRow
	var hash, price []byte
	sql := `SELECT id,workspace_id,project_id,resource_type,conversation_id,model_id,model_version,price_version,request_hash,price,reserve_micros,expires_at,created_at,COALESCE(reservation_id,'') FROM studio_ledger_quotes WHERE id=$1`
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, id).Scan(&v.ID, &v.WorkspaceID, &v.ProjectID, &v.ResourceType, &v.ConversationID, &v.ModelID, &v.ModelVersion, &v.PriceVersion, &hash, &price, &v.ReserveMicros, &v.ExpiresAt, &v.CreatedAt, &v.ReservationID)
	if err != nil {
		return v, err
	}
	if len(hash) != 32 {
		return v, errors.New("invalid persisted quote hash")
	}
	copy(v.RequestHash[:], hash)
	err = json.Unmarshal(price, &v.Price)
	return v, err
}
func readLedgerWalletSQL(ctx context.Context, q ledgerQuerier, workspace string) (LedgerWallet, error) {
	v := LedgerWallet{WorkspaceID: workspace, Unit: LedgerUnit, Scale: LedgerScale}
	err := q.QueryRow(ctx, `SELECT a.balance_micros,h.balance_micros,w.charged_micros,w.refunded_micros,w.version,w.updated_at,COALESCE((SELECT sum(reserved_micros-charged_micros-released_micros) FROM studio_ledger_reservations WHERE workspace_id=w.workspace_id AND state='REVIEW'),0) FROM studio_ledger_wallets w JOIN studio_ledger_accounts a ON a.id='available:'||w.workspace_id JOIN studio_ledger_accounts h ON h.id='reserved:'||w.workspace_id WHERE w.workspace_id=$1`, workspace).Scan(&v.AvailableMicros, &v.ReservedMicros, &v.ChargedMicros, &v.RefundedMicros, &v.Version, &v.UpdatedAt, &v.ReviewMicros)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	return v, err
}
