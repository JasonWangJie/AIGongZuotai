package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func addChatPageTurn(t *testing.T, a *App, server *httptest.Server, client *http.Client, p testIdentity, conv string, version uint64, parent *string) Run {
	t.Helper()
	code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/conversations/"+conv+"/runs",
		newID("key"), p.Project.ID, conversationInput(version, parent, "历史 <&> "+strconv.FormatUint(version, 10)))
	if code != 201 {
		t.Fatalf("could not append a fixture turn: %d", code)
	}
	return waitConversationRun(t, a, decode[Run](t, data).ID)
}

// Compute the original response independently from the pre-existing full-path
// readers. Compare the actual HTTP bytes, including cursor, escaping and order.
func TestPostgresConversationPageMatchesOriginalShortBranch(t *testing.T) {
	dsn := testDatabase(t)
	const restricted = "restricted-page-fixture"
	a, server, client, p := newModeratedExecutionFixture(t, dsn, []ModerationRule{{Phase: "output", Contains: restricted, Decision: "block"}})
	a.chat = &conversationChatFixture{output: "公开 <&> 回复"}
	c := createPGConversation(t, client, server, p)
	first := addChatPageTurn(t, a, server, client, p, c.ID, 1, nil)
	second := addChatPageTurn(t, a, server, client, p, c.ID, 2, &first.AssistantMessageID)
	a.chat = &conversationChatFixture{output: restricted}
	sibling := addChatPageTurn(t, a, server, client, p, c.ID, 3, &first.AssistantMessageID)
	a.chat = &conversationChatFixture{output: "公开 <&> 回复"}
	third := addChatPageTurn(t, a, server, client, p, c.ID, 4, &second.AssistantMessageID)
	if first.Status != "COMPLETED" || second.Status != "COMPLETED" || third.Status != "COMPLETED" ||
		sibling.Moderation == nil || sibling.Moderation.State != "BLOCKED" || sibling.Output != "" {
		t.Fatal("fixture did not produce the approved and quarantined branches")
	}
	ctx := context.Background()
	admin := deliveryAdmin(t, dsn)
	for _, leaf := range []string{third.AssistantMessageID, sibling.AssistantMessageID, second.UserMessageID} {
		path, err := readChatPathSQL(ctx, admin, p.Workspace.ID, p.Project.ID, c.ID, leaf)
		if err != nil {
			t.Fatal(err)
		}
		runs, err := readChatRunMapSQL(ctx, admin, p.Workspace.ID, p.Project.ID, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{1, 3, 40} {
			query := url.Values{"leaf_id": {leaf}, "limit": {strconv.Itoa(limit)}}
			for pos := 0; pos < len(path); {
				page := conversationMessagePage{Items: []MessageNode{}, LeafMessageID: &leaf, ConversationVersion: 5}
				size := 4096
				for pos < len(path) && len(page.Items) < limit {
					node, err := hydrateChatNode(path[pos], func(id string) (Run, error) { return runs[id], nil })
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(node)
					if err != nil {
						t.Fatal(err)
					}
					if size+len(raw)+1 > 1<<20 {
						break
					}
					size += len(raw) + 1
					page.Items = append(page.Items, node)
					pos++
				}
				if len(page.Items) == 0 {
					t.Fatal("fixture exceeds the wire budget")
				}
				if pos < len(path) {
					page.HasMore = true
					page.NextCursor = encodeChatCursor(conversationCursor{Version: 1, User: p.User.ID, Workspace: p.Workspace.ID,
						Project: p.Project.ID, Kind: "messages", Conversation: c.ID, Leaf: leaf, BeforeID: path[pos].ID})
				}
				slices.Reverse(page.Items)
				want, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				code, raw, _ := authRequest(t, client, server.URL, "GET", "/api/v1/conversations/"+c.ID+"/messages?"+query.Encode(), "", p.Project.ID, nil)
				if code != 200 || bytes.Contains(raw, []byte(restricted)) || !bytes.Equal(bytes.TrimSpace(raw), want) {
					t.Fatalf("page differs from original at limit %d, position %d, status %d", limit, pos, code)
				}
				query.Set("before", page.NextCursor)
			}
		}
	}
	forged := conversationCursor{Version: 1, User: p.User.ID, Workspace: p.Workspace.ID, Project: p.Project.ID,
		Kind: "messages", Conversation: c.ID, Leaf: third.AssistantMessageID, BeforeID: sibling.UserMessageID}
	query := url.Values{"leaf_id": {forged.Leaf}, "before": {encodeChatCursor(forged)}}
	code, data, _ := authRequest(t, client, server.URL, "GET", "/api/v1/conversations/"+c.ID+"/messages?"+query.Encode(), "", p.Project.ID, nil)
	if code != 400 || !bytes.Contains(data, []byte("INVALID_CHAT_CURSOR")) {
		t.Fatalf("cursor starting on a sibling was accepted: %d", code)
	}
}

type chatPageQueryTrace struct {
	bodyIDs []string
	queries int
}

func (trace *chatPageQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	trace.queries++
	if strings.Contains(q.SQL, "studio_runs") {
		// Capture only local message IDs in memory. An unbounded run query
		// or a different argument shape also fails the expected sequence.
		var id string
		if len(q.Args) != 0 {
			id, _ = q.Args[0].(string)
		}
		trace.bodyIDs = append(trace.bodyIDs, id)
	}
	return ctx
}

func (*chatPageQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPostgresConversationPageHydratesOnlyReturnedRowsAndByteBoundary(t *testing.T) {
	a, server, client, p, dsn := conversationPGFixture(t)
	a.chat = &conversationChatFixture{output: strings.Repeat("\x01", maxRunOutputBytes)}
	c := createPGConversation(t, client, server, p)
	first := addChatPageTurn(t, a, server, client, p, c.ID, 1, nil)
	second := addChatPageTurn(t, a, server, client, p, c.ID, 2, &first.AssistantMessageID)
	third := addChatPageTurn(t, a, server, client, p, c.ID, 3, &second.AssistantMessageID)
	if len(first.Output) != maxRunOutputBytes {
		t.Fatal("page-external run lacks its large, committed output")
	}
	ctx := context.Background()
	admin := deliveryAdmin(t, dsn)
	lock, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, "SELECT id FROM studio_runs WHERE id=$1 FOR UPDATE", first.ID); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	trace := &chatPageQueryTrace{}
	config.Tracer = trace
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	store := &postgresStore{conn: conn}
	actor := &principal{User: &p.User, Workspace: p.Workspace, Project: p.Project}
	req := httptest.NewRequest("GET", "/?leaf_id="+third.AssistantMessageID, nil)
	req.SetPathValue("id", c.ID)
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, actor))
	for _, check := range []struct {
		limit int
		want  []string
		count int
	}{
		{1, []string{third.AssistantMessageID}, 1},
		{40, []string{third.AssistantMessageID, third.UserMessageID, second.AssistantMessageID}, 2},
	} {
		trace.queries, trace.bodyIDs = 0, nil
		start := time.Now()
		page, err := store.conversationMessagesPage(req, actor, check.limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != check.count || !page.HasMore || !reflect.DeepEqual(trace.bodyIDs, check.want) {
			t.Fatalf("limit %d hydrated %d rows, returned %d", check.limit, len(trace.bodyIDs), len(page.Items))
		}
		raw, err := json.Marshal(page)
		if err != nil || len(raw) > 1<<20 {
			t.Fatal("escaped response exceeded the v1 wire budget")
		}
		t.Logf("limit=%d items=%d body_queries=%d statements=%d bytes=%d elapsed=%v", check.limit, len(page.Items), len(trace.bodyIDs), trace.queries, len(raw), time.Since(start))
	}
	// A row lock alone cannot prove that a PostgreSQL SELECT did not read
	// an MVCC version. The traced queries above establish the exact read set.
	done := make(chan error, 1)
	a.mu.Lock()
	go func() {
		_, err := a.conversationMessagesPage(req, 1)
		done <- err
	}()
	select {
	case err = <-done:
		a.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		a.mu.Unlock()
		<-done
		t.Fatal("PostgreSQL message read needed the application state lock")
	}
}
