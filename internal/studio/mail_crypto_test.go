package studio

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestMailConfigurationFailsClosed(t *testing.T) {
	c := MailConfig{Enabled: true, Host: "smtp.example.test", Port: 465, TLSMode: "implicit_tls", Username: "fixture", Password: "fixture-pass", From: "security@example.test", PublicOrigin: "https://studio.example.test", OutboxKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))}
	if err := validateMailConfig(c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*MailConfig)
	}{
		{"plain", func(c *MailConfig) { c.TLSMode = "plain" }},
		{"host-injection", func(c *MailConfig) { c.Host = "smtp.test\r\nAUTH" }},
		{"from-injection", func(c *MailConfig) { c.From = "sender@test\r\nBcc:other@test" }},
		{"origin-userinfo", func(c *MailConfig) { c.PublicOrigin = "https://user:pass@example.test" }},
		{"origin-query", func(c *MailConfig) { c.PublicOrigin = "https://example.test/?redirect=evil" }},
		{"origin-path", func(c *MailConfig) { c.PublicOrigin = "https://example.test/redirect" }},
		{"http-public", func(c *MailConfig) { c.PublicOrigin = "http://example.test" }},
		{"key-reuse", func(c *MailConfig) { c.Password = c.OutboxKey }},
		{"bad-key", func(c *MailConfig) { c.OutboxKey = "short" }},
		{"workers", func(c *MailConfig) { c.Workers = 5 }},
		{"timeout", func(c *MailConfig) { c.Timeout = 61 * time.Second }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := c
			tc.edit(&v)
			err := validateMailConfig(v)
			if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), v.Password) || strings.Contains(err.Error(), v.OutboxKey) {
				t.Fatal("configuration error leaked credentials")
			}
		})
	}
	c.Enabled = false
	c.Password = ""
	if err := validateMailConfig(c); err != nil {
		t.Fatal("disabled mail should not require credentials")
	}
}

func TestMailCipherBindsIdentityAndFreezesTrustedOrigin(t *testing.T) {
	f := newMailSMTPFixture(t, mailSMTPOptions{})
	crypt, err := newMailCipher(f.Config.OutboxKey)
	if err != nil {
		t.Fatal(err)
	}
	m := &mailDelivery{config: f.Config, cipher: crypt}
	e := testMailEnvelope()
	item, err := m.seal(e)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(item.Ciphertext, []byte(e.Token)) || bytes.Contains(item.Ciphertext, []byte(e.To)) {
		t.Fatal("plaintext secret persisted")
	}
	m.config.PublicOrigin = "https://changed.example.test"
	p, err := crypt.open(item)
	if err != nil {
		t.Fatal(err)
	}
	if p.Origin != f.Config.PublicOrigin || p.Token != e.Token || p.To != e.To {
		t.Fatal("encrypted envelope did not preserve original trusted inputs")
	}
	for _, tc := range []struct {
		name string
		edit func(*encryptedMail)
	}{
		{"id", func(v *encryptedMail) { v.ID += "2" }},
		{"purpose", func(v *encryptedMail) { v.Purpose = "verify_email" }},
		{"expiry", func(v *encryptedMail) { v.ExpiresAt = v.ExpiresAt.Add(time.Microsecond) }},
		{"ciphertext", func(v *encryptedMail) {
			v.Ciphertext = append([]byte(nil), v.Ciphertext...)
			v.Ciphertext[len(v.Ciphertext)-1] ^= 1
		}},
		{"key", func(v *encryptedMail) { v.KeyID = strings.Repeat("0", 32) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := item
			tc.edit(&v)
			_, err := crypt.open(v)
			if err == nil {
				t.Fatal("swapped encrypted metadata accepted")
			}
			if strings.Contains(err.Error(), e.Token) || strings.Contains(err.Error(), e.To) {
				t.Fatal("encryption error leaked envelope")
			}
		})
	}
}

func TestMailRenderingBoundsAndHeaderInjection(t *testing.T) {
	f := newMailSMTPFixture(t, mailSMTPOptions{})
	item, p := testMailSealed(t, f.Config)
	for _, edit := range []func(*mailPayload){func(p *mailPayload) { p.To = "a@test\r\nBcc:x@test" }, func(p *mailPayload) { p.Token = strings.Repeat("x", 65<<10) }, func(p *mailPayload) { p.Origin = "https://evil.test/?redirect=1" }} {
		bad := p
		edit(&bad)
		if _, err := renderSecurityMail(item, bad); err == nil {
			t.Fatal("unsafe mail payload accepted")
		}
	}
	first, err := renderSecurityMail(item, p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderSecurityMail(item, p)
	if err != nil {
		t.Fatal(err)
	}
	a, err := mail.ReadMessage(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	b, err := mail.ReadMessage(bytes.NewReader(second))
	if err != nil {
		t.Fatal(err)
	}
	if a.Header.Get("Message-ID") == "" || a.Header.Get("Message-ID") != b.Header.Get("Message-ID") {
		t.Fatal("safe retry changed Message-ID")
	}
	for _, r := range first {
		if r > 127 {
			t.Fatal("non-ASCII SMTP wire body bypassed MIME encoding")
		}
	}
	_, _ = io.Copy(io.Discard, a.Body)
}
