#!/usr/bin/env bash
# LAN install/management entry. Shared Python services own all operations.
set -Eeuo pipefail
umask 077

command -v python3 >/dev/null 2>&1 || {
  printf '需要 Python 3.10+；未安装任何依赖。\n' >&2
  exit 2
}
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 2)' || {
  printf '需要 Python 3.10 或更高版本。\n' >&2
  exit 2
}
script_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ -f "$script_root/src/tgcloud/cli/commands.py" && -f "$script_root/scripts/run_source.py" ]]; then
  exec python3 -B "$script_root/scripts/run_source.py" lan "$@"
fi
printf '需要完整的 TGCloud 源码目录，请在该目录运行此脚本。\n' >&2
exit 2
