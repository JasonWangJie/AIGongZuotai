package main

import "testing"

func TestMediaQueryDBConnectionEnvironment(t *testing.T) {
	for raw, want := range map[string]int{"0": 0, "4": 4, "32": 32} {
		c, err := mediaSchedulingFromEnv(func(k string) (string, bool) { return raw, k == "MEDIA_QUERY_DB_CONNECTIONS" })
		if err != nil || c.QueryDBConnections != want {
			t.Fatal("invalid parsed connection budget", err)
		}
	}
	for _, raw := range []string{"-1", " 2", "1.0", "33", "000"} {
		_, err := mediaSchedulingFromEnv(func(k string) (string, bool) { return raw, k == "MEDIA_QUERY_DB_CONNECTIONS" })
		if err == nil {
			t.Fatal("invalid query database connection setting accepted")
		}
	}
}
