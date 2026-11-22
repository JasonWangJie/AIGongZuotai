package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"
)

func (a *App) registerPresentationRoutes(mux *http.ServeMux) {
	const base = "/api/v1/projects/{project}/presentations"
	const versions = base + "/{presentation}/versions"
	const version = versions + "/{version}"
	register := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if a.store == nil || a.config.AuthMode != "required" {
				writeError(w, failure(501, "PRESENTATION_STORAGE_REQUIRED", "项目演示文稿需要 PostgreSQL 和账户认证"))
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
			// Every store operation verifies the current account, session and project.
			handler(w, r)
		})
	}
	register("GET "+base, a.listPresentationsHTTP)
	register("POST "+base, a.savePresentationHTTP)
	register("GET "+base+"/receipt", a.presentationReceiptHTTP)
	register("GET "+versions, a.presentationHistoryHTTP)
	register("GET "+version, a.presentationVersionHTTP)
	register("GET "+version+"/export", a.presentationExportHTTP)
	register("GET "+version+"/slides/{slide}/image", a.presentationImageHTTP)
}

// The write DTO intentionally has no output metadata, source URL or object locator.
type presentationWriteSlide struct {
	ID            string `json:"id"`
	Layout        string `json:"layout"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Notes         string `json:"notes"`
	AssetID       string `json:"asset_id"`
	AssetRevision uint64 `json:"asset_revision"`
}

func decodePresentationJSON(w http.ResponseWriter, r *http.Request) (PresentationSaveInput, error) {
	var out PresentationSaveInput
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return out, failure(415, "JSON_REQUIRED", "请使用 application/json")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return out, failure(413, "PRESENTATION_BODY_TOO_LARGE", "演示文稿请求体上限为 2 MiB")
		}
		return out, failure(400, "INVALID_JSON", "无法读取演示文稿")
	}
	if !utf8.Valid(raw) {
		return out, failure(400, "INVALID_JSON", "演示文稿必须使用 UTF-8")
	}
	var in struct {
		PresentationID  string  `json:"presentation_id"`
		ExpectedVersion *uint64 `json:"expected_version"`
		Document        struct {
			Title  string                   `json:"title"`
			Theme  string                   `json:"theme"`
			Slides []presentationWriteSlide `json:"slides"`
		} `json:"document"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil || d.Decode(new(any)) != io.EOF {
		return out, failure(400, "INVALID_JSON", "请提交单份文稿，仅包含可编辑字段")
	}
	out.PresentationID, out.ExpectedVersion = in.PresentationID, in.ExpectedVersion
	out.Document = PresentationDocument{Title: in.Document.Title, Theme: in.Document.Theme, Slides: make([]PresentationSlide, len(in.Document.Slides))}
	for i, s := range in.Document.Slides {
		out.Document.Slides[i] = PresentationSlide{ID: s.ID, Layout: s.Layout, Title: s.Title, Body: s.Body, Notes: s.Notes, AssetID: s.AssetID, AssetRevision: s.AssetRevision}
	}
	return out, nil
}

func (a *App) savePresentationHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	in, err := decodePresentationJSON(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, replayed, err := a.savePresentation(principalFrom(r), r.PathValue("project"), in, key)
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *App) presentationReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := a.presentationReceipt(principalFrom(r), r.PathValue("project"), key)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func presentationLimit(r *http.Request) (int, error) {
	return editorPageLimit(r, "INVALID_PRESENTATION_LIMIT")
}

func editorPageLimit(r *http.Request, errorCode string) (int, error) {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			return 0, failure(400, errorCode, "分页数量须为 1 至 50")
		}
		return n, nil
	}
	return 20, nil
}

func parsePresentationVersion(value string) (uint64, error) {
	v, err := strconv.ParseUint(value, 10, 63)
	if err != nil || v == 0 {
		return 0, failure(400, "INVALID_PRESENTATION_VERSION", "文稿版本必须为正整数")
	}
	return v, nil
}

