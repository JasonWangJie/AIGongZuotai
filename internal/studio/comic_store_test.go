package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func comicTestInput(v uint64, asset string) ComicSaveInput {
	panel := ComicPanel{ID: "dawn", Title: "第一镜", SceneID: "quay", CharacterIDs: []string{"lin"}, Action: "走向码头。", Dialogue: "船来了。", AssetID: asset}
	if asset != "" {
		panel.AssetRevision = 1
	}
	second := panel
	second.ID = "meeting"
	second.Title = "第二镜"
	second.Action = "回头招手。"
	return ComicSaveInput{ExpectedVersion: &v, Document: ComicDocument{Title: "码头", Story: "两位朋友在码头相遇。", Characters: []ComicCard{{ID: "lin", Name: "林", Description: "旧外套"}}, Scenes: []ComicCard{{ID: "quay", Name: "河边码头", Description: "清晨"}}, Panels: []ComicPanel{panel, second}}}
}

func TestComicStoreDocumentBoundsAndReferences(t *testing.T) {
	asset := "lib_" + strings.Repeat("d", 64)
	d := comicTestInput(0, asset)
	if e := validateComic(d); e != nil {
		t.Fatal(e)
	}
	for _, modify := range []func(*ComicDocument){
		func(x *ComicDocument) { x.Characters = append(x.Characters, x.Characters[0]) },
		func(x *ComicDocument) { x.Panels[0].CharacterIDs = []string{"stranger"} },
		func(x *ComicDocument) { x.Panels[0].CharacterIDs = []string{"lin", "lin"} },
		func(x *ComicDocument) { x.Panels[0].SceneID = "absent" },
		func(x *ComicDocument) { x.Story = " \n " },
		func(x *ComicDocument) { x.Scenes[0].Description = strings.Repeat("场", 1001) },
		func(x *ComicDocument) { x.Panels[0].Action = "\u0000" },
		func(x *ComicDocument) { x.Panels[0].AssetID = "file:///private.png" },
		func(x *ComicDocument) { x.Panels[0].AssetRevision = 0 },
		func(x *ComicDocument) { x.Panels[0].ContentVersion = strings.Repeat("a", 64) },
		func(x *ComicDocument) { x.Panels = append(x.Panels, x.Panels[1]) },
	} {
		in := comicTestInput(0, asset)
		modify(&in.Document)
		requireOperation(t, validateComic(in), "INVALID_COMIC")
	}
	for i := len(d.Document.Panels); i < 5; i++ {
		p := d.Document.Panels[0]
		p.ID = fmt.Sprintf("s%d", i)
		d.Document.Panels = append(d.Document.Panels, p)
	}
	requireOperation(t, validateComic(d), "COMIC_IMAGE_LIMIT")
	d = comicTestInput(0, "")
	for i := len(d.Document.Panels); i < 12; i++ {
		p := d.Document.Panels[0]
		p.ID = fmt.Sprintf("s%d", i)
		d.Document.Panels = append(d.Document.Panels, p)
	}
	if e := validateComic(d); e != nil {
		t.Fatal("at 12 panels", e)
	}
	d.Document.Panels = append(d.Document.Panels, d.Document.Panels[0])
	requireOperation(t, validateComic(d), "INVALID_COMIC")
	large := comicTestInput(0, "")
	escaped := strings.Repeat("<", 1000)
	large.Document.Story = strings.Repeat("<", 4000)
	large.Document.Characters = nil
	large.Document.Scenes = nil
	for i := 0; i < 16; i++ {
		card := ComicCard{ID: fmt.Sprintf("c%d", i), Name: "人", Description: escaped}
		large.Document.Characters = append(large.Document.Characters, card)
		card.ID = fmt.Sprintf("s%d", i)
		large.Document.Scenes = append(large.Document.Scenes, card)
	}
	large.Document.Panels = nil
	for i := 0; i < 12; i++ {
		large.Document.Panels = append(large.Document.Panels, ComicPanel{ID: fmt.Sprintf("p%d", i), Title: "镜头", SceneID: "s0", CharacterIDs: []string{"c0"}, Action: escaped, Dialogue: escaped})
	}
	requireOperation(t, validateComic(large), "COMIC_TOO_LARGE")
}

