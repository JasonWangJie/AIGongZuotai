package studio

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type OrderIntentProduct struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CatalogVersion   string `json:"catalog_version"`
	CatalogDigest    string `json:"catalog_digest"`
	ProductDigest    string `json:"product_digest"`
	Currency         string `json:"currency"`
	CurrencyExponent int    `json:"currency_exponent"`
	AmountMinor      string `json:"amount_minor"`
	CreditMicros     string `json:"credit_micros"`
}

type orderCatalog struct {
	version, digest string
	products        []OrderIntentProduct
}

type orderCatalogItem struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Currency         string `json:"currency"`
	CurrencyExponent int    `json:"currency_exponent"`
	AmountMinor      string `json:"amount_minor"`
	CreditMicros     string `json:"credit_micros"`
}

var orderCatalogID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
var orderCatalogVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$`)
var orderCurrencyExponents = map[string]int{"CNY": 2, "USD": 2, "EUR": 2, "HKD": 2, "GBP": 2, "JPY": 0, "KWD": 3}

func positiveOrderInteger(s string, max int64) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && n <= max && strconv.FormatInt(n, 10) == s
}

// All fields in these small, versioned objects are required. Checking exact
// keys also prevents Go's case-insensitive struct decoding from aliasing fields.
func orderFields(raw []byte, fields ...string) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != len(fields) {
		return nil, false
	}
	for _, k := range fields {
		v, ok := object[k]
		if !ok || string(v) == "null" {
			return nil, false
		}
	}
	return object, true
}

func loadOrderCatalog(path, authMode, databaseURL string) (*orderCatalog, error) {
	if path == "" {
		return nil, nil
	}
	if authMode != "required" || databaseURL == "" {
		return nil, errors.New("ORDER_CATALOG_FILE requires PostgreSQL and AUTH_MODE=required")
	}
	if strings.Contains(path, "://") || strings.HasPrefix(strings.ReplaceAll(path, "\\", "/"), "//") {
		return nil, errors.New("ORDER_CATALOG_FILE must name a trusted local file")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("ORDER_CATALOG_FILE must be a regular file of at most 64 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("ORDER_CATALOG_FILE cannot be opened")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, errors.New("ORDER_CATALOG_FILE could not be read within 64 KiB")
	}
	return parseOrderCatalog(raw)
}

func parseOrderCatalog(raw []byte) (*orderCatalog, error) {
	if !utf8.Valid(raw) || validateCatalogJSONKeys(raw) != nil {
		return nil, errors.New("invalid order catalog JSON or duplicate keys")
	}
	root, ok := orderFields(raw, "schema_version", "catalog_version", "products")
	var version int
	var label string
	var encoded []json.RawMessage
	if !ok || json.Unmarshal(root["schema_version"], &version) != nil || version != 1 ||
		json.Unmarshal(root["catalog_version"], &label) != nil || !orderCatalogVersion.MatchString(label) ||
		json.Unmarshal(root["products"], &encoded) != nil || len(encoded) < 1 || len(encoded) > 32 {
		return nil, errors.New("order catalog requires schema_version 1, a version label and 1 to 32 products")
	}
	items := make([]orderCatalogItem, 0, len(encoded))
	seen := make(map[string]bool)
	for _, obj := range encoded {
		var item orderCatalogItem
		if _, ok := orderFields(obj, "id", "name", "currency", "currency_exponent", "amount_minor", "credit_micros"); !ok || json.Unmarshal(obj, &item) != nil {
			return nil, errors.New("invalid order catalog product fields")
		}
		exp, known := orderCurrencyExponents[item.Currency]
		if !orderCatalogID.MatchString(item.ID) || seen[item.ID] || item.Name == "" || strings.TrimSpace(item.Name) != item.Name ||
			utf8.RuneCountInString(item.Name) > 80 || strings.IndexFunc(item.Name, unicode.IsControl) >= 0 ||
			!known || exp != item.CurrencyExponent || !positiveOrderInteger(item.AmountMinor, 1_000_000_000_000) ||
			!positiveOrderInteger(item.CreditMicros, 1_000_000_000_000_000) {
			return nil, errors.New("invalid or duplicate order product, currency scale, amount or credits")
		}
		seen[item.ID] = true
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	hash := payloadHash(struct {
		SchemaVersion  int                `json:"schema_version"`
		CatalogVersion string             `json:"catalog_version"`
		Products       []orderCatalogItem `json:"products"`
	}{version, label, items})
	result := &orderCatalog{version: label, digest: hex.EncodeToString(hash[:])}
	for _, item := range items {
		sum := payloadHash(item)
		result.products = append(result.products, OrderIntentProduct{
			ID: item.ID, Name: item.Name, Currency: item.Currency, CurrencyExponent: item.CurrencyExponent,
			CatalogVersion: label, CatalogDigest: result.digest, ProductDigest: hex.EncodeToString(sum[:]),
			AmountMinor: item.AmountMinor, CreditMicros: item.CreditMicros,
		})
	}
	return result, nil
}

func (a *App) lookupOrderIntentProduct(productID, catalogDigest, productDigest string) (OrderIntentProduct, error) {
	if a.orderCatalog == nil {
		return OrderIntentProduct{}, failure(409, "ORDER_CATALOG_DISABLED", "当前商品目录已关闭，可查看或取消已有意向")
	}
	if a.orderCatalog.digest == catalogDigest {
		for _, item := range a.orderCatalog.products {
			if item.ID == productID && item.ProductDigest == productDigest {
				return item, nil
			}
		}
	}
	return OrderIntentProduct{}, failure(409, "ORDER_CATALOG_CHANGED", "商品或目录版本已变化，请读取最新商品后重新确认")
}
