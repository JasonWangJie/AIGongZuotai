package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestReferenceAndResultStoresRequireSeparateTrees(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct{ name, refs, assets string }{
		{"same", directory, directory},
		{"references-inside-results", filepath.Join(directory, "references"), directory},
		{"results-inside-references", directory, filepath.Join(directory, "assets")},
		{"default-results", ".runtime/assets/references", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, err := New(Config{ReferenceDir: test.refs, AssetDir: test.assets})
			if app != nil {
				app.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "REFERENCE_DIR and ASSET_DIR") {
				t.Fatal("overlapping private stores accepted", err)
			}
		})
	}
	app, err := New(Config{ReferenceDir: filepath.Join(directory, "references"), AssetDir: filepath.Join(directory, "assets")})
	if err != nil {
		t.Fatal("separate sibling stores rejected", err)
	}
	app.Close()
}

func referencePNG(t *testing.T, shade byte) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 12, 9))
	for y := range 9 {
		for x := range 12 {
			img.Set(x, y, color.NRGBA{R: shade, G: byte(x * 10), B: byte(y * 15), A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func uploadReferenceFixture(t *testing.T, client *http.Client, base, project, key, name string, blob []byte, extra bool) (int, []byte, http.Header) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(blob)
	if extra {
		_ = writer.WriteField("unexpected", "field")
	}
	_ = writer.Close()
	req, err := http.NewRequest("POST", base+"/api/v1/references", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Idempotency-Key", key)
	if project != "" {
		req.Header.Set("X-Project-ID", project)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data, response.Header
}

func TestReferenceUploadBoundaryIndependentIdempotencyAndPrivatePreview(t *testing.T) {
	_, s := testApp(t, Config{PollInterval: time.Hour})
	blob := referencePNG(t, 50)
	code, body, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "same-image-key", "photo.png", blob, false)
	if code != 201 {
		t.Fatalf("upload %d %s", code, body)
	}
	asset := decode[ReferenceAsset](t, body)
	if asset.ByteSize != int64(len(blob)) || asset.Width != 12 || asset.Height != 9 || asset.ContentType != "image/png" || !strings.Contains(asset.ThumbnailURL, "project_id=prj_demo") || strings.Contains(string(body), "blob_") || strings.Contains(string(body), "sha256") {
		t.Fatal("unsafe or incomplete reference response", string(body))
	}
	code, body, headers := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "same-image-key", "photo.png", blob, false)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || decode[ReferenceAsset](t, body).ID != asset.ID {
		t.Fatal("random multipart boundary broke idempotency")
	}
	code, _, _ = uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "same-image-key", "changed.png", blob, false)
	if code != 409 {
		t.Fatal("different metadata did not conflict")
	}
	code, _, _ = uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "same-image-key", "photo.png", referencePNG(t, 90), false)
	if code != 409 {
		t.Fatal("different content did not conflict")
	}
	for _, endpoint := range []string{asset.URL, asset.ThumbnailURL} {
		response, err := s.Client().Get(s.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("private reference inaccessible")
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Fatal("preview not a valid image", err)
		}
	}
	request, _ := http.NewRequest("GET", s.URL+asset.URL, nil)
	request.Header.Set("Range", "bytes=0-7")
	response, err := s.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 206 {
		t.Fatal("reference Range not preserved")
	}
	code, body, _ = requestAPI(t, s, "GET", "/api/v1/references?limit=1", nil)
	if code != 200 || !bytes.Contains(body, []byte(asset.ID)) {
		t.Fatal("project list missing accepted upload")
	}
}

// A separate helper name avoids shadowing the shared request fixture by local requests.
func requestAPI(t *testing.T, s *httptest.Server, method, path string, body any) (int, []byte, http.Header) {
	return request(t, s, method, path, "", body)
}

func TestReferenceUploadValidationQuotasAndReplayAtCapacity(t *testing.T) {
	a, s := testApp(t, Config{})
	blob := referencePNG(t, 33)
	for name, data := range map[string][]byte{"fake.png": []byte("<svg onload='bad()'></svg>"), "broken.png": blob[:15]} {
		code, _, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "invalid-"+name, name, data, false)
		if code != 422 {
			t.Fatal("invalid image accepted", name, code)
		}
	}
	code, _, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "extra-form-field", "photo.png", blob, true)
	if code != 400 {
		t.Fatal("extra multipart field accepted")
	}
	code, _, _ = uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "oversized-upload", "photo.png", bytes.Repeat([]byte{1}, int(MaxReferenceBytes)+1), false)
	if code != 413 {
		t.Fatal("oversized file accepted", code)
	}
	code, body, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "capacity-first-key", "photo.png", blob, false)
	if code != 201 {
		t.Fatal(code, string(body))
	}
	first := decode[ReferenceAsset](t, body)
	a.mu.Lock()
	original := a.referenceItems[first.ID]
	a.mu.Unlock()
	for i := 1; i < maxWorkspaceReferenceFiles; i++ {
		if _, _, err := a.acceptReference("ws_demo", "prj_demo", fmt.Sprintf("copy-%d", i), fmt.Sprintf("ws_demo:references:capacity-%d", i), original.Blob, ""); err != nil {
			t.Fatal(err)
		}
	}
	code, _, headers := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "capacity-first-key", "photo.png", blob, false)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("full quota blocked upload replay")
	}
	code, body, _ = uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "capacity-new-key", "photo.png", []byte("not decoded when quota is full"), false)
	if code != 429 || !bytes.Contains(body, []byte("REFERENCE_WORKSPACE_LIMIT")) {
		t.Fatal("quota not checked before new blob", code, string(body))
	}
	if len(a.referenceItems) != 100 || len(a.referenceIdem) != 100 {
		t.Fatal("rejection or replay consumed another reference")
	}
}

