package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/store"
)

// newInferencePipeline 是组合根：把现有模块的能力装进主流程的固定槽位。
// 新增改写、检查或日志只改这里的注册，不改 core.Pipeline.Run。
// Mutate 顺序：先按原始路径 sanitize 和合并推理参数，最后 mutateResponsesBridge 保持路径与 tools 原样。
func (s *Server) newInferencePipeline() *core.Pipeline {
	// 本地 Provider 按自身 ID 获取缓存客户端；活动后端切换不会让另一个 Provider 借用错误的连接池。
	if s.providers == nil {
		s.providers = provider.NewLocalRegistryWithBackendClient(
			func(backend string) provider.LocalHTTPClient {
				client, err := s.localClientForBackend(backend)
				if err != nil {
					applog.Printf("local Provider client unavailable: backend=%s error=%v", backend, err)
					return nil
				}
				return client
			},
			func(id string) provider.LocalModelManager { return &serverLocalModelManager{s: s, backend: id} },
		)
		for _, cfg := range s.cfg.Providers.OpenAICompatible {
			if !cfg.Enabled {
				continue
			}
			remote, err := provider.NewOpenAICompatibleProvider(provider.OpenAICompatibleConfig{
				ID: cfg.ID, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, APIKeyHeader: cfg.APIKeyHeader,
				AllowInsecureHTTP: cfg.AllowInsecureHTTP, AllowPrivateNetwork: cfg.AllowPrivateNetwork,
				MaxResponseBytes: cfg.MaxResponseBytes, MaxConcurrentRequests: cfg.MaxConcurrentRequests,
				MaxQueueSize: cfg.MaxQueueSize, QueueWaitTimeoutSeconds: cfg.QueueWaitTimeoutSeconds,
				RequestTimeoutSeconds: cfg.RequestTimeoutSeconds,
			})
			if err != nil {
				// Load 已做配置校验；保留 fail-closed 行为，不能注册一个未通过安全校验的远程目标。
				applog.Printf("remote provider registration skipped: id=%s error=%v", cfg.ID, err)
				continue
			}
			if err := s.providers.Register(cfg.ID, func() provider.Provider { return remote }); err != nil {
				applog.Printf("remote provider registration failed: id=%s error=%v", cfg.ID, err)
			}
		}
	}
	return &core.Pipeline{
		Resolve: s.resolveInference,
		Mutate: []core.Mutator{
			s.mutateSanitize,
			s.mutateInferenceParams,
			s.mutateResponsesBridge,
			s.mutateProviderTokenBudget,
		},
		Guard:     []core.Guard{s.providerTokenBudgetGuard(), s.guardModelReady},
		WarmStart: s.warmStart,
		Forward:   inferenceForwarder{s},
		Observe:   inferenceObserver{s},
	}
}

func choiceFromTarget(t proxyModelTarget) core.ModelChoice {
	key := t.ModelKey
	if t.Model != nil && t.Model.ModelKey != "" {
		key = t.Model.ModelKey
	}
	return core.ModelChoice{ProviderID: t.ProviderID, Hint: t.Hint, ModelKey: key, UpstreamID: t.UpstreamID}
}

