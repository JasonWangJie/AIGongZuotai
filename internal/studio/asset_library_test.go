package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

type libraryList struct {
	Items      []LibraryEntry `json:"items"`
	NextCursor *string        `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
	Limit      int            `json:"limit"`
}

func uploadLibraryReference(t *testing.T, s *httptest.Server, key string) ReferenceAsset {
	t.Helper()
	code, body, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", key, "original.png", referencePNG(t, 17), false)
	if code != 201 {
		t.Fatal(code, string(body))
	}
	return decode[ReferenceAsset](t, body)
}
func libraryEntryHTTP(t *testing.T, s *httptest.Server, id string) LibraryEntry {
	t.Helper()
	code, data, _ := request(t, s, "GET", "/api/v1/library/"+id, "", nil)
	if code != 200 {
		t.Fatal(code, string(data))
	}
	return decode[LibraryEntry](t, data)
}
func metadataInput(revision uint64, name string, tags []string) libraryMutation {
	return libraryMutation{ExpectedRevision: &revision, Name: &name, Tags: &tags}
}
func stateInput(revision uint64) libraryMutation { return libraryMutation{ExpectedRevision: &revision} }

func TestLibraryReferenceLifecycleCASReplayAndFrozenEdit(t *testing.T) {
	app, server := testApp(t, Config{PollInterval: time.Hour})
	reference := uploadLibraryReference(t, server, "library-upload-key")
	id := libraryID("reference", reference.ID, "")
	initial := libraryEntryHTTP(t, server, id)
	if initial.Source != "reference" || initial.Revision != 1 || initial.ThumbnailURL == nil || initial.ByteSize < 1 {
		t.Fatal("reference registration incomplete", initial)
	}
	in := taskInput("image")
	in.Operation = "edit"
	in.Inputs.ReferenceAssetIDs = []string{reference.ID}
	frozen, _, err := app.createTask(in, "before-library-trash")
	if err != nil {
		t.Fatal(err)
	}
	first := metadataInput(1, "  新名称  ", []string{" Hero ", "hero", "中文"})
	code, data, _ := request(t, server, "PATCH", "/api/v1/library/"+id, "library-metadata-key", first)
	updated := decode[LibraryEntry](t, data)
	if code != 200 || updated.Revision != 2 || updated.Name != "新名称" || len(updated.Tags) != 2 || updated.Tags[0] != "hero" {
		t.Fatal(code, string(data))
	}
	code, _, _ = request(t, server, "PATCH", "/api/v1/library/"+id, "stale-library-key", first)
	if code != 409 {
		t.Fatal("stale CAS accepted")
	}
	code, data, _ = request(t, server, "POST", "/api/v1/library/"+id+"/trash", "trash-library-key", stateInput(2))
	trashed := decode[LibraryEntry](t, data)
	if code != 200 || trashed.State != "trashed" || trashed.Revision != 3 || trashed.ContentURL != nil || trashed.ThumbnailURL != nil || trashed.DeletedAt == nil {
		t.Fatal("bad trash state", code, string(data))
	}
	for _, endpoint := range []string{reference.URL, reference.ThumbnailURL, *initial.ContentURL, *initial.ThumbnailURL} {
		code, _, _ = request(t, server, "GET", endpoint, "", nil)
		if code != 404 {
			t.Fatal("trash bypass", endpoint, code)
		}
	}
	code, data, _ = request(t, server, "GET", "/api/v1/references", "", nil)
	if code != 200 || bytes.Contains(data, []byte(reference.ID)) {
		t.Fatal("trashed reference remains selectable")
	}
	if _, _, err = app.createTask(in, "after-library-trash"); err == nil {
		t.Fatal("new task referenced trashed source")
	}
	if replay, replayed, err := app.createTask(in, "before-library-trash"); err != nil || !replayed || replay.ID != frozen.ID {
		t.Fatal("trash altered existing frozen operation", err)
	}
	code, data, headers := request(t, server, "PATCH", "/api/v1/library/"+id, "library-metadata-key", first)
	if code != 200 || headers.Get("Idempotency-Replayed") != "true" || decode[LibraryEntry](t, data).Revision != 2 {
		t.Fatal("replay did not preserve original response")
	}
	if current := libraryEntryHTTP(t, server, id); current.Revision != 3 {
		t.Fatal("replay rolled back metadata")
	}
	code, data, _ = request(t, server, "POST", "/api/v1/library/"+id+"/restore", "restore-library-key", stateInput(3))
	if code != 200 || decode[LibraryEntry](t, data).State != "active" {
		t.Fatal("restore failed", code, string(data))
	}
	code, _, _ = request(t, server, "GET", reference.ThumbnailURL, "", nil)
	if code != 200 {
		t.Fatal("restore did not reopen content")
	}
	code, data, _ = request(t, server, "GET", "/api/v1/library/"+id+"/revisions?limit=2", "", nil)
	var history struct {
		Items   []LibraryRevision `json:"items"`
		HasMore bool              `json:"has_more"`
		Next    *uint64           `json:"next_before_revision"`
	}
	_ = json.Unmarshal(data, &history)
	if code != 200 || !history.HasMore || len(history.Items) != 2 || history.Items[0].Revision != 4 || history.Next == nil || *history.Next != 3 || bytes.Contains(data, []byte("content_url")) {
		t.Fatal("history pagination incorrect", string(data))
	}
	code, data, _ = request(t, server, "GET", "/api/v1/library/"+id+"/revisions?before_revision=3", "", nil)
	_ = json.Unmarshal(data, &history)
	if code != 200 || len(history.Items) != 2 || history.Items[1].Name != "original.png" {
		t.Fatal("initial metadata revision lost")
	}
}

func TestLibrarySearchCursorAndDuplicateDemoArtifactIdentities(t *testing.T) {
	app, server := testApp(t, Config{PollInterval: time.Hour})
	now := time.Now().UTC().Truncate(time.Microsecond)
	app.mu.Lock()
	for i := range 5 {
		task := Task{ID: fmt.Sprintf("task_fixture_%d", i), WorkspaceID: "ws_demo", ProjectID: "prj_demo", ProviderMode: "demo", CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{{ID: "same-demo-id", Kind: "image", URL: "/demo-assets/image-1.svg", Title: fmt.Sprintf("100%%图像_%d", i)}}}
		app.registerMemoryTaskLibraryLocked(task)
	}
	app.mu.Unlock()
	code, data, _ := request(t, server, "GET", "/api/v1/library?limit=2&q="+url.QueryEscape("100%图像_"), "", nil)
	list := decode[libraryList](t, data)
	if code != 200 || len(list.Items) != 2 || !list.HasMore || list.NextCursor == nil {
		t.Fatal("literal search or page incorrect", code, string(data))
	}
	seen := map[string]bool{}
	for _, entry := range list.Items {
		seen[entry.ID] = true
		if entry.ProviderMode != "demo" || entry.ThumbnailURL == nil {
			t.Fatal("demo provenance missing")
		}
	}
	cursor := *list.NextCursor
	for list.NextCursor != nil {
		code, data, _ = request(t, server, "GET", "/api/v1/library?limit=2&q="+url.QueryEscape("100%图像_")+"&cursor="+url.QueryEscape(*list.NextCursor), "", nil)
		list = decode[libraryList](t, data)
		if code != 200 {
			t.Fatal(code, string(data))
		}
		for _, entry := range list.Items {
			if seen[entry.ID] {
				t.Fatal("keyset duplicated same-time row")
			}
			seen[entry.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatal("task+artifact identities collapsed", len(seen))
	}
	code, _, _ = request(t, server, "GET", "/api/v1/library?state=trashed&cursor="+url.QueryEscape(cursor), "", nil)
	if code != 400 {
		t.Fatal("cursor accepted different filter")
	}
	for _, query := range []string{"cursor=" + strings.Repeat("a", 2049), "limit=101", "q=" + strings.Repeat("a", 129), "state=all", "tag=%00"} {
		code, _, _ = request(t, server, "GET", "/api/v1/library?"+query, "", nil)
		if code != 400 {
			t.Fatal("invalid filter accepted", query)
		}
	}
}

func TestLibraryMetadataValidationAndMemoryBounds(t *testing.T) {
	app, server := testApp(t, Config{})
	reference := uploadLibraryReference(t, server, "library-validation-upload")
	id := libraryID("reference", reference.ID, "")
	for i, input := range []libraryMutation{metadataInput(1, " ", []string{}), metadataInput(1, strings.Repeat("界", 121), []string{}), metadataInput(1, "ok", []string{"bad\nvalue"}), metadataInput(1, "ok", make([]string, 11)), stateInput(1)} {
		code, _, _ := request(t, server, "PATCH", "/api/v1/library/"+id, fmt.Sprintf("invalid-metadata-%d", i), input)
		if code != 422 {
			t.Fatal("invalid metadata accepted", i, code)
		}
	}
	app.mu.Lock()
	app.libraryRevisionCount = memoryLibraryRevisionLimit
	app.mu.Unlock()
	code, _, _ := request(t, server, "PATCH", "/api/v1/library/"+id, "library-full-key", metadataInput(1, "ok", []string{}))
	if code != 503 {
		t.Fatal("memory revisions are unbounded")
	}
	if entry := libraryEntryHTTP(t, server, id); entry.Revision != 1 {
		t.Fatal("capacity failure mutated resource")
	}
}

func TestLibraryRevisionLimitReservesRestore(t *testing.T) {
	current := libraryRecord{LibraryEntry: LibraryEntry{Revision: 998, State: "active", Name: "example", Tags: []string{}}}
	trashed, err := validateLibraryMutation(current, stateInput(998), "trash")
	if err != nil || trashed.Revision != 999 {
		t.Fatal("last reversible trash failed", err)
	}
	if _, err = validateLibraryMutation(trashed, metadataInput(999, "changed", []string{}), "update"); err == nil {
		t.Fatal("trashed metadata consumed restore slot")
	}
	restored, err := validateLibraryMutation(trashed, stateInput(999), "restore")
	if err != nil || restored.Revision != 1000 || restored.State != "active" {
		t.Fatal("reserved final restore failed", err)
	}
	current.Revision = 999
	if _, err = validateLibraryMutation(current, stateInput(999), "trash"); err == nil {
		t.Fatal("trash accepted without enough history for restore")
	}
}

func TestLibraryMemoryGlobalLimitPreservesTrashRestoreSlot(t *testing.T) {
	app, server := testApp(t, Config{})
	ref := uploadLibraryReference(t, server, "memory-restore-reserve")
	id := libraryID("reference", ref.ID, "")
	app.mu.Lock()
	app.libraryRevisionCount = memoryLibraryRevisionLimit - 2
	app.mu.Unlock()
	entry, _, err := app.mutateLibrary("ws_demo", "prj_demo", id, "last-memory-trash", "trash", "", stateInput(1))
	if err != nil || entry.State != "trashed" {
		t.Fatal("trash with one reserved restore rejected", err)
	}
	app.mu.Lock()
	capacityErr := app.checkMemoryLibraryCapacityLocked(1)
	app.mu.Unlock()
	if capacityErr == nil {
		t.Fatal("new source consumed reserved restore capacity")
	}
	entry, _, err = app.mutateLibrary("ws_demo", "prj_demo", id, "last-memory-restore", "restore", "", stateInput(2))
	if err != nil || entry.State != "active" {
		t.Fatal("full memory catalog blocked reserved restore", err)
	}
}

func seedPrivateLibraryArtifact(t *testing.T, app *App, taskID, artifactID, projectID string) {
	t.Helper()
	data := referencePNG(t, 66)
	hash := sha256.Sum256(data)
	info := AssetInfo{ID: artifactID, ContentType: "image/png", ByteSize: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), CreatedAt: time.Now().UTC()}
	body, _ := json.Marshal(info)
	if err := os.WriteFile(filepath.Join(app.config.AssetDir, artifactID+".bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.config.AssetDir, artifactID+".json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := Task{ID: taskID, WorkspaceID: "ws_demo", ProjectID: projectID, ProviderMode: "live", Kind: "image", Status: "SUCCEEDED", CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{{ID: artifactID, URL: "/api/v1/assets/" + artifactID, Kind: "image", Title: "private", StorageInfo: &info}}}
	app.mu.Lock()
	defer app.mu.Unlock()
	app.registerMemoryTaskLibraryLocked(task)
}
func TestLibraryLegacyDownloadCannotBypassTrashAndScope(t *testing.T) {
	app, server := testApp(t, Config{})
	artifactID := "asset_" + strings.Repeat("c", 64)
	seedPrivateLibraryArtifact(t, app, "task_library_original", artifactID, "prj_demo")
	id := libraryID("generated", "task_library_original", artifactID)
	code, _, _ := request(t, server, "GET", "/api/v1/assets/"+artifactID, "", nil)
	if code != 200 {
		t.Fatal("registered private artifact denied", code)
	}
	code, _, _ = request(t, server, "POST", "/api/v1/library/"+id+"/trash", "private-trash-key", stateInput(1))
	if code != 200 {
		t.Fatal(code)
	}
	code, _, _ = request(t, server, "GET", "/api/v1/assets/"+artifactID, "", nil)
	if code != 404 {
		t.Fatal("legacy ID bypassed trash")
	}
	seedPrivateLibraryArtifact(t, app, "task_library_second", artifactID, "prj_demo")
	code, _, _ = request(t, server, "GET", "/api/v1/assets/"+artifactID, "", nil)
	if code != 200 {
		t.Fatal("independent active source did not authorize old ID")
	}
	code, _, _ = request(t, server, "GET", "/api/v1/library/"+id+"/content", "", nil)
	if code != 404 {
		t.Fatal("independent source reopened trashed entry URL")
	}
}

func TestPostgresLibraryLifecycleIsolationAndAtomicMutations(t *testing.T) {
	db := testDatabase(t)
	cfg := Config{DatabaseURL: db, AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), PollInterval: time.Hour}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	defer func() { server.Close(); app.Close() }()
	alice, bob := authClient(t), authClient(t)
	a := registerFixture(t, alice, server.URL, "library-alice@example.test")
	b := registerFixture(t, bob, server.URL, "library-bob@example.test")
	code, body, _ := uploadReferenceFixture(t, alice, server.URL, a.Project.ID, "persistent-library-upload", "secret-name.png", referencePNG(t, 90), false)
	if code != 201 {
		t.Fatal(code, string(body))
	}
	ref := decode[ReferenceAsset](t, body)
	id := libraryID("reference", ref.ID, "")
	code, body, _ = authRequest(t, alice, server.URL, "PATCH", "/api/v1/library/"+id, "persistent-library-edit", a.Project.ID, metadataInput(1, "私密名称", []string{"secret-tag"}))
	if code != 200 {
		t.Fatal(code, string(body))
	}
	for _, endpoint := range []string{"/api/v1/library/" + id, "/api/v1/library/" + id + "/revisions", "/api/v1/library/" + id + "/content"} {
		code, _, _ = authRequest(t, bob, server.URL, "GET", endpoint, "", b.Project.ID, nil)
		if code != 404 {
			t.Fatal("cross-account library leak", endpoint, code)
		}
	}
	code, _, _ = authRequest(t, bob, server.URL, "POST", "/api/v1/library/"+id+"/trash", "foreign-library-key", b.Project.ID, stateInput(2))
	if code != 404 {
		t.Fatal("foreign mutation accepted")
	}
	code, body, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/audit", "", a.Project.ID, nil)
	if code != 200 || bytes.Contains(body, []byte("私密名称")) || bytes.Contains(body, []byte("secret-tag")) || bytes.Contains(body, []byte("secret-name")) {
		t.Fatal("metadata leaked to audit")
	}
	server.Close()
	app.Close()
	app, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	code, body, headers := authRequest(t, alice, server.URL, "PATCH", "/api/v1/library/"+id, "persistent-library-edit", a.Project.ID, metadataInput(1, "私密名称", []string{"secret-tag"}))
	if code != 200 || headers.Get("Idempotency-Replayed") != "true" || decode[LibraryEntry](t, body).Revision != 2 {
		t.Fatal("metadata replay lost on restart", code, string(body))
	}
	admin, err := pgx.Connect(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	_, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_library_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='asset.trashed' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_library_audit BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_library_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/library/"+id+"/trash", "rollback-library-trash", a.Project.ID, stateInput(2))
	if code != 503 {
		t.Fatal("audit error not failclosed", code)
	}
	var revision, revisions, idem int
	var state string
	if err = admin.QueryRow(context.Background(), "SELECT revision,state FROM studio_library_entries WHERE id=$1", id).Scan(&revision, &state); err != nil {
		t.Fatal(err)
	}
	_ = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_library_revisions WHERE asset_id=$1", id).Scan(&revisions)
	_ = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_library_idempotency WHERE idempotency_key='rollback-library-trash'").Scan(&idem)
	if revision != 2 || revisions != 2 || state != "active" || idem != 0 {
		t.Fatal("audit failure left partial metadata", revision, revisions, state, idem)
	}
}

func TestPostgresLibraryConcurrentCASAndImmutableHistory(t *testing.T) {
	db := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: db})
	ref := uploadLibraryReference(t, server, "cas-library-upload")
	id := libraryID("reference", ref.ID, "")
	type result struct {
		entry  LibraryEntry
		replay bool
		err    error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for i := range 2 {
		go func() {
			conn, err := pgx.Connect(context.Background(), db)
			if err != nil {
				results <- result{err: err}
				ready.Done()
				return
			}
			defer conn.Close(context.Background())
			tx, err := conn.Begin(context.Background())
			if err != nil {
				results <- result{err: err}
				ready.Done()
				return
			}
			defer tx.Rollback(context.Background())
			input := metadataInput(1, fmt.Sprintf("writer-%d", i), []string{})
			key := fmt.Sprintf("library-cas-key-%d", i)
			hash := payloadHash(key)
			ready.Done()
			<-start
			entry, replay, err := mutateLibrarySQL(context.Background(), tx, "ws_demo", "prj_demo", id, key, "update", "", input, hash)
			if err == nil {
				err = tx.Commit(context.Background())
			}
			results <- result{entry, replay, err}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	success := 0
	conflict := 0
	for _, r := range []result{first, second} {
		if r.err == nil {
			success++
		} else if e, ok := r.err.(*operationError); ok && e.code == "ASSET_REVISION_CONFLICT" {
			conflict++
		} else {
			t.Fatal(r.err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("CAS lost update", first, second)
	}
	// Two independent transactions with the same original key converge on one
	// immutable response, even though the current revision has already advanced.
	start = make(chan struct{})
	ready.Add(2)
	for range 2 {
		go func() {
			conn, err := pgx.Connect(context.Background(), db)
			if err != nil {
				ready.Done()
				results <- result{err: err}
				return
			}
			defer conn.Close(context.Background())
			tx, err := conn.Begin(context.Background())
			if err != nil {
				ready.Done()
				results <- result{err: err}
				return
			}
			defer tx.Rollback(context.Background())
			input := metadataInput(2, "same-key-name", []string{})
			ready.Done()
			<-start
			entry, replay, err := mutateLibrarySQL(context.Background(), tx, "ws_demo", "prj_demo", id, "shared-library-key", "update", "", input, payloadHash("identical-request"))
			if err == nil {
				err = tx.Commit(context.Background())
			}
			results <- result{entry, replay, err}
		}()
	}
	ready.Wait()
	close(start)
	first, second = <-results, <-results
	if first.err != nil || second.err != nil || first.replay == second.replay || first.entry.Revision != 3 || second.entry.Revision != 3 {
		t.Fatal("same-key concurrent transactions diverged", first, second)
	}
	conn, err := pgx.Connect(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	for _, statement := range []string{"UPDATE studio_library_revisions SET name='overwritten' WHERE asset_id=$1", "DELETE FROM studio_library_revisions WHERE asset_id=$1", "UPDATE studio_library_entries SET original_url='/changed' WHERE id=$1"} {
		if _, err = conn.Exec(context.Background(), statement, id); err == nil {
			t.Fatal("immutable history/source changed")
		}
	}
	_ = app
}

func TestPostgresLibraryRegistrationFailureRollsBackSourceTransaction(t *testing.T) {
	for _, source := range []string{"reference", "generated"} {
		t.Run(source, func(t *testing.T) {
			db := testDatabase(t)
			provider := &recoveryMedia{}
			provider.complete.Store(true)
			app, server := testApp(t, Config{DatabaseURL: db, PollInterval: time.Hour, mediaProvider: provider})
			ctx := context.Background()
			admin, err := pgx.Connect(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(ctx)
			_, err = admin.Exec(ctx, `CREATE FUNCTION reject_library_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejects catalog'; END $$; CREATE TRIGGER reject_library_insert BEFORE INSERT ON studio_library_entries FOR EACH ROW EXECUTE FUNCTION reject_library_insert()`)
			if err != nil {
				t.Fatal(err)
			}
			if source == "reference" {
				code, _, _ := uploadReferenceFixture(t, server.Client(), server.URL, "prj_demo", "library-registration-reject", "ref.png", referencePNG(t, 4), false)
				if code != 503 {
					t.Fatal("source accepted without catalog", code)
				}
				var refs int
				if err = admin.QueryRow(ctx, "SELECT count(*) FROM studio_references").Scan(&refs); err != nil || refs != 0 {
					t.Fatal("source metadata not rolled back", refs, err)
				}
			} else {
				task, _, err := app.createTask(taskInput("image"), "catalog-result-reject")
				if err != nil {
					t.Fatal(err)
				}
				app.executeTaskStep(task.ID)
				app.executeTaskStep(task.ID)
				var document []byte
				if err = admin.QueryRow(ctx, "SELECT document FROM studio_tasks WHERE id=$1", task.ID).Scan(&document); err != nil {
					t.Fatal(err)
				}
				persisted := decode[Task](t, document)
				if persisted.Status == "SUCCEEDED" || len(persisted.Artifacts) > 0 {
					t.Fatal("result committed without atomic catalog")
				}
			}
			var entries int
			_ = admin.QueryRow(ctx, "SELECT count(*) FROM studio_library_entries").Scan(&entries)
			if entries != 0 || !app.store.failed.Load() {
				t.Fatal("catalog failure not atomic/failclosed", entries)
			}
		})
	}
}

