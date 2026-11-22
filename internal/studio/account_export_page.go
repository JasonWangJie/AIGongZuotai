package studio

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	accountExportItemLimit = 100
	accountExportByteLimit = 512 << 10
)

type accountExportDocument struct {
	Format         string            `json:"format"`
	AccountID      string            `json:"account_id"`
	Section        string            `json:"section"`
	ReadAt         time.Time         `json:"read_at"`
	CreatedThrough time.Time         `json:"created_through"`
	Items          []json.RawMessage `json:"items"`
	NextCursor     *string           `json:"next_cursor"`
}

type accountExportProfile struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Email           string     `json:"email"`
	CreatedAt       time.Time  `json:"created_at"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
}

type accountExportAppeal struct {
	ID            string    `json:"id"`
	RestrictionID string    `json:"restriction_id"`
	State         string    `json:"state"`
	Outcome       string    `json:"outcome"`
	Version       uint64    `json:"version"`
	ReasonCode    string    `json:"reason_code"`
	Statement     string    `json:"statement"`
	PublicReply   string    `json:"public_reply"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type accountExportCursor struct {
	Through time.Time `json:"through"`
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
}

func validExportSection(section string) bool {
	return section == "profile" || section == "security_events" || section == "account_appeals"
}

func invalidExportCursor() error {
	return failure(400, "INVALID_EXPORT_CURSOR", "分页游标与当前账户、凭据或分区不匹配，请重新读取首页")
}

// Domain-separated MAC over the public range. The server-side salted verifier
// is identical across valid sessions for this account and changes on rotation.
// It and the password are never included in the cursor.
func accountExportCursorMAC(proof passwordSnapshot, section string, payload []byte) []byte {
	mac := hmac.New(sha256.New, proof.Hash)
	mac.Write([]byte("studio.account-export.cursor.v1\x00" + proof.UserID + "\x00" + section + "\x00"))
	mac.Write(payload)
	return mac.Sum(nil)
}

func encodeAccountExportCursor(cursor accountExportCursor, section string, proof passwordSnapshot) string {
	payload, _ := json.Marshal(cursor)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(accountExportCursorMAC(proof, section, payload))
}

func parseAccountExportCursor(raw, section string, proof passwordSnapshot, now time.Time) (accountExportCursor, error) {
	if !validExportSection(section) {
		return accountExportCursor{}, failure(400, "INVALID_EXPORT_SECTION", "请选择一个账户导出分区")
	}
	if raw == "" {
		return accountExportCursor{Through: now}, nil
	}
	if len(raw) > 1024 || section == "profile" {
		return accountExportCursor{}, invalidExportCursor()
	}
	body, tag, ok := strings.Cut(raw, ".")
	enc := base64.RawURLEncoding.Strict()
	payload, err := enc.DecodeString(body)
	sig, sigErr := enc.DecodeString(tag)
	var cursor accountExportCursor
	if !ok || err != nil || sigErr != nil || !hmac.Equal(sig, accountExportCursorMAC(proof, section, payload)) ||
		json.Unmarshal(payload, &cursor) != nil || cursor.At.IsZero() || cursor.At.After(cursor.Through) ||
		cursor.Through.After(now) {
		return cursor, invalidExportCursor()
	}
	if (section == "security_events" && !validSecurityID(cursor.ID, "sec")) ||
		(section == "account_appeals" && !validOperationsID(cursor.ID, "aapl")) {
		return cursor, invalidExportCursor()
	}
	return cursor, nil
}

type accountExportRow struct {
	id      string
	created time.Time
	body    json.RawMessage
}

func singleExportItemTooLarge() error {
	return failure(413, "ACCOUNT_EXPORT_ITEM_TOO_LARGE", "该条记录超过单份导出的字节上限")
}

