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
	"sync/atomic"
	"testing"
	"time"
)

type networkBrowserInput struct {
	URL          string `json:"url"`
	Dist         string `json:"dist"`
	Binary       string `json:"binary"`
	Mode         string `json:"mode"`
	SessionName  string `json:"session_name"`
	SessionValue string `json:"session_value"`
	Project      string `json:"project"`
	Conversation string `json:"conversation"`
	Title        string `json:"title"`
	Leaf         string `json:"leaf"`
	UserNode     string `json:"user_node"`
	Turns        int    `json:"turns"`
}

func TestLoopbackNetworkCalibrationChromium(t *testing.T) {
	if os.Getenv("NETWORK_CALIBRATION_BROWSER") != "1" {
		t.Skip("opt-in loopback network calibration without a database")
	}
	report := os.Getenv("NETWORK_CALIBRATION_REPORT")
	if report == "" && os.Getenv("NETWORK_BROWSER_TEST") == "" {
		report = os.Getenv("NETWORK_BROWSER_REPORT")
	}
	if !filepath.IsAbs(report) {
		t.Fatal("absolute path for a fresh calibration report required")
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Fatal("report path must be unused")
	}
	probe := newNetworkProbe(http.NotFoundHandler())
	server := httptest.NewServer(probe)
	defer server.Close()
	result := runNetworkBrowser(t, networkBrowserInput{URL: server.URL, Mode: "calibration"})
	probe.mu.Lock()
	progress := probe.progress
	probe.mu.Unlock()
	result["server_calibration_totals"] = progress
	writeNetworkReport(t, report, result)
	if result["passed"] != true || progress.RTT != 24 || progress.Uploads != 9 || progress.VerifiedUploads != 9 ||
		progress.UploadBytes != 9*(4<<20) || progress.Downloads != 9 || progress.DownloadBytes != 9*(4<<20) {
		t.Fatal("calibration incomplete; raw samples, numeric input and failure point are in the report")
	}
}

