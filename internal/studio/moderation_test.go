package studio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func fixtureModerationPolicy() moderationPolicyFile {
	return moderationPolicyFile{SchemaVersion: 1, PolicyID: "fixture-text", PolicyVersion: "fixture-v1", MaxInputBytes: MaxChatContentBytes, MaxOutputBytes: maxRunOutputBytes, Rules: []ModerationRule{{Phase: "input", Contains: "[INPUT_BLOCK]", Decision: "block"}, {Phase: "input", Contains: "[INPUT_REVIEW]", Decision: "review"}, {Phase: "output", Contains: "[OUTPUT_BLOCK]", Decision: "block"}, {Phase: "output", Contains: "[OUTPUT_REVIEW]", Decision: "review"}}}
}
func writeFixtureModerationPolicy(t *testing.T, p moderationPolicyFile) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return writeFixtureModerationBytes(t, raw)
}
func writeFixtureModerationBytes(t *testing.T, raw []byte) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}
func fixtureModerationChat(text string) ChatRequest {
	return ChatRequest{Messages: []ChatMessage{{Role: "user", Content: text}}, MaxOutputTokens: 512, OutputLimitField: "max_tokens"}
}
func rulesModerationFixture(t *testing.T) *moderationEngine {
	t.Helper()
	e, err := newModerationEngine(ModerationConfig{Mode: "rules", PolicyFile: writeFixtureModerationPolicy(t, fixtureModerationPolicy())}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestModerationDisabledAndLiveRulesBoundary(t *testing.T) {
	e, err := newModerationEngine(ModerationConfig{}, "demo")
	if err != nil || e != nil {
		t.Fatal("disabled engine", err)
	}
	caps := moderationCapabilities(e)
	if caps.Enabled || caps.Mode != "disabled" || caps.TextDelivery != "stream" || !caps.MediaSupported {
		t.Fatal(caps)
	}
	if p, err := e.CheckInput(context.Background(), ChatRequest{}); p != nil || err != nil {
		t.Fatal("disabled input changed legacy behavior")
	}
	path := writeFixtureModerationPolicy(t, fixtureModerationPolicy())
	if _, err = newModerationEngine(ModerationConfig{Mode: "rules", PolicyFile: path}, "live"); err == nil {
		t.Fatal("literal rules advertised as live moderation")
	}
	e = rulesModerationFixture(t)
	caps = e.Capabilities()
	if !caps.Enabled || caps.TextDelivery != "after_review" || caps.MediaSupported || caps.PolicyID != "fixture-text" {
		t.Fatal(caps)
	}
	if InitialModerationSummary(nil) != nil {
		t.Fatal("legacy summary created")
	}
}

func TestModerationPolicyStrictConfigAndBounds(t *testing.T) {
	base := fixtureModerationPolicy()
	raw, _ := json.Marshal(base)
	var bad [][]byte
	bad = append(bad, []byte(`null`), []byte(`[]`), []byte(strings.Replace(string(raw), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)), []byte(strings.Replace(string(raw), `"policy_id"`, `"Policy_ID"`, 1)), append(append([]byte{}, raw...), []byte(` {}`)...), []byte(strings.Repeat("x", moderationPolicyLimit+1)))
	for _, change := range []func(*moderationPolicyFile){
		func(p *moderationPolicyFile) { p.SchemaVersion = 2 }, func(p *moderationPolicyFile) { p.PolicyID = "bad\x00id" }, func(p *moderationPolicyFile) { p.MaxInputBytes = MaxChatContentBytes + 1 }, func(p *moderationPolicyFile) { p.MaxOutputBytes = maxRunOutputBytes + 1 }, func(p *moderationPolicyFile) { p.MaxOutputBytes = 0 }, func(p *moderationPolicyFile) { p.Rules = []ModerationRule{} }, func(p *moderationPolicyFile) { p.Rules = nil }, func(p *moderationPolicyFile) { p.Rules[0].Decision = "allow" }, func(p *moderationPolicyFile) { p.Rules[0].Contains = "x\x00" }, func(p *moderationPolicyFile) { p.Rules[0].Contains = strings.Repeat("x", 257) }, func(p *moderationPolicyFile) { p.Rules[0].Phase = "media" },
	} {
		p := base
		p.Rules = append([]ModerationRule{}, base.Rules...)
		change(&p)
		v, _ := json.Marshal(p)
		bad = append(bad, v)
	}
	for i, blob := range bad {
		if _, err := newModerationEngine(ModerationConfig{Mode: "rules", PolicyFile: writeFixtureModerationBytes(t, blob)}, "demo"); err == nil {
			t.Fatal("invalid policy accepted", i)
		}
	}
	if _, err := newModerationEngine(ModerationConfig{Mode: "rules", PolicyFile: writeFixtureModerationPolicy(t, base), APIKey: "private-fixture-secret"}, "demo"); err == nil || strings.Contains(err.Error(), "private-fixture-secret") {
		t.Fatal("credentials ignored or leaked", err)
	}
}

func TestModerationChecksCompleteInputAndFrozenOutput(t *testing.T) {
	e := rulesModerationFixture(t)
	ctx := context.Background()
	request := fixtureModerationChat("普通内容🌌")
	policy, err := e.CheckInput(ctx, request)
	if err != nil || !validModerationPolicy(policy) {
		t.Fatal(err)
	}
	summary := InitialModerationSummary(policy)
	if !validModerationSummary(summary) || summary.State != "PENDING" || summary.Version != 1 {
		t.Fatal(summary)
	}
	request.Messages = []ChatMessage{{Role: "user", Content: "历史输入"}, {Role: "assistant", Content: "历史 [INPUT_BLOCK]"}, {Role: "user", Content: "最新普通内容"}}
	if _, err = e.CheckInput(ctx, request); err == nil {
		t.Fatal("only latest user input was checked")
	}
	if _, err = e.CheckInput(ctx, fixtureModerationChat("[INPUT_REVIEW]")); err == nil {
		t.Fatal("review input generated")
	}
	for _, sample := range []struct{ text, state, reason string }{{"普通结果", "ALLOWED", "allowed"}, {"[OUTPUT_REVIEW]", "REVIEW", "policy_review"}, {"[OUTPUT_REVIEW] [OUTPUT_BLOCK]", "BLOCKED", "policy_block"}, {"x\x00y", "REVIEW", "invalid_content"}, {"", "REVIEW", "invalid_content"}, {strings.Repeat("x", maxRunOutputBytes+1), "REVIEW", "content_too_large"}} {
		got := e.CheckOutput(ctx, policy, sample.text)
		if got.State != sample.state || got.ReasonCode != sample.reason {
			t.Fatal("output boundary", got, sample.state)
		}
	}
	next := fixtureModerationPolicy()
	next.PolicyVersion = "fixture-v2"
	next.Rules = []ModerationRule{{Phase: "output", Contains: "new-block", Decision: "block"}}
	replacement, err := newModerationEngine(ModerationConfig{Mode: "rules", PolicyFile: writeFixtureModerationPolicy(t, next)}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got := replacement.CheckOutput(ctx, policy, "[OUTPUT_BLOCK]"); got.State != "BLOCKED" {
		t.Fatal("new policy replaced accepted rules", got)
	}
	if got := replacement.CheckOutput(ctx, replacement.Snapshot(), "[OUTPUT_BLOCK]"); got.State != "ALLOWED" {
		t.Fatal("new acceptance did not use new rules", got)
	}
	policy.Rules[2].Contains = "tampered"
	if got := e.CheckOutput(ctx, policy, "ordinary"); got.ReasonCode != "policy_unavailable" {
		t.Fatal("mutated frozen policy accepted", got)
	}
	if got := e.CheckOutput(ctx, e.Snapshot(), "[OUTPUT_BLOCK]"); got.State != "BLOCKED" {
		t.Fatal("snapshot mutated live configuration")
	}
	var disabled *moderationEngine
	if got := disabled.CheckOutput(ctx, e.Snapshot(), "ordinary"); got.State != "REVIEW" {
		t.Fatal("disabled config released existing pending content")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := e.CheckOutput(cancelled, e.Snapshot(), "ordinary"); got.State != "REVIEW" {
		t.Fatal("cancelled review approved")
	}
}

func TestModerationInputLimitsAndSummaryClosure(t *testing.T) {
	file := fixtureModerationPolicy()
	file.MaxInputBytes = 12
	e, err := newModerationEngine(ModerationConfig{Mode: "rules", PolicyFile: writeFixtureModerationPolicy(t, file)}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.CheckInput(context.Background(), fixtureModerationChat(strings.Repeat("x", 13))); err == nil {
		t.Fatal("policy input limit ignored")
	}
	if _, err = e.CheckInput(context.Background(), fixtureModerationChat("x\x00y")); err == nil {
		t.Fatal("NUL input accepted")
	}
	if _, err = e.CheckInput(context.Background(), fixtureModerationChat(string([]byte{255}))); err == nil {
		t.Fatal("invalid UTF8 input accepted")
	}
	for _, s := range []ModerationSummary{{PolicyID: "fixture", PolicyVersion: "v1", Phase: "output", State: "ALLOWED", Version: 2, ReasonCode: "private-error"}, {PolicyID: "fixture", PolicyVersion: "v1", Phase: "output", State: "PENDING", Version: 1, ReasonCode: "allowed"}, {PolicyID: "fixture", PolicyVersion: "v1", Phase: "input", State: "REVIEW", Version: 2, ReasonCode: "cancelled"}, {PolicyID: "fixture", PolicyVersion: "v1", Phase: "output", State: "ALLOWED", Version: 0, ReasonCode: "allowed"}} {
		if validModerationSummary(&s) {
			t.Fatal("open summary accepted", s)
		}
	}
	for _, reason := range []string{"execution_incomplete", "process_restarted", "cancelled"} {
		s := ModerationSummary{PolicyID: "fixture", PolicyVersion: "v1", Phase: "output", State: "REVIEW", Version: 2, ReasonCode: reason}
		if !validModerationSummary(&s) {
			t.Fatal("core recovery reason rejected")
		}
	}
}

func TestPostgresModerationFrozenPolicyAndDecisionTransaction(t *testing.T) {
	database := testDatabase(t)
	a, server := testApp(t, Config{DatabaseURL: database, AuthMode: "required"})
	identity := registerFixture(t, authClient(t), server.URL, "moderation-evidence@example.test")
	engine := rulesModerationFixture(t)
	run := outboxFixtureRun(newID("run"))
	run.WorkspaceID, run.ProjectID, run.ProviderMode = identity.Workspace.ID, identity.Project.ID, "demo"
	run.Output = ""
	run.ModerationPolicy = engine.Snapshot()
	run.Moderation = InitialModerationSummary(run.ModerationPolicy)
	if err := a.store.createRunCandidate(&run, newID("idem"), payloadHash(run)); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal("moderation inspection connection unavailable")
	}
	defer admin.Close(context.Background())
	var raw []byte
	var count int
	if err = admin.QueryRow(context.Background(), "SELECT policy FROM studio_run_moderation WHERE run_id=$1", run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var frozen ModerationPolicy
	if json.Unmarshal(raw, &frozen) != nil || !validModerationPolicy(&frozen) || frozen.Digest != run.ModerationPolicy.Digest {
		t.Fatal("policy was not frozen")
	}
	decision := run
	decision.Moderation = copyRunModeration(run.Moderation)
	decision.Moderation.State, decision.Moderation.ReasonCode, decision.Moderation.Version = "BLOCKED", "policy_block", 2
	// A savepoint/outer rollback must leave neither the public decision nor its
	// immutable evidence. The same helper is used by the run transaction.
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		return saveRunModerationSQL(ctx, tx, decision)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_moderation_decisions WHERE run_id=$1", run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("decision escaped rollback", count, err)
	}
	decision.Status = "FAILED"
	decision.Seq++
	err = a.store.saveRunCandidate(&decision)
	if err != nil {
		t.Fatal(err)
	}
	// Exact repeated state is read-only; a final decision is never overwritten.
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		return saveRunModerationSQL(ctx, tx, decision)
	})
	if err != nil {
		t.Fatal(err)
	}
	decision.Moderation.State, decision.Moderation.ReasonCode, decision.Moderation.Version = "ALLOWED", "allowed", 3
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		return saveRunModerationSQL(ctx, tx, decision)
	})
	if err == nil || a.store.failed.Load() {
		t.Fatal("final decision overwritten or conflict poisoned PG", err)
	}
	if err = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_moderation_decisions WHERE run_id=$1", run.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("decision replay duplicated evidence", count, err)
	}
	if _, err = admin.Exec(context.Background(), "UPDATE studio_run_moderation SET policy=policy||'{\"policy_id\":\"tampered\"}'::jsonb WHERE run_id=$1", run.ID); err == nil {
		t.Fatal("frozen policy changed")
	}
	if _, err = admin.Exec(context.Background(), "DELETE FROM studio_moderation_decisions WHERE run_id=$1", run.ID); err == nil {
		t.Fatal("decision audit deleted")
	}
}
