"""Bounded local-source snapshots and installed-file identity, without Git or downloads."""
from __future__ import annotations

import ast
import hashlib
import json
import os
import re
import stat
import tempfile
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

PROJECT_ID = "tgcloud"
MARKER = "tgcloud-source.json"
OWNER = "tgcloud.local-source"
MAX_FILE = 2 * 1024**2
MAX_TOTAL = 32 * 1024**2
MAX_FILES = 1000
LEGACY_NOTICE = "THIRD_PARTY_NOTICES.md"
SHA = re.compile(r"[0-9a-f]{64}")


def regular_bytes(path: Path, *, limit: int = MAX_FILE) -> bytes:
    path = path.absolute()
    if path.resolve() != path or any(parent.is_symlink() for parent in (path, *path.parents)):
        raise ValueError("源码或清单路径经过链接")
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size > limit:
        raise ValueError("源码或清单不是有界的普通独立文件")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_BINARY", 0))
    try:
        opened = os.fstat(descriptor)
        def identity(value):
            # Windows stat/fstat expose different legacy ctime semantics; birthtime
            # identifies creation there. Linux keeps ctime to detect metadata races.
            changed = value.st_birthtime_ns if os.name == "nt" else value.st_ctime_ns
            return value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, changed
        if identity(opened) != identity(before):
            raise ValueError("读取期间文件发生变化")
        with os.fdopen(descriptor, "rb", closefd=False) as stream:
            body = stream.read(limit + 1)
        if (len(body) > limit or identity(os.fstat(descriptor)) != identity(opened)
                or identity(path.lstat()) != identity(opened) or path.resolve() != path):
            raise ValueError("读取期间文件发生变化")
        return body
    finally:
        os.close(descriptor)


def source_version(root: Path) -> str:
    tree = ast.parse(regular_bytes(root / "src/tgcloud/version.py").decode("utf-8"))
    values = [node.value.value for node in tree.body if isinstance(node, ast.Assign)
              and any(isinstance(target, ast.Name) and target.id == "VERSION" for target in node.targets)
              and isinstance(node.value, ast.Constant) and isinstance(node.value.value, str)]
    if len(values) != 1 or not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", values[0]):
        raise ValueError("本地源码版本无效")
    return values[0]


def hashes(values: dict[str, bytes]) -> dict[str, str]:
    return {name: hashlib.sha256(body).hexdigest() for name, body in sorted(values.items())}


