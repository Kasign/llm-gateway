// logutil（tokens）：从 OpenAI 兼容响应或 SSE 块提取 usage / llama.cpp timings。
package logutil

import (
	"encoding/json"
	"math"
	"strings"
)

// Timings 为 llama.cpp chat/completions 响应中的 timings 对象。
type Timings struct {
	PromptN            int64   `json:"prompt_n"`
	PredictedN         int64   `json:"predicted_n"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
	PromptMS           float64 `json:"prompt_ms"`
	PredictedMS        float64 `json:"predicted_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
}

func parseTimingsJSON(raw string) (Timings, bool) {
	if raw == "" || raw == "[DONE]" {
		return Timings{}, false
	}
	var v struct {
		Timings *Timings `json:"timings"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil || v.Timings == nil {
		return Timings{}, false
	}
	if v.Timings.PromptN == 0 && v.Timings.PredictedN == 0 && v.Timings.PredictedPerSecond == 0 {
		return Timings{}, false
	}
	return *v.Timings, true
}

// ParseTimingsChunk 扫描一块 SSE 文本中的 data 行，解析 llama.cpp timings（末包常见）。
func ParseTimingsChunk(chunk []byte) (Timings, bool) {
	var out Timings
	var ok bool
	for _, line := range splitChunkDataLines(chunk) {
		if t, got := parseTimingsJSON(line); got {
			out = t
			ok = true
		}
	}
	return out, ok
}

func splitChunkDataLines(chunk []byte) []string {
	var lines []string
	for _, line := range strings.Split(string(chunk), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw != "" {
			lines = append(lines, raw)
		}
	}
	return lines
}

// TimingsTokenCounts 将 timings 映射为 OpenAI 风格的 in/out/total。
func TimingsTokenCounts(t Timings) (in, out, total int64) {
	in = t.PromptN
	out = t.PredictedN
	total = in + out
	return in, out, total
}

func timingsPredictedPerSec(t Timings) *float64 {
	if t.PredictedPerSecond <= 0 {
		return nil
	}
	v := math.Round(t.PredictedPerSecond*100) / 100
	return &v
}

// ParseTimingsBody 解析非流式 JSON body 顶层的 timings。
func ParseTimingsBody(body []byte) (*int64, *int64, *int64, *float64) {
	var v struct {
		Timings *Timings `json:"timings"`
	}
	if json.Unmarshal(body, &v) != nil || v.Timings == nil {
		return nil, nil, nil, nil
	}
	in, out, total := TimingsTokenCounts(*v.Timings)
	return &in, &out, &total, timingsPredictedPerSec(*v.Timings)
}

// TokenHarvest 汇总一次响应中的 token 与 llama decode 速度（usage 优先于 timings）。
type TokenHarvest struct {
	InputTokens     *int64
	OutputTokens    *int64
	TotalTokens     *int64
	PredictedPerSec *float64
	Source          string // "usage" | "llama_timings"
}

// Empty 表示还没有从 usage 或 timings 读到任何计数。
func (h TokenHarvest) Empty() bool {
	return h.InputTokens == nil && h.OutputTokens == nil && h.TotalTokens == nil
}

func (h *TokenHarvest) mergeUsage(in, out, total int64) {
	if in > 0 {
		v := in
		h.InputTokens = &v
	}
	if out > 0 {
		v := out
		h.OutputTokens = &v
	}
	if total > 0 {
		v := total
		h.TotalTokens = &v
	} else if h.InputTokens != nil && h.OutputTokens != nil {
		v := *h.InputTokens + *h.OutputTokens
		h.TotalTokens = &v
	}
	h.Source = "usage"
}

func (h *TokenHarvest) mergeTimings(t Timings) {
	in, out, total := TimingsTokenCounts(t)
	if h.InputTokens == nil && in > 0 {
		v := in
		h.InputTokens = &v
	}
	if h.OutputTokens == nil && out > 0 {
		v := out
		h.OutputTokens = &v
	}
	if h.TotalTokens == nil && total > 0 {
		v := total
		h.TotalTokens = &v
	}
	if h.Source != "usage" && (in > 0 || out > 0) {
		h.Source = "llama_timings"
	}
	if pps := timingsPredictedPerSec(t); pps != nil {
		h.PredictedPerSec = pps
	}
}

func applyStreamDataTokens(h *TokenHarvest, raw string) {
	if in, out, total, ok := parseUsageJSON(raw); ok {
		h.mergeUsage(in, out, total)
	}
	if t, got := parseTimingsJSON(raw); got {
		h.mergeTimings(t)
	}
}

// ApplyStreamChunkTokens 从一块 SSE 更新 harvest。
// carry 把拆到两个网络包里的半行拼回去；和记流式事件用的是同一套拼行。传 nil 则只看这一块里的完整行。
func ApplyStreamChunkTokens(h *TokenHarvest, chunk []byte, carry *string) {
	if h == nil {
		return
	}
	if carry == nil {
		var tmp string
		carry = &tmp
	}
	if len(chunk) == 0 && *carry == "" {
		return
	}
	ForEachStreamDataLine(chunk, carry, func(raw string) {
		applyStreamDataTokens(h, raw)
	})
}

// FlushStreamTokenCarry 在流结束时消化最后半行（上游有时不给末尾换行）。
func FlushStreamTokenCarry(h *TokenHarvest, carry *string) {
	if h == nil || carry == nil || *carry == "" {
		return
	}
	raw := strings.TrimSpace(*carry)
	*carry = ""
	if strings.HasPrefix(raw, "data:") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "data:"))
	}
	if raw == "" || raw == "[DONE]" {
		return
	}
	applyStreamDataTokens(h, raw)
}

// TokensFromResponse 扫描完整响应（SSE 或 JSON）得到 token 统计。
func TokensFromResponse(body []byte) TokenHarvest {
	var h TokenHarvest
	if len(body) == 0 {
		return h
	}
	if LooksLikeSSE(body) {
		var carry string
		ForEachStreamDataLine(body, &carry, func(raw string) {
			applyStreamDataTokens(&h, raw)
		})
		FlushStreamTokenCarry(&h, &carry)
		return h
	}
	in, out, total := ParseUsageBody(body)
	if in != nil || out != nil || total != nil {
		var i, o, t int64
		if in != nil {
			i = *in
		}
		if out != nil {
			o = *out
		}
		if total != nil {
			t = *total
		}
		h.mergeUsage(i, o, t)
		return h
	}
	in2, out2, total2, pps := ParseTimingsBody(body)
	if in2 != nil || out2 != nil {
		h.InputTokens = in2
		h.OutputTokens = out2
		h.TotalTokens = total2
		h.PredictedPerSec = pps
		h.Source = "llama_timings"
	}
	return h
}
