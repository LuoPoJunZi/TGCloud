"""Shared canonical path, ownership, and atomic-write primitives for host management."""
from __future__ import annotations

import os
import stat
import tempfile
from pathlib import Path


def ensure_canonical_path(
    path: Path,
    *,
    link_error: str = "受管路径经过符号链接或不是规范绝对位置。",
    parent_error: str | None = None,
    require_regular_or_dir: bool = False,
    type_error: str = "受管文件类型不安全。",
) -> None:
    if not path.is_absolute() or path.resolve() != path or path.is_symlink():
        raise ValueError(link_error)
    for parent in path.parents:
        if parent.is_symlink():
            raise ValueError(parent_error or link_error)
    if require_regular_or_dir and path.exists() and not (path.is_file() or path.is_dir()):
        raise ValueError(type_error)


def ensure_safe_managed_path(
    path: Path,
    *,
    private: bool = False,
    allowed_uids: tuple[int, ...] = (0,),
    allow_sticky_root_ancestors: bool = True,
    sticky_paths: tuple[Path, ...] | None = None,
    link_error: str = "受管路径经过符号链接或不是规范绝对位置。",
    parent_link_error: str | None = None,
    owner_error: str = "受管路径的所有者/写入权限不安全",
    private_error: str | None = None,
    writable_error: str | None = None,
) -> None:
    ensure_canonical_path(path, link_error=link_error, parent_error=parent_link_error)
    if os.name != "posix":
        return
    for item in (path, *path.parents):
        if not item.exists():
            continue
        info = item.stat()
        sticky_ancestor = (
            allow_sticky_root_ancestors
            and item != path
            and stat.S_ISDIR(info.st_mode)
            and info.st_uid == 0
            and bool(info.st_mode & stat.S_ISVTX)
            and (sticky_paths is None or item in sticky_paths)
        )
        if info.st_uid not in allowed_uids:
            raise ValueError(owner_error)
        if info.st_mode & 0o022 and not sticky_ancestor:
            raise ValueError(writable_error or owner_error)
    if private and path.exists():
        info = path.stat()
        if info.st_uid not in allowed_uids or info.st_mode & 0o077:
            raise ValueError(private_error or owner_error)


def atomic_write_bytes(
    path: Path,
    data: bytes,
    *,
    mode: int = 0o600,
    gid: int | None = None,
    chown=None,
    create_parent_mode: int = 0o700,
    prefix: str = ".tgcloud-",
    fsync_dir: bool = True,
) -> None:
    ensure_canonical_path(path, require_regular_or_dir=True)
    path.parent.mkdir(mode=create_parent_mode, parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=prefix, delete=False) as handle:
        temporary = Path(handle.name)
        try:
            os.chmod(temporary, mode)
            if gid is not None:
                owner_fn = chown if chown is not None else getattr(os, "chown", None)
                if owner_fn is not None:
                    owner_fn(temporary, 0, gid)
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        except BaseException:
            temporary.unlink(missing_ok=True)
            raise
    try:
        os.replace(temporary, path)
        if fsync_dir and os.name == "posix":
            descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
    finally:
        temporary.unlink(missing_ok=True)
