package studio

import (
	"encoding/base64"
	"errors"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MailConfig is server-only. No credential, encrypted payload, or recipient is
// exposed by the public capabilities or included in application access logs.
type MailConfig struct {
	Enabled                                                    bool
	Host                                                       string
	Port                                                       int
	TLSMode, Username, Password, From, PublicOrigin, OutboxKey string
	Timeout                                                    time.Duration
	Workers                                                    int
}

func (c MailConfig) normalized() MailConfig {
	if c.Timeout == 0 {
		c.Timeout = 20 * time.Second
	}
	if c.Workers == 0 {
		c.Workers = 1
	}
	return c
}

func validateMailConfig(c MailConfig) error {
	if !c.Enabled {
		return nil
	}
	c = c.normalized()
	bad := func() error {
		return errors.New("invalid mail configuration; verify server-only SMTP, TLS, origin and independent outbox key settings")
	}
	if !safeMailHost(c.Host) || c.Port < 1 || c.Port > 65535 || (c.TLSMode != "implicit_tls" && c.TLSMode != "starttls") || c.Timeout < 5*time.Second || c.Timeout > time.Minute || c.Workers < 1 || c.Workers > 4 {
		return bad()
	}
	if len(c.Username) == 0 || len(c.Username) > 320 || len(c.Password) == 0 || len(c.Password) > 4096 || hasMailControl(c.Username) || hasMailControl(c.Password) {
		return bad()
	}
	if _, err := strictMailbox(c.From); err != nil {
		return bad()
	}
	if _, err := mailOrigin(c.PublicOrigin); err != nil {
		return bad()
	}
	key, err := base64.StdEncoding.Strict().DecodeString(c.OutboxKey)
	if err != nil || len(key) != 32 || c.OutboxKey == c.Password || c.OutboxKey == c.Username || string(key) == c.Password {
		return bad()
	}
	return nil
}

func hasMailControl(v string) bool {
	for _, r := range v {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

func safeMailHost(v string) bool {
	if v == "" || len(v) > 253 || hasMailControl(v) || strings.TrimSpace(v) != v {
		return false
	}
	if net.ParseIP(v) != nil {
		return true
	}
	for _, p := range strings.Split(v, ".") {
		if p == "" || len(p) > 63 || p[0] == '-' || p[len(p)-1] == '-' {
			return false
		}
		for _, c := range p {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func strictMailbox(v string) (string, error) {
	if len(v) > 254 || hasMailControl(v) || strings.TrimSpace(v) != v {
		return "", errors.New("invalid mail address")
	}
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || a.Name != "" {
		return "", errors.New("invalid mail address")
	}
	for _, r := range v {
		if r > 126 || r < 33 {
			return "", errors.New("unsupported mail address")
		}
	}
	return a.Address, nil
}

func mailOrigin(v string) (*url.URL, error) {
	u, err := url.Parse(v)
	if err != nil || len(v) > 512 || hasMailControl(v) || u == nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || !safeMailHost(u.Hostname()) {
		return nil, errors.New("invalid mail public origin")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid mail public origin")
		}
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("mail public origin requires HTTPS")
	}
	u.Path = "/"
	return u, nil
}
