package studio

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

type AssetInfo struct {
	ID              string     `json:"id"`
	ContentType     string     `json:"content_type"`
	ByteSize        int64      `json:"byte_size"`
	SHA256          string     `json:"sha256"`
	CreatedAt       time.Time  `json:"created_at"`
	StorageBackend  string     `json:"storage_backend,omitempty"`
	StorageIdentity string     `json:"storage_identity,omitempty"`
	ObjectKey       string     `json:"object_key,omitempty"`
	ObjectVersion   string     `json:"object_version,omitempty"`
	ObjectETag      string     `json:"object_etag,omitempty"`
	Thumbnail       *AssetInfo `json:"thumbnail,omitempty"`
}

// LocalAssetStore stores private files. Download authorization is deliberately
// delegated to the application's task ownership check, before ServeAsset.
type LocalAssetStore struct {
	root        *os.Root
	base        *url.URL
	hosts       map[string]bool
	client      *http.Client
	allowLocal  bool
	slots       chan struct{}
	publishMu   sync.Mutex
	imageLimit  int64
	videoLimit  int64
	destination *S3AssetStorage
}

type assetPolicyError struct{ message string }

func (e *assetPolicyError) Error() string { return e.message }

var localAssetID = regexp.MustCompile(`^asset_[a-f0-9]{64}$`)

// OpenLocalAssetReader opens already-imported files independently of current
// provider mode, base URL or API credentials. It never creates directories and
// rejects Import. The caller must authorize ownership before ServeAsset.
func OpenLocalAssetReader(directory string) (*LocalAssetStore, error) {
	if strings.TrimSpace(directory) == "" {
		directory = ".runtime/assets"
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, os.ErrNotExist
	}
	return &LocalAssetStore{root: root}, nil
}

func newLocalAssetStore(directory, baseURL string, allowedHosts []string, allowLocal bool) (*LocalAssetStore, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("ASSET_DIR must be configured for asynchronous media")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, errors.New("could not create private asset storage")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("could not open private asset storage")
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		root.Close()
		return nil, err
	}
	store := &LocalAssetStore{root: root, base: base, hosts: map[string]bool{canonicalAssetHost(base.Hostname()): true},
		allowLocal: allowLocal, slots: make(chan struct{}, 4), imageLimit: 32 << 20, videoLimit: 256 << 20}
	for _, host := range allowedHosts {
		host = canonicalAssetHost(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if strings.ContainsAny(host, "/@?#* \\:") || !regexp.MustCompile(`^[a-z0-9.-]+$`).MatchString(host) {
			root.Close()
			return nil, errors.New("ASSET_ALLOWED_HOSTS accepts exact DNS hostnames, without URLs, ports or wildcards")
		}
		store.hosts[host] = true
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = store.dialPublicAddress
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 4
	transport.MaxConnsPerHost = 4
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 20 * time.Second
	transport.IdleConnTimeout = 30 * time.Second
	store.client = &http.Client{Transport: transport, Timeout: 2 * time.Minute,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= 4 {
				return &assetPolicyError{"too many media redirects"}
			}
			request.Header.Del("Authorization")
			request.Header.Del("Cookie")
			request.Header.Del("Referer")
			return store.validateURL(request.URL)
		}}
	return store, nil
}

func canonicalAssetHost(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }

func (store *LocalAssetStore) validateURL(u *url.URL) error {
	if u.User != nil || u.Fragment != "" || u.Host == "" || !store.hosts[canonicalAssetHost(u.Hostname())] {
		return &assetPolicyError{"media URL host is not explicitly allowed"}
	}
	if u.Scheme != "https" && !(store.allowLocal && u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return &assetPolicyError{"media download requires HTTPS"}
	}
	if port := u.Port(); port != "" && port != "443" && u.Host != store.base.Host && !store.allowLocal {
		return &assetPolicyError{"media download port is not allowed"}
	}
	return nil
}

var forbiddenAssetNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func publicAssetIP(ip net.IP, allowLocal bool) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if allowLocal && address.IsLoopback() {
		return true
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, network := range forbiddenAssetNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

// Resolve at connection time, reject mixed public/private answers, then dial
// the exact checked IP. TLS still verifies the original URL hostname.
func (store *LocalAssetStore) dialPublicAddress(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &assetPolicyError{"invalid media address"}
	}
	if !store.hosts[canonicalAssetHost(host)] {
		return nil, &assetPolicyError{"media address is not allowed"}
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errors.New("media hostname resolution failed")
	}
	if len(ips) == 0 {
		return nil, errors.New("media hostname has no addresses")
	}
	for _, ip := range ips {
		if !publicAssetIP(ip.IP, store.allowLocal) {
			return nil, &assetPolicyError{"media hostname resolves to a non-public address"}
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return connection, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("media connection failed")
}

func (store *LocalAssetStore) importLocal(ctx context.Context, identity, sourceURL, kind string) (AssetInfo, error) {
	if store.client == nil {
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_STORE_READ_ONLY", Message: "this asset reader does not allow downloads or writes", ReconcileRequired: true}
	}
	sum := sha256.Sum256([]byte(identity))
	id := "asset_" + hex.EncodeToString(sum[:])
	if file, metadata, err := store.OpenAsset(id); err == nil {
		file.Close()
		return metadata, nil
	}
	u, err := url.Parse(sourceURL)
	if err == nil {
		u = store.base.ResolveReference(u)
		err = store.validateURL(u)
	}
	if err != nil {
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_SOURCE_NOT_ALLOWED", Message: "result source is not allowed for safe server import; configure its exact public host", ReconcileRequired: true}
	}
	if kind != "image" && kind != "video" {
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_KIND_INVALID", Message: "unsupported asset kind", ReconcileRequired: true}
	}
	select {
	case store.slots <- struct{}{}:
		defer func() { <-store.slots }()
	case <-ctx.Done():
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_DOWNLOAD_TEMPORARY", Message: "asset import deadline elapsed before admission", Retryable: true, RetryAfter: 3 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return AssetInfo{}, err
	}
	request.Header.Set("Accept", map[string]string{"image": "image/png,image/jpeg,image/webp,image/gif", "video": "video/mp4"}[kind])
	// Result URLs are shareable/externally signed. Never forward gateway API key,
	// browser cookie, Origin or Referer to a result host, even on a redirect.
	response, err := store.client.Do(request)
	if err != nil {
		var policy *assetPolicyError
		if errors.As(err, &policy) {
			return AssetInfo{}, &MediaProviderError{Code: "ASSET_SOURCE_NOT_ALLOWED", Message: "result URL or network destination violates the import policy", ReconcileRequired: true}
		}
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_DOWNLOAD_TEMPORARY", Message: "result download temporarily failed; retain the accepted task", Retryable: true, RetryAfter: 3 * time.Second}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == 429 || response.StatusCode >= 500 {
			return AssetInfo{}, &MediaProviderError{Code: "ASSET_DOWNLOAD_TEMPORARY", Message: fmt.Sprintf("result download temporarily unavailable (HTTP %d)", response.StatusCode), Retryable: true, RetryAfter: retryAfterDuration(response.Header.Get("Retry-After"), time.Now())}
		}
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_UNAVAILABLE", Message: "original result is expired or unavailable; generation must not be resubmitted", ReconcileRequired: true}
	}
	limit := store.imageLimit
	if kind == "video" {
		limit = store.videoLimit
	}
	if response.ContentLength > limit {
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_TOO_LARGE", Message: "result exceeds the configured local import limit", ReconcileRequired: true}
	}
	part := newID("part") + ".tmp"
	file, err := store.root.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return AssetInfo{}, assetStorageError()
	}
	defer func() { file.Close(); store.root.Remove(part) }()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, limit+1))
	if err != nil {
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_DOWNLOAD_TEMPORARY", Message: "result transfer was interrupted; retry the original result", Retryable: true, RetryAfter: 3 * time.Second}
	}
	if size == 0 || size > limit {
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_TOO_LARGE", Message: "result is empty or exceeds the local import limit", ReconcileRequired: true}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	contentType, err := validateAssetEncoding(file, kind, size)
	if err != nil {
		return AssetInfo{}, &MediaProviderError{Code: "ASSET_FORMAT_UNSUPPORTED", Message: "result content failed image header/dimension or MP4 container validation", ReconcileRequired: true}
	}
	metadata := AssetInfo{ID: id, ContentType: contentType, ByteSize: size, SHA256: hex.EncodeToString(hash.Sum(nil)), CreatedAt: time.Now().UTC()}
	if err := file.Sync(); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if err := file.Close(); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	store.publishMu.Lock()
	defer store.publishMu.Unlock()
	if existing, info, err := store.OpenAsset(id); err == nil {
		existing.Close()
		return info, nil
	}
	if err := store.root.Rename(part, id+".bin"); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	encoded, _ := json.Marshal(metadata)
	metaPart := newID("meta") + ".tmp"
	meta, err := store.root.OpenFile(metaPart, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return AssetInfo{}, assetStorageError()
	}
	defer func() { meta.Close(); store.root.Remove(metaPart) }()
	if _, err := meta.Write(encoded); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if err := meta.Sync(); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if err := meta.Close(); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	if err := store.root.Rename(metaPart, id+".json"); err != nil {
		return AssetInfo{}, assetStorageError()
	}
	return metadata, nil
}

func assetStorageError() error {
	return &MediaProviderError{Code: "ASSET_STORAGE_TEMPORARY", Message: "local asset storage needs recovery; retain the original gateway task", Retryable: true, RetryAfter: 3 * time.Second}
}

