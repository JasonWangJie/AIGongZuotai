package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMediaModerationReferenceRejectBeforeRegistrationAndReplayFirst(t *testing.T) {
	a, s := testApp(t, Config{})
	a.mediaModeration = mediaPolicyFixture(t) // Unit seam; public startup requires PG/auth.
	for _, name := range []string{"block.png", "review.png"} {
		data, _ := mediaBytesFixture(t, name)
		blob, err := a.references.PutImage(context.Background(), bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = a.acceptReference("ws_demo", "prj_demo", name, "ws_demo:references:"+name, blob, "")
		if err == nil || len(a.referenceItems) != 0 || len(a.referenceIdem) != 0 || len(a.library) != 0 {
			t.Fatal("rejected reference registered or was accepted", name, err)
		}
	}
	data, _ := mediaBytesFixture(t, "allow.png")
	blob, err := a.references.PutImage(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	ref, replay, err := a.acceptReference("ws_demo", "prj_demo", "allowed.png", "ws_demo:references:original", blob, "")
	if err != nil || replay || ref.Moderation == nil || ref.Moderation.State != "ALLOWED" || ref.URL == "" {
		t.Fatal("approved reference not registered", err)
	}
	// The approved original key must remain readable when current policy denies it.
	a.mediaModeration.policy.Fixtures = nil
	_, replay, err = a.acceptReference("ws_demo", "prj_demo", "allowed.png", "ws_demo:references:original", blob, "")
	if err != nil || !replay {
		t.Fatal("old upload rechecked under changed policy", err)
	}
	code, _, _ := request(t, s, "GET", ref.URL, "", nil)
	if code != 200 {
		t.Fatal("approved original inaccessible", code)
	}
}

func TestMediaModerationPrivateLibraryProjectionAndContentGates(t *testing.T) {
	a, s := testApp(t, Config{})
	data, _ := mediaBytesFixture(t, "allow.png")
	info, err := importMediaModerationFixture(context.Background(), a.assets, "access-test", "image", data)
	if err != nil {
		t.Fatal(err)
	}
	policy := mediaPolicyFixture(t).Snapshot()
	for _, state := range []string{"PENDING", "BLOCKED", "REVIEW", "ALLOWED"} {
		summary := InitialMediaModerationSummary(policy, "output")
		summary.State = state
		task := Task{ID: "task_" + state, WorkspaceID: "ws_demo", ProjectID: "prj_demo", Kind: "image", ProviderMode: "demo", Status: "SUCCEEDED", Moderation: summary, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		entry := libraryFromArtifact(task, Artifact{ID: info.ID, URL: "/api/v1/assets/" + info.ID, Kind: "image", StorageInfo: &info})
		a.library = map[string]libraryRecord{entry.ID: entry}
		public := entry.public()
		want := 404
		if state == "ALLOWED" {
			want = 200
		}
		if (public.ContentURL != nil) != (want == 200) || (public.ThumbnailURL != nil) != (want == 200) {
			t.Fatal("metadata exposes restricted content", state)
		}
		for _, path := range []string{"/api/v1/assets/" + info.ID, "/api/v1/library/" + entry.ID + "/content?project_id=prj_demo", "/api/v1/library/" + entry.ID + "/thumbnail?project_id=prj_demo"} {
			code, _, _ := request(t, s, "GET", path, "", nil)
			if code != want {
				t.Fatal("content gate", state, path, code)
			}
		}
		if state == "ALLOWED" {
			entry.ModerationTaskStatus = "FAILED"
			if entry.public().ContentURL != nil {
				t.Fatal("non-delivered task escaped gate")
			}
		}
	}
}

func TestPostgresMediaModerationReferenceAuditAndPrivateCandidates(t *testing.T) {
	database := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: database, AuthMode: "required", Moderation: executionModerationConfig(t, nil), MediaModeration: MediaModerationConfig{Mode: "fixtures", PolicyFile: filepath.Join("..", "..", "config", "media-moderation.example.json")}})
	client := authClient(t)
	identity := registerFixture(t, client, s.URL, "media-access@example.test")
	for _, path := range []string{"/api/v1/assets/asset_%00", "/api/v1/library/lib_%00", "/api/v1/library/lib_%00/content", "/api/v1/library/lib_%00/thumbnail"} {
		code, _, _ := authRequest(t, client, s.URL, "GET", path, "", identity.Project.ID, nil)
		if code != 404 || a.store.failed.Load() {
			t.Fatal("malformed asset identifier reached database", path, code)
		}
	}
	cursor, _ := json.Marshal(libraryCursor{Filter: libraryFilter{WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, State: "active"}, CreatedAt: time.Now(), ID: "lib_" + strings.Repeat("a", 63) + "\x00"})
	code, _, _ := authRequest(t, client, s.URL, "GET", "/api/v1/library?cursor="+base64.RawURLEncoding.EncodeToString(cursor), "", identity.Project.ID, nil)
	if code != 400 || a.store.failed.Load() {
		t.Fatal("malformed cursor reached database", code)
	}
	code, _, _ = authRequest(t, client, s.URL, "PATCH", "/api/v1/library/lib_%00", "bad-id-key", identity.Project.ID, map[string]any{"expected_revision": 1, "name": "test", "tags": []string{}})
	if code != 404 || a.store.failed.Load() {
		t.Fatal("malformed mutation reached database", code)
	}
	code, _, _ = authRequest(t, client, s.URL, "GET", "/readyz", "", identity.Project.ID, nil)
	for _, path := range []string{"/api/v1/library?q=%ff", "/api/v1/library?tag=%ff"} {
		invalidCode, _, _ := authRequest(t, client, s.URL, "GET", path, "", identity.Project.ID, nil)
		if invalidCode != 400 || a.store.failed.Load() {
			t.Fatal("invalid UTF-8 reached database", path, invalidCode)
		}
	}
	code, _, _ = authRequest(t, client, s.URL, "GET", "/readyz", "", identity.Project.ID, nil)
	if code != 200 {
		t.Fatal("bad asset identifier tripped readiness", code)
	}
	admin, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	data, _ := mediaBytesFixture(t, "allow.png")
	blob, err := a.references.PutImage(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	ref, _, err := a.acceptReference(identity.Workspace.ID, identity.Project.ID, "source.png", identity.Workspace.ID+":references:original", blob, identity.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ref.URL, ref.ThumbnailURL, "/api/v1/references?project_id=" + identity.Project.ID, "/api/v1/library?project_id=" + identity.Project.ID} {
		code, body, _ := authRequest(t, client, s.URL, "GET", path, "", identity.Project.ID, nil)
		if code != 200 {
			t.Fatal("approved reference unavailable", path, code, string(body))
		}
		if path == "/api/v1/references?project_id="+identity.Project.ID || path == "/api/v1/library?project_id="+identity.Project.ID {
			if !bytes.Contains(body, []byte(`"state":"ALLOWED"`)) {
				t.Fatal("missing public decision", string(body))
			}
		}
	}
	// A stored, blocked candidate has no catalog entry or public download, and
	// remains reachable for maintenance regardless of terminal provider state.
	info, err := importMediaModerationFixture(context.Background(), a.assets, "durable-private-candidate", "image", data)
	if err != nil {
		t.Fatal(err)
	}
	input := TaskInput{ProjectID: identity.Project.ID, Kind: "image", ModelID: "studio-image"}
	input.Inputs.Prompt = "original"
	input.Parameters.Count = 1
	now := time.Now().UTC()
	policy := a.mediaModeration.Snapshot()
	summary := InitialMediaModerationSummary(policy, "output")
	record := &taskRecord{Task: Task{ID: newID("task"), WorkspaceID: identity.Workspace.ID, ProjectID: identity.Project.ID, Kind: "image", ModelID: "studio-image", ProviderMode: "demo", Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{}, Moderation: summary}, Input: input, NextPoll: now.Add(time.Hour), MediaModeration: &MediaModerationRecord{Policy: policy, TextPolicy: a.moderation.Snapshot(), Summary: copyRunModeration(summary)}}
	if err = a.store.createTask(record, identity.Workspace.ID+":tasks:private", payloadHash(input)); err != nil {
		t.Fatal(err)
	}
	record.Task.Status = "MODERATING"
	record.Task.Version++
	record.MediaModeration.Candidates = []Artifact{{ID: info.ID, URL: "/api/v1/assets/" + info.ID, Kind: "image", StorageInfo: &info}}
	if err = a.store.saveTask(record); err != nil {
		t.Fatal(err)
	}
	record.Task.Status = "FAILED"
	record.Task.Version++
	record.Task.Moderation.State = "BLOCKED"
	record.Task.Moderation.Version = 2
	record.Task.Moderation.ReasonCode = "policy_block"
	record.MediaModeration.Summary = copyRunModeration(record.Task.Moderation)
	if err = a.store.saveTask(record); err != nil {
		t.Fatal(err)
	}
	_, assets, err := maintenanceReachable(context.Background(), admin)
	if err != nil || !assets[info.ID] {
		t.Fatal("candidate not protected by maintenance", err)
	}
	code, _, _ = authRequest(t, client, s.URL, "GET", "/api/v1/assets/"+info.ID, "", identity.Project.ID, nil)
	if code != 404 {
		t.Fatal("private candidate downloadable", code)
	}
	if outboxCount(t, admin, "SELECT count(*) FROM studio_library_entries WHERE artifact_id=$1", info.ID) != 0 {
		t.Fatal("candidate was registered")
	}
	var raw []byte
	if err = admin.QueryRow(context.Background(), "SELECT document FROM studio_tasks WHERE id=$1", record.Task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var task Task
	if json.Unmarshal(raw, &task) != nil || len(task.Artifacts) != 0 {
		t.Fatal("candidate leaked into public document")
	}
	if _, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_media_ref() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='reference.uploaded' THEN RAISE EXCEPTION 'fixture audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_media_ref BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_media_ref()`); err != nil {
		t.Fatal(err)
	}
	_, _, err = a.acceptReference(identity.Workspace.ID, identity.Project.ID, "rollback.png", identity.Workspace.ID+":references:rollback", blob, identity.User.ID)
	if err == nil {
		t.Fatal("audit rollback accepted")
	}
	for _, table := range []string{"studio_references", "studio_reference_media_moderation", "studio_reference_idempotency", "studio_library_entries"} {
		if outboxCount(t, admin, "SELECT count(*) FROM "+table) != 1 {
			t.Fatal("reference transaction not atomic", table)
		}
	}
	if _, err = admin.Exec(context.Background(), `DROP TRIGGER reject_media_ref ON studio_audit_events; DROP FUNCTION reject_media_ref()`); err != nil {
		t.Fatal(err)
	}

}

// Exercise the real selector/trigger lock order. The business transaction owns
// a workspace row before the selector takes its lane lock; updating the active
// count twice in studio_sync_task_schedule must still acquire the lane FK's
// KEY SHARE lock without forming a lane -> workspace -> lane cycle.
func TestPostgresMediaScheduleCursorLocksAllowProjectionForeignKeys(t *testing.T) {
	database := testDatabase(t)
	a, err := newIsolatedApp(t, Config{DatabaseURL: database, PollInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	task, _, err := a.createTask(taskInput("image"), "cursor-fk-projection")
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.store.loadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(context.Background())
	selector, err := pgx.Connect(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer selector.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM studio_workspace_schedule WHERE lane='submit' AND provider_mode=$1 AND workspace_id=$2 FOR UPDATE`, a.config.Mode, task.WorkspaceID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	type selectionResult struct {
		candidate *mediaScheduleCandidate
		err       error
	}
	done := make(chan selectionResult, 1)
	go func() {
		candidate, e := a.selectMediaScheduleCandidate(ctx, selector, mediaSubmitLane)
		done <- selectionResult{candidate, e}
	}()
	joined := false
	defer func() {
		cancel()
		_ = tx.Rollback(context.Background())
		if !joined {
			// Do not close a pgx connection until its in-flight query has exited.
			<-done
		}
	}()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err = tx.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		var blockedOnWorkspace bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(wait_event_type='Lock' AND query LIKE 'SELECT task_cursor FROM studio_workspace_schedule%',false) FROM pg_stat_activity WHERE pid=$1`, int32(selector.PgConn().PID())).Scan(&blockedOnWorkspace); err != nil {
			t.Fatal(err)
		}
		if blockedOnWorkspace {
			break
		}
		select {
		case result := <-done:
			joined = true
			t.Fatalf("selector did not wait for the business workspace lock: %v", result.err)
		case <-ctx.Done():
			t.Fatal("selector never reached the workspace lock")
		case <-poll.C:
		}
	}
	record.Task.Status = "RUNNING"
	record.Task.Version++
	record.NextPoll = time.Now().Add(time.Hour)
	if err = saveBilledTaskSQL(ctx, tx, record); err != nil {
		t.Fatalf("projection/FK update deadlocked with a waiting selector: %v", err)
	}
	select {
	case result := <-done:
		joined = true
		t.Fatalf("selector bypassed an uncommitted workspace update: %v", result.err)
	default:
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-done
	joined = true
	if result.err != nil || result.candidate != nil {
		t.Fatalf("selector did not observe the committed future due time: candidate=%v error=%v", result.candidate != nil, result.err)
	}
	var active int
	var cursor string
	if err = writer.QueryRow(ctx, `SELECT active_count,task_cursor FROM studio_workspace_schedule WHERE lane='submit' AND provider_mode=$1 AND workspace_id=$2`, a.config.Mode, task.WorkspaceID).Scan(&active, &cursor); err != nil || active != 1 || cursor != task.ID || a.store.failed.Load() {
		t.Fatalf("projection occupancy, cursor or store health changed: active=%d cursor_matches=%v error=%v failed=%v", active, cursor == task.ID, err, a.store.failed.Load())
	}
}
