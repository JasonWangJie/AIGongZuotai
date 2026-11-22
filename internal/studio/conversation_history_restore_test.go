package studio

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// Build a branch beyond the original default at the normal admission endpoint.
// Restore the stopped database and both private directories with the large-write
// flag off, then compare original pages and replay the original acceptance.
func TestPostgresLongHistoryPairedRestoreWithOriginalLimits(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	f.cfg.LongConversationHistory = true
	a, s := f.open(t)
	client := authClient(t)
	const email = "history-restore@example.test"
	owner := registerFixture(t, client, s.URL, email)
	c := createPGConversation(t, client, s, owner)
	var parent *string
	var lastInput ConversationRunInput
	var last Run
	const total = 66
	route := "/api/v1/conversations/" + c.ID + "/runs"
	for n := 1; n <= total; n++ {
		lastInput = conversationInput(uint64(n), parent, fmt.Sprintf("restore turn %d", n))
		raw, _ := presentationRequest(t, client, s.URL, owner.Project.ID, "POST", route, fmt.Sprintf("restore-turn-%d", n), lastInput, 201)
		accepted := decode[Run](t, raw)
		last = waitConversationRun(t, a, accepted.ID)
		if last.Status != "COMPLETED" || last.Output != workflowTestText {
			t.Fatal("required original reply was not committed")
		}
		// Each input retains its own immutable parent, including the replay below.
		next := last.AssistantMessageID
		parent = &next
	}
	messages := "/api/v1/conversations/" + c.ID + "/messages?limit=20&leaf_id=" + last.AssistantMessageID
	pages := func(session *http.Client, base string) [][]byte {
		t.Helper()
		var out [][]byte
		cursor := ""
		var nodes int
		for i := 0; i < 7; i++ {
			raw, _ := presentationRequest(t, session, base, owner.Project.ID, "GET", messages+"&before="+url.QueryEscape(cursor), "", nil, 200)
			p := decode[conversationMessagePage](t, raw)
			if len(p.Items) != min(20, total*2-nodes) || p.HasMore != (i < 6) || (p.NextCursor != "") != p.HasMore {
				t.Fatal("unexpected original page boundary")
			}
			nodes += len(p.Items)
			out = append(out, raw)
			cursor = p.NextCursor
		}
		if nodes != total*2 {
			t.Fatal("lost messages on deep branch")
		}
		return out
	}
	original := pages(client, s.URL)
	s.Close()
	a.Close()
	restoreWorkflowFixture(t, f, "studio_conversations", "studio_chat_messages", "studio_chat_ancestry", "studio_chat_contexts", "studio_runs", "studio_idempotency", "studio_run_moderation", "studio_moderation_decisions")
	f.cfg.LongConversationHistory = false
	_, s = f.open(t)
	client = authClient(t)
	presentationRequest(t, client, s.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	for i, raw := range pages(client, s.URL) {
		if !bytes.Equal(raw, original[i]) {
			t.Fatal("original authorized page or cursor changed after restore", i)
		}
	}
	raw, headers := presentationRequest(t, client, s.URL, owner.Project.ID, "POST", route, fmt.Sprintf("restore-turn-%d", total), lastInput, 201)
	if headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("restored original key did not replay its existing receipt")
	}
	got := decode[Run](t, raw)
	if got.ID != last.ID || got.Output != last.Output || got.AssistantMessageID != last.AssistantMessageID {
		t.Fatal("old-key replay under default limits did not recover original run")
	}
	rejected, _ := presentationRequest(t, client, s.URL, owner.Project.ID, "POST", route, "restore-extra-new", conversationInput(total+1, parent, "new input after downgrade"), 429)
	if !bytes.Contains(rejected, []byte(`"CONVERSATION_LIMIT"`)) || f.chatCalls.Load() != total {
		t.Fatal("disabled large writes did not reject new growth before execution")
	}
}
