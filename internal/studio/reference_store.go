package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"regexp"
	"sync"

	"golang.org/x/image/draw"
)

const (
	MaxReferenceBytes      = int64(8 << 20)
	MaxReferencePixels     = int64(16_000_000)
	MaxReferenceDimension  = 8192
	MaxReferenceFiles      = 4
	MaxMediaBodyBytes      = int64(33 << 20)
	MaxReferenceStoreBytes = int64(2 << 30)
	MaxReferenceStoreFiles = 4096
)

var (
	ErrReferenceTooLarge    = errors.New("reference exceeds platform byte limit")
	ErrReferenceFormat      = errors.New("reference image encoding or dimensions unsupported")
	ErrReferenceBusy        = errors.New("reference storage concurrency exhausted")
	ErrReferenceCorrupt     = errors.New("private reference content missing or changed")
	ErrReferenceStorageFull = errors.New("private reference store capacity exhausted")
	privateBlobID           = regexp.MustCompile(`^blob_[a-f0-9]{64}$`)
)

// ReferenceBlob is private persistence metadata. Application authorization is
// required before opening it; content hashes never grant access to a reference.
type ReferenceBlob struct {
	ID          string         `json:"id"`
	SHA256      string         `json:"sha256"`
	ByteSize    int64          `json:"byte_size"`
	ContentType string         `json:"content_type"`
	Width       int            `json:"width,omitempty"`
	Height      int            `json:"height,omitempty"`
	Thumbnail   *ReferenceBlob `json:"thumbnail,omitempty"`
}

// ReferenceStore keeps uploads and immutable multipart request bodies outside
// public trees. A single request body is streamed to disk, never retained as a
// multi-megabyte JSONB or Go byte slice. The caller owns this store's lifetime.
type ReferenceStore struct {
	root          *os.Root
	slots         chan struct{}
	publishMu     sync.Mutex
	quotaMu       sync.Mutex
	usedBytes     int64
	usedFiles     int
	reservedBytes int64
	reservedFiles int
}

func OpenReferenceStore(directory string) (*ReferenceStore, error) {
	if directory == "" {
		return nil, errors.New("private reference directory is required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	s := &ReferenceStore{root: root, slots: make(chan struct{}, 4)}
	// Count all existing files, including crash-left temporary files and blobs
	// whose database transaction failed. Such orphans must not bypass capacity.
	directoryFile, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	defer directoryFile.Close()
	for {
		entries, readErr := directoryFile.ReadDir(256)
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == storageLeaseName || entry.Name() == storageOwnerName {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				root.Close()
				return nil, err
			}
			s.usedFiles++
			s.usedBytes += info.Size()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			root.Close()
			return nil, readErr
		}
	}
	return s, nil
}

func (s *ReferenceStore) Close() error { return s.root.Close() }

func (s *ReferenceStore) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.slots <- struct{}{}:
		return nil
	default:
		return ErrReferenceBusy
	}
}

func (s *ReferenceStore) reserve(limit int64) error {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if s.usedFiles+s.reservedFiles >= MaxReferenceStoreFiles || s.usedBytes+s.reservedBytes > MaxReferenceStoreBytes-limit {
		return ErrReferenceStorageFull
	}
	s.reservedBytes += limit
	s.reservedFiles++
	return nil
}

func (s *ReferenceStore) release(limit int64) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	s.reservedBytes -= limit
	s.reservedFiles--
}

func (s *ReferenceStore) account(size int64) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	s.usedBytes += size
	s.usedFiles++
}

type privateBlobWriter struct {
	ctx       context.Context
	writer    io.Writer
	remaining int64
	err       error
}

