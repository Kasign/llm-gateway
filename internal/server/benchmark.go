// server（benchmark）：管理台「性能测试」的后端实现。
//
// 流程概览：
//  1. POST /api/benchmark（benchmarkAPI）校验管理员、解析要测的模型与采样参数，创建 benchmarkJob 并立即返回 job_id。
//  2. 后台 goroutine（runBenchmark）确保上游已 load、流式调用 /v1/chat/completions，边收边更新 job 与 live 视图。
//  3. GET /api/benchmark/{id} 轮询 job 快照、流事件；完成后附带 request 分析；DELETE 可取消进行中的任务。
//
// Task/Run 元数据持久化到 MariaDB；运行中的 Job 仍保留在内存供实时轮询。请求正文与事件继续由现有 request_logs / payload / stream event 仓储管理。
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/model"
	"github.com/whl/llm-gateway/internal/store"
)

// benchmarkJobTTL 已完成任务在内存中保留时长，避免 benchmarkStore 无限增长。
const (
	benchmarkJobTTL = time.Hour
	maxActiveBenchmarkJobs = 1
	maxBenchmarkResponseBytes = 8 << 20
	maxBenchmarkExecutionTime = 30 * time.Minute
)

var errBenchmarkResponseTooLarge = errors.New("benchmark response exceeded the 8 MiB capture limit")

func benchmarkExecutionTimeout(configuredSeconds int) time.Duration {
	limit := maxBenchmarkExecutionTime
	if configuredSeconds > 0 {
		configured := time.Duration(configuredSeconds) * time.Second
		if configured < limit { limit = configured }
	}
	return limit
}

// benchmarkJob 表示一次基准测试任务的状态与结果。
//
// ModelKey：网关侧 models 表主键（见下方 benchmarkAPI 中 modelKey 的解析说明），用于展示与 load；实际发给 llama 的名字在 LlamaModel。
// RequestID 与 job ID 相同，便于 /api/requests/{id} 查完整审计与流式片段。
type benchmarkJob struct {
	mu             sync.RWMutex
	ID             string     `json:"job_id"`
	Status         string     `json:"status"`
	ModelKey       string     `json:"model_key"`
	LlamaModel     string     `json:"llama_model"`
	PromptLen      int        `json:"prompt_len"`
	MaxTokens      int        `json:"max_tokens"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	LatencyMS      int64      `json:"latency_ms,omitempty"`
	HTTPStatus     int        `json:"http_status,omitempty"`
	ResponseBytes  int64      `json:"response_bytes,omitempty"`
	ResponseTruncated bool `json:"response_truncated,omitempty"`
	InputTokens    int64      `json:"input_tokens,omitempty"`
	OutputTokens   int64      `json:"output_tokens,omitempty"`
	TotalTokens    int64      `json:"total_tokens,omitempty"`
	Response       string     `json:"response,omitempty"`
	Answer         string     `json:"answer,omitempty"`
	Error          string     `json:"error,omitempty"`
	RequestID      string     `json:"request_id,omitempty"`
	Stream         bool       `json:"stream"`
	TTFTMS         *int64     `json:"ttft_ms,omitempty"`
	StreamEvents   int        `json:"stream_event_count,omitempty"`
	ReasoningChars int        `json:"reasoning_chars,omitempty"`
	ContentText    string     `json:"content_text,omitempty"`
	ReasoningText  string     `json:"reasoning_text,omitempty"`
	ContentChars   int        `json:"content_chars,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	taskRecord     store.BenchmarkTask
	runRecord      store.BenchmarkRun
	cancel         context.CancelFunc
}

// benchRunInput 性能测试可调参数；指针字段为 nil 时不写入上游 JSON（沿用模型/服务端默认）。
type benchRunInput struct {
	Prompt           string
	MaxTokens        int
	Temperature      *float64
	TopP             *float64
	TopK             *int
	MinP             *float64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	RepeatPenalty    *float64
	Seed             *int64
	EnableThinking   *bool
	Stop             []string
}

// setCancel / clearCancel / requestStop：DELETE /api/benchmark/{id} 通过 cancel 中断 runBenchmark（加载阶段与推理阶段）。
func (j *benchmarkJob) setCancel(c context.CancelFunc) {
	j.mu.Lock()
	j.cancel = c
	j.mu.Unlock()
}

func (j *benchmarkJob) clearCancel() {
	j.mu.Lock()
	j.cancel = nil
	j.mu.Unlock()
}

