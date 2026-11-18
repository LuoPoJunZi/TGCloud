"""Synthetic settings, transfer services and in-memory WebDAV storage."""
from __future__ import annotations

import asyncio
import base64
import os
import time
from collections import deque
from pathlib import Path
from unittest.mock import Mock, patch

from tgcloud.app import TransferService
from tgcloud.config import Settings
from tgcloud.persistence.sqlite import TaskDB


def encoded(value: str) -> str:
    return base64.b64encode(value.encode()).decode()


def make_settings(root: Path) -> Settings:
    env = {
        "TELEGRAM_API_ID": "12345",
        "TELEGRAM_API_HASH_B64": encoded("a" * 32),
        "BOT_TOKEN_B64": encoded("12345:" + "a" * 30),
        "ALLOWED_USER_ID": "987654321",
        "CD2_WEBDAV_URL_B64": encoded("http://clouddrive2:19798/dav"),
        "CD2_WEBDAV_USERNAME_B64": encoded("user"),
        "CD2_WEBDAV_PASSWORD_B64": encoded("password"),
        "CD2_TARGET_PATH_B64": encoded("115/Telegram"),
        "DATA_DIR": str(root / "data"),
        "DOWNLOAD_DIR": str(root / "downloads"),
        "LOG_DIR": str(root / "logs"),
        "RCLONE_CONFIG_PATH": str(root / "config" / "rclone.conf"),
    }
    with patch.dict(os.environ, env, clear=True):
        return Settings.from_env()


def make_service(settings: Settings, db: TaskDB) -> TransferService:
    service = TransferService.__new__(TransferService)
    service.settings = settings
    service.db = db
    service.log = Mock()
    service.recent_download_errors = deque(maxlen=50)
    service.recent_upload_errors = deque(maxlen=50)
    service._finalize_lock = asyncio.Lock()
    service.download_tasks = {}
    service.upload_tasks = {}
    service._progress = {}
    service._watch_messages = {}
    service.destination_last_checked = time.time()
    return service


class FakeRclone:
    def __init__(self) -> None:
        self.files: dict[str, bytes] = {}
        self.removed: list[str] = []

    async def upload(self, local_path: Path, remote_path: str, *, progress=None) -> None:
        self.files[remote_path] = local_path.read_bytes()
        if progress:
            progress(local_path.stat().st_size, 1)

    async def open_upload_stream(self, remote_path: str, exact_size: int):
        owner = self

        class Stream:
            def __init__(self) -> None:
                self.data = bytearray()
                self.finished = False

            async def write(self, data: bytes) -> int:
                self.data.extend(data)
                return len(data)

            def tell(self) -> int:
                return len(self.data)

            def flush(self) -> None:
                return None

            async def finish(self) -> None:
                if len(self.data) != exact_size:
                    raise RuntimeError("stream size mismatch")
                owner.files[remote_path] = bytes(self.data)
                self.finished = True

            async def abort(self) -> None:
                self.finished = True

        return Stream()

    async def remote_size(self, remote_path: str) -> int:
        return len(self.files[remote_path])

    async def exists(self, remote_path: str) -> bool:
        return remote_path in self.files

    async def move(self, source: str, destination: str) -> None:
        self.files[destination] = self.files.pop(source)

    async def remove(self, remote_path: str) -> None:
        self.removed.append(remote_path)
        self.files.pop(remote_path, None)

    async def list_staging_objects(self) -> list[tuple[int, str]]:
        result = []
        for name in self.files:
            parts = name.split("-", 2)
            if len(parts) == 3 and parts[0] == ".uploading" and parts[1].isdigit():
                result.append((int(parts[1]), name))
        return result
