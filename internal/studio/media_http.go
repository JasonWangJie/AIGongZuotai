package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MediaProviderError distinguishes an uncertain generation from a failed GET.
// Callers must persist uncertainty and must never turn it into a fresh submit.
type MediaProviderError struct {
	Code              string
	Message           string
	SubmissionUnknown bool
	Retryable         bool
	ReconcileRequired bool
	RetryAfter        time.Duration
}

func (e *MediaProviderError) Error() string { return e.Message }

var ErrMediaCancelUnsupported = errors.New("the documented asynchronous media API does not expose cancellation")

// MediaSubmission is immutable once saved. JSON media idempotency is defined over
// the original bytes, submission path and API key, not equivalent JSON values.
type MediaSubmission struct {
	Body             []byte    `json:"body,omitempty"`
	BodyFileID       string    `json:"body_file_id,omitempty"`
	BodySize         int64     `json:"body_size,omitempty"`
	ContentType      string    `json:"content_type,omitempty"`
	IdempotencyKey   string    `json:"idempotency_key"`
	CredentialID     string    `json:"credential_id"`
	ProviderIdentity string    `json:"provider_identity"`
	BaseURL          string    `json:"base_url"`
	Endpoint         string    `json:"endpoint"`
	Kind             string    `json:"kind"`
	Count            int       `json:"count"`
	PreparedAt       time.Time `json:"prepared_at"`
	BodySHA256       string    `json:"body_sha256"`
}

type AsyncMediaConfig struct {
	BaseURL           string
	APIKey            string
	ImageModel        string
	VideoModel        string
	VideoSeconds      int
	VideoResolution   string
	CredentialID      string
	AssetDir          string
	AssetStorage      AssetStorageConfig
	AllowedAssetHosts []string
	// The application owns this store's lifecycle; it contains immutable local
	// multipart requests, not public asset URLs or provider credentials.
	BodyStore MediaBodyStore
	// Catalog mode may have no legacy generation defaults, while original
	// prepared submissions and handles must still use this gateway identity.
	AllowNoDefaultModel bool
	// Tests may use an isolated local fixture. Never expose this as an env flag.
	allowLocalAssets bool
}

type AsyncMediaProvider struct {
	base             string
	key              string
	credentialID     string
	providerIdentity string
	imageModel       string
	videoModel       string
	videoSeconds     int
	videoRes         string
	client           *http.Client
	editClient       *http.Client
	assets           *LocalAssetStore
	bodies           MediaBodyStore
}

const (
	imageGenerationEndpoint = "/v1/images/generations_async"
	videoGenerationEndpoint = "/v1/videos/generations_async"
	mediaQueryPrefix        = "/v1/media/tasks_async/"
	maxMediaJSONBytes       = 256 << 10
)

