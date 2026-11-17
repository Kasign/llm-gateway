package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	s := &Server{
		cfg: cfg,
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKeyHeader: "authorization", DefaultModel: "deepseek-chat"},
		},
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
		state := core.State{
		Request: core.Request{ID: id, Method: "POST", Path: "/v1/chat/completions", Started: time.Now()},
		Stream:  true,
	}

		for pb.Next() {
			observer.onChunk(state, chunk)
		}
		s.callDrop(id)
		s.liveFinish(context.Background(), id)
	})
}

// Benchmark 创建同时要求管理员会话与独立的调用方服务密钥，不能用 Authorization 中的管理员凭据代替。
func TestAuthenticateBenchmarkKeyRequiresDedicatedClientKey(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", nil)
	r.Header.Set("Authorization", "Bearer admin-session-or-admin-key")
	if _, code := s.authenticateBenchmarkKey(r); code != "invalid_api_key" {
		t.Fatalf("authenticateBenchmarkKey() code = %q, want invalid_api_key", code)
	}
}

func TestAuthenticateBenchmarkKeyRejectsMissingClientKey(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", nil)
	if _, code := s.authenticateBenchmarkKey(r); code != "invalid_api_key" {
		t.Fatalf("authenticateBenchmarkKey() code = %q, want invalid_api_key", code)
	}
}


// TestBenchmarkCancelIsRegisteredBeforeWorkerStarts 验证任务在 Worker 调度前即可被取消。
func TestBenchmarkCancelIsRegisteredBeforeWorkerStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &benchmarkJob{}
	job.setCancel(cancel)
	if !job.requestStop() {
		t.Fatal("requestStop() = false, want true before worker starts")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("worker context was not cancelled")
	}
}


// TestBenchmarkCancellationSerializesWithFinish 验证取消持久化与自然完成按 Job 锁排序。
func TestBenchmarkCancellationSerializesWithFinish(t *testing.T) {
	job := &benchmarkJob{Status: "running"}
	persistStarted := make(chan struct{})
	allowPersist := make(chan struct{})
	cancelDone := make(chan error, 1)
	go func() {
		changed, err := job.markCancellingWith(func() (bool, error) {
			close(persistStarted)
			<-allowPersist
			return true, nil
		})
		if err == nil && !changed {
			err = fmt.Errorf("running job did not enter cancelling")
		}
		cancelDone <- err
	}()
	<-persistStarted

	finishDone := make(chan struct{})
	go func() {
		job.finish("completed", http.StatusOK, 10, nil, nil, nil, 0, 0)
		close(finishDone)
	}()
	close(allowPersist)
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	<-finishDone
	if got := job.snapshot().Status; got != "completed" {
		t.Fatalf("final status = %q, want completed", got)
	}

	failed := &benchmarkJob{Status: "running"}
	if changed, err := failed.markCancellingWith(func() (bool, error) {
		return false, fmt.Errorf("database unavailable")
	}); err == nil || changed {
		t.Fatalf("failed persistence result = (%v, %v), want (false, error)", changed, err)
	}
	if got := failed.snapshot().Status; got != "running" {
		t.Fatalf("status after persistence failure = %q, want running", got)
	}
}

// TestBenchmarkExecutionTimeoutHasHardLimit 验证关闭通用请求超时也不能取消 Benchmark 硬时限。
func TestBenchmarkExecutionTimeoutHasHardLimit(t *testing.T) {
	if got := benchmarkExecutionTimeout(0); got != maxBenchmarkExecutionTime { t.Fatalf("zero timeout = %s", got) }
	if got := benchmarkExecutionTimeout(60); got != time.Minute { t.Fatalf("short timeout = %s", got) }
	if got := benchmarkExecutionTimeout(3600); got != maxBenchmarkExecutionTime { t.Fatalf("long timeout = %s", got) }
}

