// server（request_analysis）：根据各阶段耗时计算请求分析 JSON（TTFT、吞吐等），供管理台展示。
package server

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/whl/llm-gateway/internal/store"
)

// RequestAnalysis 写入 request_logs.analysis_json，概括一次调用的阶段耗时与生成速度。
type RequestAnalysis struct {
	Phases     RequestPhaseMS    `json:"phases_ms"`
	Throughput RequestThroughput `json:"throughput"`
	Summary    []string          `json:"summary,omitempty"`
}

type RequestPhaseMS struct {
	TotalMS                int64  `json:"total_ms"`
	PrepMS                 int64  `json:"prep_ms"`
	ModelLoadMS            int64  `json:"model_load_ms"`
	UpstreamMS             int64  `json:"upstream_ms"`
	ProviderQueueWaitMS    *int64 `json:"provider_queue_wait_ms,omitempty"`
	ProviderQueueWaitStatus string `json:"provider_queue_wait_status,omitempty"`
	TimeToUpstreamHeadersMS *int64 `json:"time_to_upstream_headers_ms,omitempty"`
	TimeToFirstByteMS       *int64 `json:"time_to_first_byte_ms,omitempty"`
	TTFTMS                  *int64 `json:"ttft_ms,omitempty"`
	TTFTContentMS           *int64 `json:"ttft_content_ms,omitempty"`
	ThinkingMS              *int64 `json:"thinking_ms,omitempty"`
	ContentGenerationMS     *int64 `json:"content_generation_ms,omitempty"`
	PrefillUntilFirstMS     *int64 `json:"prefill_until_first_ms,omitempty"`
	GatewayBeforeInferMS    int64  `json:"gateway_before_infer_ms"`
}

// RequestModelTimings 保留 llama.cpp 原始阶段测量；指针 nil 表示上游未提供该字段。
type RequestModelTimings struct {
	PromptN           *int64   `json:"prompt_n,omitempty"`
	PredictedN        *int64   `json:"predicted_n,omitempty"`
	PromptMS          *float64 `json:"prompt_ms,omitempty"`
	PredictedMS       *float64 `json:"predicted_ms,omitempty"`
	PromptPerSecond   *float64 `json:"prompt_per_second,omitempty"`
	PredictedPerSecond *float64 `json:"predicted_per_second,omitempty"`
}

type RequestThroughput struct {
	OutputTokensPerSec *float64             `json:"output_tokens_per_sec,omitempty"`
	OutputTokens       *int64               `json:"output_tokens,omitempty"`
	GenerationWindowMS int64                `json:"generation_window_ms,omitempty"`
	TokenSource        string               `json:"token_source,omitempty"`
	SpeedSource        string               `json:"speed_source,omitempty"`
	ModelTimings       *RequestModelTimings `json:"llama_timings,omitempty"`
	Source             string               `json:"source,omitempty"` // 兼容旧版详情读取
	Note               string               `json:"note,omitempty"`
}

type analysisInput struct {
	TotalMS                 int64
	PrepMS                  int64
	ModelLoadMS             int64
	UpstreamMS              int64
	ProviderQueueWaitMS *int64
	ProviderQueueWaitStatus string
	Stream                  bool
	TTFTMS                  *int64
	TimeToFirstByteMS       *int64
	TimeToUpstreamHeadersMS *int64
	StreamTiming            streamTimings
	InputTokens             *int64
	OutputTokens            *int64
	TokenSource             string
	SpeedSource             string
	PredictedPerSec         *float64
	ModelTimings            *RequestModelTimings
}

type streamTimings struct {
	FirstAnyMS       *int64
	FirstReasoningMS *int64
	FirstContentMS   *int64
	LastEventMS      int64
}

