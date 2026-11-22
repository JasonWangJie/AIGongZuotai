package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"
)

func (a *App) registerCanvasRoutes(mux *http.ServeMux) {
	const base = "/api/v1/projects/{project}/canvas"
	const rev = base + "/revisions/{revision}"
	register := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if a.store == nil || a.config.AuthMode != "required" {
				writeError(w, failure(501, "CANVAS_STORAGE_REQUIRED", "项目自由画布需要 PostgreSQL 与账户认证"))
				return
			}
			p := principalFrom(r)
			if p == nil || p.User == nil {
				writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
				return
			}
			if !resourceAllowed(r, p.Workspace.ID, r.PathValue("project")) {
				writeError(w, canvasMissing())
				return
			}
			h(w, r)
		})
	}
	register("PUT "+base, a.saveCanvasHTTP)
	register("GET "+base, a.readCanvasHTTP)
	register("GET "+base+"/receipt", a.readCanvasReceiptHTTP)
	register("GET "+base+"/revisions", a.listCanvasHTTP)
	register("GET "+rev, a.readCanvasHTTP)
	register("GET "+rev+"/sources", a.canvasSourcesHTTP)
	register("GET "+rev+"/nodes/{node}/thumbnail", a.canvasThumbHTTP)
}

func canvasUint(s string, max uint64) (uint64, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n < 1 || n > max || strconv.FormatUint(n, 10) != s {
		return 0, failure(400, "INVALID_CANVAS_PAGE", "请提供有效画布修订或分页数量")
	}
	return n, nil
}
func canvasDecode(w http.ResponseWriter, r *http.Request) (in CanvasSaveInput, err error) {
	mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" {
		return in, failure(415, "JSON_REQUIRED", "请使用 application/json")
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, canvasMaxBytes))
	var large *http.MaxBytesError
	if errors.As(e, &large) {
		return in, failure(413, "CANVAS_BODY_TOO_LARGE", "画布请求上限为 2 MiB")
	}
	if e != nil || !utf8.Valid(data) || validateCatalogJSONKeys(data) != nil {
		return in, failure(400, "INVALID_JSON", "请提交有效且无重复字段的 UTF-8 JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		return in, failure(400, "INVALID_JSON", "画布只接受可编辑字段和已登记来源身份")
	}
	return in, nil
}
func (a *App) saveCanvasHTTP(w http.ResponseWriter, r *http.Request) {
	k, e := idempotencyKey(r)
	if e != nil {
		writeError(w, e)
		return
	}
	in, e := canvasDecode(w, r)
	if e != nil {
		writeError(w, e)
		return
	}
	out, replay, e := a.saveCanvas(principalFrom(r), r.PathValue("project"), k, in)
	if e != nil {
		writeError(w, e)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, out)
}
func (a *App) readCanvasHTTP(w http.ResponseWriter, r *http.Request) {
	var v uint64
	var e error
	if raw := r.PathValue("revision"); raw != "" {
		v, e = canvasUint(raw, canvasMaxRevision)
		if e != nil {
			writeError(w, e)
			return
		}
	}
	out, e := a.readCanvas(principalFrom(r), r.PathValue("project"), v, "")
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) readCanvasReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	k, e := idempotencyKey(r)
	if e != nil {
		writeError(w, e)
		return
	}
	out, e := a.readCanvas(principalFrom(r), r.PathValue("project"), 0, k)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) listCanvasHTTP(w http.ResponseWriter, r *http.Request) {
	before, limit := uint64(0), uint64(20)
	var e error
	if raw := r.URL.Query().Get("before_revision"); raw != "" {
		before, e = canvasUint(raw, canvasMaxRevision)
		if e != nil {
			writeError(w, e)
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = canvasUint(raw, 50)
		if e != nil {
			writeError(w, e)
			return
		}
	}
	out, e := a.canvasHistory(principalFrom(r), r.PathValue("project"), before, int(limit))
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) canvasSourcesHTTP(w http.ResponseWriter, r *http.Request) {
	v, e := canvasUint(r.PathValue("revision"), canvasMaxRevision)
	if e != nil {
		writeError(w, e)
		return
	}
	out, _, e := a.canvasSources(principalFrom(r), r.PathValue("project"), v, "")
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, 200, out)
}

// AssetStorage's HTTP adapter also supports immutable, versioned S3 objects.
// Its full response stays private until size, digest and read access pass.
type canvasThumbBuffer struct {
	header   http.Header
	code     int
	body     bytes.Buffer
	limit    int64
	overflow bool
}

func (b *canvasThumbBuffer) Header() http.Header { return b.header }
func (b *canvasThumbBuffer) WriteHeader(status int) {
	if b.code == 0 {
		b.code = status
	}
}
func (b *canvasThumbBuffer) Write(p []byte) (int, error) {
	if b.code == 0 {
		b.code = 200
	}
	if int64(b.body.Len()+len(p)) > b.limit {
		b.overflow = true
		return 0, io.ErrShortWrite
	}
	return b.body.Write(p)
}

var canvasThumbSlots = make(chan struct{}, 4)

func (a *App) canvasThumbBytes(r *http.Request, src presentationImageSource) ([]byte, error) {
	n, hash := canvasThumbInfo(src)
	if n == 0 {
		return nil, canvasMissing()
	}
	var raw []byte
	if src.ReferenceBlob != nil {
		f, e := a.references.OpenVerified(r.Context(), *src.ReferenceBlob.Thumbnail)
		if e != nil {
			return nil, canvasMissing()
		}
		defer f.Close()
		raw, e = io.ReadAll(io.LimitReader(f, n+1))
		if e != nil {
			return nil, canvasMissing()
		}
	} else {
		probe := r.Clone(r.Context())
		probe.Header = make(http.Header)
		probe.Method = "GET"
		b := canvasThumbBuffer{header: make(http.Header), limit: n}
		a.assets.ServeThumbnail(&b, probe, src.AssetInfo.ID)
		if b.code == 429 || b.code == 503 {
			return nil, failure(503, "CANVAS_THUMB_BUSY", "私有预览存储暂时繁忙")
		}
		if b.code != 200 || b.overflow {
			return nil, canvasMissing()
		}
		raw = b.body.Bytes()
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != n || hex.EncodeToString(sum[:]) != hash {
		return nil, canvasMissing()
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 512 || cfg.Height > 512 {
		return nil, canvasMissing()
	}
	return raw, nil
}
func (a *App) canvasThumbHTTP(w http.ResponseWriter, r *http.Request) {
	v, e := canvasUint(r.PathValue("revision"), canvasMaxRevision)
	if e != nil {
		writeError(w, e)
		return
	}
	node := r.PathValue("node")
	if !presentationSlideID.MatchString(node) {
		writeError(w, canvasMissing())
		return
	}
	select {
	case canvasThumbSlots <- struct{}{}:
		defer func() { <-canvasThumbSlots }()
	default:
		writeError(w, failure(429, "CANVAS_THUMB_BUSY", "画布预览同时读取数量已满"))
		return
	}
	lookup := func() (*presentationImageSource, error) {
		_, s, e := a.canvasSources(principalFrom(r), r.PathValue("project"), v, node)
		return s, e
	}
	source, e := lookup()
	if e != nil {
		writeError(w, e)
		return
	}
	raw, e := a.canvasThumbBytes(r, *source)
	if e != nil {
		writeError(w, e)
		return
	}
	current, e := lookup()
	if e != nil {
		writeError(w, e)
		return
	}
	if !reflect.DeepEqual(source, current) {
		writeError(w, canvasMissing())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, "thumbnail.png", time.Time{}, bytes.NewReader(raw))
}