// TestBenchmarkStoreReservationLimitsActiveTasks 验证活动任务与并发创建预留受服务端上限约束。
func TestBenchmarkStoreReservationLimitsActiveTasks(t *testing.T) {
	bs := newBenchmarkStore()
	if !bs.reserveSlot(maxActiveBenchmarkJobs) { t.Fatal("first reservation should succeed") }
	if bs.reserveSlot(maxActiveBenchmarkJobs) { t.Fatal("second reservation must be rejected") }
	bs.releaseSlot()
	job := &benchmarkJob{ID: "active", Status: "running"}
	if !bs.reserveSlot(maxActiveBenchmarkJobs) { t.Fatal("reservation should succeed after release") }
	bs.putReserved(job)
	if bs.reserveSlot(maxActiveBenchmarkJobs) { t.Fatal("active job must occupy the only slot") }
	job.finish("completed", http.StatusOK, 1, nil, nil, nil, 0, 0)
	if !bs.reserveSlot(maxActiveBenchmarkJobs) { t.Fatal("terminal job must release the active slot") }
	bs.releaseSlot()
}

// TestMemSinkBoundsBenchmarkResponse 验证 Benchmark 内存响应缓存不会超过固定上限。
func TestMemSinkBoundsBenchmarkResponse(t *testing.T) {
	sink := newMemSink()
	payload := make([]byte, maxBenchmarkResponseBytes+1)
	if n, err := sink.Write(payload[:maxBenchmarkResponseBytes]); err != nil || n != maxBenchmarkResponseBytes {
		t.Fatalf("write up to limit = %d, %v", n, err)
	}
	n, err := sink.Write(payload[maxBenchmarkResponseBytes:])
	if !errors.Is(err, errBenchmarkResponseTooLarge) { t.Fatalf("overflow Write error = %v", err) }
	if n != 0 || len(sink.Bytes()) != maxBenchmarkResponseBytes { t.Fatalf("captured bytes = %d/%d", n, len(sink.Bytes())) }
	if sink.StatusCode() != http.StatusBadGateway { t.Fatalf("sink status = %d, want 502", sink.StatusCode()) }
	gatewayStatus, _, state, code, _ := finishStatus(context.Background(), core.Event{State: core.State{Request: core.Request{Sink: sink}}, Err: err}, "")
	if gatewayStatus != http.StatusBadGateway || state != "error" || code != "benchmark_response_too_large" { t.Fatalf("audit finish = %d/%s/%s", gatewayStatus, state, code) }
}
// TestBenchmarkObserverBoundsCapturedResponse 验证观察器不会因超大流式分片无限累积响应。
func TestBenchmarkObserverBoundsCapturedResponse(t *testing.T) {
	job := &benchmarkJob{}
	observer := &benchRunObserver{job: job, started: time.Now()}
	observer.On(context.Background(), core.Event{Stage: core.StageChunk, Chunk: make([]byte, maxBenchmarkResponseBytes+1)})
	if len(observer.buf) != maxBenchmarkResponseBytes || !observer.truncated { t.Fatalf("observer capture = %d, truncated=%v", len(observer.buf), observer.truncated) }
	observer.On(context.Background(), core.Event{Stage: core.StageFinish, Result: &core.Result{Status: http.StatusOK}})
	snapshot := job.snapshot()
	if !snapshot.ResponseTruncated || snapshot.ErrorCode != "benchmark_response_too_large" || snapshot.Status != "failed" { t.Fatalf("unexpected truncated result: %+v", snapshot) }
}
// TestBenchmarkCancellationTakesPrecedenceOverResponseLimit 验证用户取消不会被响应上限错误覆盖。
func TestBenchmarkCancellationTakesPrecedenceOverResponseLimit(t *testing.T) {
	job := &benchmarkJob{}
	observer := &benchRunObserver{job: job, started: time.Now(), truncated: true}
	observer.On(context.Background(), core.Event{Stage: core.StageFinish, Err: context.Canceled, Result: &core.Result{Status: 499}})
	snapshot := job.snapshot()
	if snapshot.Status != "cancelled" || snapshot.ErrorCode != "cancelled" || snapshot.ResponseTruncated { t.Fatalf("unexpected cancellation result: %+v", snapshot) }
}

