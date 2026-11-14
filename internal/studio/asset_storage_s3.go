package studio

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3AssetStorage uses a private local manifest as its stable routing receipt.
// A receipt naming S3 never falls back to a leftover local backup. Credentials
// remain configuration only; object locators do not grant browser access.
type S3AssetStorage struct {
	local                    *LocalAssetStore
	client                   *s3.Client
	http                     *http.Client
	bucket, prefix, identity string
	slots                    chan struct{}
	publishLocks             [64]sync.Mutex
}

func newS3AssetStorage(config AssetStorageConfig, directory string) (*S3AssetStorage, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
		return nil, errors.New("S3_ENDPOINT must be an HTTPS service root; HTTP is limited to loopback testing")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(config.Bucket) || strings.Contains(config.Bucket, "..") || config.Region == "" || len(config.Region) > 64 {
		return nil, errors.New("S3_BUCKET and S3_REGION must be configured")
	}
	if config.AccessKeyID == "" || config.SecretAccessKey == "" {
		return nil, errors.New("S3 credentials must be configured explicitly on the server")
	}
	prefix := strings.Trim(config.Prefix, "/")
	if prefix != "" && (!regexp.MustCompile(`^[a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_-]+)*$`).MatchString(prefix) || len(prefix) > 128) {
		return nil, errors.New("S3_PREFIX contains invalid path components")
	}
	if prefix != "" {
		prefix += "/"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 8
	transport.MaxConnsPerHost = 8
	transport.ResponseHeaderTimeout = 20 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("object storage redirects are disabled") }}
	endpoint := strings.TrimSuffix(u.String(), "/")
	sdk := s3.New(s3.Options{Region: config.Region, BaseEndpoint: aws.String(endpoint), UsePathStyle: config.ForcePathStyle,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, config.SessionToken), HTTPClient: client, RetryMaxAttempts: 2,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	local, err := OpenLocalAssetReader(directory)
	if err != nil {
		return nil, err
	}
	identity := sha256.Sum256([]byte(endpoint + "\n" + config.Region + "\n" + config.Bucket + "\n" + prefix))
	return &S3AssetStorage{local: local, client: sdk, http: client, bucket: config.Bucket, prefix: prefix, identity: hex.EncodeToString(identity[:]), slots: make(chan struct{}, 8)}, nil
}

func (s *S3AssetStorage) Close() error { s.http.CloseIdleConnections(); return s.local.Close() }

func (s *S3AssetStorage) objectKey(id string, thumb bool) string {
	suffix := ".bin"
	if thumb {
		suffix = ".thumb.png"
	}
	return s.prefix + "assets/" + id + suffix
}

func (s *S3AssetStorage) checkReceipt(info AssetInfo) error {
	if info.StorageBackend != "s3" || info.StorageIdentity != s.identity || info.ObjectKey != s.objectKey(info.ID, false) {
		return &MediaProviderError{Code: "ASSET_STORAGE_IDENTITY_CHANGED", Message: "private asset storage identity does not match its saved receipt", ReconcileRequired: true}
	}
	if info.Thumbnail != nil && (info.Thumbnail.StorageBackend != "s3" || info.Thumbnail.StorageIdentity != s.identity || info.Thumbnail.ObjectKey != s.objectKey(info.ID, true)) {
		return &MediaProviderError{Code: "ASSET_STORAGE_IDENTITY_CHANGED", Message: "private thumbnail storage identity does not match", ReconcileRequired: true}
	}
	return nil
}

func (s *S3AssetStorage) Stat(ctx context.Context, id string) (AssetInfo, error) {
	info, err := readAssetManifest(s.local.root, id)
	if err != nil {
		return AssetInfo{}, err
	}
	if info.StorageBackend != "s3" {
		return s.local.Stat(ctx, id)
	}
	if err = s.checkReceipt(info); err != nil {
		return AssetInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err = s.checkRemote(ctx, info); err != nil {
		return AssetInfo{}, err
	}
	return info, nil
}

func (s *S3AssetStorage) headInput(info AssetInfo) *s3.HeadObjectInput {
	in := &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(info.ObjectKey)}
	if info.ObjectVersion != "" {
		in.VersionId = aws.String(info.ObjectVersion)
	}
	return in
}

func (s *S3AssetStorage) checkRemote(ctx context.Context, info AssetInfo) error {
	out, err := s.client.HeadObject(ctx, s.headInput(info))
	if err != nil {
		return safeS3Error(err)
	}
	if aws.ToInt64(out.ContentLength) != info.ByteSize || out.Metadata["sha256"] != info.SHA256 || aws.ToString(out.ContentType) != info.ContentType || (info.ObjectETag != "" && aws.ToString(out.ETag) != info.ObjectETag) {
		return &MediaProviderError{Code: "ASSET_INTEGRITY_MISMATCH", Message: "private object no longer matches its accepted metadata", ReconcileRequired: true}
	}
	return nil
}

