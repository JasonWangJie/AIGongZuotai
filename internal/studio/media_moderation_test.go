package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func mediaPolicyFixture(t *testing.T) *mediaModerationEngine {
	t.Helper()
	e, err := newMediaModerationEngine(MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func mediaBytesFixture(t *testing.T, name string) ([]byte, MediaModerationContent) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "moderation", name))
	if err != nil {
		t.Fatal(err)
	}
	c := MediaModerationContent{Kind: "image", ContentType: "image/png", ByteSize: int64(len(raw)), SHA256: moderationSHA(raw)}
	if strings.HasSuffix(name, ".mp4") {
		c.Kind = "video"
		c.ContentType = "video/mp4"
	}
	return raw, c
}
func writeMediaPolicyFixture(t *testing.T, raw []byte) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "media-policy.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestMediaModerationExactBytesBothKindsAndPhases(t *testing.T) {
	e := mediaPolicyFixture(t)
	for _, ext := range []string{"png", "mp4"} {
		for _, decision := range []string{"allow", "block", "review"} {
			for _, phase := range []string{"input", "output"} {
				t.Run(decision+"-"+ext+"-"+phase, func(t *testing.T) {
					raw, c := mediaBytesFixture(t, decision+"."+ext)
					got := e.Check(context.Background(), e.Snapshot(), phase, c, bytes.NewReader(raw))
					want := map[string]string{"allow": "ALLOWED", "block": "BLOCKED", "review": "REVIEW"}[decision]
					if got.State != want {
						t.Fatalf("got %v want %s", got, want)
					}
				})
			}
		}
	}
	// Unrecognized valid image bytes and altered metadata do not inherit allow.
	raw, c := mediaBytesFixture(t, "allow.png")
	unknown := append(append([]byte{}, raw...), 0)
	c.ByteSize = int64(len(unknown))
	c.SHA256 = moderationSHA(unknown)
	if got := e.Check(context.Background(), e.Snapshot(), "input", c, bytes.NewReader(unknown)); got.State != "REVIEW" {
		t.Fatal("unknown bytes allowed", got)
	}
	_, c = mediaBytesFixture(t, "allow.png")
	c.ContentType = "image/jpeg"
	if got := e.Check(context.Background(), e.Snapshot(), "input", c, bytes.NewReader(raw)); got.ReasonCode != "invalid_content" {
		t.Fatal("declared type trusted", got)
	}
	c.ContentType = "image/png"
	c.ByteSize++
	if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "REVIEW" {
		t.Fatal("length mismatch accepted", got)
	}
	_, c = mediaBytesFixture(t, "allow.png")
	raw[20] ^= 1
	if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "REVIEW" {
		t.Fatal("hash mismatch accepted", got)
	}
}

func TestMediaModerationFrozenPolicyAndConfiguration(t *testing.T) {
	e := mediaPolicyFixture(t)
	old := e.Snapshot()
	raw, c := mediaBytesFixture(t, "allow.png")
	e.policy.Fixtures[1].Decision = "block"
	e.policy.PolicyVersion = "next"
	e.policy.Digest = mediaModerationPolicyDigest(e.policy)
	if got := e.Check(context.Background(), old, "output", c, bytes.NewReader(raw)); got.State != "ALLOWED" {
		t.Fatal("old policy replaced", got)
	}
	if got := e.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.State != "BLOCKED" {
		t.Fatal("new policy ignored", got)
	}
	old.MaxImageBytes++
	if got := e.Check(context.Background(), old, "output", c, bytes.NewReader(raw)); got.ReasonCode != "policy_unavailable" {
		t.Fatal("tampered snapshot accepted", got)
	}
	var disabled *mediaModerationEngine
	if got := disabled.Check(context.Background(), e.Snapshot(), "output", c, bytes.NewReader(raw)); got.ReasonCode != "policy_unavailable" {
		t.Fatal("disabled engine released bytes", got)
	}
	if v, err := newMediaModerationEngine(MediaModerationConfig{}, "demo"); err != nil || v != nil {
		t.Fatal("disabled default changed")
	}
	if _, err := newMediaModerationEngine(MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}, "live"); err == nil {
		t.Fatal("fixtures accepted live")
	}
}

