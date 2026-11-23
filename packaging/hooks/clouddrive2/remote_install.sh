#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SOURCE_DIR="${1:-}"
CONFIG_FILE="${2:-}"
INSTALL_DIR="${INSTALL_DIR:-/opt/tgcloud}"
BACKUP_DIR="/opt/tgcloud-backups"
BOT_SERVICE="tgcloud-clouddrive2-bot"
BOT_CONTAINER="tgcloud-clouddrive2-bot"
BACKUP=""
CONFIG_BACKUP_PENDING=""
DATABASE_BACKUP=""
DATABASE_NAME=""
DATABASE_BACKUP_CANDIDATE=""
ROLLBACK_DATABASE=""
RELEASE_STAGE=""
CANDIDATE_TAG=""
OLD_IMAGE_ID=""
OLD_IMAGE_TAG=""
DB_TEMP_NAME=""
ROLLBACK_ARMED=false
GATEWAY_NETWORK_PENDING=false
RESTART_OLD_BOT=false
RESUME_GO_UPGRADE=false
APPLY_UPGRADE_CONFIG=false

PROGRAM_PATHS=(
  app .dockerignore backup_retention.sh Dockerfile docker-compose.yml manage.sh remote_install.sh
  preserve_runtime_config.sh repair_clouddrive_network.sh requirements.txt tgcloud-source.json
  LICENSE NOTICE README.md THIRD_PARTY_NOTICES.md
)

log() {
  printf '[TGCloud] %s\n' "$*"
}

remove_program_files() {
  local relative
  for relative in "${PROGRAM_PATHS[@]}"; do
    rm -rf -- "${INSTALL_DIR:?}/$relative" || return 1
  done
}

validate_database_backup() {
  python3 - "$1" <<'PY'
import sqlite3
import sys
from contextlib import closing
from pathlib import Path
try:
    path = Path(sys.argv[1])
    if path.is_symlink() or not path.is_file() or path.stat().st_size == 0:
        raise ValueError("invalid snapshot")
    with closing(sqlite3.connect(path.resolve().as_uri() + "?mode=ro&immutable=1", uri=True)) as database:
        if database.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
            raise ValueError("invalid snapshot")
except Exception:
    print("TGCLOUD_DATABASE_CHECK=FAILED", file=sys.stderr)
    sys.exit(1)
PY
}

wait_bot_healthy() {
  local health=""
  for _ in $(seq 1 40); do
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$BOT_CONTAINER" 2>/dev/null || true)"
    [[ "$health" == healthy ]] && return 0
    [[ "$health" != unhealthy && "$health" != exited ]] || break
    sleep 3
  done
  return 1
}

resume_old_bot() {
  if [[ "$(docker inspect --format '{{.State.Running}}' "$BOT_CONTAINER" 2>/dev/null || true)" != true ]]; then
    docker start "$BOT_CONTAINER" >/dev/null || return 1
  fi
  wait_bot_healthy
}

publish_upgrade_backup() {
  local pending="$1" destination="${1%.partial}"
  [[ ! -e "$destination" && ! -L "$destination" ]] && mv -T -- "$pending" "$destination"
}

snapshot_failed() {
  printf 'TGCLOUD_DATABASE_BACKUP=FAILED\n' >&2
  fail "SQLite 一致性快照未通过，已停止升级"
}

rollback_install() {
  ROLLBACK_ARMED=false
  [[ -n "$BACKUP" && -f "$BACKUP" ]] || return 1
  if [[ -n "$DATABASE_BACKUP" ]]; then
    validate_database_backup "$DATABASE_BACKUP" || return 1
    ROLLBACK_DATABASE="$(mktemp "$INSTALL_DIR/data/.rollback-XXXXXXXX.db")" || return 1
    install -m 600 "$DATABASE_BACKUP" "$ROLLBACK_DATABASE" || return 1
    validate_database_backup "$ROLLBACK_DATABASE" || return 1
    chown 10001:10001 "$ROLLBACK_DATABASE" || return 1
  fi
  log "新版本未通过验收，开始恢复升级前的程序、配置和数据库"
  if native_check rollback-upgrade "$INSTALL_DIR" --backup-path "$BACKUP" \
    --restore-db "$ROLLBACK_DATABASE" --old-image-id "$OLD_IMAGE_ID" --old-image-tag "$OLD_IMAGE_TAG"; then
    ROLLBACK_DATABASE=""
    return 0
  elif [[ $? -ne 3 ]]; then
    return 1
  fi
  if [[ -n "$OLD_IMAGE_ID" && -n "$OLD_IMAGE_TAG" ]]; then
    if [[ "$OLD_IMAGE_TAG" == *@sha256:* ]]; then
      [[ "$(docker image inspect --format '{{.Id}}' "$OLD_IMAGE_TAG")" == "$OLD_IMAGE_ID" ]] || return 1
    else
      docker tag "$OLD_IMAGE_ID" "$OLD_IMAGE_TAG" || return 1
    fi
  fi
  if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
    (cd "$INSTALL_DIR" && docker compose rm -sf "$BOT_SERVICE") || return 1
  fi
  remove_program_files || return 1
  tar -xzf "$BACKUP" -C "$INSTALL_DIR" || return 1
  if [[ -n "$DATABASE_BACKUP" ]]; then
    rm -f -- "$INSTALL_DIR/data/$DATABASE_NAME-wal" "$INSTALL_DIR/data/$DATABASE_NAME-shm" \
      "$INSTALL_DIR/data/$DATABASE_NAME-journal" || return 1
    mv -f -- "$ROLLBACK_DATABASE" "$INSTALL_DIR/data/$DATABASE_NAME" || return 1
    ROLLBACK_DATABASE=""
  fi
  cd "$INSTALL_DIR" || return 1
  docker compose up -d --no-deps --force-recreate "$BOT_SERVICE" || return 1
  wait_bot_healthy
}

