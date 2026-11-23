#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

INSTALL_DIR="${INSTALL_DIR:-/opt/tgcloud}"
BACKUP_DIR="/opt/tgcloud-backups"
BOT_SERVICE="tgcloud-clouddrive2-bot"
BOT_CONTAINER="tgcloud-clouddrive2-bot"
CD2_MANAGED_CONTAINER="tgcloud-clouddrive2"
DOCKER_NETWORK="tgcloud-clouddrive2-net"
MODE="${1:-full}"

log() {
  printf '[TGCloud-REPAIR] %s\n' "$*"
}

fail() {
  trap - ERR
  printf '[TGCloud-REPAIR][ERROR] %s\n' "$*" >&2
  printf 'TGCLOUD_REPAIR=FAILED\n' >&2
  exit 1
}

REPAIR_TEMP_ENV=""
COMPOSE_TMP=""

cleanup_repair_temps() {
  rm -f -- "${REPAIR_TEMP_ENV:-}" "${COMPOSE_TMP:-}"
}

trap cleanup_repair_temps EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

recover_webdav_credentials() {
  local backup username_b64 password_b64 username password esc_u esc_p status key
  REPAIR_TEMP_ENV="$(mktemp /tmp/tgcloud-webdav-credentials.XXXXXX)"
  while IFS= read -r backup; do
    [[ -n "$backup" ]] || continue
    if ! tar -xOf "$backup" ./.env > "$REPAIR_TEMP_ENV" 2>/dev/null; then
      continue
    fi
    sed -i 's/\r$//' "$REPAIR_TEMP_ENV"
    username_b64="$(
      awk -F= '$1=="WEBDAV_USERNAME_B64" {sub(/^[^=]*=/,""); print; exit}' \
        "$REPAIR_TEMP_ENV"
    )"
    [[ -n "$username_b64" ]] || username_b64="$(
      awk -F= '$1=="CD2_WEBDAV_USERNAME_B64" {sub(/^[^=]*=/,""); print; exit}' \
        "$REPAIR_TEMP_ENV"
    )"
    password_b64="$(
      awk -F= '$1=="WEBDAV_PASSWORD_B64" {sub(/^[^=]*=/,""); print; exit}' \
        "$REPAIR_TEMP_ENV"
    )"
    [[ -n "$password_b64" ]] || password_b64="$(
      awk -F= '$1=="CD2_WEBDAV_PASSWORD_B64" {sub(/^[^=]*=/,""); print; exit}' \
        "$REPAIR_TEMP_ENV"
    )"
    [[ "$username_b64" =~ ^[A-Za-z0-9+/=]+$ && "$password_b64" =~ ^[A-Za-z0-9+/=]+$ ]] || continue
    username="$(printf '%s' "$username_b64" | base64 -d 2>/dev/null)" \
      || continue
    password="$(printf '%s' "$password_b64" | base64 -d 2>/dev/null)" \
      || continue
    [[ "$username" != *$'\n'* && "$username" != *$'\r'* && "$password" != *$'\n'* && "$password" != *$'\r'* ]] || continue
    esc_u="${username//\\/\\\\}"
    esc_u="${esc_u//\"/\\\"}"
    esc_p="${password//\\/\\\\}"
    esc_p="${esc_p//\"/\\\"}"
    status="$(
      printf 'user = "%s:%s"\n' "$esc_u" "$esc_p" \
        | curl -sS --connect-timeout 5 --max-time 10 \
            -K - \
            -X PROPFIND -H 'Depth: 0' \
            -o /dev/null -w '%{http_code}' \
            http://127.0.0.1:19798/dav/ 2>/dev/null || true
    )"
    if [[ "$status" == "200" || "$status" == "207" ]]; then
      for key in WEBDAV_USERNAME_B64 CD2_WEBDAV_USERNAME_B64; do
        if grep -q "^${key}=" "$INSTALL_DIR/.env"; then
          sed -i "s|^${key}=.*$|${key}=$username_b64|" "$INSTALL_DIR/.env"
        fi
      done
      for key in WEBDAV_PASSWORD_B64 CD2_WEBDAV_PASSWORD_B64; do
        if grep -q "^${key}=" "$INSTALL_DIR/.env"; then
          sed -i "s|^${key}=.*$|${key}=$password_b64|" "$INSTALL_DIR/.env"
        fi
      done
      rm -f -- "$REPAIR_TEMP_ENV"
      REPAIR_TEMP_ENV=""
      return 0
    fi
  done < <(
    find "$BACKUP_DIR" -maxdepth 1 -type f -name 'config-*.tar.gz' \
      -printf '%T@ %p\n' 2>/dev/null \
      | sort -nr | cut -d' ' -f2-
  )
  rm -f -- "$REPAIR_TEMP_ENV"
  REPAIR_TEMP_ENV=""
  return 1
}

trap 'fail "修复在第 ${LINENO} 行失败。请保留完整日志。"' ERR

