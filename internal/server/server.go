// server 是网关 HTTP 核心：路由注册、/v1 代理、管理 API、鉴权与限流，并协调各 internal 包。
package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whl/llm-gateway/internal/adminuser"
	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/audit"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/downloader"
	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/ratelimit"
	"github.com/whl/llm-gateway/internal/settings"
	"github.com/whl/llm-gateway/internal/store"
)

// Server 是网关的"大脑"，把所有组件组装在一起。
// 一次 AI 请求会依次经过：配置 → 数据库 → 鉴权 → 限流 → 模型路由 → 上游 llama.cpp → 日志记录。
type Server struct {
	// —— 基础依赖 ——
	cfg *config.Config // 网关的全部配置（从配置文件加载，含 Redis、模型存储、上游地址等）
	trustedProxyRanges []netip.Prefix // 只有直连来源命中可信网段时，才信任转发 IP 头。
	db  *sql.DB        // 数据库连接池（SQLite/MySQL，存模型、Key、请求日志等）
	// —— 数据仓库（各自负责一张表的增删改查）——
	models   *model.Repo    // 模型仓库：管理本地安装了哪些 AI 模型、下载状态、是否启用
	keys     *auth.Repo     // API Key 仓库：管理调用方的密钥、限速、过期时间
	logs     *store.Repo    // 请求日志仓库：每次 AI 请求的详细记录（耗时、token、状态等）
	audit    *audit.Writer  // 审计写入器：异步批量把日志写进数据库（不阻塞请求）
	settings *settings.Repo // 设置仓库：管理员在后台保存的上游配置、推理参数等
	// —— 外部服务客户端 ——
	llama      *llama.Client            // 当前活动本地后端客户端，负责推理传输和控制 API。
	providers  *provider.Registry       // Provider 注册表，统一推理调用边界。
	limiter    *ratelimit.Limiter       // Redis 限流器：控制每个 API Key 每分钟能请求多少次
	live       *live.Registry           // 实时请求看板：内存里记录正在跑的请求，管理后台可实时查看
	downloader *downloader.HFDownloader // HuggingFace 下载器：从网上下载 AI 模型文件（GGUF 格式）
	// —— 并发控制锁 ——
	mu               sync.Mutex   // 通用互斥锁：保护需要串行访问的共享状态
	upstreamMu       sync.RWMutex // 上游配置读写锁：改上游地址时加写锁，读时加读锁（多读单写）
	upstreamChangeMu sync.Mutex   // 串行化管理员切换与请求触发的模型自动采纳。
	localClientsMu   sync.Mutex   // 保护各本地 Provider 的稳定 HTTP 客户端缓存。
	localClients     map[string]localClientEntry
	providerConfigMu sync.RWMutex
	webProviders map[string]managedProvider
	providerEncryptionKey []byte
	providerEncryptionKeyErr error
	// —— 上游配置 ——
	upstream     settings.Upstream // 当前生效的上游配置（管理员在后台改的，优先级高）
	fileUpstream settings.Upstream // 配置文件里的默认上游配置（管理员没改时用这个）
	// —— 下载任务管理 ——
	downloadMu     sync.Mutex                    // 下载任务锁：保护下载取消函数的 map
	downloadCancel map[string]context.CancelFunc // 下载取消函数表：key 是模型名，value 是取消该下载的函数
	// —— 其他状态缓存 ——
	benchmarks  *benchmarkStore // 基准测试结果缓存：跑模型性能测试时的临时数据
	llamaRouter *bool           // 是否启用 llama 路由（缓存值，避免每次都查）
	// —— CPU 使用率采样（用于管理后台展示服务器负载）——
	cpuSampleMu         sync.Mutex // CPU 采样锁
	cpuLastTotalJiffies uint64     // 上次采样时 CPU 总时间片（用来算使用率）
	cpuLastIdleJiffies  uint64     // 上次采样时 CPU 空闲时间片
	cpuProcLastSample   time.Time  // 上次采样时间
	cpuCachedPercent    float64    // 缓存的 CPU 使用率百分比（避免频繁重算）
	// —— 管理后台状态缓存 ——
	adminStateMu     sync.Mutex                          // 管理状态锁
	adminState       *adminStateSnap                     // 管理后台快照（登录状态、配置概览等，缓存减少 DB 查询）
	runtimeSnapMu    sync.Mutex                          // 运行时快照锁
	runtimeSnapCache map[string]upstreamRuntimeSnapEntry // 上游运行时缓存（模型加载状态、显存占用等）
	systemHostMu     sync.Mutex                          // 系统主机信息锁
	systemHostCache  *systemHostStats                    // 系统主机信息缓存（CPU 核数、内存大小、磁盘空间等）
	// —— 管理页面 HTML 缓存 ——
	adminHTML    []byte // 缓存的管理后台 HTML 页面内容（读一次文件后缓存，避免每次请求都读磁盘）
	adminHTMLErr error  // 读 HTML 文件时的错误（缓存起来，避免反复尝试读一个不存在的文件）
	// —— 请求列表缓存 ——
	reqListCacheMu sync.Mutex                        // 请求列表缓存锁
	reqListCache   map[string]requestsListCacheEntry // 请求列表查询结果缓存（相同查询条件直接返回缓存）
	// —— 网页登录状态 ——
	webLoginMu sync.RWMutex     // 网页登录状态读写锁
	webLogin   webLoginSnapshot // 当前网页登录快照（谁登录了、登录态是否有效）
	pipe       *core.Pipeline   // 固化的推理主流程
	runs       *runCancels      // 进行中推理的取消函数
	calls      sync.Map         // request id -> *callState
}

// New 根据静态配置构造 Server，并尝试从数据库加载管理员保存的上游设置。
// 这是网关的"组装工厂"：把配置、数据库、Redis、下载器、llama 客户端等全部零件拼成一个能跑的 Server。
func New(cfg *config.Config, dbx *sql.DB) *Server {
	// 1. 创建 Redis 限流器（用配置里的 Redis 地址、密码、数据库编号）
	lim := ratelimit.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	// 2. 创建 HuggingFace 模型下载器
	//    传入下载基础地址、User-Agent、超时时间、模型存放目录，以及一个"下载进度回调函数"
	//    回调函数的作用：每下载一部分就把进度（已下载/总大小/状态）写到 Redis，管理后台可以实时看到下载进度
	dl := downloader.NewHF(cfg.Download.HFBaseURL, cfg.Download.UserAgent, time.Duration(cfg.Download.Timeout)*time.Second, cfg.Storage.ModelsDir,
		func(id string, done, total int64, status string) {
			_ = lim.SetDownload(context.Background(), id, map[string]interface{}{"done": done, "total": total, "status": status, "updated_at": time.Now().Unix()})
		})
	// 3. 从配置文件里组装"默认上游配置"（llama.cpp 的后端类型、地址、模型名、模型目录）
	//    Normalize 会做一些规范化处理（比如补全默认值、修正格式）
	fileUpstream := settings.Normalize(settings.Upstream{
		Backend:   cfg.Llama.Backend,
		BaseURL:   cfg.BaseURLForBackend(cfg.Llama.Backend),
		ModelName: cfg.Llama.ModelName,
		ModelsDir: cfg.Llama.ModelsDir,
	})
	// 如果配置里没指定模型目录，就用默认的存储目录
	if fileUpstream.ModelsDir == "" {
		fileUpstream.ModelsDir = cfg.Storage.ModelsDir
	}
	// 4. 创建请求日志仓库（基于数据库）
	logsRepo := store.NewRepo(dbx)
	// 5. 把所有组件组装成 Server 结构体
	srv := &Server{
		cfg: cfg, db: dbx, models: model.NewRepo(dbx), keys: auth.NewRepo(dbx), logs: logsRepo,
		audit:    audit.NewWriter(logsRepo, 0),                                                                                                   // 审计写入器（异步批量写日志）
		settings: settings.NewRepo(dbx),                                                                                                          // 设置仓库
		llama:    llama.New(cfg.Llama.BaseURL, time.Duration(cfg.Llama.RequestTimeout)*time.Second, cfg.Llama.Backend, cfg.Llama.UpstreamAPIKey), // llama.cpp 客户端
		limiter:  lim, live: live.NewRegistry(), downloader: dl, downloadCancel: make(map[string]context.CancelFunc), benchmarks: newBenchmarkStore(),
		trustedProxyRanges: parseTrustedProxyRanges(cfg.Server.TrustedProxies),
		upstream: fileUpstream, fileUpstream: fileUpstream, // 初始时当前上游 = 配置文件默认上游
	}
	// 6. 从数据库加载管理员在后台保存的上游设置（如果有的话，覆盖配置文件的默认值）
	srv.pipe = srv.newInferencePipeline()
	srv.runs = newRunCancels()
	srv.loadWebProviders(context.Background())
	srv.initUpstreamFromDB(context.Background())
	// 7. 加载网页登录状态（从数据库恢复上次的登录会话）
	srv.loadWebLoginState(context.Background())
	// 8. 打一条启动日志，记录关键配置（方便排查问题）
	applog.Infof("server initialized: redis=%s backend=%s llama=%s model=%s models_dir=%s timeout=%ds auto_load=%v", cfg.Redis.Addr, srv.effectiveUpstream().Backend, srv.effectiveUpstream().BaseURL, srv.effectiveUpstream().ModelName, cfg.Storage.ModelsDir, cfg.Llama.RequestTimeout, cfg.Llama.AutoLoadOnProxy)
	return srv
}

