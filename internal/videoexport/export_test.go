package videoexport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sourceClip(t *testing.T, p string, start, end int64) Clip {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		t.Fatal(err)
	}
	return Clip{f, hex.EncodeToString(h.Sum(nil)), n, start, end}
}
func destination(t *testing.T, dir string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(dir, "result-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestVideoExportFrozenInputAndLimits(t *testing.T) {
	dir := t.TempDir()
	exe, _ := os.Executable()
	e, err := New(Config{exe, exe, dir})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "private.mp4")
	if err = os.WriteFile(p, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	c := sourceClip(t, p, 0, 1000)
	dst := destination(t, dir)
	if err = os.WriteFile(p, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Export(t.Context(), []Clip{c}, dst); !errors.Is(err, ErrInput) {
		t.Fatalf("changed bytes: %v", err)
	}
	for _, mutate := range []func(*Clip){func(c *Clip) { c.ByteSize = MaxClipBytes + 1 }, func(c *Clip) { c.EndMS = MaxDurationMS + 1 },
		func(c *Clip) { c.StartMS = -1 }, func(c *Clip) { c.EndMS = 1 }, func(c *Clip) { c.ExpectedSHA256 = "http://example.invalid/never" }} {
		bad := c
		mutate(&bad)
		if _, err = e.Export(t.Context(), []Clip{bad}, dst); !errors.Is(err, ErrInput) {
			t.Fatal("invalid cut accepted", err)
		}
	}
	if _, err = e.Export(t.Context(), make([]Clip, 9), dst); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "video-export-*"))
	if len(files) != 0 {
		t.Fatal("private stage survived failure")
	}
}

func realExecutor(t *testing.T) (*Executor, string) {
	t.Helper()
	bin := os.Getenv("VIDEOEXPORT_CODEC_DIR")
	if runtime.GOOS != "windows" || bin == "" {
		t.Skip("set VIDEOEXPORT_CODEC_DIR to verified Windows FFmpeg/ffprobe directory")
	}
	root := t.TempDir()
	e, err := New(Config{filepath.Join(bin, "ffmpeg.exe"), filepath.Join(bin, "ffprobe.exe"), root})
	if err != nil {
		t.Fatal(err)
	}
	return e, root
}
func mediaCommand(t *testing.T, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, append([]string{"-v", "error", "-nostdin"}, args...)...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("local codec fixture: %v %s", err, errBuf.String())
	}
	return out
}
func toneAt(t *testing.T, pcm []byte, at float64) (float64, float64) {
	t.Helper()
	offset, n := int(at*48000), 4800
	if (offset+n)*2 > len(pcm) {
		t.Fatal("missing audio samples")
	}
	var power float64
	crossings := 0
	var previous int16
	for i := 0; i < n; i++ {
		v := int16(binary.LittleEndian.Uint16(pcm[(offset+i)*2:]))
		power += float64(v) * float64(v)
		if i > 0 && previous <= 0 && v > 0 {
			crossings++
		}
		previous = v
	}
	return float64(crossings) * 48000 / float64(n), math.Sqrt(power / float64(n))
}
func TestVideoExportActualPicturesToneCutsAndSilence(t *testing.T) {
	e, dir := realExecutor(t)
	original, mute := filepath.Join(dir, "original.mp4"), filepath.Join(dir, "mute.mp4")
	mediaCommand(t, e.cfg.FFmpegPath, "-f", "lavfi", "-i", "color=c=red:s=160x90:r=24:d=0.5", "-f", "lavfi", "-i", "color=c=blue:s=160x90:r=24:d=0.5",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=0.5", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=0.5",
		"-filter_complex", "[0:v][2:a][1:v][3:a]concat=n=2:v=1:a=1[v][a]", "-map", "[v]", "-map", "[a]", "-c:v", "libx264", "-threads", "1", "-g", "120", "-sc_threshold", "0", "-c:a", "aac", original)
	mediaCommand(t, e.cfg.FFmpegPath, "-f", "lavfi", "-i", "color=c=lime:s=160x90:r=24:d=1", "-c:v", "libx264", "-threads", "1", mute)
	dst := destination(t, dir)
	clips := []Clip{sourceClip(t, original, 583, 958), sourceClip(t, original, 83, 458), sourceClip(t, mute, 125, 500)}
	result, err := e.Export(t.Context(), clips, dst)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if result.DurationMS != 1125 || result.Width != 1280 || result.Height != 720 {
		t.Fatalf("output dimensions/time: %+v", result)
	}
	got := sourceClip(t, dst.Name(), 0, 1125)
	if got.ByteSize != result.ByteSize || got.ExpectedSHA256 != result.SHA256 {
		t.Fatal("returned bytes differ")
	}
	rgb := mediaCommand(t, e.cfg.FFmpegPath, "-i", dst.Name(), "-map", "0:v:0", "-vf", "scale=1:1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	if len(rgb) != 27*3 {
		t.Fatalf("expected 27 actual frames, got %d", len(rgb)/3)
	}
	for frame := 0; frame < 27; frame++ {
		color := rgb[frame*3 : frame*3+3]
		channel := []int{2, 0, 1}[frame/9]
		if color[channel] < 185 || color[(channel+1)%3] > 60 || color[(channel+2)%3] > 60 {
			t.Fatalf("frame %d color %v", frame, color)
		}
	}
	pcm := mediaCommand(t, e.cfg.FFmpegPath, "-i", dst.Name(), "-vn", "-ac", "1", "-ar", "48000", "-f", "s16le", "pipe:1")
	for i, want := range []float64{880, 440, 0} {
		hz, rms := toneAt(t, pcm, float64(i)*0.375+0.12)
		if want == 0 {
			if rms > 8 {
				t.Fatalf("silent segment RMS %.1f", rms)
			}
		} else if math.Abs(hz-want) > 20 || rms < 300 {
			t.Fatalf("segment %d: hz %.1f RMS %.1f", i, hz, rms)
		}
	}
	t.Logf("decoded %d frames and %d audio samples; H.264/AAC %dx%d, %d ms", len(rgb)/3, len(pcm)/2, result.Width, result.Height, result.DurationMS)
}