[[ "$(id -u)" -eq 0 ]] || fail "修复脚本必须以 root 或 sudo 运行"
[[ "$INSTALL_DIR" =~ ^/opt/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$ ]] \
  || fail "安装目录必须是 /opt/ 下的安全绝对路径"
[[ "/$INSTALL_DIR/" != *"/../"* && "/$INSTALL_DIR/" != *"/./"* ]] \
  || fail "安装目录不能包含 . 或 .. 路径段"
[[ "$(realpath -e -- "$INSTALL_DIR")" == "$INSTALL_DIR" ]] \
  || fail "安装目录不能经过符号链接"
[[ -f "$INSTALL_DIR/docker-compose.yml" ]] || fail "找不到现有 TGCloud 部署"
[[ -f "$INSTALL_DIR/.env" ]] || fail "找不到现有 TGCloud 配置"
command -v docker >/dev/null 2>&1 || fail "Docker 不可用"

cd "$INSTALL_DIR"
docker compose config --quiet

DEPLOY_CLOUDDRIVE2="$(awk -F= '$1=="DEPLOY_CLOUDDRIVE2" {print $2; exit}' .env)"
DEPLOY_CLOUDDRIVE2="${DEPLOY_CLOUDDRIVE2%$'\r'}"

CD2_CONTAINER=""
for target_container in "$BOT_CONTAINER" "$CD2_MANAGED_CONTAINER"; do
  if docker container inspect "$target_container" >/dev/null 2>&1; then
    owner="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$target_container" 2>/dev/null || true)"
    [[ "$owner" == "$INSTALL_DIR" ]] || fail "同名容器不属于当前 TGCloud 目录；停止修复"
  fi
done
if [[ "$(docker inspect --format '{{.State.Running}}' "$CD2_MANAGED_CONTAINER" 2>/dev/null || true)" == true ]]; then
  CD2_CONTAINER="$CD2_MANAGED_CONTAINER"
fi
PORT_19798_CONTAINER="$(
  docker ps --format '{{.Names}}|{{.Ports}}' \
    | awk -F'|' '$2 ~ /:19798->/ {print $1; exit}'
)"
if [[ -n "$PORT_19798_CONTAINER" && "$PORT_19798_CONTAINER" != "$CD2_MANAGED_CONTAINER" ]]; then
  fail "固定端口 19798 被其他容器占用；不会接管或修改该容器"
fi
if [[ -z "$CD2_CONTAINER" && -n "$PORT_19798_CONTAINER" ]]; then
  fail "容器 $PORT_19798_CONTAINER 占用 19798 端口，但无法确认它是 CloudDrive2；拒绝自动修改"
fi
docker network inspect "$DOCKER_NETWORK" >/dev/null 2>&1 \
  || docker network create "$DOCKER_NETWORK" >/dev/null

if [[ -z "$CD2_CONTAINER" ]]; then
  [[ "${DEPLOY_CLOUDDRIVE2:-true}" == "true" ]] \
    || fail "当前配置没有启用由 VPS 管理 CloudDrive2，且没有发现现有容器"
  log "没有发现正在运行的 CloudDrive2，按当前 Compose 配置启动"
  modprobe fuse 2>/dev/null || true
  [[ -c /dev/fuse ]] || fail "VPS 没有提供 /dev/fuse"
  docker compose pull clouddrive2
  docker compose up -d clouddrive2
  CD2_CONTAINER="$CD2_MANAGED_CONTAINER"
fi

docker inspect "$CD2_CONTAINER" >/dev/null 2>&1 \
  || fail "无法检查 CloudDrive2 容器：$CD2_CONTAINER"
[[ "$(docker inspect --format '{{.State.Running}}' "$CD2_CONTAINER")" == "true" ]] \
  || fail "CloudDrive2 容器没有运行：$CD2_CONTAINER"

log "保留现有 CloudDrive2 容器和登录数据：$CD2_CONTAINER"
CD2_NETWORK_MODE="$(
  docker inspect --format '{{.HostConfig.NetworkMode}}' "$CD2_CONTAINER"
)"
CD2_ENDPOINT_HOST="$CD2_MANAGED_CONTAINER"
if [[ "$CD2_NETWORK_MODE" == "host" || "$CD2_NETWORK_MODE" == container:* ]]; then
  log "CloudDrive2 使用 $CD2_NETWORK_MODE 网络；Bot 自动使用相同网络命名空间"
  COMPOSE_TMP="$INSTALL_DIR/.docker-compose.yml.tgcloud-repair"
  awk -v network_mode="$CD2_NETWORK_MODE" '
    /^  tgcloud-clouddrive2-bot:/ { in_bot=1; print; next }
    in_bot && /^networks:/ { in_bot=0 }
    in_bot && /^    (extra_hosts|networks):[[:space:]]*$/ {
      skip_list=1
      next
    }
    skip_list && /^      - / { next }
    skip_list { skip_list=0 }
    in_bot && /^    network_mode:/ { next }
    in_bot && /^    container_name:[[:space:]]*tgcloud-clouddrive2-bot[[:space:]]*$/ {
      print
      print "    network_mode: " network_mode
      next
    }
    { print }
  ' "$INSTALL_DIR/docker-compose.yml" > "$COMPOSE_TMP"
  install -m 600 "$COMPOSE_TMP" "$INSTALL_DIR/docker-compose.yml"
  rm -f -- "$COMPOSE_TMP"

  sed -i 's/\r$//' "$INSTALL_DIR/.env"
  CD2_HOST_URL_B64="$(
    printf '%s' 'http://127.0.0.1:19798/dav' | base64 | tr -d '\n'
  )"
  for url_key in WEBDAV_URL_B64 CD2_WEBDAV_URL_B64; do
    if grep -q "^${url_key}=" "$INSTALL_DIR/.env"; then
      sed -i "s|^${url_key}=.*$|${url_key}=$CD2_HOST_URL_B64|" \
        "$INSTALL_DIR/.env"
    else
      printf '%s=%s\n' "$url_key" "$CD2_HOST_URL_B64" \
        >> "$INSTALL_DIR/.env"
    fi
  done
  docker compose config --quiet
  docker compose up -d --force-recreate "$BOT_SERVICE"
  CD2_ENDPOINT_HOST="127.0.0.1"
