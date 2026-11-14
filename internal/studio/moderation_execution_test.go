package studio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func executionModerationConfig(t *testing.T, rules []ModerationRule) ModerationConfig {
	t.Helper()
	if len(rules) == 0 {
		rules = []ModerationRule{{Phase: "both", Contains: "never-match-fixture-token", Decision: "block"}}
	}
	raw, _ := json.Marshal(moderationPolicyFile{SchemaVersion: 1, PolicyID: "execution-fixture", PolicyVersion: "v1", MaxInputBytes: MaxChatContentBytes, MaxOutputBytes: maxRunOutputBytes, Rules: rules})
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return ModerationConfig{Mode: "rules", PolicyFile: path}
}

// Memory injection is test-only. Production New rejects enabled moderation
// without PostgreSQL + required authentication.
func newModeratedExecutionFixture(t *testing.T, databaseURL string, rules []ModerationRule) (*App, *httptest.Server, *http.Client, testIdentity) {
	t.Helper()
	config := Config{DatabaseURL: databaseURL}
	policy := executionModerationConfig(t, rules)
	if databaseURL != "" {
		config.AuthMode = "required"
		config.Moderation = policy
	}
	a, server := testApp(t, config)
	if databaseURL == "" {
		var err error
		a.moderation, err = newModerationEngine(policy, "demo")
		if err != nil {
			t.Fatal(err)
		}
	}
	client := authClient(t)
	identity := testIdentity{Workspace: Workspace{ID: "ws_demo"}, Project: Project{ID: "prj_demo", WorkspaceID: "ws_demo"}}
	if databaseURL != "" {
		identity = registerFixture(t, client, server.URL, "moderation-execution@example.test")
	}
	return a, server, client, identity
}

type moderatedExecutionChat struct {
	text     string
	emitted  chan struct{}
	release  chan struct{}
	calls    atomic.Int32
	requests chan ChatRequest
}

