package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const reconciliationSchemaVersion = maintenanceSchemaVersion

type OrderReconciliationReport struct {
	Format               string         `json:"format"`
	Coverage             string         `json:"coverage"`
	Authenticated        bool           `json:"source_authenticated"`
	SchemaVersion        int            `json:"schema_version"`
	Rows                 int            `json:"input_rows"`
	Events               int            `json:"unique_events"`
	DuplicateRows        int            `json:"duplicate_event_rows"`
	Referenced           int            `json:"referenced_orders"`
	Matched              int            `json:"matched_clue_orders"`
	MatchedPaymentOnly   int            `json:"matched_payment_only_clue_orders"`
	MatchedFullRefund    int            `json:"matched_full_refund_clue_orders"`
	MatchedPartialRefund int            `json:"matched_partial_refund_clue_orders"`
	Missing              int            `json:"missing_evidence_orders"`
	Contradictory        int            `json:"contradictory_evidence_orders"`
	Findings             map[string]int `json:"affected_orders_by_finding"`
	InputSHA             string         `json:"input_sha256"`
	SnapshotSHA          string         `json:"scoped_order_snapshot_sha256"`
	ReportSHA            string         `json:"report_sha256,omitempty"`
}

func reconciliationDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func jsonReconciliationDigest(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", errors.New("unable to summarize reconciliation")
	}
	return reconciliationDigest(b), nil
}

const (
	observation = iota
	missingEvidence
	contradiction
)

// Finding counts count affected orders, so independent evidence is retained
// even when a contradiction outranks a missing item in the overall partition.
var reconciliationFindings = map[string]int{
	"order_not_found_in_scope":         missingEvidence,
	"payment_evidence_missing":         missingEvidence,
	"refund_parent_missing":            missingEvidence,
	"frozen_currency_scale_mismatch":   contradiction,
	"payment_referenced_across_orders": contradiction,
	"payment_identity_conflict":        contradiction,
	"refund_identity_conflict":         contradiction,
	"multiple_payment_ids":             contradiction,
	"order_currency_mismatch":          contradiction,
	"payment_amount_mismatch":          contradiction,
	"event_before_order":               contradiction,
	"event_at_or_after_cancellation":   contradiction,
	"refund_before_payment_time":       contradiction,
	"refund_currency_mismatch":         contradiction,
	"refund_exceeds_payment":           contradiction,
	"repeated_payment_event_ids":       observation,
	"repeated_refund_event_ids":        observation,
	"refund_listed_before_payment":     observation,
	"partial_refund_in_material":       observation,
	"full_refund_in_material":          observation,
	"cancelled_order":                  observation,
}

type reconciliationMarks map[string]map[string]bool

func (m reconciliationMarks) add(id, k string) {
	if m[id] == nil {
		m[id] = map[string]bool{}
	}
	m[id][k] = true
}

type economicGroup struct {
	first         evidenceEvent
	index         int
	orders        map[string]bool
	currencies    map[string]bool
	earliest      time.Time
	largestMinor  int64
	smallestMinor int64
	mixedPayments bool
	count         int
	conflict      bool
}

func addEconomic(groups map[string]*economicGroup, id string, e evidenceEvent, index int) {
	amount, _ := strconv.ParseInt(e.Minor, 10, 64) // input amounts are canonical and bounded
	g := groups[id]
	if g == nil {
		g = &economicGroup{first: e, index: index, orders: map[string]bool{}, currencies: map[string]bool{}, earliest: e.At, smallestMinor: amount}
		groups[id] = g
	}
	g.count++
	g.orders[e.Order] = true
	g.currencies[e.Currency] = true
	if e.At.Before(g.earliest) {
		g.earliest = e.At
	}
	g.smallestMinor = min(g.smallestMinor, amount)
	if e.Payment != g.first.Payment {
		g.mixedPayments = true
	}
	g.largestMinor = max(g.largestMinor, amount)
	// Event and economic-operation identities belong to different namespaces.
	a := g.first
	a.ID = e.ID
	if a != e {
		g.conflict = true
	}
}

func markGroup(m reconciliationMarks, g *economicGroup, key string) {
	for id := range g.orders {
		m.add(id, key)
	}
}

type evidenceIndex struct {
	payments, refunds map[string]*economicGroup
	allPaymentOrders  map[string]map[string]bool
	orderPayments     map[string]map[string]bool
}

func addLink(link map[string]map[string]bool, a, b string) {
	if link[a] == nil {
		link[a] = map[string]bool{}
	}
	link[a][b] = true
}

