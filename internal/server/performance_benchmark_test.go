package server

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/settings"
	"github.com/whl/llm-gateway/internal/store"
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


// BenchmarkRequestTraceRecordStreamChunk 测量 SSE 分片解析与事件元数据生成开销。
// 不连接数据库，也不保存正文预览或原始 SSE；每次迭代从事件预算起点开始。
func BenchmarkRequestTraceRecordStreamChunk(b *testing.B) {
	cfg := &config.Config{}
	trace := &requestTrace{
		s: &Server{cfg: cfg}, id: "benchmark-stream-trace",
		start: time.Now(), stream: true, ttftRecorded: true,
	}
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"benchmark token\"}}]}\n\n")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trace.seq = 0
		trace.contentBytes = 0
		trace.lineCarry = ""
		trace.timing = streamTimings{}
		trace.recordStreamChunk(chunk)
	}
}

// BenchmarkInferenceObserverOnChunkParallel 测量长流稳定阶段的观察器并发热路径。
// 每个 worker 使用独立请求和 Live Session；模拟事件保留预算已耗尽的长流，
// 继续测量 token 解析、响应字节计数、请求锁和实时标量更新，不涉及真实模型或 DB。
func BenchmarkInferenceObserverOnChunkParallel(b *testing.B) {
	cfg := &config.Config{} // StorePayload、StoreStreamPreview、StoreStreamRaw 默认关闭。
	s := &Server{cfg: cfg, live: live.NewRegistry()}
	observer := inferenceObserver{s: s}
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"benchmark token\"}}]}\n\n")
	var nextID atomic.Uint64

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := fmt.Sprintf("benchmark-observer-%d", nextID.Add(1))
		s.liveStart(context.Background(), store.RequestLog{
			RequestID: id, Status: "running", StartedAt: time.Now(),
		})
		cs := s.callBegin(id, nil)
		trace := s.attachRequestTrace(id, time.Now())
		trace.ttftRecorded = true
		trace.seq = maxStreamEventsPerRequest // 测量长流达到事件上限后的稳态路径。
		cs.trace = trace
		state := core.State{Request: core.Request{
			ID: id, Method: "POST", Path: "/v1/chat/completions", Stream: true, Started: time.Now(),
		}}

		for pb.Next() {
			observer.onChunk(state, chunk)
		}
		s.callDrop(id)
		s.liveFinish(context.Background(), id)
	})
}
