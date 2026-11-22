package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPresentationHTTPWriteFieldsAndInputLimit(t *testing.T) {
	valid := `{"expected_version":0,"document":{"title":"A","theme":"paper","slides":[{"id":"one","title":"Hello","layout":"content","body":"text","notes":""}]}}`
	parse := func(raw string) (PresentationSaveInput, error) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		return decodePresentationJSON(httptest.NewRecorder(), req)
	}
	if input, err := parse(valid); err != nil || input.Document.Slides[0].Body != "text" {
		t.Fatal("valid write fields rejected", err)
	}
	for _, field := range []string{`"content_version":null`, `"asset_available":false`, `"content_url":"/private"`, `"object_key":"secret"`} {
		if _, err := parse(strings.Replace(valid, `"notes":""`, `"notes":"",`+field, 1)); err == nil {
			t.Fatal("accepted output or private source field", field)
		}
	}
	for _, raw := range []string{valid + valid, string([]byte{0xff}), strings.Repeat(" ", (2<<20)+1) + valid} {
		if _, err := parse(raw); err == nil {
			t.Fatal("accepted malformed or oversized presentation")
		}
	}
	_, s := testApp(t, Config{})
	if status, _, _ := authRequest(t, s.Client(), s.URL, "GET", "/api/v1/projects/prj_demo/presentations", "", "", nil); status != 501 {
		t.Fatal("memory mode incorrectly exposed persistent presentations", status)
	}
}

// Uploads an independently known PNG through the ordinary private reference
// endpoint. The fixture's moderation service must issue ALLOW on its real bytes.
func presentationReferenceFixture(t *testing.T, s *httptest.Server, c *http.Client, project string) (string, []byte) {
	t.Helper()
	image := referencePNG(t, 82)
	code, raw, _ := uploadReferenceFixture(t, c, s.URL, project, "presentation-reference", "private-slide.png", image, false)
	if code != 201 {
		t.Fatal("private reference upload", code, string(raw))
	}
	ref := decode[ReferenceAsset](t, raw)
	if ref.Moderation == nil || ref.Moderation.State != "ALLOWED" {
		t.Fatal("reference was not approved")
	}
	return libraryID("reference", ref.ID, ""), image
}

func presentationInputFixture(id string, prior uint64, title, asset string) PresentationSaveInput {
	return PresentationSaveInput{PresentationID: id, ExpectedVersion: &prior, Document: PresentationDocument{
		Title: title, Theme: "paper", Slides: []PresentationSlide{
			{ID: "cover", Layout: "title", Title: title, Body: "Editable text", Notes: "Original notes"},
			{ID: "picture", Layout: "image", Title: "Project material", Body: "Original caption", Notes: "Private original", AssetID: asset, AssetRevision: 1},
		},
	}}
}

func presentationRequest(t *testing.T, c *http.Client, base, project, method, route, key string, input any, want int) ([]byte, http.Header) {
	t.Helper()
	status, data, h := authRequest(t, c, base, method, route, key, project, input)
	if status != want {
		t.Fatalf("%s %s returned %d, wanted %d: %.256s", method, route, status, want, data)
	}
	return data, h
}

