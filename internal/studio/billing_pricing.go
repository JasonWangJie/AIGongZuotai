package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const billingPolicyVersion = "credits-v1"

// Prices describe platform credits. They never convert an undocumented gateway
// quota/cost, imply CNY, or derive final token usage from visible text.
type pricingFile struct {
	SchemaVersion int           `json:"schema_version"`
	ProviderMode  string        `json:"provider_mode"`
	Unit          string        `json:"unit"`
	Scale         int           `json:"scale"`
	Rates         []pricingRate `json:"rates"`
}
type pricingRate struct {
	ModelID                string `json:"model_id"`
	Kind                   string `json:"kind"`
	Operation              string `json:"operation"`
	PresetID               string `json:"preset_id,omitempty"`
	InputPerMillionMicros  string `json:"input_per_million_micros,omitempty"`
	OutputPerMillionMicros string `json:"output_per_million_micros,omitempty"`
	ArtifactMicros         string `json:"artifact_micros,omitempty"`
}
type pricingCatalog struct {
	Version      string
	ProviderMode string
	Rates        map[string]LedgerPriceSnapshot
}

func pricingKey(model, kind, operation, preset string) string {
	return strings.Join([]string{model, kind, operation, preset}, "\x00")
}

func parseCreditMicros(raw string, allowZero bool) (int64, error) {
	if raw == "" || len(raw) > 16 || len(raw) > 1 && raw[0] == '0' {
		return 0, errors.New("amount must be a canonical integer decimal string")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, errors.New("amount must be a canonical integer decimal string")
		}
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 || v > MaxLedgerMicros || !allowZero && v == 0 {
		return 0, errors.New("amount exceeds the credit range")
	}
	return v, nil
}

func loadPricingCatalog(c Config) (*pricingCatalog, error) {
	if c.BillingMode == "" || c.BillingMode == "disabled" {
		return nil, nil
	}
	if c.BillingMode != "credits" || c.BillingPriceFile == "" {
		return nil, errors.New("BILLING_MODE credits requires BILLING_PRICE_FILE")
	}
	f, err := os.Open(c.BillingPriceFile)
	if err != nil {
		return nil, errors.New("BILLING_PRICE_FILE cannot be opened")
	}
	defer f.Close()
	blob, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err != nil || len(blob) > 128<<10 || !utf8.Valid(blob) {
		return nil, errors.New("BILLING_PRICE_FILE requires at most 128 KiB UTF-8 JSON")
	}
	if validateCatalogJSONKeys(blob) != nil {
		return nil, errors.New("BILLING_PRICE_FILE contains duplicate or invalid JSON keys")
	}
	var file pricingFile
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, errors.New("BILLING_PRICE_FILE contains invalid JSON or unknown fields")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || file.SchemaVersion != 1 || file.Unit != LedgerUnit || file.Scale != LedgerScale || len(file.Rates) < 1 || len(file.Rates) > 512 {
		return nil, errors.New("price catalog requires schema 1, CREDIT scale 6 and 1 to 512 rates")
	}
	if (file.ProviderMode != "demo" && file.ProviderMode != "live") || file.ProviderMode != c.Mode {
		return nil, errors.New("price catalog provider_mode must match the configured provider mode; demo prices cannot enable live charging")
	}
	result := &pricingCatalog{ProviderMode: file.ProviderMode, Rates: map[string]LedgerPriceSnapshot{}}
	for _, r := range file.Rates {
		if !catalogID.MatchString(r.ModelID) {
			return nil, errors.New("price model_id is invalid")
		}
		key := pricingKey(r.ModelID, r.Kind, r.Operation, r.PresetID)
		if _, exists := result.Rates[key]; exists {
			return nil, errors.New("price catalog contains a duplicate model/operation/preset rate")
		}
		p := LedgerPriceSnapshot{PolicyVersion: billingPolicyVersion}
		switch r.Kind {
		case "chat":
			if r.Operation != "chat" || r.PresetID != "" || r.ArtifactMicros != "" {
				return nil, errors.New("chat prices require operation chat and token rates only")
			}
			p.Basis = "chat_tokens"
			p.InputPerMillionMicros, err = parseCreditMicros(r.InputPerMillionMicros, true)
			if err != nil {
				return nil, errors.New("invalid input token price")
			}
			p.OutputPerMillionMicros, err = parseCreditMicros(r.OutputPerMillionMicros, true)
			if err != nil || p.InputPerMillionMicros == 0 && p.OutputPerMillionMicros == 0 {
				return nil, errors.New("invalid output token price or both token prices zero")
			}
		case "image", "video":
			if r.Operation != "generate" && !(r.Kind == "image" && r.Operation == "edit") || !catalogID.MatchString(r.PresetID) || r.InputPerMillionMicros != "" || r.OutputPerMillionMicros != "" {
				return nil, errors.New("media prices require an exact preset and operation with artifact price only")
			}
			p.Basis = "delivered_artifact"
			p.ArtifactMicros, err = parseCreditMicros(r.ArtifactMicros, false)
			if err != nil {
				return nil, errors.New("invalid delivered artifact price")
			}
		default:
			return nil, errors.New("unsupported price kind")
		}
		result.Rates[key] = p
	}
	result.Version = "price_" + contentVersion(struct {
		File   pricingFile
		Policy string
	}{file, billingPolicyVersion})
	return result, nil
}