func TestMediaModerationPolicyStrictLimits(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "media-moderation.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p mediaModerationPolicyFile
	if json.Unmarshal(raw, &p) != nil {
		t.Fatal("fixture")
	}
	canonical, _ := json.Marshal(p)
	bad := [][]byte{[]byte("null"), []byte("[]"), append(append([]byte{}, canonical...), []byte(" {}")...), []byte(strings.Replace(string(canonical), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)), []byte(strings.Replace(string(canonical), `"policy_id"`, `"Policy_ID"`, 1)), []byte(strings.Replace(string(canonical), `"byte_size"`, `"Byte_Size"`, 1)), []byte(strings.Repeat("x", moderationPolicyLimit+1))}
	for _, mutate := range []func(*mediaModerationPolicyFile){func(p *mediaModerationPolicyFile) { p.MaxImageBytes = mediaModerationImageLimit + 1 }, func(p *mediaModerationPolicyFile) { p.MaxVideoBytes = mediaModerationVideoLimit + 1 }, func(p *mediaModerationPolicyFile) { p.Fixtures = nil }, func(p *mediaModerationPolicyFile) { p.Fixtures[0].ByteSize = 0 }, func(p *mediaModerationPolicyFile) { p.Fixtures[0].Phase = "both" }, func(p *mediaModerationPolicyFile) { p.Fixtures[0].Decision = "ALLOW" }, func(p *mediaModerationPolicyFile) { p.PolicyID = "bad\x00id" }, func(p *mediaModerationPolicyFile) { p.Fixtures = append(p.Fixtures, p.Fixtures[0]) }} {
		q := p
		q.Fixtures = append([]MediaModerationFixture{}, p.Fixtures...)
		mutate(&q)
		v, _ := json.Marshal(q)
		bad = append(bad, v)
	}
	for i, v := range bad {
		if _, err := newMediaModerationEngine(MediaModerationConfig{Mode: "fixtures", PolicyFile: writeMediaPolicyFixture(t, v)}, "demo"); err == nil {
			t.Fatalf("invalid policy %d accepted", i)
		}
	}
	if _, err := newMediaModerationEngine(MediaModerationConfig{Mode: "fixtures", PolicyFile: writeMediaPolicyFixture(t, canonical), APIKey: "do-not-leak-secret"}, "demo"); err == nil || strings.Contains(err.Error(), "do-not-leak-secret") {
		t.Fatal("credentials accepted or leaked")
	}
}

func mediaCandidateFixture(t *testing.T) Artifact {
	raw, c := mediaBytesFixture(t, "allow.png")
	_ = raw
	id := "asset_" + strings.Repeat("a", 64)
	return Artifact{ID: id, URL: "/api/v1/assets/" + id, Kind: "image", Title: "fixture", StorageInfo: &AssetInfo{ID: id, ContentType: c.ContentType, ByteSize: c.ByteSize, SHA256: c.SHA256}}
}
func TestMediaModerationPrivateCandidateCodec(t *testing.T) {
	a := mediaCandidateFixture(t)
	raw, err := encodeMediaCandidates([]Artifact{a}, "image")
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMediaCandidates(raw)
	if err != nil || len(got) != 1 || got[0].StorageInfo == nil || got[0].StorageInfo.SHA256 != a.StorageInfo.SHA256 {
		t.Fatal("private storage identity lost", err)
	}
	public, _ := json.Marshal(got[0])
	if bytes.Contains(public, []byte(a.StorageInfo.SHA256)) {
		t.Fatal("private identity leaked into Artifact JSON")
	}
	a.URL = "https://private.invalid/secret"
	if _, err := encodeMediaCandidates([]Artifact{a}, "image"); err == nil {
		t.Fatal("remote URL persisted as candidate")
	}
	a = mediaCandidateFixture(t)
	if _, err := encodeMediaCandidates([]Artifact{a, a}, "image"); err == nil {
		t.Fatal("duplicate candidate accepted")
	}
}

