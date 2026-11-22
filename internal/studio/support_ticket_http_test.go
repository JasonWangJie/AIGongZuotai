package studio

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupportTicketHTTPExactFieldsAndOfflineRole(t *testing.T) {
	cfg := OperationsAdminConfig{Action: "grant", WorkspaceID: newID("ws"), UserID: newID("usr"), Role: "support",
		OperationKey: "support-role-001", Reason: "staff_assignment", EvidenceRef: "ticket-001"}
	if err := validateOperationsAdmin(cfg); err != nil {
		t.Fatal("offline grant rejected the explicit workspace support role", err)
	}
	cfg.WorkspaceID = ""
	if validateOperationsAdmin(cfg) == nil {
		t.Fatal("unscoped support role accepted")
	}
	var in SupportTicketCreate
	read := func(blob, media string, dst any, fields ...string) error {
		r := httptest.NewRequest("POST", "/api/v1/support/tickets", strings.NewReader(blob))
		r.Header.Set("Content-Type", media)
		return decodeSupportTicketInput(httptest.NewRecorder(), r, dst, fields...)
	}
	const header = `{"category":"other","subject":"工单","body":`
	const media = "application/json"
	create := func(blob string) error { return read(blob, media, &in, "category", "subject", "body") }
	if err := create(header + `"` + strings.Repeat(`\u4e2d`, 2000) + `"}`); err != nil || !validSupportCreate(in) {
		t.Fatal("valid six-thousand-byte UTF-8 message failed after escaping", err)
	}
	for _, raw := range []string{
		header + `"a","password":"unexpected"}`, header + `"a","workspace_id":"other"}`, header + `"a","actor_user_id":"other"}`,
		header + `"a","body":"b"}`, header + `null}`, header + `"a"}{}`, `{"Category":"other","subject":"x","body":"y"}`,
		string([]byte{0xff}),
	} {
		if draftErrorCode(create(raw)) != "INVALID_JSON" {
			t.Fatal("invalid create wire shape was accepted")
		}
	}
	var action SupportTicketAction
	if draftErrorCode(read(`{"expected_version":1,"body":"unexpected"}`, media, &action, "expected_version")) != "INVALID_JSON" ||
		draftErrorCode(read(`{"expected_version":1}`, "text/plain", &action, "expected_version")) != "JSON_REQUIRED" ||
		draftErrorCode(create(strings.Repeat(" ", (32<<10)+1))) != "SUPPORT_BODY_TOO_LARGE" {
		t.Fatal("support action field, media type or byte bound was widened")
	}
	_, s := testApp(t, Config{})
	code, _, _ := request(t, s, "GET", "/api/v1/support/tickets", "", nil)
	if code != 501 {
		t.Fatal("memory mode offered private support records", code)
	}
}

