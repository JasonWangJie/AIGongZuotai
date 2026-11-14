package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

const maxReportedChatTokens int64 = 1_000_000_000

// ChatUsage is private execution evidence, not a supplier invoice. Only a
// complete, successful run with reported/demo usage is eligible for the
// ledger's separate pricing and settlement policy. Never derive it from text.
type ChatUsage struct {
	Status           string `json:"status"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	Source           string `json:"source"`
	Reason           string `json:"reason,omitempty"`
	Complete         bool   `json:"complete"`
}

// ChatUsageProvider is optional so older/demo fixture providers remain valid.
// Missing support must be reconciled by billing, never estimated as usage.
type ChatUsageProvider interface {
	StreamMessagesUsage(context.Context, string, ChatRequest, func(string) error) (ChatUsage, error)
}

func missingChatUsage(source, reason string) ChatUsage {
	return ChatUsage{Status: "missing", Source: source, Reason: reason}
}

func (p *openAIChat) StreamMessagesUsage(ctx context.Context, upstreamModel string, request ChatRequest, emit func(string) error) (ChatUsage, error) {
	if _, err := validateChatWireRequest(request); err != nil {
		return missingChatUsage("gateway", "request_not_started"), err
	}
	return p.streamMessagesWithUsage(ctx, upstreamModel, request, emit, true)
}

func (d demoChat) StreamMessagesUsage(ctx context.Context, model string, request ChatRequest, emit func(string) error) (ChatUsage, error) {
	if err := d.StreamMessages(ctx, model, request, emit); err != nil {
		return missingChatUsage("demo", "stream_incomplete"), err
	}
	if err := ctx.Err(); err != nil {
		return missingChatUsage("demo", "stream_incomplete"), err
	}
	// Deliberately fixed fixture units: not a tokenizer, a cost estimate, or a
	// measurement of prompt/output length. Production usage comes from gateway.
	return ChatUsage{Status: "demo", Source: "demo", PromptTokens: 128, CompletionTokens: 64, TotalTokens: 192, Complete: true}, nil
}

// encoding/json calls this for every occurrence, including duplicate object
// keys. Do not allow last-key-wins semantics to make ambiguous usage billable.
type chatUsageField struct {
	Raw         json.RawMessage
	Occurrences int
}

func (f *chatUsageField) UnmarshalJSON(raw []byte) error {
	f.Occurrences++
	f.Raw = append(f.Raw[:0], raw...)
	return nil
}

type chatUsageAccumulator struct {
	usage ChatUsage
	seen  bool
}

func newChatUsageAccumulator() *chatUsageAccumulator {
	return &chatUsageAccumulator{usage: missingChatUsage("gateway", "usage_missing")}
}

func (a *chatUsageAccumulator) observe(field chatUsageField) {
	if field.Occurrences > 1 {
		a.usage.Status, a.usage.Reason = "invalid", "usage_conflict"
		return
	}
	if field.Occurrences == 0 || bytes.Equal(bytes.TrimSpace(field.Raw), []byte("null")) {
		return
	}
	parsed, err := parseReportedChatUsage(field.Raw)
	if err != nil {
		if a.usage.Status != "invalid" {
			a.usage.Status, a.usage.Reason = "invalid", "usage_invalid"
		}
		return
	}
	if a.seen && (a.usage.PromptTokens != parsed.PromptTokens || a.usage.CompletionTokens != parsed.CompletionTokens || a.usage.TotalTokens != parsed.TotalTokens) {
		a.usage.Status, a.usage.Reason = "invalid", "usage_conflict"
		return
	}
	if !a.seen {
		a.usage.PromptTokens, a.usage.CompletionTokens, a.usage.TotalTokens = parsed.PromptTokens, parsed.CompletionTokens, parsed.TotalTokens
		a.seen = true
	}
	if a.usage.Status != "invalid" {
		a.usage.Status, a.usage.Reason = "reported", ""
	}
}

func (a *chatUsageAccumulator) finish(err error) ChatUsage {
	a.usage.Complete = err == nil
	if err != nil {
		// Retain any observed counts for private review, but never treat a partial
		// stream, cancellation, or downstream emit failure as complete usage.
		a.usage.Status, a.usage.Reason = "missing", "stream_incomplete"
	}
	return a.usage
}

func parseReportedChatUsage(raw []byte) (ChatUsage, error) {
	bad := func() (ChatUsage, error) { return ChatUsage{}, errors.New("invalid upstream usage") }
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return bad()
	}
	seen := make(map[string]bool)
	counts := make(map[string]int64, 3)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] || len(seen) >= 32 {
			return bad()
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return bad()
		}
		if name != "prompt_tokens" && name != "completion_tokens" && name != "total_tokens" {
			// Details (cached/reasoning tokens) are subsets of the reported totals;
			// this flat-token tariff never adds them or guesses a discount.
			continue
		}
		value = bytes.TrimSpace(value)
		if len(value) < 1 || len(value) > 10 {
			return bad()
		}
		for _, b := range value {
			if b < '0' || b > '9' {
				return bad()
			}
		}
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil || n > maxReportedChatTokens {
			return bad()
		}
		counts[name] = n
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(counts) != 3 {
		return bad()
	}
	if _, err = decoder.Token(); err != io.EOF {
		return bad()
	}
	if counts["prompt_tokens"]+counts["completion_tokens"] != counts["total_tokens"] {
		return bad()
	}
	return ChatUsage{Status: "reported", Source: "gateway", PromptTokens: counts["prompt_tokens"], CompletionTokens: counts["completion_tokens"], TotalTokens: counts["total_tokens"]}, nil
}

func parseOpenAIStreamUsage(r io.Reader, emit func(string) error) (ChatUsage, error) {
	accumulator := newChatUsageAccumulator()
	err := parseOpenAIStreamObserved(r, emit, accumulator)
	return accumulator.finish(err), err
}
