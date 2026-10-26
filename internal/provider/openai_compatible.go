package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/whl/llm-gateway/internal/core"
)

const (
	defaultRemoteResponseLimit int64 = 32 << 20
	maxRemoteResponseLimit     int64 = 512 << 20

	// Anthropic Messages 文档规定单请求体上限为 32 MB（十进制字节）。
	AnthropicMessagesMaxRequestBodyBytes int64 = 32_000_000

	remoteDialTimeout             = 10 * time.Second
	remoteTLSHandshakeTimeout     = 10 * time.Second
	remoteResponseHeaderTimeout   = 5 * time.Minute
	remoteIdleConnTimeout         = 90 * time.Second
	remoteMaxIdleConnections      = 100
	remoteMaxIdleConnectionsPerHost = 16
	remoteMaxConnectionsPerHost   = 64
	// MaxOpenAICompatibleConcurrentRequests matches the HTTP transport per-host connection ceiling.
	MaxOpenAICompatibleConcurrentRequests        = 64
	defaultOpenAICompatibleMaxConcurrentRequests = 64
)

// ConcurrencyLimiter 跨同一 Provider 的运行时实例共享并发预算。
// Web 配置热更新会替换 Provider 实例，但仍在执行的请求必须继续占用同一份预算。
type ConcurrencyLimiter struct {
	mu     sync.Mutex
	active int
	limit  int
}

// NewConcurrencyLimiter 创建有界的并发许可器；0 表示默认上限。
func NewConcurrencyLimiter(maxConcurrentRequests int) (*ConcurrencyLimiter, error) {
	limiter := &ConcurrencyLimiter{}
	if err := limiter.SetLimit(maxConcurrentRequests); err != nil {
		return nil, err
	}
	return limiter, nil
}

// SetLimit 更新并发预算。降低上限时不会取消在途请求，只会拒绝新请求，直到活动数降至上限以下。
func (l *ConcurrencyLimiter) SetLimit(maxConcurrentRequests int) error {
	if l == nil {
		return fmt.Errorf("provider concurrency limiter is nil")
	}
	if maxConcurrentRequests < 0 || maxConcurrentRequests > MaxOpenAICompatibleConcurrentRequests {
		return fmt.Errorf("provider max_concurrent_requests must be between 0 and %d", MaxOpenAICompatibleConcurrentRequests)
	}
	if maxConcurrentRequests == 0 {
		maxConcurrentRequests = defaultOpenAICompatibleMaxConcurrentRequests
	}
	l.mu.Lock()
	l.limit = maxConcurrentRequests
	l.mu.Unlock()
	return nil
}

// TryAcquire 使用 fail-fast 策略获得一个并发许可。
func (l *ConcurrencyLimiter) TryAcquire() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= l.limit {
		return false
	}
	l.active++
	return true
}

// Release 释放一个此前取得的并发许可。
func (l *ConcurrencyLimiter) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.mu.Unlock()
}

// Limit 返回当前有效上限，供诊断和单元测试使用。
func (l *ConcurrencyLimiter) Limit() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

// OpenAICompatibleConfig 是远程 OpenAI-compatible Provider 的运行时参数。
// 私网访问和明文 HTTP 均默认关闭；只有管理员明确配置后才放行。
type OpenAICompatibleConfig struct {
	ID                  string
	BaseURL             string
	APIKey              string
	APIKeyHeader        string
	AllowInsecureHTTP   bool
	AllowPrivateNetwork bool
	MaxResponseBytes    int64
	// MaxConcurrentRequests 为此 Provider 的最大并发推理数；0 使用默认值 64。
	MaxConcurrentRequests int
	// RequestLimiter 可用于同一 Provider 的热更新实例共享并发预算；nil 时会创建独立许可器。
	RequestLimiter *ConcurrencyLimiter
}

// OpenAICompatibleProvider 负责 OpenAI-compatible JSON 与 SSE 传输，不负责 Gateway 鉴权/模型路由。
type OpenAICompatibleProvider struct {
	id                  string
	baseURL             *url.URL
	apiKey              string
	apiKeyHeader        string
	allowPrivateNetwork bool
	maxResponseBytes    int64
	requestLimiter      *ConcurrencyLimiter
	client              *http.Client
}

func NewOpenAICompatibleProvider(cfg OpenAICompatibleConfig) (*OpenAICompatibleProvider, error) {
	return NewOpenAICompatibleProviderWithResolver(cfg, net.DefaultResolver)
}

