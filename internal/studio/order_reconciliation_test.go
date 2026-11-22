package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const reconFrom = "2026-11-01T00:00:00Z"

func reconCfg() OrderReconciliationConfig {
	return OrderReconciliationConfig{Merchant: "synthetic-shop", Environment: "test", WorkspaceID: "ws_" + strings.Repeat("a", 32), From: reconFrom, To: "2026-11-02T00:00:00Z"}
}
func reconTime(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func reconPayment(order, event, payment string) evidenceEvent {
	return evidenceEvent{ID: event, Kind: "payment", Payment: payment, Order: order, Currency: "CNY", Minor: "1200", At: reconTime("2026-11-01T09:00:00Z")}
}
func reconRefund(p evidenceEvent, id, ref, amt string) evidenceEvent {
	p.ID = id
	p.Kind = "refund"
	p.Refund = ref
	p.Minor = amt
	p.At = p.At.Add(time.Hour)
	return p
}
func evidenceJSON(t *testing.T, c OrderReconciliationConfig, entries []evidenceEvent) []byte {
	t.Helper()
	scope, e := parseEvidenceScope(c)
	if e != nil {
		t.Fatal(e)
	}
	data, e := json.Marshal(struct {
		evidenceScope
		Entries []evidenceEvent `json:"entries"`
	}{scope, entries})
	if e != nil {
		t.Fatal(e)
	}
	return data
}
func TestOrderReconciliationMaterialAndScope(t *testing.T) {
	c := reconCfg()
	s, _ := parseEvidenceScope(c)
	o := "ord_" + strings.Repeat("b", 32)
	p := reconPayment(o, "event-1", "pay-1")
	b := evidenceJSON(t, c, []evidenceEvent{p, p})
	good, e := parseOrderEvidence(b, s)
	if e != nil || good.Rows != 2 || len(good.Events) != 1 || good.InputDigest != reconciliationDigest(b) {
		t.Fatal("identical event replay", e)
	}
	// The file and operator inputs must independently identify the same scope.
	for _, mut := range []func(*evidenceScope){func(v *evidenceScope) { v.Merchant = "different" }, func(v *evidenceScope) { v.Environment = "live" }, func(v *evidenceScope) { v.Workspace = "ws_" + strings.Repeat("0", 32) }, func(v *evidenceScope) { v.To = v.To.Add(time.Nanosecond) }} {
		other := s
		mut(&other)
		if _, e = parseOrderEvidence(b, other); e == nil {
			t.Fatal("mismatched scope accepted")
		}
	}
	for _, pair := range [][2]string{{`"kind":"payment"`, `"kind":"payment","kind":"payment"`}, {`"amount_minor":"1200"`, `"amount_minor":1200`}, {`"amount_minor":"1200"`, `"Amount_minor":"1200"`}, {`"kind":"payment"`, `"kind":"payment","refund_id":"not-allowed"`}, {`"kind":"payment"`, `"kind":"payment","payer_account":"private"`}, {`"currency":"CNY"`, `"currency":"ZZZ"`}, {p.Order, "foreign-order"}, {p.Payment, "not an opaque token"}, {p.At.Format(time.RFC3339), c.To}, {`"amount_minor":"1200"`, `"amount_minor":"00012"`}, {`"amount_minor":"1200"`, `"amount_minor":"1000000000001"`}} {
		bad := bytes.ReplaceAll(b, []byte(pair[0]), []byte(pair[1]))
		if bytes.Equal(bad, b) {
			t.Fatal("unmatched fixture")
		}
		if _, e = parseOrderEvidence(bad, s); e == nil {
			t.Fatal("invalid field passed")
		}
	}
	q := p
	q.Minor = "1201"
	if _, e = parseOrderEvidence(evidenceJSON(t, c, []evidenceEvent{p, q}), s); e == nil {
		t.Fatal("conflicting event identity accepted")
	}
	for _, raw := range [][]byte{append(append([]byte{}, b...), b...), {}, bytes.Repeat([]byte(" "), maxEvidenceBytes+1), evidenceJSON(t, c, []evidenceEvent{}), evidenceJSON(t, c, make([]evidenceEvent, maxEvidenceRows+1))} {
		if _, e = parseOrderEvidence(raw, s); e == nil {
			t.Fatal("unbounded or invalid document accepted")
		}
	}
	for _, change := range []func(*OrderReconciliationConfig){func(c *OrderReconciliationConfig) { c.From = c.To }, func(c *OrderReconciliationConfig) { c.To = "2027-01-01T00:00:00Z" }, func(c *OrderReconciliationConfig) { c.Merchant = "person@example.invalid" }, func(c *OrderReconciliationConfig) { c.Environment = "stage" }} {
		bad := c
		change(&bad)
		if _, e = parseEvidenceScope(bad); e == nil {
			t.Fatal("invalid operator scope accepted")
		}
	}
}
func analyzed(t *testing.T, records []evidenceEvent, orders ...OrderIntent) OrderReconciliationReport {
	t.Helper()
	c := reconCfg()
	scope, _ := parseEvidenceScope(c)
	input, e := parseOrderEvidence(evidenceJSON(t, c, records), scope)
	if e != nil {
		t.Fatal(e)
	}
	items := map[string]OrderIntent{}
	for _, o := range orders {
		items[o.ID] = o
	}
	r, e := summarizeOrderEvidence(input, items)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestOrderReconciliationClassificationsAndEvidenceBoundaries(t *testing.T) {
	id := "ord_" + strings.Repeat("a", 32)
	second := "ord_" + strings.Repeat("b", 32)
	p := reconPayment(id, "evt-1", "payment-1")
	refund := reconRefund(p, "evt-2", "refund-1", "1200")
	o := OrderIntent{ID: id, Product: OrderIntentProduct{Currency: "CNY", CurrencyExponent: 2, AmountMinor: "1200", CreditMicros: "10000000"}, State: "PENDING", Version: 1, CreatedAt: reconTime(reconFrom)}
	clone := func(ev evidenceEvent, key string, edit func(*evidenceEvent)) evidenceEvent {
		ev.ID = key
		edit(&ev)
		return ev
	}
	type scenario struct {
		name                            string
		rows                            []evidenceEvent
		labels                          []string
		matched, missing, contradictory int
	}
	cases := []scenario{
		{"matching only", []evidenceEvent{p}, nil, 1, 0, 0},
		{"same economic payment and refund, observed in different callback order", []evidenceEvent{refund, p, p, clone(p, "callback-2", func(*evidenceEvent) {}), clone(refund, "refund-callback-2", func(*evidenceEvent) {})}, []string{"refund_listed_before_payment", "repeated_payment_event_ids", "repeated_refund_event_ids", "full_refund_in_material"}, 1, 0, 0},
		{"partial", []evidenceEvent{p, clone(refund, "part", func(e *evidenceEvent) { e.Minor = "50" })}, []string{"partial_refund_in_material"}, 1, 0, 0},
		{"over across two refund IDs", []evidenceEvent{p, refund, clone(refund, "extra", func(e *evidenceEvent) { e.Refund = "refund-2"; e.Minor = "1" })}, []string{"refund_exceeds_payment"}, 0, 0, 1},
		{"refund timestamp early", []evidenceEvent{p, clone(refund, "early", func(e *evidenceEvent) { e.At = p.At.Add(-time.Minute) })}, []string{"refund_before_payment_time"}, 0, 0, 1},
		{"unseen order", []evidenceEvent{clone(p, "outside", func(e *evidenceEvent) { e.Order = second })}, []string{"order_not_found_in_scope"}, 0, 1, 0},
		{"refund missing payment", []evidenceEvent{refund}, []string{"refund_parent_missing", "payment_evidence_missing"}, 0, 1, 0},
		{"two different transactions", []evidenceEvent{p, clone(p, "evt-other-pay", func(e *evidenceEvent) { e.Payment = "payment-2" })}, []string{"multiple_payment_ids"}, 0, 0, 1},
		{"wrong terms", []evidenceEvent{clone(p, "wrong", func(e *evidenceEvent) { e.Currency = "USD"; e.Minor = "1199" }), refund}, []string{"order_currency_mismatch", "payment_amount_mismatch", "refund_currency_mismatch"}, 0, 0, 1},
		{"conflicting transaction and refund identities", []evidenceEvent{p, clone(p, "evt-foreign", func(e *evidenceEvent) { e.Order = second }), refund, clone(refund, "evt-refund-conflict", func(e *evidenceEvent) { e.Minor = "400" })}, []string{"payment_identity_conflict", "payment_referenced_across_orders", "refund_identity_conflict", "order_not_found_in_scope"}, 0, 0, 2},
		{"over and early refund independently proved despite ambiguous order", []evidenceEvent{
			p, clone(p, "other-order", func(e *evidenceEvent) { e.Order = second }),
			clone(refund, "early-and-over", func(e *evidenceEvent) { e.Minor = "1201"; e.At = p.At.Add(-time.Minute) }),
		}, []string{"refund_exceeds_payment", "payment_identity_conflict", "refund_before_payment_time"}, 0, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := analyzed(t, tc.rows, o)
			if r.Matched != tc.matched || r.Missing != tc.missing || r.Contradictory != tc.contradictory || r.Authenticated || r.Referenced != r.Matched+r.Missing+r.Contradictory || r.Matched != r.MatchedFullRefund+r.MatchedPartialRefund+r.MatchedPaymentOnly {
				t.Fatal("partition", r)
			}
			for _, k := range tc.labels {
				if r.Findings[k] == 0 {
					t.Fatalf("missing %s", k)
				}
			}
			again := analyzed(t, tc.rows, o)
			if !reflect.DeepEqual(r, again) {
				t.Fatal("unstable report")
			}
		})
	}
	cancel := p.At.Add(time.Second)
	o.State = "CANCELLED"
	o.Version = 2
	o.CancelledAt = &cancel
	r := analyzed(t, []evidenceEvent{p}, o)
	if r.Matched != 1 || r.Findings["cancelled_order"] != 1 || r.Findings["event_at_or_after_cancellation"] != 0 {
		t.Fatal("earlier transaction was classified as post-cancellation")
	}
	r = analyzed(t, []evidenceEvent{p, refund}, o)
	if r.Contradictory != 1 || r.Findings["event_at_or_after_cancellation"] != 1 {
		t.Fatal("missing later refund after cancellation")
	}
}
func TestOrderReconciliationLocalFileAndSanitizedResult(t *testing.T) {
	b, e := os.ReadFile(filepath.Join("..", "..", "config", "order-reconciliation.example.json"))
	if e != nil {
		t.Fatal(e)
	}
	c := reconCfg()
	scope, _ := parseEvidenceScope(c)
	in, e := parseOrderEvidence(b, scope)
	if e != nil {
		t.Fatal(e)
	}
	r, e := summarizeOrderEvidence(in, map[string]OrderIntent{})
	if e != nil {
		t.Fatal(e)
	}
	out, _ := json.Marshal(r)
	for _, v := range []string{c.Merchant, c.WorkspaceID, in.Events[0].Order, in.Events[0].Payment, in.Events[0].ID, in.Events[1].Refund} {
		if strings.Contains(string(out), v) {
			t.Fatal("raw input identity in output")
		}
	}
	if r.Authenticated || len(r.InputSHA) != 64 || len(r.SnapshotSHA) != 64 || len(r.ReportSHA) != 64 {
		t.Fatal("incomplete unverified evidence report")
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fixture.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	read, e := readStatementFile(path)
	if e != nil || !bytes.Equal(read, b) {
		t.Fatal("local bytes", e)
	}
	for _, path := range []string{tmp, "https://merchant.invalid/private", "//remote/private"} {
		if _, e = readStatementFile(path); e == nil {
			t.Fatal("invalid local file accepted")
		}
	}
	c.Statement = filepath.Join(tmp, "private-alias")
	_, e = ReconcileOrderEvidence(context.Background(), c)
	if e == nil || strings.Contains(e.Error(), "private-alias") {
		t.Fatal("unredacted file error")
	}
}

func TestOrderReconciliationStrictTimeInputs(t *testing.T) {
	c := reconCfg()
	scope, err := parseEvidenceScope(c)
	if err != nil {
		t.Fatal(err)
	}
	order := "ord_" + strings.Repeat("a", 32)
	p := reconPayment(order, "event", "payment")
	raw := string(evidenceJSON(t, c, []evidenceEvent{p, p}))
	at := p.At.Format(time.RFC3339)
	// Each alternative would represent the same instant if an invalid offset or
	// excessive precision was silently normalized by the time package.
	for _, tc := range []struct{ name, from, to, event string }{
		{"tenth fractional digit", "2026-11-01T00:00:00.0000000001Z", "2026-11-02T00:00:00.0000000001Z", "2026-11-01T09:00:00.1234567891Z"},
		{"decimal comma", "2026-11-01T00:00:00,0Z", "2026-11-02T00:00:00,0Z", "2026-11-01T09:00:00,0Z"},
		{"offset hour 24", "2026-11-02T00:00:00+24:00", "2026-11-03T00:00:00+24:00", "2026-11-02T09:00:00+24:00"},
		{"offset minute 60", "2026-11-01T01:00:00+00:60", "2026-11-02T01:00:00+00:60", "2026-11-01T10:00:00+00:60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, end := range []bool{false, true} {
				bad := c
				old, replacement := c.From, tc.from
				if end {
					bad.To = tc.to
					old, replacement = c.To, tc.to
				} else {
					bad.From = tc.from
				}
				if _, err := parseEvidenceScope(bad); err == nil {
					t.Error("invalid CLI boundary accepted")
				}
				if _, err := parseOrderEvidence([]byte(strings.Replace(raw, old, replacement, 1)), scope); err == nil {
					t.Error("invalid file boundary accepted")
				}
			}
			material := strings.Replace(raw, at, tc.event, 1)
			sameID := strings.Replace(material, at, strings.Replace(tc.event, ".1234567891", ".1234567892", 1), 1)
			if _, err := parseOrderEvidence([]byte(sameID), scope); err == nil {
				t.Error("invalid event accepted")
			}
		})
	}
	c.From = "2026-11-01T08:00:00+08:00"
	c.To = "2026-11-01T17:00:00-07:00"
	other, err := parseEvidenceScope(c)
	if err != nil || other != scope {
		t.Fatal("equivalent CLI offsets", err)
	}
	material := strings.Replace(raw, reconFrom, c.From, 1)
	material = strings.Replace(material, "2026-11-02T00:00:00Z", c.To, 1)
	material = strings.Replace(material, at, "2026-11-01T09:00:00.123456789Z", 1)
	material = strings.Replace(material, at, "2026-11-01T17:00:00.123456789+08:00", 1)
	data, err := parseOrderEvidence([]byte(material), scope)
	if err != nil || len(data.Events) != 1 || data.Events[0].At.Nanosecond() != 123456789 {
		t.Fatal("equivalent event replay at nanosecond precision", err)
	}
	material = strings.Replace(material, ".123456789+08", ".123456788+08", 1)
	if _, err := parseOrderEvidence([]byte(material), scope); err == nil {
		t.Error("distinct representable instants collapsed")
	}
}