func safeS3Error(err error) error {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket", "NoSuchVersion", "AccessDenied", "InvalidObjectState", "PreconditionFailed":
			return &MediaProviderError{Code: "ASSET_UNAVAILABLE", Message: "private object is missing, expired, inaccessible or changed; do not regenerate", ReconcileRequired: true}
		}
	}
	return &MediaProviderError{Code: "ASSET_STORAGE_TEMPORARY", Message: "private object storage is temporarily unavailable; retain the original task", Retryable: true, RetryAfter: 3 * time.Second}
}

// promote verifies the immutable local source, uploads using a create-only
// condition, verifies the full remote bytes, and atomically switches the small
// local receipt. Retrying never overwrites different remote content. Copies of
// all local original/thumbnail bytes are deliberately retained.
func (s *S3AssetStorage) promote(ctx context.Context, info AssetInfo) (AssetInfo, error) {
	if !localAssetID.MatchString(info.ID) {
		return AssetInfo{}, os.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return AssetInfo{}, safeS3Error(ctx.Err())
	}
	// Fixed stripes keep duplicate IDs serialized without holding a global
	// network lock or retaining an unbounded map of asset IDs.
	stripe, _ := strconv.ParseUint(info.ID[len("asset_"):len("asset_")+2], 16, 8)
	lock := &s.publishLocks[stripe%uint64(len(s.publishLocks))]
	lock.Lock()
	defer lock.Unlock()
	current, err := readAssetManifest(s.local.root, info.ID)
	if err != nil {
		return AssetInfo{}, err
	}
	if current.StorageBackend == "s3" {
		return s.Stat(ctx, info.ID)
	}
	info = current
	remote, err := s.copyObject(ctx, info, info.ID+".bin", false)
	if err != nil {
		return AssetInfo{}, err
	}
	info, err = ensureAssetThumbnail(ctx, s.local.root, info)
	if err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if info.Thumbnail != nil {
		thumb, err := s.copyObject(ctx, *info.Thumbnail, info.ID+".thumb.png", true)
		if err != nil {
			return AssetInfo{}, err
		}
		remote.Thumbnail = &thumb
	}
	if err = writeAssetManifest(s.local.root, remote); err != nil {
		return AssetInfo{}, err
	}
	return remote, nil
}

func (s *S3AssetStorage) copyObject(ctx context.Context, info AssetInfo, name string, thumb bool) (AssetInfo, error) {
	f, err := s.local.root.Open(name)
	if err != nil {
		return AssetInfo{}, os.ErrNotExist
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != info.ByteSize {
		return AssetInfo{}, integrityError()
	}
	h := sha256.New()
	n, err := io.Copy(h, &assetContextReader{ctx: ctx, r: io.LimitReader(f, info.ByteSize+1)})
	if err != nil {
		return AssetInfo{}, safeS3Error(err)
	}
	if n != info.ByteSize || hex.EncodeToString(h.Sum(nil)) != info.SHA256 {
		return AssetInfo{}, integrityError()
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	info.StorageBackend = "s3"
	info.StorageIdentity = s.identity
	info.ObjectKey = s.objectKey(info.ID, thumb)
	// A successful readback makes a lost PUT response safe to retry. ETag is
	// only a generation precondition, never treated as the content checksum.
	existing, headErr := s.client.HeadObject(ctx, s.headInput(info))
	if headErr == nil {
		info.ObjectETag = aws.ToString(existing.ETag)
		info.ObjectVersion = aws.ToString(existing.VersionId)
		if err = s.checkRemote(ctx, info); err != nil {
			return AssetInfo{}, err
		}
	} else {
		var api smithy.APIError
		if !errors.As(headErr, &api) || (api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchKey") {
			return AssetInfo{}, safeS3Error(headErr)
		}
		checksum, _ := hex.DecodeString(info.SHA256)
		out, putErr := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(info.ObjectKey), Body: f, ContentLength: aws.Int64(info.ByteSize), ContentType: aws.String(info.ContentType), Metadata: map[string]string{"sha256": info.SHA256}, ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(checksum)), IfNoneMatch: aws.String("*")})
		if putErr != nil {
			// Never overwrite blindly after timeout or a conditional race.
			recovered, recoveryErr := s.client.HeadObject(ctx, s.headInput(info))
			if recoveryErr != nil {
				return AssetInfo{}, safeS3Error(putErr)
			}
			info.ObjectETag = aws.ToString(recovered.ETag)
			info.ObjectVersion = aws.ToString(recovered.VersionId)
		} else {
			info.ObjectETag = aws.ToString(out.ETag)
			info.ObjectVersion = aws.ToString(out.VersionId)
		}
	}
	if err = s.verifyObject(ctx, info); err != nil {
		return AssetInfo{}, err
	}
	return info, nil
}

func integrityError() error {
	return &MediaProviderError{Code: "ASSET_INTEGRITY_MISMATCH", Message: "asset bytes do not match the saved length and SHA256", ReconcileRequired: true}
}

func (s *S3AssetStorage) getInput(info AssetInfo) *s3.GetObjectInput {
	in := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(info.ObjectKey)}
	if info.ObjectVersion != "" {
		in.VersionId = aws.String(info.ObjectVersion)
	}
	if info.ObjectETag != "" {
		in.IfMatch = aws.String(info.ObjectETag)
	}
	return in
}