func TestPostgresPresentationHTTPVersionAndPrivateImage(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	_, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "presentation-owner@example.test")
	project := owner.Project.ID
	api := "/api/v1/projects/" + project + "/presentations"
	call := func(method, route, key string, input any, want int) []byte {
		t.Helper()
		b, _ := presentationRequest(t, c, s.URL, project, method, route, key, input, want)
		return b
	}
	asset, image := presentationReferenceFixture(t, s, c, project)
	in := presentationInputFixture("", 0, "Private draft", asset)
	first := decode[PresentationVersion](t, call("POST", api, "presentation-save-original", in, 201))
	route := api + "/" + first.ID + "/versions/1"
	manifest := decode[presentationExportManifest](t, call("GET", route+"/export", "", nil, 200))
	url := manifest.ImageURLs["picture"]
	wantURL := route + "/slides/picture/image"
	sha := sha256.Sum256(image)
	if url != wantURL || len(manifest.ImageURLs) != 1 || manifest.Document.Slides[1].ContentVersion != hex.EncodeToString(sha[:]) {
		t.Fatal("export did not bind its original bytes to the requested slide and version")
	}
	raw, h := presentationRequest(t, c, s.URL, project, "GET", url, "", nil, 200)
	if !bytes.Equal(raw, image) || h.Get("Content-Type") != "image/png" || h.Get("X-Image-Width") != "12" || h.Get("X-Image-Height") != "9" || !strings.Contains(h.Get("Cache-Control"), "no-store") {
		t.Fatal("wrong private bytes, dimensions or cache policy")
	}
	next := presentationInputFixture(first.ID, 1, "Revised draft", asset)
	next.Document.Slides[1].Notes = "Later notes"
	second := decode[PresentationVersion](t, call("POST", api, "presentation-second-key", next, 201))
	if second.Version != 2 {
		t.Fatal("missing second version")
	}
	for _, p := range []string{route, route + "/export"} {
		v := decode[PresentationVersion](t, call("GET", p, "", nil, 200))
		if v.Document.Title != "Private draft" || v.Document.Slides[1].Notes != "Private original" {
			t.Fatal("reading an old version returned a newer edit")
		}
	}
	receipt := decode[PresentationVersion](t, call("GET", api+"/receipt", "presentation-save-original", nil, 200))
	if receipt.ID != first.ID || receipt.Version != 1 {
		t.Fatal("original save receipt drifted")
	}
	_, h = presentationRequest(t, c, s.URL, project, "POST", api, "presentation-save-original", in, 201)
	if h.Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay was not identified")
	}
	call("POST", api, "presentation-stale-version", next, 409)
	if history := decode[PresentationHistoryPage](t, call("GET", api+"/"+first.ID+"/versions?limit=1", "", nil, 200)); len(history.Items) != 1 || history.Items[0].Version != 2 || history.NextBeforeVersion == nil {
		t.Fatal("history pagination missing or reordered")
	}
	if page := decode[PresentationPage](t, call("GET", api, "", nil, 200)); len(page.Items) != 1 || page.Items[0].Version != 2 {
		t.Fatal("incorrect project list")
	}
	stranger := authClient(t)
	presentationRequest(t, stranger, s.URL, "", "GET", url, "", nil, 401)
	other := registerFixture(t, stranger, s.URL, "presentation-other@example.test")
	otherAPI := "/api/v1/projects/" + other.Project.ID + "/presentations"
	for _, p := range []string{api, route, route + "/export", url, api + "/receipt"} {
		presentationRequest(t, stranger, s.URL, other.Project.ID, "GET", p, "presentation-save-original", nil, 404)
	}
	presentationRequest(t, stranger, s.URL, other.Project.ID, "POST", otherAPI, "presentation-foreign-asset", in, 409)
	// Rename is metadata-only: a new export can use the same frozen original.
	lib := "/api/v1/library/" + asset
	call("PATCH", lib, "presentation-rename", map[string]any{"expected_revision": 1, "name": "Renamed", "tags": []string{}}, 200)
	call("GET", route+"/export", "", nil, 200)
	call("POST", lib+"/trash", "presentation-trash", map[string]any{"expected_revision": 2}, 200)
	still := decode[PresentationVersion](t, call("GET", route, "", nil, 200))
	if available := still.Document.Slides[1].AssetAvailable; available == nil || *available {
		t.Fatal("archived slide did not report its recycled image")
	}
	call("GET", route+"/export", "", nil, 409)
	call("GET", url, "", nil, 409)
	call("POST", lib+"/restore", "presentation-restore", map[string]any{"expected_revision": 3}, 200)
	if !bytes.Equal(image, call("GET", url, "", nil, 200)) {
		t.Fatal("restored same original did not match")
	}
	// A library entry alone cannot authorize exporting missing or changed bytes.
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	var blobID string
	if err := admin.QueryRow(context.Background(), "SELECT r.blob->>'id' FROM studio_references r JOIN studio_library_entries l ON l.reference_id=r.id WHERE l.id=$1", asset).Scan(&blobID); err != nil {
		t.Fatal(err)
	}
	if !privateBlobID.MatchString(blobID) {
		t.Fatal("unexpected fixture blob ID")
	}
	if err := os.WriteFile(filepath.Join(f.cfg.ReferenceDir, blobID+".bin"), bytes.Repeat([]byte{0}, len(image)), 0600); err != nil {
		t.Fatal(err)
	}
	call("GET", url, "", nil, 409)
}

