package studio

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFrontendJavaScriptNegotiation(t *testing.T) {
	const chunk = "/assets/index-ABcd_123.js"
	original := []byte(strings.Repeat("console.log('public index');\n", 200))
	var zipped bytes.Buffer
	g := gzip.NewWriter(&zipped)
	if _, err := g.Write(original); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	if err := os.Mkdir(filepath.Join(dist, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"index.html": []byte("<html>shell</html>"), chunk[1:]: original, chunk[1:] + ".gz": zipped.Bytes(),
		"assets/legacy-AAAAAAAA.js": original, "assets/plain.js": original, "assets/style.css": []byte("html{}"),
	} {
		if err := os.WriteFile(filepath.Join(dist, filepath.FromSlash(name)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, server := testApp(t, Config{WebDir: dist})
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true // inspect the negotiated wire bytes
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	request := func(method, path string, headers http.Header) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = headers.Clone()
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, body
	}
	encoding := func(value string) http.Header { return http.Header{"Accept-Encoding": {value}} }
	var gzipTag, identityTag string
	for _, tc := range []struct {
		accept []string
		wire   string
	}{
		{nil, "identity"}, {[]string{""}, "identity"}, {[]string{"br"}, "identity"},
		{[]string{"gzip, deflate, br"}, "gzip"}, {[]string{"br, *;q=0.3"}, "gzip"},
		{[]string{"GZip ; Q=0.2", "br"}, "gzip"}, {[]string{"*;q=1, gzip;q=0"}, "identity"},
		{[]string{"gzip;q=0.2, identity;q=0.5"}, "identity"}, {[]string{"gzip;q=0.5, identity;q=0.5"}, "gzip"},
		{[]string{"gzip;q=0.1, *;q=0"}, "gzip"}, {[]string{"identity;q=1, *;q=0"}, "identity"},
		{[]string{"*;q=0"}, "denied"}, {[]string{"identity;q=0, gzip;q=0"}, "denied"},
		{[]string{"gzip;q=invalid"}, "identity"}, {[]string{"gzip, gzip;q=0"}, "identity"},
	} {
		status, h, body := request("GET", chunk, http.Header{"Accept-Encoding": tc.accept})
		if !strings.Contains(h.Get("Vary"), "Accept-Encoding") {
			t.Fatal("missing Vary", tc.accept)
		}
		if tc.wire == "denied" {
			if status != 406 || h.Get("Content-Encoding") != "" {
				t.Fatal("unacceptable encoding served", tc.accept, status)
			}
			continue
		}
		want := original
		if tc.wire == "gzip" {
			want = zipped.Bytes()
			gzipTag = h.Get("ETag")
		} else {
			identityTag = h.Get("ETag")
		}
		if status != 200 || !bytes.Equal(body, want) || (h.Get("Content-Encoding") == "gzip") != (tc.wire == "gzip") ||
			h.Get("Content-Length") != strconv.Itoa(len(want)) || !strings.Contains(h.Get("Content-Type"), "javascript") ||
			h.Get("Cache-Control") != "public, max-age=3600" {
			t.Fatal("incorrect negotiated representation", tc.accept, status)
		}
	}
	if gzipTag == "" || identityTag == "" || gzipTag == identityTag {
		t.Fatal("missing distinct validators")
	}
	for _, tc := range []struct {
		method, accept, extra, value, coding string
		status, size                         int
	}{
		{"HEAD", "gzip", "", "", "gzip", 200, zipped.Len()}, {"HEAD", "identity", "", "", "", 200, len(original)},
		{"GET", "gzip", "If-None-Match", gzipTag, "", 304, 0}, {"GET", "identity", "If-None-Match", gzipTag, "", 200, len(original)},
		{"GET", "gzip", "If-None-Match", identityTag, "gzip", 200, zipped.Len()}, {"GET", "gzip", "If-Match", `"other"`, "", 412, 0},
		{"GET", "gzip", "Range", "bytes=3-8", "", 206, 6}, {"GET", "gzip, identity;q=0", "Range", "bytes=1-2", "", 406, -1},
	} {
		h := encoding(tc.accept)
		if tc.extra != "" {
			h.Set(tc.extra, tc.value)
		}
		status, headers, body := request(tc.method, chunk, h)
		if status != tc.status || headers.Get("Content-Encoding") != tc.coding || !strings.Contains(headers.Get("Vary"), "Accept-Encoding") {
			t.Fatal("conditional/range response", tc.method, tc.extra, status)
		}
		if tc.method == "HEAD" {
			if len(body) != 0 || headers.Get("Content-Length") != strconv.Itoa(tc.size) {
				t.Fatal("HEAD headers or body")
			}
		} else if tc.size >= 0 && len(body) != tc.size {
			t.Fatal("conditional/range length", tc.extra, len(body))
		}
		if status == 304 && headers.Get("ETag") != gzipTag {
			t.Fatal("304 lost representation validator")
		}
		if status == 206 && (!bytes.Equal(body, original[3:9]) || headers.Get("Content-Range") != "bytes 3-8/"+strconv.Itoa(len(original))) {
			t.Fatal("wrong identity range")
		}
	}
	for _, tc := range []struct {
		path, accept string
		status       int
	}{
		{"/assets/legacy-AAAAAAAA.js", "gzip", 200}, {"/assets/legacy-AAAAAAAA.js", "gzip, identity;q=0", 406},
		{chunk + ".gz", "gzip", 404}, {"/", "gzip", 200}, {"/assets/plain.js", "gzip", 200}, {"/assets/style.css", "gzip", 200},
		{"/api/v1/bootstrap", "gzip", 200}, {"/api/v1/no-such-event-stream", "gzip", 404},
	} {
		status, h, _ := request("GET", tc.path, encoding(tc.accept))
		if status != tc.status || h.Get("Content-Encoding") != "" {
			t.Fatal("encoding escaped static chunk scope", tc.path, status)
		}
	}
}
