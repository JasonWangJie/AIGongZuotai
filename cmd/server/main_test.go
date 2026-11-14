package main

import "testing"

func TestModerationEnvironmentStrict(t *testing.T) {
	for _, value := range []string{"", "true", "RULES", " http", "disabled ", "custom"} {
		_, err := moderationFromEnv(func(key string) (string, bool) { return value, key == "MODERATION_MODE" })
		if err == nil {
			t.Fatal("invalid mode accepted")
		}
	}
	config, err := moderationFromEnv(func(string) (string, bool) { return "", false })
	if err != nil || config.Mode != "disabled" {
		t.Fatal("missing mode must disable moderation")
	}
	values := map[string]string{"MODERATION_MODE": "http", "MODERATION_POLICY_FILE": "private-policy.json", "MODERATION_ENDPOINT": "https://review.example.invalid/evaluate", "MODERATION_API_KEY": "fixture-not-a-real-key", "MODERATION_CREDENTIAL_ID": "fixture-v1"}
	config, err = moderationFromEnv(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil || config.Mode != "http" || config.PolicyFile != values["MODERATION_POLICY_FILE"] || config.Endpoint != values["MODERATION_ENDPOINT"] || config.APIKey != values["MODERATION_API_KEY"] || config.CredentialID != values["MODERATION_CREDENTIAL_ID"] {
		t.Fatal("moderation environment mapping changed")
	}
}

func TestOperationsEnvironmentStrict(t *testing.T) {
	for _, tc := range []struct {
		raw                    string
		present, want, invalid bool
	}{{"", false, false, false}, {"false", true, false, false}, {"true", true, true, false}, {"", true, false, true}, {"TRUE", true, false, true}, {"1", true, false, true}, {" true", true, false, true}} {
		value, err := operationsFromEnv(func(string) (string, bool) { return tc.raw, tc.present })
		if value != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("value=%q present=%t: value=%t err=%v", tc.raw, tc.present, value, err)
		}
	}
}

func TestAdmissionEnvironmentStrictValues(t *testing.T) {
	for _, name := range []string{"ADMISSION_WORKSPACE_RUNS", "ADMISSION_MODEL_RUNS", "ADMISSION_WORKSPACE_TASKS", "ADMISSION_MODEL_TASKS", "ADMISSION_WORKSPACE_STREAMS"} {
		for _, raw := range []string{"", "0", "-1", "1.5", "abc", "4097", "9999999999999999999999999"} {
			t.Run(name+"/"+raw, func(t *testing.T) {
				_, err := admissionFromEnv(func(key string) (string, bool) { return raw, key == name })
				if err == nil {
					t.Fatal("invalid explicit environment value accepted")
				}
			})
		}
	}
	limits, err := admissionFromEnv(func(string) (string, bool) { return "", false })
	if err != nil || limits.WorkspaceRuns != 0 {
		t.Fatal("missing environment must leave defaults for Config")
	}
	limits, err = admissionFromEnv(func(string) (string, bool) { return "1", true })
	if err != nil || limits.ModelTasks != 1 || limits.WorkspaceStreams != 1 {
		t.Fatal("valid lower bound rejected")
	}
	_, err = admissionFromEnv(func(key string) (string, bool) { return "129", key == "ADMISSION_WORKSPACE_STREAMS" })
	if err == nil {
		t.Fatal("stream ceiling ignored")
	}
}
