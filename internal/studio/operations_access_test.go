package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestOperationsRoleMatrixAndClosedInputs(t *testing.T) {
	for role, permissions := range map[string][]string{"observer": {"read"}, "operator": {"read", "case_open", "investigate"}, "finance": {"read", "case_open", "finance"}, "moderator": {"read", "generation_restrict", "appeal_resolve"}} {
		got := operationsRolePermissions(role)
		if strings.Join(got, ",") != strings.Join(permissions, ",") {
			t.Fatal(role, got)
		}
		for _, permission := range []string{"read", "case_open", "investigate", "finance", "generation_restrict", "appeal_resolve"} {
			allowed := false
			for _, p := range permissions {
				allowed = allowed || p == permission
			}
			granted := false
			for _, r := range operationsRoles(permission) {
				granted = granted || r == role
			}
			if granted != allowed {
				t.Fatal("role union mismatch", role, permission)
			}
		}
	}
	if operationsRoles("admin") != nil || operationsRolePermissions("admin") != nil {
		t.Fatal("implicit administrator")
	}
	for _, id := range []string{"ws_demo", newID("usr"), newID("ws") + "\x00", strings.Repeat("x", 9999)} {
		if validOperationsID(id, "ws") {
			t.Fatal("invalid scope accepted")
		}
	}
	if err := checkOperationsPermissionSQL(context.Background(), nil, nil, newID("ws"), "finance", nil); err == nil {
		t.Fatal("missing proof accepted")
	}
	event := OperationsAuditEvent{WorkspaceID: newID("ws"), Action: "case.created", EvidenceRef: "https://secret.example/token"}
	if _, err := insertOperationsAuditRecordSQL(context.Background(), nil, event); err == nil {
		t.Fatal("URL evidence accepted")
	}
	event.EvidenceRef = "secret\x00"
	if _, err := insertOperationsAuditRecordSQL(context.Background(), nil, event); err == nil {
		t.Fatal("NUL audit accepted")
	}
	raw, _ := json.Marshal(&operationsPasswordProof{snapshot: passwordSnapshot{Hash: []byte("sensitive")}, sessionHash: []byte("sensitive")})
	if string(raw) != "{}" {
		t.Fatal("password proof serializable")
	}
	a, s := testApp(t, Config{})
	if a.operationsCapabilities(nil).Enabled {
		t.Fatal("demo capability enabled")
	}
	code, data, _ := authRequest(t, authClient(t), s.URL, "GET", "/api/v1/operations/access", "", "", nil)
	if code != 409 || !bytes.Contains(data, []byte("OPERATIONS_DISABLED")) {
		t.Fatal(code, string(data))
	}
}

func operationsFixture(t *testing.T) (*App, *httptest.Server, *http.Client, testIdentity, string) {
	t.Helper()
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required", OperationsEnabled: true})
	c := authClient(t)
	id := registerFixture(t, c, s.URL, "operations-owner@example.test")
	return a, s, c, id, db
}
func operationsRequest(t *testing.T, c *http.Client, s *httptest.Server) *http.Request {
	t.Helper()
	r := httptest.NewRequest("POST", s.URL+"/api/v1/operations/test", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	for _, cookie := range c.Jar.Cookies(r.URL) {
		r.AddCookie(cookie)
	}
	return r
}
func operationsGrantFixture(t *testing.T, a *App, user, workspace, role string) string {
	t.Helper()
	id := newID("opg")
	securityExec(t, a, `INSERT INTO studio_operations_grants(id,user_id,workspace_id,role) VALUES($1,$2,$3,$4)`, id, user, workspace, role)
	return id
}
func operationsCheckFixture(t *testing.T, a *App, p *principal, workspace, permission string, proof *operationsPasswordProof) error {
	t.Helper()
	return a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		return checkOperationsPermissionSQL(ctx, tx, p, workspace, permission, proof)
	})
}

