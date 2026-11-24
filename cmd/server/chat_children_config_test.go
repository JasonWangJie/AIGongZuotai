package main

import "testing"

func TestChatChildrenFromEnv(t *testing.T) {
	for _, tc := range []struct {
		values map[string]string
		valid  bool
		n, k   int
	}{
		{map[string]string{}, true, 0, 0},
		{map[string]string{"CHAT_CHILD_PROCESSES": "2", "CHAT_CHILD_SLOTS": "40"}, true, 2, 40},
		{map[string]string{"CHAT_CHILD_PROCESSES": "2"}, false, 0, 0},
		{map[string]string{"CHAT_CHILD_SLOTS": "40"}, false, 0, 0},
		{map[string]string{"CHAT_CHILD_PROCESSES": "0", "CHAT_CHILD_SLOTS": "0"}, false, 0, 0},
		{map[string]string{"CHAT_CHILD_PROCESSES": "5", "CHAT_CHILD_SLOTS": "40"}, true, 5, 40},
		{map[string]string{"CHAT_CHILD_PROCESSES": "8", "CHAT_CHILD_SLOTS": "64"}, true, 8, 64},
		{map[string]string{"CHAT_CHILD_PROCESSES": "9", "CHAT_CHILD_SLOTS": "64"}, true, 9, 64},
		{map[string]string{"CHAT_CHILD_PROCESSES": "16", "CHAT_CHILD_SLOTS": "64"}, true, 16, 64},
		{map[string]string{"CHAT_CHILD_PROCESSES": "17", "CHAT_CHILD_SLOTS": "1"}, false, 0, 0},
		{map[string]string{"CHAT_CHILD_PROCESSES": "2", "CHAT_CHILD_SLOTS": "65"}, false, 0, 0},
		{map[string]string{"CHAT_CHILD_PROCESSES": "+2", "CHAT_CHILD_SLOTS": "40"}, false, 0, 0},
		{map[string]string{"CHAT_CHILD_PROCESSES": "2", "CHAT_CHILD_SLOTS": "04"}, false, 0, 0},
	} {
		out, err := chatChildrenFromEnv(func(key string) (string, bool) { v, ok := tc.values[key]; return v, ok })
		if (err == nil) != tc.valid || out.Processes != tc.n || out.SlotsPerProcess != tc.k {
			t.Fatal("configuration parser returned an unexpected topology")
		}
	}
}