func readAccountExportRows(ctx context.Context, tx pgx.Tx, actor, section string, cursor accountExportCursor) ([]accountExportRow, error) {
	if section == "profile" {
		var p accountExportProfile
		// 001 allowed unbounded SQL text before today's registration validator.
		// Avoid transferring a legacy field that cannot fit even without escapes.
		var name, email *string
		err := tx.QueryRow(ctx, `SELECT id,
			CASE WHEN octet_length(name)::bigint+octet_length(email)<=$2 THEN name END,
			CASE WHEN octet_length(name)::bigint+octet_length(email)<=$2 THEN email END,
			created_at,email_verified_at FROM studio_users WHERE id=$1`, actor, accountExportByteLimit).
			Scan(&p.ID, &name, &email, &p.CreatedAt, &p.EmailVerifiedAt)
		if err != nil {
			return nil, err
		}
		if name == nil || email == nil {
			return nil, singleExportItemTooLarge()
		}
		p.Name, p.Email, p.CreatedAt = *name, *email, p.CreatedAt.UTC()
		if p.EmailVerifiedAt != nil {
			utc := p.EmailVerifiedAt.UTC()
			p.EmailVerifiedAt = &utc
		}
		body, err := json.Marshal(p)
		return []accountExportRow{{p.ID, p.CreatedAt, body}}, err
	}
	var query string
	switch section {
	case "security_events":
		query = `SELECT id,action,created_at FROM studio_security_events WHERE user_id=$1 AND created_at<=$2`
	case "account_appeals":
		query = `SELECT id,restriction_id,state,outcome,version,reason_code,statement,public_reply,created_at,updated_at
			FROM studio_account_appeals WHERE target_user_id=$1 AND applicant_id=$1 AND created_at<=$2`
	default:
		return nil, failure(400, "INVALID_EXPORT_SECTION", "请选择一个账户导出分区")
	}
	args := []any{actor, cursor.Through}
	if cursor.ID != "" {
		query += " AND (created_at,id)<($3,$4)"
		args = append(args, cursor.At, cursor.ID)
	}
	rows, err := tx.Query(ctx, query+" ORDER BY created_at DESC,id DESC LIMIT 101", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]accountExportRow, 0, accountExportItemLimit+1)
	size := 0
	for rows.Next() {
		var item accountExportRow
		if section == "security_events" {
			var v SecurityEvent
			if err = rows.Scan(&v.ID, &v.Action, &v.CreatedAt); err != nil {
				return nil, err
			}
			v.CreatedAt = v.CreatedAt.UTC()
			item.id, item.created = v.ID, v.CreatedAt
			item.body, err = json.Marshal(v)
		} else {
			var v accountExportAppeal
			if err = rows.Scan(&v.ID, &v.RestrictionID, &v.State, &v.Outcome, &v.Version, &v.ReasonCode,
				&v.Statement, &v.PublicReply, &v.CreatedAt, &v.UpdatedAt); err != nil {
				return nil, err
			}
			v.CreatedAt, v.UpdatedAt = v.CreatedAt.UTC(), v.UpdatedAt.UTC()
			item.id, item.created = v.ID, v.CreatedAt
			item.body, err = json.Marshal(v)
		}
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		size += len(item.body)
		if size > accountExportByteLimit {
			break
		} // Last row guarantees a continuation.
	}
	return items, rows.Err()
}

func accountExportBytes(section string, proof passwordSnapshot, readAt, through time.Time, rows []accountExportRow) ([]byte, error) {
	doc := accountExportDocument{Format: "studio.account-export.v1", AccountID: proof.UserID, Section: section,
		ReadAt: readAt, CreatedThrough: through, Items: []json.RawMessage{}}
	count := min(len(rows), accountExportItemLimit)
	size := 0
	for _, row := range rows[:count] {
		size += len(row.body)
	}
	for ; count > 0; count-- {
		if count < len(rows) {
			last := rows[count-1]
			next := encodeAccountExportCursor(accountExportCursor{through, last.created, last.id}, section, proof)
			doc.NextCursor = &next
		}
		// [] is already in the encoded envelope. The only extra punctuation
		// for nonempty items is one comma between adjacent encoded rows.
		envelope, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if len(envelope)+size+count-1 <= accountExportByteLimit {
			for _, row := range rows[:count] {
				doc.Items = append(doc.Items, row.body)
			}
			return json.Marshal(doc)
		}
		size -= len(rows[count-1].body)
	}
	if len(rows) > 0 {
		return nil, singleExportItemTooLarge()
	}
	return json.Marshal(doc)
}