func (s *S3AssetStorage) verifyObject(ctx context.Context, info AssetInfo) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.checkRemote(ctx, info); err != nil {
		return err
	}
	out, err := s.client.GetObject(ctx, s.getInput(info))
	if err != nil {
		return safeS3Error(err)
	}
	defer out.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, &assetContextReader{ctx: ctx, r: io.LimitReader(out.Body, info.ByteSize+1)})
	if err != nil {
		return safeS3Error(err)
	}
	if n != info.ByteSize || hex.EncodeToString(h.Sum(nil)) != info.SHA256 {
		return integrityError()
	}
	return nil
}

type assetContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *assetContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (s *S3AssetStorage) ServeAsset(w http.ResponseWriter, r *http.Request, id string) {
	s.serve(w, r, id, false)
}
func (s *S3AssetStorage) ServeThumbnail(w http.ResponseWriter, r *http.Request, id string) {
	s.serve(w, r, id, true)
}

func (s *S3AssetStorage) serve(w http.ResponseWriter, r *http.Request, id string, thumbnail bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	info, err := readAssetManifest(s.local.root, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.StorageBackend != "s3" {
		if thumbnail {
			s.local.ServeThumbnail(w, r, id)
		} else {
			s.local.ServeAsset(w, r, id)
		}
		return
	}
	if err = s.checkReceipt(info); err != nil {
		writeError(w, failure(404, "ASSET_UNAVAILABLE", "私有资产存储身份不匹配"))
		return
	}
	if thumbnail {
		if info.Thumbnail == nil {
			http.NotFound(w, r)
			return
		}
		info = *info.Thumbnail
	}
	if len(r.Header.Get("Range")) > 256 || strings.Count(r.Header.Get("Range"), ",") > 7 {
		http.Error(w, "unsupported range count", 416)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		writeError(w, failure(503, "ASSET_STORAGE_BUSY", "私有资产读取繁忙，请稍后重试"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err = s.checkRemote(ctx, info); err != nil {
		var p *MediaProviderError
		if errors.As(err, &p) && p.Retryable {
			writeError(w, failure(503, "ASSET_STORAGE_TEMPORARY", "私有对象存储暂不可用"))
		} else {
			writeError(w, failure(404, "ASSET_UNAVAILABLE", "私有资产已过期或不可用"))
		}
		return
	}
	reader := &s3AssetReader{ctx: ctx, store: s, info: info}
	defer reader.Close()
	assetResponseHeaders(w, info)
	// ServeContent retains HTTP preconditions, single/multiple ranges and HEAD.
	// Its seek operations only change offsets; each read opens a bounded remote
	// Range request instead of buffering the whole object in memory.
	http.ServeContent(w, r, id, info.CreatedAt, reader)
}

type s3AssetReader struct {
	ctx    context.Context
	store  *S3AssetStorage
	info   AssetInfo
	offset int64
	body   io.ReadCloser
	opens  int
}

func (r *s3AssetReader) Close() error {
	if r.body != nil {
		err := r.body.Close()
		r.body = nil
		return err
	}
	return nil
}
func (r *s3AssetReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.offset + offset
	case io.SeekEnd:
		next = r.info.ByteSize + offset
	default:
		return 0, errors.New("invalid seek")
	}
	if next < 0 || next > r.info.ByteSize {
		return 0, errors.New("invalid seek")
	}
	if next != r.offset {
		_ = r.Close()
		r.offset = next
	}
	return next, nil
}
func (r *s3AssetReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.offset >= r.info.ByteSize {
		return 0, io.EOF
	}
	if r.body == nil {
		r.opens++
		if r.opens > 16 {
			return 0, errors.New("range budget exceeded")
		}
		input := r.store.getInput(r.info)
		input.Range = aws.String("bytes=" + strconv.FormatInt(r.offset, 10) + "-")
		out, err := r.store.client.GetObject(r.ctx, input)
		if err != nil {
			return 0, safeS3Error(err)
		}
		if aws.ToInt64(out.ContentLength) != r.info.ByteSize-r.offset {
			out.Body.Close()
			return 0, integrityError()
		}
		r.body = out.Body
	}
	if int64(len(p)) > r.info.ByteSize-r.offset {
		p = p[:r.info.ByteSize-r.offset]
	}
	n, err := r.body.Read(p)
	r.offset += int64(n)
	return n, err
}

func (s *S3AssetStorage) Delete(ctx context.Context, id string) error {
	info, err := readAssetManifest(s.local.root, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.StorageBackend != "s3" {
		return s.local.Delete(ctx, id)
	}
	if err = s.checkReceipt(info); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	objects := []AssetInfo{info}
	if info.Thumbnail != nil {
		objects = append(objects, *info.Thumbnail)
	}
	for _, object := range objects {
		in := &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(object.ObjectKey)}
		if object.ObjectVersion != "" {
			in.VersionId = aws.String(object.ObjectVersion)
		}
		if _, err = s.client.DeleteObject(ctx, in); err != nil {
			return safeS3Error(err)
		}
	}
	return deleteLocalAssetFiles(s.local.root, id)
}
