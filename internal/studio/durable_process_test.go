package studio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This fixture runs the real App and HTTP media adapter in a separate process.
// The parent kills it after the loopback supplier receives POST, before any
// handle response exists. Neither the supplier nor the database is mocked.
type durableProcessConfig struct {
	DatabaseURL, ReferenceDir, AssetDir, GatewayURL, ReadyFile string
	ChatGatewayURL                                             string
	BillingPriceFile                                           string
	RunDeltaBatching                                           bool        `json:",omitempty"`
	Mail                                                       *MailConfig `json:",omitempty"`
	MailCAPEM                                                  []byte      `json:",omitempty"`
}

func TestDurableProcessChild(t *testing.T) {
	path := os.Getenv("STUDIO_DURABLE_PROCESS_CONFIG")
	if path == "" {
		return
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private process fixture config")
	}
	var fixture durableProcessConfig
	if json.Unmarshal(blob, &fixture) != nil {
		t.Fatal("invalid process fixture config")
	}
	config := Config{DatabaseURL: fixture.DatabaseURL, AuthMode: "required", Mode: "live", ReferenceDir: fixture.ReferenceDir,
		AssetDir: fixture.AssetDir, AsyncImageBaseURL: fixture.GatewayURL, AsyncImageAPIKey: fixtureMediaKey,
		AsyncImageModel: "fixture-image-model", AsyncImageCredentialID: "fixture-credential", PollInterval: 20 * time.Millisecond, Heartbeat: 100 * time.Millisecond}
	// Each restart uses the same test-only independent delivery key; production
	// still rejects absent keys before touching an asynchronous PG deployment.
	config.MediaDelivery = MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()}
	config.RunDeltaBatching = fixture.RunDeltaBatching
	if fixture.ChatGatewayURL != "" {
		config.BaseURL, config.APIKey, config.ChatModel = fixture.ChatGatewayURL, "local-chat-process-fixture", "fixture-chat-model"
	}
	if fixture.BillingPriceFile != "" {
		config.BillingMode, config.BillingPriceFile = "credits", fixture.BillingPriceFile
	}
	if fixture.Mail != nil {
		// This test executable alone injects a fixture CA. The SMTP implementation
		// still verifies TLS/hostname; production main has no bypass/config path.
		config.Mail = *fixture.Mail
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(fixture.MailCAPEM) {
			t.Fatal("invalid process SMTP fixture certificate")
		}
		sender := newSMTPMailSender(config.Mail)
		sender.tlsConfig.RootCAs = roots
		config.mailSender = sender
		if fixture.GatewayURL == "" && fixture.ChatGatewayURL == "" {
			config.Mode = "demo"
			config.AsyncImageBaseURL, config.AsyncImageAPIKey, config.AsyncImageModel, config.AsyncImageCredentialID = "", "", "", ""
		}
	}
	app, err := New(config)
	if err != nil {
		t.Fatal("process fixture startup rejected", err)
	}
	defer app.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: time.Second}
	defer server.Close()
	go server.Serve(listener)
	if err = os.WriteFile(fixture.ReadyFile, []byte("http://"+listener.Addr().String()), 0600); err != nil {
		t.Fatal(err)
	}
	<-time.After(90 * time.Second) // Parent owns and kills the fixture; bounded orphan lifetime.
}