// RemoteIPResolver 是 Provider 出站连接使用的 DNS 解析边界；生产默认使用系统 Resolver。
// 注入能力供可重复的 DNS 故障测试与受控运行环境使用，不通过外部配置暴露任意网络访问能力。
type RemoteIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// NewOpenAICompatibleProviderWithResolver 使用指定 Resolver 构造 Provider。
// resolver 为 nil 时回退系统 Resolver；所有解析结果仍经过原有 SSRF/特殊网段校验。
func NewOpenAICompatibleProviderWithResolver(cfg OpenAICompatibleConfig, resolver RemoteIPResolver) (*OpenAICompatibleProvider, error) {
	id := normalizeID(cfg.ID)
	if id == "" || len(id) > 64 {
		return nil, errors.New("provider ID is required and must be at most 64 characters")
	}
	if cfg.MaxConcurrentRequests < 0 || cfg.MaxConcurrentRequests > MaxOpenAICompatibleConcurrentRequests {
		return nil, fmt.Errorf("provider max_concurrent_requests must be between 0 and %d", MaxOpenAICompatibleConcurrentRequests)
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return nil, errors.New("provider ID contains unsupported characters")
		}
	}

	baseRaw := strings.TrimSpace(cfg.BaseURL)
	base, err := url.Parse(baseRaw)
	if err != nil || base == nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Opaque != "" {
		return nil, errors.New("provider base_url must be an absolute HTTP(S) URL without userinfo, query, or fragment")
	}
	base.Scheme = strings.ToLower(base.Scheme)
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, errors.New("provider base_url scheme must be HTTPS")
	}
	if base.Scheme == "http" && !cfg.AllowInsecureHTTP {
		return nil, errors.New("plain HTTP provider URL is disabled")
	}
	if strings.Contains(base.Path, "\\") {
		return nil, errors.New("provider base_url path is invalid")
	}
	for _, segment := range strings.Split(base.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("provider base_url path must not contain dot segments")
		}
	}
	if literalIP, parseErr := netip.ParseAddr(base.Hostname()); parseErr == nil && !cfg.AllowPrivateNetwork && forbiddenRemoteIP(literalIP) {
		return nil, errors.New("provider base_url resolves to a private or non-routable address")
	}

	apiKeyHeader := strings.ToLower(strings.TrimSpace(cfg.APIKeyHeader))
	if apiKeyHeader == "" {
		apiKeyHeader = "authorization"
	}
	switch apiKeyHeader {
	case "authorization", "api-key", "x-api-key":
	default:
		return nil, errors.New("provider api_key_header must be Authorization, api-key, or x-api-key")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("provider API key contains invalid characters")
	}
	maxConcurrentRequests := cfg.MaxConcurrentRequests
	if maxConcurrentRequests == 0 {
		maxConcurrentRequests = defaultOpenAICompatibleMaxConcurrentRequests
	}
	maxResponseBytes := cfg.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultRemoteResponseLimit
	}
	// 避免不合理的内存上限配置，也防止 maxResponseBytes+1 在读取时发生整数溢出。
	if maxResponseBytes > maxRemoteResponseLimit {
		return nil, fmt.Errorf("provider max_response_bytes must not exceed %d bytes (512 MiB)", maxRemoteResponseLimit)
	}
	requestLimiter := cfg.RequestLimiter
	if requestLimiter == nil {
		requestLimiter, err = NewConcurrencyLimiter(maxConcurrentRequests)
		if err != nil {
			return nil, err
		}
	}
	return &OpenAICompatibleProvider{
		id:                  id,
		baseURL:             base,
		apiKey:              cfg.APIKey,
		apiKeyHeader:        apiKeyHeader,
		allowPrivateNetwork: cfg.AllowPrivateNetwork,
		maxResponseBytes:    maxResponseBytes,
		requestLimiter:      requestLimiter,
		client:              newRestrictedHTTPClientWithResolver(cfg.AllowPrivateNetwork, resolver),
	}, nil
}

func (p *OpenAICompatibleProvider) ID() string { return p.id }

// ConcurrencyLimiter 返回该 Provider 正在使用的共享并发预算。
func (p *OpenAICompatibleProvider) ConcurrencyLimiter() *ConcurrencyLimiter {
	if p == nil {
		return nil
	}
	return p.requestLimiter
}

func (p *OpenAICompatibleProvider) Capabilities() Capabilities {
	return Capabilities{Protocol: "openai-compatible", ChatCompletions: true, Streaming: true}
}

