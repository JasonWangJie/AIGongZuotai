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
	if !setCount || !setSlots || countErr != nil || slotsErr != nil || strconv.Itoa(n) != count || strconv.Itoa(k) != slots ||
		n < 1 || n > 4 || k < 1 || k > 64 {
		return studio.ChatChildConfig{}, errors.New("set CHAT_CHILD_PROCESSES (1-4) and CHAT_CHILD_SLOTS (1-64) together, or omit both")
	}
	return studio.ChatChildConfig{Processes: n, SlotsPerProcess: k}, nil
}