func indexEvidence(data orderEvidence, orders map[string]OrderIntent, m reconciliationMarks) evidenceIndex {
	ix := evidenceIndex{
		payments:         make(map[string]*economicGroup),
		refunds:          make(map[string]*economicGroup),
		allPaymentOrders: make(map[string]map[string]bool),
		orderPayments:    make(map[string]map[string]bool),
	}
	for pos, e := range data.Events {
		if m[e.Order] == nil {
			m[e.Order] = map[string]bool{}
		}
		addLink(ix.allPaymentOrders, e.Payment, e.Order)
		if e.Kind == "payment" {
			addEconomic(ix.payments, e.Payment, e, pos)
			addLink(ix.orderPayments, e.Order, e.Payment)
		} else {
			addEconomic(ix.refunds, e.Refund, e, pos)
		}
		o, ok := orders[e.Order]
		if !ok {
			m.add(e.Order, "order_not_found_in_scope")
			continue
		}
		if scale, known := orderCurrencyExponents[o.Product.Currency]; !known || scale != o.Product.CurrencyExponent {
			m.add(e.Order, "frozen_currency_scale_mismatch")
		}
		if o.State == "CANCELLED" {
			m.add(e.Order, "cancelled_order")
		}
		if e.Currency != o.Product.Currency {
			m.add(e.Order, "order_currency_mismatch")
		}
		if e.Kind == "payment" && e.Minor != o.Product.AmountMinor {
			m.add(e.Order, "payment_amount_mismatch")
		}
		if e.At.Before(o.CreatedAt) {
			m.add(e.Order, "event_before_order")
		}
		if o.CancelledAt != nil && !e.At.Before(*o.CancelledAt) {
			m.add(e.Order, "event_at_or_after_cancellation")
		}
	}
	for _, ids := range ix.allPaymentOrders {
		if len(ids) > 1 {
			for id := range ids {
				m.add(id, "payment_referenced_across_orders")
			}
		}
	}
	for _, p := range ix.payments {
		if p.count > 1 {
			markGroup(m, p, "repeated_payment_event_ids")
		}
		if p.conflict {
			markGroup(m, p, "payment_identity_conflict")
		}
	}
	for id := range m {
		switch n := len(ix.orderPayments[id]); {
		case n == 0:
			m.add(id, "payment_evidence_missing")
		case n > 1:
			m.add(id, "multiple_payment_ids")
		}
	}
	return ix
}

func diagnoseRefunds(events []evidenceEvent, ix evidenceIndex, m reconciliationMarks) {
	// Test each observation, including every side of a conflicting refund.
	// A contradiction in one field does not cancel evidence in other fields.
	ambiguous := map[string]bool{}
	for pos, r := range events {
		if r.Kind != "refund" {
			continue
		}
		p := ix.payments[r.Payment]
		if ix.refunds[r.Refund].conflict {
			ambiguous[r.Payment] = true
		}
		if p == nil {
			m.add(r.Order, "refund_parent_missing")
			continue
		}
		if pos < p.index {
			m.add(r.Order, "refund_listed_before_payment")
		}
		// Independent contradictions can still be proved when the original
		// transaction was attributed to several orders or effective times.
		if !p.currencies[r.Currency] {
			m.add(r.Order, "refund_currency_mismatch")
			ambiguous[r.Payment] = true
		}
		if r.At.Before(p.earliest) {
			m.add(r.Order, "refund_before_payment_time")
		}
	}
	type refundScope struct{ payment, order string }
	lowerBounds := map[refundScope]int64{}
	for _, r := range ix.refunds {
		if r.count > 1 {
			markGroup(m, r, "repeated_refund_event_ids")
		}
		if r.conflict {
			markGroup(m, r, "refund_identity_conflict")
		}
		// Only compare values with the same payment, order and unit.
		if r.mixedPayments || len(r.orders) != 1 || len(r.currencies) != 1 {
			continue
		}
		pay := ix.payments[r.first.Payment]
		if pay == nil || !pay.orders[r.first.Order] || len(pay.currencies) != 1 || !pay.currencies[r.first.Currency] {
			continue
		}
		key := refundScope{payment: r.first.Payment, order: r.first.Order}
		lowerBounds[key] += r.smallestMinor
	}
	for key, total := range lowerBounds {
		id := key.payment
		p := ix.payments[id]
		limit := p.largestMinor
		switch {
		case total > limit:
			m.add(key.order, "refund_exceeds_payment")
		case ambiguous[id] || p.conflict || len(ix.allPaymentOrders[id]) != 1: // uncertain attribution or amounts preclude a matching sum
		case total == limit:
			markGroup(m, p, "full_refund_in_material")
		default:
			markGroup(m, p, "partial_refund_in_material")
		}
	}
}