else
  if [[ "$(
    docker inspect \
      --format '{{if index .NetworkSettings.Networks "tgcloud-clouddrive2-net"}}yes{{end}}' \
      "$CD2_CONTAINER"
  )" == "yes" ]]; then
    log "刷新现有 $DOCKER_NETWORK 网络连接和 $CD2_MANAGED_CONTAINER 别名"
    docker network disconnect "$DOCKER_NETWORK" "$CD2_CONTAINER"
  fi
  docker network connect --alias "$CD2_MANAGED_CONTAINER" \
    "$DOCKER_NETWORK" "$CD2_CONTAINER"
  docker compose up -d "$BOT_SERVICE"
fi

log "等待 CloudDrive2 管理端口和 Bot 内部 DNS/TCP 连通"
NETWORK_OK="false"
for _ in $(seq 1 45); do
  if curl -fsS --max-time 5 http://127.0.0.1:19798/ >/dev/null 2>&1 \
    && docker exec -e TGCLOUD_CD2_ENDPOINT="$CD2_ENDPOINT_HOST" \
      "$BOT_CONTAINER" python -c \
      'import os,socket; h=os.environ["TGCLOUD_CD2_ENDPOINT"]; socket.getaddrinfo(h,19798); s=socket.create_connection((h,19798),5); s.close()' \
      >/dev/null 2>&1; then
    NETWORK_OK="true"
    break
  fi
  sleep 2
done

if [[ "$NETWORK_OK" != "true" ]]; then
  docker ps -a --filter "name=$CD2_CONTAINER" || true
  docker network inspect "$DOCKER_NETWORK" || true
  fail "Bot 仍无法通过 $CD2_ENDPOINT_HOST:19798 访问 CloudDrive2"
fi

CD2_IP="$(
  docker exec -e TGCLOUD_CD2_ENDPOINT="$CD2_ENDPOINT_HOST" \
    "$BOT_CONTAINER" python -c \
    'import os,socket; print(socket.gethostbyname(os.environ["TGCLOUD_CD2_ENDPOINT"]))'
)"
log "Bot 已解析并连通 CloudDrive2：$CD2_IP:19798"

if [[ "$MODE" != "--network-only" ]]; then
  log "执行真实 WebDAV 写入、校验、改名和清理"
  if ! VERIFY_OUTPUT="$(
    docker compose exec -T "$BOT_SERVICE" python -m app.verify_destination 2>&1
  )"; then
    printf '%s\n' "$VERIFY_OUTPUT"
    if grep -Eqi '401 Unauthorized|Invalid credentials' <<< "$VERIFY_OUTPUT" \
      && recover_webdav_credentials; then
      log "已从 VPS 安全备份恢复可用 WebDAV 凭据；保留当前根目录设置"
      docker compose up -d --force-recreate "$BOT_SERVICE"
      for _ in $(seq 1 30); do
        [[ "$(
          docker inspect --format \
            '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
            "$BOT_CONTAINER" 2>/dev/null || true
        )" == "healthy" ]] && break
        sleep 2
      done
      docker compose exec -T "$BOT_SERVICE" python -m app.verify_destination
    else
      fail "真实 WebDAV 写入验收失败"
    fi
  else
    printf '%s\n' "$VERIFY_OUTPUT"
  fi
fi

PERSISTED_REPAIR="$INSTALL_DIR/repair_clouddrive_network.sh"
if [[ "$(readlink -f -- "$0")" != "$(readlink -f -- "$PERSISTED_REPAIR")" ]]; then
  install -m 700 -- "$0" "$PERSISTED_REPAIR"
  log "Persisted the current repair program to $PERSISTED_REPAIR"
fi

printf 'TGCLOUD_REPAIR_CONTAINER=%s\n' "$CD2_CONTAINER"
printf 'TGCLOUD_REPAIR=SUCCESS\n'
