package studio

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestChatAncestryBudgetMatchesWireValidation(t *testing.T) {
	for _, window := range []int{2048, 32768, 1048576} {
		model := catalogEntry{ChatOutputLimitField: "max_tokens", Model: Model{Capabilities: ModelCapabilities{ContextWindowTokens: window, MaxOutputTokens: 128}}}
		for _, n := range []int{1, 3, 127, 129, 131} {
			for _, size := range []int{n, window - 128 - ChatSafetyReserve - n*ChatMessageOverhead, window - 127 - ChatSafetyReserve - n*ChatMessageOverhead, MaxChatContentBytes, MaxChatContentBytes + 1} {
				if size < n {
					continue
				}
				messages := make([]ChatMessage, n)
				for i := range messages {
					role := "user"
					if i%2 == 1 {
						role = "assistant"
					}
					messages[i] = ChatMessage{Role: role, Content: "x"}
				}
				messages[0].Content = strings.Repeat("x", size-n+1)
				req := ChatRequest{Messages: messages, MaxOutputTokens: 128, OutputLimitField: "max_tokens"}
				if chatPairFits(size, n, model) != (ValidateChatRequest(req, window) == nil) {
					t.Fatalf("mismatched wire budget size=%d messages=%d window=%d", size, n, window)
				}
			}
		}
	}
}

