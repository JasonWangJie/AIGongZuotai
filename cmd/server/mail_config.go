package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"aigongzuotai/internal/studio"
)

// Secrets are passed directly to the mail adapter. Validation messages name
// only the setting; malformed credentials must never appear in startup logs.
func mailFromEnv(lookup func(string) (string, bool)) (studio.MailConfig, error) {
	get := func(name string) string { value, _ := lookup(name); return value }
	c := studio.MailConfig{
		Host: get("AUTH_MAIL_HOST"), TLSMode: get("AUTH_MAIL_TLS_MODE"),
		Username: get("AUTH_MAIL_USERNAME"), Password: get("AUTH_MAIL_PASSWORD"),
		From: get("AUTH_MAIL_FROM"), PublicOrigin: get("AUTH_MAIL_PUBLIC_ORIGIN"),
		OutboxKey: get("AUTH_MAIL_OUTBOX_KEY"),
	}
	if value, present := lookup("AUTH_MAIL_ENABLED"); present {
		if value != "true" && value != "false" {
			return studio.MailConfig{}, errors.New("AUTH_MAIL_ENABLED must be true or false when set")
		}
		c.Enabled = value == "true"
	}
	for _, field := range []struct {
		name     string
		min, max int
		set      func(int)
	}{
		{"AUTH_MAIL_PORT", 1, 65535, func(v int) { c.Port = v }},
		{"AUTH_MAIL_TIMEOUT_SECONDS", 5, 60, func(v int) { c.Timeout = time.Duration(v) * time.Second }},
		{"AUTH_MAIL_WORKERS", 1, 4, func(v int) { c.Workers = v }},
	} {
		if raw, present := lookup(field.name); present {
			decimal := len(raw) > 0 && len(raw) <= 5
			for _, ch := range raw {
				decimal = decimal && ch >= '0' && ch <= '9'
			}
			value, err := strconv.Atoi(raw)
			if !decimal || err != nil || value < field.min || value > field.max {
				return studio.MailConfig{}, fmt.Errorf("%s must be decimal between %d and %d", field.name, field.min, field.max)
			}
			field.set(value)
		}
	}
	return c, nil
}
