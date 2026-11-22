package studio

import (
	"bytes"
	"net/http"
	"testing"
)

func TestPostgresCanvasPairedRestorePreservesFrozenRevision(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	const email = "canvas-backup@example.test"
	ownerClient := authClient(t)
	p := registerFixture(t, ownerClient, s.URL, email).Project.ID
	base := "/api/v1/projects/" + p + "/canvas"
	call := func(method, path, key string, input any, want int) []byte {
		t.Helper()
		status, data, _ := authRequest(t, ownerClient, s.URL, method, path, key, p, input)
		if status != want {
			t.Fatalf("canvas recovery %s got %d, expected %d", method, status, want)
		}
		return data
	}
	status, data, _ := uploadReferenceFixture(t, ownerClient, s.URL, p, newID("upload"), "原图.png", referencePNG(t, 36), false)
	if status != 201 {
		t.Fatalf("reference upload returned %d", status)
	}
	id := libraryID("reference", decode[ReferenceAsset](t, data).ID, "")
	run := decode[Run](t, call("POST", "/api/v1/runs", newID("runkey"), RunInput{ProjectID: p, ModelID: "studio-chat", Input: "本机画布恢复测试"}, 201))
	if awaitAuthRun(t, ownerClient, s.URL, run.ID).Status != "COMPLETED" {
		t.Fatal("run did not complete")
	}
	original := canvasFixtureInput(0, 4)
	original.Document.Nodes[0].AssetID = id
	original.Document.Nodes[0].AssetRevision = 1
	original.Document.Nodes[1].RunID = run.ID
	first := call("PUT", base, "canvas-backup-original", original, 200)
	v := decode[CanvasVersion](t, first)
	img := v.Sources["n0"]
	runSrc := v.Sources["n1"]
	if !img.Available || !mediaContentHash.MatchString(img.ContentVersion) || !runSrc.Available || runSrc.Status != "COMPLETED" {
		t.Fatal("missing frozen resources")
	}
	thumb := call("GET", img.ThumbnailURL, "", nil, 200)
	second := canvasFixtureInput(1, 3)
	second.Document.Title = "修订二"
	second.Document.Groups[0].Collapsed = false
	latest := call("PUT", base, "canvas-backup-second", second, 200)
	s.Close()
	a.Close()
	restoreWorkflowFixture(t, f, "studio_canvas_versions", "studio_canvas_sources", "studio_runs", "studio_library_revisions", "studio_references", "studio_reference_idempotency")
	_, s = f.open(t)
	ownerClient = authClient(t)
	presentationRequest(t, ownerClient, s.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	if !bytes.Equal(latest, call("GET", base, "", nil, 200)) || !bytes.Equal(first, call("GET", base+"/revisions/1", "", nil, 200)) || !bytes.Equal(first, call("GET", base+"/receipt", "canvas-backup-original", nil, 200)) || !bytes.Equal(thumb, call("GET", img.ThumbnailURL, "", nil, 200)) {
		t.Fatal("restored canvas revision, receipt, or authorized original thumbnail changed")
	}
	if !bytes.Equal(first, call("PUT", base, "canvas-backup-original", original, 200)) || !bytes.Equal(latest, call("GET", base, "", nil, 200)) {
		t.Fatal("old key changed current revision")
	}
	guest := authClient(t)
	if code, _, _ := authRequest(t, guest, s.URL, http.MethodGet, img.ThumbnailURL, "", p, nil); code != 401 {
		t.Fatal("restored thumbnail accepted unauthenticated reader")
	}
	call("POST", "/api/v1/library/"+id+"/trash", newID("trash"), map[string]int{"expected_revision": 1}, 200)
	call("GET", img.ThumbnailURL, "", nil, 404)
	old := decode[CanvasVersion](t, call("GET", base+"/receipt", "canvas-backup-original", nil, 200))
	if old.Sources["n0"].Available || old.Sources["n0"].ThumbnailURL != "" {
		t.Fatal("old canvas receipt disclosed a recycled preview")
	}
}
