package main

import (
	"strings"
	"testing"
	"time"
)

func TestMailEnvironmentRejectsMalformedSettingsWithoutLeakingSecrets(t *testing.T) {
	for _, field := range []struct {
		name    string
		invalid []string
	}{
		{"AUTH_MAIL_ENABLED", []string{"", "yes", "1", "TRUE", "secret-value"}},
		{"AUTH_MAIL_PORT", []string{"", "0", "65536", "1.5", "+465", " 465", "secret-value"}},
		{"AUTH_MAIL_TIMEOUT_SECONDS", []string{"", "4", "61", "1e1", "secret-value"}},
		{"AUTH_MAIL_WORKERS", []string{"", "0", "5", "-1", "secret-value"}},
	} {
		for _, value := range field.invalid {
			_, err := mailFromEnv(func(key string) (string, bool) { return value, key == field.name })
			if err == nil {
				t.Fatalf("%s accepted invalid setting", field.name)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("configuration value leaked")
			}
		}
	}
}

func TestMailEnvironmentIsExplicitAndPreservesCredentials(t *testing.T) {
	c, err := mailFromEnv(func(string) (string, bool) { return "", false })
	if err != nil || c.Enabled || c.Port != 0 || c.Timeout != 0 {
		t.Fatal("missing settings must leave mail disabled with adapter defaults")
	}
	values := map[string]string{"AUTH_MAIL_ENABLED": "true", "AUTH_MAIL_HOST": "smtp.example.invalid", "AUTH_MAIL_PORT": "465", "AUTH_MAIL_TLS_MODE": "implicit_tls", "AUTH_MAIL_USERNAME": "local-user", "AUTH_MAIL_PASSWORD": " unchanged secret ", "AUTH_MAIL_FROM": "studio@example.invalid", "AUTH_MAIL_PUBLIC_ORIGIN": "https://studio.example.invalid", "AUTH_MAIL_OUTBOX_KEY": "key-is-validated-by-adapter", "AUTH_MAIL_TIMEOUT_SECONDS": "20", "AUTH_MAIL_WORKERS": "2"}
	c, err = mailFromEnv(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil || !c.Enabled || c.Port != 465 || c.Timeout != 20*time.Second || c.Workers != 2 || c.Password != values["AUTH_MAIL_PASSWORD"] || c.OutboxKey != values["AUTH_MAIL_OUTBOX_KEY"] {
		t.Fatal("mail environment mapping changed")
	}
}
