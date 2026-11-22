package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"aigongzuotai/internal/videoexport"
)

type videoTimelineIO struct {
	executor          *videoexport.Executor
	dir               string
	lease             *StorageLease
	ctx               context.Context
	cancel            context.CancelFunc
	previews, exports chan struct{}
	mu                sync.Mutex
	closing           bool
	wg                sync.WaitGroup
	once              sync.Once
}

func videoTempClean(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := f.ReadDir(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != storageLeaseName || !entry.Type().IsRegular() {
			return errors.New("private video export directory must be empty; retain and inspect leftover files")
		}
	}
	return nil
}

func newVideoTimelineIO(c Config) (*videoTimelineIO, error) {
	v := &videoTimelineIO{previews: make(chan struct{}, 2), exports: make(chan struct{}, 1)}
	v.ctx, v.cancel = context.WithCancel(context.Background())
	cfg := c.VideoExport
	if cfg.FFmpegPath == "" && cfg.FFprobePath == "" {
		return v, nil
	}
	if runtime.GOOS != "windows" || cfg.FFmpegPath == "" || cfg.FFprobePath == "" || cfg.TempDir == "" || !filepath.IsAbs(cfg.TempDir) || c.AuthMode != "required" || c.DatabaseURL == "" {
		v.close()
		return nil, errors.New("video export requires Windows, both local tools, a private temporary directory, PostgreSQL and authenticated accounts")
	}
	assetDir := c.AssetDir
	if assetDir == "" {
		assetDir = ".runtime/assets"
	}
	for _, existing := range []string{c.WebDir, c.ReferenceDir, assetDir} {
		if err := validateAssetSeparation(existing, cfg.TempDir); err != nil {
			v.close()
			return nil, errors.New("video export storage must be separate from the web, references and assets")
		}
	}
	resolved, err := resolveConfigPath(cfg.TempDir)
	if err != nil || !strings.EqualFold(filepath.Clean(cfg.TempDir), resolved) {
		v.close()
		return nil, errors.New("invalid private video export directory")
	}
	v.dir = resolved
	if err = os.MkdirAll(v.dir, 0700); err == nil {
		err = videoTempClean(v.dir)
	}
	if err == nil {
		v.lease, err = AcquireStorageLease(v.dir)
	}
	if err == nil {
		err = videoTempClean(v.dir)
	}
	if err == nil {
		v.executor, err = videoexport.New(cfg)
	}
	if err != nil {
		v.close()
		return nil, errors.New("video export tools or private storage unavailable")
	}
	return v, nil
}

func (v *videoTimelineIO) enabled() bool { return v != nil && v.executor != nil }
func (v *videoTimelineIO) close() {
	if v == nil {
		return
	}
	v.once.Do(func() {
		v.mu.Lock()
		v.closing = true
		v.cancel()
		v.mu.Unlock()
		v.wg.Wait()
		_ = v.lease.Close()
	})
}

func (v *videoTimelineIO) begin(parent context.Context, export bool) (context.Context, func(), error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closing {
		return nil, nil, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	slot := v.previews
	if export {
		slot = v.exports
	}
	select {
	case slot <- struct{}{}:
	default:
		return nil, nil, failure(429, "VIDEO_TIMELINE_BUSY", "视频处理繁忙，请稍后重试")
	}
	v.wg.Add(1)
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	unlink := context.AfterFunc(v.ctx, cancel)
	return ctx, func() { unlink(); cancel(); <-slot; v.wg.Done() }, nil
}

func videoReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return failure(503, "VIDEO_TIMELINE_INTERRUPTED", "视频处理已中断")
	}
	var provider *MediaProviderError
	if errors.As(err, &provider) && provider.Code == "ASSET_STORAGE_BUSY" {
		return failure(429, "VIDEO_TIMELINE_BUSY", "原片读取繁忙，请稍后重试")
	}
	return videoTimelineAssetUnavailable()
}

func (a *App) copyTimelineSource(ctx context.Context, src AssetInfo, dst io.Writer) error {
	if src.ContentType != "video/mp4" || src.ByteSize < 1 || src.ByteSize > videoTimelineSourceLimit || !mediaContentHash.MatchString(src.SHA256) || a.assets == nil {
		return videoTimelineAssetUnavailable()
	}
	stat, err := a.assets.Stat(ctx, src.ID)
	if err != nil {
		return videoReadError(err)
	}
	if !sameMediaDeliveryReceipt(&stat, &src) {
		return videoTimelineAssetUnavailable()
	}
	original, actual, err := OpenMediaModerationAsset(ctx, a.assets, src.ID)
	if err != nil {
		return videoReadError(err)
	}
	defer original.Close()
	if !sameMediaDeliveryReceipt(&actual, &src) {
		return videoTimelineAssetUnavailable()
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(original, src.ByteSize+1))
	if err != nil {
		return videoReadError(err)
	}
	if n != src.ByteSize || hex.EncodeToString(hash.Sum(nil)) != src.SHA256 {
		return videoTimelineAssetUnavailable()
	}
	return nil
}

func timelineTransfer(ctx context.Context, w http.ResponseWriter) (func(), error) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
		return nil, failure(503, "VIDEO_TIMELINE_TRANSFER_UNAVAILABLE", "视频传输暂不可用")
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(finished); _ = controller.SetWriteDeadline(time.Now()) })
	return func() {
		if !stop() {
			<-finished
		}
		_ = controller.SetWriteDeadline(time.Time{})
	}, nil
}

