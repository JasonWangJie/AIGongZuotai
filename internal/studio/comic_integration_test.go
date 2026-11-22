package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresComicBrowserEditableDownload(t *testing.T) {
	if os.Getenv("COMIC_BROWSER_TEST") != "1" {
		t.Skip("requires built frontend and Chromium")
	}
	f := newWorkflowIntegrationFixture(t)
	_, s := f.open(t)
	client := authClient(t)
	const email = "comic-browser@example.test"
	owner := registerFixture(t, client, s.URL, email)
	asset, image := presentationReferenceFixture(t, s, client, owner.Project.ID)
	digest := sha256.Sum256(image)
	output := os.Getenv("COMIC_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("COMIC_BROWSER_OUTPUT must be an absolute directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/comic-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "COMIC_BROWSER_URL="+s.URL, "COMIC_BROWSER_OUTPUT="+output,
		"COMIC_EMAIL="+email, "COMIC_PASSWORD=fixture-password-123", "COMIC_PROJECT_ID="+owner.Project.ID,
		"COMIC_ASSET_ID="+asset, "COMIC_ASSET_SHA256="+hex.EncodeToString(digest[:]))
	result, err := cmd.CombinedOutput()
	if bytes.Contains(result, []byte("fixture-password-123")) {
		t.Fatal("browser disclosed a fixture credential")
	}
	if err != nil {
		t.Fatalf("storyboard browser: %v\n%s", err, result)
	}
	t.Log(strings.TrimSpace(string(result)))
	for _, name := range []string{"comic-v1.pptx", "comic-v2.pptx", "comic-v1-reopened.pptx", "comic-expected.json"} {
		stat, err := os.Stat(filepath.Join(output, name))
		if err != nil || !stat.Mode().IsRegular() || stat.Size() < 1 {
			t.Fatal("missing real storyboard download", name)
		}
	}
	db := deliveryAdmin(t, f.cfg.DatabaseURL)
	if outboxCount(t, db, "SELECT count(*) FROM studio_tasks") != 0 ||
		outboxCount(t, db, "SELECT count(*) FROM studio_ledger_reservations") != 0 {
		t.Fatal("manual storyboard editing and download created generation or a reservation")
	}
}

func TestPostgresComicConfirmedVersionPairedRestore(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("requires pg_dump and pg_restore")
	}
	f := newWorkflowIntegrationFixture(t)
	app, s := f.open(t)
	c := authClient(t)
	const email = "comic-restore@example.test"
	owner := registerFixture(t, c, s.URL, email)
	project := owner.Project.ID
	asset, image := presentationReferenceFixture(t, s, c, project)
	api := "/api/v1/projects/" + project + "/comic"
	document := ComicDocument{Title: "潮汐站", Story: "两次列车抵达。", Characters: []ComicCard{{ID: "actor", Name: "林岚"}},
		Scenes: []ComicCard{{ID: "station", Name: "车站"}}, Panels: []ComicPanel{
			{ID: "arrival", Title: "抵达", SceneID: "station", CharacterIDs: []string{"actor"}, Action: "她看向站台。", AssetID: asset, AssetRevision: 1},
			{ID: "departure", Title: "离开", SceneID: "station", CharacterIDs: []string{"actor"}, Action: "她转身登车。", AssetID: asset, AssetRevision: 1},
		}}
	var previous uint64
	input := ComicSaveInput{ExpectedVersion: &previous, Document: document}
	presentationRequest(t, c, s.URL, project, "POST", api, "comic-original-revision", input, 201)
	presentationRequest(t, c, s.URL, project, "POST", api+"/versions/1/confirm", "comic-approval-original", nil, 200)
	previous = 1
	input.Document.Characters[0].Name = "小树"
	presentationRequest(t, c, s.URL, project, "POST", api, "comic-second-revision", input, 201)
	s.Close()
	app.Close()
	restoreWorkflowFixture(t, f, "studio_comics", "studio_comic_versions", "studio_comic_assets", "studio_comic_receipts")
	_, restored := f.open(t)
	c = authClient(t)
	presentationRequest(t, c, restored.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	raw, _ := presentationRequest(t, c, restored.URL, project, "GET", api+"/receipt?operation=confirm", "comic-approval-original", nil, 200)
	old := decode[ComicVersion](t, raw)
	if old.Version != 1 || !old.Confirmed || len(old.Document.Characters) != 1 || old.Document.Characters[0].Name != "林岚" {
		t.Fatal("confirmed version or original approval receipt changed across restore")
	}
	raw, _ = presentationRequest(t, c, restored.URL, project, "GET", api, "", nil, 200)
	current := decode[ComicVersion](t, raw)
	if current.Version != 2 || current.Confirmed || len(current.Document.Characters) != 1 || current.Document.Characters[0].Name != "小树" {
		t.Fatal("unconfirmed head did not survive separately from approved first version")
	}
	presentationRequest(t, c, restored.URL, project, "GET", api+"/versions/2/export", "", nil, 409)
	for _, id := range []string{"arrival", "departure"} {
		raw, _ = presentationRequest(t, c, restored.URL, project, "GET", api+"/versions/1/panels/"+id+"/image", "", nil, 200)
		if !bytes.Equal(raw, image) {
			t.Fatal("restored panel has different private bytes", id)
		}
	}
}
