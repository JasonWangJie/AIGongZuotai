package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"aigongzuotai/internal/videoexport"
)

func TestVideoTimelineSourceReadsFrozenOriginalWithBoundedSlots(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// This test exercises the storage byte boundary; actual decoded frames
	// and audio are independently checked by internal/videoexport.
	const body = "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isommp42"
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	info := AssetInfo{ID: "asset_" + digest, ContentType: "video/mp4", ByteSize: int64(len(body)), SHA256: digest, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err = os.WriteFile(filepath.Join(dir, info.ID+".bin"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err = writeAssetManifest(root, info); err != nil {
		t.Fatal(err)
	}
	assets, err := OpenLocalAssetReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	app := App{assets: assets}
	for i := 0; i < 8; i++ {
		var raw bytes.Buffer
		if err = app.copyTimelineSource(t.Context(), info, &raw); err != nil || raw.String() != body {
			t.Fatal("original no longer readable", err)
		}
	}
	if len(mediaModerationAssetSlots) != 0 {
		t.Fatal("source read leaked a shared media permit")
	}
	source := filepath.Join(dir, info.ID+".bin")
	if err = os.WriteFile(source, bytes.Repeat([]byte{'x'}, len(body)), 0600); err != nil {
		t.Fatal(err)
	}
	var leaked bytes.Buffer
	if err = app.copyTimelineSource(t.Context(), info, &leaked); err == nil || leaked.Len() != 0 {
		t.Fatal("changed original copied")
	}
	info.ByteSize = videoTimelineSourceLimit + 1
	if err = app.copyTimelineSource(t.Context(), info, &leaked); err == nil {
		t.Fatal("oversized video accepted")
	}
}

func TestVideoTimelineIOShutdownWaitsForReadCleanup(t *testing.T) {
	state, err := newVideoTimelineIO(Config{})
	if err != nil || state.enabled() {
		t.Fatal("default capabilities")
	}
	first, done1, err := state.begin(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	_, done2, err := state.begin(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.begin(context.Background(), false); err == nil {
		t.Fatal("exceeded preview slots")
	}
	exited := make(chan struct{})
	go func() { state.close(); close(exited) }()
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("reader survived shutdown")
	}
	select {
	case <-exited:
		t.Fatal("shutdown returned before both reads had actually exited")
	default:
	}
	done1()
	done2()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("reader cleanup did not finish")
	}
	if _, _, err = state.begin(context.Background(), false); err == nil {
		t.Fatal("shut down runner accepted another operation")
	}
}

func TestVideoTimelineTempDirectoryBoundary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("controlled executor runs on Windows")
	}
	base := t.TempDir()
	exe, _ := os.Executable()
	c := Config{DatabaseURL: "fixture", AuthMode: "required", WebDir: filepath.Join(base, "web"), ReferenceDir: filepath.Join(base, "ref"), AssetDir: filepath.Join(base, "assets"),
		VideoExport: videoexport.Config{FFmpegPath: exe, FFprobePath: exe, TempDir: filepath.Join(base, "private")}}
	for _, bad := range []string{c.WebDir, filepath.Join(c.ReferenceDir, "child"), c.AssetDir, base} {
		invalid := c
		invalid.VideoExport.TempDir = bad
		if _, err := newVideoTimelineIO(invalid); err == nil {
			t.Fatal("overlapping storage accepted")
		}
	}
	h, err := newVideoTimelineIO(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = newVideoTimelineIO(c); err == nil {
		t.Fatal("second owner acquired private temporary directory")
	}
	h.close()
	leftover := filepath.Join(c.VideoExport.TempDir, "unknown-private-file")
	if err = os.WriteFile(leftover, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = newVideoTimelineIO(c); err == nil {
		t.Fatal("startup accepted uncertain spool")
	}
	if got, err := os.ReadFile(leftover); err != nil || string(got) != "retain" {
		t.Fatal("caller file changed")
	}
}