// ShutdownAudit 优雅退出前排空审计写队列（应在 HTTP Shutdown 之后调用）。
// 作用：程序关闭前，把还在内存里没写完的日志全部刷到数据库，避免丢失请求记录。
func (s *Server) ShutdownAudit(ctx context.Context) error {
	// 如果审计写入器还没创建（极端情况），直接返回
	if s.audit == nil {
		return nil
	}
	// 等待审计队列排空（ctx 控制最长等多久）
	return s.audit.Shutdown(ctx)
}

// CheckDependencies 启动时探测 Redis 与 LLM 上游是否可达；失败不阻止进程启动但会打日志。
// 作用：启动时做一次"健康体检"，看看 Redis 和 llama.cpp 能不能连上，连不上就警告但照样启动。
func (s *Server) CheckDependencies(ctx context.Context) error {
	// Redis 是请求治理的必需依赖；推理端只检查当前活动 Provider，避免误探测被保留的旧本地客户端。
	if err := s.limiter.Ping(ctx); err != nil {
		applog.Printf("dependency check failed: redis: %v", err)
		return fmt.Errorf("redis: %w", err)
	}
	backend, providerStatus, err := s.activeProviderStatus(ctx)
	if err != nil {
		applog.Printf("dependency check failed: active Provider %s: %v", backend, err)
		return fmt.Errorf("active Provider %s: %w", backend, err)
	}
	// 远程 Provider 在启动时只校验配置和 Registry；实际网络连通性在推理请求中验证。
	applog.Infof("dependency check ok: redis and active Provider %s (%s)", backend, providerStatus)
	return nil
}

// RecoverDownloads 把数据库里 status=downloading 的任务重新拉起来，避免重启后半截下载丢失。
// 场景：网关程序意外重启时，有些模型可能正在下载中。重启后这个函数会把那些"半截下载"重新启动。
func (s *Server) RecoverDownloads() {
	// 从数据库列出所有模型
	ms, err := s.models.List(context.Background())
	if err != nil {
		applog.Printf("recover downloads: list models failed: %v", err)
		return
	}
	// 遍历每个模型，找出状态是"正在下载"且有完整下载信息的
	for _, m := range ms {
		// 跳过不是下载中、或者缺少 HuggingFace 仓库名/文件名的（信息不全没法恢复）
		if m.Status != "downloading" || m.HFRepo == "" || m.Filename == "" {
			continue
		}
		applog.Printf("resuming model download: %s", m.ModelKey)
		// 开一个后台 goroutine 重新下载这个模型
		go s.downloadModel(m.ModelKey, m.DisplayName, m.HFRepo, m.HFRevision, m.Filename, m.Quantization)
	}
}

// Handler 返回带 request ID 中间件的 mux：对外 OpenAI 兼容路径 + 内置管理页与 /api。
// 这是网关的"路由总表"：把每个 URL 路径对应到处理函数，就像餐厅的菜单把菜名对应到厨师。
func (s *Server) Handler() http.Handler {
	// 创建一个 HTTP 路由多路复用器（mux = multiplexer，根据 URL 路径分发请求）
	mux := http.NewServeMux()

	// —— 对外 OpenAI 兼容接口（给 AI 应用调用）——
	mux.HandleFunc("/health", s.health) // 健康检查：返回 Redis 和 llama.cpp 是否正常
	// 静态资源不长期缓存，否则改完 app.js 后浏览器仍用旧脚本（例如看不到新按钮）。
	mux.Handle("/static/", noStoreStatic(http.StripPrefix("/static/", http.FileServer(http.Dir(s.webStaticDir())))))
	mux.HandleFunc("/v1/models", s.v1Models) // 兼容入口：使用管理台当前活动 Provider。
	// 独立 Agent 入口按最长路径匹配；剥离前缀后复用相同鉴权、限流、Pipeline 与审计。
	if s.cfg != nil {
		for _, route := range s.cfg.Agents {
			if !route.Enabled {
				continue
			}
			mux.Handle(route.PathPrefix+"/v1/models", s.agentRouteHandler(route, http.HandlerFunc(s.v1Models)))
			mux.Handle(route.PathPrefix+"/v1/", s.agentRouteHandler(route, http.HandlerFunc(s.proxyV1)))
		}
	}
	// 所有 /v1/*（含 /v1/chat/completions）走同一入口 proxyV1；详见 proxy_v1_chat_flow.go 流程说明。
	mux.HandleFunc("/v1/", s.proxyV1) // 兼容原来的全局活动 Provider 入口

	// —— 管理后台页面 ——
	mux.HandleFunc("/", s.adminPage) // 管理后台首页（Web UI）

	// —— 管理 API：模型管理（/api/models）——
	mux.HandleFunc("/api/models", s.modelsAPI)             // 列出已安装模型
	mux.HandleFunc("/api/models/refresh", s.refreshModels) // 刷新上游模型列表
	mux.HandleFunc("/api/models/install", s.installModel)  // 安装新模型（从 HuggingFace 下载）
	mux.HandleFunc("/api/models/", s.modelAction)          // 对单个模型操作（加载/卸载/删除/取消下载等）

	// —— 管理 API：HuggingFace 目录浏览 ——
	mux.HandleFunc("/api/hf/files", s.hfFiles)   // 查看某个 HF 仓库里有哪些 GGUF 文件
	mux.HandleFunc("/api/hf/search", s.hfSearch) // 搜索 HuggingFace 上的模型

	// —— 管理 API：管理员配置 ——
	mux.HandleFunc("/api/admin/catalog/search", s.catalogSearchAPI)                  // 搜索模型目录
	mux.HandleFunc("/api/admin/inference-params/schema", s.inferenceParamsSchemaAPI) // 获取推理参数的配置表单结构
	mux.HandleFunc("/api/admin/inference-params/reset", s.inferenceParamsResetAPI)   // 重置推理参数为默认
	mux.HandleFunc("/api/admin/inference-params", s.inferenceParamsAPI)              // 读取/保存推理参数
	mux.HandleFunc("/api/admin/audit/metrics", s.auditMetricsAPI)                     // 查看审计队列的只读聚合指标

	// —— 管理 API：下载任务 ——
	mux.HandleFunc("/api/downloads", s.downloadsAPI)                // 查看下载中的任务列表
	mux.HandleFunc("/api/downloads/dismiss", s.downloadsDismissAPI) // 清除已完成/失败的下载记录

	// —— 管理 API：请求日志 ——
	mux.HandleFunc("/api/requests", s.requestsAPI)       // 查看请求日志列表（支持筛选）
	mux.HandleFunc("/api/requests/", s.requestDetailAPI) // 查看单次请求的详细信息

	// —— 管理 API：系统引导/后端管理 ——
	mux.HandleFunc("/api/admin/bootstrap/lite", s.bootstrapLiteAPI)           // 轻量引导（首次启动的快速配置）
	mux.HandleFunc("/api/admin/bootstrap", s.bootstrapAPI)                    // 完整引导配置
	mux.HandleFunc("/api/admin/backend/unload-all", s.backendUnloadAllAPI)    // 卸载所有已加载模型
	mux.HandleFunc("/api/admin/backend/upstream-models", s.upstreamModelsAPI) // 查看上游有哪些模型
	mux.HandleFunc("/api/admin/backend", s.backendAPI)                        // 读取/修改上游后端配置
	mux.HandleFunc("/api/admin/providers", s.adminProvidersAPI)
	mux.HandleFunc("/api/admin/providers/", s.adminProviderActionAPI)

	// —— 管理 API：运行时管理（控制 llama.cpp 加载哪个模型）——
	mux.HandleFunc("/api/admin/runtime/preview", s.runtimePreviewAPI) // 预览运行时状态
	mux.HandleFunc("/api/admin/runtime/unload", s.runtimeUnloadAPI)   // 卸载当前模型
	mux.HandleFunc("/api/admin/runtime/select", s.runtimeSelectAPI)   // 选择/加载指定模型
	mux.HandleFunc("/api/admin/runtime", s.runtimeAPI)                // 读取运行时状态

	// —— 管理 API：基准测试 ——
	mux.HandleFunc("/api/benchmark/", s.benchmarkStatusAPI) // 查看基准测试进度/结果
	mux.HandleFunc("/api/benchmark", s.benchmarkAPI)        // 启动基准测试

	// —— 管理 API：本地模型清单 / Ollama 管理 ——
	mux.HandleFunc("/api/admin/local-inventory", s.localInventoryAPI)        // 扫描本地有哪些模型文件
	mux.HandleFunc("/api/admin/ollama", s.ollamaAdminAPI)                    // Ollama 管理总入口
	mux.HandleFunc("/api/admin/ollama/pull/cancel", s.ollamaPullCancelAPI)   // 取消 Ollama 拉取
	mux.HandleFunc("/api/admin/ollama/pull", s.ollamaPullAPI)                // 从 Ollama 拉取模型
	mux.HandleFunc("/api/admin/catalog/ollama/tags", s.ollamaCatalogTagsAPI) // 查看 Ollama 模型的标签列表
	mux.HandleFunc("/api/admin/ollama/load", s.ollamaLoadAPI)                // 加载 Ollama 模型
	mux.HandleFunc("/api/admin/ollama/unload", s.ollamaUnloadAPI)            // 卸载 Ollama 模型
	mux.HandleFunc("/api/admin/ollama/delete", s.ollamaDeleteAPI)            // 删除 Ollama 模型

	// —— 管理 API：其他 ——
	mux.HandleFunc("/api/admin/model-ops", s.modelOpsTestAPI) // 模型操作测试（调试用）

	// —— 管理 API：API Key 管理 ——
	mux.HandleFunc("/api/admin/keys", s.keysAPI)    // 列出/创建 API Key
	mux.HandleFunc("/api/admin/keys/", s.keyAction) // 对单个 Key 操作（删除/禁用/启用/轮换/修改）

	// —— 管理 API：网页登录认证 ——
	mux.HandleFunc("/api/auth/login", s.authLoginAPI)             // 管理员登录
	mux.HandleFunc("/api/auth/logout", s.authLogoutAPI)           // 管理员登出
	mux.HandleFunc("/api/auth/me", s.authMeAPI)                   // 获取当前登录用户信息
	mux.HandleFunc("/api/auth/credentials", s.authCredentialsAPI) // 修改管理员密码

	applog.Printf("http handler initialized: routes registered")
	// 最外层套两个中间件：
	// 1. withRecover：捕获 panic（程序崩溃），返回 500 而不是整个程序挂掉
	// 2. withRequestID：给每个请求分配唯一 ID，方便日志追踪
	return s.withRequestID(s.withCallRecord(s.withRecover(mux)))
}

