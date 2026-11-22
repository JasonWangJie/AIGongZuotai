// risk-policy manages a workspace's credit admission thresholds while the workbench is stopped.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"aigongzuotai/internal/studio"
)

func main() {
	var cfg studio.OperationsAdminConfig
	var limits studio.CreditRiskPolicyLimits
	flag.StringVar(&cfg.Action, "action", "inspect", "inspect | set | disable")
	flag.StringVar(&cfg.WorkspaceID, "workspace", "", "target workspace ID")
	flag.Int64Var(&limits.MaxHeldMicros, "max-held-micros", 0, "maximum outstanding holds in micro-CREDIT")
	flag.IntVar(&limits.MaxPerMinute, "max-per-minute", 0, "maximum newly accepted paid generations per UTC minute (1–1000)")
	flag.StringVar(&cfg.OperationKey, "operation-key", "", "stable key for this exact change")
	flag.StringVar(&cfg.Reason, "reason", "", "bounded opaque reason code")
	flag.StringVar(&cfg.EvidenceRef, "evidence", "", "internal evidence reference")
	flag.StringVar(&cfg.PlanDigest, "plan-digest", "", "exact dry-run digest")
	flag.BoolVar(&cfg.Apply, "apply", false, "commit the reviewed change")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	cfg.RiskPolicy = &limits
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := studio.AdministerOperations(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "could not output the policy report; check the original operation key")
		os.Exit(1)
	}
}
