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
		return core.Result{Status: resp.StatusCode, ResponseBytes: written}, NewHTTPError(p.id, resp.StatusCode, errors.New("provider redirects are disabled"))
	}
	// 上游非 2xx 时，不透传供应商原始错误正文，防止暴露内部诊断或凭据上下文。
	// 如果 Gateway 已提交响应头（例如 warmStart 的 200），用 SSE error 事件收口；
	// 如果尚未提交，则返回保留上游 HTTP 状态的安全 JSON 错误。
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		upstreamErr := NewHTTPError(p.id, resp.StatusCode, errors.New("provider returned a non-success HTTP status"))
		code := streamErrorCode(upstreamErr)
		if call.WriterWritten() {
			written := writeStreamError(code)
			return core.Result{Status: resp.StatusCode, ResponseBytes: written}, upstreamErr
		}

		copyRemoteResponseHeaders(call.Writer.Header(), resp.Header)
		call.Writer.Header().Set("Content-Type", "application/json")
		call.Writer.Header().Set("Cache-Control", "no-store")
		body := safeProviderErrorBody(code)
		call.Writer.WriteHeader(resp.StatusCode)
		n, writeErr := call.Writer.Write(body)
		result := core.Result{Status: resp.StatusCode, ResponseBytes: int64(n), Body: body}
		if writeErr != nil {
			return result, writeErr
		}
		if n != len(body) {
			return result, io.ErrShortWrite
		}
		return result, upstreamErr
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

// safeProviderStreamErrorBody 根据请求协议生成安全终止事件。
// Chat Completions 使用 error 包装；Responses API 要求 error 事件数据顶层带 type/code/message/param/sequence_number。
func safeProviderStreamErrorBody(rawPath, code string, sequenceNumber int64) []byte {
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
