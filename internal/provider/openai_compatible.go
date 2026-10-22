package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/core"
)

const defaultRemoteResponseLimit int64 = 32 << 20

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
}

// OpenAICompatibleProvider 负责 OpenAI-compatible JSON 与 SSE 传输，不负责 Gateway 鉴权/模型路由。
type OpenAICompatibleProvider struct {
	id                  string
	baseURL             *url.URL
	apiKey              string
	apiKeyHeader        string
	allowPrivateNetwork bool
	maxResponseBytes    int64
	client              *http.Client
}

func NewOpenAICompatibleProvider(cfg OpenAICompatibleConfig) (*OpenAICompatibleProvider, error) {
	id := normalizeID(cfg.ID)
	if id == "" || len(id) > 64 {
		return nil, errors.New("provider ID is required and must be at most 64 characters")
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
	maxResponseBytes := cfg.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultRemoteResponseLimit
	}
	return &OpenAICompatibleProvider{
		id:                  id,
		baseURL:             base,
		apiKey:              cfg.APIKey,
		apiKeyHeader:        apiKeyHeader,
		allowPrivateNetwork: cfg.AllowPrivateNetwork,
		maxResponseBytes:    maxResponseBytes,
		client:              newRestrictedHTTPClient(cfg.AllowPrivateNetwork),
	}, nil
}

func (p *OpenAICompatibleProvider) ID() string { return p.id }

func (p *OpenAICompatibleProvider) Capabilities() Capabilities {
	return Capabilities{Protocol: "openai-compatible", ChatCompletions: true, Streaming: true}
}

func (p *OpenAICompatibleProvider) Forward(ctx context.Context, call InferenceCall) (core.Result, error) {
	if p == nil || p.client == nil {
		return core.Result{}, &ProviderError{Kind: ErrorUnknown, StatusCode: http.StatusBadGateway}
	}
	if call.Writer == nil || call.Request == nil {
		return core.Result{}, &ProviderError{ProviderID: p.id, Kind: ErrorInvalidRequest, StatusCode: http.StatusBadRequest, Err: errors.New("request and writer are required")}
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
	copyRemoteRequestHeaders(req.Header, call.Request.Header)
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
	if call.Stream {
		return p.forwardStream(ctx, call, req)
	}
	return p.forwardJSON(ctx, call, req)
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
	if basePath == "/v1" && strings.HasPrefix(requestPath, "/v1/") {
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

func copyRemoteRequestHeaders(dst, src http.Header) {
	for _, name := range []string{
		"content-type", "accept-language", "user-agent",
		"openai-beta", "openai-organization", "openai-project", "x-title",
	} {
		for key, values := range src {
			if strings.EqualFold(key, name) {
				for _, value := range values {
					dst.Add(http.CanonicalHeaderKey(name), value)
				}
			}
		}
	}
	dst.Set("Content-Type", "application/json")
}

func copyRemoteResponseHeaders(dst, src http.Header) {
	for _, name := range []string{
		"Content-Type", "Cache-Control", "Retry-After",
		"X-Request-Id", "Request-Id", "OpenAI-Processing-Ms",
		"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset",
	} {
		for _, value := range src.Values(name) {
			dst.Add(name, value)
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
		return core.Result{Status: resp.StatusCode}, NewHTTPError(p.id, resp.StatusCode, errors.New("provider redirects are disabled"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.maxResponseBytes+1))
	if err != nil {
		return core.Result{Status: resp.StatusCode}, p.transportError(ctx, err)
	}
	if int64(len(body)) > p.maxResponseBytes {
		return core.Result{Status: resp.StatusCode}, &ProviderError{ProviderID: p.id, Kind: ErrorUpstream, StatusCode: resp.StatusCode, Err: errors.New("provider response exceeds configured size limit")}
	}
	copyRemoteResponseHeaders(call.Writer.Header(), resp.Header)
	call.Writer.WriteHeader(resp.StatusCode)
	n, writeErr := call.Writer.Write(body)
	result := core.Result{Status: resp.StatusCode, ResponseBytes: int64(n), Body: body}
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
	resp, err := p.client.Do(req)
	if err != nil {
		transportErr := p.transportError(ctx, err)
		written := p.writeStreamErrorIfCommitted(call, streamErrorCode(transportErr))
		return core.Result{ResponseBytes: written}, transportErr
	}
	defer resp.Body.Close()
	if call.OnHeaders != nil {
		call.OnHeaders(resp.Header, resp.StatusCode)
	}
	if isRedirectStatus(resp.StatusCode) {
		written := p.writeStreamErrorIfCommitted(call, "provider_redirect_rejected")
		return core.Result{Status: resp.StatusCode, ResponseBytes: written}, NewHTTPError(p.id, resp.StatusCode, errors.New("provider redirects are disabled"))
	}
	// Gateway warmStart 可能已提交 HTTP 200；若此时上游返回 401/429/5xx，必须发送 SSE error 而非混入 JSON 错误正文。
	if (resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices) && call.WriterWritten() {
		upstreamErr := NewHTTPError(p.id, resp.StatusCode, errors.New("provider returned a non-success HTTP status"))
		written := p.writeStreamErrorIfCommitted(call, streamErrorCode(upstreamErr))
		return core.Result{Status: resp.StatusCode, ResponseBytes: written}, upstreamErr
	}
	copyRemoteResponseHeaders(call.Writer.Header(), resp.Header)
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
				return core.Result{Status: resp.StatusCode, ResponseBytes: total}, err
			}
			total += int64(len(chunk))
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
			errorBytes := p.writeStreamErrorIfCommitted(call, streamErrorCode(transportErr))
			return core.Result{Status: resp.StatusCode, ResponseBytes: total + errorBytes}, transportErr
		}
	}
	return core.Result{Status: resp.StatusCode, ResponseBytes: total}, nil
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
	return "provider_stream_interrupted"
}

func (p *OpenAICompatibleProvider) writeStreamErrorIfCommitted(call InferenceCall, code string) int64 {
	if !call.WriterWritten() {
		return 0
	}
	body, _ := json.Marshal(map[string]interface{}{
		"error": map[string]string{
			"message": "upstream provider request failed",
			"type":    "gateway_error",
			"code":    code,
		},
	})
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
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			var addresses []netip.Addr
			if ip, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
				addresses = []netip.Addr{ip.Unmap()}
			} else {
				addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
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
			if blocked == len(addresses) {
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

func forbiddenRemoteIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	return netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}
