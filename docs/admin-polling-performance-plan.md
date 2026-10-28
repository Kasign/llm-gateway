# 管理台轮询性能基准方案

> 当前状态：方案已定义，尚未在生产环境采样。不要将隔离 CI 的短时通过结果当作轮询性能结论。

## 目标

量化首页/调用记录/Provider 状态等管理页轮询对 Gateway、MariaDB、Redis 与推理热路径的影响，确认刷新频率是否需要调整。不得仅凭一次采样或单个 Runner 的绝对耗时设硬阈值。

## 对照组

1. A：页面关闭或停止轮询，仅保持 Gateway 空闲基线。
2. B：按当前默认设置打开页面，至少覆盖首页、调用记录和 Provider 页面。
3. C：使用受控测试页面/配置将轮询频率提高到默认的约 2 倍；只在隔离环境执行，不对生产环境制造高频压力。
4. 每组重复至少 3 次，保持 Go 版本、容器资源限制、数据库数据量、Provider 配置和测试时长一致；记录版本与环境。

## 采样流程

- 每组先预热 2 分钟，再采样 10 分钟；测试窗口内同时运行固定、低成本的 Mock 推理请求，以观察管理面板是否影响推理热路径。
- 记录管理 API 请求数/分钟、各端点 P50/P95/P99、HTTP 5xx、超时、Gateway CPU/RSS、MariaDB CPU/查询量/慢查询、Redis 命令量与缓存命中率（若该指标可用）。
- 记录固定推理请求的 TTFT 与总耗时 P50/P95/P99、吞吐及错误率；不要使用真实付费模型做高频基准。
- 对数据库查询量按端点分类；若当前观测工具无法按端点归因，明确记录为数据缺口，不要猜测缓存命中率。
- 导出只包含聚合数据的 CSV/JSON；剔除 Key、请求正文、Authorization Header、用户输入和原始错误正文。

## 比较与决策

- 比较 B-A 与 C-B 的变化幅度和跨重复样本的波动范围；同时检查推理 TTFT/P95 是否发生一致性回退。
- 先定位主要贡献端点和查询，再决定是否调整刷新周期、合并请求或加短期缓存；不要为未证实的热点增加架构抽象。
- 建议把相对变化和样本离散程度作为首要证据。绝对阈值须基于目标服务器的容量预算与实际基线制定。

## 报告模板

| 指标 | A：无轮询 | B：默认轮询 | C：高频轮询 | 说明 |
|---|---:|---:|---:|---|
| 管理 API 请求/分钟 | 未测 | 未测 | 未测 | |
| 管理 API P95 / P99 | 未测 | 未测 | 未测 | 分端点记录 |
| MariaDB 查询/分钟 | 未测 | 未测 | 未测 | 尽量按端点归因 |
| Gateway CPU / RSS | 未测 | 未测 | 未测 | |
| 推理 TTFT P95 | 未测 | 未测 | 未测 | 固定 Mock 请求 |
| 推理总耗时 P95 | 未测 | 未测 | 未测 | 固定 Mock 请求 |
| 错误率 / 超时 | 未测 | 未测 | 未测 | |

## 当前可复用的基准

- `go test ./internal/server -run '^$' -bench 'Benchmark(InferenceObserverOnChunkParallel|BuildRequestAnalysis|ResolveInferenceAgentRoute)' -benchmem`：用于定位 Go 内部观察器/DTO/路由路径成本，不代表浏览器轮询或 MariaDB 负载。
- `go test ./internal/provider -run '^$' -bench BenchmarkOpenAICompatibleProviderForwardJSON -benchmem`：只测 loopback Mock Provider 转发开销，不代表真实上游延迟。
- 管理 API 的轮询频率与请求量必须通过真实页面或受控脚本单独采集；当前 Go benchmark 不能替代该测量。
