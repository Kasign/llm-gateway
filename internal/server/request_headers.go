package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/core"
)

// secretHeaders 只用于打码，不是展示白名单。
// 展示时遍历完整 Header，客户端新来的字段都会留下。
var secretHeaders = map[string]bool{
	"Authorization": true,
	"X-Api-Key":     true,
	"X-Admin-Key":   true,
	"Cookie":        true,
	"Set-Cookie":    true,
}

func isSecretHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "x-api-key", "x-admin-key", "cookie", "set-cookie":
		return true
	default:
		return false
	}
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
