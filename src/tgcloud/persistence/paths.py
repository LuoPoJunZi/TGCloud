"""One database identity for fresh installs and existing TGCloud instances.

Never rename/copy a live SQLite file, merge histories or create a replacement
when an ambiguous database/sidecar is present. No Bot dependencies or schema work.
"""
from __future__ import annotations

import argparse
import stat
import sys
from pathlib import Path

DATABASE_NAME = "tgcloud.db"
DATABASE_NAMES = (DATABASE_NAME,)
SIDECARS = ("-wal", "-shm", "-journal")


def database_path(directory: Path, *, require_existing: bool = False) -> Path:
    directory = directory.absolute()
    if directory.resolve() != directory or directory.is_symlink():
        raise ValueError("数据库目录经过链接；停止，不创建或移动数据库")
    if directory.exists():
        for entry in directory.iterdir():
            name = entry.name
            for suffix in SIDECARS:
                name = name.removesuffix(suffix)
            if name.endswith((".db", ".sqlite", ".sqlite3")) and name != DATABASE_NAME:
                raise ValueError("发现其他数据库或旁文件；停止，不选择、移动或创建替代数据库")
    present = []
    for name in DATABASE_NAMES:
        path = directory / name
        if path.exists() or path.is_symlink():
            info = path.lstat()
            if path.is_symlink() or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                raise ValueError("数据库不是安全的独立常规文件；停止")
            present.append(path)
        for suffix in SIDECARS:
            sidecar = directory / (name + suffix)
            if sidecar.exists() or sidecar.is_symlink():
                info = sidecar.lstat()
                if sidecar.is_symlink() or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or path not in present:
                    raise ValueError("数据库旁文件归属不明确；停止，不创建替代数据库")
    if len(present) > 1:
        raise ValueError("数据库身份不唯一；停止自动选择")
    if present:
        return present[0]
    if require_existing:
        raise ValueError("业务数据库不存在；不会创建空数据库替代")
    return directory / DATABASE_NAME


def database_entry(names) -> str:
    names = set(names)
    for name in names:
        path = Path(name)
        if path.parent.as_posix() == "data" and path.suffix in {".db", ".sqlite", ".sqlite3"} and path.name != DATABASE_NAME:
            raise ValueError("备份含其他数据库；停止自动选择")
    selected = {"data/" + name for name in DATABASE_NAMES} & set(names)
    if len(selected) != 1:
        raise ValueError("备份必须包含唯一的业务数据库；不接受缺失或多个数据库")
    return selected.pop()


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="只读核对 TGCloud 数据库文件名，不创建或移动数据")
    parser.add_argument("directory", type=Path)
    args = parser.parse_args(argv)
    try:
        print(database_path(args.directory).name)
        return 0
    except (ValueError, OSError):
        print("数据库身份不明确或路径不安全；停止，不修改现有数据。", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
