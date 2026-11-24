"""Open the independent, verified WebDAV manager from the selected main instance.

The main manager can be a partial bootstrap bundle. Never configure a route
using that bundle and only discover missing add-on resources after activation.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import shlex
import shutil
import subprocess  # nosec B404
import sys
import tempfile
from pathlib import Path

from tgcloud.addons.external_webdav import config, releases

COMMAND = Path("/usr/local/bin/tgcloud-webdav")
HEADER = '#!/bin/sh\n# tgcloud external_webdav managed\n'


def _check_ownership(info, *, private: bool = False) -> None:
    if os.name != "posix":
        return
    if info.st_uid != 0 or info.st_mode & (0o077 if private else 0o022):
        raise ValueError("外部 WebDAV 维护代码必须归 root 所有，且权限不可由其他用户修改。")


def _safe_path(path: Path, *, private: bool = False) -> None:
    from tgcloud.deployment.path_guard import ensure_canonical_path
    ensure_canonical_path(
        path,
        link_error="外部 WebDAV 维护路径经过链接或不是绝对路径。",
        parent_error="外部 WebDAV 维护路径经过链接。",
    )
    if os.name != "posix":
        return
    for item in (path, *path.parents):
        if not item.exists() or (item != path and item == Path("/tmp")):  # nosec B108
            continue
        _check_ownership(item.stat(), private=private and item == path)


def installed(*, launcher: Path = COMMAND, root: Path = config.ROOT) -> Path | None:
    """Verify the historical add-on layout without requiring new marker fields.

    Existing releases record the digest of sorted relative host paths and file
    contents as both the directory name and installation.json.code_sha256.
    This is integrity/ownership verification, not a digital signature.
    """
    if not launcher.exists() and not launcher.is_symlink():
        return None
    _safe_path(launcher, private=True)
    if not launcher.is_file() or launcher.stat().st_size > 4096:
        raise ValueError("现有 tgcloud-webdav 命令不属于受支持的受管入口。")
    text = launcher.read_text(encoding="utf-8")
    match = re.fullmatch(re.escape(HEADER) + r'exec python3 -B (.+) webdav "\$@"\n', text)
    arguments = shlex.split(match[1]) if match else []
    if len(arguments) != 1:
        raise ValueError("现有 tgcloud-webdav 命令不属于受支持的受管入口。")
    entry = Path(arguments[0])
    bundle = entry.parent.parent
    if (bundle.parent != root / "runtime" or not re.fullmatch(r"[0-9a-f]{64}", bundle.name)
            or entry != bundle / "scripts/run_source.py"
            or text != HEADER + 'exec python3 -B ' + shlex.quote(str(entry)) + ' webdav "$@"\n'):
        raise ValueError("现有 tgcloud-webdav 命令的代码位置不符合受管布局。")
    _safe_path(bundle, private=True)
    marker = bundle / "installation.json"
    _safe_path(marker, private=True)
    if not marker.is_file() or marker.stat().st_size > 128 * 1024:
        raise ValueError("外部 WebDAV 离线运行清单缺失或过大。")
    identity = json.loads(marker.read_text(encoding="utf-8"))
    if not isinstance(identity, dict) or identity.get("code_sha256") != bundle.name:
        raise ValueError("外部 WebDAV 离线代码摘要与运行清单不一致。")
    releases.metadata(identity)
    releases.source_identity(bundle)
    releases.supported(bundle)
    paths = releases.host_files(bundle)
    if len(paths) > 2048:
        raise ValueError("外部 WebDAV 离线源码文件数量超过安全上限。")
    checksum, total = hashlib.sha256(), 0
    expected = {*paths, "installation.json"}
    directories = {parent.as_posix() for name in expected for parent in Path(name).parents if parent != Path(".")}
    for relative in paths:
        path = bundle / relative
        _safe_path(path, private=True)
        size = path.stat().st_size
        total += size
        if size > 2 * 1024 * 1024 or total > 32 * 1024 * 1024:
            raise ValueError("外部 WebDAV 离线源码超过安全大小上限。")
        checksum.update(relative.encode() + b"\0" + path.read_bytes())
    for path in bundle.rglob("*"):
        _safe_path(path)
        relative = path.relative_to(bundle).as_posix()
        if (path.is_file() and relative not in expected) or (path.is_dir() and relative not in directories):
            raise ValueError("外部 WebDAV 离线目录含未知资源；停止，不执行。")
        if not path.is_file() and not path.is_dir():
            raise ValueError("外部 WebDAV 离线目录含特殊文件。")
    if checksum.hexdigest() != bundle.name:
        raise ValueError("外部 WebDAV 离线源码摘要不匹配；停止，不执行。")
    return bundle


def _stop(process) -> None:
    """The interactive child shares the terminal group; never kill that group."""
    if process.poll() is not None:
        return
    try:
        process.terminate()
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=150)
    except subprocess.TimeoutExpired:
        try:
            process.kill()
        except ProcessLookupError:
            pass
        process.wait(timeout=10)


def _run(source: Path, instance, log) -> None:
    environment = os.environ.copy()
    environment.pop("TGCLOUD_SOURCE_ROOT", None)
    environment["TGCLOUD_MANAGER_MENU"] = "1"
    arguments = [sys.executable, "-B", str(source / "scripts/run_source.py"), "webdav",
                 "--edition", instance.product.key, "--install-dir", str(instance.directory)]
    process = subprocess.Popen(arguments, env=environment)  # nosec B603
    try:
        result = process.wait()
    except BaseException:
        _stop(process)
        raise
    if result:
        log("外部 WebDAV 管理已结束；请核对上方结果，未假定操作成功。")


def _publication_lock_path(root: Path) -> Path:
    from tgcloud.deployment import runtime

    if root == config.ROOT:
        return runtime.LOCK_ROOT / "webdav-code.lock"
    return root.parent / (root.name + "-code.lock")


def _publication_lock(root: Path):
    from tgcloud.deployment.host import edition_lock

    return edition_lock(_publication_lock_path(root))


def install_runtime_bundle(source: Path, identity: dict, *, root: Path = config.ROOT,
                           launcher: Path = COMMAND) -> Path:
    """Install only reviewed host Python, never Bot deps or user configs."""
    paths = releases.host_files(source)
    checksum = hashlib.sha256()
    for relative in paths:
        checksum.update(relative.encode() + b"\0" + (source / relative).read_bytes())
    revision = checksum.hexdigest()
    identity = releases.metadata(dict(identity, code_sha256=revision))
    if identity["origin"] != releases.source_identity(source) or identity["version"] != releases.core.source_version(source):
        raise ValueError("维护源码与来源记录不一致；停止切换")
    destination = root / "runtime" / revision
    config.canonical(destination)

    def verify(directory: Path) -> None:
        actual = hashlib.sha256()
        for relative in paths:
            path = directory / relative
            config.canonical(path)
            if not path.is_file():
                raise ValueError("已有离线维护代码不完整，拒绝切换。")
            actual.update(relative.encode() + b"\0" + path.read_bytes())
        if actual.hexdigest() != revision:
            raise ValueError("已有离线维护代码摘要不匹配，拒绝覆盖或切换。")

    if not destination.exists():
        root.mkdir(parents=True, mode=0o700, exist_ok=True)
        destination.parent.mkdir(mode=0o700, exist_ok=True)
        # Only this invocation's private candidate is removed on ordinary
        # failure/interruption. Old stage-* paths (including unknown files)
        # are preserved and cannot block a fresh, uniquely named attempt.
        with tempfile.TemporaryDirectory(prefix="stage-" + revision + "-", dir=destination.parent) as temporary:
            staged = Path(temporary)
            config.canonical(staged)
            for relative in paths:
                target = staged / relative
                # parents=True only applies mode to the leaf directory.
                # Every copied source directory must stay private under 022.
                for parent in reversed(Path(relative).parents):
                    (staged / parent).mkdir(mode=0o700, exist_ok=True)
                shutil.copyfile(source / relative, target)
                target.chmod(0o600)
            config.atomic(staged / "installation.json", (json.dumps(dict(identity, code_sha256=revision)) + "\n").encode())
            verify(staged)
            os.replace(staged, destination)
    verify(destination)
    if json.loads((destination / "installation.json").read_text(encoding="utf-8")) != identity:
        raise ValueError("维护目录来源记录不一致；不替换旧命令")
    config.canonical(launcher)
    if launcher.exists() and not launcher.read_text(encoding="utf-8").startswith(HEADER):
        raise ValueError("已有同名命令不属于 TGCloud，拒绝覆盖。")
    script = HEADER + 'exec python3 -B ' + shlex.quote(str(destination / "scripts/run_source.py")) + ' webdav "$@"\n'
    config.atomic(launcher, script.encode(), mode=0o700)
    return destination


def sync_installed_runtime(source: Path, target_version: str, log, *, launcher: Path = COMMAND,
                           root: Path = config.ROOT) -> bool:
    """Keep an already-installed offline WebDAV runtime synchronized with main updates.

    Never auto-installs when external WebDAV is not present, never mutates
    records/auth/routes, and rejects automatic downgrades.
    """
    if not launcher.exists() and not launcher.is_symlink():
        return False
    try:
        previous = installed(launcher=launcher, root=root)
    except ValueError:
        log("检测到外部 WebDAV 入口异常或不属于 TGCloud，已跳过自动同步；请进入菜单 2.3 核对。")
        return False
    if previous is None:
        return False
    previous_identity = releases.source_identity(previous)
    previous_version = releases.core.source_version(previous)
    source_id = releases.source_identity(source)
    new_version = releases.core.source_version(source)
    if target_version and new_version != target_version:
        raise ValueError("外部 WebDAV 同步版本与主程序目标版本不一致。")
    releases.supported(source)
    if previous_identity == source_id and previous_version == new_version:
        return False
    if releases.core.version_tuple(new_version) < releases.core.version_tuple(previous_version):
        raise ValueError("拒绝自动降级外部 WebDAV 维护代码。")
    with _publication_lock(root):
        if installed(launcher=launcher, root=root) != previous:
            raise ValueError("同步期间外部 WebDAV 维护代码已变化；停止自动切换。")
        metadata = {"schema": 1, "owner": releases.OWNER, "project": releases.PROJECT_ID,
                    "version": new_version, "origin": source_id}
        install_runtime_bundle(source, metadata, root=root, launcher=launcher)
        updated = installed(launcher=launcher, root=root)
        if (updated is None or releases.source_identity(updated) != source_id
                or releases.core.source_version(updated) != new_version):
            raise RuntimeError("同步后的外部 WebDAV 离线命令未通过完整性核验。")
    log("已同步更新外部 WebDAV 维护工具：tgcloud-webdav · v" + new_version + "（保留既有配置与启用状态）")
    return True


def open_menu(instance, ui, log) -> None:
    """Use the independent lifecycle; returning never resumes obsolete code."""
    from tgcloud.deployment.configuration import checked_directory
    from tgcloud.deployment.profiles import PRODUCTS

    if instance.product.key not in PRODUCTS or instance.present is not True:
        raise ValueError("请先安装并核验当前实例，再打开外部 WebDAV 管理。")
    checked_directory(str(instance.directory))
    if os.name == "posix" and os.geteuid() != 0:
        raise ValueError("外部 WebDAV 管理需要 Linux root 权限。")
    record_path = config.ROOT / instance.product.key / "config.json"
    if record_path.exists() or record_path.is_symlink():
        record = config.read_record(record_path)
        if record is None or record["edition"] != instance.product.key or record["instance_dir"] != str(instance.directory):
            raise ValueError("外部 WebDAV 记录与当前实例目录不一致；请先核对附加功能配置，不自动切换实例。")
    previous = installed()
    source = Path(__file__).resolve().parents[4]
    identity = releases.source_identity(source)
    if not instance.source_digest or identity["source_sha256"] != instance.source_digest:
        raise ValueError("外部 WebDAV 维护源码与当前实例来源不同；请用同一完整本地源码更新主实例后重试。")
    releases.supported(source)
    if previous is not None and releases.source_identity(previous) == identity:
        _run(previous, instance, log)
        return
    log("首次打开／更新只复制同源本地维护代码，不联网、不会自动启用文件访问。")
    if not ui.confirm("是否安装同源离线外部 WebDAV 维护代码并进入菜单？"):
        return
    from tgcloud.addons.external_webdav.service import Service
    from tgcloud.deployment.host import LocalSession

    service = Service(instance.product, LocalSession(log), log, ui)
    with service.locked():
        if installed() != previous:
            raise ValueError("确认期间外部 WebDAV 管理代码已变化；停止，请刷新。")
        current = service.record()
        if current is not None and current["instance_dir"] != str(instance.directory):
            raise ValueError("外部 WebDAV 记录与当前实例目录不一致；停止，不迁移。")
        current_instance, _ = service.preflight(str(instance.directory), require_running=False)
        if current_instance.source_digest != identity["source_sha256"]:
            raise ValueError("确认期间实例来源已变化；未切换维护代码。")
        metadata = {"schema": 1, "owner": releases.OWNER, "project": releases.PROJECT_ID,
                    "version": releases.core.source_version(source), "origin": identity}
        service.install_runtime(source, metadata)
    source = installed()
    if source is None:
        raise ValueError("安装后的外部 WebDAV 离线命令不可验证；未确认成功。")
    _run(source, instance, log)
