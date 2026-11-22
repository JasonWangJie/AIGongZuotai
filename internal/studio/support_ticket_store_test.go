package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSupportTicketTextAndProjection(t *testing.T) {
	in := SupportTicketCreate{"other", strings.Repeat("题", 120), strings.Repeat("文", 2000)}
	if !validSupportCreate(in) || validSupportText(in.Subject+"a", 120, 480, false) || validSupportText(in.Body+"文", 2000, 6000, true) ||
		validSupportText(strings.Repeat("🎨", 1501), 2000, 6000, true) || validSupportText("\t\n", 2000, 6000, true) ||
		validSupportText("原文\x00", 2000, 6000, true) || validSupportText("跨\n行", 120, 480, false) {
		t.Fatal("text input boundary")
	}
	b, e := json.Marshal(SupportTicketPage{Items: []SupportTicketCard{{ID: "ticket", Subject: "主题"}}})
	if e != nil || bytes.Contains(b, []byte("body")) || bytes.Contains(b, []byte("messages")) || bytes.Contains(b, []byte("owner_user_id")) {
		t.Fatal("list disclosed private body or identity")
	}
}
func expectSupportFailure(t *testing.T, err error, code string) {
	t.Helper()
	var op *operationError
	if !errors.As(err, &op) || op.code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func TestPostgresSupportTicketScopeRepliesReceiptsAndRevocation(t *testing.T) {
	a, s, ownerClient, one, db := operationsFixture(t)
	staffClient, otherClient := authClient(t), authClient(t)
	staffID := registerFixture(t, staffClient, s.URL, "support-staff@example.test")
	registerFixture(t, otherClient, s.URL, "support-unrelated@example.test")
	owner, e := a.currentPrincipal(operationsRequest(t, ownerClient, s))
	if e != nil {
		t.Fatal(e)
	}
	staff, e := a.currentPrincipal(operationsRequest(t, staffClient, s))
	if e != nil {
		t.Fatal(e)
	}
	other, e := a.currentPrincipal(operationsRequest(t, otherClient, s))
	if e != nil {
		t.Fatal(e)
	}
	w := one.Workspace.ID
	in := SupportTicketCreate{"generation", "查询已提交结果", "本人提交的正文哨兵"}
	original, rep, e := a.createSupportTicket(owner, "support-scope-create", in)
	if e != nil || rep || original.Version != 1 || original.State != "OPEN" || original.Messages[0].Body != in.Body {
		t.Fatal("creation", e)
	}
	_, e = a.supportTicket(other, other.Workspace.ID, original.ID, false)
	expectSupportFailure(t, e, "SUPPORT_TICKET_NOT_FOUND")
	securityExec(t, a, "UPDATE studio_sessions SET workspace_id=$1 WHERE id=$2", w, other.SessionID)
	other, e = a.currentPrincipal(operationsRequest(t, otherClient, s))
	if e != nil {
		t.Fatal(e)
	}
	if page, e := a.supportTickets(other, w, false, ""); e != nil || len(page.Items) != 0 {
		t.Fatal("same workspace data leak", e)
	}
	_, e = a.supportTicket(other, w, original.ID, false)
	expectSupportFailure(t, e, "SUPPORT_TICKET_NOT_FOUND")
	_, e = a.supportTicketReceipt(other, w, "create", "support-scope-create")
	expectSupportFailure(t, e, "SUPPORT_TICKET_NOT_FOUND")
	for _, r := range []string{"observer", "operator", "moderator", "finance", "risk_reviewer"} {
		operationsGrantFixture(t, a, staffID.User.ID, w, r)
	}
	if _, e = a.supportTickets(staff, w, true, ""); e == nil {
		t.Fatal("legacy roles inherited support authority")
	}
	grant := operationsGrantFixture(t, a, staffID.User.ID, w, "support")
	if _, e = a.supportTickets(staff, staff.Workspace.ID, true, ""); e == nil {
		t.Fatal("another workspace allowed")
	}
	page, e := a.supportTickets(staff, w, true, "")
	if e != nil || len(page.Items) != 1 {
		t.Fatal("scoped staff queue", e)
	}
	b, _ := json.Marshal(page)
	if bytes.Contains(b, []byte(in.Body)) {
		t.Fatal("queue contains correspondence")
	}
	proof, e := a.verifyOperationsPassword(operationsRequest(t, staffClient, s), "fixture-password-123")
	if e != nil {
		t.Fatal(e)
	}
	reply := SupportTicketAction{1, "对应工作区已收到，本次只作公开答复。"}
	_, _, e = a.actOnSupportTicket(staff, w, original.ID, "reply", "support-scope-reply", reply)
	expectSupportFailure(t, e, "OPERATIONS_REAUTH_REQUIRED")
	replied, rep, e := a.actOnSupportTicket(staff, w, original.ID, "reply", "support-scope-reply", reply, proof)
	if e != nil || rep || replied.Version != 2 || replied.Messages[1].Kind != "reply" || replied.Messages[1].Body != reply.Body {
		t.Fatal("reply", e)
	}
	_, _, e = a.actOnSupportTicket(owner, w, original.ID, "followup", "support-stale", SupportTicketAction{1, "迟到"})
	expectSupportFailure(t, e, "SUPPORT_TICKET_CHANGED")
	closed, rep, e := a.actOnSupportTicket(owner, w, original.ID, "close", "support-scope-close", SupportTicketAction{ExpectedVersion: 2})
	if e != nil || rep || closed.State != "CLOSED" || closed.Messages[2].Kind != "closed" {
		t.Fatal("close", e)
	}
	got, rep, e := a.createSupportTicket(owner, "support-scope-create", in)
	if e != nil || !rep || !reflect.DeepEqual(got, original) {
		t.Fatal("original creation receipt was rewritten", e)
	}
	got, e = a.supportTicketReceipt(staff, w, "reply", "support-scope-reply")
	if e != nil || !reflect.DeepEqual(got, replied) {
		t.Fatal("original reply receipt was rewritten", e)
	}
	changed := reply
	changed.Body = "不同原文"
	_, _, e = a.actOnSupportTicket(staff, w, original.ID, "reply", "support-scope-reply", changed, proof)
	expectSupportFailure(t, e, "IDEMPOTENCY_CONFLICT")
	admin := deliveryAdmin(t, db)
	ctx := context.Background()
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "UPDATE studio_operations_grants SET active=false,version=version+1 WHERE id=$1", grant); e != nil {
		t.Fatal(e)
	}
	delayed := make(chan error, 1)
	go func() {
		_, _, e := a.actOnSupportTicket(staff, w, original.ID, "reply", "support-scope-reply", reply, proof)
		delayed <- e
	}()
	generationWaitWorkspaceBlock(t, tx, a.store.conn.PgConn().PID())
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-delayed; e == nil {
		t.Fatal("revoked grant disclosed old POST response")
	}
	if _, e = a.supportTicket(staff, w, original.ID, true); e == nil {
		t.Fatal("revoked staff read private messages")
	}
	if _, e = a.supportTicketReceipt(staff, w, "reply", "support-scope-reply"); e == nil {
		t.Fatal("revoked staff read receipt")
	}
	if got, e = a.supportTicket(owner, w, original.ID, false); e != nil || !reflect.DeepEqual(got, closed) {
		t.Fatal("owner lost public response", e)
	}
	if _, e = admin.Exec(ctx, "UPDATE studio_support_messages SET body='changed' WHERE ticket_id=$1 AND version=2", original.ID); e == nil {
		t.Fatal("history was mutable")
	}
	if _, e = admin.Exec(ctx, "DELETE FROM studio_support_receipts WHERE ticket_id=$1", original.ID); e == nil {
		t.Fatal("receipt was mutable")
	}
}

