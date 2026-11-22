package studio

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"
)

type orderCatalogCapabilities struct {
	Enabled        bool                 `json:"enabled"`
	CanCreate      bool                 `json:"can_create"`
	CanManage      bool                 `json:"can_manage"`
	CatalogVersion string               `json:"catalog_version,omitempty"`
	CatalogDigest  string               `json:"catalog_digest,omitempty"`
	Items          []OrderIntentProduct `json:"items"`
}

func (a *App) registerOrderIntentRoutes(mux *http.ServeMux) {
	const base = "/api/v1/order-intents"
	mux.HandleFunc("GET "+base+"/catalog", a.orderCatalogHTTP)
	mux.HandleFunc("POST "+base, a.createOrderIntentHTTP)
	mux.HandleFunc("GET "+base, a.listOrderIntentsHTTP)
	mux.HandleFunc("GET "+base+"/receipt", a.orderIntentReceiptHTTP)
	mux.HandleFunc("GET "+base+"/{order}", a.orderIntentDetailHTTP)
	mux.HandleFunc("GET "+base+"/{order}/payment-status", a.orderPaymentStatusHTTP)
	mux.HandleFunc("POST "+base+"/{order}/cancel", a.cancelOrderIntentHTTP)
}

func (a *App) orderCatalogHTTP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	canManage := a.store != nil && a.config.AuthMode == "required" && p != nil && p.User != nil && !p.AccountRestricted
	out := orderCatalogCapabilities{CanManage: canManage, Items: []OrderIntentProduct{}}
	if c := a.orderCatalog; c != nil && canManage {
		out.Enabled, out.CanCreate = true, true
		out.CatalogVersion, out.CatalogDigest, out.Items = c.version, c.digest, c.products
	}
	writeJSON(w, 200, out)
}

func decodeOrderInput(w http.ResponseWriter, r *http.Request, out any, fields ...string) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return failure(415, "JSON_REQUIRED", "请使用 application/json")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return failure(413, "ORDER_BODY_TOO_LARGE", "订购意向请求最多 4 KiB")
		}
		return failure(400, "INVALID_JSON", "无法读取订购意向请求")
	}
	if !utf8.Valid(raw) || validateCatalogJSONKeys(raw) != nil {
		return failure(400, "INVALID_JSON", "订购意向请求必须是有效 UTF-8，字段不可重复")
	}
	if _, ok := orderFields(raw, fields...); !ok || json.Unmarshal(raw, out) != nil {
		return failure(400, "INVALID_JSON", "订购意向请求字段或类型无效")
	}
	return nil
}

func (a *App) createOrderIntentHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in OrderIntentCreate
	if err = decodeOrderInput(w, r, &in, "product_id", "catalog_digest", "product_digest"); err != nil {
		writeError(w, err)
		return
	}
	order, replay, err := a.createOrderIntent(principalFrom(r), in, key)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
		status = http.StatusOK
	}
	writeJSON(w, status, order)
}

func (a *App) cancelOrderIntentHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in struct {
		ExpectedVersion uint64 `json:"expected_version"`
	}
	if err = decodeOrderInput(w, r, &in, "expected_version"); err != nil {
		writeError(w, err)
		return
	}
	order, replay, err := a.cancelOrderIntent(principalFrom(r), r.PathValue("order"), in.ExpectedVersion, key)
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, order)
}

func (a *App) orderIntentReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	op := r.URL.Query()["operation"]
	if len(op) != 1 || (op[0] != "create" && op[0] != "cancel") {
		writeError(w, failure(400, "INVALID_ORDER_OPERATION", "请选择创建或取消操作的原收据"))
		return
	}
	order, err := a.orderIntentReceipt(principalFrom(r), op[0], key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, order)
}

func (a *App) orderIntentDetailHTTP(w http.ResponseWriter, r *http.Request) {
	order, err := a.orderIntentDetail(principalFrom(r), r.PathValue("order"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, order)
}

func (a *App) listOrderIntentsHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := editorPageLimit(r, "INVALID_ORDER_LIMIT")
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := a.listOrderIntents(principalFrom(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
