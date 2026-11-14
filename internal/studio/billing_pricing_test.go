package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreditPrecisionAndPriceValidation(t *testing.T) {
	for _, s := range []string{"", "01", "-1", "+1", "1.0", "1e6", " 1", "9000000000000001"} {
		if _, err := parseCreditMicros(s, true); err == nil {
			t.Fatalf("invalid amount accepted: %q", s)
		}
	}
	if v, err := parseCreditMicros("9000000000000000", false); err != nil || v != MaxLedgerMicros {
		t.Fatal(v, err)
	}
	if _, err := parseCreditMicros("0", false); err == nil {
		t.Fatal("zero credit accepted")
	}
	p := LedgerPriceSnapshot{InputPerMillionMicros: 1, OutputPerMillionMicros: 1}
	if v, err := ledgerChatAmount(p, 1, 1); err != nil || v != 1 {
		t.Fatal("must ceil once after adding both token legs", v, err)
	}
	p.InputPerMillionMicros = MaxLedgerMicros
	if _, err := ledgerChatAmount(p, 1_000_000_000, 0); err == nil {
		t.Fatal("overflow accepted")
	}
	base := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"4000000"}]}`
	path := filepath.Join(t.TempDir(), "prices.json")
	load := func(s, mode string) (*pricingCatalog, error) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
		return loadPricingCatalog(Config{BillingMode: "credits", BillingPriceFile: path, Mode: mode})
	}
	price, err := load(base, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if price.Version == "" || len(price.Rates) != 1 {
		t.Fatal("catalog not versioned")
	}
	for _, invalid := range []string{
		strings.Replace(base, `"scale":6`, `"scale":6,"scale":6`, 1),
		strings.Replace(base, `"scale":6`, `"scale":2`, 1),
		strings.Replace(base, `"unit":"CREDIT"`, `"unit":"CNY"`, 1),
		strings.Replace(base, `"1000000"`, `1000000`, 1),
		strings.Replace(base, `"1000000"`, `"-1000000"`, 1),
		strings.Replace(base, `"1000000"`, `"1e6"`, 1),
		strings.Replace(base, `"schema_version":1`, `"schema_version":1,"extra":true`, 1),
		base + `{}`,
	} {
		if _, err = load(invalid, "demo"); err == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
	if _, err = load(base, "live"); err == nil {
		t.Fatal("demo price accepted for live")
	}
	model := Model{ID: "studio-chat", Kind: "chat", Capabilities: ModelCapabilities{ContextWindowTokens: 32768, MaxOutputTokens: 4096}}
	_, hold, err := price.chatPrice(model)
	if err != nil || hold != 45056 {
		t.Fatal(hold, err)
	}
	model.ID = "unpriced"
	if _, _, err = price.chatPrice(model); err == nil {
		t.Fatal("unpriced model accepted")
	}
}

func TestMediaPriceUsesExactOperationPresetAndCount(t *testing.T) {
	p := &pricingCatalog{Rates: map[string]LedgerPriceSnapshot{
		pricingKey("image", "image", "generate", "square"): {ArtifactMicros: 200000, Basis: "delivered_artifact"},
		pricingKey("image", "image", "edit", "square"):     {ArtifactMicros: 300000, Basis: "delivered_artifact"},
	}}
	m := Model{ID: "image", Kind: "image", Capabilities: ModelCapabilities{MaxCount: 4, ParameterPresets: []ParameterPreset{{ID: "square", AspectRatio: "1:1", Size: "1024x1024"}}}}
	parameters := TaskParameters{Count: 3, AspectRatio: "1:1", Size: "1024x1024"}
	for op, want := range map[string]int64{"generate": 600000, "edit": 900000} {
		snap, got, err := p.taskPrice(m, op, parameters)
		if err != nil || got != want || snap.RequestedUnits != 3 {
			t.Fatal(op, got, err)
		}
	}
	parameters.Size = "4096x4096"
	if _, _, err := p.taskPrice(m, "generate", parameters); err == nil {
		t.Fatal("unpriced preset accepted")
	}
	parameters.Size = "1024x1024"
	parameters.Count = 5
	if _, _, err := p.taskPrice(m, "generate", parameters); err == nil {
		t.Fatal("excess count accepted")
	}
}
