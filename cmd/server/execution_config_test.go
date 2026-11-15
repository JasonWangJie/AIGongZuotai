package main

import (
	"fmt"
	"strings"
	"testing"
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
