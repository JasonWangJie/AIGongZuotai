package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAccountExportByteBoundaryAndCursor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	proof := passwordSnapshot{UserID: newID("usr"), Hash: []byte("fixture-only-private-verifier"), Version: 1}
	var rows []accountExportRow
	for i := 0; i < 101; i++ {
		v := accountExportAppeal{ID: fmt.Sprintf("aapl_%032x", 101-i), CreatedAt: now,
			Statement: strings.Repeat("<", 1024), PublicReply: strings.Repeat("&", 1024)}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, accountExportRow{v.ID, now, b})
	}
	b, err := accountExportBytes("account_appeals", proof, now, now, rows)
	if err != nil {
		t.Fatal(err)
	}
	d := decode[accountExportDocument](t, b)
	if len(b) > accountExportByteLimit || len(d.Items) < 1 || len(d.Items) >= 100 || d.NextCursor == nil {
		t.Fatal("byte boundary", len(b), len(d.Items))
	}
	c, err := parseAccountExportCursor(*d.NextCursor, "account_appeals", proof, now)
	if err != nil || c.ID != rows[len(d.Items)-1].id || !c.Through.Equal(now) {
		t.Fatal("wrong resume position", c, err)
	}
	for _, tc := range []struct {
		s     string
		p     passwordSnapshot
		token string
	}{
		{"security_events", proof, *d.NextCursor}, {"account_appeals", passwordSnapshot{UserID: newID("usr"), Hash: proof.Hash}, *d.NextCursor},
		{"account_appeals", passwordSnapshot{UserID: proof.UserID, Hash: []byte("rotated")}, *d.NextCursor},
		{"account_appeals", proof, "A" + *d.NextCursor}, {"account_appeals", proof, *d.NextCursor + "!"}} {
		if _, e := parseAccountExportCursor(tc.token, tc.s, tc.p, now); e == nil {
			t.Fatal("accepted wrong cursor")
		}
	}
	tooBig, _ := json.Marshal(strings.Repeat("x", accountExportByteLimit))
	if data, e := accountExportBytes("profile", proof, now, now, []accountExportRow{{body: tooBig}}); e == nil || data != nil {
		t.Fatal("unrepresentable single row returned a truncated document")
	}
}

const accountExportPath = "/api/v1/auth/data-export"

func getAccountExport(t *testing.T, client *http.Client, base, section, cursor string) (accountExportDocument, []byte) {
	t.Helper()
	input := map[string]string{"section": section, "password": "fixture-password-123"}
	if cursor != "" {
		input["cursor"] = cursor
	}
	code, raw, h := authRequest(t, client, base, "POST", accountExportPath, "", "", input)
	if code != 200 {
		t.Fatalf("account export failed %d %s", code, raw)
	}
	hash := sha256.Sum256(raw)
	if h.Get("X-Content-SHA256") != hex.EncodeToString(hash[:]) || h.Get("Content-Length") != strconv.Itoa(len(raw)) ||
		h.Get("Content-Disposition") != `attachment; filename="account-`+section+`.json"` || h.Get("Cache-Control") != "private, no-store" ||
		h.Get("Content-Type") != "application/json" || len(raw) > accountExportByteLimit {
		t.Fatal("attachment integrity/size/header mismatch")
	}
	result := decode[accountExportDocument](t, raw)
	if result.Format != "studio.account-export.v1" || result.Section != section || result.Items == nil || len(result.Items) > accountExportItemLimit {
		t.Fatal("invalid file schema")
	}
	return result, raw
}

func expectAccountExportError(t *testing.T, c *http.Client, base string, input map[string]string, status int, errorCode string) {
	t.Helper()
	code, raw, h := authRequest(t, c, base, "POST", accountExportPath, "", "", input)
	if code != status || !bytes.Contains(raw, []byte(errorCode)) || h.Get("Content-Disposition") != "" || h.Get("X-Content-SHA256") != "" ||
		bytes.Contains(raw, []byte(`"items"`)) {
		t.Fatalf("failure emitted an attachment or incorrect status: %d %s", code, raw)
	}
}

