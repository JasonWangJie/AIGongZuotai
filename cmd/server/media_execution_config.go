package main

import (
	"fmt"
	"strconv"

	"aigongzuotai/internal/studio"
)

func mediaSchedulingFromEnv(lookup func(string) (string, bool)) (studio.MediaSchedulingConfig, error) {
	var config studio.MediaSchedulingConfig
	for _, setting := range []struct {
		name     string
		value    *int
		min, max int
	}{
		{"MEDIA_SUBMIT_WORKERS", &config.SubmitWorkers, 1, 32},
		{"MEDIA_QUERY_WORKERS", &config.QueryWorkers, 1, studio.MaxMediaQueryWorkers},
		{"MEDIA_QUERY_WORKSPACE_CONCURRENCY", &config.QueryWorkspaceConcurrency, 0, studio.MaxMediaQueryWorkers},
		{"MEDIA_QUERY_CHANNEL_CONCURRENCY", &config.QueryChannelConcurrency, 0, studio.MaxMediaQueryWorkers},
		{"MEDIA_QUERY_DB_CONNECTIONS", &config.QueryDBConnections, 0, 32},
		{"MEDIA_DELIVERY_WORKERS", &config.DeliveryWorkers, 1, 16},
		{"MEDIA_REVIEW_WORKERS", &config.ReviewWorkers, 1, 2},
		{"MEDIA_WORKSPACE_CONCURRENCY", &config.WorkspaceConcurrency, 1, 32},
		{"MEDIA_CHANNEL_CONCURRENCY", &config.ChannelConcurrency, 1, 32},
	} {
		raw, present := lookup(setting.name)
		if !present {
			continue
		}
		valid := len(raw) > 0 && len(raw) <= max(2, len(strconv.Itoa(setting.max))) && (len(raw) <= 2 || raw[0] != '0')
		for _, ch := range raw {
			valid = valid && ch >= '0' && ch <= '9'
		}
		value, err := strconv.Atoi(raw)
		if !valid || err != nil || value < setting.min || value > setting.max {
			return studio.MediaSchedulingConfig{}, fmt.Errorf("%s must be decimal digits between %d and %d", setting.name, setting.min, setting.max)
		}
		*setting.value = value
	}
	return config, nil
}
