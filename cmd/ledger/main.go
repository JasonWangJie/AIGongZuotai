// ledger is an explicit offline operator tool. It never loads .env or charges
// money. Values are CREDIT micros (six decimals), not a currency conversion.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"aigongzuotai/internal/studio"
)

func main() {
	action := flag.String("action", "inspect", "inspect | grant | refund | resolve")
	workspace := flag.String("workspace", "", "workspace ID for inspect/grant")
	reservation := flag.String("reservation", "", "reservation ID for refund/resolve")
	amount := flag.String("amount-micros", "", "positive canonical integer CREDIT micros, only grant")
	key := flag.String("operation-key", "", "stable unique operation key; reuse unchanged after an unknown response")
	reason := flag.String("reason", "", "opaque reason code without personal data")
	evidence := flag.String("evidence", "", "opaque evidence reference without secrets or message content")
	decision := flag.String("decision", "", "resolve only: charge | release")
	prompt := flag.Int64("prompt-tokens", -1, "verified prompt tokens for resolve charge")
	completion := flag.Int64("completion-tokens", -1, "verified completion tokens for resolve charge")
	units := flag.Int("delivered-units", 0, "verified delivered artifacts for resolve charge")
	source := flag.String("usage-source", "gateway", "gateway | demo, only with verified token counts")
	apply := flag.Bool("apply", false, "commit the exact reviewed plan (default rolls back)")
	digest := flag.String("plan-digest", "", "SHA256 from dry-run, required for apply")
	flag.Parse()
	fail := func(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(2) }
	if flag.NArg() != 0 {
		fail("unexpected arguments; use --help")
	}
	var value int64
	if *action == "grant" {
		v, err := strconv.ParseInt(*amount, 10, 64)
		if err != nil || v < 1 || v > studio.MaxLedgerMicros || strconv.FormatInt(v, 10) != *amount {
			fail("grant requires canonical positive amount-micros within range")
		}
		value = v
	} else if *amount != "" {
		fail("amount-micros is only valid for grant")
	}
	if *action != "resolve" && (*decision != "" || *prompt != -1 || *completion != -1 || *units != 0) {
		fail("resolution evidence is only valid for resolve")
	}
	if (*action == "refund" || *action == "resolve") && *workspace != "" {
		fail("reservation determines workspace; omit --workspace")
	}
	if (*action == "inspect" || *action == "grant") && *reservation != "" {
		fail("reservation is only valid for refund/resolve")
	}
	var usage *studio.ChatUsage
	if *prompt != -1 || *completion != -1 {
		if *prompt < 0 || *completion < 0 || *prompt > 1_000_000_000 || *completion > 1_000_000_000 || *units != 0 || (*source != "gateway" && *source != "demo") {
			fail("use either valid token evidence or delivered-units")
		}
		status := "reported"
		if *source == "demo" {
			status = "demo"
		}
		usage = &studio.ChatUsage{Status: status, Source: *source, PromptTokens: *prompt, CompletionTokens: *completion, TotalTokens: *prompt + *completion, Complete: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := studio.AdministerLedger(ctx, studio.LedgerAdminConfig{DatabaseURL: os.Getenv("DATABASE_URL"), Action: *action, WorkspaceID: *workspace, Apply: *apply, PlanDigest: *digest,
		Credit:  studio.LedgerCreditRequest{WorkspaceID: *workspace, AmountMicros: value, OperationKey: *key, Reason: *reason, EvidenceRef: *evidence},
		Refund:  studio.LedgerRefundRequest{ReservationID: *reservation, OperationKey: *key, Reason: *reason, EvidenceRef: *evidence},
		Resolve: studio.LedgerResolveRequest{ReservationID: *reservation, OperationKey: *key, Reason: *reason, EvidenceRef: *evidence, Decision: *decision, Usage: usage, DeliveredUnits: *units}})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