func TestPostgresSupportTicketCapacityHistoryAndCAS(t *testing.T) {
	a, s, client, account, _ := operationsFixture(t)
	p, e := a.currentPrincipal(operationsRequest(t, client, s))
	if e != nil {
		t.Fatal(e)
	}
	w := account.Workspace.ID
	in := SupportTicketCreate{"other", "容量测试", "完整正文"}
	for i := 0; i < 7; i++ {
		if _, _, e = a.createSupportTicket(p, newID("key"), in); e != nil {
			t.Fatal(e)
		}
	}
	type result struct {
		ticket SupportTicketDetail
		err    error
	}
	ch := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { d, _, e := a.createSupportTicket(p, newID("key"), in); ch <- result{d, e} }()
	}
	first, second := <-ch, <-ch
	if first.err != nil {
		first, second = second, first
	}
	if first.err != nil {
		t.Fatal(first.err)
	}
	expectSupportFailure(t, second.err, "SUPPORT_TICKET_OPEN_LIMIT")
	id := first.ticket.ID
	for i := 0; i < 2; i++ {
		go func() {
			d, _, e := a.actOnSupportTicket(p, w, id, "followup", newID("key"), SupportTicketAction{1, "补充"})
			ch <- result{d, e}
		}()
	}
	first, second = <-ch, <-ch
	if first.err != nil {
		first, second = second, first
	}
	if first.err != nil || first.ticket.Version != 2 {
		t.Fatal("competing action", first.err)
	}
	expectSupportFailure(t, second.err, "SUPPORT_TICKET_CHANGED")
	for version := uint64(2); version < 31; version++ {
		if _, _, e = a.actOnSupportTicket(p, w, id, "followup", newID("key"), SupportTicketAction{version, "补充"}); e != nil {
			t.Fatal(version, e)
		}
	}
	_, _, e = a.actOnSupportTicket(p, w, id, "followup", newID("key"), SupportTicketAction{31, "满额时追加"})
	expectSupportFailure(t, e, "SUPPORT_TICKET_MESSAGE_LIMIT")
	closed, _, e := a.actOnSupportTicket(p, w, id, "close", newID("key"), SupportTicketAction{ExpectedVersion: 31})
	if e != nil || closed.Version != 32 || len(closed.Messages) != 32 {
		t.Fatal("reserved close capacity", e)
	}
	for i := 0; i < 18; i++ {
		d, _, e := a.createSupportTicket(p, newID("key"), in)
		if e != nil {
			t.Fatal("closed history occupied capacity", e)
		}
		if _, _, e = a.actOnSupportTicket(p, w, d.ID, "close", newID("key"), SupportTicketAction{ExpectedVersion: 1}); e != nil {
			t.Fatal(e)
		}
	}
	page, e := a.supportTickets(p, w, false, "")
	if e != nil || len(page.Items) != 20 || page.NextCursor == nil {
		t.Fatal("history page", e)
	}
	more, e := a.supportTickets(p, w, false, *page.NextCursor)
	if e != nil || len(more.Items) != 6 || more.NextCursor != nil {
		t.Fatal("history remainder", e)
	}
	got, e := a.supportTicket(p, w, id, false)
	if e != nil || got.Version != 32 {
		t.Fatal("closed history unavailable", e)
	}
}
