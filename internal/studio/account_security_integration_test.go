package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// This test uses the real HTTP handlers, PostgreSQL, encrypted queue and SMTP
// sender over a loopback TLS connection. Only the fixture CA is injected; no
// production certificate-verification bypass or reset-token API is introduced.
func TestPostgresAccountSecurityTLSMailHTTPJourney(t *testing.T) {
	f := newMailSMTPFixture(t, mailSMTPOptions{TLSMode: "implicit_tls"})
	const metricToken = "account-integration-metrics-only-token"
	_, server := testApp(t, Config{DatabaseURL: testDatabase(t), AuthMode: "required", Mail: f.Config, mailSender: f.sender(), MetricsToken: metricToken})
	owner, peer, stranger, anon := authClient(t), authClient(t), authClient(t), authClient(t)
	const email = "journey@example.test"
	const initialPassword = "fixture-password-123"
	const resetPassword = "reset-fixture-password-456"
	const changedPassword = "changed-fixture-password-789"
	identity := registerFixture(t, owner, server.URL, email)
	registerFixture(t, stranger, server.URL, "other-journey@example.test")
	call := func(c *http.Client, method, path string, body any, want int) ([]byte, http.Header) {
		t.Helper()
		status, data, headers := authRequest(t, c, server.URL, method, path, "", "", body)
		if status != want {
			t.Fatalf("security HTTP %s: expected %d, got %d", method, want, status)
		}
		return data, headers
	}
	login := func(c *http.Client, password string, status int) {
		t.Helper()
		call(c, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, status)
	}
	mailToken := func(purpose string) string {
		t.Helper()
		select {
		case raw := <-f.Messages:
			body := mailFixtureBody(t, raw)
			for _, word := range strings.Fields(body) {
				if !strings.HasPrefix(word, "http://127.0.0.1:5173/#") {
					continue
				}
				u, err := url.Parse(word)
				if err != nil {
					t.Fatal("fixture security URL invalid")
				}
				q, err := url.ParseQuery(u.Fragment)
				if err != nil {
					t.Fatal("fixture security fragment invalid")
				}
				if q.Get("auth") != purpose || len(q.Get("token")) != 43 || u.RawQuery != "" {
					t.Fatal("wrong mail purpose or token placement")
				}
				return q.Get("token")
			}
			t.Fatal("fixture mail lacks security link")
		case <-time.After(5 * time.Second):
			t.Fatal("local SMTP fixture did not receive queued mail")
		}
		return ""
	}

	data, _ := call(anon, "GET", "/api/v1/auth/session", nil, 200)
	var capabilities struct {
		User     *User                       `json:"user"`
		Security AccountSecurityCapabilities `json:"account_security"`
	}
	if json.Unmarshal(data, &capabilities) != nil || capabilities.User != nil || !capabilities.Security.PasswordResetEnabled {
		t.Fatal("anonymous recovery capability incorrect")
	}
	call(anon, "GET", "/api/v1/auth/sessions", nil, 401)
	call(anon, "GET", "/api/v1/auth/security/events", nil, 401)
	login(peer, initialPassword, 200)
	data, _ = call(owner, "GET", "/api/v1/auth/sessions", nil, 200)
	var sessions struct {
		Items []AccountSession `json:"items"`
	}
	if json.Unmarshal(data, &sessions) != nil || len(sessions.Items) != 2 {
		t.Fatal("expected two independently identified sessions")
	}
	otherSession := ""
	for _, s := range sessions.Items {
		if !s.Current {
			otherSession = s.ID
		}
	}
	if !strings.HasPrefix(otherSession, "ses_") {
		t.Fatal("public session identity missing")
	}
	call(stranger, "POST", "/api/v1/auth/sessions/"+otherSession+"/revoke", map[string]any{}, 404)
	call(owner, "POST", "/api/v1/auth/sessions/"+otherSession+"/revoke", map[string]any{}, 200)
	call(peer, "GET", "/api/v1/bootstrap", nil, 401)
	login(peer, initialPassword, 200)

	requestBody, _ := call(owner, "POST", "/api/v1/auth/email/verification/request", map[string]any{}, 202)
	verifyToken := mailToken("verify")
	if bytes.Contains(requestBody, []byte(verifyToken)) || bytes.Contains(requestBody, []byte(email)) {
		t.Fatal("mail request leaked private delivery data")
	}
	_, headers := call(anon, "POST", "/api/v1/auth/email/verification/confirm", map[string]string{"token": verifyToken}, 200)
	if headers.Get("Set-Cookie") != "" {
		t.Fatal("verification unexpectedly created a login")
	}
	call(anon, "POST", "/api/v1/auth/email/verification/confirm", map[string]string{"token": verifyToken}, 400)
	data, _ = call(owner, "GET", "/api/v1/auth/session", nil, 200)
	verified := decode[testIdentity](t, data)
	if !verified.User.EmailVerified || verified.User.ID != identity.User.ID {
		t.Fatal("verification changed identity or did not persist")
	}

	unknownBody, _ := call(anon, "POST", "/api/v1/auth/password/reset/request", map[string]string{"email": "unknown-journey@example.test"}, 202)
	knownBody, _ := call(anon, "POST", "/api/v1/auth/password/reset/request", map[string]string{"email": email}, 202)
	if !bytes.Equal(unknownBody, knownBody) {
		t.Fatal("recovery response reveals whether an account exists")
	}
	resetToken := mailToken("reset")
	if f.DataCalls.Load() != 2 {
		t.Fatal("unknown account request unexpectedly sent mail")
	}
	call(anon, "POST", "/api/v1/auth/email/verification/confirm", map[string]string{"token": resetToken}, 400)
	call(anon, "POST", "/api/v1/auth/password/reset/confirm", map[string]string{"token": resetToken, "new_password": resetPassword}, 200)
	call(owner, "GET", "/api/v1/bootstrap", nil, 401)
	call(peer, "GET", "/api/v1/bootstrap", nil, 401)
	call(anon, "GET", "/api/v1/bootstrap", nil, 401)
	call(anon, "POST", "/api/v1/auth/password/reset/confirm", map[string]string{"token": resetToken, "new_password": resetPassword}, 400)
	login(owner, initialPassword, 401)
	login(owner, resetPassword, 200)
	login(peer, resetPassword, 200)
	call(owner, "POST", "/api/v1/auth/password/change", map[string]string{"current_password": resetPassword, "new_password": changedPassword}, 200)
	call(owner, "GET", "/api/v1/bootstrap", nil, 401)
	call(peer, "GET", "/api/v1/bootstrap", nil, 401)
	login(owner, resetPassword, 401)
	login(owner, changedPassword, 200)

	var events struct {
		Items []SecurityEvent `json:"items"`
		Next  *string         `json:"next_cursor"`
	}
	data, _ = call(owner, "GET", "/api/v1/auth/security/events?limit=2", nil, 200)
	if json.Unmarshal(data, &events) != nil || len(events.Items) != 2 || events.Next == nil {
		t.Fatal("security event keyset page missing")
	}
	firstID := events.Items[0].ID
	pagePath := "/api/v1/auth/security/events?limit=2&cursor=" + url.QueryEscape(*events.Next)
	data, _ = call(owner, "GET", pagePath, nil, 200)
	if json.Unmarshal(data, &events) != nil || len(events.Items) != 2 || events.Items[0].ID == firstID {
		t.Fatal("security event cursor did not advance")
	}
	call(stranger, "GET", pagePath, nil, 400)
	data, _ = call(owner, "GET", "/api/v1/auth/security/events?limit=50", nil, 200)
	for _, secret := range []string{email, initialPassword, resetPassword, changedPassword, verifyToken, resetToken, "token_hash", "password_hash"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("security events leaked sensitive values")
		}
	}
	for _, action := range []string{"email.verified", "password.reset", "password.changed", "session.revoked"} {
		if !bytes.Contains(data, []byte(action)) {
			t.Fatalf("missing security action %s", action)
		}
	}
	call(owner, "POST", "/api/v1/auth/sessions/revoke-all", map[string]any{}, 200)
	call(owner, "GET", "/api/v1/bootstrap", nil, 401)
	call(stranger, "GET", "/api/v1/bootstrap", nil, 200)

	req, _ := http.NewRequest("GET", server.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+metricToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("metrics read failed")
	}
	metrics, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatal("metrics unavailable")
	}
	for _, route := range []string{"email_verification_request", "email_verification_confirm", "password_reset_request", "password_reset_confirm", "password_change", "account_sessions", "account_session_revoke", "account_sessions_revoke_all", "account_security_events"} {
		if !bytes.Contains(metrics, []byte(`route="`+route+`"`)) {
			t.Fatalf("missing fixed security route: %s", route)
		}
	}
	for _, secret := range []string{email, verifyToken, resetToken, initialPassword, resetPassword, changedPassword, otherSession, metricToken} {
		if bytes.Contains(metrics, []byte(secret)) {
			t.Fatal("metrics leaked sensitive values")
		}
	}
}