apply_upgrade_program() {
  if native_check apply-upgrade "$INSTALL_DIR" --stage-path "$RELEASE_STAGE" --apply-config "$APPLY_UPGRADE_CONFIG"; then
    return 0
  elif [[ $? -ne 3 ]]; then
    return 1
  fi
  remove_program_files || return 1
  if [[ "$APPLY_UPGRADE_CONFIG" == false ]]; then
    rm -f -- "$RELEASE_STAGE/.env" || return 1
  fi
  cp -a "$RELEASE_STAGE/." "$INSTALL_DIR/"
}

cleanup_release_stage() {
  [[ -n "$RELEASE_STAGE$CANDIDATE_TAG$CONFIG_BACKUP_PENDING$DATABASE_BACKUP_CANDIDATE$DB_TEMP_NAME$ROLLBACK_DATABASE" ]] || return 0
  if native_check cleanup-install "$INSTALL_DIR" --stage-path "$RELEASE_STAGE" --candidate-tag "$CANDIDATE_TAG" \
    --config-partial "$CONFIG_BACKUP_PENDING" --database-partial "$DATABASE_BACKUP_CANDIDATE" \
    --snapshot-name "$DB_TEMP_NAME" --restore-db "$ROLLBACK_DATABASE"; then
    return 0
  elif [[ $? -ne 3 ]]; then
    return 1
  fi

  local candidates id snapshot suffix
  if [[ "$CANDIDATE_TAG" =~ ^tgcloud-clouddrive2-candidate:[0-9]+-[0-9]+$ ]]; then
    candidates="$(docker container ls --all --quiet --no-trunc --filter "label=com.tgcloud.candidate=$CANDIDATE_TAG")" || candidates=""
    while IFS= read -r id; do
      if [[ "$id" =~ ^[0-9a-f]{64}$ ]]; then
        docker container rm --force "$id" >/dev/null 2>&1 || true
      fi
    done <<< "$candidates"
    docker image rm "$CANDIDATE_TAG" >/dev/null 2>&1 || true
  fi
  if [[ "$ROLLBACK_DATABASE" == "$INSTALL_DIR"/data/.rollback-*.db ]]; then
    rm -f -- "$ROLLBACK_DATABASE" || return 1
  fi
  if [[ "$CONFIG_BACKUP_PENDING" == "$BACKUP_DIR"/config-*.tar.gz.partial ]]; then
    rm -f -- "$CONFIG_BACKUP_PENDING" || return 1
  fi
  if [[ "$DATABASE_BACKUP_CANDIDATE" == "$BACKUP_DIR"/database-*.db.partial ]]; then
    rm -f -- "$DATABASE_BACKUP_CANDIDATE" || return 1
  fi
  if [[ "$RELEASE_STAGE" =~ ^/opt/tgcloud-release-[A-Za-z0-9]{8}$ && "$INSTALL_DIR/" != "$RELEASE_STAGE/"* ]]; then
    rm -rf --one-file-system -- "$INSTALL_DIR/.upgrade-work-${RELEASE_STAGE##*-}" "$RELEASE_STAGE" || return 1
  fi
  if [[ "$DB_TEMP_NAME" =~ ^\.upgrade-backup-[0-9]+\.db$ ]]; then
    rm -f -- "$INSTALL_DIR/data/$DB_TEMP_NAME"{,-journal,-wal,-shm} || return 1
    for snapshot in "$INSTALL_DIR/data/.$DB_TEMP_NAME.tmp-"*; do
      suffix="${snapshot##*.tmp-}"
      if [[ "$suffix" =~ ^[0-9]+(-(journal|wal|shm))?$ ]]; then
        rm -f -- "$snapshot" || return 1
      fi
    done
  fi
}

fail() {
  trap - ERR
  trap '' INT TERM HUP
  local message="$*"
  if [[ "$GATEWAY_NETWORK_PENDING" == true && -f "$RELEASE_STAGE/.gateway-network" ]]; then
    native_check restore-gateway-network "$INSTALL_DIR" --stage-path "$RELEASE_STAGE" \
      || message+=" CloudDrive2 网络恢复未完成，请核对网关连接。"
  fi
  if [[ "$ROLLBACK_ARMED" == true ]]; then
    if rollback_install; then
      message+=" 已自动恢复升级前版本。"
    else
      message+=" 自动恢复未完成，请保留备份并人工处理。"
    fi
  elif [[ "$RESUME_GO_UPGRADE" == true ]]; then
    native_check resume-upgrade "$INSTALL_DIR" --stage-path "$RELEASE_STAGE" \
      || message+=" 原 Bot 未恢复健康，请检查容器日志。现有程序、配置和数据库保留。"
  elif [[ "$RESTART_OLD_BOT" == true ]]; then
    if resume_old_bot; then
      message+=" 已恢复原 Bot，现有程序、配置和数据库保留。"
    else
      message+=" 原 Bot 未恢复健康，请检查容器日志。现有程序、配置和数据库保留。"
    fi
  fi
  printf '[TGCloud][ERROR] %s\n' "$message" >&2
  printf 'TGCLOUD_RESULT=FAILED\n' >&2
  exit 1
}

cleanup_on_exit() {
  trap '' INT TERM HUP
  cleanup_release_stage || log "警告：部分安装暂存资源未能清理，请核对上方日志"
  exit "$1"
}