func (p *moderatedExecutionChat) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	return p.StreamMessages(ctx, "", ChatRequest{Messages: []ChatMessage{{Role: "user", Content: prompt}}}, emit)
}
func (p *moderatedExecutionChat) StreamMessages(ctx context.Context, _ string, request ChatRequest, emit func(string) error) error {
	p.calls.Add(1)
	if p.requests != nil {
		p.requests <- request
	}
	for _, part := range []string{p.text[:len(p.text)/2], p.text[len(p.text)/2:]} {
		// Test strings are ASCII so this split never creates invalid UTF-8.
		if err := emit(part); err != nil {
			return err
		}
	}
	if p.emitted != nil {
		p.emitted <- struct{}{}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (p *moderatedExecutionChat) StreamMessagesUsage(ctx context.Context, model string, request ChatRequest, emit func(string) error) (ChatUsage, error) {
	err := p.StreamMessages(ctx, model, request, emit)
	return ChatUsage{Status: "demo", Source: "demo", PromptTokens: 128, CompletionTokens: 64, TotalTokens: 192, Complete: err == nil}, err
}

func awaitModeratedEmission(t *testing.T, p *moderatedExecutionChat) {
	t.Helper()
	select {
	case <-p.emitted:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not emit")
	}
}

func TestModeratedExecutionCompleteTextOnlyAndEveryReadSurface(t *testing.T) {
	for _, tc := range []struct{ name, decision, want string }{{"allowed", "", "ALLOWED"}, {"blocked", "block", "BLOCKED"}, {"unknown", "review", "REVIEW"}} {
		t.Run(tc.name, func(t *testing.T) {
			rules := []ModerationRule{}
			if tc.decision != "" {
				rules = append(rules, ModerationRule{Phase: "output", Contains: "PRIVATE-CANDIDATE", Decision: tc.decision})
			}
			a, server, client, identity := newModeratedExecutionFixture(t, "", rules)
			p := &moderatedExecutionChat{text: "PRIVATE-CANDIDATE complete text", emitted: make(chan struct{}, 1), release: make(chan struct{})}
			a.chat = p
			c := memoryConversation(t, a)
			input := conversationInput(c.Version, nil, "safe user request")
			run, _, err := a.createConversationRun(identity.Workspace.ID, identity.Project.ID, c.ID, newID("key"), "", input)
			if err != nil {
				t.Fatal(err)
			}
			awaitModeratedEmission(t, p)
			paths := []string{"/api/v1/runs", "/api/v1/runs/" + run.ID + "/snapshot", "/api/v1/conversations/" + c.ID + "/messages", "/api/v1/conversations/" + c.ID + "/branches"}
			for _, path := range paths {
				code, body, _ := authRequest(t, client, server.URL, "GET", path, "", identity.Project.ID, nil)
				if code != 200 || strings.Contains(string(body), "PRIVATE-CANDIDATE") {
					t.Fatalf("pending output visible %s %d %s", path, code, body)
				}
			}
			a.mu.Lock()
			if a.runs[run.ID].Run.Output != "" || len(a.runs[run.ID].Events) != 0 {
				a.mu.Unlock()
				t.Fatal("candidate entered public cache or replay ring")
			}
			a.mu.Unlock()
			response, err := client.Get(server.URL + "/api/v1/runs/" + run.ID + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			close(p.release)
			stream, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			final := waitConversationRun(t, a, run.ID)
			if final.Moderation == nil || final.Moderation.State != tc.want || final.Moderation.Version != 2 {
				t.Fatalf("wrong result %+v", final)
			}
			if strings.Contains(string(stream), "event: message.delta") || !strings.Contains(string(stream), "event: run.snapshot") {
				t.Fatalf("invalid public SSE %s", stream)
			}
			if tc.want == "ALLOWED" {
				if final.Status != "COMPLETED" || final.Output != p.text || !strings.Contains(string(stream), p.text) {
					t.Fatal("allowed full text not delivered")
				}
			} else {
				if final.Status != "FAILED" || final.Output != "" || strings.Contains(string(stream), "PRIVATE-CANDIDATE") {
					t.Fatal("quarantined output leaked")
				}
				for _, path := range paths {
					_, body, _ := authRequest(t, client, server.URL, "GET", path, "", identity.Project.ID, nil)
					if strings.Contains(string(body), "PRIVATE-CANDIDATE") {
						t.Fatal("terminal read leaked", path)
					}
				}
			}
		})
	}
}

func TestModeratedInputNoSideEffectAndOldReplayPrecedesPolicy(t *testing.T) {
	a, server := testApp(t, Config{})
	a.chat = &moderatedExecutionChat{text: "legacy output"}
	legacy := runInput()
	legacy.Input = "reject-input"
	old, _, err := a.createRun(legacy, "old-run-replay")
	if err != nil {
		t.Fatal(err)
	}
	waitConversationRun(t, a, old.ID)
	task := taskInput("image")
	oldTask, _, err := a.createTask(task, "old-task-replay")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := newModerationEngine(executionModerationConfig(t, []ModerationRule{{Phase: "input", Contains: "reject-input", Decision: "block"}}), "demo")
	if err != nil {
		t.Fatal(err)
	}
	a.moderation = engine
	if got, replay, e := a.createRun(legacy, "old-run-replay"); e != nil || !replay || got.ID != old.ID {
		t.Fatal("legacy replay hit new policy", e)
	}
	if got, replay, e := a.createTask(task, "old-task-replay"); e != nil || !replay || got.ID != oldTask.ID {
		t.Fatal("legacy media replay blocked", e)
	}
	a.mu.Lock()
	count := len(a.runs)
	idems := len(a.idem)
	a.mu.Unlock()
	if _, _, e := a.createRun(legacy, "new-denied-key"); !isFailureCode(e, "MODERATION_INPUT_BLOCKED") {
		t.Fatal("denied input accepted", e)
	}
	if _, _, e := a.createTask(task, "new-task-key"); !isFailureCode(e, "MODERATION_MEDIA_UNSUPPORTED") {
		t.Fatal("new media accepted", e)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.runs) != count || len(a.idem) != idems {
		t.Fatal("rejected input created state")
	}
	_ = server
}

func isFailureCode(err error, code string) bool {
	if op, ok := err.(*operationError); ok {
		return op.code == code
	}
	return false
}

func TestModeratedContextSkipsQuarantineAndForbidsContinue(t *testing.T) {
	a, _, _, _ := newModeratedExecutionFixture(t, "", []ModerationRule{{Phase: "output", Contains: "PRIVATE-CANDIDATE", Decision: "block"}})
	p := &moderatedExecutionChat{text: "PRIVATE-CANDIDATE", requests: make(chan ChatRequest, 4)}
	a.chat = p
	c := memoryConversation(t, a)
	run, _, err := a.createConversationRun("ws_demo", "prj_demo", c.ID, newID("key"), "", conversationInput(1, nil, "previous user"))
	if err != nil {
		t.Fatal(err)
	}
	run = waitConversationRun(t, a, run.ID)
	<-p.requests
	input := conversationInput(2, &run.AssistantMessageID, "")
	input.Operation = "continue"
	if _, _, err = a.createConversationRun("ws_demo", "prj_demo", c.ID, newID("key"), "", input); !isFailureCode(err, "CHAT_CONTINUE_UNAVAILABLE") {
		t.Fatal("continued quarantined content", err)
	}
	input = conversationInput(2, &run.AssistantMessageID, "new user")
	second, _, err := a.createConversationRun("ws_demo", "prj_demo", c.ID, newID("key"), "", input)
	if err != nil {
		t.Fatal(err)
	}
	waitConversationRun(t, a, second.ID)
	request := <-p.requests
	if len(request.Messages) != 1 || request.Messages[0].Content != "new user" {
		t.Fatalf("quarantine entered context %+v", request)
	}
}

func TestModeratedCancelNeverReleasesCandidate(t *testing.T) {
	a, _, _, _ := newModeratedExecutionFixture(t, "", nil)
	p := &moderatedExecutionChat{text: "PRIVATE-CANDIDATE", emitted: make(chan struct{}, 1), release: make(chan struct{})}
	a.chat = p
	run, _, err := a.createRun(runInput(), newID("key"))
	if err != nil {
		t.Fatal(err)
	}
	awaitModeratedEmission(t, p)
	cancelled, err := a.cancelRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if cancelled.Status != "CANCELLED" || cancelled.Output != "" || cancelled.Moderation.State != "REVIEW" || cancelled.Moderation.Version != 2 {
		t.Fatalf("cancelled %+v", cancelled)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.runs[run.ID].Events {
		if strings.Contains(string(e.Data), "PRIVATE-CANDIDATE") {
			t.Fatal("candidate appeared in replay")
		}
	}
}

func TestModeratedAdmissionContextDigestAndConfigurationGuards(t *testing.T) {
	engine, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
	if err != nil {
		t.Fatal(err)
	}
	request := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "approved"}}, MaxOutputTokens: 4096, OutputLimitField: "max_tokens"}
	approval := &runModerationAdmission{}
	if !isModerationPreparation(approval.prepare(request)) {
		t.Fatal("preflight not staged")
	}
	approval.check(context.Background(), engine)
	request.Messages[0].Content = "changed"
	if !isFailureCode(approval.prepare(request), "MODERATION_CONTEXT_CHANGED") {
		t.Fatal("stale context accepted")
	}
	for _, cfg := range []Config{{Moderation: executionModerationConfig(t, nil)}, {AuthMode: "required", Moderation: executionModerationConfig(t, nil)}} {
		if a, e := newIsolatedApp(t, cfg); e == nil {
			a.Close()
			t.Fatal("enabled moderation without PG admitted")
		}
	}
}

func executionHTTPModerator(t *testing.T, handle func(http.ResponseWriter, *http.Request, moderationHTTPRequest)) *moderationEngine {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in moderationHTTPRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad fixture request", 400)
			return
		}
		handle(w, r, in)
	}))
	t.Cleanup(s.Close)
	path := filepath.Join(t.TempDir(), "http-policy.json")
	raw, _ := json.Marshal(moderationPolicyFile{SchemaVersion: 1, PolicyID: "execution-http", PolicyVersion: "v1", MaxInputBytes: MaxChatContentBytes, MaxOutputBytes: maxRunOutputBytes, Rules: []ModerationRule{}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	e, err := newModerationEngine(ModerationConfig{Mode: "http", PolicyFile: path, Endpoint: s.URL, APIKey: "isolated-checker-fixture-token", CredentialID: "fixture"}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func allowExecutionHTTP(w http.ResponseWriter, in moderationHTTPRequest) {
	writeJSON(w, 200, moderationHTTPResponse{Contract: in.Contract, RequestID: in.RequestID, PolicyID: in.PolicyID, PolicyVersion: in.PolicyVersion, PolicyDigest: in.PolicyDigest, Phase: in.Phase, ContentDigest: in.ContentDigest, Decision: "allow", ReasonCode: "allowed"})
}

func TestModeratedInputHTTPOutsideLockAndConcurrentSameKey(t *testing.T) {
	a, _ := testApp(t, Config{})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	a.moderation = executionHTTPModerator(t, func(w http.ResponseWriter, r *http.Request, in moderationHTTPRequest) {
		if in.Phase == "input" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		allowExecutionHTTP(w, in)
	})
	p := &moderatedExecutionChat{text: "safe approved output"}
	a.chat = p
	type result struct {
		run Run
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { r, _, e := a.createRun(runInput(), "concurrent-moderation-key"); results <- result{r, e} }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("checker did not receive independent preflights")
		}
	}
	locked := make(chan struct{})
	go func() { a.mu.Lock(); a.mu.Unlock(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("input checker held global application lock")
	}
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.run.ID != second.run.ID {
		t.Fatal("concurrent input checks created duplicate accepted runs", first, second)
	}
	waitConversationRun(t, a, first.run.ID)
	if p.calls.Load() != 1 {
		t.Fatal("same original request reached provider more than once")
	}
}

func TestModeratedOutputHTTPUnknownAndLateAllowAfterCancel(t *testing.T) {
	for _, mode := range []string{"unavailable", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := testApp(t, Config{})
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			a.moderation = executionHTTPModerator(t, func(w http.ResponseWriter, r *http.Request, in moderationHTTPRequest) {
				if in.Phase == "input" {
					allowExecutionHTTP(w, in)
					return
				}
				entered <- struct{}{}
				if mode == "unavailable" {
					http.Error(w, "PRIVATE-CHECKER-DETAIL", 503)
					return
				}
				<-release // Deliberately returns ALLOW after caller cancelled.
				allowExecutionHTTP(w, in)
			})
			a.chat = &moderatedExecutionChat{text: "PRIVATE-CANDIDATE"}
			run, _, err := a.createRun(runInput(), newID("key"))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("output checker not reached")
			}
			if mode == "cancel" {
				if _, err = a.cancelRun(run.ID); err != nil {
					t.Fatal(err)
				}
				close(release)
			}
			final := waitConversationRun(t, a, run.ID)
			if final.Output != "" || final.Moderation.State != "REVIEW" {
				t.Fatal("unknown/late allow escaped quarantine", final)
			}
			if mode == "cancel" && final.Status != "CANCELLED" {
				t.Fatal("late allow revived cancelled run")
			}
			body, _ := json.Marshal(final)
			if strings.Contains(string(body), "PRIVATE-") {
				t.Fatal("checker/provider details leaked")
			}
		})
	}
}
