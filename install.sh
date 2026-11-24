#!/usr/bin/env bash
# TGCloud Linux installation and management entry (local source or stable release).
set -Eeuo pipefail
umask 077

if ! command -v python3 >/dev/null 2>&1; then
  printf '需要 Python 3.10+。Debian/Ubuntu 可先手动执行：apt-get update && apt-get install -y python3 ca-certificates\n' >&2
  exit 2
fi
python3 -I -B -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 2)' || {
  printf '需要 Python 3.10 或更高版本；未安装任何依赖。\n' >&2
  exit 2
}

script_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ -f "$script_root/src/tgcloud/cli/commands.py" && -f "$script_root/scripts/run_source.py" ]]; then
  if [[ "${1:-}" == --lan ]]; then
    shift
    exec python3 -B "$script_root/scripts/run_source.py" lan "$@"
  fi
  exec python3 -B "$script_root/scripts/run_source.py" install "$@"
fi
if [[ -e "$script_root/src/tgcloud" || -e "$script_root/scripts/run_source.py" || -e "$script_root/packaging/bootstrap-manifest.json" ]]; then
  printf 'TGCloud 本地源码不完整，请补齐后重试；未下载或修改实例。\n' >&2
  exit 2
fi

# TGCloud online bootstrap.
if [[ "${1:-}" == --help || "${1:-}" == -h ]]; then
  printf 'TGCloud 在线安装入口\n用法：bash install.sh [--lan] [向导参数]\n自动下载并校验 v0.1.0 正式源码；--lan 使用可信内网向导。\n请先 sudo -i 进入 root 终端。不会安装 Git 或 Python 依赖。\n'
  exit 0
fi
if [[ "$(id -u)" != 0 ]]; then
  printf '请先执行 sudo -i 进入 root 终端，再运行在线安装命令。\n' >&2
  exit 2
fi
for tgcloud_tool in curl mktemp; do
  command -v "$tgcloud_tool" >/dev/null 2>&1 || {
    printf '缺少 %s；Debian/Ubuntu 可执行 apt-get update && apt-get install -y curl python3 ca-certificates。\n' "$tgcloud_tool" >&2
    exit 2
  }
done

tgcloud_tag=v0.1.0
tgcloud_command=install
if [[ "${1:-}" == --lan ]]; then
  tgcloud_command=lan
  shift
fi
tgcloud_work="$(mktemp -d "${TMPDIR:-/tmp}/tgcloud-online.XXXXXXXX")"
tgcloud_cleanup() { rm -rf -- "$tgcloud_work"; }
trap tgcloud_cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
tgcloud_asset="TGCloud-${tgcloud_tag}-source.tar.gz"
tgcloud_base="https://github.com/LuoPoJunZi/TGCloud/releases/download/${tgcloud_tag}"
printf 'TGCloud %s\n正在下载正式源码并验证 SHA256，随后进入安装／管理向导。\n' "$tgcloud_tag"
curl -fL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 --retry 2 \
  --max-filesize 104857600 -o "$tgcloud_work/$tgcloud_asset" "$tgcloud_base/$tgcloud_asset"
curl -fL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 60 --retry 2 \
  --max-filesize 4096 -o "$tgcloud_work/SHA256SUMS.txt" "$tgcloud_base/SHA256SUMS.txt"

# Keep the terminal stdin available to the child wizard (no curl | bash).
python3 -I -B -c '
import ast
import hashlib
import re
import shutil
import sys
import tarfile
from pathlib import Path, PurePosixPath

directory, tag = Path(sys.argv[1]), sys.argv[2]
name = "TGCloud-" + tag + "-source.tar.gz"
archive = directory / name
try:
    if archive.stat().st_size > 100 * 1024**2:
        raise ValueError("源码包超过大小限制")
    checksum = (directory / "SHA256SUMS.txt").read_text(encoding="ascii")
    if not re.fullmatch(r"[0-9a-f]{64}  " + re.escape(name) + r"\n", checksum):
        raise ValueError("SHA256 校验文件格式或版本不符")
    if hashlib.sha256(archive.read_bytes()).hexdigest() != checksum[:64]:
        raise ValueError("源码包 SHA256 不匹配")
    root = directory / "source"
    root.mkdir(mode=0o700)
    seen, total = set(), 0
    with tarfile.open(archive, "r:gz") as bundle:
        for number, member in enumerate(bundle):
            parts = PurePosixPath(member.name).parts
            if (number >= 20000 or not parts or parts[0] != "TGCloud-" + tag
                    or member.name.startswith("/") or ".." in parts
                    or "\\" in member.name or ":" in member.name
                    or not (member.isfile() or member.isdir()) or member.name in seen):
                raise ValueError("源码包包含不安全或重复的路径／文件类型")
            seen.add(member.name)
            total += member.size
            if member.size < 0 or member.size > 32 * 1024**2 or total > 100 * 1024**2:
                raise ValueError("源码包解压大小超限")
            if len(parts) == 1:
                if not member.isdir():
                    raise ValueError("源码包顶层不是目录")
                continue
            target = root.joinpath(*parts[1:])
            if member.isdir():
                target.mkdir(mode=0o700, parents=True, exist_ok=True)
            else:
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with bundle.extractfile(member) as reader, target.open("xb") as writer:
                    shutil.copyfileobj(reader, writer)
                target.chmod(0o600)
    for required in ("scripts/run_source.py", "src/tgcloud/cli/commands.py", "LICENSE"):
        if not (root / required).is_file():
            raise ValueError("源码包不完整")
    module = ast.parse((root / "src/tgcloud/version.py").read_text(encoding="utf-8"))
    versions = [ast.literal_eval(node.value) for node in module.body
                if isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == "VERSION" for t in node.targets)]
    if versions != [tag[1:]]:
        raise ValueError("源码版本与下载版本不符")
except (OSError, ValueError, SyntaxError, tarfile.TarError):
    print("TGCloud 源码校验／解包失败；未启动安装向导，未修改运行实例。", file=sys.stderr)
    sys.exit(2)
print("TGCloud 安装文件校验通过，正在启动向导。", flush=True)
' "$tgcloud_work" "$tgcloud_tag"

python3 -B "$tgcloud_work/source/scripts/run_source.py" "$tgcloud_command" "$@"
