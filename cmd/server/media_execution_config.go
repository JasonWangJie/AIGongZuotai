package main

import (
	"fmt"
	"strconv"

	"aigongzuotai/internal/studio"
)

func mediaSchedulingFromEnv(lookup func(string) (string, bool)) (studio.MediaSchedulingConfig, error) {
	var config studio.MediaSchedulingConfig
	for _, setting := range []struct {
		name  string
		value *int
		max   int
	}{
		{"MEDIA_SUBMIT_WORKERS", &config.SubmitWorkers, 32},
		{"MEDIA_QUERY_WORKERS", &config.QueryWorkers, 32},
		{"MEDIA_DELIVERY_WORKERS", &config.DeliveryWorkers, 16},
		{"MEDIA_REVIEW_WORKERS", &config.ReviewWorkers, 2},
		{"MEDIA_WORKSPACE_CONCURRENCY", &config.WorkspaceConcurrency, 32},
		{"MEDIA_CHANNEL_CONCURRENCY", &config.ChannelConcurrency, 32},
	} {
		raw, present := lookup(setting.name)
		if !present {
			continue
		}
		valid := len(raw) > 0 && len(raw) <= 2
		for _, ch := range raw {
			valid = valid && ch >= '0' && ch <= '9'
		}
		value, err := strconv.Atoi(raw)
		if !valid || err != nil || value < 1 || value > setting.max {
			return studio.MediaSchedulingConfig{}, fmt.Errorf("%s must be decimal digits between 1 and %d", setting.name, setting.max)
		}
		*setting.value = value
	}
	return config, nil
}
