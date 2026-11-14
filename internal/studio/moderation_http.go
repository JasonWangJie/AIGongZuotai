package studio

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// text-v1 is our explicit private adapter contract, not an assumed vendor API.
// It sends only the bounded material being checked; no account/provider secrets.
type moderationHTTPRequest struct {
	Contract      string        `json:"contract"`
	RequestID     string        `json:"request_id"`
	PolicyID      string        `json:"policy_id"`
	PolicyVersion string        `json:"policy_version"`
	PolicyDigest  string        `json:"policy_digest"`
	Phase         string        `json:"phase"`
	ContentDigest string        `json:"content_digest"`
	Messages      []ChatMessage `json:"messages,omitempty"`
	Text          string        `json:"text,omitempty"`
}
type moderationHTTPResponse struct {
	Contract      string `json:"contract"`
	RequestID     string `json:"request_id"`
	PolicyID      string `json:"policy_id"`
	PolicyVersion string `json:"policy_version"`
	PolicyDigest  string `json:"policy_digest"`
	Phase         string `json:"phase"`
	ContentDigest string `json:"content_digest"`
	Decision      string `json:"decision"`
	ReasonCode    string `json:"reason_code"`
}
type moderationHTTPClient struct {
	endpoint, key, identity string
	client                  *http.Client
}

func newModerationHTTPClient(cfg ModerationConfig, providerMode string) (*moderationHTTPClient, error) {
	if len(cfg.Endpoint) > 2048 || strings.ContainsAny(cfg.Endpoint, "\x00\r\n") {
		return nil, errors.New("moderation endpoint is invalid")
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" || strings.ContainsRune(endpoint.Path, 0) {
		return nil, errors.New("moderation requires an explicit endpoint without userinfo, query or fragment")
	}
	if endpoint.Scheme != "https" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || providerMode != "demo" || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("moderation requires HTTPS; HTTP loopback literals are demo fixtures only")
		}
	}
	if !moderationNamePattern.MatchString(cfg.CredentialID) || len(cfg.APIKey) < 16 || len(cfg.APIKey) > 2048 {
		return nil, errors.New("moderation HTTP requires a stable credential ID and private bearer secret")
	}
	for _, r := range cfg.APIKey {
		if r < 33 || r > 126 {
			return nil, errors.New("moderation bearer secret must be bounded visible ASCII without whitespace")
		}
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: moderationTimeout, MaxResponseHeaderBytes: 8 << 10, DisableKeepAlives: true, DisableCompression: true, MaxConnsPerHost: 4}
	client := &http.Client{Timeout: moderationTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	identity := moderationSHA([]byte(endpoint.String() + "\n" + cfg.CredentialID + "\n" + moderationSHA([]byte(cfg.APIKey))))
	return &moderationHTTPClient{endpoint: endpoint.String(), key: cfg.APIKey, identity: identity, client: client}, nil
}

func (client *moderationHTTPClient) check(parent context.Context, policy *ModerationPolicy, phase string, messages []ChatMessage, text string) ModerationDecision {
	content, _ := json.Marshal(struct {
		Messages []ChatMessage `json:"messages,omitempty"`
		Text     string        `json:"text,omitempty"`
	}{messages, text})
	input := moderationHTTPRequest{Contract: moderationContract, RequestID: newID("mreq"), PolicyID: policy.PolicyID, PolicyVersion: policy.PolicyVersion, PolicyDigest: policy.Digest, Phase: phase, ContentDigest: moderationSHA(content), Messages: messages, Text: text}
	raw, _ := json.Marshal(input)
	if len(raw) > 6*MaxChatContentBytes+(16<<10) {
		return moderationReview("content_too_large")
	}
	ctx, cancel := context.WithTimeout(parent, moderationTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(raw))
	if err != nil {
		return moderationReview("check_unavailable")
	}
	// No Idempotency-Key, redirect, shared cookie jar or body rewind/retry path.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+client.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := client.client.Do(req)
	if err != nil {
		var network net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
			return moderationReview("check_timeout")
		}
		return moderationReview("check_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return moderationReview("check_unavailable")
	}
	contentType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" || len(params) > 1 || params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8") {
		return moderationReview("check_protocol")
	}
	for key := range params {
		if key != "charset" {
			return moderationReview("check_protocol")
		}
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (8<<10)+1))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return moderationReview("check_timeout")
		}
		return moderationReview("check_unavailable")
	}
	if len(raw) > 8<<10 || !utf8.Valid(raw) || validateCatalogJSONKeys(raw) != nil {
		return moderationReview("check_protocol")
	}
	var fields map[string]json.RawMessage
	allowed := map[string]bool{"contract": true, "request_id": true, "policy_id": true, "policy_version": true, "policy_digest": true, "phase": true, "content_digest": true, "decision": true, "reason_code": true}
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(allowed) {
		return moderationReview("check_protocol")
	}
	for k := range fields {
		if !allowed[k] {
			return moderationReview("check_protocol")
		}
	}
	var result moderationHTTPResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return moderationReview("check_protocol")
	}
	if result.Contract != input.Contract || result.RequestID != input.RequestID || result.PolicyID != input.PolicyID || result.PolicyVersion != input.PolicyVersion || result.PolicyDigest != input.PolicyDigest || result.Phase != input.Phase || result.ContentDigest != input.ContentDigest {
		return moderationReview("check_protocol")
	}
	if ctx.Err() != nil {
		return moderationReview("check_unavailable")
	}
	switch {
	case result.Decision == "allow" && result.ReasonCode == "allowed":
		return ModerationDecision{State: "ALLOWED", ReasonCode: "allowed"}
	case result.Decision == "block" && result.ReasonCode == "policy_block":
		return ModerationDecision{State: "BLOCKED", ReasonCode: "policy_block"}
	case result.Decision == "review" && result.ReasonCode == "policy_review":
		return moderationReview("policy_review")
	default:
		return moderationReview("check_protocol")
	}
}
