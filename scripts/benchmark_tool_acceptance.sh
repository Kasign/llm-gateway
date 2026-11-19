#!/usr/bin/env bash
# 单次真实模型工具 Benchmark 验收采集器。
# 凭据仅从环境变量读取，不写入结果文件或命令行参数。

set -euo pipefail
umask 077

: "${GATEWAY_URL:?请设置 GATEWAY_URL，例如 https://gateway.example.com}"
: "${GATEWAY_ADMIN_SESSION:?请设置 GATEWAY_ADMIN_SESSION（网页登录会话 Token）}"
: "${GATEWAY_CLIENT_API_KEY:?请设置 GATEWAY_CLIENT_API_KEY（独立客户端服务 API Key）}"

SCENARIO="${SCENARIO:-tool_performance_auto}"
ACCEPTANCE_CASE_ID="${ACCEPTANCE_CASE_ID:-}"
MODEL_ID="${MODEL_ID:-}"
MAX_TOKENS="${MAX_TOKENS:-384}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-2}"
POLL_TIMEOUT_SECONDS="${POLL_TIMEOUT_SECONDS:-1800}"
OUTPUT_DIR="${OUTPUT_DIR:-./benchmark-tool-evidence}"
PROMPT="${PROMPT:-}"

case "$SCENARIO" in
  tool_performance_auto)
    if [[ -z "$PROMPT" ]]; then
      PROMPT="Go 语言当前最新正式稳定版的版本号和发布日期是什么？请说明依据并给出来源链接。"
    fi
    ;;
  tool_performance)
    if [[ -z "$PROMPT" ]]; then
      PROMPT="请搜索 Go 官方发布信息，并总结最新稳定版本、发布日期和官方来源链接。"
    fi
    ;;
  *)
    echo "SCENARIO 仅支持 tool_performance_auto 或 tool_performance" >&2
    exit 2
    ;;
esac

for command_name in curl jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "缺少依赖：$command_name" >&2
    exit 2
  fi
done

if ! [[ "$MAX_TOKENS" =~ ^[1-9][0-9]*$ ]] || (( MAX_TOKENS > 8192 )); then
  echo "MAX_TOKENS 必须是 1～8192 的整数" >&2
  exit 2
fi
if ! [[ "$POLL_INTERVAL_SECONDS" =~ ^[1-9][0-9]*$ ]] || ! [[ "$POLL_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ]]; then
  echo "轮询间隔和超时必须是正整数" >&2
  exit 2
fi

BASE_URL="${GATEWAY_URL%/}"
if [[ -n "$ACCEPTANCE_CASE_ID" ]]; then
  case "$ACCEPTANCE_CASE_ID" in TM-01|TM-02|TM-03|TM-04|TM-05) ;; *) echo "未知验收用例 ID：$ACCEPTANCE_CASE_ID" >&2; exit 2 ;; esac
  CATALOG_STATUS="$(request GET "$BASE_URL/api/benchmark/acceptance-cases" "$RESPONSE_FILE")"
  if [[ "$CATALOG_STATUS" != "200" ]]; then
    echo "读取验收用例目录失败，HTTP $CATALOG_STATUS" >&2
    jq . "$RESPONSE_FILE" >&2 2>/dev/null || cat "$RESPONSE_FILE" >&2
    exit 1
  fi
  CASE_META="$(jq -c --arg id "$ACCEPTANCE_CASE_ID" '.items[] | select(.id == $id)' "$RESPONSE_FILE")"
  if [[ -z "$CASE_META" ]]; then
    echo "Gateway 未提供验收用例：$ACCEPTANCE_CASE_ID" >&2
    exit 2
  fi
  if [[ "$(jq -r '.manual_only' <<<"$CASE_META")" == "true" ]]; then
    echo "$ACCEPTANCE_CASE_ID 需要隔离环境；当前 Gateway 尚未开放自动执行。" >&2
    exit 2
  fi
  EXPECTED_SCENARIO="$(jq -r '.scenario' <<<"$CASE_META")"
  if [[ "$SCENARIO" != "$EXPECTED_SCENARIO" ]]; then
    echo "$ACCEPTANCE_CASE_ID 要求 SCENARIO=$EXPECTED_SCENARIO，当前为 $SCENARIO" >&2
    exit 2
  fi
