package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Includes writers blocked on a slow client. The shared bcrypt budget is held
// only for the password check, never while reading the account or downloading.
var accountExportSlots = make(chan struct{}, 2)

func (a *App) accountExportIdentity(r *http.Request) (*principal, passwordSnapshot, error) {
	var snapshot passwordSnapshot
	if a.store == nil || a.config.AuthMode != "required" {
		return nil, snapshot, failure(409, "AUTH_DISABLED", "当前模式未启用账户认证")
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) != 43 {
		return nil, snapshot, failure(401, "AUTH_REQUIRED", "请先登录")
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	p := &principal{User: &User{}, SessionHash: digest[:]}
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT s.user_id,s.workspace_id,s.id,s.credential_version,u.password_hash
			FROM studio_sessions s JOIN studio_users u ON u.id=s.user_id AND u.credential_version=s.credential_version
			WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp()`, p.SessionHash).
			Scan(&p.User.ID, &p.Workspace.ID, &p.SessionID, &p.CredentialVersion, &snapshot.Hash)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, snapshot, failure(401, "AUTH_REQUIRED", "会话已失效，请重新登录")
	}
	snapshot.UserID, snapshot.Version = p.User.ID, p.CredentialVersion
	return p, snapshot, err
}

func (a *App) accountExportPassword(r *http.Request, snapshot passwordSnapshot, password string) error {
	if !a.authAdmission(r, "account-export", snapshot.UserID) {
		return failure(429, "ACCOUNT_EXPORT_RATE_LIMIT", "导出核验过于频繁，请稍后重试")
	}
	select {
	case a.bcryptSlots <- struct{}{}:
		defer func() { <-a.bcryptSlots }()
	default:
		return failure(429, "AUTH_BUSY", "密码核验繁忙，请稍后重试")
	}
	if !validAccountPassword(password) || bcrypt.CompareHashAndPassword(snapshot.Hash, []byte(password)) != nil {
		return failure(403, "ACCOUNT_EXPORT_PASSWORD", "当前密码核验失败")
	}
	return nil
}

func (a *App) accountDataExportHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case accountExportSlots <- struct{}{}:
		defer func() { <-accountExportSlots }()
	default:
		writeError(w, failure(429, "AUTH_BUSY", "账户导出繁忙，请稍后重试"))
		return
	}
	p, snapshot, err := a.accountExportIdentity(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input map[string]*string
	if err = decodeGenerationJSON(w, r, &input, "section", "password", "cursor"); err != nil {
		writeError(w, err)
		return
	}
	if input["section"] == nil || !validExportSection(*input["section"]) {
		writeError(w, failure(400, "INVALID_EXPORT_SECTION", "请选择一个账户导出分区"))
		return
	}
	var cursor, password string
	if value, ok := input["cursor"]; ok {
		if value == nil || *value == "" || len(*value) > 1024 {
			writeError(w, invalidExportCursor())
			return
		}
		cursor = *value
	}
	if value := input["password"]; value != nil {
		password = *value
	}
	if err = a.accountExportPassword(r, snapshot, password); err != nil {
		writeError(w, err)
		return
	}
	section := *input["section"]
	data, err := a.exportAccountPage(r.Context(), p, snapshot, section, cursor)
	if err != nil {
		writeError(w, err)
		return
	}
	digest := sha256.Sum256(data)
	controller := http.NewResponseController(w)
	if err = controller.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, failure(503, "ACCOUNT_EXPORT_UNAVAILABLE", "当前连接无法准备下载"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="account-`+section+`.json"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
	_, _ = w.Write(data)
}

func (a *App) exportAccountPage(requestCtx context.Context, p *principal, proof passwordSnapshot, section, rawCursor string) ([]byte, error) {
	var data []byte
	checkCancelled := func() error {
		if requestCtx.Err() != nil {
			return failure(408, "ACCOUNT_EXPORT_CANCELLED", "下载请求已取消")
		}
		return nil
	}
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			if err := checkCancelled(); err != nil {
				return err
			}
			// Lock the same account and session rows as credential rotation and
			// revocation. TRANSACTION COMMIT precedes setting any attachment headers.
			current, err := operationsSessionSQL(ctx, tx, p)
			if err != nil {
				return err
			}
			if current.UserID != proof.UserID || current.Version != proof.Version || !bytes.Equal(current.Hash, proof.Hash) {
				return failure(401, "AUTH_REQUIRED", "凭据已变化，请重新登录")
			}
			var readAt time.Time
			if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&readAt); err != nil {
				return err
			}
			cursor, err := parseAccountExportCursor(rawCursor, section, current, readAt.UTC())
			if err != nil {
				return err
			}
			rows, err := readAccountExportRows(ctx, tx, current.UserID, section, cursor)
			if err != nil {
				return err
			}
			data, err = accountExportBytes(section, current, readAt.UTC(), cursor.Through, rows)
			if err != nil {
				return err
			}
			if err = checkCancelled(); err != nil {
				return err
			}
			// The rows remain locked, but the cookie can also expire with time.
			return validateCurrentSessionSQL(ctx, tx, p, current.Version)
		})
	})
	if err != nil {
		return nil, err
	}
	if err = checkCancelled(); err != nil {
		return nil, err
	}
	return data, nil
}