func TestPostgresComicAuthorConfirmationFrozenCardsAndPrivateImages(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "comic-origin@example.test")
	project := owner.Project.ID
	p, e := a.currentPrincipal(operationsRequest(t, c, s))
	if e != nil {
		t.Fatal(e)
	}
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	image, raw := presentationReferenceFixture(t, s, c, project)
	in := comicTestInput(0, image)
	saveKey := newID("save")
	v, replay, e := a.saveComic(p, project, in, saveKey)
	if e != nil || v.Version != 1 || v.Confirmed || replay || len(v.Document.Panels) != 2 || v.Document.Panels[1].ContentVersion != moderationSHA(raw) {
		t.Fatal("initial version", e)
	}
	_, e = a.comicPanelImage(p, project, 1, "dawn")
	requireOperation(t, e, "COMIC_NOT_CONFIRMED")
	otherC := authClient(t)
	other := registerFixture(t, otherC, s.URL, "comic-elsewhere@example.test")
	op, e := a.currentPrincipal(operationsRequest(t, otherC, s))
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.comicVersion(op, project, 1, false)
	requireOperation(t, e, "COMIC_NOT_FOUND")
	_, _, e = a.saveComic(op, other.Project.ID, in, newID("save"))
	requireOperation(t, e, "COMIC_ASSET_UNAVAILABLE")
	// Explicitly put a fixture session for a second actor on the same
	// workspace. Read permission for that session must not confer authorship.
	if _, e = admin.Exec(context.Background(), "UPDATE studio_sessions SET workspace_id=$1 WHERE id=$2", p.Workspace.ID, op.SessionID); e != nil {
		t.Fatal(e)
	}
	op, e = a.currentPrincipal(operationsRequest(t, otherC, s))
	if e != nil || op == nil || op.Workspace.ID != p.Workspace.ID {
		t.Fatal("second actor session did not enter the same project workspace", e)
	}
	if _, e = a.comicVersion(op, project, 1, false); e != nil {
		t.Fatal("second actor must have read access for the author check", e)
	}
	_, _, e = a.confirmComic(op, project, 1, newID("confirm"))
	requireOperation(t, e, "COMIC_AUTHOR_REQUIRED")
	_, e = a.comicReceipt(op, project, "save", saveKey)
	requireOperation(t, e, "COMIC_NOT_FOUND")
	confirmKey := newID("confirm")
	code, _, _ := authRequest(t, c, s.URL, "POST", "/api/v1/library/"+image+"/trash", newID("trash"), project, stateInput(1))
	if code != 200 {
		t.Fatal("trash", code)
	}
	_, _, e = a.confirmComic(p, project, 1, confirmKey)
	requireOperation(t, e, "COMIC_ASSET_UNAVAILABLE")
	_, e = a.comicReceipt(p, project, "confirm", confirmKey)
	requireOperation(t, e, "COMIC_NOT_FOUND")
	code, _, _ = authRequest(t, c, s.URL, "POST", "/api/v1/library/"+image+"/restore", newID("restore"), project, stateInput(2))
	if code != 200 {
		t.Fatal("restore", code)
	}
	v, replay, e = a.confirmComic(p, project, 1, confirmKey)
	if e != nil || replay || !v.Confirmed {
		t.Fatal("confirmation", e)
	}
	proof, e := a.comicPanelImage(p, project, 1, "meeting")
	if e != nil || proof.SHA256 != moderationSHA(raw) || proof.ReferenceBlob == nil {
		t.Fatal("private source", e)
	}
	next := comicTestInput(1, "")
	next.Document.Characters[0].Name = "新姓名"
	next.Document.Scenes[0].Description = "夜晚"
	v, _, e = a.saveComic(p, project, next, newID("save"))
	if e != nil || v.Version != 2 || v.Confirmed {
		t.Fatal("new version auto-confirmed", e)
	}
	_, _, e = a.confirmComic(p, project, 1, newID("confirm"))
	requireOperation(t, e, "COMIC_VERSION_CONFLICT")
	_, _, e = a.confirmComic(p, project, 2, confirmKey)
	requireOperation(t, e, "IDEMPOTENCY_CONFLICT")
	old, replay, e := a.confirmComic(p, project, 1, confirmKey)
	if e != nil || !replay || !old.Confirmed || old.Document.Characters[0].Name != "林" || old.Document.Scenes[0].Description != "清晨" {
		t.Fatal("original confirmation changed previous cards", e)
	}
	code, _, _ = authRequest(t, c, s.URL, "POST", "/api/v1/library/"+image+"/trash", newID("trash"), project, stateInput(3))
	if code != 200 {
		t.Fatal("second trash", code)
	}
	old, e = a.comicReceipt(p, project, "confirm", confirmKey)
	if e != nil || !old.Confirmed || old.Document.Panels[0].AssetAvailable == nil || *old.Document.Panels[0].AssetAvailable {
		t.Fatal("confirmation changed current visibility", e)
	}
	_, e = a.comicPanelImage(p, project, 1, "dawn")
	requireOperation(t, e, "COMIC_ASSET_UNAVAILABLE")
	old, replay, e = a.saveComic(p, project, in, saveKey)
	if e != nil || !replay || old.Version != 1 {
		t.Fatal("read original save at changed head", e)
	}
	bytes, e := json.Marshal(old)
	if e != nil || strings.Contains(string(bytes), "ReferenceBlob") || strings.Contains(string(bytes), "storage_identity") {
		t.Fatal("private source leaked")
	}
	history, e := a.comicHistory(p, project, 0, 1)
	if e != nil || len(history.Items) != 1 || history.Items[0].Version != 2 || history.Items[0].Confirmed || history.NextBeforeVersion == nil {
		t.Fatal("history head", e)
	}
	history, e = a.comicHistory(p, project, *history.NextBeforeVersion, 1)
	if e != nil || len(history.Items) != 1 || !history.Items[0].Confirmed || history.NextBeforeVersion != nil {
		t.Fatal("historical confirmation", e)
	}
}

