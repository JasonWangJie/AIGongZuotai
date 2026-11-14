package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type AccountSecurityCapabilities struct {
	Enabled              bool   `json:"enabled"`
	MailConfigured       bool   `json:"mail_configured"`
	VerificationEnabled  bool   `json:"verification_enabled"`
	PasswordResetEnabled bool   `json:"password_reset_enabled"`
	Reason               string `json:"reason,omitempty"`
}

func (a *App) accountSecurityCapabilities() AccountSecurityCapabilities {
	enabled := a.config.AuthMode == "required"
	c := AccountSecurityCapabilities{Enabled: enabled, MailConfigured: a.mail != nil, VerificationEnabled: enabled && a.mail != nil, PasswordResetEnabled: enabled && a.mail != nil}
	if !enabled {
		c.Reason = "当前模式未启用账户认证"
	} else if a.mail == nil {
		c.Reason = "邮件渠道尚未配置"
	}
	return c
}
func validAccountPassword(v string) bool {
	return len(v) >= 10 && len(v) <= 72 && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}
func validSecurityID(v, prefix string) bool {
	if (prefix != "ses" && prefix != "sec") || len(v) != len(prefix)+33 || !strings.HasPrefix(v, prefix+"_") {
		return false
	}
	for _, c := range v[len(prefix)+1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func accountTokenHash(raw string) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, invalidAccountToken() }
	if len(raw) != 43 {
		return bad()
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != raw {
		return bad()
	}
	hash := sha256.Sum256([]byte(raw))
	return hash[:], nil
}
func invalidAccountToken() error {
	return failure(400, "ACCOUNT_TOKEN_INVALID", "链接无效、已使用或已过期，请重新申请")
}
func insertSecurityEventSQL(ctx context.Context, q sqlExecer, user, action string) error {
	_, err := q.Exec(ctx, "INSERT INTO studio_security_events(id,user_id,action) VALUES($1,$2,$3)", newID("sec"), user, action)
	return err
}
func clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}
func (a *App) requireAccount(r *http.Request) (*principal, error) {
	if a.config.AuthMode != "required" {
		return nil, failure(409, "AUTH_DISABLED", "当前本地模式未启用账户认证")
	}
	p, err := a.currentPrincipal(r)
	if err != nil {
		return nil, err
	}
	if p == nil || p.User == nil {
		return nil, failure(401, "AUTH_REQUIRED", "请先登录")
	}
	return p, nil
}
func (a *App) securityRate(w http.ResponseWriter, r *http.Request, operation, identity string) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	d := sha256.Sum256([]byte(identity))
	if !a.limiter.allow("security:"+operation+":ip:"+ip, 30) || !a.limiter.allow("security:"+operation+":id:"+base64.RawURLEncoding.EncodeToString(d[:]), 10) {
		w.Header().Set("Retry-After", "900")
		writeError(w, failure(429, "AUTH_RATE_LIMIT", "操作过于频繁，请稍后重试"))
		return false
	}
	return true
}
func emptyAccountBody(w http.ResponseWriter, r *http.Request) error {
	var in struct{}
	return decodeJSON(w, r, &in)
}
func (a *App) acquirePasswordSlot(w http.ResponseWriter) bool {
	select {
	case a.bcryptSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "3")
		writeError(w, failure(429, "AUTH_BUSY", "认证处理繁忙，请稍后重试"))
		return false
	}
}

