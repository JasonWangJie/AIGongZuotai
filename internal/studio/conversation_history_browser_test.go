package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type historyBrowserMessage struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type historyBrowserTree struct {
	Conversation string                  `json:"conversation"`
	Project      string                  `json:"project"`
	Title        string                  `json:"title"`
	Messages     []historyBrowserMessage `json:"messages"`
}

func historyBrowserAnswer(prompt string) string {
	return "收到 " + prompt + "\n已记入当前分支。"
}

func TestPostgresConversationHistoryBrowser035(t *testing.T) {
	if os.Getenv("MESSAGE_HISTORY_BROWSER_TEST") != "1" {
		t.Skip("opt-in PostgreSQL and Chromium conversation history")
	}
	if !libraryLoadLocalDB(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("loopback test database required")
	}
	report := os.Getenv("MESSAGE_HISTORY_BROWSER_REPORT")
	if report != "" && !filepath.IsAbs(report) {
		t.Fatal("report destination must be absolute")
	}
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Messages []ChatMessage }
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" ||
			json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || len(in.Messages) == 0 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		calls.Add(1)
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": historyBrowserAnswer(in.Messages[len(in.Messages)-1].Content)}}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	}))
	defer gateway.Close()
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, AuthMode: "required", WebDir: filepath.Join("..", "..", "apps", "workbench", "dist"), Heartbeat: time.Second})
	provider, err := newOpenAIChat(gateway.URL+"/v1", "history-loopback-fixture", "fixture-chat")
	if err != nil {
		t.Fatal("invalid loopback chat fixture")
	}
	a.chat = provider
	client := authClient(t)
	const email = "history-window@example.test"
	owner := registerFixture(t, client, s.URL, email)
	read := func(project, method, path, key string, in any, want int) []byte {
		t.Helper()
		code, body, _ := authRequest(t, client, s.URL, method, path, key, project, in)
		if code != want {
			t.Fatalf("history fixture %s status=%d expected=%d", method, code, want)
		}
		return body
	}
	other := decode[Project](t, read(owner.Project.ID, "POST", "/api/v1/projects", "", map[string]string{"name": "分区乙"}, 201))
	seed := func(project, title, prefix string, turns int) historyBrowserTree {
		t.Helper()
		c := decode[Conversation](t, read(project, "POST", "/api/v1/conversations", newID("key"), ConversationInput{ProjectID: project, Title: title}, 201))
		tree := historyBrowserTree{Conversation: c.ID, Project: project, Title: title}
		var parent *string
		for i := 0; i < turns; i++ {
			prompt := fmt.Sprintf("%s 第%03d轮", prefix, i+1) + strings.Repeat("\n本页保留换行。", i%4)
			r := decode[Run](t, read(project, "POST", "/api/v1/conversations/"+c.ID+"/runs", newID("key"), conversationInput(c.Version+uint64(i), parent, prompt), 201))
			limit := time.Now().Add(30 * time.Second)
			for {
				got := decode[Run](t, read(project, "GET", "/api/v1/runs/"+r.ID+"/snapshot", "", nil, 200))
				if terminal(got.Status) {
					if got.Status != "COMPLETED" || got.Output != historyBrowserAnswer(prompt) || got.AssistantMessageID != r.AssistantMessageID {
						t.Fatalf("unexpected committed turn %d", i+1)
					}
					break
				}
				if time.Now().After(limit) {
					t.Fatalf("turn %d did not terminate", i+1)
				}
				time.Sleep(15 * time.Millisecond)
			}
			tree.Messages = append(tree.Messages, historyBrowserMessage{r.UserMessageID, "user", prompt}, historyBrowserMessage{r.AssistantMessageID, "assistant", historyBrowserAnswer(prompt)})
			parent = &r.AssistantMessageID
		}
		return tree
	}
	main := seed(owner.Project.ID, "分镜手记甲", "原分区", 64)
	sibling := seed(other.ID, "分镜手记乙", "另一个分区", 1)
	// Reassemble the authenticated production API pages independently of the browser.
	var rows []historyBrowserMessage
	cursor := ""
	for i := 0; i < 7; i++ {
		path := "/api/v1/conversations/" + main.Conversation + "/messages?limit=20&leaf_id=" + main.Messages[127].ID + "&before=" + url.QueryEscape(cursor)
		page := decode[conversationMessagePage](t, read(main.Project, "GET", path, "", nil, 200))
		if page.HasMore != (i < 6) || (page.NextCursor != "") != page.HasMore || len(page.Items) != min(20, 128-i*20) {
			t.Fatalf("invalid history page %d", i+1)
		}
		block := make([]historyBrowserMessage, 0, len(page.Items))
		for _, item := range page.Items {
			if item.ConversationID != main.Conversation || item.Status != "COMPLETED" {
				t.Fatal("incorrect historical scope or status")
			}
			block = append(block, historyBrowserMessage{item.ID, item.Role, item.Content})
		}
		rows = append(block, rows...)
		cursor = page.NextCursor
	}
	if !slices.Equal(rows, main.Messages) {
		t.Fatal("authenticated pages diverged from original turns")
	}
	// Cross-project access must remain private even for the same account.
	read(sibling.Project, "GET", "/api/v1/conversations/"+main.Conversation+"/messages?limit=20", "", nil, 404)
	input, _ := json.Marshal(map[string]any{"url": s.URL, "email": email, "password": "fixture-password-123", "main": main, "sibling": sibling})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/conversation-history-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	control, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("could not open browser input")
	}
	defer control.Close()
	for _, key := range []string{"PATH", "SystemRoot", "USERPROFILE", "LOCALAPPDATA", "HOME", "TMPDIR", "TEMP", "TMP", "WORKFLOW_CHROME", "LD_LIBRARY_PATH", "XDG_RUNTIME_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	attach, release, err := longReplyProcessGuard(cmd)
	if err != nil {
		t.Fatal("could not prepare owned browser processes", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error("could not release owned browser processes", err)
		}
	}()
	if err := cmd.Start(); err != nil {
		t.Fatal("could not start history browser driver", err)
	}
	// Chromium can only launch after stdin closes. Assign the driver to
	// its owned process tree before providing the fixture and EOF.
	if err := attach(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("could not attach browser driver", err)
	}
	_, writeErr := control.Write(input)
	closeErr := control.Close()
	if writeErr != nil || closeErr != nil {
		_ = release()
		_ = cmd.Wait()
		t.Fatal("could not deliver local browser fixture")
	}
	if err := cmd.Wait(); err != nil {
		var fault struct {
			Phase        string
			Step         int
			Line         int
			LoadingState struct {
				Count, Reads, OlderReads int
				Focused, Busy            bool
			}
		}
		_ = json.Unmarshal(diag.Bytes(), &fault)
		switch fault.Phase {
		case "auth", "first-page", "prepend", "resize", "keyboard", "scope-switch", "report":
		default:
			fault.Phase = "child-exit"
		}
		t.Fatalf("history browser phase=%s step=%d script_line=%d page=%+v (%v)", fault.Phase, fault.Step, fault.Line, fault.LoadingState, err)
	}
	if err := release(); err != nil {
		t.Fatal("could not finish owned browser processes", err)
	}
	var measurement map[string]any
	if json.Unmarshal(out.Bytes(), &measurement) != nil || measurement["history_rows_seen"] != float64(128) || measurement["late_response_isolated"] != true {
		t.Fatal("invalid browser evidence")
	}
	independent := deliveryAdmin(t, db)
	var nodes, completed, contexts int
	err = independent.QueryRow(ctx, `SELECT (SELECT count(*) FROM studio_chat_messages WHERE conversation_id=$1 AND workspace_id=$2 AND project_id=$3),
		(SELECT count(*) FROM studio_runs WHERE conversation_id=$1 AND workspace_id=$2 AND project_id=$3 AND document->>'status'='COMPLETED'),
		(SELECT count(*) FROM studio_chat_contexts WHERE conversation_id=$1 AND workspace_id=$2 AND project_id=$3)`, main.Conversation, owner.Workspace.ID, main.Project).Scan(&nodes, &completed, &contexts)
	if err != nil || nodes != 128 || completed != 64 || contexts != 64 || calls.Load() != 65 {
		t.Fatalf("persistent count mismatch %d/%d/%d, external fixture calls %d", nodes, completed, contexts, calls.Load())
	}
	measurement["pg_messages"] = nodes
	measurement["pg_completed_turns"] = completed
	measurement["pg_execution_snapshots"] = contexts
	measurement["sibling_completed_turns"] = 1
	measurement["loopback_provider_posts"] = calls.Load()
	measurement["scope_negative_checks"] = 1
	b, err := json.MarshalIndent(measurement, "", "  ")
	if err != nil {
		t.Fatal("invalid aggregate result")
	}
	if report != "" {
		f, err := os.OpenFile(report, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("cannot create new aggregate result")
		}
		_, writeErr := f.Write(append(b, '\n'))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("could not finish aggregate result")
		}
	}
	t.Log(string(b))
}
