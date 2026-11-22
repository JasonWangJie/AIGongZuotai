// order-reconcile compares one operator-normalized evidence file with frozen
// order terms. It neither loads .env nor sends or acknowledges payments.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"aigongzuotai/internal/studio"
)

const usage = "order-reconcile -statement <local.json> -merchant <alias> -environment <test|live> -workspace <ws_id> -from <RFC3339> -to <RFC3339>\nDATABASE_URL supplies the database connection. Output contains unverified clues and aggregate counts only."

func run(args []string, dsn string, out, diagnostic io.Writer) int {
	fs := flag.NewFlagSet("order-reconcile", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := studio.OrderReconciliationConfig{DatabaseURL: dsn}
	fs.StringVar(&cfg.Statement, "statement", "", "input file")
	fs.StringVar(&cfg.Merchant, "merchant", "", "opaque merchant alias")
	fs.StringVar(&cfg.Environment, "environment", "", "test or live evidence")
	fs.StringVar(&cfg.WorkspaceID, "workspace", "", "workspace ID")
	fs.StringVar(&cfg.From, "from", "", "inclusive RFC3339 start")
	fs.StringVar(&cfg.To, "to", "", "exclusive RFC3339 end")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, usage)
			return 0
		}
		fmt.Fprintln(diagnostic, usage)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(diagnostic, usage)
		return 2
	}
	report, err := studio.ReconcileOrderEvidence(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if enc.Encode(report) != nil {
		fmt.Fprintln(diagnostic, "unable to write reconciliation report")
		return 1
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Getenv("DATABASE_URL"), os.Stdout, os.Stderr)) }
