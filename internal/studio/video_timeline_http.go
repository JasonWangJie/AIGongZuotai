package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"unicode/utf8"
)

func (a *App) registerVideoTimelineRoutes(mux *http.ServeMux) {
	const base = "/api/v1/projects/{project}/video-timelines"
	const history = base + "/{timeline}/versions"
	const version = history + "/{version}"
	register := func(pattern string, action http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if a.store == nil || a.config.AuthMode != "required" {
				writeError(w, failure(501, "VIDEO_TIMELINE_STORAGE_REQUIRED", "分镜剪辑需要 PostgreSQL 和账户认证"))
				return
			}
			p := principalFrom(r)
			if p == nil || p.User == nil {
				writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
				return
			}
			if !resourceAllowed(r, p.Workspace.ID, r.PathValue("project")) {
				writeError(w, failure(404, "PROJECT_NOT_FOUND", "未找到该项目"))
				return
			}
			action(w, r)
		})
	}
	register("GET "+base, a.videoTimelineListHTTP)
	register("POST "+base, a.videoTimelineSaveHTTP)
	register("GET "+base+"/receipt", a.videoTimelineReceiptHTTP)
	register("GET "+base+"/capabilities", a.videoTimelineCapabilitiesHTTP)
	register("GET "+history, a.videoTimelineHistoryHTTP)
	register("GET "+version, a.videoTimelineVersionHTTP)
	register("GET "+version+"/clips/{clip}/source", a.videoTimelinePreviewHTTP)
	register("POST "+version+"/clips/{clip}/replace-from-panel", a.replacePanelVideoHTTP)
	register("POST "+version+"/export", a.videoTimelineExportHTTP)
}

func videoTimelineInput(w http.ResponseWriter, r *http.Request) (VideoTimelineSaveInput, error) {
	var in VideoTimelineSaveInput
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return in, failure(415, "JSON_REQUIRED", "请使用 application/json")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return in, failure(413, "TIMELINE_BODY_TOO_LARGE", "分镜请求上限为 64 KiB")
	}
	if err != nil || !utf8.Valid(raw) {
		return in, failure(400, "INVALID_JSON", "请提交 UTF-8 JSON")
	}
	// Only editable fields are accepted. Frozen hashes and availability are
	// produced by the server for each registered project asset.
	var body struct {
		TimelineID      string  `json:"timeline_id"`
		ExpectedVersion *uint64 `json:"expected_version"`
		Document        struct {
			Title string `json:"title"`
			Clips []struct {
				ID            string `json:"id"`
				Title         string `json:"title"`
				AssetID       string `json:"asset_id"`
				AssetRevision uint64 `json:"asset_revision"`
				InMS          *int64 `json:"in_ms"`
				OutMS         *int64 `json:"out_ms"`
			} `json:"clips"`
		} `json:"document"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
		return in, failure(400, "INVALID_JSON", "请提交一份只含可编辑字段的分镜")
	}
	in.TimelineID, in.ExpectedVersion, in.Document.Title = body.TimelineID, body.ExpectedVersion, body.Document.Title
	for _, clip := range body.Document.Clips {
		if clip.InMS == nil || clip.OutMS == nil {
			return in, failure(422, "TIMELINE_RANGE_REQUIRED", "每个镜头都需要入点与出点")
		}
		in.Document.Clips = append(in.Document.Clips, VideoTimelineClip{ID: clip.ID, Title: clip.Title,
			AssetID: clip.AssetID, AssetRevision: clip.AssetRevision, InMS: *clip.InMS, OutMS: *clip.OutMS})
	}
	return in, nil
}

func (a *App) videoTimelineCapabilitiesHTTP(w http.ResponseWriter, r *http.Request) {
	// Authenticate the actual project just as for a document read.
	if _, err := a.listVideoTimelines(principalFrom(r), r.PathValue("project"), "", 1); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"editable": true, "export_enabled": a.timelineIO.enabled(), "max_clips": 8, "max_duration_ms": 30000})
}

func (a *App) videoTimelineSaveHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	in, err := videoTimelineInput(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	v, replay, err := a.saveVideoTimeline(principalFrom(r), r.PathValue("project"), in, key)
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, v)
}

func (a *App) videoTimelineReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := a.videoTimelineReceipt(principalFrom(r), r.PathValue("project"), key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *App) videoTimelineListHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := editorPageLimit(r, "INVALID_TIMELINE_LIMIT")
	if err != nil {
		writeError(w, err)
		return
	}
	items, err := a.listVideoTimelines(principalFrom(r), r.PathValue("project"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func timelineVersionNumber(raw string) (uint64, error) {
	v, err := strconv.ParseUint(raw, 10, 63)
	if err != nil || v == 0 || v > 128 {
		return 0, failure(400, "INVALID_TIMELINE_VERSION", "分镜版本应在 1 至 128 之间")
	}
	return v, nil
}

func (a *App) videoTimelineHistoryHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := editorPageLimit(r, "INVALID_TIMELINE_LIMIT")
	if err != nil {
		writeError(w, err)
		return
	}
	var before uint64
	if raw := r.URL.Query().Get("before_version"); raw != "" {
		before, err = timelineVersionNumber(raw)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	items, err := a.videoTimelineHistory(principalFrom(r), r.PathValue("project"), r.PathValue("timeline"), before, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) videoTimelineVersionHTTP(w http.ResponseWriter, r *http.Request) {
	num, err := timelineVersionNumber(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := a.videoTimelineVersion(principalFrom(r), r.PathValue("project"), r.PathValue("timeline"), num, false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
