package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Counts the bytes written by the real application for static JavaScript.
// SSE, JSON and all business handlers keep their original ResponseWriter.
type networkJSReceipt struct {
	ID        int    `json:"id"`
	File      string `json:"file"`
	Status    int    `json:"status"`
	Gzip      bool   `json:"gzip"`
	BodyBytes int64  `json:"body_bytes"`
	Declared  int64  `json:"declared"`
	Invalid   bool   `json:"invalid"`
}

func TestNetworkProbeValidatesBothDirectionBytesAndRealStaticWriter(t *testing.T) {
	const file = "/assets/chunk-verification.js"
	p := newNetworkProbe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "6")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Write([]byte("abcdef"))
	}))
	call := func(method, path string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return w
	}
	data := call("GET", "/__network_benchmark/down", nil)
	if data.Code != 200 || data.Header().Get("X-Benchmark-SHA256") != p.digest ||
		call("POST", "/__network_benchmark/up", data.Body.Bytes()).Code != 204 {
		t.Fatal("calibration bytes failed")
	}
	bad := append([]byte(nil), data.Body.Bytes()...)
	bad[0] ^= 1
	if call("POST", "/__network_benchmark/up", bad).Code != 400 || call("POST", "/__network_benchmark/up", append(bad, 0)).Code != 400 {
		t.Fatal("corrupt or oversized upload accepted")
	}
	resp := call("GET", file, nil)
	if resp.Body.String() != "abcdef" || resp.Header().Get("Server-Timing") != "benchscript;desc=\"1\"" {
		t.Fatal("static forwarding changed body")
	}
	d := p.scripts[0]
	if d.ID != 1 || d.File != path.Base(file) || !d.Gzip || d.Invalid || d.BodyBytes != 6 || d.Declared != 6 {
		t.Fatal("static counts changed")
	}
}

type networkProbe struct {
	app      http.Handler
	payload  []byte
	digest   string
	mu       sync.Mutex
	scripts  []networkJSReceipt
	progress networkProbeProgress
}

type networkProbeProgress struct {
	RTT             int   `json:"rtt_requests"`
	Downloads       int   `json:"downloads"`
	DownloadBytes   int64 `json:"download_bytes"`
	Uploads         int   `json:"uploads_started"`
	UploadBytes     int64 `json:"upload_bytes_read"`
	VerifiedUploads int   `json:"uploads_verified"`
}

func newNetworkProbe(app http.Handler) *networkProbe {
	// The download is served with identity encoding. Generate deterministic
	// bytes outside of measured requests so transfer calibration excludes CPU.
	payload := make([]byte, 4<<20)
	for i := range payload {
		payload[i] = byte((i*31 + i/13) % 251)
	}
	sum := sha256.Sum256(payload)
	return &networkProbe{app: app, payload: payload, digest: hex.EncodeToString(sum[:])}
}

type networkByteWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *networkByteWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *networkByteWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(data)
	w.written += int64(n)
	return n, err
}

func (p *networkProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__network_benchmark/") {
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case r.Method == "GET" && r.URL.Path == "/__network_benchmark/blank":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<!doctype html><title>Local calibration</title>")
		case r.Method == "GET" && r.URL.Path == "/__network_benchmark/rtt":
			p.mu.Lock()
			p.progress.RTT++
			p.mu.Unlock()
			w.WriteHeader(204)
		case r.Method == "GET" && r.URL.Path == "/__network_benchmark/down":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", "4194304")
			w.Header().Set("X-Benchmark-SHA256", p.digest)
			n, _ := w.Write(p.payload)
			p.mu.Lock()
			p.progress.Downloads++
			p.progress.DownloadBytes += int64(n)
			p.mu.Unlock()
		case r.Method == "POST" && r.URL.Path == "/__network_benchmark/up":
			p.mu.Lock()
			p.progress.Uploads++
			p.mu.Unlock()
			h := sha256.New()
			n, err := io.Copy(h, http.MaxBytesReader(w, r.Body, int64(len(p.payload))))
			ok := err == nil && n == int64(len(p.payload)) && hex.EncodeToString(h.Sum(nil)) == p.digest
			p.mu.Lock()
			p.progress.UploadBytes += n
			if ok {
				p.progress.VerifiedUploads++
			}
			p.mu.Unlock()
			if !ok {
				http.Error(w, "invalid calibration bytes", 400)
				return
			}
			w.WriteHeader(204)
		case r.Method == "GET" && r.URL.Path == "/__network_benchmark/progress":
			p.mu.Lock()
			snapshot := p.progress
			p.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(snapshot)
		case r.Method == "GET" && r.URL.Path == "/__network_benchmark/counts":
			p.mu.Lock()
			head := len(p.scripts)
			var receipts []networkJSReceipt
			if raw := r.URL.Query().Get("after"); raw != "" {
				after, err := strconv.Atoi(raw)
				if err != nil || after < 0 || after > head || head-after > 256 {
					p.mu.Unlock()
					http.Error(w, "invalid script window", 400)
					return
				}
				receipts = append([]networkJSReceipt{}, p.scripts[after:]...)
			}
			p.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(struct {
				Head     int                `json:"head"`
				Receipts []networkJSReceipt `json:"receipts"`
			}{head, receipts})
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != "GET" || path.Dir(r.URL.Path) != "/assets" || !strings.HasSuffix(r.URL.Path, ".js") {
		p.app.ServeHTTP(w, r)
		return
	}
	// A public test-only receipt ID travels with the response into the exact
	// ResourceTiming entry, even when this request crosses the user's click.
	p.mu.Lock()
	id := len(p.scripts) + 1
	if id > 4096 {
		p.mu.Unlock()
		http.Error(w, "script witness capacity", 503)
		return
	}
	p.scripts = append(p.scripts, networkJSReceipt{ID: id, File: path.Base(r.URL.Path)})
	p.mu.Unlock()
	w.Header().Add("Server-Timing", "benchscript;desc=\""+strconv.Itoa(id)+"\"")
	dst := &networkByteWriter{ResponseWriter: w}
	p.app.ServeHTTP(dst, r)
	length, err := strconv.ParseInt(dst.Header().Get("Content-Length"), 10, 64)
	p.mu.Lock()
	defer p.mu.Unlock()
	record := &p.scripts[id-1]
	record.Status = dst.status
	record.BodyBytes = dst.written
	if dst.status == 200 && dst.Header().Get("Content-Encoding") == "gzip" {
		record.Gzip = true
		record.Declared = length
		if err != nil || length <= 0 || length != dst.written || !strings.Contains(strings.ToLower(dst.Header().Get("Vary")), "accept-encoding") {
			record.Invalid = true
		}
	} else if dst.status != 304 {
		record.Invalid = true
	}
}