func TestReferenceProjectIsolationAndEditingContract(t *testing.T) {
	a, s := testApp(t, Config{PollInterval: time.Hour})
	code, body, _ := request(t, s, "POST", "/api/v1/projects", "", map[string]string{"name": "Second"})
	if code != 201 {
		t.Fatal(code)
	}
	second := decode[Project](t, body)
	_, body, _ = uploadReferenceFixture(t, s.Client(), s.URL, second.ID, "project-reference-key", "ref.png", referencePNG(t, 2), false)
	asset := decode[ReferenceAsset](t, body)
	response, err := s.Client().Get(s.URL + asset.ThumbnailURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("thumbnail lacks owning project context")
	}
	code, _, _ = request(t, s, "GET", "/api/v1/references/"+asset.ID+"/content?project_id=prj_demo", "", nil)
	if code != 404 {
		t.Fatal("wrong project accessed reference", code)
	}
	in := taskInput("image")
	in.Operation = "edit"
	in.Inputs.ReferenceAssetIDs = []string{asset.ID}
	code, _, _ = request(t, s, "POST", "/api/v1/tasks", "cross-project-edit", in)
	if code != 404 {
		t.Fatal("cross-project edit accepted", code)
	}
	in.ProjectID = second.ID
	code, body, _ = request(t, s, "POST", "/api/v1/tasks", "project-edit-key", in)
	if code != 202 {
		t.Fatal(code, string(body))
	}
	task := decode[Task](t, body)
	if task.Operation != "edit" || len(task.ReferenceAssetIDs) != 1 || task.ReferenceAssetIDs[0] != asset.ID {
		t.Fatal("reference snapshot missing")
	}
	in.Operation = "generate"
	code, _, _ = request(t, s, "POST", "/api/v1/tasks", "invalid-generation-refs", in)
	if code != 422 {
		t.Fatal("generation accepted references")
	}
	in.Operation = "edit"
	in.Inputs.ReferenceAssetIDs = append(in.Inputs.ReferenceAssetIDs, asset.ID)
	code, _, _ = request(t, s, "POST", "/api/v1/tasks", "duplicate-edit-refs", in)
	if code != 422 {
		t.Fatal("duplicate reference accepted")
	}
	a.mu.Lock()
	record := a.tasks[task.ID]
	snapshot := copyTask(record.Task)
	snapshot.ReferenceAssetIDs[0] = "mutated"
	storedReference := record.Task.ReferenceAssetIDs[0]
	a.mu.Unlock()
	if storedReference != asset.ID {
		t.Fatal("reference snapshot alias")
	}
}

type blockingReferencePrepare struct {
	demoMedia
	started chan struct{}
	release chan struct{}
}

