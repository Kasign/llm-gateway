# 1Panel 部署

配置分两层，字段含义见 [配置说明.md](./配置说明.md)。这篇只写第一次把服务跑起来、以及之后怎么换上游。

## 第一次部署

1. 将项目放到宿主机目录，例如 `/server-whl/docker/llm-gateway-v11/`。
2. 进入项目根目录：

```bash
cd /server-whl/docker/llm-gateway-v11
cp .env.example .env
cp config.example.yaml config.yaml
cp providers.env.example providers.env
```

3. 编辑 `.env`。等号后面不要加空格。必填：

- `EXISTING_DOCKER_NETWORK`：llama.cpp、Ollama、Redis、MariaDB 所在的 Docker network。
- `LLAMA_CPP_BASE_URL`：llama.cpp 地址，例如 `http://Llama-cpp:8080`。主机名必须是容器名。
- `OLLAMA_BASE_URL`：本机 Ollama 地址，例如 `http://ollama:11434`。必须和 llama.cpp 不同。
- `MODEL_HOST_DIR`：宿主机上的 GGUF 目录。Gateway 下载的文件也写到这里，容器内是 `/data/models`。
- `MARIADB_ROOT_PASSWORD`、`LLM_GATEWAY_DB_PASSWORD`、`REDIS_ROOT_PASSWORD`。
- `LLM_GATEWAY_ADMIN_API_KEY`。
- Web 登录仅使用密码，无需用户名。主 `.env` 包含初始密码哈希与默认密码保护开关，按 [控制台与密码说明.md](./控制台与密码说明.md) 进行迁移和修改；无需再创建 `admin-auth.env`。

`LLM_BACKEND` 只决定数据库还没有上游记录时，第一次把 `/v1` 打到哪边。默认 `llamacpp`。

3.5. 只有需要增加 DeepSeek/OpenAI 以外的自定义远程 Provider 时，才编辑 `providers.env`。它只传递 `LLM_GATEWAY_PROVIDER_<ID>_*` 字段，与主 `.env` 分开，不能把主 `.env` 整份作为容器环境文件。每个新 ID 必须在所有 YAML/环境配置中唯一。示例文件默认只有注释，不会创建 Provider。

4. 编辑 `config.yaml` 里和现有容器名有关的两项，不要在这里再写一份上游地址：

- `database.host`：MariaDB 容器名。当前环境是 `Mariadb`。
- `redis.addr`：Redis 地址。当前环境是 `Redis:6379`。

5. 启动：

```bash
sudo docker compose up -d --build
```

6. 检查：

```bash
sudo docker compose ps
sudo docker compose logs --tail=100 gateway
curl http://127.0.0.1:8088/health
curl http://127.0.0.1:8088/api/admin/bootstrap -H "X-Admin-Key: YOUR_KEY"
sudo docker exec llm-gateway wget -qO- http://Llama-cpp:8080/models
sudo docker exec llm-gateway wget -qO- http://ollama:11434/api/tags
```

### OpenResty 反向代理（尤其是慢速本地模型）

Gateway 容器内监听 `0.0.0.0:8088`，宿主机发布端口限定为 `127.0.0.1:8088`。反代地址要按 OpenResty 的运行位置选择：

- OpenResty 运行在**宿主机进程**中：使用 [`openresty-ai.conf.example`](./openresty-ai.conf.example)，上游地址是 `http://127.0.0.1:8088`。
- OpenResty 运行在 **1Panel/Docker 容器**中（常见部署）：使用 [`openresty-ai-container.conf.example`](./openresty-ai-container.conf.example)，并确保 OpenResty 容器与 Gateway 加入同一个用户定义 Docker 网络（通常就是 `.env` 中的 `EXISTING_DOCKER_NETWORK`）。容器内的 `127.0.0.1` 指向 OpenResty 自己，不是宿主机或 Gateway；同网络下应使用 `http://llm-gateway:8088`。不需要为了容器互访把 8088 发布到公网。

两个样例都关闭 SSE 缓冲、缓存和 gzip，设置较长的读写超时，并关闭 Nginx 对上游请求的自动重试，避免推理 POST 被代理层意外重放。示例将代理读写空闲间隔设为 24 小时；`proxy_read_timeout` 是**相邻两次读取之间的空闲期限**，不是整个请求的总时限。样例还把 `client_max_body_size` 设为与 Gateway 默认 64 MiB 请求体上限一致。

慢速本地模型建议让 `LLAMA_REQUEST_TIMEOUT_SECONDS=0`（或 `llama.request_timeout_seconds: 0`）取消 Gateway 客户端整体时限；兼容旧配置仍保留未填写时的 3600 秒默认值。Gateway 的时限、OpenResty 的空闲超时，以及 Codex/Cursor 等客户端自己的请求期限彼此独立，仍需分别检查。配置里的 `warm_start` SSE 注释保活可帮助等待首 Token 时维持反代连接，但不能解除客户端自身的超时。

如果需要让审计记录获取真实客户端 IP，`server.trusted_proxies` 必须配置为实际 OpenResty 来源的固定 IP 或受控 CIDR；不要不加区分地信任所有转发头。

## 切换上游

两个地址已经在 `.env` 里。换上游在管理台做，不用改环境变量，也不用为了换上游重建容器。

1. 打开「上游推理服务」，选择 llama.cpp 或 Ollama，点「切换上游服务」。
2. 打开「本地模型」，给当前上游加载一个模型。

已加载的模型，或该上游目录里仅有的一个模型，会被自动用作 `/v1` 的模型。有多个模型且都没加载时，先加载再请求。每个上游记住自己上次的模型。

Codex 和性能测试都走 Gateway。对比两个上游时用同一条 prompt，看调用记录里的耗时。
