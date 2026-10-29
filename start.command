#!/bin/bash
# Finder opens .command files in Terminal; use macOS's built-in Bash.
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd -- "$root"

finish() {
  local status=$?
  if ((status != 0)); then
    printf '\n启动未完成，请查看上方错误信息。\n' >&2
    if [[ -t 0 ]]; then read -r -p '按回车退出…' _ || true; fi
  fi
}
trap finish EXIT

for arg in "$@"; do
  case "$arg" in --help|-h) /bin/bash "$root/scripts/start.sh" "$@"; exit 0;; esac
done

# Finder may not inherit the same PATH as an interactive terminal.
export PATH="$PATH:/usr/local/bin:/opt/homebrew/bin:/Applications/Docker.app/Contents/Resources/bin:$HOME/.docker/bin"
if ! command -v docker >/dev/null 2>&1; then
  printf '请先安装 Docker Desktop，再双击此文件。\n' >&2
  exit 1
fi

if ! docker info --format '{{.ServerVersion}}' >/dev/null 2>&1; then
  printf '正在打开 Docker Desktop，请等待启动完成…\n'
  if ! open -a Docker; then
    printf '无法打开 Docker Desktop，请手动启动 Docker 后重试。\n' >&2
    exit 1
  fi
  deadline=$((SECONDS + 120))
  until docker info --format '{{.ServerVersion}}' >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
      printf 'Docker 尚未就绪，请确认 Docker Desktop 已启动后重试。\n' >&2
      exit 1
    fi
    sleep 2
  done
fi

/bin/bash "$root/scripts/start.sh" "$@"

# Read only the validated origin; never execute the private configuration.
project=yishuld-refund-app
while (($#)); do
  case "$1" in
    --check|--help|-h) exit 0;;
    --project) project=$2; shift;;
  esac
  shift
done
while IFS= read -r line || [[ -n $line ]]; do
  line=${line%$'\r'}
  if [[ $line == APP_ORIGIN=* ]]; then
    url=${line#APP_ORIGIN=}
    open "$url" || printf '请在浏览器打开：%s\n' "$url"
    break
  fi
done < "$root/.runtime/start-$project.env"