func TestPostgresAccountExportPersonalSectionsAndRevocation(t *testing.T) {
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required"})
	client, second, other := authClient(t), authClient(t), authClient(t)
	owner := registerFixture(t, client, s.URL, "export-own@example.test")
	foreign := registerFixture(t, other, s.URL, "export-other-private@example.test")
	pass := "fixture-password-123"
	code, _, _ := authRequest(t, second, s.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": owner.User.Email, "password": pass})
	if code != 200 {
		t.Fatal("second live session", code)
	}
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `INSERT INTO studio_security_events(id,user_id,action,created_at)
		SELECT 'sec_'||md5(n::text),$1,'session.revoked',clock_timestamp()::date-interval '1 day' FROM generate_series(1,103)n`, owner.User.ID); err != nil {
		t.Fatal(err)
	}
	p, raw := getAccountExport(t, client, s.URL, "profile", "")
	profile := decode[accountExportProfile](t, p.Items[0])
	if p.AccountID != owner.User.ID || profile.Email != owner.User.Email || profile.ID != owner.User.ID || p.NextCursor != nil ||
		bytes.Contains(raw, []byte(foreign.User.Email)) || bytes.Contains(raw, []byte("password")) {
		t.Fatal("profile disclosure")
	}
	expectAccountExportError(t, client, s.URL, map[string]string{"section": "profile", "password": "wrong-password-123"}, 403, "ACCOUNT_EXPORT_PASSWORD")
	expectAccountExportError(t, client, s.URL, map[string]string{"section": "profile", "password": pass, "user_id": foreign.User.ID}, 400, "INVALID_JSON")
	page, _ := getAccountExport(t, client, s.URL, "security_events", "")
	if len(page.Items) != 100 || page.NextCursor == nil {
		t.Fatal("event count boundary")
	}
	var countBefore int
	err := admin.QueryRow(ctx, "SELECT count(*) FROM studio_security_events WHERE user_id=$1 AND created_at<=$2", owner.User.ID, page.CreatedThrough).Scan(&countBefore)
	if err != nil {
		t.Fatal(err)
	}
	late := newID("sec")
	if _, err = admin.Exec(ctx, "INSERT INTO studio_security_events(id,user_id,action) VALUES($1,$2,'account.login')", late, owner.User.ID); err != nil {
		t.Fatal(err)
	}
	input := map[string]string{"section": "security_events", "password": pass, "cursor": *page.NextCursor}
	expectAccountExportError(t, other, s.URL, input, 400, "INVALID_EXPORT_CURSOR")
	expectAccountExportError(t, client, s.URL, map[string]string{"section": "account_appeals", "password": pass, "cursor": *page.NextCursor}, 400, "INVALID_EXPORT_CURSOR")
	last, _ := getAccountExport(t, second, s.URL, "security_events", *page.NextCursor)
	if last.NextCursor != nil || !last.CreatedThrough.Equal(page.CreatedThrough) || !last.ReadAt.After(page.ReadAt) {
		t.Fatal("range changed between two live same-account sessions")
	}
	seen := map[string]bool{}
	for _, item := range append(page.Items, last.Items...) {
		v := decode[SecurityEvent](t, item)
		if seen[v.ID] || v.ID == late {
			t.Fatal("repeat or new event entered range")
		}
		seen[v.ID] = true
	}
	if len(seen) != countBefore {
		t.Fatal("incomplete event query", len(seen), countBefore)
	}
	// Capture the real old session/password proof, then exercise revocation
	// between password verification and the final locked read.
	u, _ := url.Parse(s.URL)
	req, _ := http.NewRequest("POST", s.URL+accountExportPath, nil)
	for _, cookie := range client.Jar.Cookies(u) {
		req.AddCookie(cookie)
	}
	identity, original, err := a.accountExportIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.accountExportPassword(req, original, pass); err != nil {
		t.Fatal(err)
	}
	// Hold the actor row on a separate connection during the final read.
	// Even a changed verifier with the original version must invalidate proof.
	change, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer change.Rollback(ctx)
	if _, err = change.Exec(ctx, "UPDATE studio_users SET password_hash=$2 WHERE id=$1", owner.User.ID, []byte("incompatible-fixture-verifier")); err != nil {
		t.Fatal(err)
	}
	finished := make(chan bool, 1)
	go func() {
		out, e := a.exportAccountPage(ctx, identity, original, "profile", "")
		var op *operationError
		finished <- errors.As(e, &op) && op.code == "AUTH_REQUIRED" && out == nil
	}()
	generationWaitWorkspaceBlock(t, change, a.store.conn.PgConn().PID())
	if err = change.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !<-finished {
		t.Fatal("password change leaked stale profile")
	}
	if _, err = admin.Exec(ctx, "UPDATE studio_users SET password_hash=$2 WHERE id=$1", owner.User.ID, original.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "DELETE FROM studio_sessions WHERE id=$1", identity.SessionID); err != nil {
		t.Fatal(err)
	}
	var op *operationError
	if out, e := a.exportAccountPage(ctx, identity, original, "profile", ""); !errors.As(e, &op) || op.code != "AUTH_REQUIRED" || out != nil {
		t.Fatal("revoked session leaked stale profile")
	}
	expectAccountExportError(t, client, s.URL, input, 401, "AUTH_REQUIRED")
	secondProfile, _ := getAccountExport(t, second, s.URL, "profile", "")
	if secondProfile.AccountID != owner.User.ID {
		t.Fatal("independent session lost its own profile")
	}
}