func (p *blockingReferencePrepare) PrepareEditModel(ctx context.Context, in TaskInput, model string, refs []MediaEditSource) (MediaSubmission, error) {
	p.started <- struct{}{}
	select {
	case <-p.release:
		return MediaSubmission{Body: []byte(`{"fixture":"frozen"}`), Kind: "image", Count: 1}, nil
	case <-ctx.Done():
		return MediaSubmission{}, ctx.Err()
	}
}
func TestReferencePreparationUnlocksStateAndRechecksQuotaAndIdempotency(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			p := &blockingReferencePrepare{started: make(chan struct{}, 2), release: make(chan struct{})}
			a, s := testApp(t, Config{mediaProvider: p, PollInterval: time.Hour, Admission: AdmissionLimits{WorkspaceTasks: 1, ModelTasks: 1}})
			_, body, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "prepare-reference-key", "ref.png", referencePNG(t, 7), false)
			ref := decode[ReferenceAsset](t, body)
			in := taskInput("image")
			in.Operation = "edit"
			in.Inputs.ReferenceAssetIDs = []string{ref.ID}
			type result struct {
				task   Task
				replay bool
				err    error
			}
			results := make(chan result, 2)
			go func() {
				task, replay, err := a.createTask(in, "parallel-prepare-key")
				results <- result{task, replay, err}
			}()
			<-p.started
			checked := make(chan error, 1)
			go func() { _, err := a.admissionSnapshot("ws_demo"); checked <- err }()
			select {
			case err := <-checked:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("disk preparation holds global state lock")
			}
			if sameKey {
				go func() {
					task, replay, err := a.createTask(in, "parallel-prepare-key")
					results <- result{task, replay, err}
				}()
				<-p.started
			} else {
				if _, _, err := a.createTask(taskInput("image"), "wins-while-preparing"); err != nil {
					t.Fatal(err)
				}
			}
			close(p.release)
			first := <-results
			if sameKey {
				second := <-results
				if first.err != nil || second.err != nil || first.task.ID != second.task.ID || first.replay == second.replay {
					t.Fatal("parallel same key not convergent", first, second)
				}
			} else {
				assertAdmissionError(t, first.err, "ADMISSION_WORKSPACE_LIMIT")
			}
		})
	}
}

func TestReferenceReadDeadlineMustBeSupported(t *testing.T) {
	a, _ := testApp(t, Config{})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "ref.png")
	_, _ = part.Write(referencePNG(t, 3))
	_ = writer.Close()
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/references", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Idempotency-Key", "deadline-fixture-key")
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	if out.Code != 503 || !strings.Contains(out.Body.String(), "REFERENCE_READ_DEADLINE_UNAVAILABLE") {
		t.Fatal("unsupported reader deadline silently accepted")
	}
}

func TestPostgresReferenceOwnershipPersistenceAndTaskBindings(t *testing.T) {
	db := testDatabase(t)
	directory := t.TempDir()
	cfg := Config{DatabaseURL: db, AuthMode: "required", ReferenceDir: directory, PollInterval: time.Hour}
	a, err := newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(a.Handler())
	defer func() { s.Close(); a.Close() }()
	alice, bob := authClient(t), authClient(t)
	u1 := registerFixture(t, alice, s.URL, "reference-one@example.test")
	u2 := registerFixture(t, bob, s.URL, "reference-two@example.test")
	code, body, _ := uploadReferenceFixture(t, alice, s.URL, u1.Project.ID, "persistent-upload-key", "private.png", referencePNG(t, 9), false)
	if code != 201 {
		t.Fatal(code, string(body))
	}
	ref := decode[ReferenceAsset](t, body)
	code, _, _ = authRequest(t, bob, s.URL, "GET", ref.URL, "", "", nil)
	if code != 404 {
		t.Fatal("foreign account read private reference", code)
	}
	in := taskInput("image")
	in.ProjectID = u2.Project.ID
	in.Operation = "edit"
	in.Inputs.ReferenceAssetIDs = []string{ref.ID}
	code, _, _ = authRequest(t, bob, s.URL, "POST", "/api/v1/tasks", "foreign-edit-key", u2.Project.ID, in)
	if code != 404 {
		t.Fatal("foreign edit reference accepted", code)
	}
	in.ProjectID = u1.Project.ID
	code, body, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/tasks", "persistent-edit-key", u1.Project.ID, in)
	if code != 202 {
		t.Fatal(code, string(body))
	}
	task := decode[Task](t, body)
	var bindings, uploads, audits int
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM studio_task_references WHERE task_id=$1 AND reference_id=$2", task.ID, ref.ID).Scan(&bindings); err != nil {
			return err
		}
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM studio_references WHERE id=$1", ref.ID).Scan(&uploads); err != nil {
			return err
		}
		return conn.QueryRow(ctx, "SELECT count(*) FROM studio_audit_events WHERE action='reference.uploaded' AND resource_id=$1", ref.ID).Scan(&audits)
	})
	if err != nil || bindings != 1 || uploads != 1 || audits != 1 {
		t.Fatal("atomic metadata missing", bindings, uploads, audits, err)
	}
	s.Close()
	a.Close()
	a, err = newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s = httptest.NewServer(a.Handler())
	code, body, headers := uploadReferenceFixture(t, alice, s.URL, u1.Project.ID, "persistent-upload-key", "private.png", referencePNG(t, 9), false)
	if code != 201 || headers.Get("Idempotency-Replayed") != "true" || decode[ReferenceAsset](t, body).ID != ref.ID {
		t.Fatal("upload replay lost after restart", code, string(body))
	}
	code, body, _ = authRequest(t, alice, s.URL, "POST", "/api/v1/tasks", "persistent-edit-key", u1.Project.ID, in)
	if code != 202 || decode[Task](t, body).ID != task.ID {
		t.Fatal("edit replay lost after restart", code, string(body))
	}
	code, _, _ = authRequest(t, alice, s.URL, "GET", ref.ThumbnailURL, "", "", nil)
	if code != 200 {
		t.Fatal("private thumbnail lost after restart", code)
	}
}