func TestPostgresComicCurrentModerationAndFrozenGeneratedOriginal(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	reviewer := newReviewDispatchHTTPFixture(t, "")
	f.cfg.MediaModeration = reviewer.config(t)
	a, server := f.open(t)
	client := authClient(t)
	owner := registerFixture(t, client, server.URL, "comic-moderation@example.test")
	p, err := a.currentPrincipal(operationsRequest(t, client, server))
	if err != nil {
		t.Fatal(err)
	}
	project := owner.Project.ID
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	allowed, _ := acceptMediaExecution(t, a, server, client, owner, "image", "原始场景", "comic-approved-generation", 1)
	allowed = laneTaskStatus(t, a, allowed.ID, "SUCCEEDED")
	if allowed.Moderation == nil || allowed.Moderation.State != "ALLOWED" || len(allowed.Artifacts) != 1 {
		t.Fatal("expected publicly delivered approved panel")
	}
	generated := libraryID("generated", allowed.ID, allowed.Artifacts[0].ID)
	ref, _ := presentationReferenceFixture(t, server, client, project)
	input := comicTestInput(0, generated)
	input.Document.Panels[1].AssetID = ref
	if _, _, err = a.saveComic(p, project, input, newID("save")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.confirmComic(p, project, 1, newID("confirm")); err != nil {
		t.Fatal(err)
	}
	original, err := a.comicPanelImage(p, project, 1, "dawn")
	if err != nil || original.AssetInfo == nil || original.SHA256 != moderationSHA(f.media.image) {
		t.Fatal("generated receipt", err)
	}
	// A locator-only substitution must fail even when the content hash remains
	// the same. The private reference on the other panel remains accessible.
	ctx := context.Background()
	if _, err = admin.Exec(ctx, `UPDATE studio_library_entries SET storage_info=jsonb_set(storage_info,'{object_version}','"different-original"') WHERE id=$1`, generated); err != nil {
		t.Fatal(err)
	}
	_, err = a.comicPanelImage(p, project, 1, "dawn")
	requireOperation(t, err, "COMIC_ASSET_UNAVAILABLE")
	_, err = a.comicVersion(p, project, 1, true)
	requireOperation(t, err, "COMIC_ASSET_UNAVAILABLE")
	if source, err := a.comicPanelImage(p, project, 1, "meeting"); err != nil || source.ReferenceBlob == nil {
		t.Fatal("unrelated panel unavailable", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE studio_library_entries SET storage_info=$2 WHERE id=$1`, generated, original.AssetInfo); err != nil {
		t.Fatal(err)
	}
	if _, err = a.comicPanelImage(p, project, 1, "dawn"); err != nil {
		t.Fatal("restored original identity", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE studio_tasks SET document=jsonb_set(document,'{moderation,state}','"REVIEW"') WHERE id=$1`, allowed.ID); err != nil {
		t.Fatal(err)
	}
	_, err = a.comicPanelImage(p, project, 1, "dawn")
	requireOperation(t, err, "COMIC_ASSET_UNAVAILABLE")
	reviewer.mu.Lock()
	reviewer.blockSHA = moderationSHA(f.media.image)
	reviewer.mu.Unlock()
	blocked, _ := acceptMediaExecution(t, a, server, client, owner, "image", "隔离场景", "comic-isolated-generation", 1)
	blocked = laneTaskStatus(t, a, blocked.ID, "FAILED")
	if blocked.Moderation == nil || blocked.Moderation.State != "BLOCKED" || len(blocked.Artifacts) != 0 {
		t.Fatal("expected blocked unpublished candidate")
	}
	private, err := loadTaskMediaModerationSQL(ctx, admin, blocked.ID)
	if err != nil || private == nil || len(private.Candidates) != 1 {
		t.Fatal("missing private candidate", err)
	}
	invalid := comicTestInput(1, libraryID("generated", blocked.ID, private.Candidates[0].ID))
	attempt := newID("blocked")
	_, _, err = a.saveComic(p, project, invalid, attempt)
	requireOperation(t, err, "COMIC_ASSET_UNAVAILABLE")
	_, err = a.comicReceipt(p, project, "save", attempt)
	requireOperation(t, err, "COMIC_NOT_FOUND")
}