func (a *App) videoTimelinePreviewHTTP(w http.ResponseWriter, r *http.Request) {
	version, err := timelineVersionNumber(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	read := func() (AssetInfo, error) {
		return a.videoTimelineSource(principalFrom(r), r.PathValue("project"), r.PathValue("timeline"), version, r.PathValue("clip"))
	}
	src, err := read()
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, end, err := a.timelineIO.begin(r.Context(), false)
	if err != nil {
		writeError(w, err)
		return
	}
	defer end()
	if src.ByteSize < 1 || src.ByteSize > videoTimelineSourceLimit {
		writeError(w, videoTimelineAssetUnavailable())
		return
	}
	var raw bytes.Buffer
	raw.Grow(int(src.ByteSize))
	if err = a.copyTimelineSource(ctx, src, &raw); err != nil {
		writeError(w, err)
		return
	}
	current, err := read()
	if err != nil {
		writeError(w, err)
		return
	}
	if !sameMediaDeliveryReceipt(&current, &src) {
		writeError(w, videoTimelineAssetUnavailable())
		return
	}
	if ctx.Err() != nil {
		writeError(w, videoReadError(ctx.Err()))
		return
	}
	release, err := timelineTransfer(ctx, w)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "source.mp4", time.Time{}, bytes.NewReader(raw.Bytes()))
}

func timelineExportError(err error) error {
	switch {
	case errors.Is(err, videoexport.ErrBusy):
		return failure(429, "VIDEO_TIMELINE_BUSY", "成片导出繁忙，请稍后重试")
	case errors.Is(err, videoexport.ErrInput), errors.Is(err, videoexport.ErrCodec):
		return failure(422, "VIDEO_TIMELINE_CLIP_INVALID", "原片编码、时长或剪辑范围不适用于本次导出")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return videoReadError(err)
	default:
		return failure(503, "VIDEO_TIMELINE_EXPORT_UNAVAILABLE", "本次成片未通过完整核验")
	}
}

func (a *App) videoTimelineExportHTTP(w http.ResponseWriter, r *http.Request) {
	version, err := timelineVersionNumber(r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.ContentLength != 0 {
		writeError(w, failure(422, "VIDEO_TIMELINE_EXPORT_BODY", "请用空正文导出指定版本"))
		return
	}
	read := func() (VideoTimelineVersion, []AssetInfo, error) {
		return a.videoTimelineSources(principalFrom(r), r.PathValue("project"), r.PathValue("timeline"), version)
	}
	v, sources, err := read()
	if err != nil {
		writeError(w, err)
		return
	}
	handler := a.timelineIO
	if !handler.enabled() {
		writeError(w, failure(501, "VIDEO_TIMELINE_EXPORT_DISABLED", "此服务未启用本地成片导出"))
		return
	}
	ctx, end, err := handler.begin(r.Context(), true)
	if err != nil {
		writeError(w, err)
		return
	}
	defer end()
	if len(sources) == 0 || len(sources) > videoexport.MaxClips || len(sources) != len(v.Document.Clips) {
		writeError(w, videoTimelineAssetUnavailable())
		return
	}
	var total int64
	for _, s := range sources {
		if s.ByteSize < 1 || s.ByteSize > videoTimelineSourceLimit || s.ByteSize > videoTimelineTotalLimit-total {
			writeError(w, videoTimelineAssetUnavailable())
			return
		}
		total += s.ByteSize
	}
	if err = videoTempClean(handler.dir); err != nil {
		writeError(w, timelineExportError(err))
		return
	}
	dir, err := os.MkdirTemp(handler.dir, "timeline-")
	if err != nil {
		writeError(w, timelineExportError(err))
		return
	}
	defer os.RemoveAll(dir)
	clips := make([]videoexport.Clip, 0, len(sources))
	for i, s := range sources {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("clip-%d.mp4", i)), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			writeError(w, timelineExportError(err))
			return
		}
		defer f.Close()
		if err = a.copyTimelineSource(ctx, s, f); err != nil {
			writeError(w, err)
			return
		}
		c := v.Document.Clips[i]
		clips = append(clips, videoexport.Clip{Source: f, ExpectedSHA256: c.ContentVersion, ByteSize: s.ByteSize, StartMS: c.InMS, EndMS: c.OutMS})
	}
	out, err := os.OpenFile(filepath.Join(dir, "output.mp4"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		writeError(w, timelineExportError(err))
		return
	}
	defer out.Close()
	result, err := handler.executor.Export(ctx, clips, out)
	if err != nil {
		writeError(w, timelineExportError(err))
		return
	}
	_, now, err := read()
	if err != nil {
		writeError(w, err)
		return
	}
	if len(now) != len(sources) {
		writeError(w, videoTimelineAssetUnavailable())
		return
	}
	for i := range sources {
		if !sameMediaDeliveryReceipt(&now[i], &sources[i]) {
			writeError(w, videoTimelineAssetUnavailable())
			return
		}
	}
	if ctx.Err() != nil {
		writeError(w, videoReadError(ctx.Err()))
		return
	}
	release, err := timelineTransfer(ctx, w)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"timeline-v%d.mp4\"", version))
	w.Header().Set("Content-Length", strconv.FormatInt(result.ByteSize, 10))
	w.Header().Set("X-Content-SHA256", result.SHA256)
	w.Header().Set("X-Video-Duration-Ms", strconv.FormatInt(result.DurationMS, 10))
	_, _ = io.Copy(w, io.NewSectionReader(out, 0, result.ByteSize))
}