// markCancellingWith 在持有 Job 状态锁时先完成持久化转换，再更新内存状态。
// 这样自然完成与取消请求按同一把锁排序，避免数据库先进入 cancelling、内存却已终态。
func (j *benchmarkJob) markCancellingWith(persist func() (bool, error)) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Status != "running" {
		return false, nil
	}
	changed, err := persist()
	if err != nil || !changed {
		return false, err
	}
	j.Status = "cancelling"
	return true, nil
}

func (j *benchmarkJob) requestStop() bool {
	j.mu.RLock()
	c := j.cancel
	j.mu.RUnlock()
	if c == nil {
		return false
	}
	c()
	return true
}

// benchmarkJobSnapshot 是用于 HTTP 返回的只读 DTO，不包含互斥锁和取消函数。
// 不能把 benchmarkJob 按值返回或复制：其中的 sync.RWMutex 复制后会破坏并发语义。
type benchmarkJobSnapshot struct {
	ID             string     `json:"job_id"`
	Status         string     `json:"status"`
	ModelKey       string     `json:"model_key"`
	LlamaModel     string     `json:"llama_model"`
	PromptLen      int        `json:"prompt_len"`
	MaxTokens      int        `json:"max_tokens"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	LatencyMS      int64      `json:"latency_ms,omitempty"`
	HTTPStatus     int        `json:"http_status,omitempty"`
	ResponseBytes  int64      `json:"response_bytes,omitempty"`
	ResponseTruncated bool `json:"response_truncated,omitempty"`
	InputTokens    int64      `json:"input_tokens,omitempty"`
	OutputTokens   int64      `json:"output_tokens,omitempty"`
	TotalTokens    int64      `json:"total_tokens,omitempty"`
	Response       string     `json:"response,omitempty"`
	Answer         string     `json:"answer,omitempty"`
	Error          string     `json:"error,omitempty"`
	RequestID      string     `json:"request_id,omitempty"`
	Stream         bool       `json:"stream"`
	TTFTMS         *int64     `json:"ttft_ms,omitempty"`
	StreamEvents   int        `json:"stream_event_count,omitempty"`
	ReasoningChars int        `json:"reasoning_chars,omitempty"`
	ContentText    string     `json:"content_text,omitempty"`
	ReasoningText  string     `json:"reasoning_text,omitempty"`
	ContentChars   int        `json:"content_chars,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
}

// snapshot 在锁内复制业务字段到独立 DTO，供 HTTP 序列化使用。
func (j *benchmarkJob) snapshot() benchmarkJobSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := benchmarkJobSnapshot{
		ID: j.ID, Status: j.Status, ModelKey: j.ModelKey, LlamaModel: j.LlamaModel,
		PromptLen: j.PromptLen, MaxTokens: j.MaxTokens, StartedAt: j.StartedAt,
		FinishedAt: j.FinishedAt, LatencyMS: j.LatencyMS, HTTPStatus: j.HTTPStatus,
		ResponseBytes: j.ResponseBytes, ResponseTruncated: j.ResponseTruncated, InputTokens: j.InputTokens, OutputTokens: j.OutputTokens,
		TotalTokens: j.TotalTokens, Response: j.Response, Answer: j.Answer, Error: j.Error,
		RequestID: j.RequestID, Stream: j.Stream, TTFTMS: j.TTFTMS, StreamEvents: j.StreamEvents,
		ReasoningChars: j.ReasoningChars, ContentText: j.ContentText, ReasoningText: j.ReasoningText,
		ContentChars: j.ContentChars, ErrorCode: j.ErrorCode,
	}
	return out
}

func (j *benchmarkJob) updateStreamText(content, reasoning string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ContentText = content
	j.ReasoningText = reasoning
	j.ContentChars = len(content)
	j.ReasoningChars = len(reasoning)
}

