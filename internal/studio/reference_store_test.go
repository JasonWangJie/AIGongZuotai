package studio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newReferenceStoreForTest(t *testing.T) (*ReferenceStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenReferenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func referenceImageBytes(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1024, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 1024; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x % 256), uint8(y % 256), 72, 255})
		}
	}
	var out bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 80})
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestReferenceStoreValidatedImageAndThumbnail(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			s, dir := newReferenceStoreForTest(t)
			data := referenceImageBytes(t, format)
			blob, err := s.PutImage(context.Background(), bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if blob.Width != 1024 || blob.Height != 512 || blob.ContentType != "image/"+format || blob.ByteSize != int64(len(data)) || blob.Thumbnail == nil {
				t.Fatalf("metadata: %+v", blob)
			}
			thumb := *blob.Thumbnail
			if thumb.Width != 512 || thumb.Height != 256 || thumb.ContentType != "image/png" {
				t.Fatalf("thumbnail: %+v", thumb)
			}
			file, err := s.OpenVerified(context.Background(), blob)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(file)
			file.Close()
			if err != nil || !bytes.Equal(actual, data) {
				t.Fatal("original bytes changed")
			}
			preview, err := s.OpenVerified(context.Background(), thumb)
			if err != nil {
				t.Fatal(err)
			}
			config, err := png.DecodeConfig(preview)
			preview.Close()
			if err != nil || config.Width != 512 || config.Height != 256 {
				t.Fatal("invalid preview")
			}
			again, err := s.PutImage(context.Background(), bytes.NewReader(data))
			if err != nil || again.ID != blob.ID || s.usedFiles != 2 {
				t.Fatalf("dedup failed: %+v %v files=%d", again, err, s.usedFiles)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 2 {
				t.Fatal("temporary files leaked")
			}
		})
	}
}

