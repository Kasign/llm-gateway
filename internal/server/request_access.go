package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

type callRecordKey struct{}

// callRecordFlag 标记这条 HTTP 请求已经由推理或 /v1/models 写过调用记录。
// 中间件在请求结束后只补记还没人写的请求，避免同一条记两次。
type callRecordFlag struct {
	recorded bool
}

// withCallRecord 给每个进入网关的 HTTP 请求补一条调用记录。
// /v1 推理和 /v1/models 自己会写更完整的记录，这里看到标记后不再重复。
// 已登录管理台对自己的页面和 /api 轮询不记，否则会把客户端调用挤出列表。
// 静态资源和健康检查同样不记。
func (s *Server) withCallRecord(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flag := &callRecordFlag{}
		r = r.WithContext(context.WithValue(r.Context(), callRecordKey{}, flag))
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		if flag.recorded || skipCallRecord(r.URL.Path, s.consoleRequest(r)) {
			return
		}
		s.recordCall(r, rec, start)
	})
}

func (s *Server) markCallRecorded(ctx context.Context) {
	flag, _ := ctx.Value(callRecordKey{}).(*callRecordFlag)
	if flag != nil {
		flag.recorded = true
	}
}

// consoleRequest 为真时，这是已登录管理台发出的页面或 /api 请求。
func (s *Server) consoleRequest(r *http.Request) bool {
	path := r.URL.Path
	if path != "/" && !strings.HasPrefix(path, "/api/") {
		return false
	}
	return s.adminAuthorized(r.Context(), r)
}

// skipCallRecord 决定哪些请求不进调用记录。
// 未登录打到 / 或 /api 的请求仍然记录，用来看到打错地址的客户端。
func skipCallRecord(path string, console bool) bool {
	if path == "/health" || path == "/favicon.ico" || strings.HasPrefix(path, "/static/") {
		return true
	}
	if path == "/" || strings.HasPrefix(path, "/api/") {
		return console
	}
	return false
}

func (s *Server) recordCall(r *http.Request, rec *statusRecorder, start time.Time) {
	if s.audit == nil {
		return
	}
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	end := time.Now()
	state := "success"
	code, msg := "", ""
	if status >= 400 {
		state = "error"
		code = "http_status"
		msg = accessErrorMessage(status, rec.errBody)
	}
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	id := requestID(r.Context())
	if id == "" {
		id = newID()
	}
	item := store.RequestLog{
		RequestID: id, ClientIP: clientIP(r, s.trustedProxyRanges), ClientName: requestClientName(r), UserAgent: r.UserAgent(),
		Endpoint: clipRunes(path, 128), RequestURL: requestPublicURL(r, s.trustedProxyRanges), Method: r.Method,
		Status: state, HTTPStatus: status, StartedAt: start, FinishedAt: &end,
		LatencyMS: end.Sub(start).Milliseconds(), ResponseBytes: rec.written, ErrorCode: code, ErrorMessage: msg,
	}
	if key, authCode := s.authenticateOutcome(r.Context(), r); authCode == "" && key != nil {
		item.APIKeyID = key.ID
		item.APIKeyName = key.Name
	}
	s.audit.CreateRequest(item)
	s.audit.FinishRequest(item)
	s.invalidateRequestListCache()
}

// requestPublicURL 是客户端用来访问网关的完整 URL。
// Host 优先用反代传来的 X-Forwarded-Host，否则用请求的 Host 头，因此能区分域名和 IP。
func requestPublicURL(r *http.Request, trusted []netip.Prefix) string {
	if r == nil || r.URL == nil {
		return ""
	}
	// X-Forwarded-* 仅在 TCP 对端命中可信代理白名单时才可用于审计 URL。
	// 直接访问者可以自行伪造这些头，不能让审计记录显示成攻击者指定的域名或协议。
	trustForwarded := false
	if peer, ok := parseRemoteAddrIP(r.RemoteAddr); ok {
		trustForwarded = isTrustedProxy(peer, trusted)
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := ""
	if trustForwarded {
		if proto := strings.ToLower(forwardedValue(r.Header.Get("X-Forwarded-Proto"))); proto == "http" || proto == "https" {
			scheme = proto
		}
		host = forwardedValue(r.Header.Get("X-Forwarded-Host"))
	}
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		host = r.URL.Host
	}
	uri := r.URL.RequestURI()
	if query := r.URL.RawQuery; query != "" {
		// 审计展示 URL 仅脱敏凭据类 query 值；原始 URL/body 仍按原始请求转发，不修改推理语义。
		parts := strings.SplitN(uri, "?", 2)
		uri = parts[0] + "?" + redactSensitiveQuery(query)
	}
	if uri == "" {
		uri = "/"
	}
	if host == "" {
		return clipRunes(uri, 2048)
	}
	return clipRunes(scheme+"://"+host+uri, 2048)
}

// redactSensitiveQuery 在请求日志 URL 中只替换凭据参数值，保留原始顺序、编码和其它诊断参数。
func redactSensitiveQuery(rawQuery string) string {
	if rawQuery == "" {
		return rawQuery
	}
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		rawKey := part
		eq := strings.IndexByte(part, '=')
		if eq >= 0 {
			rawKey = part[:eq]
		}
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			key = rawKey
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if !isSensitiveQueryKey(key) {
			continue
		}
		if eq >= 0 {
			parts[i] = part[:eq+1] + "***"
		} else {
			parts[i] = rawKey + "=***"
		}
	}
	return strings.Join(parts, "&")
}

func isSensitiveQueryKey(key string) bool {
	switch key {
	case "authorization", "auth", "token", "access_token", "refresh_token", "id_token",
		"api_key", "api-key", "apikey", "x-api-key", "key",
		"client_secret", "secret", "secret_key", "private_key", "password", "passwd",
		"signature", "sig", "credential", "credentials", "x-amz-credential",
		"x-amz-signature", "x-amz-security-token":
		return true
	}
	return strings.HasSuffix(key, "_token") ||
		strings.HasSuffix(key, "_secret") ||
		strings.HasSuffix(key, "_password") ||
		strings.HasSuffix(key, "_signature") ||
		strings.HasSuffix(key, "_api_key")
}

func forwardedValue(v string) string {
	v = strings.TrimSpace(strings.Split(v, ",")[0])
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return ""
	}
	return v
}

func accessErrorMessage(status int, body []byte) string {
	sample := strings.TrimSpace(string(body))
	if msg := jsonErrorMessage(sample); msg != "" {
		return msg
	}
	if sample == "" {
		text := http.StatusText(status)
		if text == "" {
			return "HTTP error"
		}
		return text
	}
	return clipRunes(strings.Join(strings.Fields(sample), " "), 300)
}

func jsonErrorMessage(sample string) string {
	if !strings.HasPrefix(sample, "{") {
		return ""
	}
	var wrap struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(sample), &wrap) != nil {
		return ""
	}
	return strings.TrimSpace(wrap.Error.Message)
}

func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// statusRecorder 记下最终状态码、写出字节，以及错误响应的一小段正文。
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	errBody []byte
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if s.status >= 400 && len(s.errBody) < 512 {
		remain := 512 - len(s.errBody)
		if len(p) < remain {
			remain = len(p)
		}
		s.errBody = append(s.errBody, p[:remain]...)
	}
	n, err := s.ResponseWriter.Write(p)
	s.written += int64(n)
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}
