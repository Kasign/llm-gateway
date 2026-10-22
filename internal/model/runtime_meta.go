// model（runtime_meta）：runtime_json 里记录 llama.cpp / Ollama 各自的上游模型名，便于切换后端。
package model

import (
	"encoding/json"
	"strings"
)

// UpstreamNames 保存成功加载后在上游服务里登记的名字（两种后端字段不同）。
// UpstreamNames are upstream registry ids persisted after a successful load/sync.
type UpstreamNames struct {
	LlamaCppID string `json:"llamacpp_id,omitempty"`
	OllamaName string `json:"ollama_name,omitempty"`
}

type runtimeDocument struct {
	Upstream UpstreamNames      `json:"upstream,omitempty"`
	Extra    map[string]any     `json:"-"`
}

// ParseRuntimeJSON 从 runtime_json 字符串读出 upstream 块，空串则返回零值。
func ParseRuntimeJSON(raw string) UpstreamNames {
	if raw == "" {
		return UpstreamNames{}
	}
	var doc struct {
		Upstream UpstreamNames `json:"upstream"`
	}
	_ = json.Unmarshal([]byte(raw), &doc)
	return doc.Upstream
}

// UpstreamNameForBackend 按当前后端从 runtime_json 取出应对上游使用的模型名。
func UpstreamNameForBackend(raw, backend string) string {
	up := ParseRuntimeJSON(raw)
	switch backend {
	case "ollama":
		return up.OllamaName
	default:
		return up.LlamaCppID
	}
}

// MergeUpstreamName 在原有 JSON 上写入当前后端的 upstream 名，其它字段尽量保留。
func MergeUpstreamName(raw, backend, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return raw
	}
	var root map[string]any
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	up, _ := root["upstream"].(map[string]any)
	if up == nil {
		up = map[string]any{}
	}
	switch backend {
	case "ollama":
		up["ollama_name"] = name
	default:
		up["llamacpp_id"] = name
	}
	root["upstream"] = up
	b, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(b)
}