trap 'fail "安装在第 ${LINENO} 行失败。请保留完整日志。"' ERR
trap 'fail "安装被中断。请保留完整日志。"' INT TERM HUP
trap 'cleanup_on_exit "$?"' EXIT

[[ "$(id -u)" -eq 0 ]] || fail "remote_install.sh 必须以 root 或 sudo 运行"
[[ -d "$SOURCE_DIR" ]] || fail "找不到部署源目录：$SOURCE_DIR"
[[ -f "$CONFIG_FILE" ]] || fail "找不到配置文件：$CONFIG_FILE"
[[ "$INSTALL_DIR" =~ ^/opt/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$ ]] \
  || fail "安装目录必须是 /opt/ 下的安全绝对路径"
[[ "/$INSTALL_DIR/" != *"/../"* && "/$INSTALL_DIR/" != *"/./"* ]] \
  || fail "安装目录不能包含 . 或 .. 路径段"
[[ "$(realpath -m -- "$INSTALL_DIR")" == "$INSTALL_DIR" ]] \
  || fail "安装目录不能经过符号链接"
SOURCE_DIR="$(realpath -e -- "$SOURCE_DIR")"
CONFIG_FILE="$(realpath -e -- "$CONFIG_FILE")"
native_check() (
  # Resolve Python modules from the verified payload, never the caller's cwd.
  cd -- "$SOURCE_DIR"
  PYTHONPATH="$SOURCE_DIR/app" python3 -B -m tgcloud.deployment.architecture "$@"
)
# Refuse unknown or retired installs before reading private configuration or changing the host.
PYTHONPATH="$SOURCE_DIR/app" python3 -B -m tgcloud.deployment.local_source "$SOURCE_DIR" --allow-pending \
  || fail "部署候选来源无效"
if [[ -f "$INSTALL_DIR/docker-compose.yml" || -f "$INSTALL_DIR/.env" ]]; then
  PYTHONPATH="$SOURCE_DIR/app" python3 -B -m tgcloud.deployment.local_source "$INSTALL_DIR" \
    || fail "现有目录不属于本地 CloudDrive2 项目；保留不动"
fi
native_check host >/dev/null || fail "需要原生 64 位 Debian/Ubuntu AMD64 或 ARM64"
native_check docker --allow-missing || fail "本机 Docker 平台检查失败"
[[ -f "$SOURCE_DIR/preserve_runtime_config.sh" ]] \
  || fail "部署包缺少现有配置保护脚本"
# shellcheck source=packaging/hooks/clouddrive2/preserve_runtime_config.sh
# shellcheck disable=SC1090
source "$SOURCE_DIR/preserve_runtime_config.sh"
[[ "$SOURCE_DIR/" != "$INSTALL_DIR/"* && "$INSTALL_DIR/" != "$SOURCE_DIR/"* ]] \
  || fail "部署源目录和安装目录不能重叠"
[[ "$CONFIG_FILE" != "$INSTALL_DIR/"* ]] || fail "请将输入配置放在安装目录之外"
export INSTALL_DIR

if [[ -d "$INSTALL_DIR" && ! -f "$INSTALL_DIR/docker-compose.yml" ]] \
  && [[ -n "$(find "$INSTALL_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  fail "目标安装目录已有非 TGCloud 文件：$INSTALL_DIR；不会覆盖，请检查后重新检测"
fi
if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
  if ! grep -Fq "container_name: $BOT_CONTAINER" "$INSTALL_DIR/docker-compose.yml" \
    || ! grep -Fq 'container_name: tgcloud-clouddrive2' "$INSTALL_DIR/docker-compose.yml"; then
    fail "目标安装目录不是可识别的 TGCloud CloudDrive2 部署；不会覆盖，请检查后重新检测"
  fi
fi
install_probe_path() {
  local path="$INSTALL_DIR" parent
  while [[ ! -e "$path" ]]; do
    parent="$(dirname -- "$path")"
    [[ "$parent" != "$path" ]] || break
    path="$parent"
  done
  [[ -e "$path" ]] || path=/
  printf '%s\n' "$path"
}

if native_check check-install-resources "$INSTALL_DIR"; then
  :
elif [[ $? -eq 3 ]]; then
  TOTAL_MEM_MB="$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)"
  CPU_CORES="$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc)"
  INSTALL_PROBE_PATH="$(install_probe_path)"
  INSTALL_TOTAL_KB="$(df -Pk -- "$INSTALL_PROBE_PATH" | awk 'NR==2 {print $2}')"
  INSTALL_FREE_KB="$(df -Pk -- "$INSTALL_PROBE_PATH" | awk 'NR==2 {print $4}')"
  log "检测到 ${CPU_CORES} 核 CPU、约 ${TOTAL_MEM_MB}MB 内存、安装文件系统可用约 $((INSTALL_FREE_KB / 1024))MB"
  [[ "$TOTAL_MEM_MB" -ge 1800 ]] || fail "内存不足 2GB，当前约 ${TOTAL_MEM_MB}MB"
  [[ "$INSTALL_TOTAL_KB" -ge 45000000 ]] || log "警告：安装文件系统总容量低于推荐的 50GB"
  [[ "$INSTALL_FREE_KB" -ge 8000000 ]] || fail "安装文件系统可用空间不足 8GB，无法安全安装"
else
  fail "安装前资源检查未通过，已停止部署"
fi

if CONFIG_MODE="$(native_check prepare-install-config "$INSTALL_DIR" --config-path "$CONFIG_FILE")"; then
  case "$CONFIG_MODE" in
    fresh) ;;
    preserved) printf 'TGCLOUD_CONFIG=PRESERVED\n' ;;
    apply) APPLY_UPGRADE_CONFIG=true; printf 'TGCLOUD_CONFIG=APPLY_REQUESTED\n' ;;
    *) fail "无法确认部署配置使用方式，已停止部署" ;;
  esac
