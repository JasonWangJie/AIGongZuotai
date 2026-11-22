package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrderReconcileCLIHelpAndRedactedErrors(t *testing.T) {
	var out, diag bytes.Buffer
	if code := run([]string{"-h"}, "", &out, &diag); code != 0 || !strings.Contains(out.String(), "-statement") {
		t.Fatal("help")
	}
	out.Reset()
	diag.Reset()
	if code := run([]string{"-merchant-account-secret=private"}, "", &out, &diag); code == 0 || out.Len() != 0 || strings.Contains(diag.String(), "private") {
		t.Fatal("invalid command disclosed an argument")
	}
	out.Reset()
	diag.Reset()
	args := []string{"-statement", filepath.Join("..", "..", "config", "order-reconciliation.example.json"), "-merchant", "synthetic-shop", "-environment", "test", "-workspace", "ws_" + strings.Repeat("a", 32), "-from", "2026-11-01T00:00:00Z", "-to", "2026-11-02T00:00:00Z"}
	if code := run(args, "", &out, &diag); code == 0 || out.Len() != 0 || !strings.Contains(diag.String(), "DATABASE_URL is required") || strings.Contains(diag.String(), "synthetic-shop") {
		t.Fatal("failure emitted report or scope identifiers")
	}
}