func TestPostgresOperationsAccessScopeAndLiveRevocation(t *testing.T) {
	a, s, alice, id, _ := operationsFixture(t)
	bob := authClient(t)
	other := registerFixture(t, bob, s.URL, "operations-target@example.test")
	r := operationsRequest(t, alice, s)
	if _, err := a.requireOperations(r, id.Workspace.ID, "read"); err == nil {
		t.Fatal("owner implicitly privileged")
	}
	code, data, _ := authRequest(t, alice, s.URL, "GET", "/api/v1/operations/access", "", "", nil)
	if code != 200 || !bytes.Contains(data, []byte(`"access":[]`)) {
		t.Fatal("default grants", code, string(data))
	}
	observer := operationsGrantFixture(t, a, id.User.ID, other.Workspace.ID, "observer")
	if _, err := a.requireOperations(r, other.Workspace.ID, "read"); err != nil {
		t.Fatal("explicit cross-workspace read failed", err)
	}
	for _, scope := range []string{id.Workspace.ID, newID("ws"), "ws_bad\x00"} {
		if _, err := a.requireOperations(r, scope, "read"); err == nil {
			t.Fatal("ungranted/malformed scope accepted")
		}
	}
	if _, err := a.requireOperations(r, other.Workspace.ID, "case_open"); err == nil {
		t.Fatal("observer may mutate")
	}
	operationsGrantFixture(t, a, id.User.ID, other.Workspace.ID, "finance")
	if _, err := a.requireOperations(r, other.Workspace.ID, "case_open"); err != nil {
		t.Fatal("finance cannot open case", err)
	}
	if _, err := a.requireOperations(r, other.Workspace.ID, "finance"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.requireOperations(r, other.Workspace.ID, "investigate"); err == nil {
		t.Fatal("finance inherits investigation")
	}
	operator := operationsGrantFixture(t, a, id.User.ID, other.Workspace.ID, "operator")
	if _, err := a.requireOperations(r, other.Workspace.ID, "investigate"); err != nil {
		t.Fatal("multi-role union failed", err)
	}
	securityExec(t, a, "UPDATE studio_operations_grants SET active=false,version=version+1 WHERE id=$1 OR id=$2", operator, observer)
	if _, err := a.requireOperations(r, other.Workspace.ID, "investigate"); err == nil {
		t.Fatal("role cached past revoke")
	}
	if _, err := a.requireOperations(operationsRequest(t, bob, s), other.Workspace.ID, "read"); err == nil {
		t.Fatal("target owner acquired actor grants")
	}
	code, data, _ = authRequest(t, alice, s.URL, "GET", "/api/v1/operations/access", "", "", nil)
	access := decode[OperationsCapabilities](t, data)
	if code != 200 || len(access.Access) != 1 || access.Access[0].Role != "finance" || access.Access[0].WorkspaceID != other.Workspace.ID {
		t.Fatal(code, string(data))
	}
	if a.store.failed.Load() {
		t.Fatal("untrusted scope poisoned DB")
	}
}

func TestPostgresOperationsPasswordProofFencesAndBounds(t *testing.T) {
	a, s, c, id, _ := operationsFixture(t)
	grant := operationsGrantFixture(t, a, id.User.ID, id.Workspace.ID, "finance")
	r := operationsRequest(t, c, s)
	p, err := a.currentPrincipal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"short", strings.Repeat("a", 73), "long-password\x00"} {
		if _, err = a.verifyOperationsPassword(r, password); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if _, err = a.verifyOperationsPassword(r, "wrong-password-123"); err == nil {
		t.Fatal("wrong password accepted")
	}
	for i := 0; i < cap(a.bcryptSlots); i++ {
		a.bcryptSlots <- struct{}{}
	}
	_, busy := a.verifyOperationsPassword(r, "fixture-password-123")
	for i := 0; i < cap(a.bcryptSlots); i++ {
		<-a.bcryptSlots
	}
	if busy == nil {
		t.Fatal("bcrypt unbounded")
	}
	proof, err := a.verifyOperationsPassword(r, "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if err = operationsCheckFixture(t, a, p, id.Workspace.ID, "finance", proof); err != nil {
		t.Fatal(err)
	}
	expired := *proof
	expired.verifiedAt = time.Now().Add(-6 * time.Minute)
	if err = operationsCheckFixture(t, a, p, id.Workspace.ID, "finance", &expired); err == nil {
		t.Fatal("expired proof accepted")
	}
	foreign := *p
	foreign.SessionID = newID("ses")
	if err = operationsCheckFixture(t, a, &foreign, id.Workspace.ID, "finance", proof); err == nil {
		t.Fatal("proof crossed session")
	}
	securityExec(t, a, "UPDATE studio_operations_grants SET active=false,version=version+1 WHERE id=$1", grant)
	if err = operationsCheckFixture(t, a, p, id.Workspace.ID, "finance", proof); err == nil {
		t.Fatal("revoked grant passed proof")
	}
	securityExec(t, a, "UPDATE studio_operations_grants SET active=true,version=version+1 WHERE id=$1", grant)
	changedHash, err := bcrypt.GenerateFromPassword([]byte("fixture-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	securityExec(t, a, "UPDATE studio_users SET password_hash=$2 WHERE id=$1", id.User.ID, changedHash)
	if err = operationsCheckFixture(t, a, p, id.Workspace.ID, "finance", proof); err == nil {
		t.Fatal("changed password hash passed proof")
	}
	proof, err = a.verifyOperationsPassword(r, "fixture-password-123")
	if err != nil {
		t.Fatal(err)
	}
	securityExec(t, a, "UPDATE studio_users SET credential_version=credential_version+1 WHERE id=$1", id.User.ID)
	if err = operationsCheckFixture(t, a, p, id.Workspace.ID, "finance", proof); err == nil {
		t.Fatal("revoked credential passed proof")
	}
	if a.store.failed.Load() {
		t.Fatal("auth failures poisoned DB")
	}
}

func TestPostgresOperationsAuditAtomicAndClosed(t *testing.T) {
	a, _, _, id, _ := operationsFixture(t)
	event := OperationsAuditEvent{ActorUserID: id.User.ID, WorkspaceID: id.Workspace.ID, Action: "case.created", CaseID: newID("opc"), ResourceType: "case", ReasonCode: "manual_review", EvidenceRef: "ticket:fixture", Version: 1}
	var audit OperationsAuditRecord
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		audit, e = insertOperationsAuditRecordSQL(ctx, tx, event)
		return e
	})
	if err != nil || audit.ID == "" || audit.CreatedAt.IsZero() {
		t.Fatal(audit, err)
	}
	var count int
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT count(*) FROM studio_operations_audit WHERE id=$1", audit.ID).Scan(&count)
	})
	if err != nil || count != 0 {
		t.Fatal("audit escaped rollback", count, err)
	}
}