elif [[ $? -eq 3 ]]; then
  sed -i 's/\r$//' "$CONFIG_FILE"
  if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
    if grep -Fxq 'TGCLOUD_REDEPLOY_APPLY_CONFIG=true' "$CONFIG_FILE"; then
      APPLY_UPGRADE_CONFIG=true
    fi
    tgcloud_prepare_redeploy_config "$CONFIG_FILE" "$INSTALL_DIR/.env" \
      || fail "无法安全保留现有 TGCloud 配置"
  fi
else
  fail "无法准备部署配置，尚未停止原 Bot 或替换程序文件"
fi

DEPENDENCY_MODE=install
[[ ! -f "$INSTALL_DIR/docker-compose.yml" ]] || DEPENDENCY_MODE=upgrade
if native_check prepare-dependencies "$DEPENDENCY_MODE"; then
  :
elif [[ $? -eq 3 ]]; then
  # The older helper delegates host preparation to the prior Shell path.
  export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l
  install_missing_packages() {
    local plan
    local args=(-o DPkg::Lock::Timeout=120 --no-install-recommends --no-upgrade --no-remove install "$@")
    plan="$(LC_ALL=C apt-get -s "${args[@]}")" || fail "无法核实 APT 安装计划"
    if ! awk '
      $1 == "Remv" || $1 == "Purg" {bad=1}
      $1 == "Inst" {if (substr($3,1,1) != "(") bad=1; else added[$2]=1}
      $1 == "Conf" && !added[$2] {bad=1}
      END {exit bad}
    ' <<< "$plan"; then
      fail "APT 计划涉及修改已安装的软件包，已停止安装依赖"
    fi
    apt-get -y "${args[@]}"
  }

  configure_docker_repository() {
    local suite architecture targets uri
    # shellcheck disable=SC1091
    source /etc/os-release
    case "${ID:-}" in
      ubuntu) suite="${UBUNTU_CODENAME:-${VERSION_CODENAME:-}}" ;;
      debian) suite="${VERSION_CODENAME:-}" ;;
      *) fail "仅支持 Debian 或 Ubuntu 软件源" ;;
    esac
    [[ "$suite" =~ ^[a-z][a-z0-9-]{0,30}$ ]] || fail "无法核实系统软件源代号"
    uri="https://download.docker.com/linux/$ID"
    architecture="$(dpkg --print-architecture)"
    apt-get -o DPkg::Lock::Timeout=120 -o APT::Update::Error-Mode=any update
    targets="$(apt-get indextargets --format "\$(URI)")"
    if [[ "$targets" == *"$uri/dists/$suite/stable/binary-$architecture/Packages"* ]]; then
      return 0
    fi
    [[ ! -e /etc/apt/sources.list.d/docker.list && ! -L /etc/apt/sources.list.d/docker.list &&
       ! -e /etc/apt/keyrings/docker.asc && ! -L /etc/apt/keyrings/docker.asc ]] || fail "现有 Docker APT 配置需要人工核对"
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL --proto '=https' --proto-redir '=https' "$uri/gpg" -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
    printf '%s\n' \
      "deb [arch=$architecture signed-by=/etc/apt/keyrings/docker.asc] $uri $suite stable" \
      > /etc/apt/sources.list.d/docker.list
    apt-get -o DPkg::Lock::Timeout=120 -o APT::Update::Error-Mode=any update
  }

  HAD_DOCKER=false
  if command -v docker >/dev/null 2>&1; then
    HAD_DOCKER=true
    native_check docker || fail "请先恢复现有 Docker 服务"
  elif [[ "$DEPENDENCY_MODE" == upgrade ]] || command -v dockerd >/dev/null 2>&1 || [[ -e /var/run/docker.sock ]]; then
    fail "请先恢复现有 Docker CLI"
  fi
  if [[ "$DEPENDENCY_MODE" == upgrade ]] && ! docker compose version >/dev/null 2>&1; then
    fail "升级要求现有 Docker Compose 可用，请先恢复 Compose"
  fi

  MISSING_PACKAGES=()
  for package in ca-certificates curl gnupg tar gzip fuse3 kmod openssh-client iproute2 python3; do
    if [[ "$(LC_ALL=C dpkg-query -W -f='${db:Status-Status}' "$package" 2>/dev/null || true)" != installed ]]; then
      MISSING_PACKAGES+=("$package")
    fi
  done
  if (( ${#MISSING_PACKAGES[@]} != 0 )); then
    log "安装缺少的基础软件"
    apt-get -o DPkg::Lock::Timeout=120 -o APT::Update::Error-Mode=any update
    install_missing_packages "${MISSING_PACKAGES[@]}"
  fi
  if [[ "$HAD_DOCKER" == false ]]; then
    log "通过 Docker 官方 APT 仓库安装 Docker"
    configure_docker_repository
    install_missing_packages docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    systemctl enable --now docker
  elif ! docker compose version >/dev/null 2>&1; then
    log "安装缺少的 Docker Compose 插件"
    configure_docker_repository
    install_missing_packages docker-compose-plugin
  fi
  docker compose version >/dev/null 2>&1 || fail "Docker Compose 不可用"
  native_check docker || fail "Docker daemon 必须与宿主原生平台一致"
else
  fail "宿主依赖准备失败，请核对上方日志"
fi

if native_check check-install-host "$INSTALL_DIR" --config-path "$CONFIG_FILE"; then
  :
elif [[ $? -eq 3 ]]; then
  for target_container in "$BOT_CONTAINER" tgcloud-clouddrive2; do
    if docker container inspect "$target_container" >/dev/null 2>&1; then
      owner="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$target_container" 2>/dev/null || true)"
      [[ "$owner" == "$INSTALL_DIR" && -f "$INSTALL_DIR/docker-compose.yml" ]] \
        || fail "目标容器名 $target_container 已被其他部署使用；不会覆盖，请检查后重新检测"
    fi
  done

  PORT_19798_OWNER="$(docker ps --format '{{.Names}}|{{.Ports}}' \
    | awk -F'|' '$2 ~ /:19798->/ {print $1; exit}')"
  if [[ -n "$PORT_19798_OWNER" && "$PORT_19798_OWNER" != tgcloud-clouddrive2 ]]; then
    fail "固定端口 19798 被其他容器占用；不会接管或修改该容器"
  fi
  if [[ -z "$PORT_19798_OWNER" ]]; then
    if command -v ss >/dev/null 2>&1; then
      [[ -z "$(ss -H -ltn '( sport = :19798 )')" ]] \
        || fail "固定端口 19798 已被其他进程占用；请自行处理后重新检测"
    elif timeout 2 bash -c 'echo >/dev/tcp/127.0.0.1/19798' >/dev/null 2>&1; then
      fail "固定端口 19798 已被其他进程占用；请自行处理后重新检测"
    fi
  fi

  DOCKER_ROOT_DIR="$(docker info --format '{{.DockerRootDir}}')" \
    || fail "无法读取 Docker 数据目录"
  [[ "$DOCKER_ROOT_DIR" == /* && "$DOCKER_ROOT_DIR" != *$'\n'* && "$DOCKER_ROOT_DIR" != *$'\r'* ]] \
    || fail "Docker 数据目录不是安全的绝对路径"
  DOCKER_PROBE_PATH="$DOCKER_ROOT_DIR"
  while [[ ! -e "$DOCKER_PROBE_PATH" ]]; do
    PARENT_PATH="$(dirname -- "$DOCKER_PROBE_PATH")"
    [[ "$PARENT_PATH" != "$DOCKER_PROBE_PATH" ]] || break
    DOCKER_PROBE_PATH="$PARENT_PATH"
  done
  [[ -e "$DOCKER_PROBE_PATH" ]] || DOCKER_PROBE_PATH=/
  INSTALL_PROBE_PATH="$(install_probe_path)"
  INSTALL_FREE_KB="$(df -Pk -- "$INSTALL_PROBE_PATH" | awk 'NR==2 {print $4}')"
  [[ "$INSTALL_FREE_KB" -ge 8000000 ]] \
    || fail "安装 Docker 后安装文件系统可用空间不足 8GB，停止部署"
  if [[ "$(stat -c %d -- "$INSTALL_PROBE_PATH")" != "$(stat -c %d -- "$DOCKER_PROBE_PATH")" ]]; then
    DOCKER_FREE_KB="$(df -Pk -- "$DOCKER_PROBE_PATH" | awk 'NR==2 {print $4}')"
    DEPLOY_CLOUDDRIVE2_REQUESTED="$(
      awk -F= '$1=="DEPLOY_CLOUDDRIVE2" {print $2; exit}' "$CONFIG_FILE"
    )"
    DEPLOY_CLOUDDRIVE2_REQUESTED="${DEPLOY_CLOUDDRIVE2_REQUESTED%$'\r'}"
    if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
      DOCKER_MIN_FREE_KB=2000000
      [[ -n "$DEPLOY_CLOUDDRIVE2_REQUESTED" && "$DEPLOY_CLOUDDRIVE2_REQUESTED" != true ]] \
        || DOCKER_MIN_FREE_KB=3000000
    else
      DOCKER_MIN_FREE_KB=4000000
      [[ -n "$DEPLOY_CLOUDDRIVE2_REQUESTED" && "$DEPLOY_CLOUDDRIVE2_REQUESTED" != true ]] \
        || DOCKER_MIN_FREE_KB=6000000
    fi
    log "Docker 数据目录位于另一文件系统，可用约 $((DOCKER_FREE_KB / 1024))MB"
    [[ "$DOCKER_FREE_KB" -ge "$DOCKER_MIN_FREE_KB" ]] \
      || fail "Docker 数据文件系统空间不足，无法安全构建和启动容器"
  fi
else
  fail "宿主复核未通过，已停止部署"
fi

log "准备持久化目录和候选配置"
GO_FRESH_INSTALL=false
GO_INSTALL_LAYOUT=false
if [[ "$DEPENDENCY_MODE" == install ]]; then
  if RELEASE_STAGE="$(native_check fresh-install "$INSTALL_DIR" --source-path "$SOURCE_DIR" --config-path "$CONFIG_FILE")"; then
    GO_FRESH_INSTALL=true
  elif [[ $? -ne 3 ]]; then
    fail "新装部署包暂存失败，已停止部署"
  fi
elif RELEASE_STAGE="$(native_check stage-upgrade "$INSTALL_DIR" --source-path "$SOURCE_DIR")"; then
  :
elif [[ $? -ne 3 ]]; then
  fail "升级部署包暂存失败，尚未停止原 Bot 或替换程序文件"
fi
if [[ "$GO_FRESH_INSTALL" == false ]]; then
  if [[ -z "$RELEASE_STAGE" ]]; then
    RELEASE_STAGE="$(mktemp -d /opt/tgcloud-release-XXXXXXXX)"
    cp -a "$SOURCE_DIR/." "$RELEASE_STAGE/"
  fi
  if native_check install-layout "$INSTALL_DIR" --stage-path "$RELEASE_STAGE" --config-path "$CONFIG_FILE"; then
    GO_INSTALL_LAYOUT=true
  elif [[ $? -eq 3 ]]; then
    mkdir -p \
      "$INSTALL_DIR" \
      "$INSTALL_DIR/data" \
      "$INSTALL_DIR/downloads" \
      "$INSTALL_DIR/logs" \
      "$INSTALL_DIR/config/rclone" \
      "$INSTALL_DIR/clouddrive/config" \
      "$INSTALL_DIR/clouddrive/mounts"
    install -d -m 700 "$BACKUP_DIR"
  else
    fail "无法准备持久化目录或候选配置"
  fi
fi
CANDIDATE_TAG="tgcloud-clouddrive2-candidate:$(date +%s)-$$"

log "在隔离目录预检新程序和配置"
if [[ "$GO_FRESH_INSTALL" == false && "$GO_INSTALL_LAYOUT" == false ]]; then
  install -m 600 "$CONFIG_FILE" "$RELEASE_STAGE/.env"
  sed -i 's/\r$//' "$RELEASE_STAGE/.env"
fi
if native_check preflight-candidate "$RELEASE_STAGE"; then
  :
elif [[ $? -eq 3 ]]; then
  (cd "$RELEASE_STAGE" && docker compose config --quiet)
  native_check prepare "$RELEASE_STAGE/docker-compose.yml" --service clouddrive2 --standalone
else
  fail "候选 Compose 或镜像预检失败，尚未停止原 Bot 或替换程序文件"
fi
native_check build-candidate "$RELEASE_STAGE" --candidate-tag "$CANDIDATE_TAG"

if [[ "$GO_FRESH_INSTALL" == true && -f "$INSTALL_DIR/docker-compose.yml" ]]; then
  fail "新装目标目录已有部署文件，请核对现有内容"
fi
if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then
  CONFIG_BACKUP_PENDING="$(mktemp "$BACKUP_DIR/config-XXXXXXXX.tar.gz.partial")" \
    || fail "无法创建配置备份暂存文件，请检查磁盘和权限"
  BACKUP="${CONFIG_BACKUP_PENDING%.partial}"
  log "备份现有程序配置到 $BACKUP"
  if native_check commit-upgrade-backup "$INSTALL_DIR" --backup-path "$CONFIG_BACKUP_PENDING"; then
    :
  elif [[ $? -eq 3 ]]; then
    if native_check backup-config "$INSTALL_DIR" --backup-path "$CONFIG_BACKUP_PENDING"; then
      :
    elif [[ $? -eq 3 ]]; then
      tar -czf "$CONFIG_BACKUP_PENDING" -C "$INSTALL_DIR" \
        --exclude='./data' --exclude='./downloads' --exclude='./logs' --exclude='./clouddrive' \
        . || fail "配置备份失败，尚未替换程序文件；请先检查磁盘和权限"
      tar -tzf "$CONFIG_BACKUP_PENDING" >/dev/null || fail "配置备份无法读取，停止升级"
    else
      fail "配置备份失败，尚未替换程序文件；请先检查磁盘和权限"
    fi
    publish_upgrade_backup "$CONFIG_BACKUP_PENDING" || fail "无法保存已校验的配置备份"
  else
    fail "配置备份失败，尚未替换程序文件；请先检查磁盘和权限"
  fi
  RESUME_GO_UPGRADE=true
  if UPGRADE_IMAGE="$(native_check quiesce-upgrade "$INSTALL_DIR" --stage-path "$RELEASE_STAGE")"; then
    IFS='|' read -r OLD_IMAGE_ID OLD_IMAGE_TAG <<< "$UPGRADE_IMAGE"
  elif [[ $? -eq 3 ]]; then
    RESUME_GO_UPGRADE=false
    OLD_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$BOT_CONTAINER" 2>/dev/null || true)"
    if [[ -n "$OLD_IMAGE_ID" ]]; then
      OLD_IMAGE_TAG="$(docker inspect --format '{{.Config.Image}}' "$BOT_CONTAINER" 2>/dev/null || true)"
      [[ "$OLD_IMAGE_TAG" != "<no value>" ]] || OLD_IMAGE_TAG=""
      BOT_WAS_RUNNING="$(docker inspect --format '{{.State.Running}}' "$BOT_CONTAINER")" \
        || fail "无法核实旧 Bot 的运行状态，停止升级"
      if [[ "$BOT_WAS_RUNNING" == true ]]; then
        RESTART_OLD_BOT=true
        docker stop --time 30 "$BOT_CONTAINER" >/dev/null || fail "停止旧 Bot 失败，停止升级"
      elif [[ "$BOT_WAS_RUNNING" != false ]]; then
        fail "无法核实旧 Bot 的运行状态，停止升级"
      fi
    fi
  else
    fail "升级前旧 Bot 停机准备失败，程序文件尚未替换"
  fi
  DATABASE_NAME="$(python3 -I -S -B "$RELEASE_STAGE/app/tgcloud/persistence/paths.py" "$INSTALL_DIR/data")" \
    || snapshot_failed
  if [[ -f "$INSTALL_DIR/data/$DATABASE_NAME" ]]; then
    DATABASE_BACKUP_CANDIDATE="$(mktemp "$BACKUP_DIR/database-XXXXXXXX.db.partial")" || snapshot_failed
    DB_TEMP_NAME=".upgrade-backup-$$.db"
    log "创建并校验升级前数据库一致性快照"
    if native_check snapshot-database "$INSTALL_DIR" --candidate-tag "$CANDIDATE_TAG" \
      --snapshot-name "$DB_TEMP_NAME" --backup-path "$DATABASE_BACKUP_CANDIDATE"; then
      :
    elif [[ $? -eq 3 ]]; then
      docker run --rm --label "com.tgcloud.candidate=$CANDIDATE_TAG" --read-only --tmpfs /tmp --user 10001:10001 \
        --volume "$INSTALL_DIR/data:/data" --entrypoint python "$CANDIDATE_TAG" \
        -m app.backup_database "/data/$DATABASE_NAME" "/data/$DB_TEMP_NAME" >/dev/null || snapshot_failed
      install -m 600 "$INSTALL_DIR/data/$DB_TEMP_NAME" "$DATABASE_BACKUP_CANDIDATE" || snapshot_failed
    else
      snapshot_failed
    fi
    if native_check commit-upgrade-backup "$INSTALL_DIR" --backup-path "$DATABASE_BACKUP_CANDIDATE"; then
      :
    elif [[ $? -eq 3 ]]; then
      validate_database_backup "$DATABASE_BACKUP_CANDIDATE" || snapshot_failed
      publish_upgrade_backup "$DATABASE_BACKUP_CANDIDATE" || snapshot_failed
    else
      snapshot_failed
    fi
    DATABASE_BACKUP="${DATABASE_BACKUP_CANDIDATE%.partial}"
    printf 'TGCLOUD_DATABASE_BACKUP=OK\n'
    rm -f -- "$INSTALL_DIR/data/$DB_TEMP_NAME"
  fi
  ROLLBACK_ARMED=true
  RESTART_OLD_BOT=false
  RESUME_GO_UPGRADE=false
fi

log "提交已预检的程序和配置"
if [[ "$ROLLBACK_ARMED" == true ]]; then
  apply_upgrade_program || fail "升级程序文件替换失败"
elif [[ "$GO_FRESH_INSTALL" == true ]]; then
  native_check fresh-install "$INSTALL_DIR" --stage-path "$RELEASE_STAGE" || fail "新装程序发布失败，已停止部署"
else
  remove_program_files
  if [[ "$GO_INSTALL_LAYOUT" == true ]]; then
    find "$RELEASE_STAGE" -mindepth 1 -maxdepth 1 ! -name .env -exec cp -a -t "$INSTALL_DIR" -- {} +
  else
    cp -a "$RELEASE_STAGE/." "$INSTALL_DIR/"
  fi
fi
if [[ "$GO_FRESH_INSTALL" == false ]]; then
  if [[ "$GO_INSTALL_LAYOUT" == true ]]; then
    INITIAL_CONFIG=()
    if [[ "$ROLLBACK_ARMED" == false ]]; then
      INITIAL_CONFIG=(--stage-path "$RELEASE_STAGE")
    fi
    native_check install-layout "$INSTALL_DIR" "${INITIAL_CONFIG[@]}" || fail "无法设置持久化权限或写入初始配置"
  else
    chmod 700 \
      "$INSTALL_DIR" \
      "$INSTALL_DIR/data" \
      "$INSTALL_DIR/downloads" \
      "$INSTALL_DIR/logs" \
      "$INSTALL_DIR/config" \
      "$INSTALL_DIR/clouddrive" \
      "$INSTALL_DIR/clouddrive/config" \
      "$INSTALL_DIR/clouddrive/mounts" \
      "$BACKUP_DIR"
    chown -R 10001:10001 \
      "$INSTALL_DIR/data" \
      "$INSTALL_DIR/downloads" \
      "$INSTALL_DIR/logs" \
      "$INSTALL_DIR/config"
    chmod +x \
      "$INSTALL_DIR/remote_install.sh" \
      "$INSTALL_DIR/manage.sh" \
      "$INSTALL_DIR/repair_clouddrive_network.sh"
  fi
fi

cd "$INSTALL_DIR"
if DEPLOY_CLOUDDRIVE2="$(native_check check-install-compose "$INSTALL_DIR")"; then
  [[ "$DEPLOY_CLOUDDRIVE2" == true || "$DEPLOY_CLOUDDRIVE2" == false ]] || fail "无法核实正式部署的 CloudDrive2 配置"
elif [[ $? -eq 3 ]]; then
  docker compose config --quiet
  DEPLOY_CLOUDDRIVE2="$(awk -F= '$1=="DEPLOY_CLOUDDRIVE2" {print $2; exit}' .env)"
  DEPLOY_CLOUDDRIVE2="${DEPLOY_CLOUDDRIVE2%$'\r'}"
  [[ -z "$DEPLOY_CLOUDDRIVE2" || "$DEPLOY_CLOUDDRIVE2" == true || "$DEPLOY_CLOUDDRIVE2" == false ]] \
    || fail "DEPLOY_CLOUDDRIVE2 必须为 true 或 false"
else
  fail "正式 Compose 配置检查失败，请核对升级回滚结果"
fi
if [[ "${DEPLOY_CLOUDDRIVE2:-true}" == "true" ]]; then
  if native_check start-gateway "$INSTALL_DIR"; then
    :
  elif [[ $? -eq 3 ]]; then
    modprobe fuse 2>/dev/null || true
    [[ -c /dev/fuse ]] || fail "VPS 没有提供 /dev/fuse；请让服务商开启 FUSE 后重试"
    EXISTING_CD2=""
    if [[ "$(docker inspect --format '{{.State.Running}}' tgcloud-clouddrive2 2>/dev/null || true)" == true ]]; then
      EXISTING_CD2=tgcloud-clouddrive2
    fi
    PORT_19798_CONTAINER="$(
      docker ps --format '{{.Names}}|{{.Ports}}' \
        | awk -F'|' '$2 ~ /:19798->/ {print $1; exit}'
    )"
    if [[ -z "$EXISTING_CD2" && -n "$PORT_19798_CONTAINER" ]]; then
      fail "容器 $PORT_19798_CONTAINER 占用 19798 端口，但无法确认它是 CloudDrive2；拒绝自动修改"
    fi
    if [[ -n "$EXISTING_CD2" ]]; then
      log "发现现有 CloudDrive2 容器 $EXISTING_CD2；保留登录和挂载数据"
    else
      printf 'TGCLOUD_STAGE=GATEWAY\n'
      log "使用已预检的镜像启动 CloudDrive2"
      docker compose up -d --pull never clouddrive2
    fi
  else
    fail "CloudDrive2 容器启动或复用失败，请核对上方日志"
  fi
fi

if native_check deploy-bot "$INSTALL_DIR"; then
  BOT_HEALTH=healthy
elif [[ $? -eq 3 ]]; then
  printf 'TGCLOUD_STAGE=BOT\n'
  log "构建 TGCloud Bot 容器"
  BUILDX_BUILDER=default docker compose build "$BOT_SERVICE"
  bash "$INSTALL_DIR/manage.sh" validate
  docker compose up -d --no-deps "$BOT_SERVICE"

  printf 'TGCLOUD_STAGE=HEALTH\n'
  log "等待 Bot 基础健康检查"
  BOT_HEALTH=""
  for _ in $(seq 1 40); do
    BOT_HEALTH="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$BOT_CONTAINER" 2>/dev/null || true)"
    if [[ "$BOT_HEALTH" == healthy || "$BOT_HEALTH" == unhealthy || "$BOT_HEALTH" == exited ]]; then
      break
    fi
    sleep 3
  done

  if [[ "$BOT_HEALTH" != healthy ]]; then
    log "Bot 当前状态：${BOT_HEALTH:-unknown}"
    docker compose ps || true
    docker compose logs --tail=120 "$BOT_SERVICE" || true
    fail "Bot 未通过基础健康检查，请根据上面的日志修正配置"
  fi
else
  fail "正式 Bot 部署失败，请核对上方日志"
fi

if [[ "${DEPLOY_CLOUDDRIVE2:-true}" == "true" ]]; then
  log "迁移或修复 CloudDrive2 Docker 网络并执行硬性连通检查"
  GATEWAY_NETWORK_PENDING=true
  if native_check repair-gateway-network "$INSTALL_DIR" --stage-path "$RELEASE_STAGE"; then
    :
  elif [[ $? -eq 3 ]]; then
    export INSTALL_DIR
    bash "$INSTALL_DIR/repair_clouddrive_network.sh" --network-only
  else
    fail "CloudDrive2 网络修复或连通检查失败，请核对上方日志"
  fi
fi

log "核对运行中的环境配置、代码指纹和基础健康状态"
ACCEPT_GATEWAY=()
if [[ "${DEPLOY_CLOUDDRIVE2:-true}" == "true" ]]; then
  ACCEPT_GATEWAY=(--check-gateway)
fi
if native_check accept-bot "$INSTALL_DIR" "${ACCEPT_GATEWAY[@]}"; then
  :
elif [[ $? -eq 3 ]]; then
  bash "$INSTALL_DIR/manage.sh" ready
  native_check container "$BOT_CONTAINER"
  if [[ "${DEPLOY_CLOUDDRIVE2:-true}" == "true" ]]; then
    native_check container tgcloud-clouddrive2
  fi
  PYTHONPATH="$INSTALL_DIR/app" python3 -B -m tgcloud.deployment.local_source "$INSTALL_DIR" --activate \
    || fail "已安装程序来源核验失败；保留现场并回退"
else
  fail "Bot 验收或安装激活失败，请核对上方日志"
fi
GATEWAY_NETWORK_PENDING=false
ROLLBACK_ARMED=false
if native_check report-install "$INSTALL_DIR"; then
  :
elif [[ $? -eq 3 ]]; then
  # shellcheck disable=SC1091
  source "$INSTALL_DIR/backup_retention.sh"
  if BACKUP_INVENTORY="$(tgcloud_backup_inventory "$BACKUP_DIR")"; then
    BACKUP_COUNT="$(awk -F= '$1=="TOTAL_COUNT" {print $2}' <<< "$BACKUP_INVENTORY")"
    BACKUP_BYTES="$(awk -F= '$1=="TOTAL_BYTES" {print $2}' <<< "$BACKUP_INVENTORY")"
    log "当前保留 ${BACKUP_COUNT:-0} 份升级／配置备份，占用约 $(( ${BACKUP_BYTES:-0} / 1024 / 1024 ))MB"
    if [[ "${BACKUP_BYTES:-0}" =~ ^[0-9]+$ ]] && ((BACKUP_BYTES > 5368709120)); then
      log "警告：历史备份已超过 5GB；请先运行 manage.sh backups 核对，再按需手动 prune-backups"
    fi
  else
    log "警告：无法统计历史备份；部署已经通过验收，请稍后运行 manage.sh backups 检查"
  fi
  log "部署完成"
  docker compose ps
else
  fail "安装已通过验收，但无法生成部署摘要，请核对当前容器状态"
fi
printf 'TGCLOUD_INSTALL_DIR=%s\n' "$INSTALL_DIR"
printf 'TGCLOUD_BOT_HEALTH=%s\n' "$BOT_HEALTH"
printf 'TGCLOUD_RESULT=SUCCESS\n'
