#!/bin/bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
project=yishuld-refund-app

finish() {
  local status=$?
  if ((status != 0)); then
    printf '\n停止未完成，请查看上方错误信息。\n' >&2
    if [[ -t 0 ]]; then read -r -p '按回车退出…' _ || true; fi
  fi
}
trap finish EXIT
die() { printf '%s\n' "$*" >&2; exit 1; }
while (($#)); do
  case "$1" in
    --project)
      (($# >= 2)) || die '请在 --project 后填写启动时的项目名称。'
      project=$2; shift 2;;
    --help|-h) printf '用法：./stop.command [--project 项目名称]\n'; exit 0;;
    *) die '不支持此参数，请使用 ./stop.command --help 查看用法。';;
  esac
done
[[ $project =~ ^[a-z0-9][a-z0-9_-]{0,62}$ ]] || die '项目名称无效。'
config=$root/.runtime/start-$project.env
[[ -f $config ]] || die '未找到该项目的启动配置；请确认项目名称或恢复原配置后重试。'

# Confirm the saved project without executing configuration contents.
saved_project='' project_count=0
while IFS= read -r line || [[ -n $line ]]; do
  line=${line%$'\r'}
  if [[ $line == DEPLOY_NAMESPACE=* ]]; then
    saved_project=${line#DEPLOY_NAMESPACE=}
    project_count=$((project_count + 1))
  fi
done < "$config"
[[ $project_count == 1 && $saved_project == "$project" ]] || die '保存的项目名称不匹配或重复，未执行停止。'

export PATH="$PATH:/usr/local/bin:/opt/homebrew/bin:/Applications/Docker.app/Contents/Resources/bin:$HOME/.docker/bin"
command -v docker >/dev/null 2>&1 || die '未找到 Docker，请确认已安装 Docker Desktop。'
docker info --format '{{.ServerVersion}}' >/dev/null 2>&1 || die 'Docker 未运行或无法连接，未执行停止。'
docker compose version >/dev/null 2>&1 || die '需要 Docker Compose v2+。'

compose=(docker compose --project-name "$project" --env-file "$config" -f "$root/compose.deploy.yaml")
"${compose[@]}" stop
remaining=$("${compose[@]}" ps --status running --quiet)
[[ -z $remaining ]] || die '仍有服务运行，请检查是否有另一个窗口正在启动项目。'
printf '已停止项目：%s\n配置和数据已保留。\n' "$project"
if [[ $project == yishuld-refund-app ]]; then
  printf '双击 start.command 可再次启动。\n'
else
  printf '再次启动：./start.command --project %s\n' "$project"
fi