func TestPostgresReferenceAuditFailureRollsBackMetadataAndTaskBindings(t *testing.T) {
	for _, action := range []string{"reference.uploaded", "task.accepted"} {
		t.Run(action, func(t *testing.T) {
			db := testDatabase(t)
			a, s := testApp(t, Config{DatabaseURL: db, ReferenceDir: t.TempDir(), PollInterval: time.Hour})
			admin, err := pgx.Connect(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close(context.Background())
			var ref ReferenceAsset
			if action == "task.accepted" {
				code, body, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "before-trigger-key", "ref.png", referencePNG(t, 4), false)
				if code != 201 {
					t.Fatal(code, string(body))
				}
				ref = decode[ReferenceAsset](t, body)
			}
			_, err = admin.Exec(context.Background(), `CREATE FUNCTION reject_reference_audit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+action+`' THEN RAISE EXCEPTION 'fixture rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_reference_audit_fixture BEFORE INSERT ON studio_audit_events FOR EACH ROW EXECUTE FUNCTION reject_reference_audit_fixture()`)
			if err != nil {
				t.Fatal(err)
			}
			if action == "reference.uploaded" {
				code, _, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "failed-upload-key", "ref.png", referencePNG(t, 4), false)
				if code != 503 {
					t.Fatal("audit error did not fail closed", code)
				}
			} else {
				in := taskInput("image")
				in.Operation = "edit"
				in.Inputs.ReferenceAssetIDs = []string{ref.ID}
				code, _, _ := request(t, s, "POST", "/api/v1/tasks", "failed-reference-task", in)
				if code != 503 {
					t.Fatal("task audit error not fail closed", code)
				}
			}
			var rows, idem, bindings int
			if action == "reference.uploaded" {
				_ = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_references").Scan(&rows)
				_ = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_reference_idempotency").Scan(&idem)
			} else {
				_ = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_tasks").Scan(&rows)
				_ = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_idempotency WHERE resource_type='task'").Scan(&idem)
			}
			_ = admin.QueryRow(context.Background(), "SELECT count(*) FROM studio_task_references").Scan(&bindings)
			if rows != 0 || idem != 0 || bindings != 0 {
				t.Fatal("failed transaction left metadata/reservation/bindings", rows, idem, bindings)
			}
			if !a.store.failed.Load() {
				t.Fatal("database failure did not trip breaker")
			}
		})
	}
}

func TestPostgresReferenceCompoundForeignKeyRejectsCrossProjectBinding(t *testing.T) {
	db := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: db, ReferenceDir: t.TempDir(), PollInterval: time.Hour})
	_, body, _ := uploadReferenceFixture(t, s.Client(), s.URL, "prj_demo", "fk-reference-key", "ref.png", referencePNG(t, 1), false)
	ref := decode[ReferenceAsset](t, body)
	_, body, _ = request(t, s, "POST", "/api/v1/projects", "", map[string]string{"name": "Other"})
	other := decode[Project](t, body)
	in := taskInput("image")
	in.ProjectID = other.ID
	task, _, err := a.createTask(in, "fk-task-original")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	_, err = admin.Exec(context.Background(), "INSERT INTO studio_task_references(task_id,position,reference_id,workspace_id,project_id) VALUES($1,0,$2,'ws_demo',$3)", task.ID, ref.ID, other.ID)
	if err == nil {
		t.Fatal("database accepted cross-project reference")
	}
	var operation *operationError
	if errors.As(err, &operation) {
		t.Fatal("fixture should exercise actual database constraint")
	}
}

func TestReferenceNewFieldsPreserveV06RawRequestHash(t *testing.T) {
	in := TaskInput{ProjectID: "prj_demo", Kind: "image", ModelID: "studio-image", ModelVersion: "v1_fixture", Parameters: TaskParameters{AspectRatio: "1:1", Count: 1, Size: "1024x1024"}}
	in.Inputs.Prompt = "v06 prompt"
	blob, _ := json.Marshal(in)
	if bytes.Contains(blob, []byte("operation")) || bytes.Contains(blob, []byte("reference_asset_ids")) {
		t.Fatal("empty edit fields changed original request JSON")
	}
	want := sha256.Sum256([]byte(`{"model_version":"v1_fixture","project_id":"prj_demo","kind":"image","model_id":"studio-image","inputs":{"prompt":"v06 prompt"},"parameters":{"aspect_ratio":"1:1","count":1,"size":"1024x1024"}}`))
	if payloadHash(in) != want {
		t.Fatal("V06 request byte order or omission rules changed")
	}
}

func TestPostgresReferenceMigrationUpgradesFiveAndRejectsOldWriter(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	oldFiles := fstest.MapFS{}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/006_" {
			continue
		}
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		oldFiles[name] = &fstest.MapFile{Data: body}
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = applyMigrationsFrom(ctx, tx, oldFiles); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	app, err := newIsolatedApp(t, Config{DatabaseURL: db, ReferenceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	app.Close()
	var version int
	if err = admin.QueryRow(ctx, "SELECT count(*) FROM studio_schema_versions WHERE version=6").Scan(&version); err != nil || version != 1 {
		t.Fatal("reference migration missing", version, err)
	}
	tx, err = admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, oldFiles); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("v0.5 writer accepted edit snapshot schema", err)
	}
}

func TestPostgresReferenceEditSpoolSurvivesDisabledCatalogAndHandleRestart(t *testing.T) {
	db := testDatabase(t)
	var submits, queries atomic.Int32
	var receivedMu sync.Mutex
	var receivedBody []byte
	var receivedType, receivedKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == imageEditEndpoint:
			submits.Add(1)
			receivedMu.Lock()
			receivedBody, _ = io.ReadAll(r.Body)
			receivedType, receivedKey = r.Header.Get("Content-Type"), r.Header.Get("Idempotency-Key")
			receivedMu.Unlock()
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"task_id":"persisted-edit-handle"}`)
		case r.Method == "GET" && r.URL.Path == mediaQueryPrefix+"persisted-edit-handle":
			queries.Add(1)
			_, _ = io.WriteString(w, `{"task_id":"persisted-edit-handle","media_type":"image","stage":"generating","status":"processing","progress":30}`)
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	catalog := customCatalogFixture()
	catalog.Models = catalog.Models[1:2]
	catalog.Models[0].Capabilities.ImageEditSupported = true
	catalog.Models[0].Capabilities.MaxReferenceImages = 4
	path := writeCatalogFixture(t, catalog)
	cfg := Config{DatabaseURL: db, Mode: "live", ModelCatalogFile: path, AsyncImageBaseURL: upstream.URL, AsyncImageAPIKey: fixtureMediaKey, AsyncImageCredentialID: "persisted-edit-credential", ReferenceDir: t.TempDir(), AssetDir: t.TempDir(), PollInterval: time.Hour}
	app, err := newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	defer func() { server.Close(); app.Close() }()
	code, body, _ := uploadReferenceFixture(t, server.Client(), server.URL, "prj_demo", "spool-upload-key", "reference.png", referencePNG(t, 25), false)
	if code != 201 {
		t.Fatal(code, string(body))
	}
	ref := decode[ReferenceAsset](t, body)
	in := taskInput("image")
	in.Operation, in.ModelID = "edit", "custom-image"
	in.ModelVersion = app.catalog.Entries[in.ModelID].Model.Version
	in.Inputs.ReferenceAssetIDs = []string{ref.ID}
	in.Parameters = TaskParameters{}
	task, _, err := app.createTask(in, "spool-edit-key")
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	saved := *app.tasks[task.ID].Submission
	app.mu.Unlock()
	if saved.BodyFileID == "" || len(saved.Body) != 0 || saved.BodySize <= 0 || saved.BodySHA256 == "" || submits.Load() != 0 {
		t.Fatal("edit not frozen on disk before upstream submission")
	}
	file, err := app.references.OpenBody(saved.BodyFileID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	app.Close()
	off := false
	catalog.Models[0].Enabled = &off
	catalog.Models[0].UpstreamModel = "must-never-use-new-model"
	catalog.Models[0].Capabilities.ImageEditSupported = false
	catalog.Models[0].Capabilities.MaxReferenceImages = 0
	rewriteCatalogFixture(t, path, catalog)
	app, err = newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	replayed, replay, err := app.createTask(in, "spool-edit-key")
	if err != nil || !replay || replayed.ID != task.ID || replayed.ReferenceAssetIDs[0] != ref.ID {
		t.Fatal("disabled edit capability blocked original replay", err)
	}
	app.executeTaskStep(task.ID)
	receivedMu.Lock()
	bytesMatch := bytes.Equal(receivedBody, original) && receivedType == saved.ContentType && receivedKey == saved.IdempotencyKey
	receivedMu.Unlock()
	if submits.Load() != 1 || !bytesMatch {
		t.Fatal("restart rebuilt edit bytes, boundary or upstream key")
	}
	server.Close()
	app.Close()
	app, err = newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	app.executeTaskStep(task.ID)
	if submits.Load() != 1 || queries.Load() != 1 {
		t.Fatal("known edit handle was resubmitted after restart")
	}
	app.mu.Lock()
	status, providerError := app.tasks[task.ID].Task.Status, app.tasks[task.ID].Task.Error
	app.mu.Unlock()
	if status != "RUNNING" || providerError != nil {
		t.Fatal("known handle could not continue polling", status, providerError)
	}
}

type shutdownReferenceMedia struct {
	recoveryMedia
	entered chan struct{}
	block   atomic.Bool
	once    sync.Once
}

func (p *shutdownReferenceMedia) Query(ctx context.Context, handle MediaHandle) (MediaResult, error) {
	if !p.block.Load() {
		return p.recoveryMedia.Query(ctx, handle)
	}
	p.queries.Add(1)
	p.once.Do(func() { close(p.entered) })
	<-ctx.Done()
	return MediaResult{}, ctx.Err()
}

func TestPostgresReferenceShutdownQueryCancellationKeepsKnownHandleRecoverable(t *testing.T) {
	provider := &shutdownReferenceMedia{entered: make(chan struct{})}
	provider.block.Store(true)
	cfg := Config{DatabaseURL: testDatabase(t), ReferenceDir: t.TempDir(), mediaProvider: provider, PollInterval: 5 * time.Millisecond}
	app, err := newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { app.Close() }()
	task, _, err := app.createTask(taskInput("image"), "shutdown-known-handle-key")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture never began its blocked query")
	}
	app.Close() // Cancels an actual task worker during an upstream Query.
	provider.block.Store(false)
	provider.complete.Store(true)
	cfg.PollInterval = time.Hour
	app, err = newIsolatedApp(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	record, loadErr := app.getTaskLocked(task.ID)
	var restored Task
	if loadErr == nil {
		restored = copyTask(record.Task)
	}
	app.mu.Unlock()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if terminal(restored.Status) {
		t.Fatalf("shutdown cancellation persisted a false terminal result: %+v", restored)
	}
	app.executeTaskStep(task.ID)
	app.mu.Lock()
	status := app.tasks[task.ID].Task.Status
	app.mu.Unlock()
	if status != "SUCCEEDED" || provider.submits.Load() != 1 || provider.queries.Load() != 2 {
		t.Fatal("known handle did not resume exclusively by query", status)
	}
}
