package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func timelineTestInput(assets ...string) VideoTimelineSaveInput {
	n := uint64(0)
	in := VideoTimelineSaveInput{ExpectedVersion: &n, Document: VideoTimelineDocument{Title: "初剪"}}
	for i, id := range assets {
		in.Document.Clips = append(in.Document.Clips, VideoTimelineClip{ID: fmt.Sprintf("shot%d", i+1), Title: "取景", AssetID: id, AssetRevision: 1, InMS: 100, OutMS: 350})
	}
	return in
}

func TestVideoTimelineStoreBounds(t *testing.T) {
	asset := "lib_" + strings.Repeat("a", 64)
	in := timelineTestInput()
	for i := 0; i < 8; i++ {
		in.Document.Clips = append(in.Document.Clips, VideoTimelineClip{ID: fmt.Sprintf("s%d", i), Title: "镜头", AssetID: asset, AssetRevision: 1, InMS: 296250, OutMS: 300000})
	}
	if e := validateVideoTimeline(in); e != nil {
		t.Fatal("8 shots and 30000 ms", e)
	}
	in.Document.Clips[0].InMS--
	requireOperation(t, validateVideoTimeline(in), "VIDEO_TIMELINE_DURATION_LIMIT")
	yes := true
	for _, change := range []func(*VideoTimelineSaveInput){
		func(x *VideoTimelineSaveInput) { x.Document.Clips = nil },
		func(x *VideoTimelineSaveInput) { x.Document.Clips = append(x.Document.Clips, x.Document.Clips[0]) },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].AssetID = "https://example.test/video.mp4" },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].AssetRevision = 0 },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].ID = "../outside" },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].Title = "\u0000" },
		func(x *VideoTimelineSaveInput) { x.Document.Title = strings.Repeat("片", 121) },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].InMS = -1 },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].OutMS = 300001 },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].OutMS = x.Document.Clips[0].InMS },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].OutMS = x.Document.Clips[0].InMS + 20 },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].ContentVersion = strings.Repeat("f", 64) },
		func(x *VideoTimelineSaveInput) { x.Document.Clips[0].AssetAvailable = &yes },
		func(x *VideoTimelineSaveInput) { x.ExpectedVersion = nil },
	} {
		draft := timelineTestInput(asset)
		change(&draft)
		requireOperation(t, validateVideoTimeline(draft), "INVALID_VIDEO_TIMELINE")
	}
	draft := timelineTestInput(asset, asset)
	draft.Document.Clips[0].OutMS = 121 // end at frame one
	draft.Document.Clips[1].OutMS = 140 // cumulative 61ms still frame one
	requireOperation(t, validateVideoTimeline(draft), "INVALID_VIDEO_TIMELINE")
}

func timelineTestSource(t *testing.T, a *App, s *httptest.Server, c *http.Client, owner testIdentity) (Task, string) {
	t.Helper()
	task, _ := acceptMediaExecution(t, a, s, c, owner, "video", "Original video", newID("make"), 1)
	task = laneTaskStatus(t, a, task.ID, "SUCCEEDED")
	if len(task.Artifacts) != 1 || task.Moderation == nil || task.Moderation.State != "ALLOWED" {
		t.Fatal("expected approved private original")
	}
	return task, libraryID("generated", task.ID, task.Artifacts[0].ID)
}