func (p *OpenAICompatibleProvider) Forward(ctx context.Context, call InferenceCall) (result core.Result, retErr error) {
	// /v1/messages 使用 Anthropic Messages 错误信封。任何尚未提交响应的 Provider
	// 错误都在本适配器内按协议收口，避免回到通用 Gateway JSON 错误格式。
	defer func() {
		if retErr == nil || !isAnthropicMessagesAPIPath(call.Path) || call.Writer == nil {
			return
		}
		if call.WriterWritten() || result.ResponseBytes > 0 || result.Body != nil {
			return
		}
		status := http.StatusBadGateway
		code := "provider_upstream"
		hasProviderStatus := false
		var providerErr *ProviderError
		if errors.As(retErr, &providerErr) && providerErr != nil &&
			providerErr.StatusCode >= http.StatusBadRequest && providerErr.StatusCode <= 599 {
			status = providerErr.StatusCode
			hasProviderStatus = true
		}
		var gatewayErr *core.Error
		if errors.As(retErr, &gatewayErr) && gatewayErr != nil {
			if gatewayErr.Status >= http.StatusBadRequest && gatewayErr.Status <= 599 {
				status = gatewayErr.Status
			}
			if gatewayErr.Code != "" {
				code = gatewayErr.Code
			}
		}
		if mapped := ToCoreError(retErr); mapped != nil {
			if !hasProviderStatus && mapped.Status >= http.StatusBadRequest && mapped.Status <= 599 {
				status = mapped.Status
			}
			if mapped.Code != "" {
				code = mapped.Code
			}
		}
		code = providerErrorCodeForPath(call.Path, status, retErr)
		body := safeProviderErrorBodyForStatus(call.Path, status, code)
		call.Writer.Header().Set("Content-Type", "application/json")
		call.Writer.Header().Set("Cache-Control", "no-store")
		call.Writer.WriteHeader(status)
		n, writeErr := call.Writer.Write(body)
		result = core.Result{Status: status, UpstreamStatus: result.UpstreamStatus, ResponseBytes: int64(n), Body: body, Err: retErr}
		if writeErr != nil {
			retErr = writeErr
		} else if n != len(body) {
			retErr = io.ErrShortWrite
		}
	}()
	if p == nil || p.client == nil {
		return core.Result{}, &ProviderError{Kind: ErrorUnknown, StatusCode: http.StatusBadGateway}
	}
	if call.Writer == nil || call.Request == nil {
		return core.Result{}, &ProviderError{ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusBadRequest, Err: errors.New("request and writer are required")}
	}
	if isAnthropicMessagesAPIPath(call.Path) && int64(len(call.Body)) > AnthropicMessagesMaxRequestBodyBytes {
		return core.Result{}, &ProviderError{
			ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusRequestEntityTooLarge,
			Err: errors.New("Anthropic Messages request body exceeds the 32 MB limit"),
		}
	}
	endpoint, err := p.endpoint(call.Path)
	if err != nil {
		return core.Result{}, &ProviderError{ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusBadRequest, Err: err}
	}
	body, err := rewriteRemoteModel(call.Body, call.ModelID)
	if err != nil {
		return core.Result{}, &ProviderError{ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusBadRequest, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return core.Result{}, &ProviderError{ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusBadRequest, Err: err}
	}
	copyRemoteRequestHeaders(req.Header, call.Request.Header, call.Path)
	if p.apiKey != "" {
		switch p.apiKeyHeader {
		case "authorization":
			req.Header.Set("Authorization", "Bearer "+p.apiKey)
		case "api-key":
			req.Header.Set("api-key", p.apiKey)
		case "x-api-key":
			req.Header.Set("X-API-Key", p.apiKey)
		}
	}
	if err := p.acquireRequestSlot(ctx); err != nil {
		// 已取消/超时的请求沿用原错误语义；只有资源已满时才是 503 过载。
		if ctx.Err() != nil {
			return core.Result{UpstreamStatus: 0}, err
		}
		status, _, _ := core.Unpack(err)
		if status < http.StatusBadRequest || status > 599 {
			status = http.StatusServiceUnavailable
		}
		if call.Stream && call.WriterWritten() {
			// warm-start 已提交状态行时，不能再返回普通 JSON；必须以 SSE error 结束流。
			written := int64(0)
			if isResponsesAPIPath(call.Path) {
				written = p.writeStreamErrorIfCommitted(call, "provider_concurrency_limit", 0)
			} else {
				written = p.writeStreamErrorIfCommitted(call, "provider_concurrency_limit")
			}
			return core.Result{Status: status, UpstreamStatus: 0, ResponseBytes: written}, err
		}
		return core.Result{Status: status, UpstreamStatus: 0}, err
	}
	defer p.releaseRequestSlot()

	if call.Stream {
		return p.forwardStream(ctx, call, req)
	}
	return p.forwardJSON(ctx, call, req)
}

// acquireRequestSlot 使用 fail-fast 策略，防止 Provider 堆积无界等待中的推理请求。
func (p *OpenAICompatibleProvider) acquireRequestSlot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.requestLimiter == nil || !p.requestLimiter.TryAcquire() {
		return &core.Error{
			Status:  http.StatusServiceUnavailable,
			Code:    "provider_concurrency_limit",
			Message: "upstream provider concurrency limit reached",
		}
	}
	return nil
}

// releaseRequestSlot 在整个 JSON/SSE 响应读取完成或失败后释放共享许可。
func (p *OpenAICompatibleProvider) releaseRequestSlot() {
	if p != nil && p.requestLimiter != nil {
		p.requestLimiter.Release()
	}
}