def digest(values: dict[str, bytes]) -> str:
    manifest = json.dumps(hashes(values), sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(b"clouddrive2-local-source-v1\0" + manifest).hexdigest()


def source_names(root: Path) -> tuple[str, ...]:
    """Select public build inputs only; never recurse into the workspace root."""
    from tgcloud.deployment.payload import payload_sources
    from tgcloud.deployment.profiles import CLOUDDRIVE2_PRODUCT
    from tgcloud.deployment.runtime import file_names

    root = root.absolute()
    if root.resolve() != root or not root.is_dir():
        raise ValueError("完整源码目录不存在或经过链接")
    selected = {path.relative_to(root).as_posix() for path in payload_sources(root, CLOUDDRIVE2_PRODUCT).values()}
    selected.update(file_names(root))
    selected.update(("LICENSE", "README.md", "docs/compose-guide.md", "docs/install.md", "packaging/compose-https.sh",
                     "packaging/external-webdav.json", "packaging/runtime-manifest.json",
                     "packaging/bootstrap-manifest.json"))
    if (root / "NOTICE").exists():
        selected.add("NOTICE")
    # Older deployment manifests include this file in their source fingerprint.
    version = source_version(root)
    if version in {"0.0.1", "0.0.2"} and (root / LEGACY_NOTICE).exists():
        selected.add(LEGACY_NOTICE)
    if len(selected) > MAX_FILES:
        raise ValueError("源码文件数超过安全上限")
    return tuple(sorted(selected))


def content(root: Path) -> dict[str, bytes]:
    result = {name: regular_bytes(root / name) for name in source_names(root)}
    if sum(map(len, result.values())) > MAX_TOTAL:
        raise ValueError("源码大小超过安全上限")
    return result


@dataclass(frozen=True)
class LocalSource:
    root: Path
    version: str
    digest: str
    kind: str = "local-source"

    @property
    def tag(self) -> str:
        return "local-v" + self.version

    @property
    def origin(self) -> dict:
        return {"kind": self.kind, "project": PROJECT_ID, "source_sha256": self.digest}


def native_source(operation: str, *args: str, selection: dict | None = None) -> str | bool | None:
    from tgcloud.cli import host_cli
    output = host_cli.checked_output("install-source", operation, *args,
                                     input_text=None if selection is None else json.dumps(selection, ensure_ascii=False))
    if output is None:
        return None
    try:
        result = json.loads(output)
    except ValueError:
        result = None
    valid = isinstance(result, str) and SHA.fullmatch(result) if operation == "snapshot" else result is True
    if not valid:
        raise ValueError("Go 返回的部署源码结果无法核实")
    return result


def native_snapshot(original: Path, target: Path) -> LocalSource | None:
    from tgcloud.deployment.runtime import packaged_host_cli

    names = source_names(original)
    optional = packaged_host_cli(original)
    selection = {"files": names}
    if optional is not None:
        name, body = optional
        selection.update(tool=name, tool_sha256=hashlib.sha256(body).hexdigest())
    result = native_source("snapshot", str(original), str(target), selection=selection)
    if result is None:
        return None
    if source_names(original) != names or source_names(target) != names:
        raise ValueError("生成快照期间源码文件清单发生变化，请重新检查")
    if packaged_host_cli(original) != optional or packaged_host_cli(target) != optional:
        raise ValueError("生成快照期间 Go 管理工具发生变化，请重新检查")
    return LocalSource(target, source_version(target), result)


def verify_digest(root: Path, expected: str) -> None:
    from tgcloud.cli import host_cli

    if host_cli.supports("install-source"):
        root = root.absolute()
        names = source_names(root)
        result = native_source("verify", str(root), expected, selection={"files": names})
        if result is not None:
            if source_names(root) != names:
                raise ValueError("确认期间部署源码文件清单已变更，停止并重新预检")
            return
    if digest(content(root)) != expected:
        raise ValueError("确认期间部署源码已变更，停止并重新预检")


@contextmanager
def snapshot(root: Path, *, host_cli: bool = False):
    original = root.absolute()
    if host_cli:
        from tgcloud.cli import host_cli as go
        if go.supports("install-source"):
            with tempfile.TemporaryDirectory(prefix="tgcloud-local-source-") as temporary:
                frozen = native_snapshot(original, Path(temporary).resolve() / "source")
                if frozen is not None:
                    yield frozen
                    return
    values = content(original)
    if host_cli:
        from tgcloud.deployment.runtime import packaged_host_cli
        optional = packaged_host_cli(original)
    else:
        optional = None
    with tempfile.TemporaryDirectory(prefix="tgcloud-local-source-") as temporary:
        target = Path(temporary).resolve()
        target.chmod(0o700)
        staged = dict(values)
        if optional is not None:
            name, body = optional
            staged[name] = body
        for name, body in staged.items():
            path = target / name
            path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with path.open("xb") as stream:
                path.chmod(0o600)
                stream.write(body)
        if content(original) != values or content(target) != values:
            raise ValueError("生成快照期间源码发生变化，请重新检查")
        if host_cli and (packaged_host_cli(original) != optional or packaged_host_cli(target) != optional):
            raise ValueError("生成快照期间 Go 管理工具发生变化，请重新检查")
        yield LocalSource(target, source_version(target), digest(values))


def _relative(name: str) -> None:
    path = PurePosixPath(name)
    if (not name or path.is_absolute() or path.as_posix() != name or ".." in path.parts
            or "\\" in name or ":" in name or any(ord(character) < 32 for character in name)):
        raise ValueError("来源清单包含不安全路径")


def _managed(name: str) -> bool:
    # Recognize recorded program files from existing TGCloud instances during upgrade.
    return (name.startswith("app/") and name.endswith(".py") or name in {
        ".dockerignore", "Dockerfile", "docker-compose.yml", "requirements.txt", "LICENSE", "NOTICE", "README.md", LEGACY_NOTICE,
        "backup_retention.sh", "preserve_runtime_config.sh", "repair_clouddrive_network.sh", "manage.sh", "remote_install.sh",
    })


def marker_for(directory: Path, source: LocalSource, *, mode: str = "shell", state: str = "pending") -> dict:
    if mode != "shell" or state not in {"pending", "ready"}:
        raise ValueError("来源标记模式无效")
    values = {}
    for path in sorted(directory.rglob("*")):
        name = path.relative_to(directory).as_posix()
        if path.is_symlink():
            raise ValueError("部署候选包含链接")
        if path.is_file():
            if not _managed(name):
                raise ValueError("部署候选包含未知文件")
            values[name] = regular_bytes(path)
    if not {"app/main.py", "app/tgcloud/version.py", "Dockerfile", "docker-compose.yml", "manage.sh"} <= values.keys():
        raise ValueError("部署候选缺少必要文件")
    return {"schema": 1, "owner": OWNER, "project": PROJECT_ID, "mode": mode,
            "edition": "clouddrive2", "version": source.version, "source_sha256": source.digest,
            "files": hashes(values), "state": state}


def write_marker(directory: Path, value: dict) -> None:
    target = directory / MARKER
    if target.is_symlink() or target.absolute() != target.resolve():
        raise ValueError("来源记录路径不安全")
    descriptor, temporary = tempfile.mkstemp(prefix=".source-", dir=directory)
    staged = Path(temporary)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            staged.chmod(0o600)
            stream.write((json.dumps(value, sort_keys=True) + "\n").encode())
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(staged, target)
    finally:
        staged.unlink(missing_ok=True)


def verify_installed(directory: Path, *, mode: str = "shell", expected_version: str | None = None,
                     allow_pending: bool = False) -> dict:
    """Verify only declared public program files; never read instance configuration/data."""
    directory = directory.absolute()
    if directory.resolve() != directory:
        raise ValueError("实例路径经过链接")
    try:
        value = json.loads(regular_bytes(directory / MARKER, limit=256 * 1024))
    except (OSError, ValueError) as exc:
        raise ValueError("缺少 TGCloud 有效来源记录；停止处理身份未知的实例") from exc
    keys = {"schema", "owner", "project", "mode", "edition", "version", "source_sha256", "files", "state"}
    if (not isinstance(value, dict) or set(value) != keys or type(value["schema"]) is not int or value["schema"] != 1
            or value["owner"] != OWNER or value["project"] != PROJECT_ID or value["mode"] != mode
            or value["edition"] != "clouddrive2" or value["state"] not in ({"ready", "pending"} if allow_pending else {"ready"})
            or not isinstance(value["source_sha256"], str) or not SHA.fullmatch(value["source_sha256"])
            or not isinstance(value["version"], str) or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", value["version"])
            or expected_version is not None and value["version"] != expected_version
            or not isinstance(value["files"], dict) or not 1 <= len(value["files"]) <= MAX_FILES):
        raise ValueError("实例来源、状态或版本不属于本项目")
    for name, expected in value["files"].items():
        _relative(name)
        if not _managed(name) or not isinstance(expected, str) or not SHA.fullmatch(expected):
            raise ValueError("部署文件清单无效")
        if hashlib.sha256(regular_bytes(directory / name)).hexdigest() != expected:
            raise ValueError("已安装程序与来源记录不一致，停止覆盖")
    required = {"app/main.py", "app/tgcloud/version.py", "Dockerfile", "docker-compose.yml", "manage.sh"}
    if not required <= value["files"].keys():
        raise ValueError("部署来源记录不完整")
    actual = {path.relative_to(directory).as_posix() for path in (directory / "app").rglob("*.py")}
    if actual != {name for name in value["files"] if name.startswith("app/")}:
        raise ValueError("实例含额外或缺失的 Python 程序")
    return value


def main(argv=None) -> int:
    import argparse
    parser = argparse.ArgumentParser(description="Validate the local deployment source without reading credentials")
    parser.add_argument("directory", type=Path)
    parser.add_argument("--activate", action="store_true")
    parser.add_argument("--allow-pending", action="store_true")
    args = parser.parse_args(argv)
    try:
        value = verify_installed(args.directory, allow_pending=args.activate or args.allow_pending)
        if args.activate:
            value["state"] = "ready"
            write_marker(args.directory, value)
        return 0
    except (OSError, ValueError, TypeError):
        print("本地部署来源检查未通过；保留现场，不输出配置。")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
