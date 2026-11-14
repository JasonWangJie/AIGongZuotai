package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestAccountSecurityTokenPasswordAndAnonymousCapabilities(t *testing.T) {
	raw, hash, err := tokenPair()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := accountTokenHash(raw)
	if err != nil || !bytes.Equal(hash, parsed) {
		t.Fatal("token digest mismatch")
	}
	for _, v := range []string{"", raw + "=", strings.Repeat("a", 42), strings.Repeat("!", 43), strings.Repeat("A", 42) + "B"} {
		if _, err = accountTokenHash(v); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	for _, v := range []string{"short", strings.Repeat("a", 73), "0123456789\x00", string([]byte{0xff}) + "0123456789"} {
		if validAccountPassword(v) {
			t.Fatal("invalid password accepted")
		}
	}
	a, s := testApp(t, Config{})
	_ = a
	code, data, _ := authRequest(t, authClient(t), s.URL, "GET", "/api/v1/auth/session", "", "", nil)
	if code != 200 || !bytes.Contains(data, []byte(`"account_security"`)) || !bytes.Contains(data, []byte(`"mail_configured":false`)) {
		t.Fatal("anonymous capability missing", code, string(data))
	}
	code, _, _ = authRequest(t, authClient(t), s.URL, "GET", "/api/v1/auth/sessions", "", "", nil)
	if code != 409 {
		t.Fatal("disabled security accepted", code)
	}
	if validName("bad\x00name") {
		t.Fatal("NUL name accepted")
	}
	for _, v := range []string{"a b@example.test", "\x00@example.test", "用户@example.test", "Name <a@example.test>"} {
		if _, ok := normalizeDeliveryEmail(v); ok {
			t.Fatal("unsupported mailbox accepted")
		}
	}
	if _, ok := normalizeEmail("用户@example.test"); !ok {
		t.Fatal("legacy UTF-8 login normalization changed")
	}
	for _, prefix := range []string{"ses", "sec"} {
		if !validSecurityID(newID(prefix), prefix) {
			t.Fatal("opaque security id rejected")
		}
	}
	if validSecurityID(newID("conv"), "conv") || validChatID(newID("ses"), "ses") {
		t.Fatal("identifier domains overlap")
	}
}

func TestAccountSecurityRejectsSharedOutboxSecrets(t *testing.T) {
	key := strings.Repeat("z", 32)
	mail := MailConfig{Enabled: true, Host: "localhost", Port: 465, TLSMode: "implicit_tls", Username: "fixture", Password: "fixture-mail-password", From: "service@example.test", PublicOrigin: "http://127.0.0.1:5173", OutboxKey: base64.StdEncoding.EncodeToString([]byte(key))}
	for _, secret := range []string{key, mail.OutboxKey} {
		_, err := newIsolatedApp(t, Config{AuthMode: "required", DatabaseURL: "not-used", Mail: mail, APIKey: secret})
		if err == nil || !strings.Contains(err.Error(), "independent") {
			t.Fatal("shared encryption secret accepted", err)
		}
	}
}

func securityFixture(t *testing.T) (*App, *httptest.Server, *http.Client, testIdentity, string) {
	t.Helper()
	db := testDatabase(t)
	a, err := newIsolatedApp(t, Config{DatabaseURL: db, AuthMode: "required"})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(a.Handler())
	t.Cleanup(func() { s.Close(); a.Close() })
	c := authClient(t)
	id := registerFixture(t, c, s.URL, "account@example.test")
	return a, s, c, id, db
}
func securityToken(t *testing.T, a *App, id, purpose string, expiry time.Time) string {
	t.Helper()
	raw, digest, err := tokenPair()
	if err != nil {
		t.Fatal(err)
	}
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		_, e := c.Exec(ctx, "INSERT INTO studio_account_tokens(token_hash,user_id,purpose,credential_version,expires_at) SELECT $1,id,$3,credential_version,$4 FROM studio_users WHERE id=$2", digest, id, purpose, expiry)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func securityExec(t *testing.T, a *App, sql string, args ...any) {
	t.Helper()
	if err := a.store.do(func(ctx context.Context, c *pgx.Conn) error { _, e := c.Exec(ctx, sql, args...); return e }); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAccountSecuritySessionsChangeAndIsolation(t *testing.T) {
	a, s, alice, id, _ := securityFixture(t)
	bob := authClient(t)
	other := registerFixture(t, bob, s.URL, "other@example.test")
	anonymous := authClient(t)
	for _, path := range []string{"/api/v1/auth/sessions", "/api/v1/auth/security/events"} {
		code, _, _ := authRequest(t, anonymous, s.URL, "GET", path, "", "", nil)
		if code != 401 {
			t.Fatal("private auth route not guarded", path, code)
		}
	}
	code, data, _ := authRequest(t, bob, s.URL, "GET", "/api/v1/auth/sessions", "", "", nil)
	if code != 200 {
		t.Fatal(code, string(data))
	}
	bobSessions := decode[struct {
		Items []AccountSession `json:"items"`
	}](t, data)
	code, _, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/auth/sessions/"+bobSessions.Items[0].ID+"/revoke", "", "", map[string]any{})
	if code != 404 {
		t.Fatal("cross-account revoke", code)
	}
	second := authClient(t)
	code, _, _ = authRequest(t, second, s.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": id.User.Email, "password": "fixture-password-123"})
	if code != 200 {
		t.Fatal("login", code)
	}
	code, data, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/auth/sessions", "", "", nil)
	items := decode[struct {
		Items []AccountSession `json:"items"`
	}](t, data).Items
	if code != 200 || len(items) != 2 {
		t.Fatal("session list", code, string(data))
	}
	var noncurrent string
	for _, v := range items {
		if !v.Current {
			noncurrent = v.ID
		}
		if !validSecurityID(v.ID, "ses") {
			t.Fatal("public id invalid")
		}
	}
	code, _, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/auth/sessions/"+noncurrent+"/revoke", "", "", map[string]any{})
	if code != 200 {
		t.Fatal("revoke", code)
	}
	code, _, _ = authRequest(t, second, s.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("revoked session survived", code)
	}
	code, _, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/auth/password/change", "", "", map[string]string{"current_password": "wrong", "new_password": "replacement-password-123"})
	if code != 401 {
		t.Fatal("wrong password accepted", code)
	}
	code, _, headers := authRequest(t, alice, s.URL, "POST", "/api/v1/auth/password/change", "", "", map[string]string{"current_password": "fixture-password-123", "new_password": "replacement-password-123"})
	if code != 200 || !strings.Contains(headers.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatal("change did not clear cookie", code, headers.Get("Set-Cookie"))
	}
	code, _, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("change remained logged in")
	}
	code, _, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": id.User.Email, "password": "fixture-password-123"})
	if code != 401 {
		t.Fatal("old password login survived", code)
	}
	code, _, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": id.User.Email, "password": "replacement-password-123"})
	if code != 200 {
		t.Fatal("new password failed", code)
	}
	code, data, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/auth/security/events?limit=2", "", "", nil)
	if code != 200 || bytes.Contains(data, []byte(id.User.Email)) || bytes.Contains(data, []byte(other.User.ID)) {
		t.Fatal("audit leakage", code, string(data))
	}
	page := decode[struct {
		Items []SecurityEvent `json:"items"`
		Next  *string         `json:"next_cursor"`
	}](t, data)
	if page.Next == nil || len(page.Items) != 2 {
		t.Fatal("audit pagination missing")
	}
	code, _, _ = authRequest(t, bob, s.URL, "GET", "/api/v1/auth/security/events?cursor="+*page.Next, "", "", nil)
	if code != 400 {
		t.Fatal("foreign audit cursor accepted", code)
	}
	code, _, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/auth/sessions/revoke-all", "", "", map[string]any{})
	if code != 200 {
		t.Fatal("revoke all", code)
	}
	code, _, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("all not revoked", code)
	}
	if a.store.failed.Load() {
		t.Fatal("validation poisoned database")
	}
}

func TestPostgresAccountTokensPurposeExpirySingleUseAndNoAutoLogin(t *testing.T) {
	a, s, alice, id, _ := securityFixture(t)
	anonymous := authClient(t)
	token := securityToken(t, a, id.User.ID, "verify_email", time.Now().Add(time.Hour))
	code, _, _ := authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/password/reset/confirm", "", "", map[string]string{"token": token, "new_password": "replacement-password-123"})
	if code != 400 {
		t.Fatal("purpose confused", code)
	}
	code, _, headers := authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/email/verification/confirm", "", "", map[string]string{"token": token})
	if code != 200 || headers.Get("Set-Cookie") != "" {
		t.Fatal("verification auto login", code)
	}
	code, data, _ := authRequest(t, alice, s.URL, "GET", "/api/v1/auth/session", "", "", nil)
	if code != 200 || !bytes.Contains(data, []byte(`"email_verified":true`)) {
		t.Fatal("not verified", string(data))
	}
	code, _, _ = authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/email/verification/confirm", "", "", map[string]string{"token": token})
	if code != 400 {
		t.Fatal("verify reuse", code)
	}
	expired := securityToken(t, a, id.User.ID, "reset_password", time.Now().Add(-time.Minute))
	code, _, _ = authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/password/reset/confirm", "", "", map[string]string{"token": expired, "new_password": "replacement-password-123"})
	if code != 400 {
		t.Fatal("expired accepted", code)
	}
	reset := securityToken(t, a, id.User.ID, "reset_password", time.Now().Add(time.Hour))
	older := securityToken(t, a, id.User.ID, "reset_password", time.Now().Add(time.Hour))
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, _ := authRequest(t, authClient(t), s.URL, "POST", "/api/v1/auth/password/reset/confirm", "", "", map[string]string{"token": reset, "new_password": "replacement-password-123"})
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[400] != 1 {
		t.Fatal("token not one-use", counts)
	}
	code, _, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("reset retained prior session", code)
	}
	code, _, _ = authRequest(t, anonymous, s.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 401 {
		t.Fatal("reset auto logged in", code)
	}
	code, _, _ = authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/password/reset/confirm", "", "", map[string]string{"token": older, "new_password": "replacement-password-456"})
	if code != 400 {
		t.Fatal("other reset link not invalidated", code)
	}
	if a.store.failed.Load() {
		t.Fatal("token rejection poisoned DB")
	}
}

func TestPostgresAccountLoginPasswordRaceSerialized(t *testing.T) {
	a, _, _, id, db := securityFixture(t)
	ctx := context.Background()
	first, e := pgx.Connect(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close(ctx)
	second, e := pgx.Connect(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close(ctx)
	original, e := readPasswordSnapshot(ctx, first, id.User.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	newHash, e := bcrypt.GenerateFromPassword([]byte("replacement-password-123"), bcrypt.MinCost)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := first.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	locked, e := readPasswordSnapshot(ctx, tx, id.User.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = rotatePasswordSQL(ctx, tx, locked, newHash, "password.changed"); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		tx, e := second.Begin(ctx)
		if e != nil {
			result <- e
			return
		}
		defer tx.Rollback(ctx)
		_, digest, _ := tokenPair()
		e = insertLoginSessionSQL(ctx, tx, id.User.ID, id.Workspace.ID, original.Hash, original.Version, digest)
		if e == nil {
			e = tx.Commit(ctx)
		}
		result <- e
	}()
	select {
	case e := <-result:
		t.Fatal("login did not wait for credential lock", e)
	case <-time.After(80 * time.Millisecond):
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-result
	var operation *operationError
	if !errors.As(e, &operation) || operation.status != 401 {
		t.Fatal("verified old password minted session", e)
	}
	var sessions int
	if e = first.QueryRow(ctx, "SELECT count(*) FROM studio_sessions WHERE user_id=$1", id.User.ID).Scan(&sessions); e != nil || sessions != 0 {
		t.Fatal("old sessions survived", sessions, e)
	}
	// Reverse ordering: a login commits first, then rotation deletes that session.
	current, e := readPasswordSnapshot(ctx, first, id.User.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	tx, e = first.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	_, digest, _ := tokenPair()
	if e = insertLoginSessionSQL(ctx, tx, id.User.ID, id.Workspace.ID, current.Hash, current.Version, digest); e != nil {
		t.Fatal(e)
	}
	go func() {
		other, e := second.Begin(ctx)
		if e != nil {
			result <- e
			return
		}
		defer other.Rollback(ctx)
		s, e := readPasswordSnapshot(ctx, other, id.User.ID, true)
		if e == nil {
			e = rotatePasswordSQL(ctx, other, s, newHash, "password.changed")
		}
		if e == nil {
			e = other.Commit(ctx)
		}
		result <- e
	}()
	select {
	case e := <-result:
		t.Fatal("rotation did not serialize", e)
	case <-time.After(80 * time.Millisecond):
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e != nil {
		t.Fatal(e)
	}
	if e = first.QueryRow(ctx, "SELECT count(*) FROM studio_sessions WHERE user_id=$1", id.User.ID).Scan(&sessions); e != nil || sessions != 0 {
		t.Fatal("racing session resurrected", sessions, e)
	}
	if a.store.failed.Load() {
		t.Fatal("operation poisoned store")
	}
}

func TestPostgresAccountSecurityAuditFailureRollsBackReset(t *testing.T) {
	a, s, alice, id, db := securityFixture(t)
	token := securityToken(t, a, id.User.ID, "reset_password", time.Now().Add(time.Hour))
	securityExec(t, a, `CREATE FUNCTION reject_security_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_security BEFORE INSERT ON studio_security_events FOR EACH ROW EXECUTE FUNCTION reject_security_event()`)
	code, _, _ := authRequest(t, authClient(t), s.URL, "POST", "/api/v1/auth/password/reset/confirm", "", "", map[string]string{"token": token, "new_password": "replacement-password-123"})
	if code != 503 || !a.store.failed.Load() {
		t.Fatal("audit fault did not fail closed", code)
	}
	conn, e := pgx.Connect(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close(context.Background())
	var version int
	var used bool
	var sessions int
	digest, _ := accountTokenHash(token)
	e = conn.QueryRow(context.Background(), "SELECT u.credential_version,t.used_at IS NOT NULL,(SELECT count(*) FROM studio_sessions WHERE user_id=u.id) FROM studio_users u JOIN studio_account_tokens t ON t.user_id=u.id WHERE t.token_hash=$1", digest).Scan(&version, &used, &sessions)
	if e != nil || version != 1 || used || sessions != 1 {
		t.Fatal("state escaped rollback", version, used, sessions, e)
	}
	code, _, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 503 {
		t.Fatal("store did not fail closed", code)
	}
}

func TestPostgresAccountMailIssuanceUniformCooldownAndAtomicity(t *testing.T) {
	a, s, alice, id, _ := securityFixture(t)
	// Domain-only fixture: construct encryption and enqueue but deliberately do
	// not start a sender. The root integration suite owns TLS SMTP delivery.
	c := MailConfig{Enabled: true, Host: "localhost", Port: 465, TLSMode: "implicit_tls", Username: "fixture", Password: "fixture-mail-password", From: "service@example.test", PublicOrigin: "http://127.0.0.1:5173", OutboxKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}
	m, e := newMailDelivery(c, a.store)
	if e != nil {
		t.Fatal(e)
	}
	a.mail = m
	anonymous := authClient(t)
	code, known, _ := authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/password/reset/request", "", "", map[string]string{"email": id.User.Email})
	if code != 202 {
		t.Fatal(code, string(known))
	}
	code, unknown, _ := authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/password/reset/request", "", "", map[string]string{"email": "missing@example.test"})
	if code != 202 || !bytes.Equal(known, unknown) {
		t.Fatal("recipient enumeration", code, string(unknown))
	}
	code, _, _ = authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/password/reset/request", "", "", map[string]string{"email": id.User.Email})
	if code != 202 {
		t.Fatal("cooldown response", code)
	}
	var count, mailCount int
	var ciphertext []byte
	e = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT (SELECT count(*) FROM studio_account_tokens),(SELECT count(*) FROM studio_mail_outbox),(SELECT ciphertext FROM studio_mail_outbox LIMIT 1)").Scan(&count, &mailCount, &ciphertext)
	})
	if e != nil || count != 1 || mailCount != 1 || bytes.Contains(ciphertext, []byte(id.User.Email)) {
		t.Fatal("issuance not bounded/encrypted", count, mailCount, e)
	}
	code, _, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/bootstrap", "", "", nil)
	if code != 200 {
		t.Fatal("request invalidated session", code)
	}
	securityExec(t, a, "UPDATE studio_account_tokens SET created_at=created_at-interval '61 seconds'")
	securityExec(t, a, `CREATE FUNCTION reject_mail_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_mail BEFORE INSERT ON studio_mail_outbox FOR EACH ROW EXECUTE FUNCTION reject_mail_event()`)
	code, _, _ = authRequest(t, anonymous, s.URL, "POST", "/api/v1/auth/password/reset/request", "", "", map[string]string{"email": id.User.Email})
	if code != 503 {
		t.Fatal("mail insert failure not surfaced", code)
	}
	// Direct writer remains transactionally rolled back despite store poisoning.
	a.store.mu.Lock()
	e = a.store.conn.QueryRow(context.Background(), "SELECT count(*) FROM studio_account_tokens").Scan(&count)
	a.store.mu.Unlock()
	if e != nil || count != 1 {
		t.Fatal("token committed without email", count, e)
	}
}

func TestPostgresAccountMigrationPreservesLegacySessionAndFencesOldWriter(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	c, e := pgx.Connect(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(ctx)
	old := fstest.MapFS{}
	names, _ := fs.Glob(migrationFiles, "migrations/*.sql")
	for _, name := range names {
		if name >= "migrations/011_" {
			continue
		}
		b, _ := migrationFiles.ReadFile(name)
		old[name] = &fstest.MapFile{Data: b}
	}
	tx, e := c.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	raw, digest, _ := tokenPair()
	_, e = c.Exec(ctx, `INSERT INTO studio_users(id,email,name,password_hash) VALUES('legacy-user','legacy@example.test','Legacy',$1);`, []byte(dummyPasswordHash))
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Exec(ctx, `INSERT INTO studio_workspaces(id,owner_id,name) VALUES('legacy-workspace','legacy-user','Legacy');INSERT INTO studio_projects(id,workspace_id,name) VALUES('legacy-project','legacy-workspace','Legacy')`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Exec(ctx, "INSERT INTO studio_sessions(token_hash,user_id,workspace_id,expires_at) VALUES($1,'legacy-user','legacy-workspace',$2)", digest, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	a, e := newIsolatedApp(t, Config{DatabaseURL: db, AuthMode: "required"})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	r := httptest.NewRequest("GET", "http://127.0.0.1/api/v1/auth/session", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: raw})
	p, e := a.currentPrincipal(r)
	if e != nil || p == nil || !validSecurityID(p.SessionID, "ses") || p.CredentialVersion != 1 || p.User.EmailVerified {
		t.Fatal("legacy session lost", e)
	}
	tx, e = c.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than this executable") {
		t.Fatal("old writer not fenced", e)
	}
}