func summarizeOrderEvidence(data orderEvidence, orders map[string]OrderIntent) (OrderReconciliationReport, error) {
	m := reconciliationMarks{}
	ix := indexEvidence(data, orders, m)
	diagnoseRefunds(data.Events, ix, m)
	r := OrderReconciliationReport{
		Format: "studio.order-reconciliation.v1", Coverage: "only_referenced_orders_unverified_material",
		SchemaVersion: reconciliationSchemaVersion, InputSHA: data.InputDigest,
		Rows: data.Rows, Events: len(data.Events), DuplicateRows: data.Rows - len(data.Events),
		Referenced: len(m), Findings: make(map[string]int, len(reconciliationFindings)),
	}
	for k := range reconciliationFindings {
		r.Findings[k] = 0
	}
	for _, labels := range m {
		severity := observation
		for label := range labels {
			r.Findings[label]++
			severity = max(severity, reconciliationFindings[label])
		}
		switch severity {
		case contradiction:
			r.Contradictory++
		case missingEvidence:
			r.Missing++
		default:
			r.Matched++
			switch {
			case labels["full_refund_in_material"]:
				r.MatchedFullRefund++
			case labels["partial_refund_in_material"]:
				r.MatchedPartialRefund++
			default:
				r.MatchedPaymentOnly++
			}
		}
	}
	// Freeze only the referenced current order snapshots. Sorting and UTC make
	// the digest independent of row iteration order and process time zone.
	projection := make([]OrderIntent, 0, len(orders))
	for id := range m {
		if o, ok := orders[id]; ok {
			o.CreatedAt = o.CreatedAt.UTC()
			if o.CancelledAt != nil {
				at := o.CancelledAt.UTC()
				o.CancelledAt = &at
			}
			projection = append(projection, o)
		}
	}
	sort.Slice(projection, func(i, j int) bool { return projection[i].ID < projection[j].ID })
	var err error
	r.SnapshotSHA, err = jsonReconciliationDigest(projection)
	if err != nil {
		return OrderReconciliationReport{}, err
	}
	r.ReportSHA, err = jsonReconciliationDigest(r)
	return r, err
}

// ReconcileOrderEvidence opens an independent read-only transaction and never
// invokes startup, migrations, ledger administration or a merchant service.
func ReconcileOrderEvidence(ctx context.Context, c OrderReconciliationConfig) (OrderReconciliationReport, error) {
	var zero OrderReconciliationReport
	scope, err := parseEvidenceScope(c)
	if err != nil {
		return zero, err
	}
	blob, err := readStatementFile(c.Statement)
	if err != nil {
		return zero, err
	}
	data, err := parseOrderEvidence(blob, scope)
	if err != nil {
		return zero, err
	}
	if c.DatabaseURL == "" {
		return zero, errors.New("DATABASE_URL is required for the scoped read-only comparison")
	}
	cfg, err := pgx.ParseConfig(c.DatabaseURL)
	if err != nil {
		return zero, errors.New("invalid database configuration")
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["statement_timeout"] = "5000"
	cfg.RuntimeParams["lock_timeout"] = "1000"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return zero, errors.New("reconciliation database unavailable")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = conn.Close(cleanup)
	}()
	orders := map[string]OrderIntent{}
	err = pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var compatible, workspace bool
		err := tx.QueryRow(ctx, `SELECT count(*)=$1::int AND min(version)=1 AND max(version)=$1::int FROM studio_schema_versions`, reconciliationSchemaVersion).Scan(&compatible)
		if err != nil || !compatible {
			return fmt.Errorf("reconciliation requires exactly schema 001..%03d", reconciliationSchemaVersion)
		}
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_workspaces WHERE id=$1)", scope.Workspace).Scan(&workspace); err != nil || !workspace {
			return errors.New("reconciliation workspace is unavailable")
		}
		wanted := map[string]bool{}
		for _, e := range data.Events {
			wanted[e.Order] = true
		}
		ids := make([]string, 0, len(wanted))
		for id := range wanted {
			ids = append(ids, id)
		}
		rows, err := tx.Query(ctx, "SELECT "+orderIntentFields+" FROM studio_order_intents WHERE workspace_id=$1 AND id=ANY($2::text[])", scope.Workspace, ids)
		if err != nil {
			return errors.New("unable to read scoped frozen order terms")
		}
		defer rows.Close()
		for rows.Next() {
			o, e := scanOrderIntent(rows)
			if e != nil {
				return errors.New("unable to read frozen order terms")
			}
			orders[o.ID] = o
		}
		if rows.Err() != nil {
			return errors.New("scoped order comparison did not finish")
		}
		return nil
	})
	if err != nil {
		return zero, fmt.Errorf("read-only snapshot failed; check database format %03d, workspace and SELECT permission", reconciliationSchemaVersion)
	}
	return summarizeOrderEvidence(data, orders)
}