func buildRequestAnalysis(in analysisInput) RequestAnalysis {
	ph := RequestPhaseMS{
		TotalMS: in.TotalMS, PrepMS: in.PrepMS, ModelLoadMS: in.ModelLoadMS, UpstreamMS: in.UpstreamMS,
		ProviderQueueWaitMS: in.ProviderQueueWaitMS, ProviderQueueWaitStatus: in.ProviderQueueWaitStatus,
		TimeToFirstByteMS: in.TimeToFirstByteMS, TimeToUpstreamHeadersMS: in.TimeToUpstreamHeadersMS,
		GatewayBeforeInferMS: in.PrepMS + in.ModelLoadMS,
	}
	if in.StreamTiming.FirstAnyMS != nil {
		ph.TTFTMS = in.StreamTiming.FirstAnyMS
	} else if in.TTFTMS != nil {
		ph.TTFTMS = in.TTFTMS
	}
	if in.StreamTiming.FirstContentMS != nil {
		ph.TTFTContentMS = in.StreamTiming.FirstContentMS
	}
	if ph.TTFTMS != nil {
		v := *ph.TTFTMS - ph.GatewayBeforeInferMS
		if v < 0 {
			v = 0
		}
		ph.PrefillUntilFirstMS = &v
	}

	if in.StreamTiming.FirstReasoningMS != nil && in.StreamTiming.FirstContentMS != nil {
		if *in.StreamTiming.FirstContentMS >= *in.StreamTiming.FirstReasoningMS {
			v := *in.StreamTiming.FirstContentMS - *in.StreamTiming.FirstReasoningMS
			ph.ThinkingMS = &v
		}
	} else if in.StreamTiming.FirstReasoningMS != nil && in.StreamTiming.FirstContentMS == nil && in.TotalMS > 0 {
		v := in.TotalMS - *in.StreamTiming.FirstReasoningMS
		if v > 0 {
			ph.ThinkingMS = &v
		}
	}

	genMS := int64(0)
	speedWindowMS := int64(0)
	if in.StreamTiming.FirstContentMS != nil {
		end := in.TotalMS
		if in.StreamTiming.LastEventMS > 0 && in.StreamTiming.LastEventMS < end {
			end = in.StreamTiming.LastEventMS
		}
		if end > *in.StreamTiming.FirstContentMS {
			genMS = end - *in.StreamTiming.FirstContentMS
			speedWindowMS = genMS
			ph.ContentGenerationMS = &genMS
		}
	} else if !in.Stream && in.UpstreamMS > 0 {
		genMS = in.UpstreamMS
		ph.ContentGenerationMS = &genMS
	} else if ph.TTFTMS != nil && in.TotalMS > *ph.TTFTMS {
		genMS = in.TotalMS - *ph.TTFTMS
		ph.ContentGenerationMS = &genMS
	}

	tp := RequestThroughput{
		OutputTokens: in.OutputTokens,
		TokenSource:  in.TokenSource,
		SpeedSource:  in.SpeedSource,
		ModelTimings: in.ModelTimings,
		Source:       in.TokenSource,
	}
	if in.PredictedPerSec != nil && *in.PredictedPerSec > 0 && in.SpeedSource == "llama_timings" {
		tp.OutputTokensPerSec = in.PredictedPerSec
		tp.Note = "llama.cpp timings.predicted_per_second（Decode 阶段实测；未提供匹配的 Gateway 生成窗口）"
	} else if in.OutputTokens != nil && *in.OutputTokens > 0 && speedWindowMS > 0 {
		tp.GenerationWindowMS = speedWindowMS
		tp.OutputTokensPerSec = tokensPerSec(in.OutputTokens, speedWindowMS)
		tp.SpeedSource = "gateway_window"
		tp.Note = "输出 Token 数 ÷ Gateway 可观测生成窗口；不是后端原生 Decode 速度"
	} else {
		tp.Note = "缺少可用的 Decode 实测速度或匹配的生成窗口，速度未知"
	}

	summary := summarizeAnalysis(ph, tp, in.Stream)
	return RequestAnalysis{Phases: ph, Throughput: tp, Summary: summary}
}

func tokensPerSec(out *int64, windowMS int64) *float64 {
	if out == nil || *out <= 0 || windowMS <= 0 {
		return nil
	}
	sec := float64(windowMS) / 1000.0
	v := float64(*out) / sec
	v = math.Round(v*100) / 100
	return &v
}

