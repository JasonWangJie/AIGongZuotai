package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func deepPrompt(n int) string { return fmt.Sprintf("历史问题 %04d", n) }
func deepAnswer(n int) string { return fmt.Sprintf("历史答复 %04d", n) }

func deepExpectedMessages(turn, ordinaryTurns int) []ChatMessage {
	begin := max(1, turn-(MaxChatMessages-1)/2)
	messages := make([]ChatMessage, 0, 2*(turn-begin)+1)
	for i := begin; i < turn; i++ {
		messages = append(messages, ChatMessage{Role: "user", Content: deepPrompt(i)}, ChatMessage{Role: "assistant", Content: deepAnswer(i)})
	}
	prompt := deepPrompt(turn)
	if turn == ordinaryTurns+1 {
		prompt = conversationContinuePrompt
	}
	return append(messages, ChatMessage{Role: "user", Content: prompt})
}

// The only upstream is this owned loopback server. Validate every wire
// request, including exactly which older complete pairs survived trimming.
func deepHistoryGateway(turns int, calls *atomic.Int32, key string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Model    string        `json:"model"`
			Stream   bool          `json:"stream"`
			Messages []ChatMessage `json:"messages"`
		}
		n := int(calls.Add(1))
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&wire)
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key ||
			err != nil || n > turns+1 || wire.Model != "deep-local" || !wire.Stream || !slices.Equal(wire.Messages, deepExpectedMessages(n, turns)) {
			http.Error(w, "fixture request mismatch", http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": deepAnswer(n)}}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
	}))
}

func TestDeepHistoryFixtureChecksCompleteWireContext(t *testing.T) {
	// Exercise only the owned HTTP fixture. The real 036 run independently
	// checks the application request on every turn and its stored final IDs.
	var calls atomic.Int32
	server := deepHistoryGateway(100, &calls, "local-fixture")
	defer server.Close()
	for _, n := range []int{1, 65, 101} {
		calls.Store(int32(n - 1))
		messages := deepExpectedMessages(n, 100)
		if len(messages) != min(2*n-1, MaxChatMessages) {
			t.Fatal("wrong closed-form fixture window")
		}
		for _, valid := range []bool{true, false} {
			calls.Store(int32(n - 1))
			if !valid {
				messages[0].Content += "-wrong"
			}
			raw, _ := json.Marshal(map[string]any{"model": "deep-local", "stream": true, "messages": messages})
			req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer local-fixture")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if (response.StatusCode == 200) != valid {
				t.Fatalf("fixture wire validation differed at turn %d", n)
			}
		}
	}
}

type deepTurn struct {
	user, assistant, run string
}

type deepProgress struct {
	Turns         int   `json:"turns"`
	ElapsedMS     int64 `json:"elapsed_ms"`
	Pending       int   `json:"outbox_pending"`
	IdleAndPinned int   `json:"cached_runs_including_pins"`
	SchemaBytes   int64 `json:"schema_table_and_index_bytes"`
}

type deepHistoryReport struct {
	Profile         string                  `json:"profile"`
	OrdinaryTurns   int                     `json:"ordinary_completed_turns"`
	Nodes           int                     `json:"nodes_after_continue"`
	Contexts        int                     `json:"frozen_contexts_after_continue"`
	ProviderPosts   int                     `json:"loopback_provider_posts"`
	Pages           int                     `json:"authenticated_history_pages"`
	HTTPReadMS      int64                   `json:"authenticated_history_read_ms"`
	Progress        []deepProgress          `json:"build_progress"`
	Replayed        bool                    `json:"original_key_replayed"`
	ContinueOmitted int                     `json:"continue_omitted_messages"`
	Browser         *deepBrowserMeasurement `json:"browser,omitempty"`
}

func deepSample(t *testing.T, db *pgx.Conn, a *App, n int, start time.Time) deepProgress {
	t.Helper()
	p := deepProgress{Turns: n, ElapsedMS: time.Since(start).Milliseconds()}
	const query = `SELECT (SELECT pending FROM studio_outbox_limits WHERE singleton),
 COALESCE(sum(pg_total_relation_size(c.oid)),0)::bigint
 FROM pg_class c JOIN pg_namespace ns ON ns.oid=c.relnamespace
 WHERE ns.nspname=current_schema() AND c.relkind='r'`
	if err := db.QueryRow(context.Background(), query).Scan(&p.Pending, &p.SchemaBytes); err != nil {
		t.Fatal("failed reading isolated schema counters")
	}
	a.mu.Lock()
	p.IdleAndPinned = len(a.runs)
	maxCached := a.config.MaxRecords + a.config.ChatScheduling.Workers + 1
	a.mu.Unlock()
	if p.IdleAndPinned > maxCached || p.Pending > outboxPendingLimit {
		t.Fatal("ordinary cache or durable queue exceeded its configured bound")
	}
	t.Logf("completed=%d elapsed_ms=%d pending=%d cached_runs=%d schema_bytes=%d", n, p.ElapsedMS, p.Pending, p.IdleAndPinned, p.SchemaBytes)
	return p
}

