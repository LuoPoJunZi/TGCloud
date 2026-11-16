"""Local-only add-on sources and complete offline host runtime selection."""
from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path

from tgcloud.deployment import releases as core

FEATURE = "packaging/external-webdav.json"
CONTRACT = {"api": 2, "proxy_schema": 2, "owner": "tgcloud.external_webdav"}
PROJECT_ID = "tgcloud"
OWNER = "tgcloud.external_webdav"


def contract(root: Path) -> dict:
    path = root / FEATURE
    try:
        if path.is_symlink() or path.resolve() != path.absolute():
            raise ValueError
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise ValueError("本地源码缺少受支持的外部 WebDAV 合同；TGCloud 在线发布尚未配置，不回退下载旧项目。") from exc
    if not isinstance(value, dict) or type(value.get("api")) is not int or value != CONTRACT:
        raise ValueError("本地外部 WebDAV 协议不受支持；不接管旧项目代码。")
    return value


def host_files(root: Path) -> list[str]:
    """Use the same complete standard-library host payload as the main manager."""
    from tgcloud.deployment import runtime

    contract(root)
    files = runtime.files(root)
    required = {FEATURE, "scripts/run_source.py", "src/tgcloud/cli/management.py",
                "src/tgcloud/deployment/local_source.py", "src/tgcloud/deployment/uninstall.py",
                "src/tgcloud/addons/external_webdav/entry.py", "src/tgcloud/addons/external_webdav/service.py"}
    if required - files.keys():
        raise ValueError("本地外部 WebDAV 维护载荷不完整；不会使用部分辅助代码。")
    return sorted(files)


def supported(root: Path) -> None:
    host_files(root)


def origin(value: object) -> dict:
    if (not isinstance(value, dict) or set(value) != {"kind", "project", "source_sha256"}
            or value.get("kind") != "local-source" or value.get("project") != PROJECT_ID
            or not isinstance(value.get("source_sha256"), str)
            or not re.fullmatch(r"[0-9a-f]{64}", value["source_sha256"])):
        raise ValueError("维护代码缺少此独立项目的完整本地来源；不接管旧项目或仅凭版本接管。")
    return dict(value)


def metadata(value: object) -> dict:
    if (not isinstance(value, dict)
            or set(value) != {"schema", "owner", "project", "version", "origin", "code_sha256"}
            or type(value.get("schema")) is not int or value["schema"] != 1
            or value.get("owner") != OWNER or value.get("project") != PROJECT_ID
            or not isinstance(value.get("version"), str)
            or not isinstance(value.get("code_sha256"), str)
            or not re.fullmatch(r"[0-9a-f]{64}", value["code_sha256"])):
        raise ValueError("外部 WebDAV 运行清单不属于此独立项目；不接管旧代码。")
    origin(value["origin"])
    return value


def source_identity(root: Path) -> dict:
    """An installed host manifest is verified; raw sources require a full snapshot."""
    from tgcloud.deployment import runtime

    marker = root / "installation.json"
    if marker.exists() or marker.is_symlink():
        runtime.safe_path(marker, private=True)
        if not marker.is_file() or marker.stat().st_size > 128 * 1024:
            raise ValueError("本地维护来源清单无效。")
        value = json.loads(marker.read_text(encoding="utf-8"))
        if isinstance(value, dict) and value.get("owner") == "tgcloud.cli":
            value = runtime.verify(root)
            if value.get("origin", {}).get("project") != PROJECT_ID:
                raise ValueError("主维护代码不属于此独立项目。")
            return origin(value.get("origin"))
        value = metadata(value)
        checksum = hashlib.sha256()
        for name in host_files(root):
            checksum.update(name.encode() + b"\0" + (root / name).read_bytes())
        if checksum.hexdigest() != value["code_sha256"] or core.source_version(root) != value["version"]:
            raise ValueError("外部 WebDAV 运行代码与来源清单不一致。")
        expected = {*host_files(root), "installation.json"}
        directories = {parent.as_posix() for name in expected for parent in Path(name).parents if parent != Path(".")}
        for path in root.rglob("*"):
            runtime.safe_path(path, private=True)
            name = path.relative_to(root).as_posix()
            if (path.is_file() and name not in expected or path.is_dir() and name not in directories
                    or not path.is_file() and not path.is_dir()):
                raise ValueError("外部 WebDAV 运行目录含未知资源。")
        return origin(value["origin"])
    from tgcloud.deployment.local_source import snapshot

    with snapshot(root) as source:
        supported(source.root)
        return origin(source.origin)


def download(destination: Path) -> tuple[Path, dict]:
    raise ValueError("TGCloud 在线发布尚未配置；请使用与当前 CloudDrive2 实例同源的完整本地源码，不下载旧项目。")