// TestBenchmarkDeadlineUsesGatewayTimeoutCode 验证硬时限在任务结果中使用稳定的超时错误码。
func TestBenchmarkDeadlineUsesGatewayTimeoutCode(t *testing.T) {
	job := &benchmarkJob{}
	job.finish("failed", http.StatusGatewayTimeout, 10, nil, context.DeadlineExceeded, nil, 0, 0)
	snapshot := job.snapshot()
	if snapshot.Status != "failed" || snapshot.HTTPStatus != http.StatusGatewayTimeout || snapshot.ErrorCode != "gateway_timeout" {
		t.Fatalf("unexpected timeout result: %+v", snapshot)
	}
}


// TestBenchmarkObserverMapsDeadlineWithoutProviderError 验证上下文先到期时也记录 gateway_timeout。
func TestBenchmarkObserverMapsDeadlineWithoutProviderError(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	job := &benchmarkJob{}
	observer := &benchRunObserver{job: job, started: time.Now()}
	observer.On(ctx, core.Event{Stage: core.StageFinish, Result: &core.Result{Status: http.StatusOK}})
	snapshot := job.snapshot()
	if snapshot.Status != "failed" || snapshot.HTTPStatus != http.StatusGatewayTimeout || snapshot.ErrorCode != "gateway_timeout" {
		t.Fatalf("unexpected deadline result: %+v", snapshot)
	}
}


// TestBenchmarkPipelineFallbackPreservesErrorClassification 验证 Pipeline 未发出 StageFinish 时兜底错误分类一致。
func TestBenchmarkPipelineFallbackPreservesErrorClassification(t *testing.T) {
	newObserver := func(job *benchmarkJob) *benchRunObserver {
		return &benchRunObserver{job: job, started: time.Now()}
	}

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		job := &benchmarkJob{StartedAt: time.Now()}
		finishBenchmarkPipelineError(job, ctx, newObserver(job), errors.New("upstream connection interrupted"))
		snapshot := job.snapshot()
		if snapshot.Status != "failed" || snapshot.HTTPStatus != http.StatusGatewayTimeout || snapshot.ErrorCode != "gateway_timeout" {
			t.Fatalf("unexpected deadline fallback: %+v", snapshot)
		}
	})

	t.Run("response limit", func(t *testing.T) {
		job := &benchmarkJob{StartedAt: time.Now()}
		finishBenchmarkPipelineError(job, context.Background(), newObserver(job), errBenchmarkResponseTooLarge)
		snapshot := job.snapshot()
		if snapshot.Status != "failed" || snapshot.HTTPStatus != http.StatusBadGateway || snapshot.ErrorCode != "benchmark_response_too_large" || !snapshot.ResponseTruncated {
			t.Fatalf("unexpected response limit fallback: %+v", snapshot)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		job := &benchmarkJob{StartedAt: time.Now()}
		finishBenchmarkPipelineError(job, context.Background(), newObserver(job), context.Canceled)
		snapshot := job.snapshot()
		if snapshot.Status != "cancelled" || snapshot.HTTPStatus != 499 || snapshot.ErrorCode != "cancelled" {
			t.Fatalf("unexpected cancellation fallback: %+v", snapshot)
		}
	})
}


// TestBenchmarkMetricAvailabilityStates 验证 unknown/not_applicable 不使用 0 伪装。
func TestBenchmarkMetricAvailabilityStates(t *testing.T) {
	job := &benchmarkJob{Stream: true}
	job.setModelLoadMetric(nil, "not_applicable")
	job.setInferenceLatencyMetric(nil, "unknown")
	job.finish("failed", http.StatusBadGateway, 12, nil, errors.New("load failed"), nil, 0, 0)
	snapshot := job.snapshot()
	if snapshot.ModelLoadMS != nil || snapshot.ModelLoadStatus != "not_applicable" {
		t.Fatalf("model load metric = %v/%s", snapshot.ModelLoadMS, snapshot.ModelLoadStatus)
	}
	if snapshot.InferenceLatencyMS != nil || snapshot.InferenceLatencyStatus != "unknown" {
		t.Fatalf("inference metric = %v/%s", snapshot.InferenceLatencyMS, snapshot.InferenceLatencyStatus)
	}
	if snapshot.TTFTMS != nil || snapshot.TTFTStatus != "unknown" {
		t.Fatalf("TTFT metric = %v/%s", snapshot.TTFTMS, snapshot.TTFTStatus)
	}
	value := int64(0)
	job.setModelLoadMetric(&value, "measured")
	if got := job.snapshot(); got.ModelLoadMS == nil || *got.ModelLoadMS != 0 || got.ModelLoadStatus != "measured" {
		t.Fatalf("measured zero-duration metric = %+v", got)
	}
}