func TestPostgresAccountExportAppealsPrivacyAndBytePagination(t *testing.T) {
	db := testDatabase(t)
	_, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required"})
	ownerClient, otherClient := authClient(t), authClient(t)
	owner := registerFixture(t, ownerClient, s.URL, "export-appeal-owner@example.test")
	other := registerFixture(t, otherClient, s.URL, "export-private-reviewer@example.test")
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	for _, target := range []string{owner.User.ID, other.User.ID} {
		count := 98
		statement := strings.Repeat("<", 1024)
		if target == other.User.ID {
			count = 1
			statement = "foreign-private-appeal"
		}
		_, err := admin.Exec(ctx, `INSERT INTO studio_account_restrictions(id,target_user_id,reason_code,public_notice,evidence_ref,created_by,created_at,released_at)
			SELECT 'arst_'||md5($1::text||n::text),$1,'manual_review','notice','operator-private-evidence',$2,
			now()-interval '1 day',now() FROM generate_series(1,$3::int)n`, target, other.User.ID, count)
		if err != nil {
			t.Fatal(err)
		}
		_, err = admin.Exec(ctx, `INSERT INTO studio_account_appeals(id,target_user_id,restriction_id,applicant_id,state,outcome,version,reason_code,statement,public_reply,created_at,updated_at)
			SELECT 'aapl_'||md5($1::text||n::text),$1,'arst_'||md5($1::text||n::text),$1,'RESOLVED','LIFTED',2,'request_review',$3,repeat('&',1024),now()-interval '1 day',now()
			FROM generate_series(1,$2::int)n`, target, count, statement)
		if err != nil {
			t.Fatal(err)
		}
	}
	restriction := newID("arst")
	if _, err := admin.Exec(ctx, `WITH origin AS (
		INSERT INTO studio_account_restrictions(id,target_user_id,reason_code,public_notice,evidence_ref,created_by)
		VALUES($1,$2,'manual_review','active notice','private-current-evidence',$3) RETURNING id,target_user_id)
		INSERT INTO studio_account_controls(user_id,state,version,active_restriction_id) SELECT target_user_id,'SUSPENDED',2,id FROM origin`, restriction, owner.User.ID, other.User.ID); err != nil {
		t.Fatal(err)
	}
	code, ordinary, _ := authRequest(t, ownerClient, s.URL, "GET", "/api/v1/models", "", "", nil)
	if code != 403 || !bytes.Contains(ordinary, []byte("ACCOUNT_RESTRICTED")) {
		t.Fatalf("restricted boundary %d %s", code, ordinary)
	}
	cursor := ""
	seen := map[string]bool{}
	var rangeEnd time.Time
	for pageNo := 0; ; pageNo++ {
		if pageNo > 4 {
			t.Fatal("cursor failed to advance")
		}
		page, raw := getAccountExport(t, ownerClient, s.URL, "account_appeals", cursor)
		for _, secret := range []string{other.User.ID, "foreign-private-appeal", "operator-private-evidence", "private-current-evidence", "evidence_ref", "password_hash", "applicant_id"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("appeal privacy projection leaked forbidden field")
			}
		}
		if pageNo == 0 {
			rangeEnd = page.CreatedThrough
			if len(page.Items) >= 98 || page.NextCursor == nil {
				t.Fatal("escaped byte-size limit ignored")
			}
		}
		if !page.CreatedThrough.Equal(rangeEnd) {
			t.Fatal("appeals range moved")
		}
		for _, item := range page.Items {
			v := decode[accountExportAppeal](t, item)
			if seen[v.ID] || v.Statement != strings.Repeat("<", 1024) || v.PublicReply != strings.Repeat("&", 1024) {
				t.Fatal("duplicate or truncated personal statement")
			}
			seen[v.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 98 {
		t.Fatal("skipped records at byte boundary", len(seen))
	}
}
