# 1Panel 部署

配置分两层，字段含义见 [配置说明.md](./配置说明.md)。这篇只写第一次把服务跑起来、以及之后怎么换上游。

## 第一次部署

1. 将项目放到宿主机目录，例如 `/server-whl/docker/llm-gateway-v11/`。
2. 进入 `deploy`：

```bash
cd /server-whl/docker/llm-gateway-v11/deploy
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
- Web 登录见 [控制台与密码说明.md](./控制台与密码说明.md)。哈希写在 `.env` 的 `LLM_GATEWAY_ADMIN_PASSWORD_HASH`，`$` 要写成 `$$`。

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

Gateway 只监听 `127.0.0.1:8088`。OpenResty 反代到这个地址，SSE 需要 `proxy_buffering off` 和较长的 `proxy_read_timeout`。

## 切换上游

两个地址已经在 `.env` 里。换上游在管理台做，不用改环境变量，也不用为了换上游重建容器。

1. 打开「上游推理服务」，选择 llama.cpp 或 Ollama，点「切换上游服务」。
2. 打开「本地模型」，给当前上游加载一个模型。

已加载的模型，或该上游目录里仅有的一个模型，会被自动用作 `/v1` 的模型。有多个模型且都没加载时，先加载再请求。每个上游记住自己上次的模型。

Codex 和性能测试都走 Gateway。对比两个上游时用同一条 prompt，看调用记录里的耗时。