func TestReferenceStoreWebPFullDecodeAndPreview(t *testing.T) {
	s, _ := newReferenceStoreForTest(t)
	data, err := os.ReadFile("testdata/references/source.webp")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := s.PutImage(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if blob.ContentType != "image/webp" || blob.Width != 960 || blob.Height != 640 || blob.Thumbnail == nil || blob.Thumbnail.Width != 512 || blob.Thumbnail.Height != 341 {
		t.Fatalf("webp metadata: %+v", blob)
	}
	if _, err = s.PutImage(context.Background(), bytes.NewReader(data[:len(data)/2])); !errors.Is(err, ErrReferenceFormat) {
		t.Fatalf("truncated WebP: %v", err)
	}
}

type referenceZeroReader struct{}

func (referenceZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestReferenceStoreRejectsUnsafeAndTruncatedImages(t *testing.T) {
	s, dir := newReferenceStoreForTest(t)
	pngData := fixturePNG(t)
	oversized := append([]byte{}, pngData...)
	binary.BigEndian.PutUint32(oversized[16:20], 8192)
	binary.BigEndian.PutUint32(oversized[20:24], 8192)
	binary.BigEndian.PutUint32(oversized[29:33], crc32.ChecksumIEEE(oversized[12:29]))
	for name, data := range map[string][]byte{"svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "spoofed": []byte("image/png"), "truncated": pngData[:len(pngData)/2], "pixel budget": oversized, "gif": []byte("GIF89a")} {
		t.Run(name, func(t *testing.T) {
			_, err := s.PutImage(context.Background(), bytes.NewReader(data))
			if !errors.Is(err, ErrReferenceFormat) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := s.PutImage(context.Background(), io.LimitReader(referenceZeroReader{}, MaxReferenceBytes+1)); !errors.Is(err, ErrReferenceTooLarge) {
		t.Fatalf("size: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 || s.usedBytes != 0 || s.reservedBytes != 0 {
		t.Fatal("rejected input published or leaked capacity")
	}
}

func TestReferenceStoreImmutableBytesAndPathConfinement(t *testing.T) {
	s, dir := newReferenceStoreForTest(t)
	blob, err := s.PutImage(context.Background(), bytes.NewReader(fixturePNG(t)))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, blob.ID+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(filepath.Join(dir, blob.ID+".bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenVerified(context.Background(), blob); !errors.Is(err, ErrReferenceCorrupt) {
		t.Fatalf("corrupt reference accepted: %v", err)
	}
	if _, err := s.PutImage(context.Background(), bytes.NewReader(fixturePNG(t))); !errors.Is(err, ErrReferenceCorrupt) {
		t.Fatalf("corrupt published blob silently overwritten: %v", err)
	}
	for _, id := range []string{"../other", "blob_../../outside", "http://example.com/file", ""} {
		if f, err := s.OpenBody(id); err == nil {
			f.Close()
			t.Fatalf("invalid ID accepted: %q", id)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	id := "blob_" + string(bytes.Repeat([]byte("a"), 64))
	if err := os.Symlink(outside, filepath.Join(dir, id+".bin")); err != nil {
		t.Log("symlink privilege unavailable; path validation still tested")
	} else if f, err := s.OpenBody(id); err == nil {
		f.Close()
		t.Fatal("symlink escaped private root")
	}
}

func TestReferenceStoreSpoolCancellationAndLimits(t *testing.T) {
	s, dir := newReferenceStoreForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := s.WriteBody(ctx, "multipart/form-data; boundary=test", func(w io.Writer) error {
		_, err := w.Write([]byte("start"))
		if err != nil {
			return err
		}
		cancel()
		_, _ = w.Write([]byte("ignored error"))
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
	_, err = s.WriteBody(context.Background(), "multipart/form-data; boundary=test", func(w io.Writer) error {
		_, err := io.Copy(w, io.LimitReader(referenceZeroReader{}, MaxMediaBodyBytes+1))
		return err
	})
	if !errors.Is(err, ErrReferenceTooLarge) {
		t.Fatalf("oversize spool: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 || s.reservedBytes != 0 || s.usedBytes != 0 {
		t.Fatal("aborted spool leaked files or reservations")
	}
	for i := 0; i < cap(s.slots); i++ {
		s.slots <- struct{}{}
	}
	if _, err = s.PutImage(context.Background(), bytes.NewReader(fixturePNG(t))); !errors.Is(err, ErrReferenceBusy) {
		t.Fatalf("slots: %v", err)
	}
	for i := 0; i < cap(s.slots); i++ {
		<-s.slots
	}
	s.usedBytes = MaxReferenceStoreBytes - MaxMediaBodyBytes + 1
	if _, err = s.WriteBody(context.Background(), "type", func(io.Writer) error { t.Fatal("capacity must reject before write"); return nil }); !errors.Is(err, ErrReferenceStorageFull) {
		t.Fatalf("disk budget: %v", err)
	}
}

func TestReferenceStoreConcurrentPublishAndRestartAccounting(t *testing.T) {
	s, dir := newReferenceStoreForTest(t)
	data := fixturePNG(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PutImage(context.Background(), bytes.NewReader(data)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if s.usedFiles != 2 || s.reservedFiles != 0 {
		t.Fatalf("files/reservations %d/%d", s.usedFiles, s.reservedFiles)
	}
	if err := os.WriteFile(filepath.Join(dir, "interrupted-upload.tmp"), []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenReferenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.usedFiles != 3 || reopened.usedBytes != s.usedBytes+6 {
		t.Fatal("orphan not included in restart capacity")
	}
	// Capacity must also bound tiny files, independently of byte totals.
	reopened.usedFiles = MaxReferenceStoreFiles
	if _, err = reopened.WriteBody(context.Background(), "type", func(io.Writer) error { return nil }); !errors.Is(err, ErrReferenceStorageFull) {
		t.Fatalf("file limit: %v", err)
	}
}

func BenchmarkReferenceBodySpool(b *testing.B) {
	s, err := OpenReferenceStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	b.ReportAllocs()
	b.SetBytes(MaxReferenceBytes)
	for i := 0; i < b.N; i++ {
		_, err := s.WriteBody(context.Background(), "application/octet-stream", func(w io.Writer) error {
			_, err := io.Copy(w, io.LimitReader(referenceZeroReader{}, MaxReferenceBytes))
			return err
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
