package main

import "testing"

func TestRunReplayReadConnectionsEnv(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		present bool
		want    int
		ok      bool
	}{
		{"", false, 0, true}, {"0", true, 0, true}, {"1", true, 1, true}, {"4", true, 4, true},
		{"", true, 0, false}, {"-1", true, 0, false}, {"5", true, 0, false},
		{"01", true, 0, false}, {" 2", true, 0, false}, {"true", true, 0, false},
	} {
		got, err := replayConnectionsFromEnv(func(key string) (string, bool) {
			if key != "RUN_REPLAY_READ_CONNECTIONS" {
				t.Fatalf("unexpected key %s", key)
			}
			return tc.raw, tc.present
		})
		if (err == nil) != tc.ok || got != tc.want {
			t.Fatalf("%q present=%t: got %d, error=%v", tc.raw, tc.present, got, err)
		}
	}
}