func TestVideoExportCancelKeepsSlotUntilCodecExit(t *testing.T) {
	e, dir := realExecutor(t)
	p := filepath.Join(dir, "moving.mp4")
	mediaCommand(t, e.cfg.FFmpegPath, "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24:d=20", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", p)
	clips := []Clip{sourceClip(t, p, 0, 20_000)}
	dst := destination(t, dir)
	second := destination(t, dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.Export(ctx, clips, dst); done <- err }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("codec exited before cancellation: %v", err)
		default:
		}
		files, _ := filepath.Glob(filepath.Join(dir, "video-export-*", "export.mp4"))
		if len(files) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live encoder never opened bounded output")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := e.Export(t.Context(), clips, second); !errors.Is(err, ErrBusy) {
		t.Fatalf("parallel admission: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("encoder did not exit on cancellation")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "video-export-") {
			t.Fatal("spool still exists after returned cancellation")
		}
	}
	for _, f := range []*os.File{dst, second} {
		s, _ := f.Stat()
		if s.Size() != 0 {
			t.Fatal("unverified output escaped")
		}
	}
	clips[0].EndMS = 250
	if _, err := e.Export(t.Context(), clips, second); err != nil {
		t.Fatal(fmt.Errorf("slot not reusable after actual codec exit: %w", err))
	}
}

func TestVideoExportRejectsTextAndOutOfRangeMedia(t *testing.T) {
	e, dir := realExecutor(t)
	fake := filepath.Join(dir, "looks-like-video.mp4")
	if err := os.WriteFile(fake, []byte("#EXTM3U\nnot-a-movie\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dst := destination(t, dir)
	if _, err := e.Export(t.Context(), []Clip{sourceClip(t, fake, 0, 500)}, dst); !errors.Is(err, ErrCodec) {
		t.Fatalf("non-movie: %v", err)
	}
	real := filepath.Join(dir, "short.mp4")
	mediaCommand(t, e.cfg.FFmpegPath, "-f", "lavfi", "-i", "color=c=gray:s=160x90:r=24:d=0.5", "-c:v", "libx264", "-threads", "1", real)
	if _, err := e.Export(t.Context(), []Clip{sourceClip(t, real, 0, 600)}, dst); !errors.Is(err, ErrInput) {
		t.Fatalf("overrun source: %v", err)
	}
	if stat, err := dst.Stat(); err != nil || stat.Size() != 0 {
		t.Fatal("invalid source produced a public output")
	}
}
