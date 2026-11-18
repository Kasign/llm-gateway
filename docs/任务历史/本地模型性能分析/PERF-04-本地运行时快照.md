# 当前任务：PERF-04 本地运行时快照

> 状态：已完成
> 更新日期：2026-11-18
> 仓库：Kasign/llm-gateway
> 分支：dev
> 验收代码 SHA：659988a40cf7489a741bde061444b573d0bd8978
> 前序任务：PERF-03 Provider 队列等待时间
> 总体方案：[本地模型全链路性能分析方案](本地模型全链路性能分析方案.md)
> 任务规划权威来源：[整体任务规划](整体任务规划.md)

## 1. 目标

建立本地 Provider 运行时快照，并作为 RequestAnalysis 的独立证据层。快照用于提供请求分析收尾时最近的运行环境背景，不得宣称它一定发生在本次请求执行区间内，更不能伪装成请求瞬时硬件状态。

## 2. 已实现能力

- CPU 与本地模型运行时分别使用独立后台采样器，默认 5 秒采样一次。
- Server Shutdown 等待两个采样器退出；上游运行时采样使用独立 Context。
- 仅 Ollama / llama.cpp 本地 Provider 采集；远程 Provider 不伪造本机模型资源。
- 复用已有运行时缓存；Ollama 优先使用 memory-only 路径。
- Snapshot 保存 source/status/sampled_at；请求侧读取防御性副本。
- 超过 15 秒的模型快照或 CPU 快照均视为不可用；CPU 缺失时状态降级为 partial。
- RequestAnalysis 新增 runtime_snapshot；OpenAPI 与请求详情页已接入。
- 模型内存汇总缺失时从所有已加载模型明细回退；未加载模型不参与汇总。

## 3. 最近修复与测试

- 修复 sampler 类型缺失和 OpenAPI Schema 缺失。
- 修复 CPU 快照读取方法重复定义。
- 修复 CPU 快照缺少独立 freshness 判断。
- 修复只提供 Loaded 明细时 RAM/VRAM 汇总为零的问题。
- 增加多模型内存汇总回退测试。
- 增加运行时快照写入/读取防御性复制测试。
- `8a2856bba01a8035eb49c1324d9ba5c68e10cd9c`：Go CI `44255981539` 与 Browser Smoke `44255981425` 均成功。
- 修正 `loaded_model_count` 仅统计 Loaded=true 条目，最终验收 SHA 为 `659988a40cf7489a741bde061444b573d0bd8978`。
- 最终 Go CI `44256340752`：5/5 Job 成功，含竞态、静态检查、构建、故障注入、稳态负载与审计持久化。
- 最终 Browser Smoke `44256340765`：桌面、窄屏及核心交互验收成功。

## 4. 重要语义边界

当前请求收尾阶段读取最近快照。由于目前只保留最新快照，它可能是在请求期间采集，也可能是在请求结束前刚采集；不能保证一定与本次请求时间区间重叠。UI 和 OpenAPI 已明确此限制。

后续 PERF-05 统一时间线时，再评估是否需要保留有限快照环形缓冲并按请求起止时间选择重叠样本；不得把请求结束后的样本错误关联为请求期间证据。

## 5. 最终 Review 结论

- CPU 与本地模型运行时分别由独立后台采样器采集，互不阻塞。
- Server Shutdown 取消并等待两个采样器退出，受调用方 Context 控制。
- 模型快照和 CPU 样本均有 15 秒 freshness 限制；过期模型快照 unavailable，CPU 缺失时降级 partial。
- 快照写入/读取均复制 Snapshot 及 Loaded/Catalog 切片，避免外部修改内部缓存。
- 仅本地 Ollama / llama.cpp Provider 采集；远程 Provider 不伪造本机资源，Ollama 使用 memory-only 路径。
- 聚合内存缺失时回退累加所有已加载条目；未加载条目不计入内存和 loaded_model_count。
- RequestAnalysis 新字段可选，旧 JSON 继续兼容；缺失字段不回填为零。
- OpenAPI、详情 UI 与服务端字段一致，明确快照为请求分析收尾时最近样本，不保证与请求区间重叠。
- 未改变普通 /v1 认证、Pipeline 顺序、取消或流式写回。

## 6. 验收证据

| 验收项 | 证据 | 结果 |
|---|---|---|
| Go CI | `44256340752`，SHA `659988a40cf7489a741bde061444b573d0bd8978` | 5/5 Job 成功 |
| Browser Smoke | `44256340765`，同一 SHA | 成功 |
| 故障注入 | Go CI 故障注入 Job | 成功 |
| 稳态负载/审计 | Go CI 稳态负载与审计持久化 Job | 成功 |
| OpenAPI/前端契约 | Go CI 配置、API 契约与前端 Job | 成功 |
| 真实部署/NUC 基线 | 留待 TOOL-09 / PERF-12 | 未执行，不计为通过 |

## 7. 不做

- 不在请求热路径主动访问 llama.cpp /metrics。
- 不保存 Prompt/Response/Reasoning 正文。
- 不修改普通 /v1 认证、解析、取消和流式写回。
- 不进入 GGML deep profiling；该工作属于 PERF-08。
- 不凭单份 RuntimeSnapshot 直接判定性能瓶颈。

## 8. 验收标准

- Go 单元/集成/竞态/静态检查/构建通过。
- 故障注入和稳态负载通过。
- Browser Smoke 通过。
- OpenAPI contract gate 通过。
- RuntimeSnapshot stale/partial/copy/multi-model 测试通过。
- RequestAnalysis 历史数据兼容。
- 不新增普通请求上游探测。


## 9. 归档说明

PERF-04 已按自动化验收范围完成。真实部署验收不属于本任务 CI 证据；TOOL-09 真实部署回归与 PERF-12 NUC 基线仍按总体规划执行，不得将本次 CI 结果视为真实环境验收。