// TestConnection 通过当前 Provider 的安全 HTTP Client 请求 /v1/models。
// 调用方应设置短 Context 超时；返回值仅包含模型 ID，不暴露 URL、响应正文或凭据。
func (p *OpenAICompatibleProvider) TestConnection(ctx context.Context) ([]string, error) {
	if p == nil || p.client == nil { return nil, errors.New("provider client is unavailable") }
	endpoint, err := p.endpoint("/v1/models")
	if err != nil { return nil, errors.New("provider connection test configuration is invalid") }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil { return nil, errors.New("provider connection test request is invalid") }
	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		switch p.apiKeyHeader {
		case "authorization": req.Header.Set("Authorization", "Bearer "+p.apiKey)
		case "api-key": req.Header.Set("api-key", p.apiKey)
		case "x-api-key": req.Header.Set("X-API-Key", p.apiKey)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil { return nil, errors.New("provider connection test timed out or was cancelled") }
		return nil, errors.New("provider connection failed; check URL, DNS, TLS, firewall and credentials")
	}
	defer resp.Body.Close()
	if isRedirectStatus(resp.StatusCode) { return nil, errors.New("provider redirects are disabled for security") }
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices { return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode) }
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1_048_577))
	if err != nil { return nil, errors.New("provider model response could not be read") }
	if len(body) > 1_048_576 { return nil, errors.New("provider model response exceeds the 1 MiB test limit") }
	var payload struct { Data []struct { ID string `json:"id"` } `json:"data"` }
	if err := json.Unmarshal(body, &payload); err != nil { return nil, errors.New("provider returned an invalid /v1/models response") }
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" && len(id) <= 255 && !strings.ContainsAny(id, "\r\n") { models = append(models, id) }
		if len(models) >= 100 { break }
	}
	return models, nil
}
func (p *OpenAICompatibleProvider) endpoint(rawPath string) (string, error) {
	requestURL, err := url.ParseRequestURI(strings.TrimSpace(rawPath))
	if err != nil || requestURL == nil || requestURL.IsAbs() || requestURL.Host != "" || !strings.HasPrefix(requestURL.Path, "/v1/") {
		return "", errors.New("only relative /v1/* provider paths are supported")
	}
	if strings.Contains(requestURL.Path, "\\") || strings.Contains(requestURL.Path, "//") {
		return "", errors.New("provider request path is invalid")
	}
	for _, segment := range strings.Split(requestURL.Path, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("provider request path must not contain dot segments")
		}
	}
	u := *p.baseURL
	basePath := strings.TrimRight(u.Path, "/")
	requestPath := requestURL.Path
	// BaseURL 可包含反向代理前缀，例如 /openai/v1；只要末尾是 /v1，就不能重复拼入版本段。
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(requestPath, "/v1/") {
		requestPath = strings.TrimPrefix(requestPath, "/v1")
	}
	u.Path = path.Join(basePath, requestPath)
	if strings.HasSuffix(requestURL.Path, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	u.RawQuery = requestURL.RawQuery
	return u.String(), nil
}

func rewriteRemoteModel(body []byte, modelID string) ([]byte, error) {
	if strings.TrimSpace(modelID) == "" {
		return append([]byte(nil), body...), nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, errors.New("inference request body must be a JSON object")
	}
	modelRaw, _ := json.Marshal(modelID)
	payload["model"] = modelRaw
	return json.Marshal(payload)
}

func copyRemoteRequestHeaders(dst, src http.Header, rawPath string) {
	anthropicMessages := isAnthropicMessagesAPIPath(rawPath)
	for key, values := range src {
		name := strings.ToLower(strings.TrimSpace(key))
		allowed := false
		switch name {
		case "content-type", "accept-language", "user-agent",
			"openai-beta", "openai-organization", "openai-project", "x-title":
			allowed = true
		default:
			// Anthropic Messages 是扩展式协议：未来新增的 anthropic-* 能力头也必须透传。
			allowed = anthropicMessages && strings.HasPrefix(name, "anthropic-")
		}
		if !allowed {
			continue
		}
		canonical := http.CanonicalHeaderKey(key)
		for _, value := range values {
			dst.Add(canonical, value)
		}
	}
	// 调用方 Authorization / X-API-Key 不在允许列表中；下面仅设置服务端 Provider 凭据。
	dst.Set("Content-Type", "application/json")
}

func copyRemoteResponseHeaders(dst, src http.Header, rawPath string) {
	for _, name := range []string{
		"Content-Type", "Cache-Control", "Retry-After",
		"X-Request-Id", "Request-Id", "X-Should-Retry", "OpenAI-Processing-Ms",
		"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset",
	} {
		for _, value := range src.Values(name) {
			dst.Add(name, value)
		}
	}
	if !isAnthropicMessagesAPIPath(rawPath) {
		return
	}
	// Claude Code 用这些响应头处理自动重试与使用量展示；新窗口维度也应可透传。
	for key, values := range src {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "anthropic-ratelimit-unified-") {
			continue
		}
		for _, value := range values {
			dst.Add(http.CanonicalHeaderKey(key), value)
		}
	}
}