// resolveInference 决定真正使用的模型。远程 Provider 仅接受已配置的公开模型别名；其他客户端 model 值仍回退到网关默认模型。
func (s *Server) resolveInference(ctx context.Context, st core.State) (core.ModelChoice, error) {
	active := s.effectiveUpstream()
	if name := strings.TrimSpace(st.ServiceModel); name != "" {
		backend := strings.TrimSpace(st.ServiceProviderID)
		if backend == "" {
			backend = active.Backend
		}
		if !s.isConfiguredProvider(backend) {
			return core.ModelChoice{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "provider_not_configured", Message: "internal Service Provider is not configured"}
		}
		return s.resolveNamedModelForProvider(ctx, st.ID, name, backend)
	}
	if routeProviderID := strings.TrimSpace(st.RouteProviderID); routeProviderID != "" {
		target, routeErr := s.resolveAgentRouteTarget(ctx, agentRoute{
			ProviderID: routeProviderID, DefaultModel: st.RouteDefaultModel,
		})
		if routeErr != nil {
			return core.ModelChoice{}, routeErr
		}
		s.rememberTarget(st.ID, target)
		return choiceFromTarget(target), nil
	}
	// 远程 Provider 配置了公开别名时，尊重请求中的已知别名；未知值继续沿用网关默认模型，保持旧客户端行为。
	if requested := strings.TrimSpace(st.ClientModel); requested != "" {
		if remote, ok := s.remoteProviderConfig(active.Backend); ok {
			if upstreamID := strings.TrimSpace(remote.ModelMappings[requested]); upstreamID != "" {
				target := proxyModelTarget{ProviderID: active.Backend, Hint: requested, ModelKey: requested, UpstreamID: upstreamID}
				s.rememberTarget(st.ID, target)
				return choiceFromTarget(target), nil
			}
		}
	}
	t, err := s.resolveProxyModelTargetForUpstream(ctx, active)
	if err != nil {
		return core.ModelChoice{}, err
	}
	s.rememberTarget(st.ID, t)
	return choiceFromTarget(t), nil
}

// resolveNamedModel 保留旧签名，供既有内部调用继续使用。
func (s *Server) resolveNamedModel(ctx context.Context, requestID, name string) (core.ModelChoice, error) {
	return s.resolveNamedModelForProvider(ctx, requestID, name, s.effectiveUpstream().Backend)
}

// resolveNamedModelForProvider 给内部调用同时固定模型及 Provider，避免并发切换时使用错后端。
func (s *Server) resolveNamedModelForProvider(ctx context.Context, requestID, name, providerID string) (core.ModelChoice, error) {
	t := proxyModelTarget{ProviderID: strings.ToLower(strings.TrimSpace(providerID)), Hint: name, ModelKey: name, UpstreamID: name}
	if m, err := s.models.GetByKey(ctx, name); err == nil && m != nil {
		t.ModelKey, t.Model = m.ModelKey, m
	} else if m := s.gatewayModelForUpstreamID(ctx, name); m != nil {
		t.ModelKey, t.Model = m.ModelKey, m
	}
	s.rememberTarget(requestID, t)
	return choiceFromTarget(t), nil
}



func (s *Server) rememberTarget(id string, t proxyModelTarget) {
	cs := s.callGet(id)
	if cs == nil {
		cs = s.callBegin(id, nil)
	}
	cs.mu.Lock()
	cs.target = t
	cs.mu.Unlock()
}

