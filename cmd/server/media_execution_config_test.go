package main

import (
	"strings"
	"testing"
)

func TestMediaExecutionEnvironmentStrictAndIndependent(t *testing.T) {
	config, err := mediaSchedulingFromEnv(func(string) (string, bool) { return "", false })
	if err != nil || config.SubmitWorkers != 0 || config.QueryWorkers != 0 || config.DeliveryWorkers != 0 || config.ReviewWorkers != 0 || config.WorkspaceConcurrency != 0 || config.ChannelConcurrency != 0 {
		t.Fatal("missing values must defer to Config defaults")
	}
	for _, raw := range []string{"", "0", "17", "32", "-1", "+1", " 2", "２", "2.0", "10000"} {
		if _, err := mediaSchedulingFromEnv(func(key string) (string, bool) { return raw, key == "MEDIA_DELIVERY_WORKERS" }); err == nil {
			t.Fatal("invalid delivery ceiling accepted")
		}
	}
	for _, raw := range []string{"1", "16"} {
		if _, err := mediaSchedulingFromEnv(func(key string) (string, bool) { return raw, key == "MEDIA_DELIVERY_WORKERS" }); err != nil {
			t.Fatal("valid delivery ceiling rejected", err)
		}
	}
	for _, name := range []string{"MEDIA_SUBMIT_WORKERS", "MEDIA_WORKSPACE_CONCURRENCY", "MEDIA_CHANNEL_CONCURRENCY"} {
		for _, raw := range []string{"", "0", "-1", "+1", " 1", "1 ", "1.5", "１", "33", "10000000000"} {
			if _, err := mediaSchedulingFromEnv(func(key string) (string, bool) { return raw, key == name }); err == nil {
				t.Fatalf("invalid %s accepted", name)
			}
		}
		for _, raw := range []string{"1", "32"} {
			if _, err := mediaSchedulingFromEnv(func(key string) (string, bool) { return raw, key == name }); err != nil {
				t.Fatalf("valid %s rejected: %v", name, err)
			}
		}
	}
	for _, raw := range []string{"", "0", "3", "16", "32", "-1", "+1", " 2", "2 ", "２", "2.0", "10000"} {
		if _, err := mediaSchedulingFromEnv(func(key string) (string, bool) { return raw, key == "MEDIA_REVIEW_WORKERS" }); err == nil {
			t.Fatal("invalid review ceiling accepted")
		}
	}
	for _, raw := range []string{"1", "2"} {
		config, err := mediaSchedulingFromEnv(func(key string) (string, bool) { return raw, key == "MEDIA_REVIEW_WORKERS" })
		if err != nil || config.ReviewWorkers < 1 || config.ReviewWorkers > 2 || config.DeliveryWorkers != 0 {
			t.Fatal("valid independent review ceiling rejected", err)
		}
	}
	values := map[string]string{"MEDIA_SUBMIT_WORKERS": "3", "MEDIA_QUERY_WORKERS": "7", "MEDIA_DELIVERY_WORKERS": "5", "MEDIA_REVIEW_WORKERS": "1", "MEDIA_WORKSPACE_CONCURRENCY": "2", "MEDIA_CHANNEL_CONCURRENCY": "4", "CHAT_WORKERS": "64", "MEDIA_MODERATION_MODE": "fixtures"}
	config, err = mediaSchedulingFromEnv(func(key string) (string, bool) { v, ok := values[key]; return v, ok })
	if err != nil || config.SubmitWorkers != 3 || config.QueryWorkers != 7 || config.DeliveryWorkers != 5 || config.ReviewWorkers != 1 || config.WorkspaceConcurrency != 2 || config.ChannelConcurrency != 4 {
		t.Fatal("lane mapping is not independent")
	}
	for _, name := range []string{"MEDIA_SUBMIT_WORKERS", "MEDIA_QUERY_WORKERS", "MEDIA_DELIVERY_WORKERS", "MEDIA_REVIEW_WORKERS", "MEDIA_WORKSPACE_CONCURRENCY", "MEDIA_CHANNEL_CONCURRENCY"} {
		_, err = mediaSchedulingFromEnv(func(key string) (string, bool) { return "private-config-mistake", key == name })
		if err == nil || strings.Contains(err.Error(), "private-config-mistake") {
			t.Fatal("raw configuration exposed")
		}
	}
}
