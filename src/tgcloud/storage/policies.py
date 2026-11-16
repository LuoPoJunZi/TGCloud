"""Backend capability boundary, independent of rclone process execution."""
from __future__ import annotations

from typing import Protocol

from .clouddrive2 import CloudDrive2Commit


class CommitPolicy(Protocol):
    async def begin(self, service, task_id, task, safe_name, state, remote_temp, **fields) -> str: ...


def policy_for(settings) -> CommitPolicy:
    if getattr(settings, "storage_backend", "clouddrive2") != "clouddrive2":
        raise ValueError("storage backend must be clouddrive2")
    return CloudDrive2Commit()
