package studio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ChatProvider emits text deltas. It must honor ctx and must not retry a
// generation after submission: a failed response can already have been billed.
type ChatProvider interface {
	Stream(context.Context, string, func(string) error) error
}

// MediaProvider separates execution from demo and configured asynchronous media
// adapters. Live cancellation is explicitly unsupported by the documented API.
type MediaProvider interface {
	Submit(context.Context, TaskInput) (MediaHandle, error)
	Query(context.Context, MediaHandle) (MediaResult, error)
	Cancel(context.Context, MediaHandle) error
}

type MediaHandle struct {
	ProviderIdentity string
	RetryAfter       time.Duration
	CredentialID     string
	ID               string
	Kind             string
	Count            int
	SubmittedAt      time.Time
}
type MediaResult struct {
	Error      *APIError
	RetryAfter time.Duration
	Status     string
	Progress   int
	Artifacts  []Artifact
}

type demoChat struct{ delay time.Duration }

func (d demoChat) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	answer := "这是本地演示模型的流式回复，尚未调用真实 AI。\n\n你提出的方向是：" + prompt + "\n\n我们可以分三步推进：\n\n1. 明确目标、受众与交付形式，把想法拆成可验证的小步骤。\n2. 在创作区试验图片或视频任务，观察任务状态、取消操作与结果回传。\n3. 保存满意的思路，再按真实网关契约接入模型。\n\n当前工作台展示逐段输出与断线重连；历史保存方式以工作区的存储提示为准。"
	runes := []rune(answer)
	ticker := time.NewTicker(d.delay)
	defer ticker.Stop()
	for offset := 0; offset < len(runes); offset += 6 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		end := min(offset+6, len(runes))
		if err := emit(string(runes[offset:end])); err != nil {
			return err
		}
	}
	return nil
}

type demoMedia struct{ duration time.Duration }

func (d demoMedia) Submit(ctx context.Context, in TaskInput) (MediaHandle, error) {
	if err := ctx.Err(); err != nil {
		return MediaHandle{}, err
	}
	return MediaHandle{ID: newID("mock"), Kind: in.Kind, Count: in.Parameters.Count, SubmittedAt: time.Now()}, nil
}
func (d demoMedia) Cancel(ctx context.Context, _ MediaHandle) error { return ctx.Err() }
func (d demoMedia) Query(ctx context.Context, h MediaHandle) (MediaResult, error) {
	if err := ctx.Err(); err != nil {
		return MediaResult{}, err
	}
	duration := d.duration
	if h.Kind == "video" {
		duration *= 2
	}
	progress := min(int(time.Since(h.SubmittedAt)*100/duration), 100)
	status := "RUNNING"
	if progress < 15 {
		status = "QUEUED"
	}
	if progress >= 90 {
		status = "INGESTING"
	}
	result := MediaResult{Status: status, Progress: progress}
	if progress == 100 {
		result.Status = "SUCCEEDED"
		for i := range h.Count {
			kind, title := "image", fmt.Sprintf("演示概念图 %d · 本地 SVG", i+1)
			path := fmt.Sprintf("/demo-assets/image-%d.svg", i%4+1)
			if h.Kind == "video" {
				kind, title, path = "poster", "模拟视频海报 · 非真实视频", "/demo-assets/video-poster.svg"
			}
			result.Artifacts = append(result.Artifacts, Artifact{ID: newID("art"), URL: path, Kind: kind, Title: title})
		}
	}
	return result, nil
}

type openAIChat struct {
	endpoint string
	key      string
	model    string
	client   *http.Client
}

func newOpenAIChat(baseURL, key, model string) (*openAIChat, error) {
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("live mode requires NEWAPI_CHAT_MODEL when using the legacy default model")
	}
	provider, err := newOpenAIChatGateway(baseURL, key)
	if err != nil {
		return nil, err
	}
	provider.model = model
	return provider, nil
}