func TestPostgresDeepConversationHistory036(t *testing.T) {
	profile := os.Getenv("DEEP_CONVERSATION_TEST")
	if profile == "" {
		t.Skip("opt-in persistent conversation history")
	}
	turns := 5000
	switch profile {
	case "pilot":
		turns = 150
	case "full":
	default:
		t.Fatal("expected pilot or full")
	}
	if !libraryLoadLocalDB(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("a disposable loopback PostgreSQL database is required")
	}
	browserMode := os.Getenv("DEEP_CONVERSATION_BROWSER")
	if browserMode != "" && browserMode != "1" {
		t.Fatal("invalid browser mode")
	}
	reportPath := os.Getenv("DEEP_CONVERSATION_REPORT")
	if reportPath != "" && !filepath.IsAbs(reportPath) {
		t.Fatal("report destination must be absolute")
	}
	dist := os.Getenv("DEEP_CONVERSATION_DIST")
	if browserMode == "1" {
		if !filepath.IsAbs(dist) {
			t.Fatal("browser requires the absolute production dist path")
		}
		if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
			t.Fatal("production dist is unavailable")
		}
	}
	var calls atomic.Int32
	key := newID("fixture")
	gateway := deepHistoryGateway(turns, &calls, key)
	defer gateway.Close()
	dsn := testDatabase(t)
	cfg := Config{DatabaseURL: dsn, AuthMode: "required", Mode: "live", BaseURL: gateway.URL + "/v1", APIKey: key,
		ChatModel: "deep-local", LongConversationHistory: true, AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), WebDir: dist}
	a, err := New(cfg)
	if err != nil {
		t.Fatal("failed to open isolated workbench", err)
	}
	defer a.Close()
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	admin := deliveryAdmin(t, dsn)
	client := authClient(t)
	const email = "deep-history@example.test"
	owner := registerFixture(t, client, server.URL, email)
	read := func(method, path, key string, input any, want int) ([]byte, http.Header) {
		t.Helper()
		code, raw, header := authRequest(t, client, server.URL, method, path, key, owner.Project.ID, input)
		if code != want {
			var failure struct{ Error APIError }
			_ = json.Unmarshal(raw, &failure)
			t.Fatalf("workbench %s status=%d business_code=%s", method, code, failure.Error.Code)
		}
		return raw, header
	}
	const title = "长分支验收手记"
	raw, _ := read("POST", "/api/v1/conversations", newID("key"), ConversationInput{ProjectID: owner.Project.ID, Title: title}, 201)
	c := decode[Conversation](t, raw)
	base := "/api/v1/conversations/" + c.ID
	report := deepHistoryReport{Profile: profile, OrdinaryTurns: turns}
	originals := make([]deepTurn, 0, turns+1)
	start := time.Now()
	report.Progress = append(report.Progress, deepSample(t, admin, a, 0, start))
	var parent *string
	accept := func(n int, input ConversationRunInput) Run {
		t.Helper()
		raw, header := read("POST", base+"/runs", fmt.Sprintf("deep-turn-%06d", n), input, 201)
		accepted := decode[Run](t, raw)
		if header.Get("Idempotency-Replayed") != "" || accepted.ConversationID != c.ID || accepted.ConversationVersion != uint64(n+1) ||
			accepted.ProjectID != owner.Project.ID || accepted.UserMessageID == "" || accepted.AssistantMessageID == "" {
			t.Fatalf("wrong acceptance at turn %d", n)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			raw, _ := read("GET", "/api/v1/runs/"+accepted.ID+"/snapshot", "", nil, 200)
			got := decode[Run](t, raw)
			if terminal(got.Status) {
				if got.Status != "COMPLETED" || got.Output != deepAnswer(n) || got.ID != accepted.ID ||
					got.AssistantMessageID != accepted.AssistantMessageID || got.ContextSummary == nil ||
					got.ContextSummary.IncludedMessages != min(2*n-1, MaxChatMessages) || got.ContextSummary.OmittedMessages != max(0, 2*n-1-MaxChatMessages) {
					t.Fatalf("incomplete or inconsistent committed turn %d, status=%s", n, got.Status)
				}
				return got
			}
			if time.Now().After(deadline) {
				t.Fatalf("turn %d did not reach a confirmed terminal state", n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for n := 1; n <= turns; n++ {
		got := accept(n, conversationInput(uint64(n), parent, deepPrompt(n)))
		originals = append(originals, deepTurn{got.UserMessageID, got.AssistantMessageID, got.ID})
		parent = &originals[len(originals)-1].assistant
		if n == 100 || n == 150 || n%500 == 0 || n == turns {
			report.Progress = append(report.Progress, deepSample(t, admin, a, n, start))
		}
	}
	if int(calls.Load()) != turns {
		t.Fatal("wrong number of upstream requests after the linear build")
	}
	raw, header := read("POST", base+"/runs", "deep-turn-000001", conversationInput(1, nil, deepPrompt(1)), 201)
	if header.Get("Idempotency-Replayed") != "true" || decode[Run](t, raw).ID != originals[0].run {
		t.Fatal("the earliest original key did not resolve to its first resource")
	}
	report.Replayed = true
	readStart := time.Now()
	var cursor string
	var hashes []string
	for end := turns * 2; end > 0; end -= 20 {
		query := url.Values{"leaf_id": {*parent}, "limit": {"20"}}
		if cursor != "" {
			query.Set("before", cursor)
		}
		raw, _ := read("GET", base+"/messages?"+query.Encode(), "", nil, 200)
		page := decode[conversationMessagePage](t, raw)
		if len(page.Items) != 20 || len(raw) > 1<<20 || stringID(page.LeafMessageID) != *parent || page.ConversationVersion != uint64(turns+1) ||
			page.HasMore != (end > 20) || (page.NextCursor != "") != page.HasMore {
			t.Fatalf("page boundary changed at %d", end)
		}
		hash := sha256.New()
		for j, m := range page.Items {
			index := end - 20 + j
			n := index/2 + 1
			o := originals[n-1]
			id, role, content, parentID := o.assistant, "assistant", deepAnswer(n), o.user
			if index%2 == 0 {
				id, role, content, parentID = o.user, "user", deepPrompt(n), ""
				if n > 1 {
					parentID = originals[n-2].assistant
				}
			}
			if m.ConversationID != c.ID || m.ID != id || m.Role != role || m.Content != content || m.Status != "COMPLETED" ||
				stringID(m.ParentID) != parentID || role == "assistant" && stringID(m.RunID) != o.run {
				t.Fatalf("history divergence at node %d", index+1)
			}
			fmt.Fprintf(hash, "%s\t%s\t%s\t%s\n", m.ID, stringID(m.ParentID), m.Role, m.Content)
		}
		hashes = append(hashes, hex.EncodeToString(hash.Sum(nil)))
		cursor = page.NextCursor
	}
	report.Pages, report.HTTPReadMS = len(hashes), time.Since(readStart).Milliseconds()
	if browserMode == "1" {
		report.Browser = deepRunBrowser(t, deepBrowserInput{URL: server.URL, Email: email, Password: "fixture-password-123", Project: owner.Project.ID,
			Conversation: c.ID, Title: title, Leaf: *parent, Turns: turns, Digests: hashes})
		if int(calls.Load()) != turns {
			t.Fatal("history browsing triggered generation")
		}
	}
	next := conversationInput(uint64(turns+1), parent, "")
	next.Operation = "continue"
	continued := accept(turns+1, next)
	report.ContinueOmitted = continued.ContextSummary.OmittedMessages
	var blob []byte
	if err := admin.QueryRow(context.Background(), "SELECT snapshot FROM studio_chat_contexts WHERE run_id=$1 AND conversation_id=$2", continued.ID, c.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	var frozen chatExecution
	if err := json.Unmarshal(blob, &frozen); err != nil {
		t.Fatal("invalid frozen request")
	}
	var ids []string
	for _, prev := range originals[turns-(MaxChatMessages-1)/2:] {
		ids = append(ids, prev.user, prev.assistant)
	}
	if !slices.Equal(frozen.SourceMessageIDs, ids) || !slices.Equal(frozen.Request.Messages, deepExpectedMessages(turns+1, turns)) ||
		frozen.UpstreamModel != "deep-local" || frozen.Summary != *continued.ContextSummary {
		t.Fatal("explicit continuation lost original sources, bytes or frozen model")
	}
	ctx := context.Background()
	var completed, nodesOnHead int
	err = admin.QueryRow(ctx, `SELECT c.node_count,
 (SELECT count(*) FROM studio_chat_messages WHERE conversation_id=c.id),
 (SELECT count(*) FROM studio_chat_contexts WHERE conversation_id=c.id),
 (SELECT count(*) FROM studio_runs WHERE conversation_id=c.id AND document->>'status'='COMPLETED')
 FROM studio_conversations c WHERE c.id=$1 AND c.workspace_id=$2 AND c.project_id=$3 AND c.head_message_id=$4`,
		c.ID, owner.Workspace.ID, owner.Project.ID, continued.AssistantMessageID).Scan(&nodesOnHead, &report.Nodes, &report.Contexts, &completed)
	report.ProviderPosts = int(calls.Load())
	if err != nil || report.Nodes != (turns+1)*2 || nodesOnHead != report.Nodes || report.Contexts != turns+1 || completed != turns+1 || report.ProviderPosts != turns+1 {
		t.Fatal("final committed counts do not match the linear history")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if reportPath != "" {
		f, err := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal("cannot create new aggregate report")
		}
		_, writeErr := f.Write(append(bytes.Clone(data), '\n'))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot finish aggregate report")
		}
	}
	t.Log("verified ordinary messages=" + strconv.Itoa(turns*2) + " pages=" + strconv.Itoa(report.Pages) + " fixture_posts=" + strconv.Itoa(report.ProviderPosts))
}
