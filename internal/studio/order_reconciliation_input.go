package studio

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxEvidenceBytes = 1 << 20
const maxEvidenceRows = 2000

var evidenceAlias = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// OrderReconciliationConfig binds a separately supplied operator scope to a
// locally normalized file. No merchant API credential is part of this format.
type OrderReconciliationConfig struct {
	DatabaseURL, Statement, Merchant, Environment, WorkspaceID, From, To string
}

type evidenceScope struct {
	Format      string    `json:"format"`
	Merchant    string    `json:"merchant"`
	Environment string    `json:"environment"`
	Workspace   string    `json:"workspace_id"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
}
type evidenceEvent struct {
	ID       string    `json:"event_id"`
	Kind     string    `json:"kind"`
	Payment  string    `json:"payment_id"`
	Refund   string    `json:"refund_id,omitempty"`
	Order    string    `json:"order_id"`
	Currency string    `json:"currency"`
	Minor    string    `json:"amount_minor"`
	At       time.Time `json:"occurred_at"`
}
type orderEvidence struct {
	Events      []evidenceEvent // unique event identities, in first observed file order
	Rows        int
	InputDigest string
}

// time.Parse alone accepts decimal commas, normalizes invalid numeric zones
// and truncates excess fractional digits. Such normalization loses evidence.
var evidenceTimeSyntax = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?([Zz]|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func parseEvidenceTime(value string) (time.Time, error) {
	if !evidenceTimeSyntax.MatchString(value) {
		return time.Time{}, errors.New("invalid evidence timestamp")
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.ToUpper(value))
	if err != nil {
		return time.Time{}, errors.New("invalid evidence timestamp")
	}
	return parsed.UTC(), nil
}

func parseEvidenceScope(c OrderReconciliationConfig) (evidenceScope, error) {
	from, err1 := parseEvidenceTime(c.From)
	to, err2 := parseEvidenceTime(c.To)
	if !evidenceAlias.MatchString(c.Merchant) || (c.Environment != "test" && c.Environment != "live") ||
		!validOperationsID(c.WorkspaceID, "ws") || err1 != nil || err2 != nil ||
		from.IsZero() || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		return evidenceScope{}, errors.New("require a merchant alias, test/live environment, workspace and an RFC3339 window of up to 31 days")
	}
	return evidenceScope{Format: "studio.order-evidence.v1", Merchant: c.Merchant, Environment: c.Environment, Workspace: c.WorkspaceID, From: from.UTC(), To: to.UTC()}, nil
}

func localStatementName(s string) bool {
	return s != "" && !strings.Contains(s, "://") && !strings.HasPrefix(strings.ReplaceAll(s, "\\", "/"), "//")
}

func readStatementFile(path string) ([]byte, error) {
	const msg = "statement must be a readable regular local file of 1 byte to 1 MiB"
	if !localStatementName(path) {
		return nil, errors.New(msg)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxEvidenceBytes {
		return nil, errors.New(msg)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !localStatementName(resolved) {
		return nil, errors.New(msg)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, errors.New(msg)
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, errors.New(msg)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEvidenceBytes+1))
	if err != nil || len(data) < 1 || len(data) > maxEvidenceBytes {
		return nil, errors.New(msg)
	}
	return data, nil
}

func parseOrderEvidence(blob []byte, want evidenceScope) (orderEvidence, error) {
	invalid := errors.New("invalid normalized statement format, bounded fields or scope")
	if len(blob) == 0 || len(blob) > maxEvidenceBytes || !utf8.Valid(blob) || validateCatalogJSONKeys(blob) != nil {
		return orderEvidence{}, invalid
	}
	root, ok := orderFields(blob, "format", "merchant", "environment", "workspace_id", "from", "to", "entries")
	if !ok {
		return orderEvidence{}, invalid
	}
	// Decode timestamps as strings until all significant digits and the
	// explicit zone have passed the same checks used by the operator flags.
	var header struct {
		Format      string `json:"format"`
		Merchant    string `json:"merchant"`
		Environment string `json:"environment"`
		Workspace   string `json:"workspace_id"`
		From        string `json:"from"`
		To          string `json:"to"`
	}
	if json.Unmarshal(blob, &header) != nil || header.Format != want.Format {
		return orderEvidence{}, invalid
	}
	got, err := parseEvidenceScope(OrderReconciliationConfig{Merchant: header.Merchant, Environment: header.Environment, WorkspaceID: header.Workspace, From: header.From, To: header.To})
	if err != nil || got != want {
		return orderEvidence{}, invalid
	}
	var entries []json.RawMessage
	if json.Unmarshal(root["entries"], &entries) != nil || len(entries) < 1 || len(entries) > maxEvidenceRows {
		return orderEvidence{}, invalid
	}
	result := orderEvidence{Rows: len(entries), InputDigest: reconciliationDigest(blob)}
	seen := make(map[string]evidenceEvent, len(entries))
	fields := []string{"event_id", "kind", "payment_id", "order_id", "currency", "amount_minor", "occurred_at"}
	for _, raw := range entries {
		var e evidenceEvent
		wire := struct {
			*evidenceEvent
			OccurredAt string `json:"occurred_at"`
		}{evidenceEvent: &e}
		if json.Unmarshal(raw, &wire) != nil {
			return orderEvidence{}, invalid
		}
		e.At, err = parseEvidenceTime(wire.OccurredAt)
		if err != nil {
			return orderEvidence{}, invalid
		}
		required := fields
		switch e.Kind {
		case "payment":
		case "refund":
			required = append(fields, "refund_id")
			if !evidenceAlias.MatchString(e.Refund) {
				return orderEvidence{}, invalid
			}
		default:
			return orderEvidence{}, invalid
		}
		if _, ok = orderFields(raw, required...); !ok {
			return orderEvidence{}, invalid
		}
		if _, ok = orderCurrencyExponents[e.Currency]; !ok || !evidenceAlias.MatchString(e.ID) ||
			!evidenceAlias.MatchString(e.Payment) || !validOperationsID(e.Order, "ord") ||
			!positiveOrderInteger(e.Minor, 1_000_000_000_000) || e.At.Before(want.From) || !e.At.Before(want.To) {
			return orderEvidence{}, invalid
		}
		if previous, ok := seen[e.ID]; ok {
			if previous != e {
				return orderEvidence{}, errors.New("one event identity has conflicting contents; no reconciliation report produced")
			}
			continue
		}
		seen[e.ID] = e
		result.Events = append(result.Events, e)
	}
	return result, nil
}