func TestMediaModerationReleaseIdentityAndState(t *testing.T) {
	candidate := mediaCandidateFixture(t)
	second := candidate
	second.ID = "asset_" + strings.Repeat("b", 64)
	second.URL = "/api/v1/assets/" + second.ID
	candidates := []Artifact{candidate, second}
	task := Task{Status: "SUCCEEDED", Moderation: &ModerationSummary{State: "ALLOWED"}, Artifacts: append([]Artifact{}, candidates...)}
	for i := range task.Artifacts {
		task.Artifacts[i].StorageInfo = nil
	}
	if !validMediaModerationRelease(task, candidates) {
		t.Fatal("public JSON without private storage identity rejected")
	}
	for _, mutate := range []func(*Task){
		func(t *Task) { t.Status = "MODERATING" },
		func(t *Task) { t.Artifacts = t.Artifacts[:1] },
		func(t *Task) { t.Artifacts[0], t.Artifacts[1] = t.Artifacts[1], t.Artifacts[0] },
		func(t *Task) { t.Artifacts[0].URL = "https://private.invalid" },
		func(t *Task) { t.Artifacts[0].Kind = "video" },
		func(t *Task) { t.Moderation.State = "REVIEW" },
		func(t *Task) { t.Moderation.State = "PENDING" },
		func(t *Task) { t.Moderation.State = "BLOCKED" },
	} {
		changed := task
		changed.Moderation = copyRunModeration(task.Moderation)
		changed.Artifacts = append([]Artifact{}, task.Artifacts...)
		mutate(&changed)
		if validMediaModerationRelease(changed, candidates) {
			t.Fatal("inconsistent release accepted")
		}
	}
}