func TestPostgresMailProcessCrashAfterDATA(t *testing.T) {
	database := testDatabase(t)
	smtp := newMailSMTPFixture(t, mailSMTPOptions{TLSMode: "implicit_tls", StallAfterData: true})
	mailConfig := smtp.Config
	mailConfig.Timeout = time.Minute // The parent kills before any DATA reply/deadline.
	fixture := durableProcessConfig{DatabaseURL: database, ReferenceDir: filepath.Join(t.TempDir(), "refs"), AssetDir: filepath.Join(t.TempDir(), "assets"), Mail: &mailConfig, MailCAPEM: smtp.CAPEM}
	first := startDurableChild(t, fixture)
	client := authClient(t)
	registerFixture(t, client, first.base, "mail-process-crash@example.test")
	if smtp.DataCalls.Load() != 0 {
		t.Fatal("register unexpectedly delivered security email")
	}
	status, _, _ := authRequest(t, client, first.base, "POST", "/api/v1/auth/email/verification/request", "", "", map[string]any{})
	if status != http.StatusAccepted {
		t.Fatalf("verification request was not durably accepted: status=%d", status)
	}
	select {
	case <-smtp.Messages: // Discard the private MIME: receipt proves DATA arrived.
	case <-time.After(8 * time.Second):
		t.Fatal("TLS SMTP fixture did not receive DATA before process kill")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, database)
	if err != nil {
		t.Fatal("cannot inspect private mail checkpoint database")
	}
	defer conn.Close(context.Background())
	var mailID, state, code string
	var attempts int
	if err = conn.QueryRow(ctx, `SELECT id,state,attempts,last_error_code FROM studio_mail_outbox WHERE purpose='verify_email' ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&mailID, &state, &attempts, &code); err != nil {
		t.Fatal("durable mail checkpoint could not be read")
	}
	if state != "SENDING" || attempts != 1 || smtp.DataCalls.Load() != 1 {
		t.Fatal("SMTP DATA did not follow exactly one durable SENDING claim")
	}
	first.kill(t)
	second := startDurableChild(t, fixture)
	status, _, _ = authRequest(t, client, second.base, "GET", "/api/v1/auth/session", "", "", nil)
	if status != http.StatusOK {
		t.Fatal("restarted account service was unavailable")
	}
	// Observe multiple worker polling cycles after real restart. Neither SMTP
	// reconnect nor an extra DATA is permitted for the pre-crash uncertain row.
	deadline := time.Now().Add(2200 * time.Millisecond)
	for {
		if err = conn.QueryRow(ctx, "SELECT state,attempts,last_error_code FROM studio_mail_outbox WHERE id=$1", mailID).Scan(&state, &attempts, &code); err != nil {
			t.Fatal("restarted mail state could not be read")
		}
		if state != "SUBMIT_UNKNOWN" || attempts != 1 || code != "SMTP_ACCEPTANCE_UNKNOWN" || smtp.DataCalls.Load() != 1 || smtp.Connections.Load() != 1 {
			t.Fatal("actual process restart changed or re-sent uncertain SMTP DATA")
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Log("Actual process kill after TLS SMTP DATA: durable SENDING recovered as SUBMIT_UNKNOWN; one connection/one DATA, no automatic retry after restart.")
}

func TestPostgresConversationActualCrashPreservesPartialAndExplicitContinue(t *testing.T) {
	database := testDatabase(t)
	var submissions atomic.Int64
	requests := make(chan []ChatMessage, 4)
	const partial = "已持久检查点：星舟项目，尚未完成的回答。"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected fixture request", 404)
			return
		}
		var body struct {
			Messages []ChatMessage `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		r.Body.Close()
		requests <- body.Messages
		n := submissions.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		text := partial
		if n > 1 {
			text = "这是用户显式继续后的一次新回答。"
		}
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": text}}}})
		_, _ = w.Write(append(append([]byte("data: "), payload...), []byte("\n\n")...))
		w.(http.Flusher).Flush()
		if n == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(gateway.Close)
	fixture := durableProcessConfig{DatabaseURL: database, ReferenceDir: filepath.Join(t.TempDir(), "refs"), AssetDir: filepath.Join(t.TempDir(), "assets"), GatewayURL: gateway.URL, ChatGatewayURL: gateway.URL + "/v1"}
	first := startDurableChild(t, fixture)
	client := authClient(t)
	identity := registerFixture(t, client, first.base, "conversation-crash@example.test")
	code, data, _ := authRequest(t, client, first.base, "POST", "/api/v1/conversations", "crash-create-conversation", identity.Project.ID, map[string]string{"project_id": identity.Project.ID, "title": "实际进程恢复验收"})
	if code != 201 {
		t.Fatalf("create conversation %d %s", code, data)
	}
	conv := decode[Conversation](t, data)
	input := map[string]any{"expected_version": conv.Version, "model_id": "studio-chat", "operation": "reply", "parent_message_id": nil, "input": "我的项目叫星舟，请展开这个方向。"}
	path := "/api/v1/conversations/" + conv.ID + "/runs"
	code, data, _ = authRequest(t, client, first.base, "POST", path, "crash-first-conversation-run", identity.Project.ID, input)
	if code != 201 {
		t.Fatalf("create conversation run %d %s", code, data)
	}
	run := decode[Run](t, data)
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not receive original chat request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, database)
	if err != nil {
		t.Fatal("cannot inspect private checkpoint database")
	}
	defer conn.Close(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for {
		var output string
		if err := conn.QueryRow(ctx, "SELECT document->>'output' FROM studio_runs WHERE id=$1", run.ID).Scan(&output); err != nil {
			t.Fatal(err)
		}
		if output == partial {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial answer was not durably checkpointed")
		}
		time.Sleep(25 * time.Millisecond)
	}
	first.kill(t)
	second := startDurableChild(t, fixture)
	code, data, _ = authRequest(t, client, second.base, "GET", "/api/v1/runs/"+run.ID+"/snapshot", "", identity.Project.ID, nil)
	recovered := decode[Run](t, data)
	if code != 200 || recovered.Status != "INTERRUPTED" || recovered.Output != partial {
		t.Fatalf("partial conversation recovery failed %d %s", code, data)
	}
	code, data, _ = authRequest(t, client, second.base, "GET", "/api/v1/conversations/"+conv.ID+"/messages?leaf_id="+run.AssistantMessageID, "", identity.Project.ID, nil)
	var messages struct {
		Items []MessageNode `json:"items"`
	}
	if code != 200 || json.Unmarshal(data, &messages) != nil || len(messages.Items) != 2 || messages.Items[1].Content != partial || messages.Items[1].Status != "INTERRUPTED" {
		t.Fatalf("conversation nodes lost after process death %d %s", code, data)
	}
	code, data, headers := authRequest(t, client, second.base, "POST", path, "crash-first-conversation-run", identity.Project.ID, input)
	if code != 201 || decode[Run](t, data).ID != run.ID || headers.Get("Idempotency-Replayed") != "true" || submissions.Load() != 1 {
		t.Fatal("restart or original-key replay duplicated chat generation")
	}
	code, data, _ = authRequest(t, client, second.base, "GET", "/api/v1/conversations/"+conv.ID, "", identity.Project.ID, nil)
	conv = decode[Conversation](t, data)
	if code != 200 || conv.ActiveRunID != nil {
		t.Fatal("interrupted conversation still claims an active run")
	}
	continuation := map[string]any{"expected_version": conv.Version, "model_id": "studio-chat", "operation": "continue", "parent_message_id": run.AssistantMessageID, "input": ""}
	code, data, _ = authRequest(t, client, second.base, "POST", path, "explicit-conversation-continue", identity.Project.ID, continuation)
	if code != 201 {
		t.Fatalf("explicit continuation %d %s", code, data)
	}
	next := decode[Run](t, data)
	completed := awaitAuthRun(t, client, second.base, next.ID)
	if next.ID == run.ID || completed.Status != "COMPLETED" || submissions.Load() != 2 {
		t.Fatal("explicit continue did not create exactly one new run")
	}
	select {
	case sent := <-requests:
		if len(sent) != 3 || sent[0].Content != input["input"] || sent[1].Content != partial || sent[1].Role != "assistant" || sent[2].Content != conversationContinuePrompt {
			t.Fatal("continue did not send persisted original branch and partial output")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("explicit continue did not reach fixture")
	}
	t.Log("Actual process kill: persisted conversation/partial recovered, one original POST, original-key replay read-only; explicit continue sends one new structured request.")
}

type durableChild struct {
	command *exec.Cmd
	done    chan struct{}
	base    string
	logs    bytes.Buffer
}

func startDurableChild(t *testing.T, fixture durableProcessConfig) *durableChild {
	t.Helper()
	fixture.ReadyFile = filepath.Join(t.TempDir(), "ready")
	configPath := filepath.Join(t.TempDir(), "config.json")
	blob, _ := json.Marshal(fixture)
	if err := os.WriteFile(configPath, blob, 0600); err != nil {
		t.Fatal(err)
	}
	child := &durableChild{command: exec.Command(os.Args[0], "-test.run=^TestDurableProcessChild$", "-test.timeout=100s"), done: make(chan struct{})}
	child.command.Env = append(os.Environ(), "STUDIO_DURABLE_PROCESS_CONFIG="+configPath)
	child.command.Stdout = &child.logs
	child.command.Stderr = &child.logs
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = child.command.Wait(); close(child.done) }()
	t.Cleanup(func() { child.kill(t) })
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-child.done:
			t.Fatalf("child startup failed: %s", child.logs.String())
		default:
		}
		if ready, err := os.ReadFile(fixture.ReadyFile); err == nil && strings.HasPrefix(string(ready), "http://127.0.0.1:") {
			child.base = string(ready)
			return child
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child startup timed out")
	return nil
}

func (c *durableChild) kill(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		return
	default:
	}
	_ = c.command.Process.Kill()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Error("child did not terminate")
	}
}

