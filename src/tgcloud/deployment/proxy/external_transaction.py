"""Compare-and-swap, file-scoped external-site transactions and crash journal."""
from __future__ import annotations

import base64
import json
import os
import re
import stat
from pathlib import Path

from .external_config import OWNER, checksum


def canonical(path: Path) -> None:
    from tgcloud.deployment.path_guard import ensure_canonical_path
    ensure_canonical_path(
        path,
        link_error="受管文件路径经过链接。",
        parent_error="受管文件父目录经过链接。",
        require_regular_or_dir=True,
        type_error="受管文件类型不安全。",
    )


def atomic(path: Path, data: bytes, *, mode: int = 0o600, gid: int | None = None) -> None:
    from tgcloud.deployment.path_guard import atomic_write_bytes
    canonical(path)
    atomic_write_bytes(path, data, mode=mode, gid=gid, chown=getattr(os, "chown", None))


def read(path: Path) -> bytes | None:
    canonical(path)
    if not path.exists():
        return None
    if not path.is_file() or path.stat().st_size > 512 * 1024:
        raise ValueError("受管配置文件过大或不是普通文件。")
    return path.read_bytes()


class SiteTransaction:
    """Caller holds the shared entry lock. Never restore an entire directory."""
    def __init__(self, directory: Path, journal: Path, expected: dict[str, str], target: dict[str, bytes]):
        self.directory, self.journal = directory, journal
        canonical(directory)
        canonical(journal)
        if journal.exists():
            raise ValueError("存在未完成外部入口事务；先执行显式恢复，不继续写入。")
        self.items = {}
        for name in sorted(expected.keys() | target.keys()):
            if Path(name).name != name or not name.startswith("tgcloud-") or not name.endswith(".conf"):
                raise ValueError("事务包含非 TGCloud 站点文件。")
            path = directory / name
            before = read(path)
            if name in expected:
                if before is None or checksum(before) != expected[name] or not before.startswith(("# " + OWNER + "\n").encode()):
                    raise ValueError("受管站点配置发生外部漂移；不覆盖。")
            elif before is not None:
                raise ValueError("同名站点文件已存在且归属不明；不覆盖。")
            after = target.get(name)
            if after is not None and not after.startswith(("# " + OWNER + "\n").encode()):
                raise ValueError("候选站点缺少归属标记。")
            self.items[name] = {"before": encode(before), "after": encode(after), "intermediate": [],
                                "mode": stat.S_IMODE(path.stat().st_mode) if before is not None else 0o644}
        self.data = {"schema": 1, "owner": OWNER, "directory": str(directory), "phase": "prepared", "items": self.items}
        self.save()

    def save(self):
        atomic(self.journal, (json.dumps(self.data, sort_keys=True) + "\n").encode())

    def publish(self):
        self.data["phase"] = "publishing"
        self.save()
        for name, item in self.items.items():
            path = self.directory / name
            if read(path) != decode(item["before"]):
                raise ValueError("站点在事务期间被并发修改，停止。")
            after = decode(item["after"])
            if after is None:
                path.unlink(missing_ok=True)
            else:
                atomic(path, after, mode=0o644)
        self.data["phase"] = "published"
        self.save()

    def rollback(self):
        for name, item in self.items.items():
            path = self.directory / name
            current, before, after = read(path), decode(item["before"]), decode(item["after"])
            if current == before:
                continue
            if current != after and current not in [decode(value) for value in item["intermediate"]]:
                raise ValueError("回滚发现外部并发修改；保留日志，不覆盖该文件。")
            if before is None:
                path.unlink(missing_ok=True)
            else:
                atomic(path, before, mode=item["mode"])
        self.data["phase"] = "rolled_back"
        self.save()

    def finish(self):
        canonical(self.journal)
        self.journal.unlink()

    @classmethod
    def recover(cls, directory: Path, journal: Path):
        canonical(directory)
        canonical(journal)
        value = json.loads(read(journal) or b"null")
        if (not isinstance(value, dict) or set(value) != {"schema", "owner", "directory", "phase", "items"}
                or value["schema"] != 1 or value["owner"] != OWNER or value["directory"] != str(directory)
                or value["phase"] not in {"prepared", "publishing", "published", "rolled_back", "committed"}
                or not isinstance(value["items"], dict) or len(value["items"]) > 8):
            raise ValueError("事务日志不受支持，不推测恢复范围。")
        instance = object.__new__(cls)
        instance.directory, instance.journal, instance.data, instance.items = directory, journal, value, value["items"]
        for name, item in instance.items.items():
            if (not re.fullmatch(r"tgcloud-(?:clouddrive2|webdav-clouddrive2)\.conf", name)
                    or not isinstance(item, dict) or set(item) != {"before", "after", "mode", "intermediate"}
                    or item["mode"] not in {0o600, 0o640, 0o644}):
                raise ValueError("事务清单不属于受支持站点。")
            if not isinstance(item["intermediate"], list) or len(item["intermediate"]) > 2:
                raise ValueError("事务中间态清单无效。")
            for encoded in [item["before"], item["after"], *item["intermediate"]]:
                content = decode(encoded)
                if content is not None and (len(content) > 512 * 1024 or not content.startswith(("# " + OWNER + "\n").encode())):
                    raise ValueError("事务恢复数据无效。")
        return instance


def encode(data):
    return None if data is None else base64.b64encode(data).decode("ascii")


def decode(data):
    return None if data is None else base64.b64decode(data, validate=True)
