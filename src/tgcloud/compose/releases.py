"""Verify and copy local CD2 packages; no registry or official release fallback."""
from __future__ import annotations

import hashlib
import json
import os
import re
import stat
from pathlib import Path, PurePosixPath

from tgcloud.compose import packaging
from tgcloud.deployment import local_source

PUBLIC_DIRS = {"build-context", "https-tools", "local-source"}
# Optional names also recognize recorded files in existing TGCloud deployment roots.
PUBLIC_FILES = {"docker-compose.yml", "compose-bundle.json", "README.md", "LICENSE", local_source.LEGACY_NOTICE, ".env.example", "https.sh", "manage.sh"}
PRIVATE_DIRS = {"data", "downloads", "logs", "config", "clouddrive", "backups"}
PRIVATE_FILES = {".env", "tgcloud-compose.json", "compose-access.json", ".compose-upgrade.json", ".tgcloud-compose.lock"}
RESOURCES = {"packaging/compose/docker-compose.clouddrive2.yml", "packaging/compose-https.sh",
             "packaging/bootstrap-manifest.json", "packaging/compose-release.json",
             "packaging/external-webdav.json"}


def unique_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate Compose metadata field")
        result[key] = value
    return result


def tree_files(root: Path) -> dict[str, bytes]:
    """Read only a bounded public tree, rejecting links and unknown directories."""
    if root.is_symlink() or root.absolute() != root.resolve() or not root.is_dir():
        raise ValueError("unsafe Compose public directory")
    result, size = {}, 0
    for directory, names, leaves in os.walk(root, followlinks=False):
        for name in names + leaves:
            path = Path(directory) / name
            info = path.lstat()
            if path.is_symlink() or path.resolve() != path.absolute():
                raise ValueError("Compose public source contains a link")
            if stat.S_ISDIR(info.st_mode):
                continue
            if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 4 * 1024**2
                    or len(result) >= 5000 or size + info.st_size > 64 * 1024**2):
                raise ValueError("Compose public source is unsafe or oversized")
            result[path.relative_to(root).as_posix()] = path.read_bytes()
            size += info.st_size
    directories = {parent.as_posix() for name in result for parent in PurePosixPath(name).parents
                   if parent != PurePosixPath(".")}
    for directory, names, _ in os.walk(root, followlinks=False):
        if any((Path(directory) / name).relative_to(root).as_posix() not in directories for name in names):
            raise ValueError("Compose public source contains an unknown empty directory")
    return result


def public_files(root: Path) -> dict[str, bytes]:
    result = {}
    for path in root.iterdir():
        name = path.name
        if name in PUBLIC_FILES:
            info = path.lstat()
            if (path.is_symlink() or path.resolve() != path.absolute() or not stat.S_ISREG(info.st_mode)
                    or info.st_nlink != 1 or info.st_size > 512 * 1024):
                raise ValueError("unsafe Compose public file")
            result[name] = path.read_bytes()
        elif name in PUBLIC_DIRS:
            result.update({name + "/" + relative: body for relative, body in tree_files(path).items()})
        elif (name in PRIVATE_DIRS or name in PRIVATE_FILES
              or re.fullmatch(r"\.env-before-resources-[a-f0-9]{16}", name)
              or re.fullmatch(r"nginx-clouddrive2-[a-f0-9]{12}\.conf", name)):
            continue  # Private data and explicit maintenance outputs never enter a build context.
        else:
            raise ValueError("unknown file in Compose deployment root")
    return result


def verify_tools(root: Path, *, require_manager: bool = True) -> dict:
    actual = tree_files(root / "https-tools")
    body = actual.get("manifest.json", b"")
    if len(body) > 256 * 1024:
        raise ValueError("Compose host manifest too large")
    value = json.loads(body, object_pairs_hook=unique_pairs)
    if (not isinstance(value, dict) or set(value) != {"format", "files"}
            or type(value.get("format")) is not int or value["format"] != 1
            or not isinstance(value.get("files"), dict) or not 1 <= len(value["files"]) <= 1000):
        raise ValueError("unsupported Compose host manifest")
    for name, digest in value["files"].items():
        allowed = (name == "scripts/run_source.py" or name in RESOURCES
                   or bool(re.fullmatch(r"src/tgcloud/(?:[a-z_][a-z0-9_]*/)*[a-z_][a-z0-9_]*\.py", name)))
        if not allowed or not isinstance(digest, str) or not re.fullmatch(r"[a-f0-9]{64}", digest):
            raise ValueError("unsafe Compose host manifest entry")
    if set(actual) != {*value["files"], "manifest.json"} or packaging.hashes(
            {name: content for name, content in actual.items() if name != "manifest.json"}) != value["files"]:
        raise ValueError("Compose host helper file set or checksums differ")
    digest = hashlib.sha256(body).hexdigest()
    for name in (("https.sh", "manage.sh") if require_manager else ("https.sh",)):
        path = root / name
        if (path.is_symlink() or not path.is_file() or path.stat().st_nlink != 1 or path.stat().st_size > 16384
                or re.findall(r'hexdigest\(\) != "([a-f0-9]{64})"', path.read_text(encoding="utf-8")) != [digest]):
            raise ValueError("Compose launcher and host manifest disagree")
    return value


def verify_contents(root: Path, info: dict) -> dict[str, bytes]:
    actual = public_files(root)
    manifest = {name: body for name, body in actual.items() if name != "compose-bundle.json"}
    if packaging.hashes(manifest) != info["files"]:
        raise ValueError("Compose package file set or checksums differ")
    source_root = root / "local-source"
    source_files = tree_files(source_root)
    if source_files != local_source.content(source_root):
        raise ValueError("Compose local-source contains unknown or incomplete public inputs")
    source_digest = local_source.digest(source_files)
    version = local_source.source_version(source_root)
    if source_digest != info["source_sha256"] or version != info["version"]:
        raise ValueError("Compose local source identity differs")
    expected = packaging.bundle_files(source_root, source_digest=source_digest, version=version)
    if local_source.content(source_root) != source_files:
        raise ValueError("Compose source changed during verification")
    # Formatting of the metadata object itself is not an identity field.
    expected.pop("compose-bundle.json")
    if expected != manifest:
        raise ValueError("Compose payload, host helpers or rendering differs from the local source")
    verify_tools(root)
    return actual


def candidate(source_path: Path, destination: Path) -> Path:
    """Copy validated bytes, never import or execute candidate Python source."""
    from tgcloud.compose import runtime
    if source_path.is_symlink() or not source_path.is_absolute() or source_path.resolve() != source_path or not source_path.is_dir():
        raise ValueError("需要不经过链接的本地完整源码或 Compose 包绝对目录")
    if (source_path / "compose-bundle.json").exists():
        info = runtime.metadata(source_path, expected_version=None)
        files = verify_contents(source_path, info)
    else:
        with local_source.snapshot(source_path) as source:
            files = packaging.bundle_files(source.root, source_digest=source.digest, version=source.version)
    packaging.write_bundle(destination, files)
    runtime.metadata(destination, expected_version=None)
    return destination


def download(*args, **kwargs):
    raise ValueError("仅支持本地完整源码或 Compose 包；不下载官方 Release 或镜像")
