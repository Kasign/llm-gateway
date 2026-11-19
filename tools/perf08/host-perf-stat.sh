#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat >&2 <<'EOF'
用法:
  tools/perf08/host-perf-stat.sh <host-pid> [duration-seconds] [output-directory]

只对宿主机上的 llama-server 进程执行短时 perf stat。
- duration 默认 10 秒，允许范围 1..10 秒
- 只采目标 PID，不使用全系统 -a
- 不采集 perf.data、调用栈、寄存器、内存地址或请求内容
- 输出目录权限为 0700，文件权限为 0600
EOF
}

fail() {
  printf '错误: %s\n' "$*" >&2
  exit 2
}

[[ $# -ge 1 && $# -le 3 ]] || { usage; exit 2; }

pid="$1"
duration="${2:-10}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
output_dir="${3:-$HOME/perf08-captures/$timestamp-pid-$pid}"

[[ "$pid" =~ ^[1-9][0-9]*$ ]] || fail "PID 必须是正整数"
[[ "$duration" =~ ^[0-9]+$ ]] || fail "采样时长必须是整数秒"
(( duration >= 1 && duration <= 10 )) || fail "首轮采样时长限制为 1..10 秒"
command -v perf >/dev/null 2>&1 || fail "未找到 perf；脚本不会自动安装或提权"
command -v readlink >/dev/null 2>&1 || fail "未找到 readlink"

proc_dir="/proc/$pid"
[[ -r "$proc_dir/stat" && -e "$proc_dir/exe" ]] || fail "PID $pid 不存在或不可读取；必须传入宿主机 PID"

proc_start_ticks() {
  local stat_line
  stat_line="$(<"$proc_dir/stat")" || return 1
  # comm 字段由括号包裹，先移除到最后一个 ') '，再取原始字段 22（剩余字段第 20 项）。
  stat_line="${stat_line##*) }"
  awk '{print $20}' <<<"$stat_line"
}

exe_path="$(readlink -f "$proc_dir/exe" 2>/dev/null || true)"
exe_name="${exe_path##*/}"
exe_name="${exe_name% (deleted)}"
[[ "$exe_name" == "llama-server" ]] || fail "目标可执行文件是 '${exe_name:-unknown}'，不是 llama-server；不会按进程名猜测模型实例"

parent_pid="$(awk '/^PPid:/ { print $2 }' "$proc_dir/status")"
[[ "$parent_pid" =~ ^[1-9][0-9]*$ ]] || fail "无法读取目标进程的父 PID"
parent_comm="$(cat "/proc/$parent_pid/comm" 2>/dev/null || true)"
[[ "$parent_comm" == "llama-server" ]] || fail "目标进程的父进程不是 llama-server Router；请确认传入的是 Router 创建的模型 child PID"

start_ticks="$(proc_start_ticks)" || fail "无法读取 PID 启动时间"
[[ "$start_ticks" =~ ^[0-9]+$ ]] || fail "PID 启动时间格式异常"

if [[ -e "$output_dir" ]]; then
  [[ -d "$output_dir" ]] || fail "输出路径已存在且不是目录"
  [[ -z "$(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]] || fail "输出目录非空，拒绝覆盖"
else
  mkdir -p "$output_dir"
fi
chmod 700 "$output_dir"

build_id=""
if command -v readelf >/dev/null 2>&1; then
  build_id="$(readelf -n "$proc_dir/exe" 2>/dev/null | awk '/Build ID:/ { print $3; exit }' || true)"
fi

cpu_model="$(awk -F: '/model name[[:space:]]*:/ { gsub(/^[[:space:]]+/, "", $2); print $2; exit }' /proc/cpuinfo 2>/dev/null || true)"
perf_version="$(perf --version 2>&1 | head -n 1)"
perf_paranoid="$(cat /proc/sys/kernel/perf_event_paranoid 2>/dev/null || printf 'unavailable')"
cap_eff="$(awk '/^CapEff:/ { print $2 }' /proc/self/status 2>/dev/null || true)"
has_perfmon=0
if [[ "$cap_eff" =~ ^[0-9a-fA-F]+$ ]] && (( (16#$cap_eff & (1 << 38)) != 0 )); then
  has_perfmon=1
fi
allow_hardware=0
if (( EUID == 0 || has_perfmon == 1 )) || { [[ "$perf_paranoid" =~ ^-?[0-9]+$ ]] && (( perf_paranoid <= 0 )); }; then
  allow_hardware=1
fi

{
  printf 'schema_version=1\n'
  printf 'scope=process_window\n'
  printf 'request_correlation=none\n'
  printf 'host_pid=%s\n' "$pid"
  printf 'parent_pid=%s\n' "$parent_pid"
  printf 'parent_executable_comm=llama-server\n'
  printf 'process_start_ticks=%s\n' "$start_ticks"
  printf 'executable=llama-server\n'
  printf 'binary_build_id=%s\n' "${build_id:-unavailable}"
  printf 'host_kernel=%s\n' "$(uname -r)"
  printf 'cpu_model=%s\n' "${cpu_model:-unavailable}"
  printf 'perf_version=%s\n' "$perf_version"
  printf 'perf_event_paranoid=%s\n' "$perf_paranoid"
  printf 'duration_seconds=%s\n' "$duration"
  printf 'started_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'hardware_pmu_events='
  pmus=()
  if (( allow_hardware )); then
    [[ -d /sys/bus/event_source/devices/cpu_core ]] && pmus+=(cpu_core)
    [[ -d /sys/bus/event_source/devices/cpu_atom ]] && pmus+=(cpu_atom)
  fi
  if (( ${#pmus[@]} )); then
    printf '%s\n' "$(IFS=,; echo "${pmus[*]}")"
  elif (( allow_hardware )); then
    printf 'not_detected\n'
  else
    printf 'not_sampled_permissions\n'
  fi
} >"$output_dir/metadata.txt"
chmod 600 "$output_dir/metadata.txt"

event_args=(
  -e task-clock
  -e context-switches
  -e cpu-migrations
  -e page-faults
)
if (( allow_hardware )) && [[ -d /sys/bus/event_source/devices/cpu_core ]]; then
  event_args+=(-e cpu_core/cycles/ -e cpu_core/instructions/)
fi
if (( allow_hardware )) && [[ -d /sys/bus/event_source/devices/cpu_atom ]]; then
  event_args+=(-e cpu_atom/cycles/ -e cpu_atom/instructions/)
fi

printf '采样目标: pid=%s executable=%s duration=%ss\n' "$pid" "$exe_name" "$duration"
printf '输出目录: %s\n' "$output_dir"
printf '采集范围: process_window；不具备 request ID 关联\n'

set +e
perf stat -x, -o "$output_dir/perf-stat.csv" -p "$pid" "${event_args[@]}" -- sleep "$duration"
perf_status=$?
set -e
chmod 600 "$output_dir/perf-stat.csv" 2>/dev/null || true

end_ticks="$(proc_start_ticks 2>/dev/null || true)"
if [[ "$end_ticks" == "$start_ticks" ]]; then
  identity_status="stable"
else
  identity_status="changed_or_exited"
fi
{
  printf 'perf_exit_code=%s\n' "$perf_status"
  printf 'process_identity_after_sample=%s\n' "$identity_status"
  printf 'finished_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >>"$output_dir/metadata.txt"

if (( perf_status != 0 )); then
  printf 'perf stat 执行失败，退出码=%s；检查 perf-stat.csv 和权限，不要修改系统级 perf_event_paranoid。\n' "$perf_status" >&2
  exit "$perf_status"
fi
if [[ "$identity_status" != "stable" ]]; then
  fail "采样期间目标 PID 退出或被复用；本次结果不可用于稳定进程对照"
fi

printf '采样完成。请保留 metadata.txt 与 perf-stat.csv；不要把结果标记为 request-level。\n'
