package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type mediaModerationHTTPClient struct {
	endpoint, key, identity string
	client                  *http.Client
}
type mediaModerationHTTPMetadata struct {
	Contract      string `json:"contract"`
	RequestID     string `json:"request_id"`
	PolicyID      string `json:"policy_id"`
	PolicyVersion string `json:"policy_version"`
	PolicyDigest  string `json:"policy_digest"`
	Phase         string `json:"phase"`
	Kind          string `json:"kind"`
	ContentType   string `json:"content_type"`
	ByteSize      int64  `json:"byte_size"`
	SHA256        string `json:"sha256"`
}
type mediaModerationHTTPResponse struct {
	mediaModerationHTTPMetadata
	Decision   string `json:"decision"`
	ReasonCode string `json:"reason_code"`
}

func newMediaModerationHTTPClient(cfg MediaModerationConfig, mode string) (*mediaModerationHTTPClient, error) {
	text, err := newModerationHTTPClient(ModerationConfig{Endpoint: cfg.Endpoint, APIKey: cfg.APIKey, CredentialID: cfg.CredentialID}, mode)
	if err != nil {
		return nil, errors.New("media moderation requires an explicit private HTTPS endpoint and bounded credentials; loopback HTTP is demo-only")
	}
	text.client.Timeout = mediaModerationTimeout
	transport := text.client.Transport.(*http.Transport)
	transport.MaxConnsPerHost = 2
	transport.ResponseHeaderTimeout = mediaModerationTimeout
	return &mediaModerationHTTPClient{endpoint: text.endpoint, key: text.key, identity: text.identity, client: text.client}, nil
}

// The checksum tracks the bytes actually consumed by the HTTP transport, not
// merely a metadata claim or an earlier hash of a different file descriptor.
type mediaModerationUploadReader struct {
	source io.Reader
	mu     sync.Mutex
	count  int64
	sum    hash.Hash
}

func (r *mediaModerationUploadReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	r.mu.Lock()
	if n > 0 {
		r.count += int64(n)
		_, _ = r.sum.Write(p[:n])
	}
	r.mu.Unlock()
	return n, err
}
func (r *mediaModerationUploadReader) matches(c MediaModerationContent) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count == c.ByteSize && hex.EncodeToString(r.sum.Sum(nil)) == c.SHA256
}
func (r *mediaModerationUploadReader) complete(n int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count == n
}
func (client *mediaModerationHTTPClient) check(ctx context.Context, p *MediaModerationPolicy, phase string, c MediaModerationContent, reader io.ReadSeeker) ModerationDecision {
	decision, _ := client.checkObserved(ctx, p, phase, c, reader)
	return decision
}
func (client *mediaModerationHTTPClient) checkObserved(ctx context.Context, p *MediaModerationPolicy, phase string, c MediaModerationContent, reader io.ReadSeeker) (ModerationDecision, *MediaScheduleObservation) {
	observation := &MediaScheduleObservation{Scope: "review", Class: "protocol"}
	decision := client.checkWithObservation(ctx, p, phase, c, reader, observation)
	return decision, observation
}
func (client *mediaModerationHTTPClient) checkWithObservation(ctx context.Context, p *MediaModerationPolicy, phase string, c MediaModerationContent, reader io.ReadSeeker, observation *MediaScheduleObservation) ModerationDecision {
	meta := mediaModerationHTTPMetadata{Contract: mediaModerationContract, RequestID: newID("mreq"), PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion, PolicyDigest: p.Digest, Phase: phase, Kind: c.Kind, ContentType: c.ContentType, ByteSize: c.ByteSize, SHA256: c.SHA256}
	raw, _ := json.Marshal(meta)
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	part, err := writer.CreateFormField("metadata")
	if err != nil {
		return moderationReview("check_unavailable")
	}
	_, _ = part.Write(raw)
	if _, err = writer.CreateFormFile("file", "media.bin"); err != nil {
		return moderationReview("check_unavailable")
	}
	first := append([]byte{}, prefix.Bytes()...)
	prefix.Reset()
	if writer.Close() != nil {
		return moderationReview("check_unavailable")
	}
	last := append([]byte{}, prefix.Bytes()...)
	// The transport budget includes framing as well as the private file.
	contentLength := int64(len(first)) + c.ByteSize + int64(len(last))
	if contentLength > mediaModerationVideoLimit {
		return moderationReview("content_too_large")
	}
	tracked := &mediaModerationUploadReader{source: io.LimitReader(reader, c.ByteSize), sum: sha256.New()}
	body := &mediaModerationUploadReader{source: io.MultiReader(bytes.NewReader(first), tracked, bytes.NewReader(last)), sum: sha256.New()}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, body)
	if err != nil {
		return moderationReview("check_unavailable")
	}
	req.ContentLength = contentLength
	req.GetBody = nil
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+client.key)
	response, err := client.client.Do(req)
	if err != nil {
		*observation = *mediaNetworkObservation("review", ctx)
		var network net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
			return moderationReview("check_timeout")
		}
		return moderationReview("check_unavailable")
	}
	defer response.Body.Close()
	*observation = *mediaResponseObservation("review", response, time.Now())
	if observation.Class == "success" {
		observation.Class = "protocol"
	}
	if response.StatusCode != http.StatusOK {
		return moderationReview("check_unavailable")
	}
	contentType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" || len(params) > 1 || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		return moderationReview("check_protocol")
	}
	for k := range params {
		if k != "charset" {
			return moderationReview("check_protocol")
		}
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (8<<10)+1))
	if err != nil {
		if ctx.Err() != nil {
			return moderationReview("check_timeout")
		}
		return moderationReview("check_protocol")
	}
	if len(raw) > 8<<10 || !utf8.Valid(raw) || validateCatalogJSONKeys(raw) != nil {
		return moderationReview("check_protocol")
	}
	keys := map[string]bool{"contract": true, "request_id": true, "policy_id": true, "policy_version": true, "policy_digest": true, "phase": true, "kind": true, "content_type": true, "byte_size": true, "sha256": true, "decision": true, "reason_code": true}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return moderationReview("check_protocol")
	}
	for k := range fields {
		if !keys[k] {
			return moderationReview("check_protocol")
		}
	}
	var out mediaModerationHTTPResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || out.mediaModerationHTTPMetadata != meta || !tracked.matches(c) || !body.complete(req.ContentLength) || ctx.Err() != nil {
		return moderationReview("check_protocol")
	}
	switch {
	case out.Decision == "allow" && out.ReasonCode == "allowed":
		observation.Class = "success"
		return ModerationDecision{State: "ALLOWED", ReasonCode: "allowed"}
	case out.Decision == "block" && out.ReasonCode == "policy_block":
		observation.Class = "success"
		return ModerationDecision{State: "BLOCKED", ReasonCode: "policy_block"}
	case out.Decision == "review" && out.ReasonCode == "policy_review":
		observation.Class = "success"
		return moderationReview("policy_review")
	}
	return moderationReview("check_protocol")
}