// withRecover 是"防崩溃中间件"：如果处理请求时代码 panic 了（比如空指针、数组越界），
// 它会捕获住，打一条错误日志，给客户端返回 500，而不是让整个网关程序崩溃。
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// defer 保证这个匿名函数在当前函数退出时一定执行（哪怕 panic 了）
		defer func() {
			// recover() 捕获 panic，如果没有 panic 就返回 nil
			if rec := recover(); rec != nil {
				// 有 panic：打日志记录崩溃信息和访问路径
				applog.Printf("http panic: %v path=%s", rec, r.URL.Path)
				// 给客户端返回 500 内部错误
				jsonError(w, 500, "internal_error", "internal server error")
			}
		}()
		// 正常情况下，继续执行下一个处理器（真正的路由逻辑）
		next.ServeHTTP(w, r)
	})
}

// health 是健康检查接口（/health）：返回 Redis 和 llama.cpp 是否正常工作。
// 运维工具（如 Docker、K8s）会定期调这个接口来判断网关是否健康，不健康就重启。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	// 给健康检查设一个 5 秒超时，避免上游卡死导致健康检查一直挂着。
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	backend, providerStatus, providerErr := s.activeProviderStatus(ctx)
	redisErr := s.limiter.Ping(ctx)
	// 保留旧 llama 字段。切换到远程 Provider 后，本地客户端仍用于管理操作，但不再决定活动推理服务的健康状态。
	llamaStatus := "inactive (not checked for active routing)"
	if isLocalInferenceBackend(backend) {
		llamaStatus = providerStatus
	}

	status := "ok"
	if providerErr != nil || redisErr != nil {
		status = "degraded"
		w.WriteHeader(http.StatusServiceUnavailable)
		applog.Printf("health degraded: active_backend=%s provider=%s redis=%s", backend, providerStatus, errString(redisErr))
	}
	// 远程 Provider 只标记为已配置，不虚报网络可达；其实际连通性由推理请求验证。
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": status, "active_backend": backend, "provider": providerStatus,
		"llama": llamaStatus, "redis": errString(redisErr), "time": time.Now().UTC(),
	})
}