// TestBenchmarkObserverTTFTStartsAtRunBoundary 验证 TTFT 从 Run 开始计时，而不是从 Task 创建计时。
func TestBenchmarkObserverTTFTStartsAtRunBoundary(t *testing.T) {
	taskStarted := time.Now().Add(-2 * time.Second)
	runStarted := time.Now().Add(-120 * time.Millisecond)
	job := &benchmarkJob{StartedAt: taskStarted, Stream: true}
	observer := &benchRunObserver{job: job, started: runStarted}
	observer.On(context.Background(), core.Event{
		Stage: core.StageChunk,
		Chunk: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"),
	})
	if observer.ttft == nil {
		t.Fatal("TTFT was not captured")
	}
	if *observer.ttft < 80 || *observer.ttft > 1000 {
		t.Fatalf("TTFT = %dms; want roughly elapsed time since Run start", *observer.ttft)
	}
}


// TestBenchmarkConditionMetadataMarksPartialAndFingerprintsKnownInputs 验证条件快照稳定，并显式暴露缺失维度。
func TestBenchmarkConditionMetadataMarksPartialAndFingerprintsKnownInputs(t *testing.T) {
	snapshot := map[string]interface{}{
		"model_identity": map[string]interface{}{"sha256": "abc123"},
		"environment": map[string]interface{}{"vcs_revision": "deadbeef"},
		"backend_version": "1.2.3",
		"model_load_state_before": "loaded",
		"hardware": map[string]interface{}{"cpu_model": "test-cpu", "memory_bytes": int64(32 << 30)},
		"max_tokens": 256,
	}
	first, status, missing, err := benchmarkConditionMetadata(snapshot, "prompt-hash")
	if err != nil || status != "complete" || len(missing) != 0 || first == "" {
		t.Fatalf("complete condition metadata = %q/%q/%v, err=%v", first, status, missing, err)
	}
	second, status, missing, err := benchmarkConditionMetadata(snapshot, "prompt-hash")
	if err != nil || second != first {
		t.Fatalf("condition fingerprint is not deterministic: %q != %q, err=%v", second, first, err)
	}
	snapshot["max_tokens"] = 512
	changed, _, _, err := benchmarkConditionMetadata(snapshot, "prompt-hash")
	if err != nil || changed == first {
		t.Fatalf("changed generation parameters retained fingerprint %q, err=%v", changed, err)
	}

	partial := map[string]interface{}{
		"model_identity": map[string]interface{}{"sha256": ""},
		"environment": map[string]interface{}{"vcs_revision": ""},
		"backend_version": "",
		"hardware": map[string]interface{}{"cpu_model": "", "memory_bytes": nil},
	}
	_, status, missing, err = benchmarkConditionMetadata(partial, "prompt-hash")
	if err != nil || status != "partial" || len(missing) != 6 {
		t.Fatalf("partial condition metadata = %q/%v, err=%v", status, missing, err)
	}
}


// TestBenchmarkConditionMetadataHandlesMissingSnapshotFields 验证旧记录和缺失字段不会被误判为完整条件。
func TestBenchmarkConditionMetadataHandlesMissingSnapshotFields(t *testing.T) {
	fingerprint, status, missing, err := benchmarkConditionMetadata(map[string]interface{}{}, "prompt-hash")
	if err != nil || fingerprint == "" || status != "partial" || len(missing) != 6 {
		t.Fatalf("missing snapshot metadata = %q/%q/%v, err=%v", fingerprint, status, missing, err)
	}
}
