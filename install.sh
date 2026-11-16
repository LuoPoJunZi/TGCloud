#!/usr/bin/env bash
# Linux install/upgrade entry. Windows deployers are frozen at v1.2.1.
set -Eeuo pipefail
umask 077

if ! command -v python3 >/dev/null 2>&1; then
  printf '需要 Python 3.10+。Debian/Ubuntu 可先手动执行：apt-get update && apt-get install -y python3 ca-certificates\n' >&2
  exit 2
fi
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 2)' || {
  printf '需要 Python 3.10 或更高版本；未安装任何依赖。\n' >&2
  exit 2
}

script_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ -f "$script_root/src/tgcloud/cli/commands.py" && -f "$script_root/scripts/run_source.py" ]]; then
  exec python3 -B "$script_root/scripts/run_source.py" install "$@"
fi
printf '需要本地精简版完整源码。请在源码目录运行此脚本；此入口不会下载旧的双版本项目。\n' >&2
exit 2