// forwardJSON 限制非流式响应体大小；HTTP 非 2xx 的上游 JSON 错误体仍按兼容协议返回。
func (p *OpenAICompatibleProvider) forwardJSON(ctx context.Context, call InferenceCall, req *http.Request) (core.Result, error) {
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return core.Result{}, p.transportError(ctx, err)
	}
	defer resp.Body.Close()
	if call.OnHeaders != nil {
		call.OnHeaders(resp.Header, resp.StatusCode)
	}
	if isRedirectStatus(resp.StatusCode) {
		return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode}, NewHTTPError(p.id, resp.StatusCode, errors.New("provider redirects are disabled"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.maxResponseBytes+1))
	if err != nil {
		return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode}, p.transportError(ctx, err)
	}
	if int64(len(body)) > p.maxResponseBytes {
		return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode}, &ProviderError{ProviderID: p.id, Kind: ErrorUpstream, StatusCode: resp.StatusCode, Err: errors.New("provider response exceeds configured size limit")}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		upstreamErr := NewHTTPError(p.id, resp.StatusCode, errors.New("provider returned a non-success HTTP status"))
		copyRemoteResponseHeaders(call.Writer.Header(), resp.Header, call.Path)
		call.Writer.Header().Set("Cache-Control", "no-store")
		var errorBody []byte
		if isAnthropicMessagesAPIPath(call.Path) {
			// Claude Code 依靠原始错误 message 做能力降级重试；Anthropic Messages 路径保留上游错误正文。
			errorBody = body
			if call.Writer.Header().Get("Content-Type") == "" {
				call.Writer.Header().Set("Content-Type", "application/json")
			}
		} else {
			call.Writer.Header().Set("Content-Type", "application/json")
			errorBody = safeProviderErrorBodyForStatus(call.Path, resp.StatusCode, providerErrorCodeForPath(call.Path, resp.StatusCode, upstreamErr))
		}
		call.Writer.WriteHeader(resp.StatusCode)
		n, writeErr := call.Writer.Write(errorBody)
		result := core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: int64(n), Body: errorBody}
		if writeErr != nil {
			return result, writeErr
		}
		if n != len(errorBody) {
			return result, io.ErrShortWrite
		}
		return result, upstreamErr
	}
	copyRemoteResponseHeaders(call.Writer.Header(), resp.Header, call.Path)
	call.Writer.WriteHeader(resp.StatusCode)
	n, writeErr := call.Writer.Write(body)
	result := core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: int64(n), Body: body}
	if writeErr != nil {
		return result, writeErr
	}
	if n != len(body) {
		return result, io.ErrShortWrite
	}
	return result, nil
}

