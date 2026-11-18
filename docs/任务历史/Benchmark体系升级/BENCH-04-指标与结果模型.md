# BENCH-04：指标与结果模型

> 状态：已完成
> 仓库：Kasign/llm-gateway
> 分支：dev
> 代码基线：b4d71c5bb0e52f273affaa5e428cd9ebea529663
> 归档日期：2026-11-18

## 目标

建立可解释的 Benchmark 指标口径，区分 Task 总耗时、模型加载/就绪、Pipeline Run 墙钟耗时、TTFT 与真实生成速度；建立测试条件快照及可比性边界。不得改变普通 `/v1` 请求主流程。

## 完成内容

1. Task 总耗时继续使用原 `latency_ms` 语义；Run 独立记录 `model_load_ms`、`inference_latency_ms` 和 TTFT 状态。
2. TTFT 从 Pipeline Run 开始计时，不包含 Pipeline 前的模型加载/就绪等待。
3. Run 指标使用 nullable 数值和 `measured / not_applicable / unknown` 状态；缺失值不伪装成 0。
4. Task/Run 指标在 MariaDB 持久化、历史查询和重启恢复中复用同一字段；Request Analysis 继续复用原有审计分析，不复制第二套性能分析。
5. 条件快照只保存 Prompt SHA-256，不保存 Prompt 正文；模型身份、Provider/Backend、生成参数、Gateway 构建与运行配置参与指纹。
6. 内部可变别名 `model_key` 从指纹输入排除；VCS 工作区状态未知时明确标记缺失。
7. 历史任务缺少条件元数据时返回 `unknown`；部分条件只能说明“条件部分一致”，不能宣称严格可比较。
8. 结果页展示条件完整性、缺失维度和指纹摘要；不自行计算性能指标。

## 关键提交

- `cb705176`、`6b375a5`、`91e2504`：Run 指标、状态和 TTFT 边界。
- `0882fa65`、`9aa08aec`、`549469d9`：条件快照与历史兼容。
- `0109aa8c`：冷热加载状态边界。
- `aa701579`：条件指纹稳定性修正。
- `b4d71c5`：结果页条件可比性展示。
- `1aa3a08`：本阶段验收记录与规划更新。

## 最终验收

- Go CI `44217405866`（代码基线 `b4d71c5`）：success。
- Browser Smoke `44217405938`（代码基线 `b4d71c5`）：success。
- Go CI `44218098681`（文档基线 `1aa3a08`）：success。
- Browser Smoke 在真实 Gateway + Chromium/Playwright 流程通过。
- 复核 Task/Run 数据模型、MariaDB 更新/读取、历史恢复、OpenAPI DTO 与 UI 字段一致。
- 普通 `/v1/chat/completions` Handler、认证、请求解析、Pipeline 阶段和取消时序未被本阶段改动。

## 未包含

- 尚未实现跨任务比较页面/比较 API；本阶段只提供条件快照及其可信状态。
- Backend 版本、硬件型号、物理内存等没有可靠通用来源，保持缺失，不额外探测推理热路径。
- 工具调用性能测试属于 BENCH-05。
