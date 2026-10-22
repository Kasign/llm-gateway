# 管理台全局状态（AdminState）

单容器部署下，Gateway 进程内维护 **`adminStateSnap`**，统一管理：

| 域 | 内容 |
|----|------|
| `upstream` | 当前代理后端（DB + 内存 `effectiveUpstream`） |
| `reachability` | llamacpp / ollama：`probeBackendReachable` + 探测时间 |
| `inference` | 各后端：`loaded_count`、`active_model`（可达时才打 memory 快照） |
| `dependencies` | 当前上游摘要、Redis ping |
| `gateway_counts` | GGUF 条数、downloading 条数 |
| `probes` | 仅用户 `GET /api/admin/backend?refresh=1` 时附带 |

TTL：**10s**。`invalidateAdminRuntimeCaches()` 会清空 AdminState 与 runtime 快照缓存。

## HTTP 投影

| 接口 | 用途 |
|------|------|
| `GET /api/admin/bootstrap` | **首页进入时一次**：upstream + 可达 + 推理摘要 + host + 计数 |
| `GET /api/admin/bootstrap/lite` | **首页轮询**：host + 可达 + 推理摘要 + 依赖（轻量） |
| `GET /api/admin/backend` | 配置 + 可选探测明细（读 AdminState） |
| `GET /api/models`、`/api/admin/runtime`、`/api/admin/ollama` | 模型 Tab **按需**，不在 bootstrap 内 |

## 前端

- `web/js/features/admin-store.js`：`syncDashboard(mode)` 统一请求 bootstrap / lite。
- 进入 Dashboard Tab、`refreshAll`、轮询 `dashboardPoll` 均只走 `loadSystem({ force? })`（唯一入口，避免叠调用）。
- 切换 Tab 进 Dashboard：`loadSystem()` 后 `dashboardPoll.start({ skipImmediate: true })`，避免与轮询各打一次 lite。
- 切换上游 `saveBackend`：`adminStore.reset()` + `loadSystem({ force: 'bootstrap' })`，表单用 `applyBackendFormFromStore()`，不再并行 `GET /api/admin/backend`。
- 模型加载/卸载：`modelHub.refresh()`；若 Dashboard 打开则 `loadSystem()` 一次。
- 探测连通性：`GET /api/admin/backend?refresh=1` 更新探针 UI 后，Dashboard 上仅 `loadSystem({ force: 'lite' })` 一次。
- `loadMmBackendPills` 优先读 `adminStore.upstream`，减少重复 `GET /api/admin/backend`。

## 写入源（唯一）

- 可达：`probeBackendReachable`（与切换上游校验一致）
- 在内存数量：`upstreamRuntimeSnapshotInMemory`
- 变更后：`InferenceLoad/Unload`、`SwitchProxyUpstream` 等调用 `invalidateAdminRuntimeCaches()`
