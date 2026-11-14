package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A complete local protocol fixture: actual HTTP uploads, application ACLs,
// reference storage, AsyncMediaProvider multipart, polling, safe result import
// and private download. It never calls the documented public gateway.
func TestReferenceUploadEditAndPrivateAssetHTTPIntegration(t *testing.T) {
	pngSource, jpegSource := fixturePNG(t), referenceImageBytes(t, "jpeg")
	resultImage := fixturePNG(t)
	var submits, queries, downloads atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == imageEditEndpoint:
			submits.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey || r.Header.Get("Idempotency-Key") == "" || r.Header.Get("Idempotency-Key") == "integration-edit-key" || r.ContentLength < 1 {
				t.Error("edit submit crossed its credential, idempotency or streaming boundary")
			}
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || params["boundary"] == "" {
				t.Error("edit multipart boundary missing")
				w.WriteHeader(400)
				return
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			fields := map[string]string{}
			var images [][]byte
			var filenames, types []string
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				blob, err := io.ReadAll(part)
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if part.FormName() == "image[]" {
					images = append(images, blob)
					filenames = append(filenames, part.FileName())
					types = append(types, part.Header.Get("Content-Type"))
				} else {
					fields[part.FormName()] = string(blob)
				}
			}
			// Input order deliberately differs from upload order.
			if len(images) != 2 || !bytes.Equal(images[0], jpegSource) || !bytes.Equal(images[1], pngSource) || filenames[0] != "reference-1.jpg" || filenames[1] != "reference-2.png" || types[0] != "image/jpeg" || types[1] != "image/png" {
				t.Error("ordered private references did not reach the actual edit adapter intact")
			}
			if len(fields) != 6 || fields["model"] != "fixture-edit-model" || fields["prompt"] != "keep both reference subjects" || fields["n"] != "1" || fields["size"] != "1024x1024" || fields["resolution"] != "2K" || fields["aspect_ratio"] != "1:1" {
				t.Error("edit adapter changed the frozen model or preset")
			}
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"task_id":"edit-http-fixture","query_url":"https://ignored.invalid/query"}`)
		case r.Method == http.MethodGet && r.URL.Path == mediaQueryPrefix+"edit-http-fixture":
			queries.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
				t.Error("query did not retain the original provider credential")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "edit-http-fixture", "media_type": "image", "stage": "completed", "status": "succeeded", "progress": 100, "storage_status": "saved", "billing_status": "settled", "data": []map[string]any{{"url": upstream.URL + "/result.png?signed=fixture", "content_type": "image/png"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/result.png":
			downloads.Add(1)
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.Query().Get("signed") != "fixture" {
				t.Error("result import leaked credentials or dropped its signed query")
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(resultImage)
		default:
			t.Errorf("unexpected fixture upstream operation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	catalog := customCatalogFixture()
	catalog.Models = catalog.Models[1:2]
	catalog.Models[0].UpstreamModel = "fixture-edit-model"
	catalog.Models[0].Capabilities.ImageEditSupported = true
	catalog.Models[0].Capabilities.MaxReferenceImages = 4
	a, server := testApp(t, Config{Mode: "live", ModelCatalogFile: writeCatalogFixture(t, catalog), AsyncImageBaseURL: upstream.URL, AsyncImageAPIKey: fixtureMediaKey, AsyncImageCredentialID: "integration-media-credential", AssetDir: t.TempDir(), ReferenceDir: t.TempDir()})
	provider, ok := a.media.(*AsyncMediaProvider)
	if !ok || provider.bodies != a.references {
		t.Fatal("application did not wire the real media provider to the shared reference store")
	}
	// Only this local fixture relaxes private-address denial for its result HTTP
	// server. There is no corresponding production environment option.
	provider.assets.allowLocal = true
	upload := func(key, filename string, image []byte) ReferenceAsset {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
		header.Set("Content-Type", "application/octet-stream") // Actual decoder determines the MIME.
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(image); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/references", &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Project-ID", "prj_demo")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		blob, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
			t.Fatalf("upload rejected: %d %s", response.StatusCode, blob)
		}
		return decode[ReferenceAsset](t, blob)
	}
	first := upload("integration-reference-png", "original-png.png", pngSource)
	second := upload("integration-reference-jpeg", "original-jpeg.jpg", jpegSource)
	replayedUpload := upload("integration-reference-png", "original-png.png", pngSource)
	if first.ID == second.ID || first.ID != replayedUpload.ID || first.ContentType != "image/png" || second.ContentType != "image/jpeg" || submits.Load() != 0 {
		t.Fatal("reference upload identity/type or non-generating boundary failed")
	}
	input := mediaFixtureInput("image", 1)
	input.ProjectID, input.ModelID, input.Operation = "prj_demo", "custom-image", "edit"
	input.ModelVersion = a.catalog.Entries[input.ModelID].Model.Version
	input.Inputs.Prompt = "keep both reference subjects"
	input.Inputs.ReferenceAssetIDs = []string{second.ID, first.ID}
	input.Parameters = TaskParameters{Count: 1, AspectRatio: "1:1", Size: "1024x1024", Resolution: "2K"}
	code, body, _ := request(t, server, http.MethodPost, "/api/v1/tasks", "integration-edit-key", input)
	if code != http.StatusAccepted {
		t.Fatalf("edit acceptance failed: %d %s", code, body)
	}
	task := decode[Task](t, body)
	if task.Operation != "edit" || len(task.ReferenceAssetIDs) != 2 || task.ReferenceAssetIDs[0] != second.ID {
		t.Fatal("accepted task lost ordered reference snapshot")
	}
	deadline := time.Now().Add(10 * time.Second)
	for !terminal(task.Status) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		code, body, _ = request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID, "", nil)
		if code != 200 {
			t.Fatalf("task polling failed: %d %s", code, body)
		}
		task = decode[Task](t, body)
	}
	if task.Status != "SUCCEEDED" || len(task.Artifacts) != 1 || submits.Load() != 1 || queries.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("edit pipeline incomplete: status=%s submits=%d queries=%d imports=%d error=%+v", task.Status, submits.Load(), queries.Load(), downloads.Load(), task.Error)
	}
	if !strings.HasPrefix(task.Artifacts[0].URL, "/api/v1/assets/") || bytes.Contains(body, []byte(fixtureMediaKey)) || bytes.Contains(body, []byte(upstream.URL)) {
		t.Fatal("task result leaked external credentials or URL")
	}
	a.mu.Lock()
	snapshot := a.tasks[task.ID].Submission
	a.projects["prj_other_fixture"] = Project{ID: "prj_other_fixture", WorkspaceID: "ws_demo", Name: "Other fixture project", CreatedAt: time.Now()}
	a.mu.Unlock()
	if snapshot == nil || snapshot.BodyFileID == "" || len(snapshot.Body) != 0 {
		t.Fatal("HTTP task did not freeze a file-backed edit submission")
	}
	for _, project := range []string{"prj_demo", "prj_other_fixture"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+task.Artifacts[0].URL, nil)
		req.Header.Set("X-Project-ID", project)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		asset, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if project == "prj_demo" {
			if response.StatusCode != 200 || !bytes.Equal(asset, resultImage) || response.Header.Get("Content-Type") != "image/png" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("authorized private asset download did not return imported image")
			}
		} else if response.StatusCode != 404 {
			t.Fatal("asset crossed explicit project scope", response.StatusCode)
		}
	}
	code, body, replayHeaders := request(t, server, http.MethodPost, "/api/v1/tasks", "integration-edit-key", input)
	replayedTask := decode[Task](t, body)
	if code != http.StatusAccepted || replayHeaders.Get("Idempotency-Replayed") != "true" || replayedTask.ID != task.ID || submits.Load() != 1 {
		t.Fatalf("original task key did not replay safely: status=%d submits=%d", code, submits.Load())
	}
}