func (p *OpenAICompatibleProvider) forwardStream(ctx context.Context, call InferenceCall, req *http.Request) (core.Result, error) {
	req.Header.Set("Accept", "text/event-stream")

	// Responses API 的错误事件带 sequence_number。只旁路观察上游 SSE 序号，
	// 不改写转发给客户端的原始数据；行/事件解析缓冲区有硬上限。
	var responseSequence *responsesSequenceTracker
	if isResponsesAPIPath(call.Path) {
		responseSequence = &responsesSequenceTracker{}
	}
	writeStreamError := func(code string) int64 {
		if responseSequence != nil {
			return p.writeStreamErrorIfCommitted(call, code, responseSequence.NextSequence())
		}
		return p.writeStreamErrorIfCommitted(call, code)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		transportErr := p.transportError(ctx, err)
		written := writeStreamError(streamErrorCode(transportErr))
		return core.Result{ResponseBytes: written}, transportErr
	}
	defer resp.Body.Close()
	if call.OnHeaders != nil {
		call.OnHeaders(resp.Header, resp.StatusCode)
	}
	if isRedirectStatus(resp.StatusCode) {
		written := writeStreamError("provider_redirect_rejected")
		return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: written}, NewHTTPError(p.id, resp.StatusCode, errors.New("provider redirects are disabled"))
	}
	// 上游非 2xx 且响应尚未提交时，Anthropic Messages 必须保留原始错误正文，供 Claude Code 识别能力拒绝。
	// 其他协议继续使用统一脱敏错误；若已经提交，HTTP 状态不可逆，只能以 SSE error 事件收口。
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		upstreamErr := NewHTTPError(p.id, resp.StatusCode, errors.New("provider returned a non-success HTTP status"))
		code := providerErrorCodeForPath(call.Path, resp.StatusCode, upstreamErr)
		if call.WriterWritten() {
			written := writeStreamError(code)
			return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: written}, upstreamErr
		}

		copyRemoteResponseHeaders(call.Writer.Header(), resp.Header, call.Path)
		call.Writer.Header().Set("Cache-Control", "no-store")
		var body []byte
		if isAnthropicMessagesAPIPath(call.Path) {
			upstreamBody, readErr := io.ReadAll(io.LimitReader(resp.Body, p.maxResponseBytes+1))
			if readErr == nil && int64(len(upstreamBody)) <= p.maxResponseBytes {
				body = upstreamBody
				if call.Writer.Header().Get("Content-Type") == "" {
					call.Writer.Header().Set("Content-Type", "application/json")
				}
			} else {
				call.Writer.Header().Set("Content-Type", "application/json")
				body = safeProviderErrorBodyForStatus(call.Path, resp.StatusCode, code)
			}
		} else {
			call.Writer.Header().Set("Content-Type", "application/json")
			body = safeProviderErrorBodyForStatus(call.Path, resp.StatusCode, code)
		}
		call.Writer.WriteHeader(resp.StatusCode)
		n, writeErr := call.Writer.Write(body)
		result := core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: int64(n), Body: body}
		if writeErr != nil {
			return result, writeErr
		}
		if n != len(body) {
			return result, io.ErrShortWrite
		}
		return result, upstreamErr
	}
	copyRemoteResponseHeaders(call.Writer.Header(), resp.Header, call.Path)
	call.Writer.WriteHeader(resp.StatusCode)
	if flusher, ok := call.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	writer := call.ClientWriter
	if writer == nil {
		writer = func(chunk []byte) error {
			_, err := call.Writer.Write(chunk)
			return err
		}
	}
	reader := make([]byte, 32*1024)
	var total int64
	firstByte := false
	for {
		n, readErr := resp.Body.Read(reader)
		if n > 0 {
			if !firstByte {
				firstByte = true
				if call.OnFirstByte != nil {
					call.OnFirstByte()
				}
			}
			chunk := append([]byte(nil), reader[:n]...)
			if err := writer(chunk); err != nil {
				return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: total}, err
			}
			total += int64(len(chunk))
			if responseSequence != nil {
				responseSequence.Observe(chunk)
			}
			if flusher, ok := call.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
			if call.OnChunk != nil {
				call.OnChunk(chunk)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			transportErr := p.transportError(ctx, readErr)
			errorBytes := writeStreamError(streamErrorCode(transportErr))
			return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: total + errorBytes}, transportErr
		}
	}
	return core.Result{Status: resp.StatusCode, UpstreamStatus: resp.StatusCode, ResponseBytes: total}, nil
}

func (p *OpenAICompatibleProvider) transportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return NewHTTPError(p.id, http.StatusGatewayTimeout, ctxErr)
		}
		return ctxErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return NewHTTPError(p.id, http.StatusGatewayTimeout, err)
	}
	return NewHTTPError(p.id, http.StatusBadGateway, err)
}

func streamErrorCode(err error) string {
	if mapped := ToCoreError(err); mapped != nil && mapped.Code != "" {
		return mapped.Code
	}
	var gatewayErr *core.Error
	if errors.As(err, &gatewayErr) && gatewayErr != nil && gatewayErr.Code != "" {
		return gatewayErr.Code
	}
	return "provider_stream_interrupted"
}

// providerErrorCodeForPath 用真实 HTTP 状态补全 Anthropic 错误分类；通用 Provider 错误码可能将 403 当作鉴权失败。
func providerErrorCodeForPath(rawPath string, status int, err error) string {
	if isAnthropicMessagesAPIPath(rawPath) {
		switch status {
		case http.StatusPaymentRequired:
			return "provider_billing"
		case http.StatusForbidden:
			return "provider_permission"
		case http.StatusConflict:
			return "provider_conflict"
		case http.StatusRequestEntityTooLarge:
			return "provider_request_too_large"
		case 529:
			return "provider_overloaded"
		}
	}
	return streamErrorCode(err)
}

func (p *OpenAICompatibleProvider) writeStreamErrorIfCommitted(call InferenceCall, code string, sequenceNumber ...int64) int64 {
	if !call.WriterWritten() {
		return 0
	}
	var sequence int64
	if len(sequenceNumber) > 0 {
		sequence = sequenceNumber[0]
	}
	body := safeProviderStreamErrorBody(call.Path, code, sequence)
	chunk := append([]byte("event: error\ndata: "), body...)
	chunk = append(chunk, []byte("\n\n")...)
	// 用原始 ResponseWriter 尝试发送终止事件，不复用尊重 request context 的 ClientWriter：
	// 超时后 context 已结束，但只要连接仍在，客户端仍应收到明确的 SSE 错误。
	n, err := call.Writer.Write(chunk)
	if err != nil || n != len(chunk) {
		return 0
	}
	if flusher, ok := call.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	if call.OnChunk != nil {
		call.OnChunk(chunk)
	}
	return int64(n)
}

