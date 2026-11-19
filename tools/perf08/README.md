# PERF-08 llama.cpp patch 工作区

本目录用于固定上游版本的独立 profiling patch 构建与验证，不属于 Gateway 生产推理链路。

## 固定基线

- 上游仓库：`ggml-org/llama.cpp`
- 上游 commit：`aa75ff3239f9cc675b081697deda9e1b112345c6`
- 首轮构建：CPU-only，构建 `llama-server`，不下载或加载 GGUF，不启动模型进程。
- Gateway Dockerfile / docker-compose 不负责构建或运行此组件。

## 当前状态

当前 workflow `.github/workflows/perf08-llama-cpu.yml` 只验证固定上游基线能在干净的 Ubuntu runner 上完成 CPU-only Server 构建。它**不是** profiling patch 验收，也不覆盖 callback 行为、scope 归因、buffer overflow 或运行期开销。

Profiling patch 尚未进入此仓库。补丁落地后，必须在同一 workflow 中增加以下顺序：

1. 验证 patch 文件的 SHA 与上游 commit pin。
2. 对干净 checkout 执行 `git apply --check`，再执行 `git apply`。
3. 在相同 CPU-only 配置下构建 `llama-server`。
4. 运行 callback 兼容性与 scope 单元测试。
5. 上传构建日志和测试结果；不上传模型、tensor 内容或请求数据。

在上述步骤加入并通过之前，不得将该 workflow 标记为 PERF-08 patch 已验证。