func TestPostgresPresentationBackupOldVersion(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("TEST_POSTGRES_BIN_DIR required for physical tools pg_dump and pg_restore")
	}
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	c := authClient(t)
	const email = "presentation-restore@example.test"
	owner := registerFixture(t, c, s.URL, email)
	project := owner.Project.ID
	api := "/api/v1/projects/" + project + "/presentations"
	asset, image := presentationReferenceFixture(t, s, c, project)
	data, _ := presentationRequest(t, c, s.URL, project, "POST", api, "presentation-backup-v1", presentationInputFixture("", 0, "Original", asset), 201)
	first := decode[PresentationVersion](t, data)
	presentationRequest(t, c, s.URL, project, "POST", api, "presentation-backup-v2", presentationInputFixture(first.ID, 1, "New title", asset), 201)
	s.Close()
	a.Close()
	restoreWorkflowFixture(t, f, "studio_presentations", "studio_presentation_versions", "studio_presentation_assets", "studio_presentation_keys")
	_, restored := f.open(t)
	c = authClient(t)
	presentationRequest(t, c, restored.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	original, _ := presentationRequest(t, c, restored.URL, project, "GET", api+"/receipt", "presentation-backup-v1", nil, 200)
	old := decode[PresentationVersion](t, original)
	if old.ID != first.ID || old.Version != 1 || old.Document.Title != "Original" {
		t.Fatal("paired restore changed original save")
	}
	data, _ = presentationRequest(t, c, restored.URL, project, "GET", api+"/"+first.ID+"/versions/1/export", "", nil, 200)
	manifest := decode[presentationExportManifest](t, data)
	raw, _ := presentationRequest(t, c, restored.URL, project, "GET", manifest.ImageURLs["picture"], "", nil, 200)
	if !bytes.Equal(raw, image) {
		t.Fatal("paired restore lost original private bytes")
	}
}

func TestPostgresPresentationGeneratedApprovalAndRestrictedAccount(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	review := newReviewDispatchHTTPFixture(t, "")
	f.cfg.MediaModeration = review.config(t)
	f.cfg.OperationsEnabled = true
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "presentation-generated@example.test")
	project := owner.Project.ID
	api := "/api/v1/projects/" + project + "/presentations"
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	allowed, _ := acceptMediaExecution(t, a, s, c, owner, "image", "Original approved picture", "presentation-allowed-generation", 1)
	allowed = laneTaskStatus(t, a, allowed.ID, "SUCCEEDED")
	if len(allowed.Artifacts) != 1 || allowed.Moderation == nil || allowed.Moderation.State != "ALLOWED" {
		t.Fatal("expected published image")
	}
	asset := libraryID("generated", allowed.ID, allowed.Artifacts[0].ID)
	input := presentationInputFixture("", 0, "Verified original", asset)
	data, _ := presentationRequest(t, c, s.URL, project, "POST", api, "presentation-approved-save", input, 201)
	doc := decode[PresentationVersion](t, data)
	version := api + "/" + doc.ID + "/versions/1"
	imagePath := version + "/slides/picture/image"
	original, _ := presentationRequest(t, c, s.URL, project, "GET", imagePath, "", nil, 200)
	if !bytes.Equal(original, f.media.image) {
		t.Fatal("generated slide did not contain verified original bytes")
	}
	review.mu.Lock()
	review.blockSHA = moderationSHA(f.media.image)
	review.mu.Unlock()
	blocked, _ := acceptMediaExecution(t, a, s, c, owner, "image", "Second picture", "presentation-blocked-generation", 1)
	blocked = laneTaskStatus(t, a, blocked.ID, "FAILED")
	if len(blocked.Artifacts) != 0 || blocked.Moderation == nil || blocked.Moderation.State != "BLOCKED" {
		t.Fatal("expected fully quarantined output")
	}
	candidates, err := loadTaskMediaModerationSQL(context.Background(), admin, blocked.ID)
	if err != nil || candidates == nil || len(candidates.Candidates) != 1 {
		t.Fatal("missing private candidate evidence", err)
	}
	quarantined := libraryID("generated", blocked.ID, candidates.Candidates[0].ID)
	presentationRequest(t, c, s.URL, project, "POST", api, "presentation-quarantined-save", presentationInputFixture("", 0, "Unapproved", quarantined), 409)
	presentationRequest(t, c, s.URL, project, "GET", api+"/receipt", "presentation-quarantined-save", nil, 404)
	opClient := authClient(t)
	op := registerFixture(t, opClient, s.URL, "presentation-account-operator@example.test")
	if _, err := admin.Exec(context.Background(), "INSERT INTO studio_account_governance_grants(id,operator_user_id,target_user_id) VALUES($1,$2,$3)", newID("agnt"), op.User.ID, owner.User.ID); err != nil {
		t.Fatal(err)
	}
	scope := "/api/v1/operations/account-targets/" + owner.User.ID
	data, _ = presentationRequest(t, opClient, s.URL, "", "POST", scope+"/plans", "", AccountPlanInput{Action: "suspend", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "ticket:presentation", PublicReply: "申请人工复核"}, 201)
	plan := decode[AccountPlan](t, data)
	presentationRequest(t, opClient, s.URL, "", "POST", scope+"/execute", "", map[string]string{"plan_id": plan.ID, "plan_digest": plan.PlanDigest, "password": "fixture-password-123"}, 200)
	presentationRequest(t, c, s.URL, "", "GET", imagePath, "", nil, 401)
	limited := authClient(t)
	presentationRequest(t, limited, s.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": owner.User.Email, "password": "fixture-password-123"}, 200)
	for _, route := range []string{api, api + "/receipt", version, version + "/export", imagePath} {
		presentationRequest(t, limited, s.URL, "", "GET", route, "presentation-approved-save", nil, 403)
	}
	presentationRequest(t, limited, s.URL, "", "POST", api, "presentation-denied-save", input, 403)
}