func TestPostgresWorkbenchNetworkBrowser(t *testing.T) {
	mode := os.Getenv("NETWORK_BROWSER_TEST")
	if mode == "" {
		t.Skip("opt-in real PostgreSQL and network-conditioned Chromium")
	}
	turns := 5000
	if mode == "pilot" {
		turns = 150
	} else if mode != "full" {
		t.Fatal("expected pilot or full")
	}
	if !libraryLoadLocalDB(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("dedicated loopback test database required")
	}
	dist, report := os.Getenv("NETWORK_BROWSER_DIST"), os.Getenv("NETWORK_BROWSER_REPORT")
	if !filepath.IsAbs(dist) || !filepath.IsAbs(report) {
		t.Fatal("absolute production dist and new report path required")
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build the production workbench first")
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Fatal("report path must be unused")
	}
	var calls atomic.Int32
	key := newID("fixture")
	gateway := deepHistoryGateway(turns, &calls, key)
	defer gateway.Close()
	dsn := testDatabase(t)
	app, err := New(Config{DatabaseURL: dsn, AuthMode: "required", Mode: "live", BaseURL: gateway.URL + "/v1", APIKey: key,
		ChatModel: "deep-local", LongConversationHistory: true, WebDir: dist, AssetDir: t.TempDir(), ReferenceDir: t.TempDir()})
	if err != nil {
		t.Fatal("could not open the local workbench")
	}
	defer app.Close()
	server := httptest.NewServer(newNetworkProbe(app.Handler()))
	defer server.Close()
	client := authClient(t)
	owner := registerFixture(t, client, server.URL, "network-browser-owner@example.test")
	request := func(method, path, key string, body any, want int) []byte {
		t.Helper()
		code, data, _ := authRequest(t, client, server.URL, method, path, key, owner.Project.ID, body)
		if code != want {
			t.Fatalf("workbench fixture HTTP %d, expected %d", code, want)
		}
		return data
	}
	const title = "网络验收会话"
	c := decode[Conversation](t, request("POST", "/api/v1/conversations", newID("key"), ConversationInput{ProjectID: owner.Project.ID, Title: title}, 201))
	base := "/api/v1/conversations/" + c.ID
	var parent *string
	var last Run
	start := time.Now()
	for n := 1; n <= turns; n++ {
		in := conversationInput(uint64(n), parent, deepPrompt(n))
		r := decode[Run](t, request("POST", base+"/runs", fmt.Sprintf("network-turn-%06d", n), in, 201))
		deadline := time.Now().Add(30 * time.Second)
		for {
			last = decode[Run](t, request("GET", "/api/v1/runs/"+r.ID+"/snapshot", "", nil, 200))
			if terminal(last.Status) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("turn %d did not finish", n)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if last.ID != r.ID || last.Status != "COMPLETED" || last.Output != deepAnswer(n) || last.ConversationVersion != uint64(n+1) ||
			last.ConversationID != c.ID || last.AssistantMessageID == "" || last.UserMessageID == "" {
			t.Fatalf("invalid persisted turn %d", n)
		}
		leaf := last.AssistantMessageID
		parent = &leaf
	}
	buildMS := time.Since(start).Milliseconds()
	var total int
	if err := deliveryAdmin(t, dsn).QueryRow(context.Background(), "SELECT count(*) FROM studio_chat_messages WHERE conversation_id=$1", c.ID).Scan(&total); err != nil || total != turns*2 || int(calls.Load()) != turns {
		t.Fatal("real accepted history count changed")
	}
	recent := decode[conversationMessagePage](t, request("GET", base+"/messages?limit=20&leaf_id="+url.QueryEscape(*parent), "", nil, 200))
	if len(recent.Items) != 20 || recent.Items[19].ID != *parent || recent.Items[18].ID != last.UserMessageID || !recent.HasMore {
		t.Fatal("missing actual bounded latest conversation page")
	}
	u, _ := url.Parse(server.URL)
	input := networkBrowserInput{URL: server.URL, Dist: dist, Mode: mode, Project: owner.Project.ID, Conversation: c.ID, Title: title, Leaf: *parent, UserNode: last.UserMessageID, Turns: turns}
	for _, cookie := range client.Jar.Cookies(u) {
		if cookie.Name == sessionCookie {
			input.SessionName, input.SessionValue = cookie.Name, cookie.Value
		}
	}
	if input.SessionValue == "" {
		t.Fatal("expected the actual registered owner's HttpOnly session")
	}
	input.Binary, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := runNetworkBrowser(t, input)
	if int(calls.Load()) != turns {
		t.Fatal("history read or input initiated another generation")
	}
	result["fixture"] = map[string]any{"accepted_chat_turns": turns, "stored_messages": total, "seeding_ms": buildMS, "additional_generation_posts": 0}
	writeNetworkReport(t, report, result)
	if passed, ok := result["passed"].(bool); !ok || !passed {
		t.Fatal("network calibration or workbench limits failed; see retained measurements")
	}
}

func writeNetworkReport(t *testing.T, report string, result map[string]any) {
	t.Helper()
	file, err := os.OpenFile(report, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err = enc.Encode(result); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func runNetworkBrowser(t *testing.T, in networkBrowserInput) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/network-history-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	for _, key := range []string{"PATH", "SystemRoot", "USERPROFILE", "LOCALAPPDATA", "HOME", "TMPDIR", "TEMP", "TMP", "WORKFLOW_CHROME", "LD_LIBRARY_PATH", "XDG_RUNTIME_DIR", "DISPLAY", "WAYLAND_DISPLAY"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	pipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	attach, stop, err := longReplyProcessGuard(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := stop(); e != nil {
			t.Error(e)
		}
	}()
	var out, fault bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &fault
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err = attach(); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err)
	}
	sendErr := json.NewEncoder(pipe).Encode(in)
	closeErr := pipe.Close()
	waitErr := cmd.Wait()
	if sendErr != nil || closeErr != nil || waitErr != nil {
		var f struct {
			Stage  string
			Sample int
		}
		json.Unmarshal(fault.Bytes(), &f)
		switch f.Stage {
		case "startup", "prepare", "calibration", "interactive", "history", "input", "transfer", "report":
		default:
			f.Stage = "driver"
		}
		t.Fatalf("network browser failed stage=%s sample=%d", f.Stage, f.Sample)
	}
	var result map[string]any
	if out.Len() > 8<<20 || json.Unmarshal(out.Bytes(), &result) != nil || result["mode"] != in.Mode {
		t.Fatal("incomplete browser result")
	}
	return result
}
