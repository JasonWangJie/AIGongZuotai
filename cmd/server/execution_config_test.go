package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestChatSchedulingEnvironmentStrictBoundsAndMissingDefaults(t *testing.T) {
	config, err := chatSchedulingFromEnv(func(string) (string, bool) { return "", false })
	if err != nil || config.Workers != 0 || config.QueueSize != 0 || config.ModelConcurrency != 0 || config.WorkspaceConcurrency != 0 || config.WorkspaceQueueSize != 0 {
		t.Fatal("missing settings must defer to joint Config defaults")
	}
	for _, setting := range []struct {
		name  string
		limit int
	}{
		{"CHAT_WORKERS", 64}, {"CHAT_QUEUE_SIZE", 4096}, {"CHAT_WORKSPACE_CONCURRENCY", 64},
		{"CHAT_MODEL_CONCURRENCY", 64}, {"CHAT_WORKSPACE_QUEUE_SIZE", 4096},
	} {
		for _, raw := range []string{"", "0", "-1", "+1", "1.0", " 1", "1 ", "１", "999999999999999999999", fmt.Sprint(setting.limit + 1)} {
			if _, err := chatSchedulingFromEnv(func(key string) (string, bool) { return raw, key == setting.name }); err == nil {
				t.Fatalf("accepted invalid setting %s", setting.name)
			}
		}
		for _, raw := range []string{"1", fmt.Sprint(setting.limit)} {
			if _, err := chatSchedulingFromEnv(func(key string) (string, bool) { return raw, key == setting.name }); err != nil {
				t.Fatalf("rejected supported boundary %s: %v", setting.name, err)
			}
		}
	}
	values := map[string]string{"CHAT_WORKERS": "7", "CHAT_QUEUE_SIZE": "101", "CHAT_WORKSPACE_CONCURRENCY": "3", "CHAT_MODEL_CONCURRENCY": "5", "CHAT_WORKSPACE_QUEUE_SIZE": "13"}
	config, err = chatSchedulingFromEnv(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
	if err != nil || config.Workers != 7 || config.QueueSize != 101 || config.WorkspaceConcurrency != 3 || config.ModelConcurrency != 5 || config.WorkspaceQueueSize != 13 {
		t.Fatal("execution setting mapping changed")
	}
	_, err = chatSchedulingFromEnv(func(key string) (string, bool) { return "secret-misconfigured-value", key == "CHAT_WORKERS" })
	if err == nil || strings.Contains(err.Error(), "secret-misconfigured-value") {
		t.Fatal("configuration error leaked raw value")
	}
}

func TestChatRunTimeoutEnvironment(t *testing.T) {
	parse := func(raw string, present bool) (time.Duration, error) {
		return chatRunTimeoutFromEnv(func(name string) (string, bool) {
			if name != "CHAT_RUN_TIMEOUT_SECONDS" {
				t.Fatalf("unexpected setting %q", name)
			}
			return raw, present
		})
	}
	if v, err := parse("", false); err != nil || v != 0 {
		t.Fatal("unset deadline must preserve the application default", v, err)
	}
	for _, seconds := range []int{1, 600, 1860, 2100, 3600} {
		v, err := parse(fmt.Sprint(seconds), true)
		if err != nil || v != time.Duration(seconds)*time.Second {
			t.Fatalf("valid deadline %d: %v, %v", seconds, v, err)
		}
	}
	for _, raw := range []string{"", "0", "-1", "+1", "0x10", "１", "1.5", "1_000", "1e3", "35m", " 60", "60 ", "3601", "100000000000000000000", "private-value"} {
		v, err := parse(raw, true)
		if err == nil || v != 0 || strings.Contains(err.Error(), "private-value") {
			t.Fatal("invalid deadline was accepted or echoed")
		}
	}
}