func TestPostgresPresentationLostSaveReceipt(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "presentation-unknown@example.test")
	project := owner.Project.ID
	api := "/api/v1/projects/" + project + "/presentations"
	in := presentationInputFixture("", 0, "Original only", "")
	in.Document.Slides = in.Document.Slides[:1]
	const key = "presentation-unknown-save"
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `CREATE FUNCTION hold_presentation_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN PERFORM pg_advisory_xact_lock(912327,29); RETURN NEW; END $$;
	CREATE TRIGGER hold_presentation_receipt BEFORE INSERT ON studio_presentation_keys
	FOR EACH ROW EXECUTE FUNCTION hold_presentation_receipt()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912327,29)"); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() { code, _, _ := authRequest(t, c, s.URL, "POST", api, key, project, in); result <- code }()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, result)
	s.Close()
	a.Close()
	f.cfg.DatabaseURL = direct
	_, again := f.open(t)
	data, _ := presentationRequest(t, c, again.URL, project, "GET", api+"/receipt", key, nil, 200)
	got := decode[PresentationVersion](t, data)
	if got.Version != 1 || got.Document.Title != "Original only" || outboxCount(t, admin, "SELECT count(*) FROM studio_presentation_versions") != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_presentation_keys") != 1 {
		t.Fatal("read-only receipt did not identify the sole committed version")
	}
}

func TestPostgresPresentationBrowserExport(t *testing.T) {
	if os.Getenv("PRESENTATION_BROWSER_TEST") != "1" {
		t.Skip("PRESENTATION_BROWSER_TEST=1 requires built web assets and local Chromium")
	}
	f := newWorkflowIntegrationFixture(t)
	_, s := f.open(t)
	c := authClient(t)
	const email = "presentation-browser@example.test"
	owner := registerFixture(t, c, s.URL, email)
	asset, image := presentationReferenceFixture(t, s, c, owner.Project.ID)
	sum := sha256.Sum256(image)
	output := os.Getenv("PRESENTATION_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("PRESENTATION_BROWSER_OUTPUT must be an absolute directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/presentation-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "PRESENTATION_BROWSER_URL="+s.URL, "PRESENTATION_BROWSER_OUTPUT="+output, "PRESENTATION_EMAIL="+email, "PRESENTATION_PASSWORD=fixture-password-123", "PRESENTATION_PROJECT_ID="+owner.Project.ID, "PRESENTATION_ASSET_ID="+asset, "PRESENTATION_ASSET_SHA256="+hex.EncodeToString(sum[:]))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("presentation browser failed: %v\n%s", err, raw)
	}
	for _, name := range []string{"presentation-v1.pptx", "presentation-v2.pptx", "presentation-v1-reopened.pptx"} {
		file, err := os.Stat(filepath.Join(output, name))
		if err != nil || !file.Mode().IsRegular() || file.Size() < 1 {
			t.Fatal("a browser export was not written", name, err)
		}
	}
}