func TestPostgresMediaModerationFrozenCandidatesAndReference(t *testing.T) {
	database := testDatabase(t)
	a, server := testApp(t, Config{DatabaseURL: database, AuthMode: "required"})
	identity := registerFixture(t, authClient(t), server.URL, "media-policy@example.test")
	// This is a SQL-helper transaction fixture, not an active leased execution.
	// Stop and join background workers before inserting its due task: a worker
	// legitimately reclaiming the frozen candidates would park them in REVIEW
	// and race the PENDING -> ALLOWED/REVIEW assertions below.
	a.cancel()
	a.wg.Wait()
	e := mediaPolicyFixture(t)
	state := &MediaModerationRecord{Policy: e.Snapshot(), TextPolicy: rulesModerationFixture(t).Snapshot()}
	state.Summary = InitialMediaModerationSummary(state.Policy, "output")
	task := Task{ID: newID("task"), WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, ProviderMode: "demo", Kind: "image", Status: "INGESTING", Moderation: copyRunModeration(state.Summary)}
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		doc, _ := json.Marshal(task)
		_, err = tx.Exec(ctx, `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll) VALUES($1,$2,$3,$4,'{}','{}',clock_timestamp())`, task.ID, task.WorkspaceID, task.ProjectID, doc)
		if err != nil {
			return err
		}
		if err = insertTaskMediaModerationSQL(ctx, tx, task, state); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Candidate registration rollback cannot publish or lose original evidence.
	state.Candidates = []Artifact{mediaCandidateFixture(t)}
	transact := func(commit bool, desired Task, private *MediaModerationRecord) error {
		return a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			if err = saveTaskMediaModerationSQL(ctx, tx, desired, private); err != nil {
				return err
			}
			if commit {
				return tx.Commit(ctx)
			}
			return nil
		})
	}
	if err = transact(false, task, state); err != nil {
		t.Fatal(err)
	}
	var restored *MediaModerationRecord
	load := func() {
		t.Helper()
		if err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var e error
			restored, e = loadTaskMediaModerationSQL(ctx, conn, task.ID)
			return e
		}); err != nil {
			t.Fatal(err)
		}
	}
	load()
	if len(restored.Candidates) != 0 {
		t.Fatal("candidate escaped rollback")
	}
	if err = transact(true, task, state); err != nil {
		t.Fatal(err)
	}
	load()
	if len(restored.Candidates) != 1 || restored.Summary.State != "PENDING" || restored.Summary.Version != 1 {
		t.Fatal("candidate registration changed the pending decision")
	}
	// An ALLOWED summary alone must never publish while still processing, or
	// register a different public file than the immutable candidate set.
	allowed := task
	allowed.Moderation = copyRunModeration(task.Moderation)
	allowed.Moderation.State, allowed.Moderation.ReasonCode, allowed.Moderation.Version = "ALLOWED", "allowed", 2
	allowed.Artifacts = append([]Artifact{}, state.Candidates...)
	if err = transact(false, allowed, nil); err == nil || a.store.failed.Load() {
		t.Fatal("ALLOWED processing state accepted or poisoned database", err)
	}
	allowed.Status = "SUCCEEDED"
	allowed.Artifacts[0].URL = "https://wrong.invalid/asset"
	if err = transact(false, allowed, nil); err == nil || a.store.failed.Load() {
		t.Fatal("mismatched release accepted or poisoned database", err)
	}
	allowed.Artifacts[0] = state.Candidates[0]
	allowed.Artifacts[0].StorageInfo = nil
	if err = transact(false, allowed, nil); err != nil {
		t.Fatal("matching public artifact without private fields rejected", err)
	}
	// Operator/cancel callers omit private state; original candidates survive.
	task.Moderation.State = "REVIEW"
	task.Moderation.ReasonCode = "cancelled"
	task.Moderation.Version = 2
	if err = transact(true, task, nil); err != nil {
		t.Fatal(err)
	}
	load()
	if len(restored.Candidates) != 1 || restored.TextPolicy.Digest != state.TextPolicy.Digest || restored.Summary.State != "REVIEW" {
		t.Fatal("original private state lost")
	}
	if err = transact(true, task, nil); err != nil {
		t.Fatal("exact replay failed", err)
	}
	task.Moderation.State = "ALLOWED"
	task.Moderation.ReasonCode = "allowed"
	task.Moderation.Version = 3
	if err = transact(false, task, nil); err == nil || a.store.failed.Load() {
		t.Fatal("final decision changed or DB fuse tripped", err)
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var count int
	if err = admin.QueryRow(context.Background(), `SELECT count(*) FROM studio_media_moderation_decisions WHERE task_id=$1`, task.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("decision replay duplicated history", count, err)
	}
	if _, err = admin.Exec(context.Background(), `DELETE FROM studio_task_media_candidates WHERE task_id=$1`, task.ID); err == nil {
		t.Fatal("candidate evidence deleted")
	}
	if _, err = admin.Exec(context.Background(), `UPDATE studio_task_media_moderation SET text_policy='{}' WHERE task_id=$1`, task.ID); err == nil {
		t.Fatal("text policy replaced")
	}
	refID := newID("ref")
	approved := InitialMediaModerationSummary(state.Policy, "input")
	approved.State = "ALLOWED"
	approved.ReasonCode = "allowed"
	approved.Version = 2
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `INSERT INTO studio_references(id,workspace_id,project_id,name,blob,stored_bytes,created_at) VALUES($1,$2,$3,'fixture','{}',1,clock_timestamp())`, refID, task.WorkspaceID, task.ProjectID)
		if err != nil {
			return err
		}
		if err = insertReferenceMediaModerationSQL(ctx, tx, refID, task.WorkspaceID, task.ProjectID, state.Policy, approved); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	p, s, err := loadReferenceMediaModerationSQL(context.Background(), admin, refID)
	if err != nil || p.Digest != state.Policy.Digest || s.Phase != "input" || s.State != "ALLOWED" {
		t.Fatal("reference approval did not roundtrip", err)
	}
}