// v1Models 是 /v1/models 接口（OpenAI 兼容）：返回当前网关可用的模型列表。
// 客户端（如 ChatBox、OpenAI Python SDK）启动时会先调这个接口看看有哪些模型可选。
func (s *Server) v1Models(w http.ResponseWriter, r *http.Request) {
	// 记录开始时间和请求 ID（和 proxyV1 一样的套路）
	start := time.Now()
	id := requestID(r.Context())
	// 创建一条"运行中"的审计记录
	s.markCallRecorded(r.Context())
	logBase := store.RequestLog{RequestID: id, ClientIP: clientIP(r, s.trustedProxyRanges), ClientName: requestClientName(r), UserAgent: r.UserAgent(), Endpoint: r.URL.Path, RequestURL: agentRequestPublicURL(r, s.trustedProxyRanges), Method: r.Method, Status: "running", StartedAt: start}
	s.audit.CreateRequest(logBase)
	s.liveStart(r.Context(), logBase)
	defer s.liveFinish(context.Background(), id)

	// 声明 API Key 变量（后面鉴权通过后赋值）
	var key *auth.Key
	// 定义收尾函数（比 proxyV1 的简单，因为不需要 token 统计）
	finish := func(status int, state, code, msg string, respBytes int64) {
		end := time.Now()
		logItem := store.RequestLog{RequestID: id, APIKeyID: 0, APIKeyName: "", RequestURL: logBase.RequestURL, Status: state, HTTPStatus: status, FinishedAt: &end, LatencyMS: end.Sub(start).Milliseconds(), ResponseBytes: respBytes, ErrorCode: code, ErrorMessage: msg}
		if key != nil {
			logItem.APIKeyID = key.ID
			logItem.APIKeyName = key.Name
		}
		s.audit.FinishRequest(logItem)
	}

	// 只接受 GET 请求
	if r.Method != http.MethodGet {
		applog.Printf("[%s] /v1/models method not allowed: %s", id, r.Method)
		finish(405, "error", "method_not_allowed", "GET required", 0)
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}

	// 鉴权：验证 API Key
	var ok bool
	key, ok = s.authenticate(r.Context(), r)
	if !ok {
		applog.Printf("[%s] /v1/models auth failed: client=%s", id, clientIP(r, s.trustedProxyRanges))
		finish(401, "error", "invalid_api_key", "Invalid API key", 0)
		jsonError(w, 401, "invalid_api_key", "Invalid API key")
		return
	}

	// 限流：检查这个 Key 还有没有额度
	allowed, _, err := s.limiter.Allow(r.Context(), strconv.FormatUint(key.ID, 10), key.RPM)
	if err != nil {
		applog.Printf("[%s] /v1/models rate limiter unavailable: %v", id, err)
		finish(503, "error", "rate_limit_unavailable", err.Error(), 0)
		jsonError(w, 503, "rate_limit_unavailable", err.Error())
		return
	}
	if !allowed {
		applog.Printf("[%s] /v1/models rate limit exceeded: key_id=%d", id, key.ID)
		finish(429, "error", "rate_limit_exceeded", "rate limit exceeded", 0)
		jsonError(w, 429, "rate_limit_exceeded", "rate limit exceeded")
		return
	}
	s.livePatch(r.Context(), id, store.RequestLog{APIKeyID: key.ID, APIKeyName: key.Name})

	if route, routed := agentRouteFromContext(r.Context()); routed {
		target, targetErr := s.resolveAgentRouteTarget(r.Context(), route)
		if targetErr != nil {
			status, code, msg := core.Unpack(targetErr)
			if status < 400 {
				status, code, msg = http.StatusServiceUnavailable, "agent_route_unavailable", "Agent route is unavailable"
			}
			finish(status, "error", code, msg, 0)
			jsonError(w, status, code, msg)
			return
		}
		body, modelCount := agentModelListBody(route, target, key)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		s.keys.Touch(context.Background(), key.ID)
		finish(http.StatusOK, "success", "", "", int64(len(body)))
		applog.Printf("[%s] Agent /%s/v1/models ok: key=%s models=%d latency=%dms", id, route.ID, key.Name, modelCount, time.Since(start).Milliseconds())
		return
	}

	if _, remote := s.remoteProviderConfig(s.effectiveUpstream().Backend); remote {
		target, err := s.resolveProxyModelTarget(r.Context())
		if err != nil {
			finish(503, "error", "no_proxy_model", err.Error(), 0)
			jsonError(w, 503, "no_proxy_model", err.Error())
			return
		}
		data := make([]map[string]interface{}, 0, 1)
		modelID := strings.TrimSpace(target.ModelKey)
		if target.Model != nil {
			modelID = target.Model.ModelKey
		}
		// 远程 Provider 的 /v1 实际只路由到其当前配置模型，且仍执行每个 Gateway Key 的模型 ACL。
		if modelID != "" && modelAllowedForKey(key, modelID, target.UpstreamID, target.Hint) {
			data = append(data, map[string]interface{}{
				"id": modelID, "object": "model", "created": time.Now().Unix(),
				"owned_by": s.effectiveUpstream().Backend, "display_name": target.UpstreamID,
			})
		}
		body, _ := json.Marshal(map[string]interface{}{"object": "list", "data": data})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		s.keys.Touch(context.Background(), key.ID)
		finish(200, "success", "", "", int64(len(body)))
		applog.Printf("[%s] /v1/models ok: key=%s models=%d latency=%dms", id, key.Name, len(data), time.Since(start).Milliseconds())
		return
	}

	// 从数据库列出所有已安装的模型
	ms, err := s.models.List(r.Context())
	if err != nil {
		applog.Printf("[%s] /v1/models list failed: %v", id, err)
		finish(500, "error", "database_error", err.Error(), 0)
		jsonError(w, 500, "database_error", err.Error())
		return
	}
	// 组装 OpenAI 兼容格式的模型列表。只展示已启用且与当前 Key 的 ACL 匹配的模型。
	// 上游 ID 优先用成功加载/同步时持久化的映射，避免为列表中每个模型额外请求上游。
	backend := s.effectiveUpstream().Backend
	data := make([]map[string]interface{}, 0, len(ms))
	for _, m := range ms {
		if !m.Enabled {
			continue
		}
		upstreamID := model.UpstreamNameForBackend(m.RuntimeJSON, backend)
		if !modelAllowedForKey(key, m.ModelKey, upstreamID, m.ModelKey) {
			continue
		}
		data = append(data, map[string]interface{}{"id": m.ModelKey, "object": "model", "created": m.CreatedAt.Unix(), "owned_by": "llm-gateway", "display_name": m.DisplayName})
	}
	// 额外：如果网关配置了"代理目标模型"，也把它加进列表（即使它不在本地模型表里）
	// 这样客户端能看到网关实际会用哪个模型
	if target, err := s.resolveProxyModelTarget(r.Context()); err == nil {
		id := target.ModelKey
		if target.Model != nil {
			id = target.Model.ModelKey
		}
		// 检查是否已经在列表里了，避免重复
		found := false
		for _, row := range data {
			if row["id"] == id {
				found = true
				break
			}
		}
		// 活动模型也必须经过同一套 ACL。未获授权时不加入模型列表。
		if !modelAllowedForKey(key, id, target.UpstreamID, target.Hint) {
			found = true // 表示无需插入未授权模型。
		}
		// 不在列表里、已授权且不为空，就插到最前面
		if !found && id != "" {
			data = append([]map[string]interface{}{{
				"id": id, "object": "model", "created": time.Now().Unix(), "owned_by": "llm-gateway", "display_name": target.UpstreamID,
			}}, data...)
		}
	}

	// 序列化成 JSON 返回给客户端
	b, _ := json.Marshal(map[string]interface{}{"object": "list", "data": data})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
	// 更新这个 Key 的"最后使用时间"
	s.keys.Touch(context.Background(), key.ID)
	// 收尾：记录成功状态
	finish(200, "success", "", "", int64(len(b)))
	applog.Printf("[%s] /v1/models ok: key=%s models=%d latency=%dms", id, key.Name, len(data), time.Since(start).Milliseconds())
}

// proxyV1 把 POST /v1/* 送进固化的推理主流程。鉴权、限流和读 body 在 Run 之外。
// 阶段说明见 proxy_v1_chat_flow.go。
func (s *Server) proxyV1(w http.ResponseWriter, r *http.Request) {
	s.serveInference(w, r)
}

func (s *Server) authenticate(ctx context.Context, r *http.Request) (*auth.Key, bool) {
	k, code := s.authenticateOutcome(ctx, r)
	return k, code == ""
}

// authenticateOutcome 区分过期和无效密钥。空 code 表示通过。
func (s *Server) authenticateOutcome(ctx context.Context, r *http.Request) (*auth.Key, string) {
	raw, ok := auth.ParseBearer(r.Header.Get("Authorization"))
	if !ok {
		raw = strings.TrimSpace(r.Header.Get("X-API-Key"))
		ok = raw != ""
	}
	if !ok {
		return nil, "invalid_api_key"
	}
	if strings.HasPrefix(raw, adminuser.SessionPrefix) {
		return nil, "invalid_api_key"
	}
	k, err := s.keys.GetByRaw(ctx, raw)
	if err != nil || k == nil {
		applog.Printf("[%s] auth failed: client=%s key_status=not_found err=%v", requestID(ctx), clientIP(r, s.trustedProxyRanges), err)
		return nil, "invalid_api_key"
	}
	if k.Status == "expired" {
		applog.Printf("[%s] auth failed: client=%s key_status=expired", requestID(ctx), clientIP(r, s.trustedProxyRanges))
		return k, "key_expired"
	}
	if k.Status != "active" {
		applog.Printf("[%s] auth failed: client=%s key_status=%s", requestID(ctx), clientIP(r, s.trustedProxyRanges), k.Status)
		return nil, "invalid_api_key"
	}
	return k, ""
}

// API key management
// keysAPI 是 /api/admin/keys 接口：管理 API Key 的列表和创建。
// GET  → 列出所有 Key（给管理后台展示用）
// POST → 创建一个新的 API Key（返回明文 Key，只在创建时显示一次）
func (s *Server) keysAPI(w http.ResponseWriter, r *http.Request) {
	// 先验证是不是管理员（网页登录态或管理员 API Key）
	if !s.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// GET：从数据库列出所有 API Key
		ks, e := s.keys.List(r.Context())
		if e != nil {
			jsonError(w, 500, "database_error", e.Error())
			return
		}
		// 返回 Key 列表（注意：返回的是 Key 的元信息，不包含明文 Key）
		json.NewEncoder(w).Encode(map[string]interface{}{"data": ks})
	case http.MethodPost:
		// POST：创建新 Key，先解析请求体里的参数
		var in struct {
			Name          string     `json:"name"`           // Key 的名字（方便识别，比如"我的项目"）
			RPM           int        `json:"rpm"`            // 每分钟最大请求数（限速）
			AllowedModels []string   `json:"allowed_models"` // 允许使用的模型列表（空表示不限制）
			ExpiresAt     *time.Time `json:"expires_at"`     // 过期时间（空表示永不过期）
		}
		// 解析 JSON 失败或名字为空，返回 400
		if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" {
			jsonError(w, 400, "invalid_request", "name is required")
			return
		}
		// 创建 Key：数据库会生成一个随机的明文 Key（sk-xxx），并只存哈希值
		k, raw, e := s.keys.Create(r.Context(), strings.TrimSpace(in.Name), in.RPM, in.AllowedModels, in.ExpiresAt)
		if e != nil {
			applog.Printf("create api key failed: %v", e)
			jsonError(w, 400, "create_failed", e.Error())
			return
		}
		applog.Printf("api key created: id=%d name=%q rpm=%d", k.ID, k.Name, k.RPM)
		// 返回明文 Key（这是唯一一次能看到明文的机会，之后数据库里只有哈希）
		json.NewEncoder(w).Encode(map[string]interface{}{"key": raw, "api_key": k})
	default:
		// 其他方法不支持
		jsonError(w, 405, "method_not_allowed", "unsupported method")
	}
}

