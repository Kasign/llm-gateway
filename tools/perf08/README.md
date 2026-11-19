# PERF-08 本地模型深度采集工作区

本目录放置 Gateway 侧的独立采集辅助工具和后续可选的上游 patch 验证材料，不属于 Gateway 生产推理热路径。

## 当前执行路线

优先复用 llama.cpp 原生 timings / metrics。原生请求级 timing 无法解释 CPU 调度、硬件计数或算子热点时，先使用宿主机 Linux `perf stat` 获取短时 process/window 证据；不立即修改 llama.cpp C++ 源码。

### 宿主机进程采样

脚本：`tools/perf08/host-perf-stat.sh`。用户当前容器名为 `Llama-cpp` 时，可直接传该容器名自动解析唯一模型 child PID。

```bash
tools/perf08/host-perf-stat.sh <host-pid|container-name> [duration-seconds] [output-directory]
```

在用户当前容器名仍为 `Llama-cpp` 的主机上，可直接执行：

```bash
bash tools/perf08/host-perf-stat.sh Llama-cpp 10
```

约束：

- 可传宿主机 PID，也可传容器名。传容器名时，脚本通过 `docker top` 查找父子进程均为 `llama-server` 的 Router model child；必须恰好找到一个，否则拒绝猜测。传 PID 时同样校验父进程 comm 为 `llama-server`，拒绝误采 Router 主进程或容器内 PID。
- 默认 10 秒，最多 10 秒；只对指定 PID 使用 `perf stat -p`。
- 不使用全系统 `-a`，不执行 `perf record`，不生成 `perf.data`。
- 基础采集 task-clock、context-switches、cpu-migrations、page-faults。只有当前为 root、持有 CAP_PERFMON，或 `perf_event_paranoid <= 0` 时，才在检测到 Intel Hybrid PMU 后加入 cpu_core / cpu_atom 的 cycles 和 instructions；否则仅采软件事件，并在 metadata 标记 `not_sampled_permissions`。
- 输出 `metadata.txt` 和 `perf-stat.csv`，目录权限 0700、文件权限 0600。
- 元数据绑定 PID、进程启动 ticks 和 binary build-id，防止 PID 复用导致错误归因。
- 输出明确为 `scope=process_window`、`request_correlation=none`，不能写入请求级 RequestAnalysis 或作为 request-native profile。
- 不自动安装 perf、不提权、不修改 `perf_event_paranoid`。权限失败应保留诊断结果并停止，不扩大权限。
- 原始输出不进入 Gateway 数据库或长期备份；分析完成后由操作者按需删除。

首轮不启用 DWARF callchain、寄存器、内存地址、全系统采样或 perf.data。硬件事件若显示 not-counted / not-supported / multiplexed，必须在结论中标记不可比较，不能用软件事件替代后仍宣称硬件计数有效。

## 固定上游构建基线

- 上游仓库：`ggml-org/llama.cpp`
- 上游 commit：`aa75ff3239f9cc675b081697deda9e1b112345c6`
- Workflow：`.github/workflows/perf08-llama-cpu.yml`
- Run `44507383958` 已通过：Ubuntu 24.04 CPU-only 配置下成功构建 `llama-server`，185/185 Ninja 步骤完成。
- 不下载 GGUF、不启动模型、不触碰用户生产容器。

该 workflow 结果只代表 `upstream_baseline_pass`，不是 profiling patch 验收。

## 何时才修改 llama.cpp

只有宿主机 process/window 级外部采样仍无法解释目标瓶颈，且确认确实需要 GGML 节点级归因时，才重新评估最小 C++ patch。若进入该阶段：

1. 固定上游 commit 和 patch SHA。
2. 对干净 checkout 执行 `git apply --check`，再执行 `git apply`。
3. 在相同 CPU-only 配置下构建 `llama-server`。
4. 验证原 callback 委托、bool 返回语义、scope 生命周期、Graph Reuse/reset 和 overflow 不改变推理结果。
5. 不记录 tensor 内容、tensor name、token ID、Prompt/Response。
6. 不把 patch 混入 Gateway Dockerfile，也不触碰生产容器。

在用户明确确认 patch 范围之前，不修改 llama.cpp 上游 C++ 源码。


## 脚本自动化验证

`.github/workflows/perf08-host-script.yml`（Run `44511368692`，success）对脚本执行 `bash -n`，并使用 mock Docker/perf 与临时 `llama-server` 父子进程验证：

- 容器名能唯一解析 Router 的模型 child PID。
- 不会把 Router 父进程作为采样目标。
- PID / PPID / executable 绑定正确。
- metadata 与 CSV 生成，目录和文件权限符合 0700/0600。
- 测试不启动真实 llama.cpp 服务、不运行真实 perf、不接触模型文件。