// All identity-independent failures (disabled channel, backlog, IP limit) are
// checked for unknown and known recipients alike; SMTP is never in this path.
func (a *App) accountMailRequest(w http.ResponseWriter, r *http.Request) {
	purpose := "reset_password"
	email := ""
	userID := ""
	var account *principal
	if a.config.AuthMode != "required" {
		writeError(w, failure(409, "AUTH_DISABLED", "当前本地模式未启用账户认证"))
		return
	}
	if strings.Contains(r.URL.Path, "verification") {
		purpose = "verify_email"
		p, err := a.requireAccount(r)
		if err != nil {
			writeError(w, err)
			return
		}
		email, userID = p.User.Email, p.User.ID
		account = p
		if err := emptyAccountBody(w, r); err != nil {
			writeError(w, err)
			return
		}
	} else {
		var in struct {
			Email string `json:"email"`
		}
		if err := decodeJSON(w, r, &in); err != nil {
			writeError(w, err)
			return
		}
		var valid bool
		email, valid = normalizeDeliveryEmail(in.Email)
		if !valid {
			writeError(w, failure(422, "INVALID_EMAIL", "请提供有效邮箱"))
			return
		}
	}
	if a.mail == nil {
		writeError(w, failure(503, "MAIL_UNAVAILABLE", "邮件渠道尚未配置"))
		return
	}
	if _, err := strictMailbox(email); err != nil {
		writeError(w, failure(503, "MAIL_UNAVAILABLE", "当前邮箱地址格式暂不支持邮件投递"))
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if !a.limiter.allow("security:mail:"+purpose+":ip:"+ip, 30) {
		w.Header().Set("Retry-After", "900")
		writeError(w, failure(429, "AUTH_RATE_LIMIT", "操作过于频繁，请稍后重试"))
		return
	}
	emailDigest := sha256.Sum256([]byte(email))
	allowRecipient := a.limiter.allow("security:mail:"+purpose+":recipient:"+base64.RawURLEncoding.EncodeToString(emailDigest[:]), 10)
	raw, digest, err := tokenPair()
	if err != nil {
		writeError(w, err)
		return
	}
	ttl := 30 * time.Minute
	if purpose == "verify_email" {
		ttl = 24 * time.Hour
	}
	expiry := time.Now().UTC().Add(ttl)
	sealed, err := a.mail.seal(EmailEnvelope{ID: newID("mail"), Purpose: purpose, To: email, Token: raw, ExpiresAt: expiry})
	if err != nil {
		writeError(w, err)
		return
	}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if e = checkMailCapacitySQL(ctx, tx); e != nil {
			return e
		}
		var id string
		var version int64
		var verified *time.Time
		e = tx.QueryRow(ctx, "SELECT id,credential_version,email_verified_at FROM studio_users WHERE email=$1 FOR UPDATE", email).Scan(&id, &version, &verified)
		if errors.Is(e, pgx.ErrNoRows) {
			return tx.Commit(ctx)
		}
		if e != nil {
			return e
		}
		if account != nil {
			if e = validateCurrentSessionSQL(ctx, tx, account, version); e != nil {
				return e
			}
		}
		if !allowRecipient || (userID != "" && userID != id) || (purpose == "verify_email" && verified != nil) {
			return tx.Commit(ctx)
		}
		// Keep up to three existing valid links. A repeated request cannot invalidate
		// an already-delivered link or affect current sessions.
		var recent bool
		var active int
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_account_tokens WHERE user_id=$1 AND purpose=$2 AND created_at>clock_timestamp()-interval '60 seconds'),(SELECT count(*) FROM studio_account_tokens WHERE user_id=$1 AND purpose=$2 AND used_at IS NULL AND expires_at>clock_timestamp() AND credential_version=$3)`, id, purpose, version).Scan(&recent, &active); e != nil {
			return e
		}
		if recent || active >= 3 {
			return tx.Commit(ctx)
		}
		if _, e = tx.Exec(ctx, "DELETE FROM studio_account_tokens WHERE user_id=$1 AND (expires_at<=clock_timestamp() OR used_at IS NOT NULL OR credential_version<>$2)", id, version); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO studio_account_tokens(token_hash,user_id,purpose,credential_version,expires_at) VALUES($1,$2,$3,$4,$5)", digest, id, purpose, version, expiry); e != nil {
			return e
		}
		if e = enqueueMailSQL(ctx, tx, sealed); e != nil {
			return e
		}
		action := "password.reset_requested"
		if purpose == "verify_email" {
			action = "email.verification_requested"
		}
		if e = insertSecurityEventSQL(ctx, tx, id, action); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "message": "如果账户符合条件，系统将安排发送邮件；请稍后检查邮箱"})
}

type passwordSnapshot struct {
	UserID  string
	Hash    []byte
	Version int64
}

func readPasswordSnapshot(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string, lock bool) (passwordSnapshot, error) {
	s := passwordSnapshot{UserID: id}
	sql := "SELECT password_hash,credential_version FROM studio_users WHERE id=$1"
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, id).Scan(&s.Hash, &s.Version)
	return s, err
}
func validateCurrentSessionSQL(ctx context.Context, tx pgx.Tx, p *principal, version int64) error {
	var valid bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_sessions WHERE token_hash=$1 AND user_id=$2 AND credential_version=$3 AND expires_at>clock_timestamp())", p.SessionHash, p.User.ID, version).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return failure(401, "AUTH_REQUIRED", "会话已失效，请重新登录")
	}
	return nil
}
func rotatePasswordSQL(ctx context.Context, tx pgx.Tx, s passwordSnapshot, hash []byte, action string) error {
	if _, err := tx.Exec(ctx, "UPDATE studio_users SET password_hash=$2,credential_version=credential_version+1 WHERE id=$1", s.UserID, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM studio_sessions WHERE user_id=$1", s.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE studio_account_tokens SET used_at=COALESCE(used_at,clock_timestamp()) WHERE user_id=$1", s.UserID); err != nil {
		return err
	}
	return insertSecurityEventSQL(ctx, tx, s.UserID, action)
}
func (a *App) accountPasswordChange(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err = decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	if !validAccountPassword(in.NewPassword) || len(in.CurrentPassword) > 72 || len(in.CurrentPassword) == 0 {
		writeError(w, failure(422, "INVALID_PASSWORD", "新密码必须为10至72字节有效文本"))
		return
	}
	if !a.securityRate(w, r, "change", p.User.ID) || !a.acquirePasswordSlot(w) {
		return
	}
	defer func() { <-a.bcryptSlots }()
	var original passwordSnapshot
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var e error
		original, e = readPasswordSnapshot(ctx, conn, p.User.ID, false)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword(original.Hash, []byte(in.CurrentPassword)) != nil {
		writeError(w, failure(401, "INVALID_CREDENTIALS", "当前密码错误"))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), 12)
	if err != nil {
		writeError(w, err)
		return
	}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		current, e := readPasswordSnapshot(ctx, tx, p.User.ID, true)
		if e != nil {
			return e
		}
		if current.Version != original.Version || !bytes.Equal(current.Hash, original.Hash) {
			return failure(401, "INVALID_CREDENTIALS", "账户凭据已变更，请重新登录")
		}
		if e = validateCurrentSessionSQL(ctx, tx, p, current.Version); e != nil {
			return e
		}
		if e = rotatePasswordSQL(ctx, tx, current, hash, "password.changed"); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	clearSession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true, "reauthenticate": true})
}

func (a *App) accountTokenConfirm(w http.ResponseWriter, r *http.Request) {
	if a.config.AuthMode != "required" {
		writeError(w, failure(409, "AUTH_DISABLED", "当前本地模式未启用账户认证"))
		return
	}
	purpose := "reset_password"
	if strings.Contains(r.URL.Path, "verification") {
		purpose = "verify_email"
	}
	var in struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password,omitempty"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	digest, err := accountTokenHash(in.Token)
	if err != nil {
		writeError(w, err)
		return
	}
	if !a.securityRate(w, r, "consume:"+purpose, in.Token) {
		return
	}
	if purpose == "reset_password" && !validAccountPassword(in.NewPassword) {
		writeError(w, failure(422, "INVALID_PASSWORD", "新密码必须为10至72字节有效文本"))
		return
	}
	var userID string
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT user_id FROM studio_account_tokens WHERE token_hash=$1 AND purpose=$2 AND used_at IS NULL AND expires_at>clock_timestamp()", digest, purpose).Scan(&userID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = invalidAccountToken()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	var hash []byte
	if purpose == "reset_password" {
		if !a.acquirePasswordSlot(w) {
			return
		}
		defer func() { <-a.bcryptSlots }()
		hash, err = bcrypt.GenerateFromPassword([]byte(in.NewPassword), 12)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		user, e := readPasswordSnapshot(ctx, tx, userID, true)
		if e != nil {
			return e
		}
		var tokenVersion int64
		e = tx.QueryRow(ctx, "SELECT credential_version FROM studio_account_tokens WHERE token_hash=$1 AND user_id=$2 AND purpose=$3 AND used_at IS NULL AND expires_at>clock_timestamp() FOR UPDATE", digest, userID, purpose).Scan(&tokenVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			return invalidAccountToken()
		}
		if e != nil {
			return e
		}
		if tokenVersion != user.Version {
			return invalidAccountToken()
		}
		if purpose == "reset_password" {
			if e = rotatePasswordSQL(ctx, tx, user, hash, "password.reset"); e != nil {
				return e
			}
		} else {
			if _, e = tx.Exec(ctx, "UPDATE studio_users SET email_verified_at=COALESCE(email_verified_at,clock_timestamp()) WHERE id=$1", userID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "UPDATE studio_account_tokens SET used_at=clock_timestamp() WHERE user_id=$1 AND purpose='verify_email' AND used_at IS NULL", userID); e != nil {
				return e
			}
			if e = insertSecurityEventSQL(ctx, tx, userID, "email.verified"); e != nil {
				return e
			}
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if purpose == "reset_password" {
		clearSession(w, r)
		writeJSON(w, 200, map[string]bool{"ok": true, "reauthenticate": true})
	} else {
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

type AccountSession struct {
	ID        string    `json:"id"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *App) accountSessions(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]AccountSession, 0)
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		rows, e := conn.Query(ctx, "SELECT id,created_at,expires_at FROM studio_sessions WHERE user_id=$1 AND credential_version=$2 AND expires_at>clock_timestamp() ORDER BY created_at DESC,id DESC LIMIT 10", p.User.ID, p.CredentialVersion)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var s AccountSession
			if e = rows.Scan(&s.ID, &s.CreatedAt, &s.ExpiresAt); e != nil {
				return e
			}
			s.Current = s.ID == p.SessionID
			items = append(items, s)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *App) accountRevokeSessions(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err = emptyAccountBody(w, r); err != nil {
		writeError(w, err)
		return
	}
	all := strings.HasSuffix(r.URL.Path, "/revoke-all")
	id := r.PathValue("id")
	if !all && !validSecurityID(id, "ses") {
		writeError(w, failure(404, "SESSION_NOT_FOUND", "会话不存在"))
		return
	}
	if !a.securityRate(w, r, "revoke", p.User.ID) {
		return
	}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		current, e := readPasswordSnapshot(ctx, tx, p.User.ID, true)
		if e != nil {
			return e
		}
		if e = validateCurrentSessionSQL(ctx, tx, p, current.Version); e != nil {
			return e
		}
		action := "session.revoked"
		if all {
			_, e = tx.Exec(ctx, "DELETE FROM studio_sessions WHERE user_id=$1", p.User.ID)
			action = "sessions.revoked_all"
		} else {
			result, deleteErr := tx.Exec(ctx, "DELETE FROM studio_sessions WHERE id=$1 AND user_id=$2", id, p.User.ID)
			e = deleteErr
			if e == nil && result.RowsAffected() == 0 {
				return failure(404, "SESSION_NOT_FOUND", "会话不存在")
			}
		}
		if e != nil {
			return e
		}
		if e = insertSecurityEventSQL(ctx, tx, p.User.ID, action); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	reauth := all || id == p.SessionID
	if reauth {
		clearSession(w, r)
	}
	writeJSON(w, 200, map[string]bool{"ok": true, "reauthenticate": reauth})
}

