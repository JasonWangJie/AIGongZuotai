package main

import "testing"

func TestLargeQueryBudgetEnvironment040(t *testing.T) {
	for _, tc := range []struct {
		key string
		max int
	}{
		{"MEDIA_QUERY_WORKERS", 1536}, {"MEDIA_QUERY_WORKSPACE_CONCURRENCY", 1536}, {"MEDIA_QUERY_CHANNEL_CONCURRENCY", 1536},
	} {
		lookup := func(raw string) func(string) (string, bool) {
			return func(k string) (string, bool) { return raw, k == tc.key }
		}
		for _, raw := range []string{"1537", "-1", "1.5", "+256", " 256", "0001", "99999", "５１２"} {
			if _, err := mediaSchedulingFromEnv(lookup(raw)); err == nil {
				t.Fatal("accepted out-of-range query configuration", tc.key)
			}
		}
		c, err := mediaSchedulingFromEnv(lookup("1536"))
		if err != nil || c.QueryWorkers+c.QueryWorkspaceConcurrency+c.QueryChannelConcurrency != tc.max {
			t.Fatal("query capacity did not map independently", tc.key, err)
		}
		c, err = mediaSchedulingFromEnv(lookup("33"))
		if err != nil || c.QueryWorkers+c.QueryWorkspaceConcurrency+c.QueryChannelConcurrency != 33 {
			t.Fatal("33-query opt-in rejected", tc.key, err)
		}
	}
}