func TestPostgresVideoTimelineFrozenOrderedSourcesAndScope(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "video-timeline-scope@example.test")
	p, e := a.currentPrincipal(operationsRequest(t, c, s))
	if e != nil {
		t.Fatal(e)
	}
	project := owner.Project.ID
	t1, asset1 := timelineTestSource(t, a, s, c, owner)
	t2, asset2 := timelineTestSource(t, a, s, c, owner)
	first := timelineTestInput(asset1, asset2)
	key := newID("save")
	v, replay, e := a.saveVideoTimeline(p, project, first, key)
	if e != nil || replay || v.Version != 1 || !validOperationsID(v.ID, "vtl") {
		t.Fatal(v, e)
	}
	clip := v.Document.Clips[0]
	if clip.AssetAvailable == nil || !*clip.AssetAvailable || clip.ContentVersion != moderationSHA(f.media.video) || clip.AssetRevision != 1 {
		t.Fatal("missing frozen approval", clip)
	}
	raw, e := json.Marshal(v)
	if e != nil || strings.Contains(string(raw), "object_key") || strings.Contains(string(raw), "storage_identity") || strings.Contains(string(raw), "delivery") {
		t.Fatal("private source information in public snapshot")
	}
	second := timelineTestInput(asset1, asset2)
	second.TimelineID = v.ID
	second.ExpectedVersion = &v.Version
	second.Document.Clips[0], second.Document.Clips[1] = second.Document.Clips[1], second.Document.Clips[0]
	second.Document.Title = "调换镜头"
	if _, _, e = a.saveVideoTimeline(p, project, second, newID("save")); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		version uint64
		ids     []string
	}{{1, []string{t1.Artifacts[0].ID, t2.Artifacts[0].ID}}, {2, []string{t2.Artifacts[0].ID, t1.Artifacts[0].ID}}} {
		got, src, e := a.videoTimelineSources(p, project, v.ID, tc.version)
		if e != nil || len(src) != 2 || src[0].ID != tc.ids[0] || src[1].ID != tc.ids[1] || got.Version != tc.version {
			t.Fatal("frozen source ordering", got, e)
		}
	}
	page, e := a.videoTimelineHistory(p, project, v.ID, 0, 1)
	if e != nil || len(page.Items) != 1 || page.Items[0].Version != 2 || page.NextBeforeVersion == nil {
		t.Fatal(page, e)
	}
	page, e = a.videoTimelineHistory(p, project, v.ID, *page.NextBeforeVersion, 1)
	if e != nil || len(page.Items) != 1 || page.Items[0].Version != 1 || page.NextBeforeVersion != nil {
		t.Fatal(page, e)
	}
	otherC := authClient(t)
	other := registerFixture(t, otherC, s.URL, "video-timeline-foreign@example.test")
	otherP, e := a.currentPrincipal(operationsRequest(t, otherC, s))
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = a.saveVideoTimeline(otherP, other.Project.ID, first, key)
	requireOperation(t, e, "VIDEO_TIMELINE_ASSET_UNAVAILABLE")
	_, e = a.videoTimelineVersion(otherP, project, v.ID, 1, false)
	requireOperation(t, e, "VIDEO_TIMELINE_NOT_FOUND")
	_, e = a.videoTimelineSource(otherP, other.Project.ID, v.ID, 1, "shot1")
	requireOperation(t, e, "VIDEO_TIMELINE_NOT_FOUND")
	_, e = a.videoTimelineReceipt(otherP, other.Project.ID, key)
	requireOperation(t, e, "VIDEO_TIMELINE_NOT_FOUND")
	code, _, _ := authRequest(t, c, s.URL, "POST", "/api/v1/library/"+asset1+"/trash", newID("trash"), project, stateInput(1))
	if code != 200 {
		t.Fatal("trash", code)
	}
	got, replay, e := a.saveVideoTimeline(p, project, first, key)
	if e != nil || !replay || got.Version != 1 || *got.Document.Clips[0].AssetAvailable || !*got.Document.Clips[1].AssetAvailable {
		t.Fatal("original key while trashed", got, e)
	}
	got, e = a.videoTimelineReceipt(p, project, key)
	if e != nil || got.Version != 1 || *got.Document.Clips[0].AssetAvailable {
		t.Fatal("receipt while trashed", e)
	}
	changed := timelineTestInput(asset2, asset1)
	_, _, e = a.saveVideoTimeline(p, project, changed, key)
	requireOperation(t, e, "IDEMPOTENCY_CONFLICT")
	_, _, e = a.videoTimelineSources(p, project, v.ID, 1)
	requireOperation(t, e, "VIDEO_TIMELINE_ASSET_UNAVAILABLE")
	_, e = a.videoTimelineSource(p, project, v.ID, 1, "shot1")
	requireOperation(t, e, "VIDEO_TIMELINE_ASSET_UNAVAILABLE")
	src, e := a.videoTimelineSource(p, project, v.ID, 1, "shot2")
	if e != nil || src.ID != t2.Artifacts[0].ID {
		t.Fatal("sibling visibility", e)
	}
	code, _, _ = authRequest(t, c, s.URL, "POST", "/api/v1/library/"+asset1+"/restore", newID("restore"), project, stateInput(2))
	if code != 200 {
		t.Fatal("restore", code)
	}
	if _, _, e = a.videoTimelineSources(p, project, v.ID, 1); e != nil {
		t.Fatal("same restored original", e)
	}
	_, _, e = a.saveVideoTimeline(p, project, first, newID("save"))
	requireOperation(t, e, "VIDEO_TIMELINE_ASSET_UNAVAILABLE")
	admin := deliveryAdmin(t, f.cfg.DatabaseURL)
	// Failure injections change only this isolated fixture's current locator and
	// current decision. The already committed version must keep both originals.
	if _, e = admin.Exec(context.Background(), `UPDATE studio_library_entries SET storage_info=jsonb_set(storage_info,'{object_version}','"replacement"') WHERE id=$1`, asset1); e != nil {
		t.Fatal(e)
	}
	_, e = a.videoTimelineSource(p, project, v.ID, 1, "shot1")
	requireOperation(t, e, "VIDEO_TIMELINE_ASSET_UNAVAILABLE")
	if _, e = admin.Exec(context.Background(), `UPDATE studio_tasks SET document=jsonb_set(document,'{moderation,state}','"REVIEW"') WHERE id=$1`, t2.ID); e != nil {
		t.Fatal(e)
	}
	_, e = a.videoTimelineSource(p, project, v.ID, 1, "shot2")
	requireOperation(t, e, "VIDEO_TIMELINE_ASSET_UNAVAILABLE")
	if _, e = admin.Exec(context.Background(), `INSERT INTO studio_video_timeline_clips SELECT timeline_id,version,project_id,workspace_id,2,'appended',title,in_ms,out_ms,asset_id,asset_revision,content_version,asset_info FROM studio_video_timeline_clips WHERE timeline_id=$1 AND version=1 AND position=0`, v.ID); e == nil {
		t.Fatal("late addition to immutable version")
	}
}