func (p *pricingCatalog) chatPrice(model Model) (LedgerPriceSnapshot, int64, error) {
	if p == nil {
		return LedgerPriceSnapshot{}, 0, billingDisabled()
	}
	price, found := p.Rates[pricingKey(model.ID, "chat", "chat", "")]
	if !found {
		return price, 0, failure(422, "PRICE_UNAVAILABLE", "当前模型尚未配置有效报价")
	}
	price.ProviderMode = p.ProviderMode
	price.MaxOutputTokens = int64(model.Capabilities.MaxOutputTokens)
	price.MaxInputTokens = int64(model.Capabilities.ContextWindowTokens) - price.MaxOutputTokens
	if price.MaxInputTokens < 1 || price.MaxOutputTokens < 1 {
		return price, 0, failure(422, "PRICE_UNAVAILABLE", "当前模型没有明确的输入和输出预算")
	}
	amount, err := ledgerChatAmount(price, price.MaxInputTokens, price.MaxOutputTokens)
	if err == nil && amount < 1 {
		err = failure(422, "PRICE_UNAVAILABLE", "当前模型没有有效的冻结额度")
	}
	return price, amount, err
}

func (p *pricingCatalog) taskPrice(model Model, operation string, parameters TaskParameters) (LedgerPriceSnapshot, int64, error) {
	if p == nil {
		return LedgerPriceSnapshot{}, 0, billingDisabled()
	}
	presetID := ""
	for _, preset := range model.Capabilities.ParameterPresets {
		if preset.AspectRatio == parameters.AspectRatio && preset.Size == parameters.Size && preset.Resolution == parameters.Resolution && preset.Seconds == parameters.Seconds {
			presetID = preset.ID
			break
		}
	}
	price, found := p.Rates[pricingKey(model.ID, model.Kind, operation, presetID)]
	if presetID == "" || !found || parameters.Count < 1 || parameters.Count > model.Capabilities.MaxCount {
		return price, 0, failure(422, "PRICE_UNAVAILABLE", "所选模型、操作或规格尚未配置报价")
	}
	price.ProviderMode = p.ProviderMode
	price.RequestedUnits = parameters.Count
	if price.ArtifactMicros > MaxLedgerMicros/int64(parameters.Count) {
		return price, 0, failure(422, "LEDGER_AMOUNT_OVERFLOW", "本次最大冻结额度超出范围")
	}
	return price, price.ArtifactMicros * int64(parameters.Count), nil
}

func billingDisabled() error {
	return failure(409, "BILLING_DISABLED", "当前环境未启用工作区权益计费")
}
