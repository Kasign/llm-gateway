#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v git >/dev/null 2>&1; then
  echo "git is required to capture the Gateway build revision" >&2
  exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to build the Gateway image" >&2
  exit 1
fi

export GATEWAY_BUILD_REVISION="$(git -C "$ROOT" rev-parse HEAD)"
if [[ -n "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ]]; then
  export GATEWAY_BUILD_MODIFIED=true
else
  export GATEWAY_BUILD_MODIFIED=false
fi

cd "$ROOT"
exec docker compose build gateway