type SecurityEvent struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"created_at"`
}
type securityCursor struct {
	User string    `json:"u"`
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
}

func (a *App) accountSecurityEvents(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			writeError(w, failure(400, "INVALID_LIMIT", "分页数量必须为1至50"))
			return
		}
	}
	cursor := securityCursor{User: p.User.ID}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 512 {
			writeError(w, failure(400, "INVALID_SECURITY_CURSOR", "安全记录游标无效"))
			return
		}
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || json.Unmarshal(b, &cursor) != nil || cursor.User != p.User.ID || !validSecurityID(cursor.ID, "sec") || cursor.At.IsZero() {
			writeError(w, failure(400, "INVALID_SECURITY_CURSOR", "安全记录游标无效"))
			return
		}
	}
	items := make([]SecurityEvent, 0, limit+1)
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		rows, e := conn.Query(ctx, "SELECT id,action,created_at FROM studio_security_events WHERE user_id=$1 AND ($2='' OR (created_at,id)<($3,$2)) ORDER BY created_at DESC,id DESC LIMIT $4", p.User.ID, cursor.ID, cursor.At, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v SecurityEvent
			if e = rows.Scan(&v.ID, &v.Action, &v.CreatedAt); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		v := items[len(items)-1]
		blob, _ := json.Marshal(securityCursor{User: p.User.ID, ID: v.ID, At: v.CreatedAt})
		raw := base64.RawURLEncoding.EncodeToString(blob)
		next = &raw
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
