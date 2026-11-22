// operations manages explicit workspace-scoped grants while the workbench is
// stopped. It intentionally never loads .env or accepts passwords or emails.
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
	action := flag.String("action", "inspect", "inspect | grant | revoke")
	workspace := flag.String("workspace", "", "target workspace ID")
	user := flag.String("user", "", "existing user ID; no email or credentials")
	target := flag.String("target-user", "", "specific target user for account governance; use -user for the authorized operator")
	role := flag.String("role", "", "observer | operator | finance | moderator | risk_reviewer | support")
	key := flag.String("operation-key", "", "stable operation key; reuse exact request after unknown commit")
	reason := flag.String("reason", "", "bounded opaque reason code")
	evidence := flag.String("evidence", "", "bounded opaque evidence reference, not a URL")
	apply := flag.Bool("apply", false, "commit the exact reviewed plan; default is rollback")
	digest := flag.String("plan-digest", "", "SHA256 dry-run digest, required with apply")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments; use --help")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := studio.AdministerOperations(ctx, studio.OperationsAdminConfig{DatabaseURL: os.Getenv("DATABASE_URL"), Action: *action, WorkspaceID: *workspace, UserID: *user, TargetUserID: *target, Role: *role, OperationKey: *key, Reason: *reason, EvidenceRef: *evidence, Apply: *apply, PlanDigest: *digest})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
