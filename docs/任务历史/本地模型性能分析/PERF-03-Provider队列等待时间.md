# PERF-03：Provider 队列等待时间

> 状态：设计确认，待实现
> 更新日期：2026-11-18
> 分支：dev
> 前置任务：PERF-02 指标来源与 timings 完整性

## 1. 目标

测量请求在 Provider 并发许可器中的真实等待时间，区分“未排队直接获得许可”“进入 FIFO 队列后获得许可”“队列满/关闭队列”“等待超时”“等待期间取消”。

该指标用于性能分析和诊断，不改变 Provider 并发、FIFO、超时或取消行为。

## 2. 时间语义

### 2.1 queue_wait_ms

仅表示 Provider admission 等待：

- 起点：请求实际进入 ConcurrencyLimiter.Acquire 的等待状态。
- 终点：该等待者获得并发许可的实际时刻。
- 单位：毫秒。
- 直接获得许可：0。
- 未进入等待队列：不记录 queue_wait_ms；不要用整个 Provider 请求耗时代替。

### 2.2 timeout / cancellation

请求未获得许可时：

- provider_queue_timeout：记录等待开始至超时的实际等待时间，并标记 status=timeout。
- 请求 Context 取消：记录等待开始至取消的实际等待时间，并标记 status=cancelled。
- 队列关闭、队列满：没有等待阶段，不记录 queue_wait_ms。

## 3. 数据来源

优先使用 ConcurrencyLimiter.Acquire 内部已有生命周期：

- waiter 入队时记录单调时钟起点。
- grantedAt 已存在，用于确认许可发放时刻。
- 不使用 wall-clock 差值作为业务计时。
- 对外持久化时只保存耗时结果，不保存内部时间戳。

当前 Provider 已经在 Forward 中先完成 admission，再发起上游 HTTP 请求，因此 queue_wait_ms 与 upstream 网络/模型耗时天然分离。

## 4. 与现有字段的关系

不得把以下时间混入 queue_wait_ms：

- HTTP Client 建连/TLS；
- 上游首字节等待；
- 上游模型生成；
- SSE 读取；
- Gateway 流式写回；
- 整个 Provider Forward 耗时。

最终性能详情应能区分：

Gateway 请求 → Provider 排队 → 上游请求 → 首字节 → 生成/流式 → 完成

其中不存在可靠观测依据的阶段保持 unknown，而不是估算。

## 5. 实现策略

优先增加 Provider admission 的内部测量结果，不改变 Acquire(ctx, timeout) error 的外部行为。

建议使用独立的内部结果结构承载：

- Waited：是否实际进入等待队列；
- WaitMS：等待耗时；
- Status：none / granted / timeout / cancelled。

InferenceCall 继续保持现有回调兼容；如需把结果交给 Server 分析层，应增加可选、非业务语义的 measurement callback，而不是改变普通请求流程。

## 6. 测试

至少覆盖：

1. 并发许可立即可用 → queue_wait_ms=0。
2. FIFO 单请求等待 → 等待时间大于 0。
3. 多请求 FIFO → 顺序不变，每个等待时间独立。
4. 队列满 → 不产生等待测量。
5. 队列关闭 → 不产生等待测量。
6. 等待超时 → 记录 timeout，不发生上游 HTTP dispatch。
7. 等待期间取消 → 记录 cancelled，不发生上游 HTTP dispatch。
8. 许可发放与 Context 取消竞争 → 不重复计算、不泄漏许可。
9. 并发/竞态测试 → 不改变 limiter 既有行为。

## 7. 不做的事情

- 不新增一次上游请求。
- 不修改普通 /v1 请求认证、解析、流式写回或取消主流程。
- 不把 queue_wait_ms 当作模型推理速度。
- 不把 Provider 整体耗时当作队列等待。
- 不为了得到非零数据而人为 sleep。
- 不在前端自行推算等待时间。

## 8. 验收标准

只有同时满足以下条件才算完成：

- limiter 原有测试全部通过；
- 新增 FIFO/timeout/cancel/zero-wait 测试通过；
- race test 通过；
- 请求详情能够明确显示 queue wait 的来源和状态；
- OpenAPI 与持久化结构同步；
- Browser Smoke 通过；
- 不影响普通 /v1 请求行为。