// finish 在任务结束时写入延迟、HTTP 状态、原始响应、解析出的 answer/token，以及 TTFT（首 token 时间）。
func (j *benchmarkJob) finish(status string, httpStatus int, latency int64, resp []byte, err error, ttft *int64, streamEvents int, reasoningChars int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	j.Status = status
	j.FinishedAt = &now
	j.LatencyMS = latency
	j.HTTPStatus = httpStatus
	j.ResponseBytes = int64(len(resp))
	j.ResponseTruncated = errors.Is(err, errBenchmarkResponseTooLarge)
	j.Response = string(resp)
	if logutil.LooksLikeSSE(resp) {
		content, reasoning := logutil.AggregateChatStream(resp)
		j.ContentText = content
		j.ReasoningText = reasoning
		j.ContentChars = len(content)
		j.ReasoningChars = len(reasoning)
		if strings.TrimSpace(content) != "" {
			j.Answer = content
		} else {
			j.Answer = extractChatAnswer(resp)
		}
	} else {
		j.Answer = extractChatAnswer(resp)
	}
	j.TTFTMS = ttft
	j.StreamEvents = streamEvents
	if j.ReasoningChars == 0 {
		j.ReasoningChars = reasoningChars
	}
	if err != nil {
		j.Error = err.Error()
		_, code, _ := core.Unpack(err)
		if errors.Is(err, errBenchmarkResponseTooLarge) {
			j.ErrorCode = "benchmark_response_too_large"
		} else if code != "" {
			j.ErrorCode = code
		} else if isCancelErr(err) {
			j.ErrorCode = "cancelled"
		} else {
			j.ErrorCode = "benchmark_failed"
		}
	} else if httpStatus >= 400 {
		j.Error = "benchmark request returned a non-success status"
		j.ErrorCode = "upstream_error"
	} else {
		j.Error = ""
		j.ErrorCode = ""
	}
	in, out, total := usageFromResponse(resp)
	j.InputTokens = ptrVal(in)
	j.OutputTokens = ptrVal(out)
	j.TotalTokens = ptrVal(total)
}

// benchmarkStore 是运行期 Job 快照缓存；Task/Run 元数据持久化在 MariaDB，重启后通过仓储恢复历史摘要。
type benchmarkStore struct {
	mu sync.RWMutex
	jobs map[string]*benchmarkJob
	reservations int
}

func usageFromResponse(body []byte) (*int64, *int64, *int64) {
	h := logutil.TokensFromResponse(body)
	return h.InputTokens, h.OutputTokens, h.TotalTokens
}

func extractChatAnswer(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if logutil.LooksLikeSSE(body) {
		content, reasoning := logutil.AggregateChatStream(body)
		if c := strings.TrimSpace(content); c != "" {
			return c
		}
		if r := strings.TrimSpace(reasoning); r != "" {
			if len(r) > 1500 {
				r = r[:1500] + "…"
			}
			return "（模型仍在/仅输出 reasoning，无正文 content）\n" + r
		}
		return ""
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &out) != nil {
		return ""
	}
	if len(out.Choices) == 0 {
		return ""
	}
	if c := strings.TrimSpace(out.Choices[0].Message.Content); c != "" {
		return c
	}
	return strings.TrimSpace(out.Choices[0].Text)
}

func newBenchmarkStore() *benchmarkStore {
	return &benchmarkStore{jobs: make(map[string]*benchmarkJob)}
}

func (bs *benchmarkStore) put(job *benchmarkJob) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.jobs[job.ID] = job
}

// reserveSlot 在任务持久化前预留名额，避免并发创建请求同时越过活动任务上限。
func (bs *benchmarkStore) reserveSlot(maxActive int) bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	active := bs.reservations
	for _, job := range bs.jobs {
		job.mu.RLock()
		if job.Status == "running" || job.Status == "cancelling" { active++ }
		job.mu.RUnlock()
	}
	if maxActive <= 0 || active >= maxActive { return false }
	bs.reservations++
	return true
}

func (bs *benchmarkStore) releaseSlot() {
	bs.mu.Lock()
	if bs.reservations > 0 { bs.reservations-- }
	bs.mu.Unlock()
}

func (bs *benchmarkStore) putReserved(job *benchmarkJob) {
	bs.mu.Lock()
	if bs.reservations > 0 { bs.reservations-- }
	bs.jobs[job.ID] = job
	bs.mu.Unlock()
}
func (bs *benchmarkStore) get(id string) (*benchmarkJob, bool) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	job, ok := bs.jobs[id]
	return job, ok
}

// cleanup 删除「已结束且 FinishedAt 早于 TTL」的任务；runBenchmark 结束时 defer 调用。
func (bs *benchmarkStore) cleanup() {
	cutoff := time.Now().Add(-benchmarkJobTTL)
	bs.mu.Lock()
	defer bs.mu.Unlock()
	for id, job := range bs.jobs {
		job.mu.RLock()
		done := job.FinishedAt != nil && job.FinishedAt.Before(cutoff)
		job.mu.RUnlock()
		if done {
			delete(bs.jobs, id)
		}
	}
}

// authenticateBenchmarkKey 只读取独立的客户端服务密钥头，不复用管理员 Session / Admin Key。
func (s *Server) authenticateBenchmarkKey(r *http.Request) (*auth.Key, string) {
	raw := strings.TrimSpace(r.Header.Get("X-Client-API-Key"))
	if raw == "" {
		return nil, "invalid_api_key"
	}
	keyRequest := r.Clone(r.Context())
	keyRequest.Header = r.Header.Clone()
	keyRequest.Header.Set("Authorization", "Bearer "+raw)
	return s.authenticateOutcome(r.Context(), keyRequest)
}

