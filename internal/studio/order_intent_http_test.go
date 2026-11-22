package studio

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrderIntentHTTPInputAndDisabledCapabilities(t *testing.T) {
	var valid OrderIntentCreate
	decodeCreate := func(body, contentType string) error {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		return decodeOrderInput(httptest.NewRecorder(), r, &valid, "product_id", "catalog_digest", "product_digest")
	}
	body := `{"product_id":"starter","catalog_digest":"a","product_digest":"b"}`
	if err := decodeCreate(body, "application/json; charset=utf-8"); err != nil || valid.ProductID != "starter" {
		t.Fatal("valid fields failed", err)
	}
	for _, raw := range []string{
		strings.Replace(body, `"product_id"`, `"amount_minor":"1","product_id"`, 1),
		strings.Replace(body, `"product_id"`, `"owner_user_id":"other","product_id"`, 1),
		strings.Replace(body, `"product_id"`, `"product_id":"old","product_id"`, 1),
		strings.Replace(body, `"product_id"`, `"PRODUCT_ID"`, 1),
		strings.Replace(body, `"starter"`, `null`, 1), body + "{}", "null",
	} {
		if code := draftErrorCode(decodeCreate(raw, "application/json")); code != "INVALID_JSON" {
			t.Fatal("invalid order fields", code)
		}
	}
	if draftErrorCode(decodeCreate(body, "text/plain")) != "JSON_REQUIRED" ||
		draftErrorCode(decodeCreate(strings.Repeat(" ", 4097), "application/json")) != "ORDER_BODY_TOO_LARGE" {
		t.Fatal("content type or size limit missing")
	}
	_, s := testApp(t, Config{})
	const base = "/api/v1/order-intents"
	status, raw, _ := request(t, s, "GET", base+"/catalog", "", nil)
	caps := decode[orderCatalogCapabilities](t, raw)
	if status != 200 || caps.Enabled || caps.CanManage || caps.CanCreate || caps.Items == nil || len(caps.Items) != 0 {
		t.Fatal("unexpected memory capabilities", status, caps)
	}
	status, raw, _ = request(t, s, "GET", base, "", nil)
	if status != 501 || !bytes.Contains(raw, []byte("ORDER_INTENT_STORAGE_REQUIRED")) {
		t.Fatal("memory offered order management")
	}
}