// A catalog resolves the private upstream model for each accepted operation.
// All such operations share this gateway client and its connection pool.
func newOpenAIChatGateway(baseURL, key string) (*openAIChat, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("NEWAPI_BASE_URL must be an absolute HTTP(S) API base URL without credentials, query or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && net.ParseIP(u.Hostname()) == nil {
		return nil, errors.New("remote NEWAPI_BASE_URL requires HTTPS")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("remote NEWAPI_BASE_URL requires HTTPS")
	}
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("live mode requires a valid NEWAPI_API_KEY")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Do not silently send private gateway credentials to an environment proxy.
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 32
	transport.MaxConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 45 * time.Second
	return &openAIChat{
		endpoint: strings.TrimRight(baseURL, "/") + "/chat/completions", key: key,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (p *openAIChat) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	return p.StreamModel(ctx, p.model, prompt, emit)
}

func (p *openAIChat) StreamModel(ctx context.Context, upstreamModel, prompt string, emit func(string) error) error {
	return p.streamMessages(ctx, upstreamModel, ChatRequest{Messages: []ChatMessage{{Role: "user", Content: prompt}}}, emit)
}

func (p *openAIChat) StreamMessages(ctx context.Context, upstreamModel string, request ChatRequest, emit func(string) error) error {
	if _, err := validateChatWireRequest(request); err != nil {
		return err
	}
	return p.streamMessages(ctx, upstreamModel, request, emit)
}

func (p *openAIChat) streamMessages(ctx context.Context, upstreamModel string, request ChatRequest, emit func(string) error) error {
	_, err := p.streamMessagesWithUsage(ctx, upstreamModel, request, emit, false)
	return err
}

func (p *openAIChat) streamMessagesWithUsage(ctx context.Context, upstreamModel string, request ChatRequest, emit func(string) error, collectUsage bool) (ChatUsage, error) {
	unavailable := missingChatUsage("gateway", "request_not_started")
	if strings.TrimSpace(upstreamModel) == "" || len(upstreamModel) > 256 || !utf8.ValidString(upstreamModel) || strings.ContainsAny(upstreamModel, "\x00\r\n") {
		return unavailable, errors.New("resolved upstream chat model is invalid")
	}
	fields := map[string]any{"model": upstreamModel, "stream": true, "messages": request.Messages}
	if request.MaxOutputTokens != 0 {
		fields[request.OutputLimitField] = request.MaxOutputTokens
	}
	if collectUsage {
		fields["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return unavailable, errors.New("could not construct upstream request")
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.GetBody = nil // Never make a generation replayable by the HTTP transport.
	// Deliberately do not forward local Idempotency-Key: upstream semantics are unknown.
	unavailable.Reason = "stream_incomplete"
	response, err := p.client.Do(req)
	if err != nil {
		return unavailable, errors.New("upstream connection failed or timed out")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return unavailable, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return unavailable, errors.New("upstream did not return an SSE stream")
	}
	if collectUsage {
		usage, err := parseOpenAIStreamUsage(response.Body, emit)
		if err == nil && ctx.Err() != nil {
			usage.Status, usage.Reason, usage.Complete = "missing", "stream_incomplete", false
			return usage, ctx.Err()
		}
		return usage, err
	}
	return unavailable, parseOpenAIStream(response.Body, emit)
}

const maxUpstreamEventBytes = 256 << 10

// parseOpenAIStream handles network fragmentation, CRLF and multi-line data.
// An EOF without [DONE] is a partial stream failure, never silent success.
func parseOpenAIStream(r io.Reader, emit func(string) error) error {
	return parseOpenAIStreamObserved(r, emit, nil)
}

func parseOpenAIStreamObserved(r io.Reader, emit func(string) error, usage *chatUsageAccumulator) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxUpstreamEventBytes)
	var data []byte
	var eventName string
	consume := func() (bool, error) {
		if len(data) == 0 {
			eventName = ""
			return false, nil
		}
		payload := bytes.TrimSpace(data)
		data = nil
		name := eventName
		eventName = ""
		if name == "error" {
			return false, errors.New("upstream reported a generation error")
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			return true, nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Usage   chatUsageField  `json:"usage"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return false, errors.New("invalid upstream SSE JSON")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, errors.New("upstream reported a generation error")
		}
		if usage != nil {
			usage.observe(chunk.Usage)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if err := emit(choice.Delta.Content); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := consume()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line[5:], " ")
			if len(data)+len(value)+1 > maxUpstreamEventBytes {
				return errors.New("upstream SSE event exceeds size limit")
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimPrefix(line[6:], " ")
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("upstream SSE read failed or event exceeded size limit")
	}
	if len(data) > 0 {
		done, err := consume()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return errors.New("upstream stream ended before completion; partial output retained")
}
