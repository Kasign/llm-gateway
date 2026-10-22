// server（openai_sanitize）：转发前改写请求体，避免 Codex/Responses 格式导致 llama.cpp 500。
package server

import (
	"encoding/json"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/logutil"
)

// Codex / OpenAI Responses 会把 system、developer 插在对话中间，
// Qwen 的 chat template 要求 system 必须在第一条，否则 llama.cpp 返回 500。
func sanitizeForLlama(path string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, "/v1/responses"):
		return sanitizeResponsesBody(body)
	case strings.Contains(p, "/v1/chat/completions"), strings.Contains(p, "/v1/messages"):
		return sanitizeChatBody(body)
	default:
		return body
	}
}

func sanitizeResponsesBody(body []byte) []byte {
	var obj map[string]interface{}
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	var sys []string
	if t := textFromValue(obj["instructions"]); t != "" {
		sys = append(sys, t)
	}
	if input, ok := obj["input"].([]interface{}); ok {
		kept := make([]interface{}, 0, len(input))
		for _, item := range input {
			m, ok := item.(map[string]interface{})
			if !ok {
				kept = append(kept, item)
				continue
			}
			role := strings.ToLower(asString(m["role"]))
			if role == "system" || role == "developer" {
				if t := textFromValue(m["content"]); t != "" {
					sys = append(sys, t)
				}
				continue
			}
			kept = append(kept, item)
		}
		obj["input"] = kept
	}
	if len(sys) > 0 {
		obj["instructions"] = strings.Join(sys, "\n\n")
	}
	applyGatewayStreamDefaults(obj)
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	applog.Infof("sanitize /v1/responses: merged_system=%d inst_chars=%d bytes %d->%d",
		len(sys), len(asString(obj["instructions"])), len(body), len(out))
	return out
}

// sanitizeChatBody 在 chat/completions 出网前整理 messages：system 合并到首位。tools 原样保留。
func sanitizeChatBody(body []byte) []byte {
	var obj map[string]interface{}
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	msgs, ok := obj["messages"].([]interface{})
	if !ok {
		return body
	}
	var sys []string
	kept := make([]interface{}, 0, len(msgs))
	for _, item := range msgs {
		m, ok := item.(map[string]interface{})
		if !ok {
			kept = append(kept, item)
			continue
		}
		role := strings.ToLower(asString(m["role"]))
		if role == "system" || role == "developer" {
			if t := textFromValue(m["content"]); t != "" {
				sys = append(sys, t)
			}
			continue
		}
		kept = append(kept, item)
	}
	if len(sys) > 0 {
		systemMsg := map[string]interface{}{"role": "system", "content": strings.Join(sys, "\n\n")}
		kept = append([]interface{}{systemMsg}, kept...)
	}
	obj["messages"] = kept
	applyGatewayStreamDefaults(obj)
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	applog.Infof("sanitize chat: merged_system=%d bytes %d->%d",
		len(sys), len(body), len(out))
	return out
}

func applyGatewayStreamDefaults(obj map[string]interface{}) {
	ensureStreamIncludeUsage(obj)
}

// ensureStreamIncludeUsage 为流式 chat/responses 注入 OpenAI stream_options，便于 Ollama 等在末 chunk 返回 usage。
func ensureStreamIncludeUsage(obj map[string]interface{}) {
	if !logutil.JSONTruthy(obj["stream"]) {
		return
	}
	opts, _ := obj["stream_options"].(map[string]interface{})
	if opts == nil {
		opts = map[string]interface{}{}
	}
	if !logutil.JSONTruthy(opts["include_usage"]) {
		opts["include_usage"] = true
	}
	obj["stream_options"] = opts
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func textFromValue(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, p := range t {
			if s := textFromValue(p); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		if s := asString(t["text"]); s != "" {
			return strings.TrimSpace(s)
		}
		return textFromValue(t["content"])
	default:
		return ""
	}
}
