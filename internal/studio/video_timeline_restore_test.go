package studio

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func TestPostgresVideoTimelineRestoreOldVersionAndOriginals(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("TEST_POSTGRES_BIN_DIR required for real pg_dump and pg_restore")
	}
	f := newWorkflowIntegrationFixture(t)
	app, server := f.open(t)
	client := authClient(t)
	const email = "timeline-backup@example.test"
	owner := registerFixture(t, client, server.URL, email)
	project := owner.Project.ID
	api := "/api/v1/projects/" + project + "/video-timelines"
	firstBytes := bytes.Clone(f.media.video)
	_, firstAsset := timelineTestSource(t, app, server, client, owner)
	secondBytes, _ := mediaBytesFixture(t, "review.mp4")
	if bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("restore fixture needs independent original bytes")
	}
	f.media.mu.Lock()
	f.media.video = secondBytes
	f.media.mu.Unlock()
	// Publication of each fixture goes through the configured HTTP reviewer.
	// The fixture filename has no effect on that decision.
	_, secondAsset := timelineTestSource(t, app, server, client, owner)
	first := timelineTestInput(firstAsset, secondAsset)
	const firstKey = "timeline-backup-original"
	body, _ := presentationRequest(t, client, server.URL, project, "POST", api, firstKey, first, 201)
	original := decode[VideoTimelineVersion](t, body)
	next := timelineTestInput(secondAsset)
	next.TimelineID, next.ExpectedVersion = original.ID, &original.Version
	next.Document.Title = "缩短新版"
	next.Document.Clips[0].InMS, next.Document.Clips[0].OutMS = 0, 125
	body, _ = presentationRequest(t, client, server.URL, project, "POST", api, "timeline-backup-latest", next, 201)
	latest := decode[VideoTimelineVersion](t, body)
	server.Close()
	app.Close()
	sourceDB, sourceAssets, sourceRefs := f.cfg.DatabaseURL, f.cfg.AssetDir, f.cfg.ReferenceDir
	restoreWorkflowFixture(t, f, "studio_video_timelines", "studio_video_timeline_versions", "studio_video_timeline_clips", "studio_video_timeline_keys", "studio_tasks", "studio_library_revisions", "studio_task_media_moderation", "studio_media_delivery_items")
	if f.cfg.DatabaseURL == sourceDB || f.cfg.AssetDir == sourceAssets || f.cfg.ReferenceDir == sourceRefs {
		t.Fatal("restore reused a live source database or directory")
	}
	_, restored := f.open(t)
	client = authClient(t)
	presentationRequest(t, client, restored.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	base := api + "/" + original.ID + "/versions"
	for _, tc := range []struct {
		path, key string
		want      VideoTimelineVersion
	}{
		{api + "/receipt", firstKey, original},
		{base + "/1", "", original},
		{base + "/2", "", latest},
	} {
		raw, _ := presentationRequest(t, client, restored.URL, project, "GET", tc.path, tc.key, nil, 200)
		if got := decode[VideoTimelineVersion](t, raw); !reflect.DeepEqual(got, tc.want) {
			t.Fatal("restored revision differs in original ID, range, order or content hash")
		}
	}
	for _, tc := range []struct {
		path string
		want []byte
	}{
		{"/1/clips/shot1/source", firstBytes},
		{"/1/clips/shot2/source", secondBytes},
		{"/2/clips/shot1/source", secondBytes},
	} {
		raw, headers := presentationRequest(t, client, restored.URL, project, "GET", base+tc.path, "", nil, 200)
		if headers.Get("Content-Type") != "video/mp4" || !bytes.Equal(raw, tc.want) {
			t.Fatal("restored old clip path did not return its complete original video")
		}
	}
	presentationRequest(t, client, restored.URL, project, "GET", base+"/2/clips/shot2/source", "", nil, 404)
	f.media.mu.Lock()
	submits := f.media.count
	f.media.mu.Unlock()
	if submits != 2 {
		t.Fatal("restore or reading fixed revisions caused a new generation")
	}
}
