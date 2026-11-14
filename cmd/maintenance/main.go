// maintenance performs explicit offline maintenance. It does not load .env
// files automatically, start workers, call a gateway, or migrate a database.
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
	apply := flag.Bool("apply", false, "remove the exact old unreferenced files from a reviewed plan")
	digest := flag.String("plan-digest", "", "exact SHA256 plan digest required with --apply")
	age := flag.Duration("min-age", 24*time.Hour, "minimum orphan age, at least 24h")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments; use --help")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := studio.MaintainPrivateStorage(ctx, studio.StorageMaintenanceConfig{DatabaseURL: os.Getenv("DATABASE_URL"), ReferenceDir: os.Getenv("REFERENCE_DIR"), AssetDir: os.Getenv("ASSET_DIR"), WebDir: os.Getenv("APP_WEB_DIR"), MinAge: *age, Apply: *apply, PlanDigest: *digest})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