func (w *privateBlobWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.err = w.ctx.Err(); w.err != nil {
		return 0, w.err
	}
	if int64(len(p)) > w.remaining {
		w.err = ErrReferenceTooLarge
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

// writeBlob is called only with an acquired slot, including thumbnail writes.
// The validation callback runs before publish and may add private image metadata.
func (s *ReferenceStore) writeBlob(ctx context.Context, limit int64, write func(io.Writer) error, validate func(*os.File, *ReferenceBlob) error) (ReferenceBlob, error) {
	if err := s.reserve(limit); err != nil {
		return ReferenceBlob{}, err
	}
	defer s.release(limit)
	part := newID("reference_part") + ".tmp"
	file, err := s.root.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return ReferenceBlob{}, err
	}
	defer func() {
		file.Close()
		if err := s.root.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
			if info, err := s.root.Stat(part); err == nil {
				s.account(info.Size())
			}
		}
	}()
	digest := sha256.New()
	w := &privateBlobWriter{ctx: ctx, writer: io.MultiWriter(file, digest), remaining: limit}
	if err := write(w); err != nil {
		return ReferenceBlob{}, err
	}
	if w.err != nil {
		return ReferenceBlob{}, w.err
	}
	if err := ctx.Err(); err != nil {
		return ReferenceBlob{}, err
	}
	size := limit - w.remaining
	if size < 1 {
		return ReferenceBlob{}, ErrReferenceFormat
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	blob := ReferenceBlob{ID: "blob_" + hash, SHA256: hash, ByteSize: size}
	if validate != nil {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return ReferenceBlob{}, err
		}
		if err := validate(file, &blob); err != nil {
			return ReferenceBlob{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ReferenceBlob{}, err
	}
	if err := file.Sync(); err != nil {
		return ReferenceBlob{}, err
	}
	if err := file.Close(); err != nil {
		return ReferenceBlob{}, err
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	// Never overwrite a file whose identity is already published. An unexpected
	// corrupt object requires repair, not a silent replacement beneath readers.
	if existing, err := s.openVerified(ctx, blob, limit); err == nil {
		existing.Close()
		return blob, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ReferenceBlob{}, err
	}
	if err := s.root.Rename(part, blob.ID+".bin"); err != nil {
		return ReferenceBlob{}, err
	}
	s.account(blob.ByteSize)
	return blob, nil
}

// PutImage streams, inspects headers, bounds decoded pixels, then fully decodes.
// MIME and filename supplied by the browser do not participate in validation.
func (s *ReferenceStore) PutImage(ctx context.Context, input io.Reader) (ReferenceBlob, error) {
	if err := s.acquire(ctx); err != nil {
		return ReferenceBlob{}, err
	}
	defer func() { <-s.slots }()
	return s.writeBlob(ctx, MaxReferenceBytes, func(w io.Writer) error {
		_, err := io.Copy(w, io.LimitReader(input, MaxReferenceBytes+1))
		return err
	}, func(file *os.File, blob *ReferenceBlob) error {
		config, format, err := image.DecodeConfig(file)
		types := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp"}
		mime, ok := types[format]
		if err != nil || !ok || config.Width < 1 || config.Height < 1 || config.Width > MaxReferenceDimension || config.Height > MaxReferenceDimension || int64(config.Width)*int64(config.Height) > MaxReferencePixels {
			return ErrReferenceFormat
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		decoded, decodedFormat, err := image.Decode(file)
		if err != nil || decodedFormat != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
			return ErrReferenceFormat
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		blob.ContentType, blob.Width, blob.Height = mime, config.Width, config.Height
		thumb, err := s.thumbnail(ctx, decoded)
		if err != nil {
			return err
		}
		blob.Thumbnail = &thumb
		return nil
	})
}

func (s *ReferenceStore) thumbnail(ctx context.Context, original image.Image) (ReferenceBlob, error) {
	w, h := original.Bounds().Dx(), original.Bounds().Dy()
	if w > 512 || h > 512 {
		if w >= h {
			h = max(1, h*512/w)
			w = 512
		} else {
			w = max(1, w*512/h)
			h = 512
		}
	}
	preview := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(preview, preview.Bounds(), original, original.Bounds(), draw.Src, nil)
	thumb, err := s.writeBlob(ctx, 2<<20, func(out io.Writer) error {
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		return encoder.Encode(out, preview)
	}, nil)
	if err != nil {
		return ReferenceBlob{}, err
	}
	thumb.ContentType, thumb.Width, thumb.Height = "image/png", w, h
	return thumb, nil
}

// WriteBody implements MediaBodyStore. The random multipart boundary and every
// byte are frozen before any external request and persisted separately by App.
func (s *ReferenceStore) WriteBody(ctx context.Context, contentType string, write func(io.Writer) error) (MediaBodyInfo, error) {
	if err := s.acquire(ctx); err != nil {
		return MediaBodyInfo{}, err
	}
	defer func() { <-s.slots }()
	limit := MaxMediaBodyBytes
	if contentType == "application/json" {
		limit = maxSourceVideoBodyBytes
	}
	blob, err := s.writeBlob(ctx, limit, write, nil)
	if err != nil {
		return MediaBodyInfo{}, err
	}
	return MediaBodyInfo{ID: blob.ID, SHA256: blob.SHA256, ByteSize: blob.ByteSize, ContentType: contentType}, nil
}

func (s *ReferenceStore) openVerified(ctx context.Context, blob ReferenceBlob, limit int64) (*os.File, error) {
	if !privateBlobID.MatchString(blob.ID) || blob.ID != "blob_"+blob.SHA256 || blob.ByteSize < 1 || blob.ByteSize > limit {
		return nil, ErrReferenceCorrupt
	}
	file, err := s.root.Open(blob.ID + ".bin")
	if err != nil {
		return nil, err
	}
	fail := func() (*os.File, error) { file.Close(); return nil, ErrReferenceCorrupt }
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != blob.ByteSize {
		return fail()
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail()
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != blob.SHA256 {
		return fail()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail()
	}
	return file, nil
}

// OpenVerified returns the same descriptor that was hashed, rewound for the
// caller. Used only after the application checks project and account ownership.
func (s *ReferenceStore) OpenVerified(ctx context.Context, blob ReferenceBlob) (*os.File, error) {
	return s.openVerified(ctx, blob, MaxReferenceBytes)
}

// OpenBody does not replace the provider's mandatory size/hash verification on
// this same descriptor. It never accepts paths or URLs, only an immutable ID.
func (s *ReferenceStore) OpenBody(id string) (*os.File, error) {
	if !privateBlobID.MatchString(id) {
		return nil, ErrReferenceCorrupt
	}
	file, err := s.root.Open(id + ".bin")
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() < 1 || stat.Size() > maxSourceVideoBodyBytes {
		file.Close()
		return nil, ErrReferenceCorrupt
	}
	return file, nil
}
