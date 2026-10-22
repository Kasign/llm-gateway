// logutil（stream）：解析 SSE 流式聊天片段，拼完整回复或统计 token 用量。
package logutil

import (
	"encoding/json"
	"strings"
)

// StreamDelta 表示 chat/completions 流里一条 data: 行解析出的增量内容。
// StreamDelta is one parsed SSE data payload from chat/completions streaming.
type StreamDelta struct {
	Content   string
	Reasoning string
	Role      string
	Finish    string
}

// ParseStreamDelta parses a single SSE `data:` JSON line (without the "data:" prefix).
func ParseStreamDelta(raw string) (StreamDelta, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[DONE]" {
		return StreamDelta{}, false
	}
	var v struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Role             string `json:"role"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil || len(v.Choices) == 0 {
		return StreamDelta{}, false
	}
	ch := v.Choices[0]
	out := StreamDelta{
		Content:   ch.Delta.Content,
		Reasoning: ch.Delta.ReasoningContent,
		Role:      ch.Delta.Role,
		Finish:    ch.FinishReason,
	}
	if out.Content == "" && out.Reasoning == "" && out.Role == "" && out.Finish == "" {
		return StreamDelta{}, false
	}
	return out, true
}

// ForEachStreamDataLine invokes fn for each complete `data:` line in a byte chunk (may span partial lines).
func ForEachStreamDataLine(chunk []byte, carry *string, fn func(line string)) {
	s := *carry + string(chunk)
	*carry = ""
	lines := strings.Split(s, "\n")
	if !strings.HasSuffix(s, "\n") && len(lines) > 0 {
		*carry = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		fn(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
}

// AggregateChatStream concatenates content/reasoning deltas from an SSE response body.
func AggregateChatStream(body []byte) (content, reasoning string) {
	var carry string
	ForEachStreamDataLine(body, &carry, func(raw string) {
		d, ok := ParseStreamDelta(raw)
		if !ok {
			return
		}
		content += d.Content
		reasoning += d.Reasoning
	})
	if carry != "" {
		line := strings.TrimSpace(carry)
		if strings.HasPrefix(line, "data:") {
			if d, ok := ParseStreamDelta(strings.TrimSpace(strings.TrimPrefix(line, "data:"))); ok {
				content += d.Content
				reasoning += d.Reasoning
			}
		}
	}
	return content, reasoning
}

func LooksLikeSSE(body []byte) bool {
	s := strings.TrimSpace(string(body))
	return strings.HasPrefix(s, "data:") || strings.Contains(s, "\ndata:")
}
