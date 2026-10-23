package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/core"
)

// secretHeaders 使用小写规范化的名称，覆盖常见云 Provider、反向代理与会话凭据头。
// 展示仍保留未知 Header；敏感头的值在写入实时详情和 payload 之前统一打码。
var secretHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true,
	"x-api-key": true, "api-key": true, "apikey": true, "x-admin-key": true,
	"x-provider-key": true, "x-auth-key": true, "x-goog-api-key": true,
	"ocp-apim-subscription-key": true,
	"cookie": true, "set-cookie": true,
	"token": true, "access-token": true, "refresh-token": true,
	"access_token": true, "refresh_token": true, "x-auth-token": true,
}

func isSecretHeader(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if secretHeaders[k] {
		return true
	}

	// 统一连字符/下划线，兼容不同 Provider 的自定义凭据命名。
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(k)
	for _, marker := range []string{
		"authorization", "api_key", "apikey", "access_token", "refresh_token",
		"secret", "password", "credential", "signature", "private_key", "bearer",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}

	// 常见厂商自定义密钥头不一定带 api 字样；保留 Model-Key、Idempotency-Key 等普通字段。
	if isSensitiveProviderKeyName(normalized) {
		return true
	}
	// 兼容标准 Token/Secret/Signature Header 后缀。
	return strings.HasSuffix(k, "-token") || strings.HasSuffix(k, "_token") ||
		strings.HasSuffix(k, "-secret") || strings.HasSuffix(k, "_secret") ||
		strings.HasSuffix(k, "-signature") || strings.HasSuffix(k, "_signature")
}

// isSensitiveProviderKeyName 识别带厂商/凭据标识的 *_key 命名；避免把所有 *-key 一概打码。
func isSensitiveProviderKeyName(normalized string) bool {
	if !strings.HasSuffix(normalized, "_key") {
		return false
	}
	for _, marker := range []string{
		"auth", "provider", "openai", "anthropic", "azure", "cloud", "google", "goog",
		"aws", "amz", "access", "secret", "credential", "private", "bearer",
		"deepseek", "gemini", "groq", "cohere", "mistral", "xai", "together",
		"fireworks", "perplexity", "replicate", "huggingface", "moonshot", "zhipu",
		"minimax", "baidu", "qwen", "doubao", "siliconflow", "bedrock", "session",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func redactHeaderValue(key, value string) string {
	if !isSecretHeader(key) {
		return value
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") {
		return "Bearer ***"
	}
	return "***"
}

func joinHeaderValues(vs []string) string {
	parts := make([]string, 0, len(vs))
	for _, v := range vs {
		v = strings.TrimSpace(v)
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// flattenHeaders 全量拷贝 Header。名单只打码密钥，不丢未知字段。
func flattenHeaders(h map[string][]string) map[string]string {
	if len(h) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(h))
	for k, vs := range h {
		v := joinHeaderValues(vs)
		if v == "" {
			continue
		}
		out[k] = redactHeaderValue(k, v)
	}
	return out
}

func headersJSON(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	b, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(b)
}

// captureRequestHeaders 只记客户端发给网关的完整头。上游头等真正出网后再记。
func (s *Server) captureRequestHeaders(st core.State) {
	if s == nil {
		return
	}
	client := flattenHeaders(st.Header)
	if cs := s.callGet(st.ID); cs != nil {
		cs.mu.Lock()
		cs.clientHeaders = client
		cs.mu.Unlock()
	}
	s.liveSetHeaders(st.ID, client, nil)
}

// noteActualUpstreamHeaders 记下发给上游的那份 Header（已组好、即将 http.Do）。
func (s *Server) noteActualUpstreamHeaders(id string, h http.Header) {
	if s == nil || id == "" || h == nil {
		return
	}
	upstream := flattenHeaders(h)
	if cs := s.callGet(id); cs != nil {
		cs.mu.Lock()
		cs.upstreamHeaders = upstream
		cs.mu.Unlock()
	}
	if sess := s.liveSession(id); sess != nil {
		sess.SetUpstreamHeaders(upstream)
	}
}

func (s *Server) persistCapturedHeaders(id string) {
	if s == nil || s.audit == nil || id == "" {
		return
	}
	cs := s.callGet(id)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	client := headersJSON(cs.clientHeaders)
	upstream := headersJSON(cs.upstreamHeaders)
	cs.mu.Unlock()
	s.audit.SavePayloadHeaders(id, client, upstream)
}

func parseHeadersJSON(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out map[string]string
	if json.Unmarshal([]byte(raw), &out) != nil || len(out) == 0 {
		return nil
	}
	return out
}