func durableFixtureStream(t *testing.T, client *http.Client, base string, identity testIdentity, cursor string) (*bufio.Scanner, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/workspaces/"+identity.Workspace.ID+"/events?project_id="+url.QueryEscape(identity.Project.ID), nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	response, err := client.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		cancel()
		t.Fatal("durable SSE rejected", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	return scanner, func() { response.Body.Close(); cancel() }
}

func durableFixtureFrame(scanner *bufio.Scanner) (id, name, data string, ok bool) {
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if name != "" {
				return id, name, data, true
			}
			continue
		}
		if value, yes := strings.CutPrefix(line, "id: "); yes {
			id = value
		}
		if value, yes := strings.CutPrefix(line, "event: "); yes {
			name = value
		}
		if value, yes := strings.CutPrefix(line, "data: "); yes {
			data = value
		}
	}
	return id, name, data, false
}

func TestPostgresCrashAfterSupplierAcceptanceDoesNotResubmitAndReplaysEvents(t *testing.T) {
	database := testDatabase(t)
	var submissions atomic.Int64
	accepted := make(chan struct{}, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != imageGenerationEndpoint {
			http.Error(w, "unexpected fixture request", 404)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		r.Body.Close()
		submissions.Add(1)
		select {
		case accepted <- struct{}{}:
		default:
		}
		<-r.Context().Done() // Simulates accepted/billed work whose reply never reaches the caller.
	}))
	t.Cleanup(gateway.Close) // Registered before child cleanup, so blocked requests die first.
	fixture := durableProcessConfig{DatabaseURL: database, ReferenceDir: filepath.Join(t.TempDir(), "refs"), AssetDir: filepath.Join(t.TempDir(), "assets"), GatewayURL: gateway.URL}
	first := startDurableChild(t, fixture)
	client := authClient(t)
	identity := registerFixture(t, client, first.base, "durable-crash@example.test")
	scanner, closeStream := durableFixtureStream(t, client, first.base, identity, "")
	cursor, name, _, ok := durableFixtureFrame(scanner)
	closeStream()
	if !ok || name != "stream.reset" || cursor == "" {
		t.Fatal("initial durable snapshot cursor missing")
	}
	input := mediaFixtureInput("image", 1)
	input.ProjectID = identity.Project.ID
	code, data, _ := authRequest(t, client, first.base, "POST", "/api/v1/tasks", "one-accepted-generation", identity.Project.ID, input)
	if code != 202 {
		t.Fatalf("create task %d %s", code, data)
	}
	task := decode[Task](t, data)
	select {
	case <-accepted:
	case <-time.After(8 * time.Second):
		t.Fatal("supplier did not receive original submit")
	}
	code, data, _ = authRequest(t, client, first.base, "GET", "/api/v1/tasks/"+task.ID, "", identity.Project.ID, nil)
	if code != 200 || decode[Task](t, data).Status != "SUBMITTING" {
		t.Fatal("POST occurred before durable SUBMITTING checkpoint")
	}
	first.kill(t)
	second := startDurableChild(t, fixture)
	code, data, _ = authRequest(t, client, second.base, "GET", "/api/v1/tasks/"+task.ID, "", identity.Project.ID, nil)
	if code != 200 || decode[Task](t, data).Status != "SUBMIT_UNKNOWN" {
		t.Fatalf("ambiguous submission did not park after crash: %d %s", code, data)
	}
	code, data, headers := authRequest(t, client, second.base, "POST", "/api/v1/tasks", "one-accepted-generation", identity.Project.ID, input)
	if code != 202 || decode[Task](t, data).ID != task.ID || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("original idempotency replay changed after crash")
	}
	scanner, closeStream = durableFixtureStream(t, client, second.base, identity, cursor)
	defer closeStream()
	recovered := false
	for i := 0; i < 30; i++ {
		id, eventName, eventData, ok := durableFixtureFrame(scanner)
		if !ok {
			break
		}
		if eventName == "stream.reset" {
			t.Fatal("valid retained pre-crash cursor was silently reset")
		}
		if eventName == "resource.changed" && strings.Contains(eventData, task.ID) && strings.Contains(eventData, "SUBMIT_UNKNOWN") {
			if id == cursor || strings.Contains(eventData, input.Inputs.Prompt) || strings.Contains(eventData, fixtureMediaKey) {
				t.Fatal("replay cursor or payload privacy invalid")
			}
			recovered = true
			break
		}
	}
	if !recovered {
		t.Fatal("durable recovery event not replayed across actual process death")
	}
	if submissions.Load() != 1 {
		t.Fatal("ambiguous accepted generation was submitted more than once")
	}
	t.Log("Actual process kill/restart: one supplier POST, original idempotency preserved, SUBMIT_UNKNOWN parked, pre-crash SSE cursor recovered.")
}