func TestOrderReconciliationRefundLowerBounds(t *testing.T) {
	order := "ord_" + strings.Repeat("a", 32)
	other := "ord_" + strings.Repeat("b", 32)
	payment := reconPayment(order, "purchase", "payment-1")
	refund := func(event, id, minor string) evidenceEvent { return reconRefund(payment, event, id, minor) }
	variant := func(e evidenceEvent, id string) evidenceEvent { e.ID = id; return e }
	alteredPayment := variant(payment, "payment-alternative")
	alteredPayment.Minor = "1400"
	secondPayment := variant(payment, "second-payment")
	secondPayment.Payment = "payment-2"
	otherOrderPayment := variant(payment, "other-order-payment")
	otherOrderPayment.Order = other
	r1 := refund("refund-event-1", "r1", "1300")
	r2 := refund("refund-event-2", "r1", "1400")
	differentOrder := r2
	differentOrder.Order = other
	differentCurrency := r2
	differentCurrency.Currency = "USD"
	differentPayment := r2
	differentPayment.Payment = secondPayment.Payment
	otherOrderRefund := refund("other-order-refund", "r2", "700")
	otherOrderRefund.Order = other
	for _, tc := range []struct {
		name           string
		rows           []evidenceEvent
		over, conflict int
	}{
		{"all alternatives exceed", []evidenceEvent{payment, r1, r2}, 1, 1},
		{"upper alternative only", []evidenceEvent{payment, refund("low", "r1", "1199"), r2}, 0, 1},
		{"minimum equals payment", []evidenceEvent{payment, refund("low", "r1", "1200"), r2}, 0, 1},
		{"sum minima across refund IDs", []evidenceEvent{payment, refund("lower", "r1", "600"), refund("higher", "r1", "1500"), refund("other-low", "r2", "601"), refund("other-high", "r2", "800")}, 1, 1},
		{"repeated callbacks and duplicate events counted once", []evidenceEvent{payment, refund("first", "r1", "700"), refund("repeat", "r1", "700"), refund("repeat", "r1", "700"), refund("alternative", "r1", "900")}, 0, 1},
		{"conflicting payment upper bound", []evidenceEvent{payment, alteredPayment, r1, r2}, 0, 1},
		{"every possible payment exceeded", []evidenceEvent{payment, alteredPayment, r1, r2, refund("extra", "r2", "101")}, 1, 1},
		{"refund identity changes currency", []evidenceEvent{payment, r1, differentCurrency}, 0, 1},
		{"refund identity changes parent", []evidenceEvent{payment, secondPayment, r1, differentPayment}, 0, 1},
		{"refund identity changes order", []evidenceEvent{payment, otherOrderPayment, r1, differentOrder}, 0, 2},
		{"do not sum refunds across ambiguous orders", []evidenceEvent{payment, otherOrderPayment, refund("first", "r1", "700"), otherOrderRefund}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := analyzed(t, tc.rows)
			if r.Findings["refund_exceeds_payment"] != tc.over || r.Findings["refund_identity_conflict"] != tc.conflict {
				t.Error("unexpected independent findings", r.Findings)
			}
			if r.Findings["partial_refund_in_material"] != 0 || r.Findings["full_refund_in_material"] != 0 {
				t.Error("uncertain refund counted as an exact amount")
			}
		})
	}
}