// benchmarkAPI 处理 POST /api/benchmark：创建异步 benchmark 任务并返回 poll URL。
//
// # model_key 是什么、从哪来？
//
// model_key 是网关在 SQLite models 表里的业务主键（model.Model.ModelKey），与磁盘上的 GGUF、
// llama.cpp 注册名（UpstreamID / llama_model_name）分开存储。典型来源：
//   - 在「模型管理」扫描/安装本地 GGUF 时写入（常基于文件名或用户指定的 key）；
//   - API 调用方在 JSON 里显式传 "model_key": "my-qwen-7b" 时直接使用。
//
// 管理台 benchPayloadForApi：若 /api/admin/runtime 返回 gateway_model_key，则 POST 只带 model_key；
// 否则 fallback 为 use_active + model_id（active_model，上游注册名，大小写常与 GGUF 文件名一致）。
//
// 注意：runBenchmark 最终请求 llama 的 JSON 里 "model" 字段是 llamaName（上游 ID），不是 model_key。
//
// # 请求体如何决定测哪个模型？（优先级从高到低）
//
//  1. model_id（或兼容字段 model）：按上游 ID 测；若 DB 里能反查到网关 Model，则 m 非 nil，否则仅 upstreamOnly 有值、走纯上游名 load。
//  2. model_key：s.models.GetByKey，必须存在于 models 表。
//  3. 以上皆空：resolveBenchmarkTarget → 与 /v1/chat 代理相同的「当前配置/active 模型」逻辑。
//
// use_active 为 true 时会清空 body 里的 model_key，避免与 model_id 冲突；管理台始终 use_active:true。
//
// # 写入 job.ModelKey 的 modelKey 变量
//
//  1. 默认取请求里的 in.ModelKey（显式指定 model_key 时即此值）；
//  2. 若为空且解析得到 m *model.Model，则回填 m.ModelKey（例如只传 model_id 但反查到了 DB 行）；
//  3. 若 m 为 nil 且从未传 model_key，job.ModelKey 可能为空字符串，但 runBenchmark 仍可用 upstreamOnly 调用上游。
func (s *Server) benchmarkAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	key, authCode := s.authenticateBenchmarkKey(r)
	if authCode != "" {
		status, message := http.StatusUnauthorized, "Invalid client API key"
		if authCode == "key_expired" { message = "Client API key expired" }
		jsonError(w, status, authCode, message)
		return
	}
	allowed, limitErr := s.allowClientKeyRequest(r.Context(), key)
	if limitErr != nil {
		jsonError(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "rate limiter unavailable")
		return
	}
	if !allowed {
		jsonError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "rate limit exceeded")
		return
	}
	var in struct {
		ModelKey         string   `json:"model_key"`
		ModelID          string   `json:"model_id"`
		Model            string   `json:"model"`
		Prompt           string   `json:"prompt"`
		MaxTokens        int      `json:"max_tokens"`
		UseActive        bool     `json:"use_active"`
		Temperature      *float64 `json:"temperature"`
		TopP             *float64 `json:"top_p"`
		TopK             *int     `json:"top_k"`
		MinP             *float64 `json:"min_p"`
		FrequencyPenalty *float64 `json:"frequency_penalty"`
		PresencePenalty  *float64 `json:"presence_penalty"`
		RepeatPenalty    *float64 `json:"repeat_penalty"`
		Seed             *int64   `json:"seed"`
		EnableThinking   *bool    `json:"enable_thinking"`
		Stop             []string `json:"stop"`
	}
	if err := decodeBenchmarkRequestBody(w, r, &in, maxInferenceRequestBodyBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			jsonError(w, http.StatusRequestEntityTooLarge, "request_too_large", "benchmark request body exceeds the 64 MiB limit")
		} else {
			jsonError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		}
		return
	}
	in.ModelKey = strings.TrimSpace(in.ModelKey)
	modelID := strings.TrimSpace(in.ModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(in.Model)
	}
	// 管理台「跟当前 loaded 模型测」：忽略 model_key，以 model_id / 默认 active 为准。
	if in.UseActive && in.ModelKey != "" {
		in.ModelKey = ""
	}
	var m *model.Model
	upstreamOnly := ""
	switch {
	case modelID != "":
		upstreamOnly = modelID
		m = s.gatewayModelForUpstreamID(r.Context(), modelID)
	case in.ModelKey != "":
		got, e := s.models.GetByKey(r.Context(), in.ModelKey)
		if e != nil {
			jsonError(w, 404, "not_found", "model not found")
			return
		}
		m = got
	default:
		var err error
		m, upstreamOnly, err = s.resolveBenchmarkTarget(r.Context())
		if err != nil {
			jsonError(w, 400, "no_active_model", err.Error())
			return
		}
	}
	if in.Prompt == "" {
		in.Prompt = "Write a small, correct Objective-C function and explain it in one paragraph."
	}
	if in.MaxTokens <= 0 || in.MaxTokens > 8192 {
		in.MaxTokens = 4000
	}
	runIn := benchRunInput{
		Prompt: in.Prompt, MaxTokens: in.MaxTokens, Temperature: in.Temperature, TopP: in.TopP, TopK: in.TopK,
		MinP: in.MinP, FrequencyPenalty: in.FrequencyPenalty, PresencePenalty: in.PresencePenalty,
		RepeatPenalty: in.RepeatPenalty, Seed: in.Seed, EnableThinking: in.EnableThinking, Stop: cleanStopList(in.Stop),
	}
	jobID := newID()
	modelKey := in.ModelKey
	if modelKey == "" && m != nil {
		modelKey = m.ModelKey
	}
	activeUpstream := s.effectiveUpstream()
	target := benchmarkTargetSnapshot{Backend: activeUpstream.Backend, ModelName: activeUpstream.ModelName}
	taskModelID := upstreamOnly
	if taskModelID == "" {
		taskModelID = activeUpstream.ModelName
	}
	configJSON, configErr := json.Marshal(map[string]interface{}{
		"backend": target.Backend, "provider_id": target.Backend, "model_key": modelKey,
		"model_id": taskModelID, "max_tokens": in.MaxTokens, "stream": true,
		"temperature": in.Temperature, "top_p": in.TopP, "top_k": in.TopK, "min_p": in.MinP,
		"frequency_penalty": in.FrequencyPenalty, "presence_penalty": in.PresencePenalty,
		"repeat_penalty": in.RepeatPenalty, "seed": in.Seed, "enable_thinking": in.EnableThinking, "stop": runIn.Stop,
	})
	if configErr != nil {
		jsonError(w, http.StatusInternalServerError, "benchmark_config_failed", "failed to snapshot benchmark configuration")
		return
	}
	promptHash := sha256.Sum256([]byte(in.Prompt))
	now := time.Now()
	keyID := key.ID
	taskRecord := store.BenchmarkTask{
		TaskID: jobID, APIKeyID: &keyID, Scenario: "performance", Status: "running",
		ProviderID: target.Backend, Backend: target.Backend, ModelKey: modelKey, ModelID: taskModelID,
		PromptSHA256: hex.EncodeToString(promptHash[:]), PromptLength: int64(len(in.Prompt)),
		MaxTokens: in.MaxTokens, ConfigJSON: string(configJSON), RunCount: 1, StartedAt: now,
	}
	runRecord := store.BenchmarkRun{
		RunID: jobID, TaskID: jobID, RunNo: 1, RequestID: jobID, Status: "running",
		ProviderID: target.Backend, ModelKey: modelKey, ModelID: taskModelID, StartedAt: now,
	}
	job := &benchmarkJob{
		ID: jobID, Status: "running", ModelKey: modelKey, RequestID: jobID, Stream: true,
		PromptLen: len(in.Prompt), MaxTokens: in.MaxTokens, StartedAt: now,
		taskRecord: taskRecord, runRecord: runRecord,
	}
	if s.benchmarkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "benchmark_store_unavailable", "benchmark task storage is unavailable")
		return
	}
	if !s.benchmarks.reserveSlot(maxActiveBenchmarkJobs) {
		jsonError(w, http.StatusTooManyRequests, "benchmark_busy", "another benchmark task is still active")
		return
	}
	reservationHeld := true
	defer func() { if reservationHeld { s.benchmarks.releaseSlot() } }()
	if err := s.benchmarkRepo.CreateTaskAndRun(r.Context(), taskRecord, runRecord); err != nil {
		applog.Printf("[benchmark:%s] task persistence failed: %v", jobID, err)
		jsonError(w, http.StatusServiceUnavailable, "benchmark_store_unavailable", "failed to persist benchmark task")
		return
	}
	s.benchmarks.putReserved(job)
	reservationHeld = false
	workerCtx, workerCancel := context.WithCancel(context.Background())
	job.setCancel(workerCancel)
	go s.runBenchmark(workerCtx, workerCancel, job, m, upstreamOnly, runIn, key, target)
	applog.Infof("[benchmark:%s] queued model_key=%q upstream_only=%q prompt_len=%d max_tokens=%d", jobID, modelKey, upstreamOnly, len(in.Prompt), in.MaxTokens)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"job_id":      jobID,
		"request_id":  jobID,
		"status":      "running",
		"poll":        "/api/benchmark/" + jobID,
		"request_log": "/api/requests/" + jobID,
		"message":     "benchmark started; poll the job endpoint for results",
	})
}