// keyAction 是 /api/admin/keys/{id}/{action} 接口：对单个 API Key 进行操作。
// 支持的操作：
//
//	DELETE /api/admin/keys/{id}          → 删除 Key
//	POST   /api/admin/keys/{id}/disable  → 禁用 Key（暂时不能用）
//	POST   /api/admin/keys/{id}/enable   → 启用 Key（恢复使用）
//	POST   /api/admin/keys/{id}/rotate   → 轮换 Key（生成新明文，旧的立即失效）
//	PUT    /api/admin/keys/{id}          → 修改 Key 的名字/限速/允许模型/过期时间
func (s *Server) keyAction(w http.ResponseWriter, r *http.Request) {
	// 验证管理员身份
	if !s.requireAdmin(w, r) {
		return
	}
	// 把 URL 路径按 "/" 拆开，比如 /api/admin/keys/123/disable → ["api", "admin", "keys", "123", "disable"]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// 路径至少要有 4 段（api/admin/keys/{id}），否则 404
	if len(parts) < 4 {
		jsonError(w, 404, "not_found", "not found")
		return
	}
	// 第 4 段是 Key 的 ID（从 0 开始数，parts[3]）
	id, _ := strconv.ParseUint(parts[3], 10, 64)

	// —— 删除 Key：DELETE /api/admin/keys/{id} ——
	if r.Method == http.MethodDelete && len(parts) == 4 {
		if e := s.keys.Delete(r.Context(), id); e != nil {
			applog.Printf("delete api key failed: id=%d err=%v", id, e)
			jsonError(w, 500, "delete_failed", e.Error())
			return
		}
		applog.Printf("api key deleted: id=%d", id)
		w.WriteHeader(204) // 204 = 删除成功，无返回内容
		return
	}

	// —— 操作 Key：POST /api/admin/keys/{id}/{action} ——
	// action 可以是 disable / enable / rotate
	if r.Method == http.MethodPost && len(parts) == 5 && (parts[4] == "disable" || parts[4] == "enable" || parts[4] == "rotate") {
		switch parts[4] {
		case "rotate":
			// 轮换：生成一个新的明文 Key，旧的立即失效（用于 Key 泄露后快速更换）
			raw, e := s.keys.Rotate(r.Context(), id)
			if e != nil {
				applog.Printf("rotate api key failed: id=%d err=%v", id, e)
				jsonError(w, 500, "rotate_failed", e.Error())
				return
			}
			applog.Printf("api key rotated: id=%d", id)
			// 返回新的明文 Key（同样只显示一次）
			json.NewEncoder(w).Encode(map[string]interface{}{"key": raw})
		default:
			// disable 或 enable：设置 Key 的状态
			st := "active"
			if parts[4] == "disable" {
				st = "disabled"
			}
			if e := s.keys.SetStatus(r.Context(), id, st); e != nil {
				applog.Printf("set api key status failed: id=%d status=%s err=%v", id, st, e)
				jsonError(w, 500, "update_failed", e.Error())
				return
			}
			applog.Printf("api key status set: id=%d status=%s", id, st)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": st})
		}
		return
	}

	// —— 修改 Key：PUT /api/admin/keys/{id} ——
	if r.Method == http.MethodPut && len(parts) == 4 {
		var in struct {
			Name          string     `json:"name"`
			RPM           int        `json:"rpm"`
			AllowedModels []string   `json:"allowed_models"`
			ExpiresAt     *time.Time `json:"expires_at"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
			jsonError(w, 400, "invalid_request", "name is required")
			return
		}
		if e := s.keys.Update(r.Context(), id, in.Name, in.RPM, in.AllowedModels, in.ExpiresAt); e != nil {
			applog.Printf("update api key failed: id=%d err=%v", id, e)
			jsonError(w, 500, "update_failed", e.Error())
			return
		}
		applog.Printf("api key updated: id=%d name=%q", id, in.Name)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
		return
	}
	// 路径或方法都不匹配，返回 404
	jsonError(w, 404, "not_found", "not found")
}

// requireAdmin 验证当前请求是否有管理员权限。
// 所有 /api/admin/* 和 /api/keys 等管理接口都会先调这个函数。
// 验证通过返回 true（继续执行），不通过返回 false（已经给客户端写好了 401 响应）。
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	// 先检查是不是通过网页登录的管理员（有有效的 session cookie）
	if s.adminAuthorized(r.Context(), r) {
		return true
	}
	// 不是网页登录，再检查有没有配置"管理员 API Key"
	// 如果根本没配置管理员 Key，说明需要先登录（返回"login required"）
	if s.cfg.Auth.AdminAPIKey == "" {
		applog.Printf("admin request rejected: not authenticated: %s from %s", r.URL.Path, clientIP(r, s.trustedProxyRanges))
		jsonError(w, 401, "unauthorized", "login required")
		return false
	}
	// 配置了管理员 Key 但请求里没带对，返回"无效的管理员 Key"
	applog.Printf("admin auth failed: %s from %s", r.URL.Path, clientIP(r, s.trustedProxyRanges))
	jsonError(w, 401, "invalid_admin_key", "Invalid admin key")
	return false
}

// Models/downloads
// modelsAPI 是 /api/models 接口：列出网关已安装的所有模型（管理后台用）。
// 和 /v1/models 不同，这个接口返回的是完整的模型信息（含下载状态、文件大小、是否启用等），且需要管理员权限。
func (s *Server) modelsAPI(w http.ResponseWriter, r *http.Request) {
	// 验证管理员身份
	if !s.requireAdmin(w, r) {
		return
	}
	// 只接受 GET
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	// 从数据库查询所有模型的完整信息（含本地文件状态、上游加载状态等）
	items, e := s.ListGatewayModelRows(r.Context())
	if e != nil {
		jsonError(w, 500, "database_error", e.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"data": items})
}

// hfFiles 是 /api/hf/files 接口：查看 HuggingFace 上某个仓库里有哪些 GGUF 模型文件。
// 管理后台"安装模型"时，用户输入仓库名后会调这个接口来展示可选的文件列表。
func (s *Server) hfFiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	// 从 URL 查询参数里取仓库名和版本（revision，默认 main）
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	rev := strings.TrimSpace(r.URL.Query().Get("revision"))
	// 仓库名不能为空
	if repo == "" {
		jsonError(w, 400, "invalid_request", "repo is required")
		return
	}
	// 设置列表查询的超时时间（默认 60 秒，HuggingFace 有时候比较慢）
	listTO := time.Duration(s.cfg.Download.CatalogTimeout) * time.Second
	if listTO <= 0 {
		listTO = 60 * time.Second
	}
	ctx, c := context.WithTimeout(r.Context(), listTO)
	defer c()
	// 调用下载器列出该仓库下所有的 GGUF 文件
	files, e := s.downloader.ListGGUF(ctx, repo, rev)
	if e != nil {
		jsonError(w, 502, "huggingface_error", e.Error())
		return
	}
	// 组装返回结果：每个文件的路径、大小、SHA256 校验值
	out := make([]map[string]interface{}, 0, len(files))
	for _, f := range files {
		out = append(out, map[string]interface{}{"path": f.Path, "size": f.SizeBytes(), "sha256": f.SHA256()})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

// installModel 是 /api/models/install 接口：安装一个新模型（从 HuggingFace 下载 GGUF 文件）。
// 流程：校验参数 → 检查是否已存在 → 写入数据库（状态 downloading）→ 开后台 goroutine 下载 → 立即返回
func (s *Server) installModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	// 解析请求体：要安装的模型信息
	var in struct {
		ModelKey     string                 `json:"model_key"`    // 模型在网关里的唯一标识（比如 "qwen2.5-7b"）
		DisplayName  string                 `json:"display_name"` // 展示名（给人看的，比如 "Qwen2.5 7B Instruct"）
		Repo         string                 `json:"repo"`         // HuggingFace 仓库名（比如 "Qwen/Qwen2.5-7B-Instruct-GGUF"）
		Revision     string                 `json:"revision"`     // 仓库版本/分支（默认 main）
		Filename     string                 `json:"filename"`     // 要下载的文件名（比如 "qwen2.5-7b-instruct-q4_k_m.gguf"）
		Quantization string                 `json:"quantization"` // 量化级别（比如 "Q4_K_M"，仅用于展示）
		Runtime      map[string]interface{} `json:"runtime"`      // 运行时参数（加载模型时的额外配置，如上下文长度）
	}
	// 解析失败或必填字段为空，返回 400
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ModelKey == "" || in.Repo == "" || in.Filename == "" {
		jsonError(w, 400, "invalid_request", "model_key, repo and filename are required")
		return
	}
	// revision 为空默认 main
	if in.Revision == "" {
		in.Revision = "main"
	}
	// 展示名为空就用 model_key 代替
	if in.DisplayName == "" {
		in.DisplayName = in.ModelKey
	}
	// 把运行时参数序列化成 JSON 字符串存数据库
	runtimeJSON, _ := json.Marshal(in.Runtime)
	// 计算模型文件在本地的存放路径（模型目录 + 文件名）
	local := filepath.Join(s.cfg.Storage.ModelsDir, filepath.Base(in.Filename))

	// —— 查重 1：model_key 不能重复 ——
	if _, e := s.models.GetByKey(r.Context(), in.ModelKey); e == nil {
		jsonError(w, 409, "model_exists", "本地已存在相同 model_key")
		return
	}
	// —— 查重 2：同一个 HF 仓库的同一个文件不能重复下载（除非之前下载失败/取消了）——
	baseFile := filepath.Base(in.Filename)
	if all, e := s.models.List(r.Context()); e == nil {
		for _, m := range all {
			if m.HFRepo == in.Repo && m.Filename == baseFile && m.Status != "download_failed" && m.Status != "download_cancelled" {
				jsonError(w, 409, "model_exists", "本地已存在该 GGUF："+m.ModelKey)
				return
			}
		}
	}

	// 写入数据库：状态设为 downloading（下载中）
	if e := s.models.Upsert(r.Context(), model.Model{ModelKey: in.ModelKey, DisplayName: in.DisplayName, SourceType: "huggingface", HFRepo: in.Repo, HFRevision: in.Revision, Filename: filepath.Base(in.Filename), LocalPath: local, Quantization: in.Quantization, LlamaModelName: filepath.Base(in.Filename), Status: "downloading", Enabled: true, RuntimeJSON: string(runtimeJSON)}); e != nil {
		applog.Printf("install model failed: key=%s err=%v", in.ModelKey, e)
		jsonError(w, 500, "database_error", e.Error())
		return
	}
	applog.Printf("model install queued: key=%s repo=%s rev=%s file=%s", in.ModelKey, in.Repo, in.Revision, in.Filename)
	// 开一个后台 goroutine 去下载（不阻塞 HTTP 响应，用户可以继续操作）
	go s.downloadModel(in.ModelKey, in.DisplayName, in.Repo, in.Revision, in.Filename, in.Quantization)
	// 立即返回"已加入下载队列"
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "status": "downloading", "model_key": in.ModelKey})
}

// downloadModel 执行真正的模型下载（在后台 goroutine 里运行）。
// 流程：加锁防重复下载 → 设置下载超时 → 更新状态为 downloading → 调用下载器下载 →
//
//	成功则更新状态为 installed 并刷新上游模型列表；失败则记录错误状态
func (s *Server) downloadModel(key, display, repo, rev, filename, quant string) {
	// 加下载锁，防止同一个模型被重复下载
	s.downloadMu.Lock()
	// 如果这个模型已经有一个正在进行的下载（downloadCancel 里有它的取消函数），就不重复下
	if _, ok := s.downloadCancel[key]; ok {
		s.downloadMu.Unlock()
		return
	}
	// 创建下载用的上下文（带超时，超时时间从配置里读）
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.Download.Timeout)*time.Second)
	// 把取消函数存到 map 里（管理后台可以通过这个函数取消下载）
	s.downloadCancel[key] = cancel
	s.downloadMu.Unlock()
	// 函数结束时：取消上下文 + 从 map 里删除这个下载的取消函数（清理资源）
	defer func() { cancel(); s.downloadMu.Lock(); delete(s.downloadCancel, key); s.downloadMu.Unlock() }()

	// 更新数据库状态为 downloading
	_ = s.models.SetStatus(ctx, key, "downloading")
	// 在 Redis 里记录下载进度（初始状态）
	_ = s.limiter.SetDownload(ctx, key, map[string]interface{}{"status": "downloading", "updated_at": time.Now().Unix()})
	// 清除之前可能残留的错误信息
	_ = s.limiter.DeleteDownloadFields(ctx, key, "error")
	applog.Printf("model download started: key=%s repo=%s rev=%s file=%s", key, repo, rev, filename)

	// 调用下载器真正下载文件（返回文件大小、SHA256 等）
	_, sha, _, e := s.downloader.Download(ctx, key, repo, rev, filename)
	if e != nil {
		// 下载失败：判断是被取消了还是真的出错了
		status := "download_failed"
		if errors.Is(e, context.Canceled) {
			status = "download_cancelled" // 用户主动取消的
		}
		applog.Printf("model download failed: key=%s status=%s err=%v", key, status, e)
		// 更新数据库状态和 Redis 里的错误信息
		_ = s.models.SetStatus(context.Background(), key, status)
		_ = s.limiter.SetDownload(context.Background(), key, map[string]interface{}{"status": status, "error": e.Error(), "updated_at": time.Now().Unix()})
		return
	}

	// 下载成功！
	applog.Printf("model download finished: key=%s sha=%s", key, sha)
	// 更新数据库状态为 installed（已安装）
	_ = s.models.SetStatus(context.Background(), key, "installed")
	// 记录文件大小和 SHA256 校验值到数据库
	local := filepath.Join(s.cfg.Storage.ModelsDir, filepath.Base(filename))
	var size int64
	if fi, err := os.Stat(local); err == nil {
		size = fi.Size()
	}
	_ = s.models.SetArtifact(context.Background(), key, size, sha)
	// 刷新 llama.cpp 的模型注册表（让上游知道有新模型可用了）
	_, _ = s.InferenceRefreshRegistry(context.Background(), "llamacpp")
}

// modelAction 是 /api/models/{key}/{action} 接口：对单个模型执行操作。
// 支持的操作：
//
//	DELETE /api/models/{key}           → 删除模型（同时删本地文件）
//	POST   /api/models/{key}/load      → 把模型加载到 llama.cpp 内存
//	POST   /api/models/{key}/unload    → 从内存卸载模型
//	POST   /api/models/{key}/refresh   → 刷新上游模型列表
//	POST   /api/models/{key}/cancel    → 取消正在进行的下载
//	POST   /api/models/{key}/retry     → 重试失败的下载
func (s *Server) modelAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	// 解析 URL 路径，比如 /api/models/qwen2.5-7b/load → ["api", "models", "qwen2.5-7b", "load"]
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		jsonError(w, 404, "not_found", "not found")
		return
	}
	// 第 3 段是模型的 model_key（可能被 URL 编码过，需要解码）
	key := parts[2]
	if k, err := url.PathUnescape(key); err == nil {
		key = k
	}
	// 从数据库查这个模型是否存在
	m, e := s.models.GetByKey(r.Context(), key)
	if e != nil {
		applog.Printf("model action: model not found: key=%s", key)
		jsonError(w, 404, "not_found", "model not found")
		return
	}

	// —— 删除模型：DELETE /api/models/{key} ——
	if len(parts) == 3 && r.Method == http.MethodDelete {
		applog.Printf("model action: delete key=%s", key)
		// DeleteGatewayModel 会删数据库记录 + 本地 GGUF 文件
		if e := s.DeleteGatewayModel(r.Context(), m); e != nil {
			applog.Printf("model delete failed: key=%s err=%v", key, e)
			jsonError(w, 500, "delete_failed", e.Error())
			return
		}
		w.WriteHeader(204) // 删除成功
		return
	}

	// —— 操作模型：POST /api/models/{key}/{action} ——
	if len(parts) != 4 || r.Method != http.MethodPost {
		jsonError(w, 404, "not_found", "not found")
		return
	}
	applog.Printf("model action: key=%s action=%s", key, parts[3])
	switch parts[3] {
	case "load":
		// 加载模型到 llama.cpp 内存（加载后才能用这个模型推理）
		// setInferenceLegacyDeprecation 会在响应头里加一个"旧接口已废弃"的警告
		setInferenceLegacyDeprecation(w)
		_, e = s.InferenceLoad(r.Context(), "llamacpp", "", m.ModelKey, nil)
	case "unload":
		// 从内存卸载模型（释放显存/内存）
		setInferenceLegacyDeprecation(w)
		_, e = s.InferenceUnload(r.Context(), "llamacpp", "", m.ModelKey, nil)
	case "refresh":
		// 刷新 llama.cpp 的模型注册表（重新扫描本地有哪些模型）
		_, e = s.InferenceRefreshRegistry(r.Context(), "llamacpp")
	case "cancel":
		// 取消正在进行的下载（调用 downloadCancel 里的取消函数）
		e = s.CancelModelDownload(key)
	case "retry":
		// 重试失败的下载（重新开始下载这个模型）
		e = s.RetryModelDownload(r.Context(), m)
	default:
		jsonError(w, 404, "not_found", "unknown action")
		return
	}
	// 操作失败返回 502（上游错误）
	if e != nil {
		jsonError(w, 502, "llama_error", e.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// refreshModels 是 /api/models/refresh 接口：手动刷新 llama.cpp 的模型注册表。
// 场景：你手动把 GGUF 文件丢进模型目录后，调这个接口让 llama.cpp 重新扫描发现新模型。
func (s *Server) refreshModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	// 调用 InferenceRefreshRegistry 让 llama.cpp 重新扫描模型目录
	b, e := s.InferenceRefreshRegistry(r.Context(), "llamacpp")
	if e != nil {
		jsonError(w, 502, "llama_error", e.Error())
		return
	}
	// 直接把上游返回的 JSON 透传给客户端
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// downloadsAPI 是 /api/downloads 接口：返回当前所有下载中的/失败的/取消的任务列表。
// 管理后台的"下载管理"页面会调这个接口来展示下载进度。
// 同时覆盖两种下载来源：HuggingFace 下载（HF）和 Ollama 拉取（ollama pull）。
func (s *Server) downloadsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	// 从数据库列出所有模型
	ms, e := s.models.List(r.Context())
	if e != nil {
		jsonError(w, 500, "database_error", e.Error())
		return
	}
	// 定义返回的下载任务结构
	type item struct {
		ModelKey string `json:"model_key"`       // 模型唯一标识
		Name     string `json:"name,omitempty"`  // 展示名（Ollama 模型用）
		Kind     string `json:"kind,omitempty"`  // 下载类型："hf"（HuggingFace）或 "ollama"
		Status   string `json:"status"`          // 状态：downloading / download_failed / download_cancelled / ready
		Done     int64  `json:"done"`            // 已下载字节数
		Total    int64  `json:"total"`           // 总字节数
		Error    string `json:"error,omitempty"` // 错误信息（失败时才有）
	}
	out := make([]item, 0)

	// —— 第一部分：HuggingFace 下载任务 ——
	// 遍历所有模型，找出状态是下载中/失败/取消的
	for _, m := range ms {
		if m.Status != "downloading" && m.Status != "download_failed" && m.Status != "download_cancelled" {
			continue
		}
		// 从 Redis 里读取实时下载进度（下载器会不断更新这个）
		v, _ := s.limiter.GetDownload(r.Context(), m.ModelKey)
		d, _ := strconv.ParseInt(v["done"], 10, 64)
		t, _ := strconv.ParseInt(v["total"], 10, 64)
		st := v["status"]
		if st == "" {
			st = m.Status // Redis 里没进度就用数据库里的状态
		}
		errMsg := v["error"]
		if st == "downloading" {
			errMsg = "" // 下载中不显示错误信息
		}
		out = append(out, item{ModelKey: m.ModelKey, Kind: "hf", Status: st, Done: d, Total: t, Error: errMsg})
	}

	// —— 第二部分：Ollama 拉取任务 ——
	now := time.Now().Unix()
	// 先收集当前正在运行的 Ollama 下载任务 ID（从 downloadCancel map 里找 ollama/ 开头的）
	s.downloadMu.Lock()
	runningOllama := make(map[string]struct{}, len(s.downloadCancel))
	for id := range s.downloadCancel {
		if strings.HasPrefix(id, "ollama/") {
			runningOllama[id] = struct{}{}
		}
	}
	s.downloadMu.Unlock()
	// 遍历所有 Ollama 拉取任务 ID（从 Redis 里收集的）
	for _, jobID := range s.collectOllamaPullJobIDs() {
		v, _ := s.limiter.GetDownload(r.Context(), jobID)
		st := strings.TrimSpace(v["status"])
		if st == "" {
			// Redis 里没状态，但如果正在运行就标为 downloading
			if _, ok := runningOllama[jobID]; ok {
				st = "downloading"
			}
		}
		updatedAt, _ := strconv.ParseInt(v["updated_at"], 10, 64)
		// 判断这个任务是否应该显示在列表里（有些已完成太久的会被隐藏）
		if !ollamaPullJobListed(st, updatedAt, now) {
			// 如果是已完成（ready）且超过了显示时长，就从 Redis 里移除跟踪
			if st == "ready" && updatedAt > 0 && now-updatedAt > ollamaPullDoneVisibleSec {
				_ = s.limiter.UntrackOllamaPullJob(r.Context(), jobID)
			}
			continue
		}
		// 读取下载进度
		d, _ := strconv.ParseInt(v["done"], 10, 64)
		t, _ := strconv.ParseInt(v["total"], 10, 64)
		// 模型名：Redis 里有就用，没有就从 jobID 解析（ollama/ 前缀 + __ 替换成 : ，_ 替换成 /）
		name := v["name"]
		if name == "" {
			name = strings.TrimPrefix(jobID, "ollama/")
			name = strings.NewReplacer("__", ":", "_", "/").Replace(name)
		}
		errMsg := v["error"]
		if st == "downloading" {
			errMsg = ""
		}
		out = append(out, item{
			ModelKey: jobID,
			Name:     name,
			Kind:     "ollama",
			Status:   st,
			Done:     d,
			Total:    t,
			Error:    errMsg,
		})
	}
	// 返回所有下载任务列表
	json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

// Request logs/system/benchmark
// requestsAPI 是 /api/requests 接口：管理后台的请求日志列表。
// GET    → 按条件查询请求日志（支持按 request_id、model、status、key 筛选，默认返回最近 50 条）
// DELETE → 清空所有请求日志（危险操作，需要管理员确认）
func (s *Server) requestsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	// DELETE：清空请求日志
	if r.Method == http.MethodDelete {
		n, err := s.logs.ClearRequests(r.Context())
		if err != nil {
			jsonError(w, 500, "database_error", err.Error())
			return
		}
		// 清除请求列表的缓存（不然还会显示旧数据）
		s.invalidateRequestListCache()
		// 清空实时看板里的请求记录
		if s.live != nil {
			s.live.Clear()
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "deleted": n})
		return
	}
	// 只支持 GET 和 DELETE
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET or DELETE required")
		return
	}
	// 从查询参数里取返回条数，默认 50
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	// 查询请求日志（合并数据库里的历史记录和内存里的实时请求）
	// 支持筛选：request_id（具体某次请求）、model（模型名）、status（状态）、key（API Key 名）
	logs, e := s.queryRequestLogsMerged(r.Context(), limit, r.URL.Query().Get("request_id"), r.URL.Query().Get("model"), r.URL.Query().Get("status"), r.URL.Query().Get("key"))
	if e != nil {
		applog.Printf("requests list failed: %v", e)
		jsonError(w, 500, "database_error", e.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"data": logs})
}

// requestDetailAPI 是 /api/requests/{id} 接口：查看单次请求的详细信息。
// 返回内容包括：请求基本信息、请求体/响应体原文（如果配置了保存）、流式事件（每个 chunk 的时间）、性能分析数据。
// 对于正在进行的请求，会从实时看板里取最新数据（而不是数据库里的旧快照）。
func (s *Server) requestDetailAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	// 从 URL 路径里取出请求 ID（/api/requests/req_xxx → req_xxx）
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/requests/"), "/")
	if strings.HasSuffix(id, "/cancel") {
		s.cancelRequestAPI(w, r, strings.TrimSuffix(id, "/cancel"))
		return
	}
	if id == "" {
		jsonError(w, 400, "invalid_request", "request id required")
		return
	}
	// 从数据库查这条请求的记录
	l, e := s.logs.Get(r.Context(), id)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		applog.Printf("request detail db get failed: %v id=%s", e, id)
		jsonError(w, 500, "database_error", e.Error())
		return
	}
	// 从实时看板里查这条请求的快照（如果请求还在进行中，看板里会有最新数据）
	var snap *live.DetailSnapshot
	if s.live != nil {
		snap = s.live.SnapshotDetail(id)
	}
	// isLive = true 表示这条请求正在进行中（实时看板里有数据）
	isLive := snap != nil
	if isLive {
		if l == nil {
			// 数据库里还没记录（请求刚开始），直接用看板里的快照
			logCopy := snap.Log
			l = &logCopy
		} else {
			// 数据库和看板都有，合并起来（看板里的实时数据覆盖数据库里的旧数据）
			merged := mergeLiveIntoLog(*l, snap.Log)
			l = &merged
		}
	}
	// 都查不到，返回 404
	if l == nil {
		jsonError(w, 404, "not_found", "request not found")
		return
	}
	// 获取请求体/响应体原文和流式事件
	var payload map[string]string
	var events []store.StreamEvent
	if isLive {
		// 正在进行的请求：从看板快照里取
		payload = livePayloadFromSnapshot(*snap)
		events = snap.Events
		// 实时事件最多返回 500 条（太多会撑爆响应）
		const maxLiveEventsAPI = 500
		if len(events) > maxLiveEventsAPI {
			events = events[len(events)-maxLiveEventsAPI:]
		}
	} else {
		// 已完成的请求：从数据库里取
		if p, e := s.logs.GetPayload(r.Context(), id); e == nil {
			payload = p
		}
		events, _ = s.logs.ListStreamEvents(r.Context(), id, 2000) // 最多 2000 条流式事件
	}
	// 获取性能分析数据（各阶段耗时、token 速度等）
	analysisRaw, _ := s.logs.GetRequestAnalysis(r.Context(), id)
	// 旧记录只存了 User-Agent、client_name 为空。这里补上显示名，列表和详情才能看到客户端。
	l.ClientName = displayClientName(l.ClientName, l.UserAgent)
	// 组装完整的详情响应
	out := buildRequestDetail(l, payload, s.cfg.Logging.StorePayload, events, analysisRaw)
	// 如果是实时请求，加一个标记让前端知道"这条还在跑"
	if isLive {
		out["live"] = true
	}
	writeJSON(w, out)
}

// adminPage 是管理后台首页（/）：返回 Web UI 的 HTML 页面。
// 访问 /admin 会 301 重定向到 /；其他不存在的路径返回 404。
func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	// /admin 旧路径重定向到 /
	if r.URL.Path == "/admin" {
		http.Redirect(w, r, "/", 301)
		return
	}
	// 只响应根路径 /，其他路径 404
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// 读取管理后台的 HTML 文件内容
	data, e := s.adminPageHTML()
	if e != nil {
		applog.Printf("admin page: read web/index.html failed: %v", e)
		http.Error(w, "admin ui not available", 500)
		return
	}
	// 返回 HTML，设置不缓存（每次都拿最新的）
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Write(data)
}

func noStoreStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// webStaticDir 返回管理后台静态资源（CSS/JS/图片）的目录路径。
func (s *Server) webStaticDir() string {
	return filepath.Join(s.webRoot(), "static")
}

// webRoot 返回 Web 资源的根目录名（固定为 "web"）。
func (s *Server) webRoot() string {
	return "web"
}

// adminPageHTML 读取并缓存管理后台的 index.html 文件内容。
// 用了缓存模式：第一次读文件，之后直接返回缓存的内容（读失败也缓存错误，避免反复尝试读一个不存在的文件）。
func (s *Server) adminPageHTML() ([]byte, error) {
	// 如果已经缓存了 HTML 内容，直接返回
	if len(s.adminHTML) > 0 {
		return s.adminHTML, nil
	}
	// 如果之前读失败过，直接返回缓存的错误
	if s.adminHTMLErr != nil {
		return nil, s.adminHTMLErr
	}
	// 第一次：从磁盘读 web/index.html
	data, e := os.ReadFile("web/index.html")
	if e != nil {
		s.adminHTMLErr = e // 缓存错误
		return nil, e
	}
	s.adminHTML = data // 缓存内容
	return data, nil
}

// withRequestID 是 chat 请求链最外层：每个 HTTP 请求分配唯一 ID，后续 proxyV1 日志与 DB 都用它关联。
// 这个中间件在所有请求的最外层执行：生成一个唯一 ID，放到请求上下文里，
// 同时写到响应头 X-Request-ID 里（客户端可以拿到这个 ID 来排查问题）。
func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 生成唯一请求 ID（格式：req_时间戳_随机字符串）
		id := newID()
		// 把 ID 放进请求上下文（后续处理函数可以通过 requestID() 取出来）
		ctx := context.WithValue(r.Context(), ctxKey("request_id"), id)
		// 把 ID 写到响应头里，客户端可以看到
		w.Header().Set("X-Request-ID", id)
		// 继续执行下一个处理器（真正的路由逻辑），用带了 ID 的新请求
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ctxKey 是上下文键的类型（用自定义类型避免和其他包的键冲突）。
type ctxKey string

// requestID 从请求上下文里取出本次请求的唯一 ID。
// 如果上下文里没有（比如不是通过 withRequestID 进来的），返回空字符串。
func requestID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey("request_id")).(string)
	return v
}

// newID 生成一个唯一的请求 ID，格式：req_纳秒时间戳_10位随机字符串。
// 比如：req_1759286400123456789_a1b2c3d4e5
func newID() string { return fmt.Sprintf("req_%d_%s", time.Now().UnixNano(), randString(10)) }

// randString 生成一个指定长度的随机十六进制字符串（0-9, a-f）。
// 优先用 crypto/rand（加密安全的随机数），失败了就用时间戳兜底。
func randString(n int) string {
	b := make([]byte, n)
	// 用加密安全的随机数生成器读 n 个随机字节
	if _, err := rand.Read(b); err == nil {
		// 转成十六进制字符串（每个字节变 2 个字符，所以可能比 n 长，截断到 n）
		out := hex.EncodeToString(b)
		if len(out) > n {
			return out[:n]
		}
		return out
	}
	// 随机数生成器失败的兜底方案：用纳秒时间戳转成字符串，取前 n 位
	return fmt.Sprintf("%0*d", n, time.Now().UnixNano())[:n]
}

// parseTrustedProxyRanges 解析可信代理 IP/CIDR。无效项忽略并记录日志，默认不信任任何转发头。
func parseTrustedProxyRanges(values []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			out = append(out, prefix.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			addr = addr.Unmap()
			bits := 128
			if addr.Is4() {
				bits = 32
			}
			out = append(out, netip.PrefixFrom(addr, bits))
			continue
		}
		applog.Printf("invalid trusted proxy entry ignored: %q", value)
	}
	return out
}

func parseRemoteAddrIP(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(remoteAddr), "[]")
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP 只在 TCP 直连来源属于显式配置的可信代理时，才读取转发头。
// X-Forwarded-For 从右向左跳过可信代理地址，避免客户端伪造一个更靠左的来源 IP。
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, ok := parseRemoteAddrIP(r.RemoteAddr)
	if !ok {
		return strings.TrimSpace(r.RemoteAddr)
	}
	if !isTrustedProxy(peer, trusted) {
		return peer.String()
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				// 链路格式异常时停止向左搜索，避免把更左侧的可伪造值当作客户端地址。
				break
			}
			addr = addr.Unmap()
			if isTrustedProxy(addr, trusted) {
				continue
			}
			return addr.String()
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		if addr, err := netip.ParseAddr(realIP); err == nil {
			return addr.Unmap().String()
		}
	}
	return peer.String()
}

// writeJSON 把任意对象序列化成 JSON 并写给客户端（设置正确的 Content-Type）。
// 是管理 API 常用的辅助函数，比每次手动 json.Marshal + w.Write 方便。
func writeJSON(w http.ResponseWriter, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		applog.Printf("json marshal failed: %v", err)
		jsonError(w, 500, "internal_error", "response encode failed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if _, err := w.Write(b); err != nil {
		applog.Printf("json write failed: %v", err)
	}
}

// jsonError 给客户端返回一个标准格式的 JSON 错误响应。
// 格式：{"error": {"type": "错误代码", "message": "错误描述"}}
// 同时会先打一条错误日志（方便排查）。
func jsonError(w http.ResponseWriter, status int, code, msg string) {
	applog.Printf("api error: status=%d code=%s msg=%q", status, code, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]interface{}{"type": code, "message": msg}})
}

// errString 把 error 转成字符串，nil 就返回 "ok"（用于健康检查里展示状态）。
func errString(e error) string {
	if e == nil {
		return "ok"
	}
	return e.Error()
}

// parseInt64 把字符串转成 int64，解析失败就返回 0（忽略错误，用于从 Redis 读数字字段）。
func parseInt64(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }

// ptrVal 把 *int64 指针解引用成 int64 值，指针为 nil 就返回 0。
// 用于日志打印：很多 token 数字是指针（可能为空），直接打印会显示 <nil>，用这个函数统一转成 0。
func ptrVal(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