fi
mkdir -p "$OUTPUT_DIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RESPONSE_FILE="$(mktemp)"
trap 'rm -f "$RESPONSE_FILE"' EXIT

request() {
  local method="$1"
  local url="$2"
  local body_file="$3"
  local payload="${4:-}"
  local -a args
  args=(-sS -X "$method" "$url"
    -H "Authorization: Bearer $GATEWAY_ADMIN_SESSION"
    -H "X-Client-API-Key: $GATEWAY_CLIENT_API_KEY"
    -H "Accept: application/json")
  if [[ -n "$payload" ]]; then
    args+=(-H "Content-Type: application/json" --data-binary "$payload")
  fi
  curl "${args[@]}" -o "$body_file" -w '%{http_code}'
}

PAYLOAD="$(jq -n \
  --arg scenario "$SCENARIO" \
  --arg prompt "$PROMPT" \
  --arg model_id "$MODEL_ID" \
  --arg acceptance_case_id "$ACCEPTANCE_CASE_ID" \
  --argjson max_tokens "$MAX_TOKENS" \
  '{scenario:$scenario,prompt:$prompt,max_tokens:$max_tokens,use_active:($model_id == "")}
   + (if $model_id == "" then {} else {model_id:$model_id} end)
   + (if $acceptance_case_id == "" then {} else {acceptance_case_id:$acceptance_case_id} end)')"

echo "创建 Benchmark：scenario=$SCENARIO max_tokens=$MAX_TOKENS"
HTTP_STATUS="$(request POST "$BASE_URL/api/benchmark" "$RESPONSE_FILE" "$PAYLOAD")"
if [[ "$HTTP_STATUS" != "200" && "$HTTP_STATUS" != "201" ]]; then
  echo "创建任务失败，HTTP $HTTP_STATUS：" >&2
  jq . "$RESPONSE_FILE" >&2 2>/dev/null || cat "$RESPONSE_FILE" >&2
  exit 1
fi

JOB_ID="$(jq -r '.job_id // empty' "$RESPONSE_FILE")"
if [[ -z "$JOB_ID" ]]; then
  echo "创建响应缺少 job_id：" >&2
  jq . "$RESPONSE_FILE" >&2
  exit 1
fi
POLL_URL="$BASE_URL/api/benchmark/$JOB_ID"
RESULT_FILE="$OUTPUT_DIR/${STAMP}-${SCENARIO}-${JOB_ID}.json"
STARTED_AT="$(date +%s)"

while true; do
  HTTP_STATUS="$(request GET "$POLL_URL" "$RESPONSE_FILE")"
  if [[ "$HTTP_STATUS" != "200" ]]; then
    echo "查询任务失败，HTTP $HTTP_STATUS：" >&2
    jq . "$RESPONSE_FILE" >&2 2>/dev/null || cat "$RESPONSE_FILE" >&2
    exit 1
  fi

  STATUS="$(jq -r '.job.status // .status // empty' "$RESPONSE_FILE")"
  if [[ -z "$STATUS" ]]; then
    echo "任务状态响应缺少 job.status：" >&2
    jq . "$RESPONSE_FILE" >&2
    exit 1
  fi
  echo "任务状态：$STATUS"

  case "$STATUS" in
    running|cancelling)
      ;;
    *)
      cp "$RESPONSE_FILE" "$RESULT_FILE"
      break
      ;;
  esac

  NOW="$(date +%s)"
  if (( NOW - STARTED_AT >= POLL_TIMEOUT_SECONDS )); then
    echo "轮询超时。任务仍可能在 Gateway 后台运行，job_id=$JOB_ID" >&2
    echo "可稍后使用 GET $POLL_URL 查询；本次未保存终态证据。" >&2
    exit 124
  fi
  sleep "$POLL_INTERVAL_SECONDS"
done

echo "证据文件：$RESULT_FILE"
jq '{
  job_id: (.job.id // .job.job_id // null),
  status: .job.status,
  scenario: .job.scenario,
  acceptance: .acceptance,
  model_key: .job.model_key,
  request_id: .request_id,
  tool_metrics: .tool_metrics,
  tool_call_events: [
    (.tool_call_events // [])[] |
    {round, seq, tool_name, status, duration_ms, error_code}
  ]
}' "$RESULT_FILE"