// benchmarkStatusAPI 路由 /api/benchmark/{job_id}：GET 查询状态，DELETE 请求停止。
func (s *Server) benchmarkStatusAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/benchmark/")
	id = strings.Trim(id, "/")
	if id == "" {
		jsonError(w, 400, "invalid_request", "job_id is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.benchmarkGet(w, r, id)
	case http.MethodDelete:
		s.benchmarkStop(w, r, id)
	default:
		jsonError(w, 405, "method_not_allowed", "GET or DELETE required")
	}
}

func (s *Server) benchmarkStop(w http.ResponseWriter, r *http.Request, id string) {
	job, ok := s.benchmarks.get(id)
	if !ok {
		if s.benchmarkRepo != nil {
			if task, err := s.benchmarkRepo.GetTask(r.Context(), id); err == nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"job_id": id, "status": task.Status, "stopped": false})
				return
			}
		}
		jsonError(w, 404, "not_found", "benchmark job not found")
		return
	}
	snap := job.snapshot()
	if snap.Status != "running" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"job_id": id, "status": snap.Status, "stopped": false})
		return
	}
	if s.benchmarkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "benchmark_store_unavailable", "benchmark task storage is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	changed, err := job.markCancellingWith(func() (bool, error) {
		return s.benchmarkRepo.MarkTaskCancelling(ctx, id)
	})
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "benchmark_store_unavailable", "failed to persist cancellation request")
		return
	}
	if !changed {
		snap = job.snapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"job_id": id, "status": snap.Status, "stopped": false})
		return
	}
	stopped := job.requestStop()
	if s.runs != nil && s.runs.Cancel(id) {
		stopped = true
	}
	finalStatus := job.snapshot().Status
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"job_id": id, "status": finalStatus, "stopped": stopped})
}