func TestPostgresOrderIntentHTTPFrozenCatalogAndDisabledHistory(t *testing.T) {
	cfg := Config{DatabaseURL: testDatabase(t), AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(),
		OrderCatalogFile: filepath.Join("..", "..", "config", "order-catalog.example.json")}
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	a, s := testApp(t, cfg)
	buyer, stranger := authClient(t), authClient(t)
	registerFixture(t, buyer, s.URL, "order-owner@example.test")
	registerFixture(t, stranger, s.URL, "order-other@example.test")
	const base = "/api/v1/order-intents"
	call := func(c *http.Client, method, route, key string, body any, want int) ([]byte, http.Header) {
		return presentationRequest(t, c, s.URL, "", method, base+route, key, body, want)
	}
	getCaps := func() orderCatalogCapabilities {
		raw, _ := call(buyer, "GET", "/catalog", "", nil, 200)
		return decode[orderCatalogCapabilities](t, raw)
	}
	caps := getCaps()
	if !caps.Enabled || !caps.CanCreate || !caps.CanManage || len(caps.Items) != 1 {
		t.Fatal("configured owner catalog missing")
	}
	p := caps.Items[0]
	in := OrderIntentCreate{ProductID: p.ID, CatalogDigest: caps.CatalogDigest, ProductDigest: p.ProductDigest}
	const key = "buy-original-intent"
	raw, _ := call(buyer, "POST", "", key, in, 201)
	original := decode[OrderIntent](t, raw)
	if original.State != "PENDING" || original.Version != 1 || original.Product != p {
		t.Fatal("new order did not freeze the exact visible commercial terms")
	}
	call(authClient(t), "GET", "/"+original.ID, "", nil, 401)
	call(stranger, "GET", "/"+original.ID, "", nil, 404)
	call(stranger, "GET", "/receipt?operation=create", key, nil, 404)
	call(stranger, "POST", "/"+original.ID+"/cancel", "other-cancel-key", map[string]int{"expected_version": 1}, 404)
	rows, _ := call(stranger, "GET", "", "", nil, 200)
	if len(decode[OrderIntentPage](t, rows).Items) != 0 {
		t.Fatal("another account saw the buyer's order")
	}
	call(buyer, "POST", "", "forged-by-buyer", map[string]any{"product_id": p.ID, "catalog_digest": caps.CatalogDigest, "product_digest": p.ProductDigest, "amount_minor": "1"}, 400)
	oldCatalog, err := os.ReadFile(cfg.OrderCatalogFile)
	if err != nil {
		t.Fatal(err)
	}
	newCatalog := bytes.Replace(oldCatalog, []byte(`"1200"`), []byte(`"1500"`), 1)
	if bytes.Equal(newCatalog, oldCatalog) {
		t.Fatal("fixture did not change the actual price")
	}
	cfg.OrderCatalogFile = filepath.Join(t.TempDir(), "revised.json")
	if err = os.WriteFile(cfg.OrderCatalogFile, newCatalog, 0600); err != nil {
		t.Fatal(err)
	}
	restart := func() {
		s.Close()
		a.Close()
		a, s = testApp(t, cfg)
	}
	restart()
	changed := getCaps()
	if changed.Items[0].AmountMinor != "1500" || changed.CatalogDigest == in.CatalogDigest || changed.CatalogVersion != caps.CatalogVersion {
		t.Fatal("changed amount reused old catalog identity")
	}
	call(buyer, "POST", "", "stale-catalog-new-key", in, 409)
	newIn := OrderIntentCreate{ProductID: p.ID, CatalogDigest: changed.CatalogDigest, ProductDigest: changed.Items[0].ProductDigest}
	call(buyer, "POST", "", key, newIn, 409)
	checkOriginal := func() {
		raw, h := call(buyer, "POST", "", key, in, 200)
		replay := decode[OrderIntent](t, raw)
		if h.Get("Idempotency-Replayed") != "true" || replay.ID != original.ID || replay.Product != original.Product || replay.State != "PENDING" {
			t.Fatal("original create key did not return the frozen receipt")
		}
	}
	checkOriginal()
	cfg.OrderCatalogFile = ""
	restart()
	caps = getCaps()
	if caps.Enabled || caps.CanCreate || !caps.CanManage || len(caps.Items) != 0 {
		t.Fatal("disabled catalog hid management or enabled new orders")
	}
	checkOriginal()
	call(buyer, "POST", "", "closed-catalog-new-key", newIn, 409)
	raw, _ = call(buyer, "POST", "/"+original.ID+"/cancel", "cancel-order-once", map[string]int{"expected_version": 1}, 200)
	cancelled := decode[OrderIntent](t, raw)
	if cancelled.ID != original.ID || cancelled.Version != 2 || cancelled.State != "CANCELLED" || cancelled.CancelledAt == nil {
		t.Fatal("original unpaid intent was not cancelled")
	}
	for _, r := range []struct{ route, key, want string }{
		{"/" + original.ID, "", "CANCELLED"}, {"/receipt?operation=cancel", "cancel-order-once", "CANCELLED"},
		{"/receipt?operation=create", key, "PENDING"},
	} {
		raw, _ := call(buyer, "GET", r.route, r.key, nil, 200)
		got := decode[OrderIntent](t, raw)
		if got.State != r.want || got.Product != p || got.ID != original.ID {
			t.Fatal("disabled catalog lost frozen detail or original receipt")
		}
	}
	checkOriginal()
	if outboxCount(t, admin, "SELECT count(*) FROM studio_order_intents") != 1 ||
		outboxCount(t, admin, "SELECT count(*) FROM studio_order_intent_receipts") != 2 {
		t.Fatal("duplicate order or receipt")
	}
	for _, table := range []string{"studio_ledger_journals", "studio_ledger_postings", "studio_ledger_reservations", "studio_ledger_wallets"} {
		if outboxCount(t, admin, "SELECT count(*) FROM "+table) != 0 {
			t.Fatal("order intent changed billing", table)
		}
	}
}
