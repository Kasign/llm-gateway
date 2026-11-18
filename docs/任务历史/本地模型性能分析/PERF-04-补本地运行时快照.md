# PERF-04：补本地运行时快照

> 状态：进行中
> 更新日期：2026-11-18
> 分支：dev
> 前置任务：PERF-03 Provider 队列等待时间

## 1. 目标

在请求性能分析中补充“请求发生时的本地运行环境快照”，用于解释冷/热启动、模型驻留和资源压力。优先复用现有运行时采集能力，不新增上游探测请求，不把管理台轮询数据冒充请求时刻数据。

## 2. 已确认的现有能力

- `llama.RuntimeSnapshot` 已提供后端、活动模型、已加载模型、磁盘大小、RAM/VRAM 等运行时信息。
- `Server.cpuUsagePercent` 已提供基于 `/proc/stat` 的主机 CPU 短期采样。
- `upstreamRuntimeSnapshot` 已有短期缓存，避免管理台重复访问上游。
- Ollama / llama.cpp 的运行时快照已有单元测试。
- Provider Queue Wait 已进入 RequestAnalysis；本任务不重复实现性能计时。

## 3. 需要解决的语义

请求性能快照必须明确：

1. **采样时点**：请求开始附近、请求结束附近，还是单一快照。
2. **数据来源**：本机进程资源、上游模型状态、还是 Gateway 管理缓存。
3. **不可用语义**：上游不可达、字段缺失、非本地 Provider 时必须为 unknown/partial，不填 0。
4. **开销边界**：不能为了一个请求分析触发新的 `/api/tags`、`/api/ps` 或 llama.cpp registry 扫描。
5. **一致性**：模型 ID、Backend、RAM/VRAM 和 CPU 必须来自同一次明确标记的采样，不把不同时间的值拼成“瞬时快照”。

## 4. 第一阶段方案

先增加内部不可变快照结构，复用已有采集结果：

- Backend
- ActiveModel
- LoadedModelCount
- LoadedRAMBytes
- LoadedVRAMBytes
- HostCPUPercent
- SnapshotStatus
- SnapshotSource

请求分析只保存可公开、可比较的摘要；不保存 URL、凭据、Prompt、Response 或完整上游响应。

对于远程 Provider：

- 不伪造本地 CPU/RAM/VRAM。
- Backend 和模型身份可以记录。
- 本地资源字段保持 unknown。

对于上游快照失败：

- 请求本身继续正常完成。
- RequestAnalysis 标记 snapshot status 为 unavailable/partial。
- 不因为观测失败改变请求状态。

## 5. 当前调查结论

现有 `RuntimeSnapshot` 足以作为模型驻留事实来源，但直接在每个请求开始/结束调用 `InferenceList` 会增加上游请求和延迟，因此不能直接复用管理 API 的完整路径。

下一步应增加**非阻塞、低开销的本地运行时采样入口**，并明确其缓存/采样时间；请求只读取最近一次有效快照。

CPU 采样不能在请求路径调用当前的首次采样逻辑，因为首次调用包含约 200ms 的采样 sleep，会污染请求性能数据。

## 6. 测试要求

至少覆盖：

- 本地快照完整字段；
- 上游不可达；
- 远程 Provider；
- CPU 首次采样不阻塞请求；
- 快照过期；
- 并发读取；
- 不新增上游请求；
- 旧 RequestAnalysis JSON 兼容。

## 7. 不做的事情

- 不修改普通 `/v1` 认证、解析、Provider 路由、流式写回和取消时序。
- 不为每个请求新增上游 HTTP 探测。
- 不把管理台缓存直接称为请求时刻精确快照。
- 不用 0 表示未知。
- 不把 Gateway 容器 CPU 与模型主机 CPU 混为一谈。

## 8. 验收标准

- 运行时快照有明确来源和采样时间语义；
- 请求分析能够区分 complete / partial / unavailable；
- 采样不会增加请求关键路径的固定等待；
- 不影响普通 /v1 主流程；
- Go CI、竞态测试、Browser Smoke 通过；
- 完成后归档 PERF-04，再进入 PERF-05。