func (s *Server) benchmarkGet(w http.ResponseWriter, r *http.Request, id string) {
	job, ok := s.benchmarks.get(id)
	if !ok {
		s.benchmarkGetFromStore(w, r, id)
		return
	}
	events, _ := s.logs.ListStreamEvents(r.Context(), id, 2000)
	snap := job.snapshot()
	payloadPersisted := false
	if s.cfg != nil && s.cfg.Logging.StorePayload && s.logs != nil && snap.Status != "running" && snap.Status != "cancelling" {
		_, payloadErr := s.logs.GetPayload(r.Context(), snap.RequestID)
		payloadPersisted = payloadErr == nil
	}
	out := map[string]interface{}{
		"job": snap, "stream_events": events, "request_id": snap.RequestID,
		"request_log": "/api/requests/" + snap.RequestID, "payload_persisted": payloadPersisted,
	}
	if snap.Status != "running" && snap.Status != "cancelling" {
		if raw, err := s.logs.GetRequestAnalysis(r.Context(), id); err == nil {
			if a, err := decodeRequestAnalysis(raw); err == nil && a != nil {
				out["analysis"] = a
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// resolveBenchmarkTarget 在未指定 model_key/model_id 时，复用代理链路的 active 模型解析。
func (s *Server) resolveBenchmarkTarget(ctx context.Context) (*model.Model, string, error) {
	t, err := s.resolveProxyModelTarget(ctx)
	if err != nil {
		return nil, "", err
	}
	return t.Model, t.UpstreamID, nil
}

var errBenchmarkNoModel = &benchErr{msg: "当前 API 推理后端无已加载模型；请在「模型管理」加载，或在概览保存上游模型名"}

type benchErr struct{ msg string }

func (e *benchErr) Error() string { return e.msg }

func cleanStopList(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func setBenchFloat(obj map[string]interface{}, key string, v *float64) {
	if v != nil {
		obj[key] = *v
	}
}

func setBenchInt(obj map[string]interface{}, key string, v *int) {
	if v != nil {
		obj[key] = *v
	}
}

func setBenchInt64(obj map[string]interface{}, key string, v *int64) {
	if v != nil {
		obj[key] = *v
	}
}

// buildBenchmarkChatBody 构造流式 chat/completions 请求体，并经 sanitizeForLlama 合并 system、补 stream_options。
func buildBenchmarkChatBody(llamaName string, in benchRunInput) []byte {
	obj := map[string]interface{}{
		"model": llamaName,
		"messages": []map[string]string{
			{"role": "user", "content": in.Prompt},
		},
		"stream": true,
	}
	if in.MaxTokens > 0 {
		obj["max_tokens"] = in.MaxTokens
	}
	setBenchFloat(obj, "temperature", in.Temperature)
	setBenchFloat(obj, "top_p", in.TopP)
	setBenchInt(obj, "top_k", in.TopK)
	setBenchFloat(obj, "min_p", in.MinP)
	setBenchFloat(obj, "frequency_penalty", in.FrequencyPenalty)
	setBenchFloat(obj, "presence_penalty", in.PresencePenalty)
	setBenchFloat(obj, "repeat_penalty", in.RepeatPenalty)
	setBenchInt64(obj, "seed", in.Seed)
	if in.EnableThinking != nil {
		obj["enable_thinking"] = *in.EnableThinking
	}
	if len(in.Stop) > 0 {
		obj["stop"] = in.Stop
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	return sanitizeForLlama("/v1/chat/completions", raw)
}

// runBenchmark 在后台执行单次测试：InferenceLoad → WaitUntilModelReady → 推理主流程 Run。
// 推理与 /v1 共用同一条 Run；请求日志由主流程观察者写入，这里只维护 job 状态。
//
// m 非 nil 时用 m.ModelKey 做网关侧 load（InferenceLoad 的 modelKey 参数）；m 为 nil 时用 upstreamOnly 作为上游模型名 load。
// llamaName 为最终写入 JSON "model" 字段的名字，可能在 load 后被 LoadedAs 改写。
type benchmarkTargetSnapshot struct {
	Backend   string
	ModelName string
}

func (s *Server) runBenchmark(workerCtx context.Context, workerCancel context.CancelFunc, job *benchmarkJob, m *model.Model, upstreamOnly string, in benchRunInput, key *auth.Key, target benchmarkTargetSnapshot) {
	defer s.benchmarks.cleanup()
	defer s.persistBenchmarkJob(job)
	defer workerCancel()
	defer job.clearCancel()
	ctx, cancel := context.WithTimeout(workerCtx, benchmarkExecutionTimeout(s.cfg.Llama.RequestTimeout))
	defer cancel()

	be := target.Backend
	var llamaName string
	if m != nil {
		llamaName = s.resolveLlamaModelForBackend(ctx, be, target.ModelName, m)
	} else {
		llamaName = upstreamOnly
	}
	job.mu.Lock()
	job.LlamaModel = llamaName
	job.mu.Unlock()

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				applog.Infof("[benchmark:%s] still running elapsed=%ds llama=%s", job.ID, int(time.Since(job.StartedAt).Seconds()), llamaName)
			case <-done:
				return
			}
		}
	}()

	loadStart := time.Now()
	if m != nil {
		applog.Infof("[benchmark:%s] ensure model loaded: model_key=%s llama=%s", job.ID, m.ModelKey, llamaName)
		if res, err := s.InferenceLoad(ctx, be, "", m.ModelKey, nil); err != nil {
			applog.Infof("[benchmark:%s] model load failed after %dms: %v", job.ID, time.Since(loadStart).Milliseconds(), err)
			if isCancelErr(err) || isCancelErr(ctx.Err()) {
				job.finish("cancelled", 499, time.Since(job.StartedAt).Milliseconds(), nil, err, nil, 0, 0)
				return
			}
			if !isLlamaNotFound(err) && !isLlamaAlreadyRunning(err) {
				job.finish("failed", 502, time.Since(job.StartedAt).Milliseconds(), nil, err, nil, 0, 0)
				return
			}
			applog.Infof("[benchmark:%s] load skipped (%v), will try chat/completions directly", job.ID, err)
		} else if res.LoadedAs != "" {
			llamaName = res.LoadedAs
		} else {
			llamaName = s.resolveLlamaModelForBackend(ctx, be, target.ModelName, m)
		}
	} else {
		applog.Infof("[benchmark:%s] ensure upstream loaded: %s", job.ID, llamaName)
		if res, err := s.InferenceLoad(ctx, be, llamaName, "", nil); err != nil {
			if isCancelErr(err) || isCancelErr(ctx.Err()) {
				job.finish("cancelled", 499, time.Since(job.StartedAt).Milliseconds(), nil, err, nil, 0, 0)
				return
			}
			if !isLlamaAlreadyRunning(err) && !isLlamaNotFound(err) {
				job.finish("failed", 502, time.Since(job.StartedAt).Milliseconds(), nil, err, nil, 0, 0)
				return
			}
			applog.Infof("[benchmark:%s] load skipped (%v), continue chat", job.ID, err)
		} else if res.LoadedAs != "" {
			llamaName = res.LoadedAs
		}
	}
	job.mu.Lock()
	job.LlamaModel = llamaName
	job.mu.Unlock()
	if client, clientErr := s.localClientForBackend(be); clientErr != nil {
		applog.Printf("[benchmark:%s] backend client: %v (will still call chat)", job.ID, clientErr)
	} else if err := client.WaitUntilModelReady(ctx, llamaName); err != nil {
		applog.Printf("[benchmark:%s] model ready wait: %v (will still call chat)", job.ID, err)
	}
	applog.Debugf("[benchmark:%s] model ready in %dms resolved=%s", job.ID, time.Since(loadStart).Milliseconds(), llamaName)

	body := buildBenchmarkChatBody(llamaName, in)
	if len(body) == 0 {
		job.finish("failed", 500, time.Since(job.StartedAt).Milliseconds(), nil, errors.New("build request body failed"), nil, 0, 0)
		return
	}
	cleanupRun := s.beginInferenceRun(job.ID, key, cancel, false)
	defer cleanupRun()
	obs := &benchRunObserver{job: job, started: job.StartedAt}
	applog.Infof("[benchmark:%s] calling inference pipeline stream=true max_tokens=%d timeout=%ds body=%d bytes", job.ID, in.MaxTokens, s.cfg.Llama.RequestTimeout, len(body))
	_, err := s.pipe.Run(ctx, core.Request{
		ID: job.ID, Method: http.MethodPost, Path: "/v1/chat/completions",
		Header:       map[string][]string{"Content-Type": {"application/json"}, "Accept": {"text/event-stream"}},
		Body:         body,
		ServiceModel:       llamaName,
		ServiceProviderID: be,
		Sink:               newMemSink(),
		Started:      job.StartedAt,
		ClientName:   "benchmark",
		UserAgent:    "llm-gateway/benchmark",
		Observe:      obs,
		Guards:       s.inferenceRequestGuards(key),
	})
	// 取消或超时后 ctx 已失效，用独立的短超时把这次的请求日志刷进库。
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = s.audit.Flush(flushCtx)
	flushCancel()
	if err != nil && obs.finished.IsZero() {
		status := 502
		if isCancelErr(err) || isCancelErr(ctx.Err()) {
			job.finish("cancelled", 499, time.Since(job.StartedAt).Milliseconds(), obs.buf, err, obs.ttft, obs.events, obs.reasoning)
			return
		}
		job.finish("failed", status, time.Since(job.StartedAt).Milliseconds(), obs.buf, err, obs.ttft, obs.events, obs.reasoning)
	}
}

// benchRunObserver 只更新基准测试任务的界面状态。请求日志仍由主流程的观察者写入。
type benchRunObserver struct {
	job       *benchmarkJob
	started   time.Time
	buf       []byte
	events    int
	ttft      *int64
	reasoning int
	finished  time.Time
	truncated bool
}

func (b *benchRunObserver) On(_ context.Context, ev core.Event) {
	switch ev.Stage {
	case core.StageChunk:
		b.events++
		remaining := maxBenchmarkResponseBytes - len(b.buf)
		if remaining <= 0 {
			b.truncated = true
		} else if len(ev.Chunk) > remaining {
			b.buf = append(b.buf, ev.Chunk[:remaining]...)
			b.truncated = true
		} else {
			b.buf = append(b.buf, ev.Chunk...)
		}
		content, reasoning := logutil.AggregateChatStream(b.buf)
		b.job.updateStreamText(content, reasoning)
		b.reasoning = len(reasoning)
		if b.ttft == nil && (content != "" || reasoning != "") {
			v := time.Since(b.started).Milliseconds()
			b.ttft = &v
		}
	case core.StageFinish:
		if !b.finished.IsZero() {
			return
		}
		b.finished = time.Now()
		latency := time.Since(b.started).Milliseconds()
		status := 502
		if ev.Result != nil && ev.Result.Status != 0 {
			status = ev.Result.Status
		}
		err := ev.Err
		if !isCancelErr(err) && (b.truncated || errors.Is(err, errBenchmarkResponseTooLarge)) {
			b.truncated = true
			err = errBenchmarkResponseTooLarge
		}
		if isCancelErr(err) {
			b.job.finish("cancelled", 499, latency, b.buf, err, b.ttft, b.events, b.reasoning)
			return
		}
		if err != nil || status < 200 || status >= 300 {
			if status < 400 {
				status = 502
			}
			b.job.finish("failed", status, latency, b.buf, err, b.ttft, b.events, b.reasoning)
			return
		}
		b.job.finish("completed", status, latency, b.buf, nil, b.ttft, b.events, b.reasoning)
	}
}
