package studio

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var frontendChunkPath = regexp.MustCompile(`^/assets/[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{8}\.js$`)
var httpQValue = regexp.MustCompile(`^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$`)

// Return thousandths. Invalid weights never opt the client into a coding.
func frontendEncodingWeight(parameters []string) int {
	if len(parameters) == 0 {
		return 1000
	}
	if len(parameters) != 1 {
		return 0
	}
	name, value, found := strings.Cut(parameters[0], "=")
	value = strings.TrimSpace(value)
	if !found || !strings.EqualFold(strings.TrimSpace(name), "q") || !httpQValue.MatchString(value) {
		return 0
	}
	if value[0] == '1' {
		return 1000
	}
	if len(value) < 3 {
		return 0
	}
	weight, _ := strconv.Atoi((value[2:] + "000")[:3])
	return weight
}

// An unlisted identity is an acceptable fallback unless '*' expressly excludes it.
func frontendEncodings(headers []string) (gzip, identity int) {
	weights := map[string]int{}
	for _, header := range headers {
		for _, item := range strings.Split(header, ",") {
			parts := strings.Split(item, ";")
			name := strings.ToLower(strings.TrimSpace(parts[0]))
			if name == "x-gzip" {
				name = "gzip"
			}
			if name != "gzip" && name != "identity" && name != "*" {
				continue
			}
			weight := frontendEncodingWeight(parts[1:])
			if old, ok := weights[name]; !ok || weight < old {
				weights[name] = weight
			}
		}
	}
	gzip, listed := weights["gzip"]
	if !listed {
		gzip = weights["*"]
	}
	identity, listed = weights["identity"]
	if !listed {
		identity = -1
		if star, ok := weights["*"]; ok && star == 0 {
			identity = 0
		}
	}
	return
}

// Sidecars come only from the release tree, under the already resolved public
// chunk's directory. Do not follow a sidecar link or use one swapped while opening.
func openFrontendGzip(path string) (*os.File, os.FileInfo) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, before) {
		file.Close()
		return nil, nil
	}
	return file, info
}

// ServeContent leaves length to the caller when Content-Encoding is present.
// Attach the known length only to a full file response, never to 304/412/416.
type frontendGzipWriter struct {
	http.ResponseWriter
	size int64
}

func (w frontendGzipWriter) WriteHeader(code int) {
	if code == http.StatusOK {
		w.Header().Set("Content-Length", strconv.FormatInt(w.size, 10))
	} else if code != http.StatusNotModified {
		w.Header().Del("Content-Encoding")
	}
	w.ResponseWriter.WriteHeader(code)
}

func serveFrontendChunk(w http.ResponseWriter, r *http.Request, path string, file *os.File, info os.FileInfo) {
	w.Header().Add("Vary", "Accept-Encoding")
	gz, identity := frontendEncodings(r.Header.Values("Accept-Encoding"))
	coding := "identity"
	if r.Header.Get("Range") == "" && gz > 0 && gz >= identity {
		if encoded, stat := openFrontendGzip(path + ".gz"); encoded != nil {
			defer encoded.Close()
			file, info, coding = encoded, stat, "gzip"
			w.Header().Set("Content-Encoding", coding)
			w = frontendGzipWriter{ResponseWriter: w, size: info.Size()}
		}
	}
	if coding == "identity" && identity == 0 {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "No acceptable JavaScript representation", http.StatusNotAcceptable)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("ETag", fmt.Sprintf(`W/"%s-%x-%x"`, coding, info.ModTime().UnixNano(), info.Size()))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