func TestPostgresConversationAncestryUpgradeAndBoundedContext(t *testing.T) {
	db := testDatabase(t)
	conn := deliveryAdmin(t, db)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, workflowOldMigrations(t, "035")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := Conversation{ID: newID("conv"), WorkspaceID: "ws_ancestry", ProjectID: "prj_ancestry", Version: 1}
	for _, statement := range []string{
		"INSERT INTO studio_workspaces(id,name) VALUES('ws_ancestry','fixture')",
		"INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_ancestry','ws_ancestry','fixture')",
	} {
		if _, err = tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_conversations(id,workspace_id,project_id,title,created_at,updated_at) VALUES($1,$2,$3,'fixture',$4,$4)`, c.ID, c.WorkspaceID, c.ProjectID, now); err != nil {
		t.Fatal(err)
	}
	add := func(parent *string, status, output, decision string) (chatNode, chatNode) {
		t.Helper()
		run := Run{ID: newID("run"), WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, ConversationID: c.ID, Status: status, Output: output}
		if decision != "" {
			run.Moderation = &ModerationSummary{State: decision}
		}
		u := chatNode{WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, MessageNode: MessageNode{ID: newID("msg"), ParentID: parent, ConversationID: c.ID, Role: "user", Content: fmt.Sprintf("问题%03d", c.NodeCount/2+1), CreatedAt: now}}
		a := chatNode{WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, MessageNode: MessageNode{ID: newID("msg"), ConversationID: c.ID, ParentID: &u.ID, RunID: &run.ID, Role: "assistant", CreatedAt: now}}
		if _, err := tx.Exec(ctx, `INSERT INTO studio_runs(id,workspace_id,project_id,conversation_id,document) VALUES($1,$2,$3,$4,$5)`, run.ID, c.WorkspaceID, c.ProjectID, c.ID, run); err != nil {
			t.Fatal(err)
		}
		if err := insertChatNodeSQL(ctx, tx, u); err != nil {
			t.Fatal(err)
		}
		if err := insertChatNodeSQL(ctx, tx, a); err != nil {
			t.Fatal(err)
		}
		c.NodeCount += 2
		c.Version++
		c.HeadMessageID = &a.ID
		if _, err = tx.Exec(ctx, "UPDATE studio_conversations SET node_count=$2,version=$3,head_message_id=$4 WHERE id=$1", c.ID, c.NodeCount, c.Version, a.ID); err != nil {
			t.Fatal(err)
		}
		return u, a
	}
	var included []string
	var legacyRoot string
	for i := range 64 {
		status, decision := "COMPLETED", ""
		if i == 63 {
			status = "CANCELLED"
		} else if i%2 == 1 {
			decision = "ALLOWED"
		}
		u, a := add(c.HeadMessageID, status, fmt.Sprintf("输出%03d", i+1), decision)
		if i == 0 {
			legacyRoot = u.ID
		} else {
			included = append(included, u.ID, a.ID)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	unchanged := []string{"studio_conversations", "studio_chat_messages", "studio_runs"}
	before := schedulerOriginalSnapshot(t, conn, unchanged)
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrations(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after := schedulerOriginalSnapshot(t, conn, unchanged)
	for _, name := range unchanged {
		if before[name] != after[name] {
			t.Fatal("migration changed existing conversations or runs")
		}
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var depth int
	if err = tx.QueryRow(ctx, "SELECT depth FROM studio_chat_ancestry WHERE message_id=$1", *c.HeadMessageID).Scan(&depth); err != nil || depth != 128 {
		t.Fatal("old leaf depth did not backfill")
	}
	oldLeaf := *c.HeadMessageID
	var quarantined chatNode
	for i := range 70 {
		if i%2 == 0 {
			_, quarantined = add(c.HeadMessageID, "FAILED", "仅用于验证门禁的合成正文", "BLOCKED")
		} else {
			add(c.HeadMessageID, "INTERRUPTED", "", "")
		}
	}
	blocked := *c.HeadMessageID
	u, a := add(c.HeadMessageID, "COMPLETED", "末轮", "ALLOWED")
	included = append(included, u.ID, a.ID)
	_, other := add(&oldLeaf, "COMPLETED", "另一个分支", "")
	model := catalogEntry{UpstreamModel: "fixture", ChatOutputLimitField: "max_tokens", Model: Model{Capabilities: ModelCapabilities{ContextWindowTokens: 32768, MaxOutputTokens: 128}}}
	build := func(leaf, op string, long bool) (*chatExecution, error) {
		in := conversationInput(c.Version, &leaf, "继续提问")
		in.Operation = op
		_, _, _, ex, err := preparePersistentConversationTurn(ctx, tx, c, in, in.Input, model, long)
		return ex, err
	}
	ex, err := build(a.ID, "reply", true)
	if err != nil || !slices.Equal(ex.SourceMessageIDs, included) || ex.Summary.IncludedMessages != 129 || ex.Summary.OmittedMessages != 142 {
		t.Fatal("bounded context is inconsistent with selected 135-turn branch", err)
	}
	if ex.Request.Messages[125].Role != "assistant" || ex.Request.Messages[125].Content != "输出064" || ex.Request.Messages[127].Content != "末轮" {
		t.Fatal("incorrect ordering of original model messages")
	}
	h, err := persistentChatPathSQL(ctx, tx, c, a.ID, "继续提问", model)
	if err != nil || len(h.runs) != 64 || h.depth != 270 {
		t.Fatal("wrong hydration bound or selected depth", err)
	}
	h, err = persistentChatPathSQL(ctx, tx, c, blocked, "新问题", model)
	if err != nil || h.depth != 268 || len(h.nodes) != 130 {
		t.Fatal("omitted direct parent did not preserve the latest 64 public turns", err)
	}
	h, err = persistentChatPathSQL(ctx, tx, c, quarantined.ID, "新问题", model)
	if err != nil || h.nodes[1].Content != "" || h.runs[*quarantined.RunID].Output != "" {
		t.Fatal("restricted direct pair was hydrated", err)
	}
	_, err = build(blocked, "continue", true)
	requireOperation(t, err, "CHAT_CONTINUE_UNAVAILABLE")
	_, err = build(a.ID, "reply", false)
	requireOperation(t, err, "CONVERSATION_LIMIT")
	ids, err := chatMessagePageIDsSQL(ctx, tx, c, conversationCursor{Leaf: a.ID, BeforeID: legacyRoot}, 2)
	if err != nil || !slices.Equal(ids, []string{legacyRoot}) {
		t.Fatal("deep original ancestor was not readable", err)
	}
	_, err = chatMessagePageIDsSQL(ctx, tx, c, conversationCursor{Leaf: a.ID, BeforeID: other.ID}, 2)
	requireOperation(t, err, "INVALID_CHAT_CURSOR")
	_, err = chatMessagePageIDsSQL(ctx, tx, c, conversationCursor{Leaf: other.ID, BeforeID: blocked}, 2)
	requireOperation(t, err, "INVALID_CHAT_CURSOR")
	_, spaces := add(&other.ID, "CANCELLED", " \t\n", "")
	_, err = build(spaces.ID, "continue", true)
	requireOperation(t, err, "CHAT_CONTINUE_UNAVAILABLE")
	valid, err := build(spaces.ID, "reply", true)
	if err != nil || valid.Request.Messages[len(valid.Request.Messages)-2].Content != " \t\n" {
		t.Fatal("ordinary reply changed nonempty whitespace history", err)
	}
	_, oversized := add(&spaces.ID, "COMPLETED", strings.Repeat("x", 2500), "")
	model.Model.Capabilities.ContextWindowTokens = 2048
	_, err = build(oversized.ID, "continue", true)
	requireOperation(t, err, "CONTEXT_TOO_LARGE")
	later, err := build(oversized.ID, "reply", true)
	if err != nil || len(later.SourceMessageIDs) != 0 {
		t.Fatal("oversized most recent round was skipped in favor of an older one", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
