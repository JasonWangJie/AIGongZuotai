// Package videoexport renders bounded cuts of caller-authorized, immutable video files.
package videoexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	MaxClips           = 8
	MaxDurationMS      = 30_000
	MaxClipBytes       = 64 << 20
	MaxInputBytes      = 192 << 20
	MaxOutputBytes     = 32 << 20
	width, height, fps = 1280, 720, 24
	maxPixels          = 1920 * 1080
)

var (
	ErrBusy        = errors.New("video export slot is in use")
	ErrInput       = errors.New("invalid or changed video export input")
	ErrCodec       = errors.New("unsupported or invalid video media")
	ErrOutput      = errors.New("video export output was not verified")
	ErrUnavailable = errors.New("controlled video export runtime unavailable")
)

type Config struct{ FFmpegPath, FFprobePath, TempDir string }
type Executor struct{ cfg Config }
type Clip struct {
	Source                   *os.File
	ExpectedSHA256           string
	ByteSize, StartMS, EndMS int64
}
type Result struct {
	DurationMS, ByteSize int64
	SHA256               string
	Width, Height        int
}

// Limit the entire process, even if several callers construct an Executor.
var exportSlot = make(chan struct{}, 1)

func New(cfg Config) (*Executor, error) {
	for index, p := range []string{cfg.FFmpegPath, cfg.FFprobePath, cfg.TempDir} {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !filepath.IsAbs(p) || !strings.EqualFold(filepath.Clean(p), resolved) {
			return nil, ErrUnavailable
		}
		info, err := os.Stat(p)
		if err != nil || (index == 2 && !info.IsDir()) || (index != 2 && !info.Mode().IsRegular()) {
			return nil, ErrUnavailable
		}
	}
	return &Executor{cfg: cfg}, nil
}

func frameCount(ms int64) int64 { return (ms*fps + 500) / 1000 }

func validate(clips []Clip, dst *os.File) (int64, error) {
	if len(clips) == 0 || len(clips) > MaxClips || dst == nil {
		return 0, ErrInput
	}
	info, err := dst.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return 0, ErrInput
	}
	var ms, bytes int64
	for _, c := range clips {
		if c.Source == nil || c.ByteSize < 1 || c.ByteSize > MaxClipBytes || c.StartMS < 0 || c.StartMS >= c.EndMS || c.EndMS > 300_000 ||
			c.EndMS-c.StartMS > MaxDurationMS-ms || c.ByteSize > MaxInputBytes-bytes {
			return 0, ErrInput
		}
		sum, err := hex.DecodeString(c.ExpectedSHA256)
		if err != nil || len(sum) != sha256.Size || hex.EncodeToString(sum) != c.ExpectedSHA256 {
			return 0, ErrInput
		}
		i, err := c.Source.Stat()
		if err != nil || !i.Mode().IsRegular() || i.Size() != c.ByteSize {
			return 0, ErrInput
		}
		previous := frameCount(ms)
		ms += c.EndMS - c.StartMS
		if frameCount(ms) == previous {
			return 0, ErrInput
		}
		bytes += c.ByteSize
	}
	return ms, nil
}

type cancellableReader struct {
	ctx context.Context
	r   io.Reader
}

func (r cancellableReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func inputName(i int) string { return fmt.Sprintf("source-%d.mp4", i) }
func snapshot(ctx context.Context, dir string, i int, c Clip) error {
	f, err := os.OpenFile(filepath.Join(dir, inputName(i)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrUnavailable
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), cancellableReader{ctx, io.NewSectionReader(c.Source, 0, c.ByteSize+1)})
	closeErr := f.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || closeErr != nil {
		return ErrUnavailable
	}
	if n != c.ByteSize || hex.EncodeToString(hash.Sum(nil)) != c.ExpectedSHA256 {
		return ErrInput
	}
	return nil
}

// Export receives only opened files. The destination must be a new, empty
// private regular file reserved by the caller; publish it only after success.
func (e *Executor) Export(ctx context.Context, clips []Clip, dst *os.File) (result Result, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	ms, err := validate(clips, dst)
	if err != nil {
		return
	}
	select {
	case exportSlot <- struct{}{}:
		defer func() { <-exportSlot }()
	default:
		return result, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp(e.cfg.TempDir, "video-export-")
	if err != nil {
		return result, ErrUnavailable
	}
	defer func() {
		if cleanErr := os.RemoveAll(dir); cleanErr != nil && err == nil {
			err = ErrUnavailable
			result = Result{}
			_ = dst.Truncate(0)
		}
	}()
	// All original hashes must match before any native reader sees content.
	for i, c := range clips {
		if err = snapshot(ctx, dir, i, c); err != nil {
			return
		}
	}
	inputs := make([]mediaInfo, len(clips))
	for i, c := range clips {
		inputs[i], err = e.inspect(ctx, dir, inputName(i))
		if err != nil {
			return
		}
		if c.EndMS > inputs[i].DurationMS {
			return result, ErrInput
		}
	}
	if _, err = e.command(ctx, dir, e.cfg.FFmpegPath, renderArgs(clips, inputs), 0); err != nil {
		return
	}
	final, err := e.inspect(ctx, dir, "export.mp4")
	if err != nil {
		return
	}
	frames := frameCount(ms)
	quantizedMS := (frames*1000 + fps/2) / fps
	if !final.Audio || final.AudioChannels != 2 || final.SampleRate != 48000 || final.Width != width || final.Height != height || final.Frames != frames ||
		final.DurationMS < quantizedMS-2 || final.DurationMS > quantizedMS+45 {
		return result, ErrOutput
	}
	f, err := os.Open(filepath.Join(dir, "export.mp4"))
	if err != nil {
		return result, ErrOutput
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() < 1 || stat.Size() > MaxOutputBytes {
		return result, ErrOutput
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(io.NewOffsetWriter(dst, 0), hash), cancellableReader{ctx, io.LimitReader(f, stat.Size()+1)})
	if copyErr != nil || ctx.Err() != nil || n != stat.Size() {
		_ = dst.Truncate(0)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, ErrOutput
	}
	return Result{quantizedMS, n, hex.EncodeToString(hash.Sum(nil)), width, height}, nil
}