func (a *App) listPresentationsHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := presentationLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := a.listPresentations(principalFrom(r), r.PathValue("project"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) presentationHistoryHTTP(w http.ResponseWriter, r *http.Request) {
	limit, err := presentationLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var before uint64
	if raw := r.URL.Query().Get("before_version"); raw != "" {
		before, err = parsePresentationVersion(raw)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	out, err := a.presentationHistory(principalFrom(r), r.PathValue("project"), r.PathValue("presentation"), before, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) readPresentationRequest(r *http.Request, requireAssets bool) (PresentationVersion, error) {
	version, err := parsePresentationVersion(r.PathValue("version"))
	if err != nil {
		return PresentationVersion{}, err
	}
	return a.presentationVersion(principalFrom(r), r.PathValue("project"), r.PathValue("presentation"), version, requireAssets)
}

func (a *App) presentationVersionHTTP(w http.ResponseWriter, r *http.Request) {
	out, err := a.readPresentationRequest(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type presentationExportManifest struct {
	PresentationVersion
	ImageURLs map[string]string `json:"image_urls"`
}

func (a *App) presentationExportHTTP(w http.ResponseWriter, r *http.Request) {
	out, err := a.readPresentationRequest(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	base := "/api/v1/projects/" + url.PathEscape(out.ProjectID) + "/presentations/" + url.PathEscape(out.ID) + "/versions/" + strconv.FormatUint(out.Version, 10) + "/slides/"
	images := make(map[string]string)
	for _, slide := range out.Document.Slides {
		if slide.AssetID != "" {
			images[slide.ID] = base + url.PathEscape(slide.ID) + "/image"
		}
	}
	writeJSON(w, http.StatusOK, presentationExportManifest{out, images})
}

// Each export image is capped at 8 MiB. Keep the verified bytes immutable
// through the final authorization check and HTTP transfer.
var presentationImageSlots = make(chan struct{}, 4)

func (a *App) presentationImageBytes(ctx context.Context, src presentationImageSource) ([]byte, error) {
	unavailable := failure(409, "PRESENTATION_IMAGE_UNAVAILABLE", "原图缺失、内容已变化或暂不可读取")
	if src.ByteSize < 1 || src.ByteSize > presentationMaxImageBytes || (src.ContentType != "image/png" && src.ContentType != "image/jpeg") {
		return nil, unavailable
	}
	var f io.ReadCloser
	var err error
	switch {
	case src.ReferenceBlob != nil && src.AssetInfo == nil:
		f, err = a.references.OpenVerified(ctx, *src.ReferenceBlob)
	case src.AssetInfo != nil && src.ReferenceBlob == nil:
		var actual AssetInfo
		f, actual, err = OpenMediaModerationAsset(ctx, a.assets, src.AssetInfo.ID)
		if err == nil && !sameMediaDeliveryReceipt(&actual, src.AssetInfo) {
			f.Close()
			return nil, unavailable
		}
	default:
		return nil, unavailable
	}
	if err != nil {
		var pe *MediaProviderError
		if errors.As(err, &pe) && pe.Code == "ASSET_STORAGE_BUSY" {
			return nil, failure(429, "PRESENTATION_IMAGE_BUSY", "私有图片读取繁忙，请稍后重试")
		}
		return nil, unavailable
	}
	defer f.Close()
	raw := make([]byte, src.ByteSize)
	if _, err = io.ReadFull(f, raw); err != nil {
		return nil, unavailable
	}
	var extra [1]byte
	if _, err = io.ReadFull(f, extra[:]); err != io.EOF {
		return nil, unavailable
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != src.SHA256 || http.DetectContentType(raw) != src.ContentType {
		return nil, unavailable
	}
	return raw, nil
}

func (a *App) presentationImageHTTP(w http.ResponseWriter, r *http.Request) {
	version, err := parsePresentationVersion(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	lookup := func() (presentationImageSource, error) {
		return a.presentationImage(principalFrom(r), r.PathValue("project"), r.PathValue("presentation"), version, r.PathValue("slide"))
	}
	src, err := lookup()
	if err != nil {
		writeError(w, err)
		return
	}
	select {
	case presentationImageSlots <- struct{}{}:
		defer func() { <-presentationImageSlots }()
	default:
		writeError(w, failure(429, "PRESENTATION_IMAGE_BUSY", "演示原图下载繁忙，请稍后重试"))
		return
	}
	raw, err := a.presentationImageBytes(r.Context(), src)
	if err != nil {
		writeError(w, err)
		return
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || "image/"+format != src.ContentType || config.Width < 1 || config.Height < 1 || int64(config.Width) > 16_000_000/int64(config.Height) {
		writeError(w, failure(409, "PRESENTATION_IMAGE_UNAVAILABLE", "原图编码或像素数不适用于演示文稿"))
		return
	}
	current, err := lookup()
	if err != nil {
		writeError(w, err)
		return
	}
	if current.SHA256 != src.SHA256 || current.ContentType != src.ContentType || current.ByteSize != src.ByteSize {
		writeError(w, failure(409, "PRESENTATION_IMAGE_UNAVAILABLE", "原图内容已变化"))
		return
	}
	w.Header().Set("Content-Type", src.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Image-Width", strconv.Itoa(config.Width))
	w.Header().Set("X-Image-Height", strconv.Itoa(config.Height))
	transfer := http.NewResponseController(w)
	_ = transfer.SetWriteDeadline(time.Now().Add(2 * time.Minute))
	defer transfer.SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, "slide-image", time.Time{}, bytes.NewReader(raw))
}
