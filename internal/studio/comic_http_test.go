package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func comicHTTPDraft(asset string) ComicSaveInput {
	zero := uint64(0)
	p := ComicPanel{ID: "east", Title: "潮汐", SceneID: "shore", CharacterIDs: []string{"keeper"}, Action: "点亮灯塔", Dialogue: "回来了。"}
	if asset != "" {
		p.AssetID = asset
		p.AssetRevision = 1
	}
	return ComicSaveInput{ExpectedVersion: &zero, Document: ComicDocument{Title: "海边来信", Story: "灯塔收到来信。",
		Characters: []ComicCard{{ID: "keeper", Name: "守塔人", Description: "身穿蓝外套"}},
		Scenes:     []ComicCard{{ID: "shore", Name: "海岸", Description: "月光下的礁石"}},
		Panels:     []ComicPanel{p, {ID: "last", Title: "尾声", SceneID: "shore", CharacterIDs: []string{}, Action: "潮水退去"}}}}
}

func TestComicHTTPAcceptsOnlyEditableFieldsAndPreservesLongText(t *testing.T) {
	d := comicHTTPDraft("")
	d.Document.Panels[0].Action = strings.Repeat("动作。\n", 200)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	read := func(input []byte) (ComicSaveInput, error) {
		r := httptest.NewRequest("POST", "/", bytes.NewReader(input))
		r.Header.Set("Content-Type", "application/json")
		return comicWriteInput(httptest.NewRecorder(), r)
	}
	d, err = read(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"asset_available":false`, `"content_version":null`, `"object_key":"private"`} {
		modified := strings.Replace(string(raw), `"id":"east"`, `"id":"east",`+field, 1)
		if _, err = read([]byte(modified)); err == nil {
			t.Fatal("accepted forbidden field", field)
		}
	}
	if _, err = read(append(append([]byte{}, raw...), raw...)); err == nil {
		t.Fatal("accepted trailing document")
	}
	if _, err = read([]byte{0xff}); err == nil {
		t.Fatal("accepted invalid utf8")
	}
	if _, err = read(bytes.Repeat([]byte{'x'}, (2<<20)+1)); err == nil {
		t.Fatal("accepted oversized request")
	}
	manifest := comicDelivery(ComicVersion{ProjectID: "prj_demo", Version: 1, Document: d.Document, Confirmed: true})
	one := manifest.Document.Slides[0]
	if !strings.Contains(one.Body, d.Document.Panels[0].Action) || !strings.HasSuffix(one.Body, "对白：回来了。") ||
		!strings.Contains(one.Notes, "场景设定：月光下的礁石") || !strings.Contains(one.Notes, "角色设定：守塔人｜身穿蓝外套") {
		t.Fatal("semantic slide lost full editable content")
	}
	if got := manifest.Document.Slides[1].Body; got != "场景：海岸\n角色：无\n动作：潮水退去" {
		t.Fatal("empty cast or dialogue resulted in invented content", got)
	}
}

func TestPostgresComicPrivateExportConfirmationAndRevokedImage(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	_, server := f.open(t)
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "comic-private@example.test")
	project := identity.Project.ID
	asset, picture := presentationReferenceFixture(t, server, client, project)
	api := "/api/v1/projects/" + project + "/comic"
	call := func(method, route, key string, body any, status int) ([]byte, http.Header) {
		return presentationRequest(t, client, server.URL, project, method, route, key, body, status)
	}
	data, _ := call("GET", api, "", nil, 404)
	if !bytes.Contains(data, []byte("COMIC_NOT_FOUND")) {
		t.Fatal("missing current draft unexpectedly exposed a version")
	}
	draft := comicHTTPDraft(asset)
	data, _ = call("POST", api, "comic-original-save", draft, 201)
	v := decode[ComicVersion](t, data)
	if v.Version != 1 || v.Confirmed {
		t.Fatal("initial save must still require author review")
	}
	old := api + "/versions/1"
	imagePath := old + "/panels/east/image"
	call("GET", old+"/export", "", nil, 409)
	call("GET", imagePath, "", nil, 409)
	call("POST", old+"/confirm", "comic-confirm-with-body", map[string]any{"approved": true}, 422)
	data, _ = call("POST", old+"/confirm", "comic-original-confirm", nil, 200)
	if !decode[ComicVersion](t, data).Confirmed {
		t.Fatal("author confirmation missing")
	}
	for _, op := range []string{"save", "confirm"} {
		data, _ = call("GET", api+"/receipt?operation="+op, "comic-original-"+op, nil, 200)
		var receipt struct {
			Operation string `json:"operation"`
			Version   uint64 `json:"version"`
		}
		if json.Unmarshal(data, &receipt) != nil || receipt.Operation != op || receipt.Version != 1 {
			t.Fatal("receipt identity differs")
		}
	}
	_, replay := call("POST", old+"/confirm", "comic-original-confirm", nil, 200)
	if replay.Get("Idempotency-Replayed") != "true" {
		t.Fatal("confirmation was executed twice")
	}
	call("GET", api+"/receipt?operation=confirm", "comic-original-save", nil, 404)
	draft.ExpectedVersion = &v.Version
	draft.Document.Title = "下一版"
	draft.Document.Panels[0].Title = "新镜头"
	call("POST", api, "comic-next-save", draft, 201)
	call("POST", old+"/confirm", "comic-late-fresh-key", nil, 409)
	call("GET", api+"/versions/2/export", "", nil, 409)
	data, _ = call("GET", old+"/export", "", nil, 200)
	m := decode[comicExportManifest](t, data)
	hash := sha256.Sum256(picture)
	if m.Source != "comic" || m.Version != 1 || m.ProjectID != project || m.Document.Title != "海边来信" || len(m.Document.Slides) != 2 ||
		m.Document.Slides[0].Title != "潮汐" || m.Document.Slides[0].ContentVersion != hex.EncodeToString(hash[:]) ||
		m.ImageURLs["east"] != imagePath || len(m.ImageURLs) != 1 {
		t.Fatal("old-version manifest mixed sources or versions", m)
	}
	var flat map[string]json.RawMessage
	if json.Unmarshal(data, &flat) != nil || flat["id"] != nil {
		t.Fatal("comic export claimed an unrelated persistent presentation id")
	}
	raw, h := call("GET", imagePath, "", nil, 200)
	if !bytes.Equal(raw, picture) || h.Get("Content-Type") != "image/png" || h.Get("X-Image-Width") == "" || h.Get("Cache-Control") != "private, no-store" {
		t.Fatal("export did not retrieve the original authorized PNG")
	}
	call("GET", old+"/export", "", nil, 200)
	stranger := authClient(t)
	for _, route := range []string{old + "/export", imagePath} {
		presentationRequest(t, stranger, server.URL, "", "GET", route, "", nil, 401)
	}
	other := registerFixture(t, stranger, server.URL, "comic-other@example.test")
	for _, route := range []string{api, old, old + "/export", imagePath, api + "/receipt?operation=confirm"} {
		presentationRequest(t, stranger, server.URL, other.Project.ID, "GET", route, "comic-original-confirm", nil, 404)
	}
	presentationRequest(t, stranger, server.URL, other.Project.ID, "POST", api+"/versions/2/confirm", "comic-other-confirm", nil, 404)
	call("POST", "/api/v1/library/"+asset+"/trash", "comic-trash-source", stateInput(1), 200)
	for _, route := range []string{old + "/export", imagePath} {
		data, h = call("GET", route, "", nil, 409)
		if bytes.Equal(data, picture) || h.Get("Content-Type") == "image/png" {
			t.Fatal("unavailable original escaped")
		}
	}
	data, _ = call("GET", old, "", nil, 200)
	archived := decode[ComicVersion](t, data)
	if !archived.Confirmed || archived.Document.Panels[0].AssetAvailable == nil || *archived.Document.Panels[0].AssetAvailable {
		t.Fatal("archive lost author confirmation or advertised a trashed image")
	}
	call("POST", "/api/v1/library/"+asset+"/restore", "comic-restore-source", stateInput(2), 200)
	data, _ = call("GET", old+"/export", "", nil, 200)
	if decode[comicExportManifest](t, data).Document.Slides[0].ContentVersion != hex.EncodeToString(hash[:]) {
		t.Fatal("restored image differs")
	}
	raw, _ = call("GET", imagePath, "", nil, 200)
	if !bytes.Equal(raw, picture) {
		t.Fatal("restored original changed")
	}
	var decks int
	if err := deliveryAdmin(t, f.cfg.DatabaseURL).QueryRow(context.Background(), "SELECT count(*) FROM studio_presentations WHERE project_id=$1", project).Scan(&decks); err != nil || decks != 0 {
		t.Fatal("export created a presentation or could not be verified", strconv.Itoa(decks), err)
	}
}
