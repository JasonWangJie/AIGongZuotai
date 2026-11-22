package studio

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrderCatalogRequiresExplicitPersistentAccountConfiguration(t *testing.T) {
	example := filepath.Join("..", "..", "config", "order-catalog.example.json")
	off, err := loadOrderCatalog("", "", "")
	if err != nil || off != nil {
		t.Fatal("empty path must leave the catalog disabled", err)
	}
	for _, c := range []struct{ path, mode, db string }{
		{example, "required", ""}, {example, "disabled", "unused"},
		{"https://example.invalid/catalog", "required", "unused"},
		{t.TempDir(), "required", "unused"},
	} {
		if _, err = loadOrderCatalog(c.path, c.mode, c.db); err == nil {
			t.Fatal("invalid order catalog configuration was accepted")
		}
	}
	large := filepath.Join(t.TempDir(), "large.json")
	if err = os.WriteFile(large, bytes.Repeat([]byte(" "), (64<<10)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadOrderCatalog(large, "required", "unused"); err == nil {
		t.Fatal("oversized local file accepted")
	}
	c, err := loadOrderCatalog(example, "required", "unused")
	if err != nil || len(c.products) != 1 {
		t.Fatal("example order catalog could not be read", err)
	}
	item := c.products[0]
	app := &App{orderCatalog: c}
	got, err := app.lookupOrderIntentProduct(item.ID, item.CatalogDigest, item.ProductDigest)
	if err != nil || got != item || got.AmountMinor != "1200" || got.CreditMicros != "10000000" ||
		got.CurrencyExponent != 2 || got.CatalogVersion != "example-v1" || len(got.CatalogDigest) != 64 || len(got.ProductDigest) != 64 {
		t.Fatal("example did not resolve to its frozen units", err)
	}
}

func TestOrderCatalogValidatesPricesAndFreezesCanonicalDigests(t *testing.T) {
	item := `{"id":"starter","name":"创作包","currency":"JPY","currency_exponent":0,"amount_minor":"100","credit_micros":"1234567"}`
	other := strings.NewReplacer("starter", "special", "JPY", "KWD", "exponent\":0", "exponent\":3").Replace(item)
	catalog := func(products string) string {
		return `{"schema_version":1,"catalog_version":"v1","products":[` + products + `]}`
	}
	original := catalog(item)
	for name, raw := range map[string]string{
		"bad UTF8":           strings.Replace(original, "创作包", string([]byte{0xff}), 1),
		"missing exponent":   strings.Replace(original, `"currency_exponent":0,`, "", 1),
		"nullable exponent":  strings.Replace(original, "exponent\":0", "exponent\":null", 1),
		"wrong yen scale":    strings.Replace(original, "exponent\":0", "exponent\":2", 1),
		"unconfigured unit":  strings.Replace(original, "JPY", "XXX", 1),
		"fractional minor":   strings.Replace(original, `"100"`, `"1.00"`, 1),
		"leading zero":       strings.Replace(original, `"100"`, `"0100"`, 1),
		"zero amount":        strings.Replace(original, `"100"`, `"0"`, 1),
		"large amount":       strings.Replace(original, `"100"`, `"1000000000001"`, 1),
		"numeric credit":     strings.Replace(original, `"1234567"`, `1234567`, 1),
		"large credit":       strings.Replace(original, `"1234567"`, `"1000000000000001"`, 1),
		"duplicate SKU":      catalog(item + "," + item),
		"unknown property":   strings.Replace(original, `"name"`, `"payment_url":"x","name"`, 1),
		"duplicate property": strings.Replace(original, `"id":"starter"`, `"id":"a","id":"starter"`, 1),
		"wrong case":         strings.Replace(original, "catalog_version", "Catalog_Version", 1),
		"following object":   original + "{}",
		"extra products":     catalog(strings.Repeat(item+",", 32) + other),
		"empty name":         strings.Replace(original, "创作包", " ", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOrderCatalog([]byte(raw)); err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
	loose := catalog(other + "," + item)
	first, err := parseOrderCatalog([]byte(loose))
	if err != nil {
		t.Fatal(err)
	}
	var indented bytes.Buffer
	if err = json.Indent(&indented, []byte(catalog(item+","+other)), "", "  "); err != nil {
		t.Fatal(err)
	}
	second, err := parseOrderCatalog(indented.Bytes())
	if err != nil || first.digest != second.digest || first.products[0] != second.products[0] {
		t.Fatal("formatting or SKU order changed the catalog digest", err)
	}
	// Keep the human-readable version identical while changing a monetary term.
	changed, err := parseOrderCatalog([]byte(strings.Replace(loose, `"100"`, `"101"`, 1)))
	if err != nil || changed.digest == first.digest || changed.products[0].ProductDigest == first.products[0].ProductDigest {
		t.Fatal("new commercial terms reused an old digest", err)
	}
	app := &App{orderCatalog: changed}
	for _, stale := range [][2]string{{first.digest, first.products[0].ProductDigest}, {changed.digest, first.products[0].ProductDigest}} {
		if _, e := app.lookupOrderIntentProduct(first.products[0].ID, stale[0], stale[1]); draftErrorCode(e) != "ORDER_CATALOG_CHANGED" {
			t.Fatal("stale product or catalog was accepted", e)
		}
	}
}