func summarizeAnalysis(ph RequestPhaseMS, tp RequestThroughput, stream bool) []string {
	var lines []string
	lines = append(lines, fmt.Sprintf("总耗时 %d ms（Gateway 准备 %d ms + 模型加载/等待 %d ms + 上游推理 %d ms）",
		ph.TotalMS, ph.PrepMS, ph.ModelLoadMS, ph.UpstreamMS))
	if ph.TTFTMS != nil {
		lines = append(lines, fmt.Sprintf("首 token（任意输出）约 %d ms", *ph.TTFTMS))
	}
	if ph.TTFTContentMS != nil {
		lines = append(lines, fmt.Sprintf("首条正文 content 约 %d ms", *ph.TTFTContentMS))
	}
	if ph.ThinkingMS != nil && *ph.ThinkingMS > 0 {
		lines = append(lines, fmt.Sprintf("思考阶段（reasoning→content 或仅 reasoning）约 %d ms", *ph.ThinkingMS))
	}
	if ph.ContentGenerationMS != nil && *ph.ContentGenerationMS > 0 {
		lines = append(lines, fmt.Sprintf("正文生成阶段约 %d ms", *ph.ContentGenerationMS))
	}
	if tp.OutputTokensPerSec != nil {
		if tp.SpeedSource == "llama_timings" {
			lines = append(lines, fmt.Sprintf("输出 token 速度 %.2f tok/s（llama.cpp Decode 实测）", *tp.OutputTokensPerSec))
		} else {
			lines = append(lines, fmt.Sprintf("输出 token 速度 %.2f tok/s（Gateway 生成窗口派生，窗口 %d ms）", *tp.OutputTokensPerSec, tp.GenerationWindowMS))
		}
	} else {
		lines = append(lines, tp.Note)
	}
	if stream {
		lines = append(lines, "流式：阶段时间来自 Gateway 时间线 + SSE 事件时间戳")
	}
	return lines
}

func encodeRequestAnalysis(a RequestAnalysis) string {
	b, err := json.Marshal(a)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeRequestAnalysis(raw string) (*RequestAnalysis, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var a RequestAnalysis
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	// 旧记录只有 source 字段时保留其 Token 来源含义；速度来源不从旧字段推断。
	if a.Throughput.TokenSource == "" {
		a.Throughput.TokenSource = a.Throughput.Source
	}
	return &a, nil
}

func rebuildAnalysisFromLog(l *store.RequestLog, events []store.StreamEvent) *RequestAnalysis {
	if l == nil {
		return nil
	}
	st := streamTimingsFromEvents(events)
	in := analysisInput{
		TotalMS: l.LatencyMS, Stream: effectiveStream(l, len(events)), TTFTMS: l.TTFTMS,
		StreamTiming: st, InputTokens: l.InputTokens, OutputTokens: l.OutputTokens,
	}
	if st.FirstAnyMS == nil && l.TTFTMS != nil {
		in.StreamTiming.FirstAnyMS = l.TTFTMS
	}
	a := buildRequestAnalysis(in)
	return &a
}

func streamTimingsFromEvents(events []store.StreamEvent) streamTimings {
	var st streamTimings
	for _, e := range events {
		if e.AtMS > st.LastEventMS {
			st.LastEventMS = e.AtMS
		}
		switch e.Kind {
		case "ttft":
			if st.FirstAnyMS == nil {
				v := e.AtMS
				st.FirstAnyMS = &v
			}
		case "delta":
			switch e.Field {
			case "reasoning":
				if st.FirstReasoningMS == nil {
					v := e.AtMS
					st.FirstReasoningMS = &v
				}
			case "content":
				if st.FirstContentMS == nil {
					v := e.AtMS
					st.FirstContentMS = &v
				}
				if st.FirstAnyMS == nil {
					v := e.AtMS
					st.FirstAnyMS = &v
				}
			}
		}
	}
	return st
}


// requestModelTimings 只在至少存在一个有效上游 timings 字段时生成快照。
func requestModelTimings(promptN, predictedN *int64, promptMS, predictedMS, promptPerSecond, predictedPerSecond *float64) *RequestModelTimings {
	if promptN == nil && predictedN == nil && promptMS == nil && predictedMS == nil &&
		promptPerSecond == nil && predictedPerSecond == nil {
		return nil
	}
	return &RequestModelTimings{
		PromptN: promptN, PredictedN: predictedN, PromptMS: promptMS, PredictedMS: predictedMS,
		PromptPerSecond: promptPerSecond, PredictedPerSecond: predictedPerSecond,
	}
}