func (s *Server) targetOf(id string) proxyModelTarget {
	cs := s.callGet(id)
	if cs == nil {
		return proxyModelTarget{}
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.target
}

func (s *Server) mutateSanitize(_ context.Context, st core.State) (core.State, error) {
	// Qwen/llama.cpp 的 system 合并属于本地模板兼容逻辑；远程 Provider 必须保留调用方原始消息结构。
	if isLocalInferenceBackend(s.providerIDForState(st)) {
		st.Body = sanitizeForLlama(st.Path, st.Body)
	}
	return st, nil
}

// mutateInferenceParams 不解析思考或采样字段。合并模块返回完整 body 后，原样交给后续转发。
func (s *Server) mutateInferenceParams(ctx context.Context, st core.State) (core.State, error) {
	st.Body = s.applyInferenceParamsToBodyForBackend(ctx, s.providerIDForState(st), st.Path, st.Body)
	return st, nil
}

// mutateResponsesBridge 留在 Mutate 末尾。llama.cpp 自己提供 /v1/responses，并自行决定保留或跳过 tool。
// 这里不改 UpstreamPath，也不改 body。
func (s *Server) mutateResponsesBridge(_ context.Context, st core.State) (core.State, error) {
	return st, nil
}

// guardModelReady 在 auto_load_on_proxy 开启时确保模型已在上游内存。
// chat 加载失败返回 503；/v1/responses（Codex）最多等 20 秒，失败仍转发，避免占满客户端超时。
// 管理端取消时直接返回 Canceled，不当成加载失败。
func (s *Server) guardModelReady(ctx context.Context, st core.State) error {
	if !s.cfg.Llama.AutoLoadOnProxy {
		return nil
	}
	// 只有注册了本地模型控制能力的 Provider 才自动加载；远程 Provider 不会误调用本地控制 API。
	if s.providers == nil {
		return nil
	}
	manager, ok := s.providers.ResolveLocalModelManager(s.providerIDForState(st))
	if !ok {
		return nil
	}
	target := s.targetOf(st.ID)
	if target.UpstreamID == "" {
		target.UpstreamID = st.Model.UpstreamID
		target.ModelKey = st.Model.ModelKey
	}
	waitCtx := ctx
	responses := proxyPathIsResponses(st.Path)
	if responses {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	err := manager.EnsureReady(waitCtx, target.ModelKey, target.UpstreamID)
	if err == nil {
		return nil
	}
	if isCancelErr(ctx.Err()) {
		return context.Canceled
	}
	if responses {
		applog.Printf("[%s] responses model ready: %v (continuing)", st.ID, err)
		return nil
	}
	return &core.Error{Status: http.StatusServiceUnavailable, Code: "model_load_failed", Message: err.Error()}
}

// allowModelsGuard 只由 /v1 门外按请求附加（benchmark 不附加）。
// 必须在 Resolve 之后检查，因为允许列表比对的是服务选定的模型，而不是客户端传的 model。
// 列表项可以写网关 model_key、上游 ID 或管理台 hint，任一匹配即放行。
func (s *Server) allowModelsGuard(key *auth.Key) core.Guard {
	return func(_ context.Context, st core.State) error {
		if modelAllowedForKey(key, st.Model.ModelKey, st.Model.UpstreamID, st.Model.Hint) {
			return nil
		}
		return &core.Error{Status: http.StatusForbidden, Code: "model_not_allowed", Message: "model is not allowed for this api key"}
	}
}

// isAnthropicMessagesRequestPath 判断内部规范化后的 Anthropic Messages 路径，支持可选 count_tokens 子端点。
func isAnthropicMessagesRequestPath(rawPath string) bool {
	rawPath = strings.TrimSpace(strings.SplitN(rawPath, "?", 2)[0])
	rawPath = strings.TrimRight(rawPath, "/")
	return rawPath == "/v1/messages" || strings.HasPrefix(rawPath, "/v1/messages/")
}

// warmStart 在出网前给流式调用方写一行 SSE 注释，并按间隔重复，直到这次 Run 结束。
// 只写一次只能重置一次读空闲计时；本地模型预填往往超过 60 秒，需要在没有上游字节时继续写。
// 注释行以冒号开头，SSE 解析器（含 Codex）会直接跳过，不会出现在回答里。
// 非流式不写：一整份 JSON 前面多出字节会让调用方解析失败。
// 写出后状态行已经是 200，上游后续即使返回错误码也无法再改，只能把错误体追加在流里。
// 每一次写出都记一条流式事件。保活不经过 onChunk，因此不计入 TTFT 和响应大小。
func (s *Server) warmStart(ctx context.Context, st core.State) int {
	if s.cfg == nil || !s.cfg.WarmStartEnabled() || !st.Stream || st.Sink == nil {
		return 0
	}
	// Anthropic Messages 必须先拿到上游 HTTP 状态和错误正文；提前写 SSE 注释会不可逆地提交 200，
	// 让 Claude Code 无法根据 400/401/429 等响应识别能力拒绝并执行降级重试。
	if isAnthropicMessagesRequestPath(st.Path) {
		return 0
	}
	line := ": " + s.cfg.WarmStartComment() + "\n\n"
	st.Sink.SetHeader("Content-Type", "text/event-stream; charset=utf-8")
	st.Sink.SetHeader("Cache-Control", "no-cache")
	n, err := writeKeepalive(st.Sink, line)
	if err != nil {
		s.failRun(st.ID, "保活写出失败："+err.Error())
		return 0
	}
	s.noteKeepalive(st.ID, n, line)
	stop := repeatKeepalive(ctx, st.ID, st.Sink, line, s.cfg.WarmStartInterval(), func(n int) {
		s.noteKeepalive(st.ID, n, line)
	}, func(err error) {
		s.failRun(st.ID, "保活写出失败："+err.Error())
	})
	s.setKeepaliveStop(st.ID, stop)
	return n
}

func writeKeepalive(sink core.ResponseSink, line string) (int, error) {
	n, err := sink.Write([]byte(line))
	sink.Flush()
	return n, err
}

// repeatKeepalive 在首行之后按间隔再写。context 取消、stop 被调用，或写失败时退出。
// stop 只发出取消，不等待写出结束。onWrite 在每次成功写出后调用，运行在保活 goroutine 上。
func repeatKeepalive(ctx context.Context, id string, sink core.ResponseSink, line string, interval time.Duration, onWrite func(int), onFail func(error)) (stop func()) {
	if interval <= 0 || sink == nil || ctx == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		defer func() { _ = recover() }()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			// ctx 取消和 tick 同时就绪时，select 可能仍选中 tick。写出前再看一次，避免结束后继续轮询。
			if ctx.Err() != nil {
				return
			}
			n, err := writeKeepalive(sink, line)
			if err != nil {
				if onFail != nil {
					onFail(err)
				}
				return
			}
			if onWrite != nil {
				onWrite(n)
			}
			applog.Debugf("[%s] warm start repeat", id)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(cancel)
	}
}

// inferenceObserver 把主流程事件写成请求日志（audit 异步入库 + live 实时看板 + 耗时分析）。
// 它只读事件，不改变响应；出错或 panic 由 core 隔离。
type inferenceObserver struct{ s *Server }

func (o inferenceObserver) On(ctx context.Context, ev core.Event) {
	if o.s == nil {
		return
	}
	switch ev.Stage {
	case core.StageParsed:
		o.s.markCallRecorded(ctx)
		o.onParsed(ev.State)
	case core.StageModelResolved:
		// Resolver 定下的 Provider 与实际上游 model 一起写入运行中记录，避免把 Agent 流量归到全局活动上游。
		providerID := strings.ToLower(strings.TrimSpace(ev.State.Model.ProviderID))
		actual := strings.TrimSpace(ev.State.Model.UpstreamID)
		o.s.livePatch(ctx, ev.State.ID, store.RequestLog{ProviderID: providerID, ModelActual: actual})
		if o.s.audit != nil {
			if providerID != "" || actual != "" {
				// Provider 与上游模型由同一条审计更新持久化，避免并发负载下重复占用关键队列。
				o.s.audit.PatchRequestResolution(ev.State.ID, providerID, actual)
				o.s.invalidateRequestListCache()
			}
		}
	case core.StagePrepared:
		o.s.liveSetReqBody(ev.State.ID, ev.State.Body)
	case core.StageWarmStart:
		o.onWarmStart(ev.State)
	case core.StageChunk:
		o.onChunk(ev.State, ev.Chunk)
	case core.StageFinish:
		o.onFinish(ctx, ev)
	}
}

// onParsed 创建 running 状态的请求记录。门外拒绝的请求不会走到这里，由 rejectInference 单独记录。
func (o inferenceObserver) onParsed(st core.State) {
	cs := o.s.callGet(st.ID)
	logItem := store.RequestLog{
		RequestID: st.ID, ClientIP: st.ClientIP, ClientName: st.ClientName, UserAgent: st.UserAgent,
		Endpoint: st.Path, RequestURL: st.RequestURL, ProviderID: o.s.providerIDForState(st), Method: st.Method, ModelRequested: st.ClientModel, Stream: st.Stream,
		Status: "running", StartedAt: st.Started, RequestBytes: int64(len(st.OriginalBody)),
	}
	if cs != nil && cs.key != nil {
		logItem.APIKeyID = cs.key.ID
		logItem.APIKeyName = cs.key.Name
	}
	// CreateRequest 已写入从请求解析得到的 model_requested 与 stream，无需紧接着重复 UPDATE。
	o.s.audit.CreateRequest(logItem)
	o.s.liveStart(context.Background(), logItem)
	// 请求头只在观察者里记。主流程和 Forward 不改。
	o.s.captureRequestHeaders(st)
	if cs != nil {
		cs.mu.Lock()
		cs.trace = o.s.attachRequestTrace(st.ID, st.Started)
		cs.mu.Unlock()
	}
}

// onWarmStart 只打日志。每一次保活在写出时已经记入事件列表，这里再记会把首条算两次。
// 保活字节不计入响应大小、token 和 TTFT。
func (o inferenceObserver) onWarmStart(st core.State) {
	if st.WarmBytes <= 0 {
		return
	}
	applog.Printf("[%s] warm start sent: %d bytes", st.ID, st.WarmBytes)
}

// noteKeepalive 把这一次保活写进调用记录。它不改 TTFT，也不累加响应字节。
func (s *Server) noteKeepalive(id string, n int, line string) {
	if n <= 0 {
		return
	}
	cs := s.callGet(id)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.trace != nil {
		cs.trace.recordKeepalive(n, line)
	}
}

// onChunk 累计上游真实数据：流式事件、可选原文、token 统计，并每 300ms 同步一次实时看板。
func (o inferenceObserver) onChunk(st core.State, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	cs := o.s.callGet(st.ID)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.trace != nil {
		cs.trace.recordStreamChunk(chunk)
	}
	cs.respBytes += int64(len(chunk))
	if o.s.cfg.Logging.StorePayload && len(cs.resp) < 8<<20 {
		remain := (8 << 20) - len(cs.resp)
		storeChunk := chunk
		if len(storeChunk) > remain {
			storeChunk = storeChunk[:remain]
		}
		cs.resp = append(cs.resp, storeChunk...)
	}
	logutil.ApplyStreamChunkTokens(&cs.tokens, chunk, &cs.tokenCarry)
	o.s.liveAppendResBody(st.ID, chunk)
	if time.Since(cs.lastLive) >= 300*time.Millisecond {
		cs.lastLive = time.Now()
		o.s.liveSyncStream(st.ID, cs.trace, cs.respBytes, &cs.tokens)
	}
}

// onFinish 每次 Run 恰好调用一次：写最终状态、耗时分析、可选原文，并移出实时看板。
// Touch / MarkUsed 只在成功后执行，失败的调用不算「使用过」。
func (o inferenceObserver) onFinish(ctx context.Context, ev core.Event) {
	st := ev.State
	cancelAccepted := o.s.markCallFinishing(st.ID)
	if o.s.runs != nil && o.s.runs.MarkFinishing(st.ID) {
		cancelAccepted = true
	}
	if cancelAccepted && ev.Err == nil {
		ev.Err = context.Canceled
	}
	// 停掉后续保活。已经写成功的都在事件列表里；正在写出的那一次不等待。
	o.s.stopKeepalive(st.ID)
	cs := o.s.callGet(st.ID)
	end := time.Now()
	totalMS := end.Sub(st.Started).Milliseconds()
	var prepMS, loadMS, upstreamMS int64
	if !st.PreparedAt.IsZero() {
		prepMS = st.PreparedAt.Sub(st.Started).Milliseconds()
	}
	if !st.ReadyAt.IsZero() && !st.PreparedAt.IsZero() {
		loadMS = st.ReadyAt.Sub(st.PreparedAt).Milliseconds()
	}
	if !st.UpstreamStart.IsZero() {
		upstreamMS = end.Sub(st.UpstreamStart).Milliseconds()
	}
	gatewayStatus, upstreamStatus, state, code, msg := finishStatus(ctx, ev, o.s.cancelReason(st.ID))
	var ttft *int64
	var firstByteMS, upstreamHeadersMS *int64
	var timings streamTimings
	var inTok, outTok, totTok *int64
	var tokenSrc string
	var pps *float64
	var resp []byte
	var respBytes int64
	var trace *requestTrace
	var key *auth.Key
	if ev.Result != nil {
		if ev.Result.TTFT != nil {
			ttft = ev.Result.TTFT
		}
		firstByteMS = ev.Result.FirstByteMS
		upstreamHeadersMS = ev.Result.UpstreamHeadersMS
		respBytes = ev.Result.ResponseBytes
		if len(ev.Result.Body) > 0 {
			resp = ev.Result.Body
		}
	}
	if cs != nil {
		cs.mu.Lock()
		key = cs.key
		trace = cs.trace
		if len(cs.resp) > 0 {
			resp = cs.resp
		}
		if cs.respBytes > 0 {
			respBytes = cs.respBytes
		}
		// 最后半行可能没有换行，先拼完再取计数。
		logutil.FlushStreamTokenCarry(&cs.tokens, &cs.tokenCarry)
		inTok, outTok, totTok = cs.tokens.InputTokens, cs.tokens.OutputTokens, cs.tokens.TotalTokens
		tokenSrc = cs.tokens.Source
		pps = cs.tokens.PredictedPerSec
		cs.mu.Unlock()
	}
	if !st.Stream && len(resp) > 0 {
		meta := logutil.TokensFromResponse(resp)
		inTok, outTok, totTok = meta.InputTokens, meta.OutputTokens, meta.TotalTokens
		tokenSrc = meta.Source
		pps = meta.PredictedPerSec
	} else if st.Stream && inTok == nil && outTok == nil && totTok == nil && len(resp) > 0 {
		// 流式边收边解析可能漏掉拆包或 Responses 嵌套 usage，结束再扫一遍累计体。
		meta := logutil.TokensFromResponse(resp)
		inTok, outTok, totTok = meta.InputTokens, meta.OutputTokens, meta.TotalTokens
		tokenSrc = meta.Source
		pps = meta.PredictedPerSec
	}
	if trace != nil {
		timings = trace.streamTimings()
		if timings.FirstAnyMS != nil {
			ttft = timings.FirstAnyMS
		}
		o.s.audit.InsertStreamEvents(st.ID, trace.drainEvents())
	}
	logItem := store.RequestLog{
		RequestID: st.ID, ClientIP: st.ClientIP, ClientName: st.ClientName, UserAgent: st.UserAgent,
		Endpoint: st.Path, RequestURL: st.RequestURL, ProviderID: o.s.providerIDForState(st), Method: st.Method, ModelRequested: st.ClientModel, ModelActual: st.Model.UpstreamID,
		Stream: st.Stream, Status: state, HTTPStatus: gatewayStatus, UpstreamHTTPStatus: upstreamStatus, FinishedAt: &end, LatencyMS: totalMS,
		TTFTMS: ttft, InputTokens: inTok, OutputTokens: outTok, TotalTokens: totTok,
		ResponseBytes: respBytes, ErrorCode: code, ErrorMessage: msg,
	}
	if key != nil {
		logItem.APIKeyID = key.ID
		logItem.APIKeyName = key.Name
	}
	o.s.audit.FinishRequest(logItem)
	if tokenSrc == "usage" {
		tokenSrc = "upstream_usage"
	}
	o.s.finishProviderTokenBudget(st.ID, !st.UpstreamStart.IsZero(), tokenSrc, inTok, outTok, totTok)
	o.s.persistRequestAnalysis(context.Background(), st.ID, analysisInput{
		TotalMS: totalMS, PrepMS: prepMS, ModelLoadMS: loadMS, UpstreamMS: upstreamMS,
		Stream: st.Stream, TTFTMS: ttft, StreamTiming: timings,
		TimeToFirstByteMS: firstByteMS, TimeToUpstreamHeadersMS: upstreamHeadersMS,
		InputTokens: inTok, OutputTokens: outTok, TokenSource: tokenSrc, PredictedPerSec: pps,
	})
	if o.s.cfg.Logging.StorePayload && (len(st.Body) > 0 || len(resp) > 0) {
		o.s.audit.SavePayload(st.ID, st.Body, resp)
	}
	o.s.persistCapturedHeaders(st.ID)
	if len(resp) > 0 {
		o.s.liveSetResBody(st.ID, resp)
	}
	o.s.liveFinish(context.Background(), st.ID)
	if state == "success" && key != nil {
		modelKey := st.Model.ModelKey
		kid := key.ID
		go func() {
			o.s.keys.Touch(context.Background(), kid)
			if modelKey != "" {
				o.s.models.MarkUsed(context.Background(), modelKey)
			}
		}()
	}
	if msg != "" {
		applog.Printf("[%s] inference finished: state=%s gateway_status=%d upstream_status=%d code=%s latency=%dms error=%s", st.ID, state, gatewayStatus, upstreamStatus, code, totalMS, msg)
		return
	}
	applog.Printf("[%s] inference finished: state=%s gateway_status=%d upstream_status=%d code=%s latency=%dms", st.ID, state, gatewayStatus, upstreamStatus, code, totalMS)
}

// finishStatus 分开计算 Gateway 状态与上游状态。流式保活或首个数据块提交状态行后，Gateway 状态不可再被上游改写。
func finishStatus(ctx context.Context, ev core.Event, reason string) (gatewayStatus, upstreamStatus int, state, code, msg string) {
	if ev.Result != nil {
		upstreamStatus = ev.Result.UpstreamStatus
	}

	committed := false
	if ev.State.Sink != nil && ev.State.Sink.HeaderWritten() {
		committed = true
		if statusSink, ok := ev.State.Sink.(interface{ StatusCode() int }); ok {
			gatewayStatus = statusSink.StatusCode()
		}
		if gatewayStatus == 0 {
			gatewayStatus = http.StatusOK
		}
	}

	fallbackStatus := 0
	if ev.Err != nil {
		fallbackStatus, code, msg = core.Unpack(ev.Err)
	} else if upstreamStatus != 0 {
		fallbackStatus = upstreamStatus
	} else {
		fallbackStatus = http.StatusOK
	}
	if !committed {
		gatewayStatus = fallbackStatus
	}

	if isDeadline(ctx, ev.Err) {
		if reason == "" {
			reason = "网关请求超时"
		}
		if !committed {
			gatewayStatus = http.StatusGatewayTimeout
		}
		return gatewayStatus, upstreamStatus, "error", "gateway_timeout", reason
	}
	if ev.Err != nil && (isCancel(ctx) || isCancelErr(ev.Err)) {
		if reason == "" {
			reason = "客户端连接已断开"
		}
		if !committed {
			gatewayStatus = 499
		}
		return gatewayStatus, upstreamStatus, "cancelled", "cancelled", reason
	}

	diagnosticStatus := upstreamStatus
	if diagnosticStatus == 0 {
		diagnosticStatus = fallbackStatus
	}
	if ev.Err != nil {
		if upstreamStatus >= 400 {
			body := []byte(nil)
			if ev.Result != nil {
				body = ev.Result.Body
			}
			return gatewayStatus, upstreamStatus, "error", "upstream_http_error", upstreamErrorMessage(upstreamStatus, body, ev.Err)
		}
		return gatewayStatus, upstreamStatus, "error", code, msg
	}
	if diagnosticStatus < 200 || diagnosticStatus >= 300 {
		body := []byte(nil)
		if ev.Result != nil {
			body = ev.Result.Body
		}
		return gatewayStatus, upstreamStatus, "error", "upstream_http_error", upstreamErrorMessage(diagnosticStatus, body, nil)
	}
	return gatewayStatus, upstreamStatus, "success", "", ""
}

func isCancel(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil && isCancelErr(ctx.Err())
}

func isCancelErr(err error) bool {
	return errors.Is(err, context.Canceled)
}

func isDeadline(ctx context.Context, err error) bool {
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