// isResponsesAPIPath 判断本次请求是否使用 Responses API；查询参数不参与协议判断。
func isResponsesAPIPath(rawPath string) bool {
	rawPath = strings.TrimSpace(strings.SplitN(rawPath, "?", 2)[0])
	rawPath = strings.TrimRight(rawPath, "/")
	return rawPath == "/v1/responses" || strings.HasSuffix(rawPath, "/v1/responses")
}

const (
	// 序号观察器只解析轻量 SSE 元数据，绝不改写实际转发的字节。
	maxResponsesSSELineBytes  = 64 << 10
	maxResponsesSSEEventBytes = 256 << 10
)

type responsesSequenceTracker struct {
	pending    []byte
	data       []byte
	skipLine   bool
	skipEvent  bool
	last       int64
	hasLast    bool
}

// Observe 从已经交付给客户端的上游原始块里旁路读取 sequence_number。
// 对不规范/超大 SSE 事件直接放弃该事件的序号解析，保证解析缓冲区有界。
func (t *responsesSequenceTracker) Observe(chunk []byte) {
	if t == nil {
		return
	}
	for len(chunk) > 0 {
		if t.skipLine {
			newline := bytes.IndexByte(chunk, '\n')
			if newline < 0 {
				return
			}
			chunk = chunk[newline+1:]
			t.skipLine = false
			continue
		}

		newline := bytes.IndexByte(chunk, '\n')
		if newline < 0 {
			if len(t.pending)+len(chunk) > maxResponsesSSELineBytes {
				t.pending = t.pending[:0]
				t.skipLine = true
				t.skipEvent = true
				t.data = t.data[:0]
				return
			}
			t.pending = append(t.pending, chunk...)
			return
		}

		if len(t.pending)+newline > maxResponsesSSELineBytes {
			t.pending = t.pending[:0]
			t.skipEvent = true
			t.data = t.data[:0]
		} else {
			t.pending = append(t.pending, chunk[:newline]...)
			t.consumeLine(t.pending)
			t.pending = t.pending[:0]
		}
		chunk = chunk[newline+1:]
	}
}

func (t *responsesSequenceTracker) consumeLine(line []byte) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		if !t.skipEvent && len(t.data) > 0 {
			var event struct {
				SequenceNumber *int64 `json:"sequence_number"`
			}
			if json.Unmarshal(t.data, &event) == nil && event.SequenceNumber != nil {
				t.last = *event.SequenceNumber
				t.hasLast = true
			}
		}
		t.data = t.data[:0]
		t.skipEvent = false
		return
	}
	if t.skipEvent || len(line) < len("data:") || !bytes.Equal(line[:len("data:")], []byte("data:")) {
		return
	}

	value := line[len("data:"):]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	extra := len(value)
	if len(t.data) > 0 {
		extra++
	}
	if len(t.data)+extra > maxResponsesSSEEventBytes {
		t.data = t.data[:0]
		t.skipEvent = true
		return
	}
	if len(t.data) > 0 {
		t.data = append(t.data, '\n')
	}
	t.data = append(t.data, value...)
}

// NextSequence 返回错误事件应使用的下一个序号。尚未收到有效上游事件时从 0 开始。
func (t *responsesSequenceTracker) NextSequence() int64 {
	if t == nil || !t.hasLast {
		return 0
	}
	return t.last + 1
}

// isAnthropicMessagesAPIPath 判断 Anthropic Messages 兼容入口；查询参数不参与协议判断。
func isAnthropicMessagesAPIPath(rawPath string) bool {
	rawPath = strings.TrimSpace(strings.SplitN(rawPath, "?", 2)[0])
	rawPath = strings.TrimRight(rawPath, "/")
	return rawPath == "/v1/messages" || strings.HasPrefix(rawPath, "/v1/messages/")
}

func anthropicErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	default:
		if status >= 400 && status < 500 {
			return "invalid_request_error"
		}
		return "api_error"
	}
}

// AnthropicErrorBodyForStatus 生成网关入口拒绝时符合 Anthropic Messages 格式的错误响应。
func AnthropicErrorBodyForStatus(status int, message string) []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type": anthropicErrorTypeForStatus(status), "message": message,
		},
	})
	return body
}

