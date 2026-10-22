// logutil 解析 OpenAI 风格请求/响应 JSON，提取 model、stream、usage 等字段供审计与计费统计。
package logutil

import (
	"encoding/json"
	"strings"
)

// ParseModel 从请求体 JSON 里读出 model 字段，解析失败返回空字符串。
func ParseModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &v) == nil {
		return v.Model
	}
	return ""
}

func IsStream(body []byte) bool {
	return ParseStreamRequested(body)
}

// ParseStreamRequested reads the JSON "stream" field (bool, "true", 1, etc.).
func ParseStreamRequested(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var obj map[string]interface{}
	if json.Unmarshal(body, &obj) != nil {
		return false
	}
	v, ok := obj["stream"]
	if !ok {
		return false
	}
	return JSONTruthy(v)
}

func JSONTruthy(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return t != 0
	case json.Number:
		n, _ := t.Int64()
		return n != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	default:
		return false
	}
}

// WantsEventStream reports Accept: text/event-stream (some clients omit body.stream).
func WantsEventStream(accept string) bool {
	return strings.Contains(strings.ToLower(accept), "text/event-stream")
}

// usageFields 同时兼容 chat 的 prompt/completion 和 Responses 的 input/output。
type usageFields struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

func (u usageFields) counts() (in, out, total int64, ok bool) {
	in = u.PromptTokens
	out = u.CompletionTokens
	if in == 0 {
		in = u.InputTokens
	}
	if out == 0 {
		out = u.OutputTokens
	}
	total = u.TotalTokens
	if total == 0 && (in != 0 || out != 0) {
		total = in + out
	}
	// 有 usage 对象但全是 0：不当成读成功，避免 Responses 字段被旧逻辑吃成空计数。
	ok = in > 0 || out > 0 || total > 0
	return in, out, total, ok
}

// parseUsageJSON 从一条 JSON 里取 usage：顶层或 response.usage（response.completed）。
func parseUsageJSON(raw string) (in, out, total int64, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[DONE]" {
		return 0, 0, 0, false
	}
	var v struct {
		Usage    *usageFields `json:"usage"`
		Response *struct {
			Usage *usageFields `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return 0, 0, 0, false
	}
	if v.Usage != nil {
		if in, out, total, ok = v.Usage.counts(); ok {
			return in, out, total, true
		}
	}
	if v.Response != nil && v.Response.Usage != nil {
		return v.Response.Usage.counts()
	}
	return 0, 0, 0, false
}

func ParseUsageChunk(chunk []byte) (in, out, total int64, ok bool) {
	for _, raw := range splitChunkDataLines(chunk) {
		if in, out, total, ok = parseUsageJSON(raw); ok {
			return in, out, total, true
		}
	}
	return 0, 0, 0, false
}

func ParseUsageBody(body []byte) (*int64, *int64, *int64) {
	if len(body) == 0 {
		return nil, nil, nil
	}
	in, out, total, ok := parseUsageJSON(string(body))
	if !ok {
		return nil, nil, nil
	}
	return &in, &out, &total
}