func TestPostgresLibrarySevenMigrationBackfillAndOldWriterFence(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	old := fstest.MapFS{}
	names, _ := fs.Glob(migrationFiles, "migrations/*.sql")
	for _, name := range names {
		if name >= "migrations/007_" {
			continue
		}
		blob, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: blob}
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','old'); INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','old'); INSERT INTO studio_references(id,workspace_id,project_id,name,blob,stored_bytes,created_at) VALUES('ref_old','ws_demo','prj_demo','old.png','{"id":"blob_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","byte_size":12,"content_type":"image/png"}',12,now()); INSERT INTO studio_tasks(id,workspace_id,project_id,input,handle,next_poll,document) SELECT 'old_task_'||i,'ws_demo','prj_demo','{}'::jsonb,'{}'::jsonb,now(),jsonb_build_object('id','old_task_'||i,'workspace_id','ws_demo','project_id','prj_demo','status','SUCCEEDED','provider_mode','demo','artifacts',jsonb_build_array(jsonb_build_object('id','shared-artifact','kind','image','title','old title','url','/demo-assets/image-1.svg'))) FROM generate_series(1,2)i;`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigrations(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	app, err := newIsolatedApp(t, Config{DatabaseURL: db})
	if err != nil {
		t.Fatal(err)
	}
	app.Close()
	var entries, revisions int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM studio_library_entries").Scan(&entries); err != nil {
		t.Fatal(err)
	}
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM studio_library_revisions").Scan(&revisions)
	if entries != 3 || revisions != 3 {
		t.Fatal("backfill collapsed sources", entries, revisions)
	}
	var id string
	if err = conn.QueryRow(ctx, "SELECT id FROM studio_library_entries WHERE reference_id='ref_old'").Scan(&id); err != nil || id != libraryID("reference", "ref_old", "") {
		t.Fatal("Go/SQL identity mismatch", id, err)
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("old writer accepted library schema", err)
	}
}
