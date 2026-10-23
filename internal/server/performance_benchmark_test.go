package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/settings"
)

var requestAnalysisBenchmarkSink RequestAnalysis

// BenchmarkResolveInferenceAgentRoute 测量固定 Agent Provider/默认模型的解析路径，不包含上游网络请求。
func BenchmarkResolveInferenceAgentRoute(b *testing.B) {
	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat",
	}}
	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://llama:8080", ModelName: "local.gguf"},
	}
	const requestID = "benchmark-agent-route"
	s.callBegin(requestID, nil)
	state := core.State{
		Request: core.Request{
			ID: requestID, Method: "POST", Path: "/v1/chat/completions",
			RouteProviderID: "deepseek", RouteDefaultModel: "deepseek-chat",
		},
		ClientModel: "client-supplied-model-is-ignored",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.resolveInference(context.Background(), state); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	s.callDrop(requestID)
}

// BenchmarkBuildRequestAnalysis 测量请求时间线到分析 DTO 的转换与分配成本，不包含数据库写入。
func BenchmarkBuildRequestAnalysis(b *testing.B) {
	firstAnyMS, firstContentMS, lastEventMS := int64(130), int64(185), int64(2150)
	firstByteMS, upstreamHeadersMS := int64(75), int64(48)
	inputTokens, outputTokens := int64(768), int64(192)
	input := analysisInput{
		TotalMS: 2300, PrepMS: 12, ModelLoadMS: 40, UpstreamMS: 2248, Stream: true,
		TTFTMS: &firstAnyMS, TimeToFirstByteMS: &firstByteMS, TimeToUpstreamHeadersMS: &upstreamHeadersMS,
		StreamTiming: streamTimings{
			FirstAnyMS: &firstAnyMS, FirstContentMS: &firstContentMS, LastEventMS: lastEventMS,
		},
		InputTokens: &inputTokens, OutputTokens: &outputTokens, TokenSource: "upstream_usage",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		requestAnalysisBenchmarkSink = buildRequestAnalysis(input)
	}
}
