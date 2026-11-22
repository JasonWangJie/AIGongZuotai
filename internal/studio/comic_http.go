package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *App) registerComicRoutes(mux *http.ServeMux) {
	const base = "/api/v1/projects/{project}/comic"
	const version = base + "/versions/{version}"
	register := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if a.store == nil || a.config.AuthMode != "required" {
				writeError(w, failure(501, "COMIC_STORAGE_REQUIRED", "项目漫剧需要 PostgreSQL 和账户认证"))
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
			handler(w, r)
		})
	}
	register("GET "+base, a.comicReadHTTP)
	register("POST "+base, a.comicSaveHTTP)
	register("GET "+base+"/receipt", a.comicReceiptHTTP)
	register("GET "+base+"/versions", a.comicHistoryHTTP)
	register("GET "+version, a.comicReadHTTP)
	register("POST "+version+"/confirm", a.comicConfirmHTTP)
	register("GET "+version+"/export", a.comicExportHTTP)
	register("GET "+version+"/panels/{panel}/image", a.comicImageHTTP)
	const videos = version + "/panels/{panel}/videos"
	register("POST "+videos+"/quote", a.quotePanelVideoHTTP)
	register("POST "+videos, a.acceptPanelVideoHTTP)
	register("GET "+videos+"/receipt", a.panelVideoReceiptHTTP)
	register("GET "+videos, a.listPanelVideosHTTP)
}

