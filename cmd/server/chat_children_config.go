package main

import (
	"errors"
	"strconv"

	"aigongzuotai/internal/studio"
)

func chatChildrenFromEnv(lookup func(string) (string, bool)) (studio.ChatChildConfig, error) {
	count, setCount := lookup("CHAT_CHILD_PROCESSES")
	slots, setSlots := lookup("CHAT_CHILD_SLOTS")
	if !setCount && !setSlots {
		return studio.ChatChildConfig{}, nil
	}
	n, countErr := strconv.Atoi(count)
	k, slotsErr := strconv.Atoi(slots)
	if !setCount || !setSlots || countErr != nil || slotsErr != nil || strconv.Itoa(n) != count || strconv.Itoa(k) != slots {
		return studio.ChatChildConfig{}, errors.New("set CHAT_CHILD_PROCESSES and CHAT_CHILD_SLOTS together as decimal integers, or omit both")
	}
	cfg := studio.ChatChildConfig{Processes: n, SlotsPerProcess: k}
	if err := cfg.ValidateTopology(); err != nil {
		return studio.ChatChildConfig{}, err
	}
	return cfg, nil
}