func validateAssetEncoding(reader io.ReadSeeker, kind string, byteSize int64) (string, error) {
	if kind == "video" {
		header := make([]byte, 12)
		if byteSize < 16 {
			return "", errors.New("MP4 too short")
		}
		if _, err := io.ReadFull(reader, header); err != nil || string(header[4:8]) != "ftyp" {
			return "", errors.New("MP4 ftyp missing")
		}
		return "video/mp4", nil
	}
	config, format, err := image.DecodeConfig(reader)
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 32_000_000 {
		return "", errors.New("unsupported image or dimensions")
	}
	types := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}
	contentType, ok := types[format]
	if !ok {
		return "", errors.New("unsupported image encoding")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if format == "gif" {
		if err := preflightGIF(reader); err != nil {
			return "", err
		}
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := gif.DecodeAll(reader); err != nil {
			return "", err
		}
	} else if _, _, err := image.Decode(reader); err != nil {
		return "", err
	}
	return contentType, nil
}

// Bound animation allocations before gif.DecodeAll. Count real image blocks,
// not matching bytes inside compressed data, and cap total decoded pixels.
func preflightGIF(reader io.Reader) error {
	header := make([]byte, 13)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	if string(header[:6]) != "GIF87a" && string(header[:6]) != "GIF89a" {
		return errors.New("invalid GIF header")
	}
	skip := func(n int64) error { _, err := io.CopyN(io.Discard, reader, n); return err }
	if header[10]&0x80 != 0 {
		if err := skip(int64(3 * (1 << ((header[10] & 7) + 1)))); err != nil {
			return err
		}
	}
	blocks := func() error {
		var size [1]byte
		for {
			if _, err := io.ReadFull(reader, size[:]); err != nil {
				return err
			}
			if size[0] == 0 {
				return nil
			}
			if err := skip(int64(size[0])); err != nil {
				return err
			}
		}
	}
	frames := 0
	var pixels int64
	for {
		var marker [1]byte
		if _, err := io.ReadFull(reader, marker[:]); err != nil {
			return err
		}
		switch marker[0] {
		case 0x3b:
			if frames == 0 {
				return errors.New("empty GIF")
			}
			return nil
		case 0x21:
			if err := skip(1); err != nil {
				return err
			}
			if err := blocks(); err != nil {
				return err
			}
		case 0x2c:
			var descriptor [9]byte
			if _, err := io.ReadFull(reader, descriptor[:]); err != nil {
				return err
			}
			frames++
			pixels += int64(binary.LittleEndian.Uint16(descriptor[4:6])) * int64(binary.LittleEndian.Uint16(descriptor[6:8]))
			if frames > 16 || pixels > 32_000_000 {
				return errors.New("GIF frame or pixel budget exceeded")
			}
			if descriptor[8]&0x80 != 0 {
				if err := skip(int64(3 * (1 << ((descriptor[8] & 7) + 1)))); err != nil {
					return err
				}
			}
			if err := skip(1); err != nil {
				return err
			}
			if err := blocks(); err != nil {
				return err
			}
		default:
			return errors.New("invalid GIF block")
		}
	}
}

func (store *LocalAssetStore) OpenAsset(id string) (*os.File, AssetInfo, error) {
	if !localAssetID.MatchString(id) {
		return nil, AssetInfo{}, os.ErrNotExist
	}
	meta, err := store.root.Open(id + ".json")
	if err != nil {
		return nil, AssetInfo{}, os.ErrNotExist
	}
	defer meta.Close()
	var info AssetInfo
	if err := json.NewDecoder(io.LimitReader(meta, 8192)).Decode(&info); err != nil || info.ID != id || info.ByteSize < 1 || info.StorageBackend == "s3" ||
		!map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "video/mp4": true}[info.ContentType] {
		return nil, AssetInfo{}, os.ErrNotExist
	}
	file, err := store.root.Open(id + ".bin")
	if err != nil {
		return nil, AssetInfo{}, os.ErrNotExist
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != info.ByteSize {
		file.Close()
		return nil, AssetInfo{}, os.ErrNotExist
	}
	return file, info, nil
}

func (store *LocalAssetStore) ServeAsset(w http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	file, info, err := store.OpenAsset(id)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	extension := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "video/mp4": ".mp4"}[info.ContentType]
	w.Header().Set("Content-Disposition", `inline; filename="`+info.ID+extension+`"`)
	http.ServeContent(w, request, info.ID+extension, info.CreatedAt, file)
}

func (store *LocalAssetStore) Close() error {
	if store.destination != nil {
		_ = store.destination.Close()
	}
	if store.client != nil {
		store.client.CloseIdleConnections()
	}
	return store.root.Close()
}
