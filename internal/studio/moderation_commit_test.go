package studio

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresModerationUnknownCommitRecoversOnlyCommittedDecision(t *testing.T) {
	for _, decision := range []string{"ALLOWED", "BLOCKED"} {
		t.Run(decision, func(t *testing.T) {
			database := testDatabase(t)
			proxy, armed, dropped := commitAckDropProxy(t, database)
			s, err := openPostgres(proxy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.close)
			engine, err := newModerationEngine(executionModerationConfig(t, nil), "demo")
			if err != nil {
				t.Fatal(err)
			}
			input := RunInput{ProjectID: "prj_demo", ModelID: "studio-chat", Input: "safe original request"}
			key := newID("key")
			run := outboxFixtureRun(newID("run"))
			run.Output, run.Prompt = "", input.Input
			run.ModerationPolicy = engine.Snapshot()
			run.Moderation = InitialModerationSummary(run.ModerationPolicy)
			if err = s.createRunCandidate(&run, "ws_demo:runs:"+key, payloadHash(input)); err != nil {
				t.Fatal(err)
			}
			run.Status = "RUNNING"
			if err = s.saveRunCandidate(&run); err != nil {
				t.Fatal(err)
			}
			record := &runRecord{Run: copyRun(run), Subscribers: map[chan event]bool{}}
			candidate := copyRun(run)
			candidate.Moderation.State, candidate.Moderation.Version = decision, 2
			status := "FAILED"
			if decision == "ALLOWED" {
				candidate.Moderation.ReasonCode = "allowed"
				candidate.Output = "APPROVED-ONLY-AFTER-COMMIT"
				status = "COMPLETED"
			} else {
				candidate.Moderation.ReasonCode = "policy_block"
				candidate.Error = &APIError{Code: "MODERATION_OUTPUT_BLOCKED", Message: "输出保持隔离"}
			}
			// No unrelated background transaction can consume this armed COMMIT.
			a := &App{store: s, config: Config{ReplaySize: 16}}
			armed.Store(true)
			err = a.finishRunCandidateLocked(record, candidate, status)
			if err == nil || !s.failed.Load() {
				t.Fatal("lost COMMIT acknowledgement did not fail closed")
			}
			select {
			case <-dropped:
			case <-time.After(time.Second):
				t.Fatal("proxy did not drop the final moderation COMMIT acknowledgement")
			}
			if record.Run.Output != "" || record.Run.Moderation.State != "PENDING" || len(record.Events) != 0 {
				t.Fatal("unconfirmed moderation decision entered cache or SSE")
			}
			s.close()
			// Restart with moderation disabled: old policy decisions remain in force.
			restarted, err := New(Config{DatabaseURL: database, ReferenceDir: filepath.Join(t.TempDir(), "references"), AssetDir: filepath.Join(t.TempDir(), "assets")})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			provider := &moderatedExecutionChat{text: "MUST-NOT-REGENERATE"}
			restarted.chat = provider
			server := httptest.NewServer(restarted.Handler())
			defer server.Close()
			code, body, _ := authRequest(t, authClient(t), server.URL, "GET", "/api/v1/runs/"+run.ID+"/snapshot", "", "", nil)
			if code != 200 {
				t.Fatal("committed run not readable after restart", code)
			}
			recovered := decode[Run](t, body)
			if recovered.Status != status || recovered.Moderation == nil || recovered.Moderation.State != decision || recovered.Moderation.Version != 2 {
				t.Fatal("public run and moderation decision did not commit together")
			}
			if decision == "ALLOWED" && recovered.Output != candidate.Output || decision == "BLOCKED" && (recovered.Output != "" || strings.Contains(string(body), "APPROVED-ONLY")) {
				t.Fatal("restart violated the committed release decision")
			}
			replayed, replay, err := restarted.createRun(input, key)
			if err != nil || !replay || replayed.ID != run.ID || provider.calls.Load() != 0 {
				t.Fatal("unknown original request did not replay without another generation")
			}
			var count int
			err = restarted.store.do(func(ctx context.Context, conn *pgx.Conn) error {
				return conn.QueryRow(ctx, "SELECT count(*) FROM studio_moderation_decisions WHERE run_id=$1", run.ID).Scan(&count)
			})
			if err != nil || count != 2 {
				t.Fatal("decision history was lost or repeated", err)
			}
		})
	}
}