func TestPostgresSupportTicketHTTPScopedReplyPasswordAndOwnerReceipts(t *testing.T) {
	a, s := testApp(t, Config{DatabaseURL: testDatabase(t), AuthMode: "required", OperationsEnabled: true})
	owner, staff, other := authClient(t), authClient(t), authClient(t)
	buyer := registerFixture(t, owner, s.URL, "support-owner@example.test")
	agent := registerFixture(t, staff, s.URL, "support-agent@example.test")
	outsider := registerFixture(t, other, s.URL, "support-observer@example.test")
	workspace := buyer.Workspace.ID
	grant := operationsGrantFixture(t, a, agent.User.ID, workspace, "support")
	operationsGrantFixture(t, a, outsider.User.ID, workspace, "observer")
	const own = "/api/v1/support/tickets"
	const desk = "/api/v1/operations/support/tickets"
	const password = "fixture-password-123"
	const key = "first-support-ticket"
	ask := func(c *http.Client, method, route, key string, body any, status int) ([]byte, http.Header) {
		return presentationRequest(t, c, s.URL, "", method, route, key, body, status)
	}
	ask(authClient(t), "GET", own, "", nil, 401)
	first, _ := ask(owner, "POST", own, key, SupportTicketCreate{Category: "other", Subject: "原始问题", Body: "此为工单私有正文"}, 201)
	ticket := decode[SupportTicketDetail](t, first)
	if ticket.Version != 1 || len(ticket.Messages) != 1 || ticket.Messages[0].Kind != "created" {
		t.Fatal("missing original support request")
	}
	scope := "?workspace_id=" + workspace
	ask(other, "GET", own+"/"+ticket.ID, "", nil, 404)
	ask(other, "GET", desk+"/"+ticket.ID+scope, "", nil, 404)
	ask(staff, "GET", desk+"/"+ticket.ID+"?workspace_id="+outsider.Workspace.ID, "", nil, 404)
	ask(staff, "GET", "/api/v1/operations/cases"+scope, "", nil, 404)
	cards, _ := ask(staff, "GET", desk+scope, "", nil, 200)
	if bytes.Contains(cards, []byte("工单私有正文")) || len(decode[SupportTicketPage](t, cards).Items) != 1 {
		t.Fatal("queue exposed message text or lost the scoped card")
	}
	const replyKey = "original-support-reply"
	replyPath := desk + "/" + ticket.ID + "/reply"
	reply := func(pw string) map[string]any {
		return map[string]any{"workspace_id": workspace, "password": pw, "expected_version": 1, "body": "已阅读原始问题"}
	}
	bad, _ := ask(staff, "POST", replyPath, replyKey, reply("incorrect-password"), 403)
	if !bytes.Contains(bad, []byte("OPERATIONS_PASSWORD_INVALID")) {
		t.Fatal("reply did not check current password")
	}
	readReply := desk + "/receipt" + scope + "&operation=reply"
	ask(staff, "GET", readReply, replyKey, nil, 404)
	raw, _ := ask(staff, "POST", replyPath, replyKey, reply(password), 200)
	message := decode[SupportTicketDetail](t, raw)
	if message.Version != 2 || len(message.Messages) != 2 || message.Messages[1].Kind != "reply" ||
		bytes.Contains(raw, []byte(password)) || bytes.Contains(raw, []byte(agent.User.ID)) {
		t.Fatal("reply response lost the message or exposed actor credentials")
	}
	ask(staff, "POST", replyPath, replyKey, reply("incorrect-password"), 403)
	raw, _ = ask(staff, "GET", readReply, replyKey, nil, 200)
	if decode[SupportTicketDetail](t, raw).Version != 2 {
		t.Fatal("reply receipt was not readable without submitting a second password")
	}
	ask(staff, "GET", readReply, replyKey, map[string]string{"password": "must-not-be-used"}, 400)
	ask(owner, "POST", own+"/"+ticket.ID+"/followup", "conflicting-followup", SupportTicketAction{ExpectedVersion: 1, Body: "并发修改"}, 409)
	ask(owner, "POST", own+"/"+ticket.ID+"/followup", "owner-followup-key", SupportTicketAction{ExpectedVersion: 2, Body: "谢谢答复"}, 200)
	closed, _ := ask(owner, "POST", own+"/"+ticket.ID+"/close", "owner-close-key", map[string]int{"expected_version": 3}, 200)
	if got := decode[SupportTicketDetail](t, closed); got.State != "CLOSED" || got.Version != 4 || len(got.Messages) != 4 {
		t.Fatal("close did not preserve the dialogue")
	}
	original, _ := ask(owner, "GET", own+"/receipt?operation=create", key, nil, 200)
	if got := decode[SupportTicketDetail](t, original); got.Version != 1 || got.State != "OPEN" || len(got.Messages) != 1 {
		t.Fatal("receipt read included later correspondence")
	}
	securityExec(t, a, "UPDATE studio_operations_grants SET active=false,version=version+1 WHERE id=$1", grant)
	ask(staff, "GET", readReply, replyKey, nil, 404)
	ask(staff, "GET", desk+"/"+ticket.ID+scope, "", nil, 404)
	ask(staff, "POST", replyPath, replyKey, reply(password), 404)
	ask(owner, "GET", own+"/"+ticket.ID, "", nil, 200)
}