var upstreamTaskID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,255}$`)

func NewAsyncMediaProvider(config AsyncMediaConfig) (*AsyncMediaProvider, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("ASYNC_IMAGE_BASE_URL must be a service root HTTP(S) URL without /v1, credentials, query or fragment")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("remote asynchronous media gateway requires HTTPS")
	}
	if strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.CredentialID) == "" {
		return nil, errors.New("asynchronous media requires API key and a stable credential identity")
	}
	if !config.AllowNoDefaultModel && config.ImageModel == "" && config.VideoModel == "" {
		return nil, errors.New("configure at least one asynchronous image or video model")
	}
	if config.VideoSeconds < 0 || config.VideoSeconds > 600 || !config.AllowNoDefaultModel && config.VideoModel != "" && config.VideoSeconds == 0 {
		return nil, errors.New("video model requires an explicit supported duration between 1 and 600 seconds")
	}
	if strings.ContainsAny(config.APIKey, "\r\n") || len(config.CredentialID) > 128 {
		return nil, errors.New("invalid asynchronous media credential configuration")
	}
	base := strings.TrimRight(u.String(), "/")
	assets, err := newLocalAssetStore(config.AssetDir, base, config.AllowedAssetHosts, config.allowLocalAssets)
	if err != nil {
		return nil, err
	}
	if config.AssetStorage.Backend == "s3" {
		destination, storageErr := newS3AssetStorage(config.AssetStorage, config.AssetDir)
		if storageErr != nil {
			_ = assets.Close()
			return nil, storageErr
		}
		assets.destination = destination
	} else if config.AssetStorage.Backend != "" && config.AssetStorage.Backend != "local" {
		_ = assets.Close()
		return nil, errors.New("ASSET_STORAGE_BACKEND must be local or s3")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 16
	transport.MaxConnsPerHost = 16
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 20 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	identity := sha256.Sum256([]byte(base + "\x00" + config.CredentialID + "\x00" + config.APIKey))
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	editClient := *client
	editClient.Timeout = 2 * time.Minute // Large uploads share the pool, with the worker context as the total deadline.
	return &AsyncMediaProvider{base: base, key: config.APIKey, credentialID: config.CredentialID, providerIdentity: hex.EncodeToString(identity[:]),
		imageModel: config.ImageModel, videoModel: config.VideoModel, videoSeconds: config.VideoSeconds, videoRes: config.VideoResolution,
		assets: assets, bodies: config.BodyStore, client: client, editClient: &editClient}, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func (p *AsyncMediaProvider) Prepare(input TaskInput) (MediaSubmission, error) {
	if input.Parameters.Count == 0 {
		input.Parameters.Count = 1
	}
	model := p.imageModel
	if input.Kind == "video" {
		model = p.videoModel
		if p.videoSeconds < 1 || p.videoSeconds > 600 {
			return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "legacy video generation requires a configured duration"}
		}
		input.Parameters.Seconds, input.Parameters.Resolution = p.videoSeconds, p.videoRes
	}
	return p.PrepareModel(input, model)
}

// PrepareModel accepts a server-resolved snapshot. It does no I/O and never
// fills parameters from mutable provider defaults or consults the catalog.
// Persist the returned bytes with the task before calling SubmitPrepared.
func (p *AsyncMediaProvider) PrepareModel(input TaskInput, upstreamModel string) (MediaSubmission, error) {
	count := input.Parameters.Count
	prompt := strings.TrimSpace(input.Inputs.Prompt)
	if prompt == "" || len(prompt) > 64<<10 || !utf8.ValidString(prompt) {
		return MediaSubmission{}, &MediaProviderError{Code: "INVALID_MEDIA_INPUT", Message: "image or video prompt is empty or too large"}
	}
	if strings.TrimSpace(upstreamModel) == "" || len(upstreamModel) > 256 || !utf8.ValidString(upstreamModel) || strings.ContainsAny(upstreamModel, "\x00\r\n") {
		return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "resolved upstream media model is invalid"}
	}
	if input.Parameters.Seconds < 0 || input.Parameters.Seconds > 600 || len(input.Parameters.Size) > 64 || len(input.Parameters.Resolution) > 32 || len(input.Parameters.AspectRatio) > 32 || !utf8.ValidString(input.Parameters.Size+input.Parameters.Resolution+input.Parameters.AspectRatio) || strings.ContainsAny(input.Parameters.Size+input.Parameters.Resolution+input.Parameters.AspectRatio, "\x00\r\n") {
		return MediaSubmission{}, &MediaProviderError{Code: "INVALID_MEDIA_INPUT", Message: "resolved media parameters are outside platform bounds"}
	}
	request := struct {
		Model       string `json:"model"`
		Prompt      string `json:"prompt"`
		N           int    `json:"n,omitempty"`
		AspectRatio string `json:"aspect_ratio,omitempty"`
		Size        string `json:"size,omitempty"`
		Seconds     int    `json:"seconds,omitempty"`
		Resolution  string `json:"resolution,omitempty"`
	}{Model: upstreamModel, Prompt: prompt, AspectRatio: input.Parameters.AspectRatio, Size: input.Parameters.Size, Resolution: input.Parameters.Resolution}
	endpoint := imageGenerationEndpoint
	switch input.Kind {
	case "image":
		if count < 1 || count > 4 || input.Parameters.Seconds != 0 {
			return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "configured image generation supports one to four results"}
		}
		request.N = count
	case "video":
		if count != 1 {
			return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "configured video generation supports one result per task"}
		}
		request.Seconds = input.Parameters.Seconds
		endpoint = videoGenerationEndpoint
	default:
		return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "unsupported media operation"}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return MediaSubmission{}, err
	}
	sum := sha256.Sum256(body)
	return MediaSubmission{Body: body, IdempotencyKey: newID("mediaop"), CredentialID: p.credentialID,
		ProviderIdentity: p.providerIdentity, BaseURL: p.base, Endpoint: endpoint, Kind: input.Kind, Count: count, PreparedAt: time.Now().UTC(), BodySHA256: hex.EncodeToString(sum[:])}, nil
}

// Submit is interface compatibility. Durable schedulers must PrepareModel, persist
// the returned submission, then call SubmitPrepared without re-serializing it.
func (p *AsyncMediaProvider) Submit(ctx context.Context, input TaskInput) (MediaHandle, error) {
	submission, err := p.Prepare(input)
	if err != nil {
		return MediaHandle{}, err
	}
	return p.SubmitPrepared(ctx, submission)
}

func (p *AsyncMediaProvider) SubmitPrepared(ctx context.Context, submission MediaSubmission) (MediaHandle, error) {
	expectedEndpoint := imageGenerationEndpoint
	if submission.Kind == "video" {
		expectedEndpoint = videoGenerationEndpoint
	} else if submission.BodyFileID != "" {
		expectedEndpoint = imageEditEndpoint
	}
	if submission.BaseURL != p.base || submission.CredentialID != p.credentialID || submission.ProviderIdentity != p.providerIdentity || submission.Endpoint != expectedEndpoint ||
		(submission.Kind != "image" && submission.Kind != "video") || submission.Count < 1 || submission.Count > 4 || submission.Kind == "video" && submission.Count != 1 ||
		len(submission.IdempotencyKey) == 0 || len(submission.IdempotencyKey) > 255 ||
		strings.ContainsAny(submission.IdempotencyKey, "\r\n") {
		return MediaHandle{}, submissionIdentityError()
	}
	body, contentType, size, err := p.openSubmissionBody(ctx, submission)
	if err != nil {
		return MediaHandle{}, err
	}
	defer body.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+submission.Endpoint, body)
	if err != nil {
		return MediaHandle{}, submissionIdentityError()
	}
	req.GetBody = nil // A retry is an explicit persisted operation, never Transport replay.
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", submission.IdempotencyKey)
	client := p.client
	if submission.BodyFileID != "" {
		client = p.editClient
	}
	response, err := client.Do(req)
	if err != nil {
		return MediaHandle{}, &MediaProviderError{Code: "SUBMIT_UNKNOWN", Message: "submission response was not received; retain the original request and key", SubmissionUnknown: true, ReconcileRequired: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return MediaHandle{}, mediaHTTPError(response, true)
	}
	var accepted struct {
		TaskID   string `json:"task_id"`
		QueryURL string `json:"query_url"`
	}
	if err := decodeMediaJSON(response.Body, &accepted); err != nil || !upstreamTaskID.MatchString(accepted.TaskID) {
		return MediaHandle{}, &MediaProviderError{Code: "SUBMIT_UNKNOWN", Message: "accepted response did not contain a valid task identity", SubmissionUnknown: true, ReconcileRequired: true}
	}
	// The public query route is documented. Never send a credential to a returned
	// cross-origin query_url; reconstruct the canonical same-origin route by ID.
	return MediaHandle{ID: accepted.TaskID, Kind: submission.Kind, Count: submission.Count,
		SubmittedAt: submission.PreparedAt, CredentialID: submission.CredentialID, ProviderIdentity: p.providerIdentity,
		RetryAfter: retryAfterDuration(response.Header.Get("Retry-After"), time.Now())}, nil
}

func decodeMediaJSON(reader io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxMediaJSONBytes+1))
	if err != nil || len(data) > maxMediaJSONBytes {
		return errors.New("media response exceeds limit or could not be read")
	}
	if err := json.Unmarshal(data, destination); err != nil {
		return errors.New("invalid media JSON response")
	}
	return nil
}

func mediaHTTPError(response *http.Response, submitting bool) *MediaProviderError {
	retry := retryAfterDuration(response.Header.Get("Retry-After"), time.Now())
	if submitting {
		switch response.StatusCode {
		case 400, 401, 403, 404, 413, 415, 422:
			return &MediaProviderError{Code: fmt.Sprintf("SUBMIT_REJECTED_%d", response.StatusCode), Message: fmt.Sprintf("gateway rejected submission with HTTP %d", response.StatusCode)}
		case 429:
			return &MediaProviderError{Code: "SUBMIT_RATE_LIMITED", Message: "gateway rejected submission because capacity is limited", Retryable: true, RetryAfter: retry}
		default:
			return &MediaProviderError{Code: "SUBMIT_UNKNOWN", Message: fmt.Sprintf("submission outcome requires reconciliation after HTTP %d", response.StatusCode), SubmissionUnknown: true, ReconcileRequired: true, RetryAfter: retry}
		}
	}
	if response.StatusCode == 429 || response.StatusCode == 408 || response.StatusCode >= 500 {
		return &MediaProviderError{Code: "QUERY_TEMPORARY", Message: fmt.Sprintf("task query temporarily unavailable (HTTP %d)", response.StatusCode), Retryable: true, RetryAfter: retry}
	}
	return &MediaProviderError{Code: "QUERY_REQUIRES_RECONCILIATION", Message: fmt.Sprintf("task query requires the original credential or operator review (HTTP %d)", response.StatusCode), ReconcileRequired: true, RetryAfter: retry}
}

func retryAfterDuration(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		if seconds > 86400 {
			seconds = 86400
		}
		return max(3*time.Second, time.Duration(seconds)*time.Second)
	}
	if date, err := http.ParseTime(value); err == nil {
		return min(24*time.Hour, max(3*time.Second, date.Sub(now)))
	}
	return 3 * time.Second
}

type mediaTaskResponse struct {
	TaskID        string          `json:"task_id"`
	MediaType     string          `json:"media_type"`
	Stage         string          `json:"stage"`
	Status        string          `json:"status"`
	Progress      int             `json:"progress"`
	StorageStatus string          `json:"storage_status"`
	BillingStatus string          `json:"billing_status"`
	ErrorCode     json.RawMessage `json:"error_code"`
	FailReason    string          `json:"fail_reason"`
	ErrorMessage  string          `json:"error_message"`
	ResultExpired bool            `json:"result_expired"`
	Data          []struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
		ByteSize    int64  `json:"byte_size"`
	} `json:"data"`
}

func (p *AsyncMediaProvider) Query(ctx context.Context, handle MediaHandle) (MediaResult, error) {
	if !upstreamTaskID.MatchString(handle.ID) || handle.CredentialID != p.credentialID || handle.ProviderIdentity != p.providerIdentity || (handle.Kind != "image" && handle.Kind != "video") {
		return MediaResult{}, &MediaProviderError{Code: "QUERY_IDENTITY_CHANGED", Message: "task requires its original gateway credential identity", ReconcileRequired: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+mediaQueryPrefix+url.PathEscape(handle.ID), nil)
	if err != nil {
		return MediaResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Accept", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return MediaResult{}, &MediaProviderError{Code: "QUERY_TEMPORARY", Message: "task query failed; the accepted generation has not been resubmitted", Retryable: true, RetryAfter: 3 * time.Second}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return MediaResult{}, mediaHTTPError(response, false)
	}
	var result mediaTaskResponse
	if err := decodeMediaJSON(response.Body, &result); err != nil {
		return MediaResult{}, &MediaProviderError{Code: "QUERY_PROTOCOL_ERROR", Message: "task query returned an invalid response", Retryable: true, RetryAfter: 3 * time.Second}
	}
	if result.TaskID != handle.ID || result.MediaType != handle.Kind {
		return MediaResult{}, &MediaProviderError{Code: "QUERY_IDENTITY_CHANGED", Message: "query response did not match the submitted task", ReconcileRequired: true}
	}
	errorCode := strings.Trim(string(result.ErrorCode), `"`)
	stateDetails := strings.ToLower(result.Status + " " + result.FailReason + " " + result.ErrorMessage + " " + result.StorageStatus + " " + result.BillingStatus)
	if errorCode == "608" || strings.Contains(stateDetails, "execution_unknown") {
		return MediaResult{}, &MediaProviderError{Code: "EXECUTION_UNKNOWN", Message: "gateway cannot confirm execution; retain this task for reconciliation", ReconcileRequired: true}
	}
	if errorCode == "609" || strings.Contains(stateDetails, "storage_failed") || strings.Contains(stateDetails, "billing_failed") {
		return MediaResult{}, &MediaProviderError{Code: "POSTPROCESS_REQUIRES_RECONCILIATION", Message: "original task storage or billing needs recovery; do not generate again", ReconcileRequired: true}
	}
	progress := max(0, min(result.Progress, 99))
	pollAfter := retryAfterDuration(response.Header.Get("Retry-After"), time.Now())
	switch result.Stage {
	case "queued":
		return MediaResult{Status: "QUEUED", Progress: progress, RetryAfter: pollAfter}, nil
	case "generating":
		return MediaResult{Status: "RUNNING", Progress: progress, RetryAfter: pollAfter}, nil
	case "saving":
		return MediaResult{Status: "INGESTING", Progress: progress, RetryAfter: pollAfter}, nil
	case "failed":
		return MediaResult{Status: "FAILED", Progress: progress, Error: &APIError{Code: "UPSTREAM_GENERATION_FAILED", Message: "gateway reported a terminal generation failure; no automatic resubmission was made"}}, nil
	case "completed":
		if result.ResultExpired || result.StorageStatus == "expired" {
			return MediaResult{}, &MediaProviderError{Code: "RESULT_EXPIRED", Message: "generation previously completed but its upstream results expired", ReconcileRequired: true}
		}
		if result.Status != "succeeded" || len(result.Data) == 0 || len(result.Data) > handle.Count || handle.Count < 1 {
			return MediaResult{}, &MediaProviderError{Code: "RESULT_REQUIRES_RECONCILIATION", Message: "completed task has no usable or consistent result set", ReconcileRequired: true}
		}
		out := MediaResult{Status: "SUCCEEDED", Progress: 100}
		for index, artifact := range result.Data {
			identity := p.base + "\n" + p.credentialID + "\n" + handle.ID + "\n" + strconv.Itoa(index)
			stored, err := p.assets.Import(ctx, identity, artifact.URL, handle.Kind)
			if err != nil {
				return MediaResult{}, err
			}
			out.Artifacts = append(out.Artifacts, Artifact{ID: stored.ID, URL: "/api/v1/assets/" + stored.ID, StorageInfo: &stored,
				Kind: handle.Kind, Title: fmt.Sprintf("生成%s %d", map[string]string{"image": "图片", "video": "视频"}[handle.Kind], index+1)})
		}
		if len(out.Artifacts) < handle.Count {
			out.Status = "PARTIAL"
			out.Error = &APIError{Code: "PARTIAL_OUTPUT", Message: "gateway completed with fewer usable results than requested"}
		}
		return out, nil
	default:
		return MediaResult{}, &MediaProviderError{Code: "UNKNOWN_TASK_STATE", Message: "gateway task state requires adapter review", ReconcileRequired: true}
	}
}

func (p *AsyncMediaProvider) Cancel(context.Context, MediaHandle) error {
	return ErrMediaCancelUnsupported
}

// ServeAsset must only be called after the HTTP layer has checked the current
// user's task/workspace ownership. It is not an authorization boundary itself.
func (p *AsyncMediaProvider) ServeAsset(w http.ResponseWriter, r *http.Request, id string) {
	if p.assets.destination != nil {
		p.assets.destination.ServeAsset(w, r, id)
		return
	}
	p.assets.ServeAsset(w, r, id)
}

func (p *AsyncMediaProvider) Close() error {
	p.client.CloseIdleConnections()
	return p.assets.Close()
}
