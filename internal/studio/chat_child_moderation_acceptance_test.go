package studio

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

// This companion to the standard executable run needs only the fixture TLS
// authority on this one transport. The OS trust store and every other HTTP
// transport remain unchanged. TestMain runs the production child command.
func observeChildModeration(t *testing.T, ctx context.Context, f childFixture, g *childAcceptanceGateway, gateway string, port int, admin *pgx.Conn) {
	t.Helper()
	lane := g.normal + 4
	output := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var inputChecks, outputChecks atomic.Int32
	key := newID("moderation")
	moderator := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in moderationHTTPRequest
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+key || json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in) != nil {
			http.Error(w, "invalid fixture", 400)
			return
		}
		if in.Phase == "input" && len(in.Messages) == 1 && in.Messages[0].Content == activePrompt(lane) {
			inputChecks.Add(1)
			allowExecutionHTTP(w, in)
			return
		}
		if in.Phase != "output" || in.Text != activeDelta(lane, 0) || outputChecks.Add(1) != 1 {
			http.Error(w, "invalid output fixture", 400)
			return
		}
		close(output)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		writeJSON(w, 200, moderationHTTPResponse{Contract: in.Contract, RequestID: in.RequestID, PolicyID: in.PolicyID, PolicyVersion: in.PolicyVersion, PolicyDigest: in.PolicyDigest, Phase: in.Phase, ContentDigest: in.ContentDigest, Decision: "block", ReasonCode: "policy_block"})
	}))
	defer moderator.Close()
	config := Config{DatabaseURL: f.dsn, AssetDir: f.asset, ReferenceDir: f.reference, Mode: "live", AuthMode: "required", BaseURL: gateway + "/v1", APIKey: g.key, ModelCatalogFile: f.catalog, BillingMode: "credits", BillingPriceFile: f.prices,
		ChatChildren: ChatChildConfig{Processes: 2, SlotsPerProcess: 2},
		Moderation:   ModerationConfig{Mode: "http", Endpoint: moderator.URL, APIKey: key, CredentialID: "fixture", PolicyFile: writeFixtureModerationPolicy(t, moderationPolicyFile{SchemaVersion: 1, PolicyID: "child", PolicyVersion: "v1", MaxInputBytes: MaxChatContentBytes, MaxOutputBytes: maxRunOutputBytes, Rules: []ModerationRule{}})},
	}
	app, err := New(config)
	if err != nil {
		t.Fatal("isolated moderated controller could not start")
	}
	defer app.Close()
	certs := x509.NewCertPool()
	certs.AddCert(moderator.Certificate())
	app.moderation.http.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = certs
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	run := childAcceptTurn(t, ctx, server.URL, f.scopes[0], lane)
	childGate(t, ctx, g.lanes[lane].entered)
	g.mu.Lock()
	peer := g.lanes[lane].peerPort
	g.mu.Unlock()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("test host executable unavailable")
	}
	parent, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal("test host unavailable")
	}
	defer parent.Release()
	observed := childOwners(t, ctx, &childAcceptanceServer{proc: &exec.Cmd{Path: executable, Process: parent}}, port, []int{peer})
	if observed[peer] == 0 {
		t.Fatal("moderated model request did not come from an actual child")
	}
	close(g.lanes[lane].release)
	childGate(t, ctx, output)
	// The model has finished and the moderator holds its actual candidate;
	// the client and the durable journal must still see an empty prefix.
	if outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1", run.run.ID) != 0 {
		t.Fatal("candidate was journaled before approval")
	}
	sse := capacityOpen(t, ctx, run.scope, server.URL, run.run.ID)
	name, cursor, data := readSSEFixture(t, bufio.NewReader(sse.Body))
	sse.Body.Close()
	// Sequence zero snapshots carry seq=0 in JSON and no positive SSE id.
	if name != "run.snapshot" || cursor != "" || string(data["seq"]) != "0" || string(data["output"]) != "\"\"" {
		t.Fatal("pending moderated output visible to subscriber")
	}
	unblock()
	final := run.wait(t, ctx, server.URL, "FAILED", 1)
	if final.Output != "" || final.Moderation == nil || final.Moderation.State != "BLOCKED" {
		t.Fatal("blocked candidate reached final public view")
	}
	childLedgerState(t, admin, run, "REVIEW", true, 0)
	if outboxCount(t, admin, `SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND (name='message.delta' OR COALESCE(data->>'output','')<>'')`, run.run.ID) != 0 {
		t.Fatal("blocked text persisted in public journal")
	}
	run.replay(t, ctx, server.URL)
	if inputChecks.Load() != 1 || outputChecks.Load() != 1 {
		t.Fatal("replay duplicated model moderation")
	}
	// Let transport exit before any later parent/worker is started.
	childGate(t, ctx, g.lanes[lane].finished)
}
