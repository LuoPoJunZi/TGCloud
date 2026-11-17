"""No-byte-progress guard; cancellation is joined before a transfer is replaced."""
from __future__ import annotations

import asyncio
import math
from time import monotonic
from typing import Any

from tgcloud.domain.errors import DownloadStalledError

IDLE_SECONDS = 300
CANCEL_SECONDS = 10
CHECK_SECONDS = 1.0


class DownloadGuard:
    def __init__(self, owner, task_id: int):
        self.owner, self.task_id = owner, task_id
        self.at = monotonic()
        self.until = self.at
        self.count = 0
        self.sink_wait = False
        self.stalled = self.cancelling = False
        self._cancel_sent = False

    def advance(self, count: int) -> None:
        if type(count) is int and count > self.count:
            self.count = count
            self.at = monotonic()

    def waiting(self, seconds: int) -> None:
        # The existing scoped Telegram chunk policy permits at most 60s waits.
        if type(seconds) in (int, float) and math.isfinite(seconds) and 0 < seconds <= 60:
            self.until = max(self.until, monotonic() + seconds)

    def idle(self) -> float:
        return max(0.0, monotonic() - max(self.at, self.until))

    async def _join(self, worker) -> None:
        self.cancelling = True
        if not self._cancel_sent:
            worker.cancel()
            self._cancel_sent = True
        warned = interrupted = False
        while not worker.done():
            try:
                done, _ = await asyncio.wait({worker}, timeout=CANCEL_SECONDS)
            except asyncio.CancelledError:
                # Repeated user/shutdown cancellation must not detach a writer.
                interrupted = True
                continue
            if not done and not warned:
                self.owner.log.warning("任务 #%s 下载取消尚未结束；保留名额，等待旧协程退出", self.task_id)
                warned = True
        # Consume CancelledError or an exception raised during cleanup.
        await asyncio.gather(worker, return_exceptions=True)
        if interrupted:
            raise asyncio.CancelledError

    async def run(self, operation):
        records = getattr(self.owner, "_download_guards", None)
        if records is None:
            records = self.owner._download_guards = {}
        worker = asyncio.ensure_future(operation)
        records[self.task_id] = self
        try:
            while not worker.done():
                remaining = IDLE_SECONDS - self.idle()
                if self.sink_wait:
                    remaining = IDLE_SECONDS  # Destination backpressure is not a Telegram stall.
                if remaining <= 0:
                    self.stalled = True
                    from tgcloud.monitoring.observations import record_event
                    record_event(self.owner, "download_stalled")
                    self.owner.log.warning("任务 #%s 下载超过 %s 秒无数据进度；取消本次尝试并等待回收", self.task_id, IDLE_SECONDS)
                    await self._join(worker)
                    raise DownloadStalledError()
                await asyncio.wait({worker}, timeout=min(remaining, CHECK_SECONDS))
            return await worker
        except asyncio.CancelledError:
            await self._join(worker)
            raise
        finally:
            if records.get(self.task_id) is self:
                records.pop(self.task_id, None)

    def stream(self, target):
        return _GuardedStream(self, target)


class _GuardedStream:
    """Preserve the awaited sink write and its existing bounded buffering."""
    def __init__(self, guard: DownloadGuard, target):
        self.guard, self.target = guard, target

    async def write(self, data: bytes) -> int:
        self.guard.sink_wait = True
        try:
            written = await self.target.write(data)
            if written:
                self.guard.at = monotonic()
            return written
        finally:
            self.guard.sink_wait = False

    def __getattr__(self, name: str) -> Any:
        return getattr(self.target, name)


def note_wait(owner, task_id: int, seconds: int) -> None:
    guard = getattr(owner, "_download_guards", {}).get(task_id)
    if isinstance(guard, DownloadGuard):
        guard.waiting(seconds)


def observation(owner) -> dict:
    records = list(getattr(owner, "_download_guards", {}).values())
    idle = [guard.idle() for guard in records if not guard.sink_wait]
    return {"active": len(records),
            "stalled": sum(guard.stalled or not guard.sink_wait and guard.idle() >= IDLE_SECONDS for guard in records),
            "cancelling": sum(guard.cancelling for guard in records),
            "oldest_idle_seconds": min(10**12, int(max(idle, default=0))),
            "timeout_seconds": IDLE_SECONDS}
