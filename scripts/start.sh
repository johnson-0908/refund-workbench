#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
project=yishuld-refund-app
bind_address='' port='' origin='' pull=false check_only=false
die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
((BASH_VERSINFO[0] >= 4)) || die 'Bash 4+ is required'
usage() {
  printf '%s\n' 'Usage: bash scripts/start.sh [--host IPv4] [--port 8080] [--origin http://server:8080] [--project name] [--pull] [--check]'
}
while (($#)); do
  case "$1" in
    --host|--port|--origin|--project)
      (($# >= 2)) || die "Missing value for $1"
      case "$1" in --host) bind_address=$2;; --port) port=$2;; --origin) origin=$2;; --project) project=$2;; esac
      shift 2;;
    --pull) pull=true; shift;;
    --check) check_only=true; shift;;
    --help|-h) usage; exit 0;;
    *) usage >&2; die 'Unknown argument';;
  esac
done
[[ $project =~ ^[a-z0-9][a-z0-9_-]{0,62}$ ]] || die 'Invalid project name'
[[ $bind_address != localhost ]] || bind_address=127.0.0.1
valid_address() {
  local value=$1 octet
  [[ $value =~ ^(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$ ]] || die 'Host must be a literal IPv4 bind address'
  local -a octets
  IFS=. read -r -a octets <<< "$value"
  for octet in "${octets[@]}"; do ((10#$octet <= 255)) || die 'Invalid IPv4 address'; done
}
valid_port() { [[ $1 =~ ^[0-9]{1,5}$ ]] && ((10#$1 >= 1 && 10#$1 <= 65535)) || die 'Invalid port'; }
valid_origin() {
  [[ $1 =~ ^https?://[A-Za-z0-9.-]+(:([0-9]+))?$ ]] || die 'Origin must be exact http(s)://host:port without path, slash, or wildcard'
  local origin_port=${BASH_REMATCH[2]:-}
  [[ -z $origin_port ]] || valid_port "$origin_port"
}
valid_transport() {
  local value=$1 secure=$2 host octet loopback=false
  [[ $value != https://* || $secure == true ]] || die 'HTTPS Origin requires SESSION_COOKIE_SECURE=true'
  if [[ $value == http://* && $secure == true ]]; then
    host=${value#http://}; host=${host%%:*}
    [[ $host != localhost ]] || loopback=true
    if [[ $host =~ ^127\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$ ]]; then
      loopback=true
      local -a octets
      IFS=. read -r -a octets <<< "$host"
      for octet in "${octets[@]}"; do ((10#$octet <= 255)) || loopback=false; done
    fi
    $loopback || die 'Non-loopback HTTP Origin requires SESSION_COOKIE_SECURE=false; use HTTPS for secure cookies'
  fi
}
random_hex() {
  if command -v openssl >/dev/null 2>&1; then openssl rand -hex 32
  else od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; fi
}
assert_no_source_links() {
  local path=$1
  while [[ $path != / && -n $path ]]; do
    [[ ! -L $path ]] || die 'Source/build path contains a symbolic link'
    path=${path%/*}
  done
}
prepare_build_context() {
  local base stage relative source target
  if [[ -n ${REFUND_BUILD_ROOT:-} ]]; then base=$REFUND_BUILD_ROOT
  elif [[ $root =~ ^/([a-zA-Z])/ && -d /${BASH_REMATCH[1]}/code ]]; then base=/${BASH_REMATCH[1]}/code/refund-workbench-build
  else base=/tmp/refund-workbench-build; fi
  [[ $base == /* && $base != *[!\ -\~]* ]] || die 'REFUND_BUILD_ROOT must be a writable ASCII-only absolute directory'
  assert_no_source_links "$base"
  mkdir -p -- "$base"
  stage=$(mktemp -d "$base/$project-XXXXXXXX")
  local -a files=(.dockerignore Dockerfile.backend Dockerfile.web backend/go.mod backend/go.sum web/package.json web/package-lock.json web/index.html web/tsconfig.json web/vite.config.ts deploy/nginx.conf)
  for source in "$root/backend/cmd" "$root/backend/internal" "$root/web/src"; do
    assert_no_source_links "$source"
    [[ -z $(find "$source" -type l -print -quit) ]] || die 'Source tree contains a symbolic link'
  done
  while IFS= read -r -d '' source; do files+=("${source#"$root/"}"); done < <(find "$root/backend/cmd" "$root/backend/internal" -type f -name '*.go' -print0)
  while IFS= read -r -d '' source; do files+=("${source#"$root/"}"); done < <(find "$root/web/src" -type f \( -name '*.ts' -o -name '*.vue' -o -name '*.css' \) ! -name '.env*' -print0)
  for relative in "${files[@]}"; do
    source=$root/$relative; target=$stage/$relative
    assert_no_source_links "$source"
    mkdir -p -- "$(dirname -- "$target")"; cp -- "$source" "$target"
  done
  printf '%s\n' "$stage" > "$runtime/build-$project.path"
  printf 'Source-only build context retained for diagnosis: %s\n' "$stage" >&2
  BUILD_CONTEXT=$stage; export BUILD_CONTEXT
}
command -v docker >/dev/null 2>&1 || die 'Docker CLI is required'
docker info --format '{{.ServerVersion}}' >/dev/null 2>&1 || die 'Docker Engine unavailable; start it and rerun'
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2+ is required'

runtime=$root/.runtime
config=$runtime/start-$project.env
if [[ ! -e $config ]]; then
  existing_volumes=$(docker volume ls --quiet --filter "label=com.docker.compose.project=$project") || die 'Could not inspect project volumes safely'
  [[ -z $existing_volumes ]] || die 'Project volumes exist but saved config is missing. Restore the config; no new passwords were generated.'
  bind_address=${bind_address:-127.0.0.1}; port=${port:-8080}
  valid_address "$bind_address"; valid_port "$port"
  if [[ -z $origin ]]; then
    [[ $bind_address == 127.* ]] || die 'A non-loopback --host requires explicit --origin http://server-ip:port'
    origin=http://$bind_address:$port
  fi
  valid_origin "$origin"
  mkdir -p -- "$runtime"
  password=$(random_hex); root_password=$(random_hex)
  [[ $password =~ ^[0-9a-f]{64}$ && $root_password =~ ^[0-9a-f]{64}$ && $password != "$root_password" ]] || die 'Secure random generation failed'
  secure=false; [[ $origin != https://* ]] || secure=true
  temp=$(umask 077; mktemp "$runtime/.start-XXXXXX")
  trap '[[ ! -n ${temp:-} ]] || rm -f -- "$temp"' EXIT
  cat > "$temp" <<EOF
# Private, generated once. Keep this file and the same project for future starts.
DEPLOY_NAMESPACE=$project
BIND_IP=$bind_address
WEB_PORT=$port
APP_ORIGIN=$origin
SESSION_COOKIE_SECURE=$secure
MYSQL_ROOT_PASSWORD=$root_password
MYSQL_PASSWORD=$password
ENABLE_DATA_RESET=true
REGISTRY=crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com
IMAGE_NAMESPACE=pandacount
IMAGE_TAG=0.1.0
EOF
  # Atomic no-clobber installation. A concurrent invocation must not overwrite it.
  ln -- "$temp" "$config" 2>/dev/null || [[ -f $config ]] || die 'Could not install private configuration'
  rm -f -- "$temp"; temp=''
  unset password root_password
fi

# Do not source arbitrary shell code from a configuration file.
declare -A settings=()
while IFS= read -r line || [[ -n $line ]]; do
  line=${line%$'\r'}
  [[ -n $line && $line != \#* ]] || continue
  [[ $line =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]] || die 'Invalid saved config; it was not changed'
  key=${BASH_REMATCH[1]}; value=${BASH_REMATCH[2]}
  [[ ! ${settings[$key]+exists} ]] || die 'Duplicate saved config key'
  settings[$key]=$value
done < "$config"
for key in DEPLOY_NAMESPACE BIND_IP WEB_PORT APP_ORIGIN MYSQL_ROOT_PASSWORD MYSQL_PASSWORD SESSION_COOKIE_SECURE ENABLE_DATA_RESET; do
  [[ ${settings[$key]+exists} ]] || die "Saved config is missing $key; it was not changed"
done
[[ ${settings[DEPLOY_NAMESPACE]} == "$project" ]] || die 'Saved project differs from requested project'
valid_address "${settings[BIND_IP]}"; valid_port "${settings[WEB_PORT]}"; valid_origin "${settings[APP_ORIGIN]}"
for key in MYSQL_ROOT_PASSWORD MYSQL_PASSWORD; do [[ ${settings[$key]} =~ ^[0-9a-fA-F]{64}$ ]] || die "Saved $key must be 64 random hex characters"; done
[[ ${settings[MYSQL_PASSWORD]} != "${settings[MYSQL_ROOT_PASSWORD]}" ]] || die 'Saved SQL passwords must be different'
for key in SESSION_COOKIE_SECURE ENABLE_DATA_RESET; do [[ ${settings[$key]} == true || ${settings[$key]} == false ]] || die "Saved $key must be true or false"; done
valid_transport "${settings[APP_ORIGIN]}" "${settings[SESSION_COOKIE_SECURE]}"
[[ -z $bind_address || $bind_address == "${settings[BIND_IP]}" ]] || die 'Saved Host differs; edit saved config deliberately or use another --project'
[[ -z $port || $port == "${settings[WEB_PORT]}" ]] || die 'Saved port differs; edit saved config deliberately or use another --project'
[[ -z $origin || $origin == "${settings[APP_ORIGIN]}" ]] || die 'Saved Origin differs; edit saved config deliberately or use another --project'
if [[ ${settings[BIND_IP]} != 127.* ]]; then printf '%s\n' 'Warning: remote access uses predefined identities, not real SSO. Restrict firewall/security-group sources; this script does not alter them.' >&2; fi
if [[ ${settings[APP_ORIGIN]} == https://* ]]; then printf '%s\n' 'Warning: this Compose publishes HTTP only; configure your TLS reverse proxy separately.' >&2; fi

compose=(docker compose --project-name "$project" --env-file "$config" -f "$root/compose.deploy.yaml")
$pull || compose+=(-f "$root/compose.build.yaml")
# Override inherited values with saved settings, without logging the secrets.
for key in REGISTRY IMAGE_NAMESPACE IMAGE_TAG DEPLOY_NAMESPACE BIND_IP WEB_PORT APP_ORIGIN MYSQL_ROOT_PASSWORD MYSQL_PASSWORD ENABLE_DATA_RESET SESSION_COOKIE_SECURE MYSQL_MEMORY_LIMIT REDIS_MEMORY_LIMIT KAFKA_MEMORY_LIMIT BACKEND_MEMORY_LIMIT WEB_MEMORY_LIMIT BUILD_CONTEXT; do
  unset "$key"
  if [[ ${settings[$key]+exists} ]]; then export "$key=${settings[$key]}"; fi
done
"${compose[@]}" config --quiet
web_id=$("${compose[@]}" ps --status running --quiet web 2>/dev/null || true)
if [[ -z $web_id ]]; then
  occupied=false
  if command -v ss >/dev/null 2>&1; then
    [[ -z $(ss -H -ltn "sport = :${settings[WEB_PORT]}") ]] || occupied=true
  elif command -v lsof >/dev/null 2>&1; then
    ! lsof -nPiTCP:"${settings[WEB_PORT]}" -sTCP:LISTEN -t >/dev/null 2>&1 || occupied=true
  else
    # Bash's local TCP probe is a best-effort fallback; Docker's bind is final.
    probe=127.0.0.1
    if (exec 3<>"/dev/tcp/$probe/${settings[WEB_PORT]}") 2>/dev/null; then occupied=true; fi
  fi
  ! $occupied || die 'Requested port is occupied; no process was stopped. Choose a free port deliberately.'
fi
if $check_only; then printf 'Configuration and port checks passed: %s (no containers started).\n' "$project"; exit 0; fi
if $pull; then
  "${compose[@]}" pull
  "${compose[@]}" up -d --wait --wait-timeout 300
else
  if [[ $root == *[!\ -\~]* ]]; then prepare_build_context; fi
  "${compose[@]}" up -d --build --wait --wait-timeout 300
fi
printf 'Ready: %s\nProject: %s\nPrivate configuration preserved: %s\n' "${settings[APP_ORIGIN]}" "$project" "$config"
printf 'Stop without deleting data: docker compose --project-name %q --env-file %q -f %q stop\n' "$project" "$config" "$root/compose.deploy.yaml"