// Omit server-only approval and source evidence from the accepted JSON schema;
// even a JSON null in one of these fields must be rejected.
func comicWriteInput(w http.ResponseWriter, r *http.Request) (ComicSaveInput, error) {
	var out ComicSaveInput
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return out, failure(415, "JSON_REQUIRED", "请使用 application/json")
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return out, failure(413, "COMIC_BODY_TOO_LARGE", "漫剧请求体上限为 2 MiB")
	}
	if err != nil || !utf8.Valid(data) {
		return out, failure(400, "INVALID_JSON", "请提交有效 UTF-8 漫剧稿")
	}
	var in struct {
		Expected *uint64 `json:"expected_version"`
		Document struct {
			Title      string      `json:"title"`
			Story      string      `json:"story"`
			Characters []ComicCard `json:"characters"`
			Scenes     []ComicCard `json:"scenes"`
			Panels     []struct {
				ID            string   `json:"id"`
				Title         string   `json:"title"`
				SceneID       string   `json:"scene_id"`
				CharacterIDs  []string `json:"character_ids"`
				Action        string   `json:"action"`
				Dialogue      string   `json:"dialogue"`
				AssetID       string   `json:"asset_id"`
				AssetRevision uint64   `json:"asset_revision"`
			} `json:"panels"`
		} `json:"document"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		return out, failure(400, "INVALID_JSON", "请提交一份只含可编辑字段的漫剧稿")
	}
	out.ExpectedVersion = in.Expected
	out.Document = ComicDocument{Title: in.Document.Title, Story: in.Document.Story, Characters: in.Document.Characters, Scenes: in.Document.Scenes,
		Panels: make([]ComicPanel, 0, len(in.Document.Panels))}
	for _, p := range in.Document.Panels {
		out.Document.Panels = append(out.Document.Panels, ComicPanel{ID: p.ID, Title: p.Title, SceneID: p.SceneID,
			CharacterIDs: p.CharacterIDs, Action: p.Action, Dialogue: p.Dialogue, AssetID: p.AssetID, AssetRevision: p.AssetRevision})
	}
	return out, nil
}

func comicNumber(s string) (uint64, error) {
	n, err := strconv.ParseUint(s, 10, 63)
	if err != nil || n == 0 {
		return 0, failure(400, "INVALID_COMIC_VERSION", "漫剧版本须为正整数")
	}
	return n, nil
}

func (a *App) comicSaveHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	in, err := comicWriteInput(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, replay, err := a.saveComic(principalFrom(r), r.PathValue("project"), in, key)
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *App) comicReadHTTP(w http.ResponseWriter, r *http.Request) {
	var version uint64
	var err error
	if s := r.PathValue("version"); s != "" {
		version, err = comicNumber(s)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	out, err := a.comicVersion(principalFrom(r), r.PathValue("project"), version, false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) comicConfirmHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	version, err := comicNumber(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1)); err != nil || len(body) != 0 {
		writeError(w, failure(422, "COMIC_CONFIRM_BODY", "确认固定版次时请使用空正文"))
		return
	}
	out, replay, err := a.confirmComic(principalFrom(r), r.PathValue("project"), version, key)
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, out)
}

func (a *App) comicReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	op := r.URL.Query().Get("operation")
	if op != "save" && op != "confirm" {
		writeError(w, failure(400, "INVALID_COMIC_OPERATION", "请指定要核对的保存或确认操作"))
		return
	}
	out, err := a.comicReceipt(principalFrom(r), r.PathValue("project"), op, key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		ComicVersion
		Operation string `json:"operation"`
	}{out, op})
}

func (a *App) comicHistoryHTTP(w http.ResponseWriter, r *http.Request) {
	var before uint64
	limit := 20
	var err error
	if raw := r.URL.Query().Get("before_version"); raw != "" {
		before, err = comicNumber(raw)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			writeError(w, failure(400, "INVALID_COMIC_LIMIT", "分页数量须为 1 至 50"))
			return
		}
	}
	out, err := a.comicHistory(principalFrom(r), r.PathValue("project"), before, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

type comicExportManifest struct {
	Source    string               `json:"source"`
	ProjectID string               `json:"project_id"`
	Version   uint64               `json:"version"`
	CreatedAt time.Time            `json:"created_at"`
	Document  PresentationDocument `json:"document"`
	ImageURLs map[string]string    `json:"image_urls"`
}

func comicDelivery(v ComicVersion) comicExportManifest {
	out := comicExportManifest{Source: "comic", ProjectID: v.ProjectID, Version: v.Version, CreatedAt: v.CreatedAt,
		Document: PresentationDocument{Title: v.Document.Title, Theme: "paper", Slides: make([]PresentationSlide, 0, len(v.Document.Panels))}, ImageURLs: map[string]string{}}
	scenes, cast := map[string]ComicCard{}, map[string]ComicCard{}
	for _, c := range v.Document.Scenes {
		scenes[c.ID] = c
	}
	for _, c := range v.Document.Characters {
		cast[c.ID] = c
	}
	base := "/api/v1/projects/" + url.PathEscape(v.ProjectID) + "/comic/versions/" + strconv.FormatUint(v.Version, 10) + "/panels/"
	for _, panel := range v.Document.Panels {
		scene := scenes[panel.SceneID]
		names := make([]string, 0, len(panel.CharacterIDs))
		notes := []string{"故事梗概：" + v.Document.Story}
		if scene.Description != "" {
			notes = append(notes, "场景设定："+scene.Description)
		}
		for _, id := range panel.CharacterIDs {
			c := cast[id]
			names = append(names, c.Name)
			if c.Description != "" {
				notes = append(notes, "角色设定："+c.Name+"｜"+c.Description)
			}
		}
		display := strings.Join(names, "、")
		if display == "" {
			display = "无"
		}
		body := []string{"场景：" + scene.Name, "角色：" + display, "动作：" + panel.Action}
		if panel.Dialogue != "" {
			body = append(body, "对白："+panel.Dialogue)
		}
		slide := PresentationSlide{ID: panel.ID, Layout: "content", Title: panel.Title, Body: strings.Join(body, "\n"), Notes: strings.Join(notes, "\n")}
		if panel.AssetID != "" {
			slide.Layout = "image"
			slide.AssetID, slide.AssetRevision = panel.AssetID, panel.AssetRevision
			slide.ContentVersion, slide.AssetAvailable = panel.ContentVersion, panel.AssetAvailable
			out.ImageURLs[panel.ID] = base + url.PathEscape(panel.ID) + "/image"
		}
		out.Document.Slides = append(out.Document.Slides, slide)
	}
	return out
}

func (a *App) comicExportHTTP(w http.ResponseWriter, r *http.Request) {
	n, err := comicNumber(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := a.comicVersion(principalFrom(r), r.PathValue("project"), n, true)
	if err != nil {
		writeError(w, err)
		return
	}
	if !v.Confirmed {
		writeError(w, failure(409, "COMIC_NOT_CONFIRMED", "原作者须先确认此版本"))
		return
	}
	writeJSON(w, 200, comicDelivery(v))
}

func (a *App) comicImageHTTP(w http.ResponseWriter, r *http.Request) {
	n, err := comicNumber(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	read := func() (presentationImageSource, error) {
		return a.comicPanelImage(principalFrom(r), r.PathValue("project"), n, r.PathValue("panel"))
	}
	src, err := read()
	if err != nil {
		writeError(w, err)
		return
	}
	busy := func() error { return failure(429, "COMIC_IMAGE_BUSY", "漫剧图片读取繁忙，请稍后重试") }
	unavailable := func() error {
		return failure(409, "COMIC_ASSET_UNAVAILABLE", "原图已不可读取或内容已变化")
	}
	select {
	case presentationImageSlots <- struct{}{}:
		defer func() { <-presentationImageSlots }()
	default:
		writeError(w, busy())
		return
	}
	raw, err := a.presentationImageBytes(r.Context(), src)
	if err != nil {
		var op *operationError
		if errors.As(err, &op) && op.status == 429 {
			writeError(w, busy())
		} else {
			writeError(w, unavailable())
		}
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || "image/"+format != src.ContentType || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > 16_000_000/int64(cfg.Height) {
		writeError(w, unavailable())
		return
	}
	current, err := read()
	if err != nil {
		writeError(w, err)
		return
	}
	if !reflect.DeepEqual(src, current) {
		writeError(w, unavailable())
		return
	}
	if err = r.Context().Err(); err != nil {
		return
	}
	c := http.NewResponseController(w)
	if err = c.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
		writeError(w, failure(503, "COMIC_IMAGE_UNAVAILABLE", "本次图片传输暂不可用"))
		return
	}
	defer c.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", src.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Image-Width", strconv.Itoa(cfg.Width))
	w.Header().Set("X-Image-Height", strconv.Itoa(cfg.Height))
	http.ServeContent(w, r, "comic-panel-image", time.Time{}, bytes.NewReader(raw))
}
