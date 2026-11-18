// logutil（tokens）：从 OpenAI 兼容响应或 SSE 块提取 usage / llama.cpp timings。
package logutil

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Timings 为 llama.cpp chat/completions 响应中的 timings 对象。
// Present 字段用于区分合法的 0 与字段缺失；它们不参与 JSON 序列化。
type Timings struct {
	PromptN                  int64   `json:"prompt_n"`
	PredictedN               int64   `json:"predicted_n"`
	PredictedPerSecond       float64 `json:"predicted_per_second"`
	PromptMS                 float64 `json:"prompt_ms"`
	PredictedMS              float64 `json:"predicted_ms"`
	PromptPerSecond          float64 `json:"prompt_per_second"`
	PromptNPresent           bool    `json:"-"`
	PredictedNPresent        bool    `json:"-"`
	PromptMSPresent          bool    `json:"-"`
	PredictedMSPresent       bool    `json:"-"`
	PromptPerSecondPresent   bool    `json:"-"`
	PredictedPerSecondPresent bool   `json:"-"`
}

func parseTimingsJSON(raw string) (Timings, bool) {
	if raw == "" || raw == "[DONE]" {
		return Timings{}, false
	}
	var envelope struct {
		Timings json.RawMessage `json:"timings"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil || len(envelope.Timings) == 0 || string(envelope.Timings) == "null" {
		return Timings{}, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(envelope.Timings, &fields) != nil || fields == nil {
		return Timings{}, false
	}

	var t Timings
	t.PromptN, t.PromptNPresent = parseTimingsInt(fields["prompt_n"])
	t.PredictedN, t.PredictedNPresent = parseTimingsInt(fields["predicted_n"])
	t.PromptMS, t.PromptMSPresent = parseTimingsFloat(fields["prompt_ms"])
	t.PredictedMS, t.PredictedMSPresent = parseTimingsFloat(fields["predicted_ms"])
	t.PromptPerSecond, t.PromptPerSecondPresent = parseTimingsFloat(fields["prompt_per_second"])
	t.PredictedPerSecond, t.PredictedPerSecondPresent = parseTimingsFloat(fields["predicted_per_second"])
	valid := t.PromptNPresent || t.PredictedNPresent || t.PromptMSPresent || t.PredictedMSPresent ||
		t.PromptPerSecondPresent || t.PredictedPerSecondPresent
	return t, valid
}

func parseTimingsInt(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

func parseTimingsFloat(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(n.String(), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
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
	if !t.PredictedPerSecondPresent || t.PredictedPerSecond <= 0 {
		return nil
	}
	v := math.Round(t.PredictedPerSecond*100) / 100
	return &v
}

// ParseTimingsBody 解析非流式 JSON body 顶层的 timings。
func ParseTimingsBody(body []byte) (*int64, *int64, *int64, *float64) {
	t, ok := parseTimingsJSON(string(body))
	if !ok {
		return nil, nil, nil, nil
	}
	var in, out, total *int64
	if t.PromptNPresent {
		v := t.PromptN
		in = &v
	}
	if t.PredictedNPresent {
		v := t.PredictedN
		out = &v
	}
	if in != nil || out != nil {
		v := int64(0)
		if in != nil {
			v += *in
		}
		if out != nil {
			v += *out
		}
		total = &v
	}
	return in, out, total, timingsPredictedPerSec(t)
}

// TokenHarvest 同时保留 Token 计数来源、速度来源和 llama.cpp 原始 timings。
// Source 为兼容旧调用方保留，后续展示应优先使用 TokenSource / SpeedSource。
type TokenHarvest struct {
	InputTokens      *int64
	OutputTokens     *int64
	TotalTokens      *int64
	PredictedPerSec  *float64
	PredictedPerSecond *float64
	PromptN          *int64
	PredictedN       *int64
	PromptMS         *float64
	PredictedMS      *float64
	PromptPerSecond  *float64
	TokenSource      string
	SpeedSource      string
	Source           string // 兼容旧值："usage" | "llama_timings"
}

// Empty 表示还没有从 usage 或 timings 读到任何 Token 计数。
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
	h.TokenSource = "upstream_usage"
	h.Source = "usage"
}

func (h *TokenHarvest) mergeTimings(t Timings) {
	if t.PromptNPresent {
		v := t.PromptN
		h.PromptN = &v
		if h.InputTokens == nil && h.TokenSource != "upstream_usage" {
			h.InputTokens = &v
		}
	}
	if t.PredictedNPresent {
		v := t.PredictedN
		h.PredictedN = &v
		if h.OutputTokens == nil && h.TokenSource != "upstream_usage" {
			h.OutputTokens = &v
		}
	}
	if t.PromptMSPresent {
		v := t.PromptMS
		h.PromptMS = &v
	}
	if t.PredictedMSPresent {
		v := t.PredictedMS
		h.PredictedMS = &v
	}
	if t.PromptPerSecondPresent {
		v := t.PromptPerSecond
		h.PromptPerSecond = &v
	}
	if h.TokenSource != "upstream_usage" && (t.PromptNPresent || t.PredictedNPresent) {
		if h.PromptN != nil && h.PredictedN != nil {
			v := *h.PromptN + *h.PredictedN
			h.TotalTokens = &v
		}
		h.TokenSource = "llama_timings"
		h.Source = "llama_timings"
	}
	if t.PredictedPerSecondPresent {
		v := t.PredictedPerSecond
		h.PredictedPerSecond = &v
		if v > 0 {
			h.PredictedPerSec = timingsPredictedPerSec(t)
			h.SpeedSource = "llama_timings"
		} else {
			h.PredictedPerSec = nil
			h.SpeedSource = ""
		}
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

// TokensFromResponse 扫描完整响应（SSE 或 JSON）得到 Token 统计与 timings。
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
		var i, o, n int64
		if in != nil {
			i = *in
		}
		if out != nil {
			o = *out
		}
		if total != nil {
			n = *total
		}
		h.mergeUsage(i, o, n)
	}
	if t, ok := parseTimingsJSON(string(body)); ok {
		h.mergeTimings(t)
	}
	return h
}