// anthropicErrorType 将 Gateway 的稳定错误码映射到 Anthropic Messages 错误类型。
func anthropicErrorType(code string) string {
	switch code {
	case "provider_authentication":
		return "authentication_error"
	case "provider_billing":
		return "billing_error"
	case "provider_permission":
		return "permission_error"
	case "provider_rate_limit":
		return "rate_limit_error"
	case "provider_model_not_found":
		return "not_found_error"
	case "provider_conflict":
		return "conflict_error"
	case "provider_request_too_large":
		return "request_too_large"
	case "provider_invalid_request":
		return "invalid_request_error"
	case "provider_timeout":
		return "timeout_error"
	case "provider_overloaded":
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// safeProviderErrorBodyForPath 按协议生成无上游正文可用时的安全错误 JSON。
func safeProviderErrorBodyForPath(rawPath, code string) []byte {
	if isAnthropicMessagesAPIPath(rawPath) {
		body, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"error": map[string]string{
				"type": anthropicErrorType(code), "message": "upstream provider request failed",
			},
		})
		return body
	}
	return safeProviderErrorBody(code)
}

func safeProviderErrorBodyForStatus(rawPath string, status int, code string) []byte {
	if isAnthropicMessagesAPIPath(rawPath) {
		return AnthropicErrorBodyForStatus(status, "upstream provider request failed")
	}
	return safeProviderErrorBody(code)
}

// safeProviderStreamErrorBody 根据请求协议生成安全终止事件。
// Chat Completions 使用 error 包装；Responses API 与 Anthropic Messages 使用各自的事件信封。
func safeProviderStreamErrorBody(rawPath, code string, sequenceNumber int64) []byte {
	if isAnthropicMessagesAPIPath(rawPath) {
		body, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"error": map[string]string{
				"type": anthropicErrorType(code), "message": "upstream provider request failed",
			},
		})
		return body
	}
	if isResponsesAPIPath(rawPath) {
		body, _ := json.Marshal(map[string]interface{}{
			"type": "error", "code": code, "message": "upstream provider request failed",
			"param": nil, "sequence_number": sequenceNumber,
		})
		return body
	}
	return safeProviderErrorBody(code)
}

// safeProviderErrorBody 生成稳定且不包含上游原始文本的 Chat Completions 公共错误正文。
func safeProviderErrorBody(code string) []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"error": map[string]string{
			"message": "upstream provider request failed",
			"type":    "gateway_error",
			"code":    code,
		},
	})
	return body
}

func (call InferenceCall) WriterWritten() bool {
	if w, ok := call.Writer.(interface{ HeaderWritten() bool }); ok {
		return w.HeaderWritten()
	}
	return false
}

func isRedirectStatus(status int) bool {
	return status >= 300 && status < 400
}

func newRestrictedHTTPClient(allowPrivateNetwork bool) *http.Client {
	return newRestrictedHTTPClientWithResolver(allowPrivateNetwork, net.DefaultResolver)
}

// newRestrictedHTTPClientWithResolver 仅用于把 DNS 故障变为可重复测试；正常 Provider 始终使用系统 Resolver。
func newRestrictedHTTPClientWithResolver(allowPrivateNetwork bool, resolver RemoteIPResolver) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := &net.Dialer{Timeout: remoteDialTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// 自定义 TLS 配置与 DialContext 后显式启用 HTTP/2，兼容只优先提供 h2 的服务。
		ForceAttemptHTTP2:      true,
		Proxy:                  nil,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    remoteTLSHandshakeTimeout,
		ResponseHeaderTimeout:  remoteResponseHeaderTimeout,
		IdleConnTimeout:        remoteIdleConnTimeout,
		MaxIdleConns:            remoteMaxIdleConnections,
		MaxIdleConnsPerHost:     remoteMaxIdleConnectionsPerHost,
		MaxConnsPerHost:         remoteMaxConnectionsPerHost,
		ExpectContinueTimeout:   time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			var addresses []netip.Addr
			if ip, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
				addresses = []netip.Addr{ip.Unmap()}
			} else {
				// DNS 解析单独设置超时；它不能只依赖可能长达一小时的推理请求 Context。
				lookupCtx, cancel := context.WithTimeout(ctx, remoteDialTimeout)
				addresses, err = resolver.LookupNetIP(lookupCtx, "ip", host)
				cancel()
				if err != nil {
					return nil, err
				}
			}
			var lastErr error
			blocked := 0
			for _, addr := range addresses {
				addr = addr.Unmap()
				if !allowPrivateNetwork && forbiddenRemoteIP(addr) {
					blocked++
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			if len(addresses) > 0 && blocked == len(addresses) {
				return nil, errors.New("provider host resolves only to private or non-routable addresses")
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errors.New("provider host has no usable addresses")
		},
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// forbiddenRemotePrefixes 覆盖不可直接作为公网 Provider 目标的特殊用途地址段。
// 规则保守处理：宁可要求管理员显式放行，也不把文档网段、过渡隧道或基准测试地址当作公开服务。
var forbiddenRemotePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func forbiddenRemoteIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	if netip.MustParsePrefix("100.64.0.0/10").Contains(addr) {
		return true
	}
	for _, prefix := range forbiddenRemotePrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
