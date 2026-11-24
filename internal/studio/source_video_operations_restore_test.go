package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

const sourceVideoReceiptPath = "/api/v1/tasks/receipt"

// Hold a real database lock after the reservation and task inserts, until a
// second login of the same applicant expires. The original login stays valid
// so it can independently check the quote and the absent receipt.
func verifySourceSessionExpiry039(t *testing.T, a *App, server *httptest.Server, owner *http.Client, email, dbURL string, in TaskInput) {
	t.Helper()
	expiring := authClient(t)
	presentationRequest(t, expiring, server.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	p, err := a.currentPrincipal(operationsRequest(t, expiring, server))
	if err != nil {
		t.Fatal(err)
	}
	db := deliveryAdmin(t, dbURL)
	ctx := t.Context()
	if _, err = db.Exec(ctx, `CREATE FUNCTION wait_source039() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(911339,1); RETURN NEW; END $$;
 CREATE TRIGGER wait_source039 BEFORE INSERT ON studio_task_video_sources FOR EACH ROW EXECUTE FUNCTION wait_source039()`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, e := db.Exec(ctx, "DROP TRIGGER wait_source039 ON studio_task_video_sources; DROP FUNCTION wait_source039()"); e != nil {
			t.Error(e)
		}
	}()
	in.QuoteID = httpBillingQuote(t, owner, server, in.ProjectID, "task", in).ID
	key := newID("expiry")
	var expires time.Time
	if err = db.QueryRow(ctx, "UPDATE studio_sessions SET expires_at=clock_timestamp()+interval '3 seconds' WHERE id=$1 RETURNING expires_at", p.SessionID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	barrier, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(911339,1)"); err != nil {
		t.Fatal(err)
	}
	response := make(chan int, 1)
	go func() {
		code, _, _ := authRequest(t, expiring, server.URL, "POST", "/api/v1/tasks", key, in.ProjectID, in)
		response <- code
	}()
	waitingDeadline, cancelWaiting := context.WithTimeout(ctx, 6*time.Second)
	defer cancelWaiting()
	for {
		var waiting bool
		err = barrier.QueryRow(waitingDeadline, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=911339 AND objid=1 AND objsubid=2 AND NOT granted)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case status := <-response:
			t.Fatalf("source request returned %d before reaching the final gate", status)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = barrier.Exec(ctx, "SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))::double precision+0.05)", expires); err != nil {
		t.Fatal(err)
	}
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-response:
		if status != 401 {
			t.Fatalf("expired source session received %d", status)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("source request did not release the transaction")
	}
	presentationRequest(t, owner, server.URL, in.ProjectID, "GET", sourceVideoReceiptPath, key, nil, 404)
	if outboxCount(t, db, "SELECT count(*) FROM studio_ledger_quotes WHERE id=$1 AND reservation_id IS NULL", in.QuoteID) != 1 {
		t.Fatal("rolled-back submission consumed the original quote")
	}
}

func sourceVideoFootage039(t *testing.T, ffmpeg, color string, pitch int) []byte {
	t.Helper()
	video := timelineFixtureFootage(t, ffmpeg, color, pitch)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	decode := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-xerror", "-i", "pipe:0", "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
	decode.Stdin = bytes.NewReader(video)
	if err := decode.Run(); err != nil {
		t.Fatal("fixture did not decode both its video and audio tracks", err)
	}
	return video
}

func TestSourceVideoEncodedFixture039(t *testing.T) {
	ffmpeg := os.Getenv("TEST_VIDEO_FFMPEG")
	if ffmpeg == "" {
		t.Skip("requires local FFmpeg")
	}
	video := sourceVideoFootage039(t, ffmpeg, "red", 440)
	if err := checkSourceMP4(bytes.NewReader(video), int64(len(video))); err != nil {
		t.Fatal("source parser rejected the local decoded original", err)
	}
}

type sourceVideoCase039 struct {
	operation, endpoint, direction string
	price                          int64
	output                         []byte
	key, taskID                    string
	input                          TaskInput
}

type sourceVideoWire039 struct {
	key, hash string
	size      int64
}

// Validate the independently specified supplier payload at the receiving socket.
// Retain only its digest, size and operation key to compare with private storage.
func watchSourceVideoWire039(t *testing.T, f *workflowIntegrationFixture, source []byte, cases []*sourceVideoCase039) func() map[string][]sourceVideoWire039 {
	t.Helper()
	router := f.gateway.Config.Handler
	var mu sync.Mutex
	wire := make(map[string][]sourceVideoWire039)
	f.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			for _, tc := range cases {
				if r.URL.Path != tc.endpoint {
					continue
				}
				raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
				var received map[string]any
				expected := map[string]any{"model": "demo-video", "prompt": tc.operation + " fixture", "aspect_ratio": "16:9", "seconds": float64(5),
					"video": "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(source)}
				if tc.direction != "" {
					expected["extension_direction"] = tc.direction
				}
				key := r.Header.Get("Idempotency-Key")
				if err != nil || json.Unmarshal(raw, &received) != nil || !reflect.DeepEqual(received, expected) || r.ContentLength != int64(len(raw)) ||
					r.Header.Get("Content-Type") != "application/json" || key == "" || r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey || r.Header.Get("Cookie") != "" {
					t.Error("supplier received different source bytes, frozen parameters, key or credentials", tc.operation)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				wire[tc.operation] = append(wire[tc.operation], sourceVideoWire039{key, moderationSHA(raw), int64(len(raw))})
				mu.Unlock()
				r.Body = io.NopCloser(bytes.NewReader(raw))
				break
			}
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(f.gateway.Close)
	return func() map[string][]sourceVideoWire039 {
		mu.Lock()
		defer mu.Unlock()
		result := make(map[string][]sourceVideoWire039, len(wire))
		for k, v := range wire {
			result[k] = slices.Clone(v)
		}
		return result
	}
}

// Run the ordinary authenticated handler to completion, then drop the socket
// before writing a status or body. The client must recover via GET alone.
func loseSourceVideoAcceptance039(t *testing.T, a *App, client *http.Client, in TaskInput, key string) {
	t.Helper()
	accepted := make(chan int, 1)
	handler := a.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, r)
		accepted <- record.Code
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error("could not simulate a lost acknowledgement", err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/tasks", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-ID", in.ProjectID)
	req.Header.Set("Idempotency-Key", key)
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	c := &http.Client{Jar: client.Jar, Transport: transport, Timeout: client.Timeout}
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("client unexpectedly received the acknowledgement")
	}
	select {
	case status := <-accepted:
		if status != 202 {
			t.Fatalf("application accepted with status %d, want 202", status)
		}
	default:
		t.Fatal("request failed before the application completed")
	}
}

func TestPostgresSourceVideoOperationsAndRestore039(t *testing.T) {
	if os.Getenv("SOURCE_VIDEO_INTEGRATION") != "1" {
		t.Skip("set SOURCE_VIDEO_INTEGRATION=1 for the PostgreSQL and FFmpeg acceptance")
	}
	for _, key := range []string{"TEST_DATABASE_URL", "TEST_POSTGRES_BIN_DIR", "TEST_VIDEO_FFMPEG"} {
		if os.Getenv(key) == "" {
			t.Fatalf("explicit acceptance requires %s", key)
		}
	}
	ffmpeg := os.Getenv("TEST_VIDEO_FFMPEG")
	source := sourceVideoFootage039(t, ffmpeg, "red", 440)
	cases := []*sourceVideoCase039{
		{operation: "video_edit", endpoint: "/v1/videos/edits_async", price: 200000, output: sourceVideoFootage039(t, ffmpeg, "blue", 880)},
		{operation: "video_extend", endpoint: "/v1/videos/extensions_async", direction: "forward", price: 350000, output: sourceVideoFootage039(t, ffmpeg, "green", 660)},
	}
	f := newWorkflowIntegrationFixture(t)
	if os.Getenv("SOURCE_VIDEO_VERSIONED_S3") == "1" {
		endpoint := os.Getenv("TEST_S3_ENDPOINT")
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil ||
			os.Getenv("TEST_S3_BUCKET") == "" || os.Getenv("TEST_S3_ACCESS_KEY_ID") == "" || os.Getenv("TEST_S3_SECRET_ACCESS_KEY") == "" {
			t.Fatal("explicit object-storage acceptance needs a private versioned loopback fixture")
		}
		f.cfg.AssetStorage = AssetStorageConfig{Backend: "s3", Endpoint: endpoint, Region: os.Getenv("TEST_S3_REGION"), Bucket: os.Getenv("TEST_S3_BUCKET"),
			Prefix: "source039/" + newID("test"), AccessKeyID: os.Getenv("TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("TEST_S3_SECRET_ACCESS_KEY"), ForcePathStyle: true}
	}
	readWire := watchSourceVideoWire039(t, f, source, cases)
	f.media.mu.Lock()
	f.media.video = source
	f.media.mu.Unlock()
	a, s := f.open(t)
	client := authClient(t)
	const email = "source039-owner@example.test"
	owner := registerFixture(t, client, s.URL, email)
	project := owner.Project.ID
	original, asset := timelineTestSource(t, a, s, client, owner)
	if f.cfg.AssetStorage.Backend == "s3" {
		info, err := a.assets.Stat(t.Context(), original.Artifacts[0].ID)
		if err != nil || info.StorageBackend != "s3" || info.ObjectVersion == "" || info.ObjectVersion == "null" {
			t.Fatal("source asset lacks a versioned private object receipt")
		}
		// Replace only the latest version in this disposable prefix. The
		// ordinary supplier socket and restored download must still see red.
		cloud := a.assets.(*S3AssetStorage)
		put, err := cloud.client.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String(f.cfg.AssetStorage.Bucket),
			Key: aws.String(info.ObjectKey), ContentType: aws.String("video/mp4"), Body: bytes.NewReader(cases[0].output)})
		if err != nil || aws.ToString(put.VersionId) == "" || aws.ToString(put.VersionId) == info.ObjectVersion {
			t.Fatal("could not create a distinct latest version in the private fixture")
		}
	}
	s.Close()
	a.Close()

	catalog := defaultCatalogConfig(Config{Mode: "demo"})
	for i := range catalog.Models {
		if catalog.Models[i].Kind == "video" {
			catalog.Models[i].Capabilities.SourceVideo = &SourceVideoCapabilities{EditPresets: []string{"preset-1"}, ExtendPresets: []string{"preset-1"}, Directions: []string{"forward"}}
		}
	}
	f.cfg.ModelCatalogFile = writeCatalogFixture(t, catalog)
	f.cfg.BillingMode = "credits"
	f.cfg.BillingPriceFile = filepath.Join(t.TempDir(), "prices.json")
	prices := `{"schema_version":1,"provider_mode":"demo","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-video","kind":"video","operation":"video_edit","preset_id":"preset-1","artifact_micros":"200000"},{"model_id":"studio-video","kind":"video","operation":"video_extend","preset_id":"preset-1","artifact_micros":"350000"}]}`
	if err := os.WriteFile(f.cfg.BillingPriceFile, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	a, s = f.open(t)
	httpCredit(t, a, owner.Workspace.ID, 1000000)
	call := func(c *http.Client, scope, method, path, key string, body any, want int) ([]byte, http.Header) {
		t.Helper()
		return presentationRequest(t, c, s.URL, scope, method, path, key, body, want)
	}
	otherC := authClient(t)
	other := registerFixture(t, otherC, s.URL, "source039-other@example.test")
	// As in the account-isolation fixtures, provision one distinct authenticated
	// session in the same workspace so the actor predicate is exercised.
	peer := authClient(t)
	registerFixture(t, peer, s.URL, "source039-peer@example.test")
	p, err := a.currentPrincipal(operationsRequest(t, peer, s))
	if err != nil {
		t.Fatal(err)
	}
	securityExec(t, a, "UPDATE studio_sessions SET workspace_id=$1 WHERE id=$2", owner.Workspace.ID, p.SessionID)
	call(peer, project, "GET", "/api/v1/tasks/"+original.ID, "", nil, 200)
	siblingRaw, _ := call(client, project, "POST", "/api/v1/projects", "", map[string]string{"name": "source039 sibling"}, 201)
	sibling := decode[Project](t, siblingRaw).ID
	model := a.catalog.Entries["studio-video"].Model
	makeInput := func(tc *sourceVideoCase039) TaskInput {
		in := taskInput("video")
		in.ProjectID, in.ModelVersion, in.Operation = project, model.Version, tc.operation
		in.Parameters.Seconds = 5
		in.Inputs.SourceVideo = &SourceVideoRef{AssetID: asset, AssetRevision: 1}
		in.Inputs.Prompt, in.Inputs.ExtensionDirection = tc.operation+" fixture", tc.direction
		return in
	}
	quoteBody := func(in TaskInput) any { return map[string]any{"resource_type": "task", "task_input": in} }
	for _, den := range []struct {
		c       *http.Client
		project string
	}{{client, sibling}, {otherC, other.Project.ID}} {
		in := makeInput(cases[0])
		in.ProjectID = den.project
		call(den.c, den.project, "POST", "/api/v1/billing/quotes", "", quoteBody(in), 409)
	}
	bad := makeInput(cases[1])
	bad.Inputs.ExtensionDirection = "backward"
	call(client, project, "POST", "/api/v1/billing/quotes", "", quoteBody(bad), 422)
	stale := makeInput(cases[0])
	stale.Inputs.SourceVideo.AssetRevision = 2
	call(client, project, "POST", "/api/v1/billing/quotes", "", quoteBody(stale), 409)
	unused := makeInput(cases[0])
	unused.QuoteID = httpBillingQuote(t, client, s, project, "task", unused).ID
	peerKey := newID("sourcepeer")
	refused, _ := call(peer, project, "POST", "/api/v1/tasks", peerKey, unused, 409)
	if !bytes.Contains(refused, []byte("QUOTE_MISMATCH")) {
		t.Fatal("different applicant could present the original source-video quote")
	}
	call(peer, project, "GET", sourceVideoReceiptPath, peerKey, nil, 404)
	verifySourceSessionExpiry039(t, a, s, client, email, f.cfg.DatabaseURL, makeInput(cases[0]))
	swapped := makeInput(cases[1])
	swapped.QuoteID = unused.QuoteID
	wrongKey := newID("source039")
	refusal, _ := call(client, project, "POST", "/api/v1/tasks", wrongKey, swapped, 409)
	if !bytes.Contains(refusal, []byte("QUOTE_MISMATCH")) {
		t.Fatal("different operation reached the wrong quote boundary")
	}
	call(client, project, "GET", sourceVideoReceiptPath, wrongKey, nil, 404)
	for _, tc := range cases {
		in := makeInput(tc)
		quote := httpBillingQuote(t, client, s, project, "task", in)
		if quote.ReserveMicros != tc.price || quote.ArtifactMicros != tc.price || quote.ModelVersion != model.Version || quote.ProjectID != project {
			t.Fatal("incorrect operation-specific quote", tc.operation)
		}
		in.QuoteID = quote.ID
		tc.key, tc.input = newID("source039"), in
		f.media.mu.Lock()
		f.media.video = tc.output
		f.media.mu.Unlock()
		if tc.operation == "video_extend" {
			loseSourceVideoAcceptance039(t, a, client, in, tc.key)
		} else {
			raw, _ := call(client, project, "POST", "/api/v1/tasks", tc.key, in, 202)
			tc.taskID = decode[Task](t, raw).ID
		}
		raw, headers := call(client, project, "GET", sourceVideoReceiptPath, tc.key, nil, 200)
		task := decode[Task](t, raw)
		if task.ID == "" || tc.taskID != "" && tc.taskID != task.ID || task.ProjectID != project || task.Operation != tc.operation ||
			task.SourceVideo == nil || *task.SourceVideo != *in.Inputs.SourceVideo || task.ExtensionDirection != tc.direction || headers.Get("Cache-Control") != "no-store" {
			t.Fatal("receipt did not identify the accepted task", tc.operation)
		}
		tc.taskID = task.ID
		laneTaskStatus(t, a, task.ID, "SUCCEEDED")
		call(client, sibling, "GET", sourceVideoReceiptPath, tc.key, nil, 404)
		call(otherC, other.Project.ID, "GET", sourceVideoReceiptPath, tc.key, nil, 404)
		call(peer, project, "GET", sourceVideoReceiptPath, tc.key, nil, 404)
		call(peer, project, "POST", "/api/v1/tasks", tc.key, tc.input, 404)
		call(client, "", "GET", sourceVideoReceiptPath, tc.key, nil, 400)
	}
	call(client, project, "POST", "/api/v1/library/"+asset+"/trash", newID("trash"), stateInput(1), 200)
	call(client, project, "POST", "/api/v1/billing/quotes", "", quoteBody(makeInput(cases[0])), 409)
	rejected := newID("source039")
	call(client, project, "POST", "/api/v1/tasks", rejected, unused, 409)
	call(client, project, "GET", sourceVideoReceiptPath, rejected, nil, 404)
	verifyReplay := func(tc *sourceVideoCase039) {
		t.Helper()
		raw, h := call(client, project, "POST", "/api/v1/tasks", tc.key, tc.input, 202)
		if h.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != tc.taskID {
			t.Fatal("original operation did not replay", tc.operation)
		}
	}
	for _, tc := range cases {
		verifyReplay(tc)
	}
	call(client, project, "POST", "/api/v1/library/"+asset+"/restore", newID("restore"), stateInput(2), 200)
	call(client, project, "POST", "/api/v1/billing/quotes", "", quoteBody(makeInput(cases[0])), 409)
	call(client, project, "POST", "/api/v1/tasks", rejected, unused, 409)
	changed := cases[0].input
	changed.Inputs.Prompt += " changed"
	call(client, project, "POST", "/api/v1/tasks", cases[0].key, changed, 409)

	verify := func(db *pgx.Conn) {
		t.Helper()
		if outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 3 || outboxCount(t, db, "SELECT count(*) FROM studio_task_video_sources") != 2 ||
			outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 2 ||
			outboxCount(t, db, "SELECT count(*) FROM studio_task_video_sources WHERE task_id=$1", original.ID) != 0 {
			t.Fatal("unexpected task, source binding or reservation for generated history, denied operation or replay")
		}
		raw, _ := call(client, project, "GET", original.Artifacts[0].URL, "", nil, 200)
		if !bytes.Equal(raw, source) {
			t.Fatal("original MP4 changed")
		}
		observed := readWire()
		for _, tc := range cases {
			w := observed[tc.operation]
			if len(w) != 1 {
				t.Fatal("unexpected supplier POST count", tc.operation, len(w))
			}
			raw, _ := call(client, project, "GET", sourceVideoReceiptPath, tc.key, nil, 200)
			result := decode[Task](t, raw)
			if result.ID != tc.taskID || result.Status != "SUCCEEDED" || result.Moderation == nil || result.Moderation.State != "ALLOWED" ||
				len(result.Artifacts) != 1 || result.Artifacts[0].ID == original.Artifacts[0].ID {
				t.Fatal("missing approved derivative", tc.operation)
			}
			for _, field := range []string{"body_file_id", "data:video/mp4", "storage_identity", "provider_identity"} {
				if bytes.Contains(raw, []byte(field)) {
					t.Fatal("receipt exposed a private submission field", tc.operation)
				}
			}
			raw, _ = call(client, project, "GET", result.Artifacts[0].URL, "", nil, 200)
			if !bytes.Equal(raw, tc.output) {
				t.Fatal("authorized output differs from delivered MP4", tc.operation)
			}
			call(otherC, other.Project.ID, "GET", result.Artifacts[0].URL, "", nil, 404)
			if outboxCount(t, db, `SELECT count(*) FROM studio_task_video_sources WHERE task_id=$1 AND operation=$2 AND source_task_id=$3 AND artifact_id=$4 AND asset_id=$5 AND asset_revision=1 AND content_version=$6 AND actor_id=$7 AND project_id=$8 AND workspace_id=$9`,
				tc.taskID, tc.operation, original.ID, original.Artifacts[0].ID, asset, moderationSHA(source), owner.User.ID, project, owner.Workspace.ID) != 1 ||
				outboxCount(t, db, `SELECT count(*) FROM studio_ledger_reservations WHERE resource_id=$1 AND state='SETTLED' AND charged_micros=$2 AND quote_id=$3`, tc.taskID, tc.price, tc.input.QuoteID) != 1 ||
				outboxCount(t, db, `SELECT count(*) FROM studio_ledger_journals WHERE resource_id=$1 AND kind='CHARGE' AND amount_micros=$2`, tc.taskID, tc.price) != 1 {
				t.Fatal("source revision, actor binding, quote, reservation or unique charge differs", tc.operation)
			}
			var sub MediaSubmission
			if err := db.QueryRow(t.Context(), "SELECT submission FROM studio_tasks WHERE id=$1", tc.taskID).Scan(&sub); err != nil {
				t.Fatal(err)
			}
			if sub.BodyFileID == "" || len(sub.Body) != 0 || sub.Endpoint != tc.endpoint || sub.InputType != tc.operation || sub.ContentType != "application/json" ||
				sub.BodySHA256 != w[0].hash || sub.BodySize != w[0].size || sub.IdempotencyKey != w[0].key {
				t.Fatal("persistent submission differs from the supplier wire", tc.operation)
			}
			file, err := a.references.OpenBody(sub.BodyFileID)
			if err != nil {
				t.Fatal("private frozen body missing", err)
			}
			body, err := io.ReadAll(io.LimitReader(file, sub.BodySize+1))
			file.Close()
			if err != nil || int64(len(body)) != sub.BodySize || moderationSHA(body) != sub.BodySHA256 {
				t.Fatal("original frozen body bytes differ", tc.operation)
			}
		}
		f.media.mu.Lock()
		posts := f.media.count
		f.media.mu.Unlock()
		if posts != 3 {
			t.Fatal("expected seed, edit and extend exactly once", posts)
		}
	}
	verify(deliveryAdmin(t, f.cfg.DatabaseURL))
	s.Close()
	a.Close()
	restoreWorkflowFixture(t, f, "studio_tasks", "studio_task_video_sources", "studio_idempotency", "studio_ledger_quotes", "studio_ledger_reservations", "studio_ledger_journals", "studio_ledger_postings", "studio_media_deliveries", "studio_media_delivery_items", "studio_library_revisions")
	a, s = f.open(t)
	login := func(email string) *http.Client {
		c := authClient(t)
		call(c, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
		return c
	}
	client = login(email)
	otherC = login("source039-other@example.test")
	for _, tc := range cases {
		verifyReplay(tc)
	}
	verify(deliveryAdmin(t, f.cfg.DatabaseURL))
}

func TestPostgresSourceVideoUpgrade038(t *testing.T) {
	db := testDatabase(t)
	admin := deliveryAdmin(t, db)
	ctx := t.Context()
	old := workflowOldMigrations(t, "038")
	err := pgx.BeginFunc(ctx, admin, func(tx pgx.Tx) error {
		if err := applyMigrationsFrom(ctx, tx, old); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_before','v038-panel@example.test','author',decode('01','hex'));
		 INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','old workspace');
		 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','old project');
		 INSERT INTO studio_comics(project_id,workspace_id,version) VALUES('prj_demo','ws_demo',1);
		 INSERT INTO studio_comic_versions(project_id,workspace_id,version,created_by,document) VALUES('prj_demo','ws_demo',1,'usr_before',
		 '{"title":"old","story":"old story","characters":[{"id":"lead","name":"Lead","description":"local"}],"scenes":[{"id":"port","name":"Port","description":"day"}],"panels":[{"id":"shot","title":"first","scene_id":"port","character_ids":["lead"],"action":"arrives"}]}');
		 INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,submission,next_poll) VALUES('task_old_panel','ws_demo','prj_demo',
		 '{"id":"task_old_panel","workspace_id":"ws_demo","project_id":"prj_demo","kind":"video","provider_mode":"demo","status":"SUCCEEDED","progress":100,"version":4}',
		 '{"project_id":"prj_demo","kind":"video","model_id":"old-video"}','null',NULL,clock_timestamp());
		 INSERT INTO studio_comic_panel_videos(task_id,project_id,workspace_id,comic_version,panel_id,attempt,actor_id,key_hash,request_hash)
		 VALUES('task_old_panel','prj_demo','ws_demo',1,'shot',1,'usr_before',decode(repeat('10',32),'hex'),decode(repeat('20',32),'hex'));
		 INSERT INTO studio_idempotency(scope_key,payload_hash,resource_id,resource_type)
		 VALUES('ws_demo:tasks:old-key',decode(repeat('20',32),'hex'),'task_old_panel','task')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_tasks", "studio_task_schedule", "studio_idempotency", "studio_comic_versions", "studio_comic_panel_videos", "studio_ledger_quotes", "studio_ledger_reservations"}
	before := schedulerOriginalSnapshot(t, admin, tables)
	store, err := openPostgres(db)
	if err != nil {
		t.Fatal(err)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, admin, tables)) || outboxCount(t, admin, "SELECT count(*) FROM studio_task_video_sources") != 0 {
		t.Fatal("039 rewrote an old panel task, original receipt or price, or inferred a source binding")
	}
	err = pgx.BeginFunc(ctx, admin, func(tx pgx.Tx) error { return applyMigrationsFrom(ctx, tx, old) })
	if err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("previous writer accepted the source-video format")
	}
}
