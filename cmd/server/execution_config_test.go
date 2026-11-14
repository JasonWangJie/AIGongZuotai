package main

import (
	"strings"
	"testing"
	"time"
)

func TestTaskLeaseEnvironmentBoundsAndRedaction(t *testing.T) {
	for _, raw := range []string{"", "0", "4", "301", "-5", "+30", "30s", "30.0", " 30", "30 ", "999999999999999999999999", "credential-should-not-appear"} {
		_, err := taskLeaseFromEnv(func(string) (string, bool) { return raw, true })
		if err == nil || strings.Contains(err.Error(), "credential-should-not-appear") {
			t.Fatalf("invalid lease input accepted or disclosed")
		}
	}
	for _, value := range []struct {
		raw  string
		want time.Duration
	}{{"5", 5 * time.Second}, {"30", 30 * time.Second}, {"300", 5 * time.Minute}} {
		got, err := taskLeaseFromEnv(func(string) (string, bool) { return value.raw, true })
		if err != nil || got != value.want {
			t.Fatal("lease bound rejected", value.raw)
		}
	}
	got, err := taskLeaseFromEnv(func(string) (string, bool) { return "", false })
	if err != nil || got != 0 {
		t.Fatal("missing environment must preserve Config default")
	}
}
