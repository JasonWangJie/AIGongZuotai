// storage performs an offline, private local-to-S3 copy and verification.
// It does not read .env files automatically and never deletes local copies.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"aigongzuotai/internal/studio"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	apply := flag.Bool("apply", false, "copy and SHA256-verify objects; default prints a read-only plan")
	limit := flag.Int("limit", 100, "maximum assets in one batch, 1..1000")
	after := flag.String("after", "", "resume after this asset ID")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	dir := os.Getenv("ASSET_DIR")
	if dir == "" {
		dir = ".runtime/assets"
	}
	lease, err := studio.AcquireStorageLease(dir)
	if err != nil {
		return err
	}
	defer lease.Close()
	pathStyle := false
	if value := os.Getenv("S3_FORCE_PATH_STYLE"); value != "" {
		pathStyle, err = strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("S3_FORCE_PATH_STYLE must be true or false")
		}
	}
	config := studio.AssetStorageConfig{Backend: os.Getenv("ASSET_STORAGE_BACKEND"), Endpoint: os.Getenv("S3_ENDPOINT"), Region: os.Getenv("S3_REGION"), Bucket: os.Getenv("S3_BUCKET"), Prefix: os.Getenv("S3_PREFIX"), AccessKeyID: os.Getenv("S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("S3_SESSION_TOKEN"), ForcePathStyle: pathStyle}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	report, err := studio.MigrateAssets(ctx, config, dir, *apply, *limit, *after)
	// Partial success is reviewable and reentrant. Do not print raw SDK errors,
	// endpoints, access keys, bucket names, object locators or source prompts.
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		return fmt.Errorf("storage migration stopped: %w", err)
	}
	return nil
}
