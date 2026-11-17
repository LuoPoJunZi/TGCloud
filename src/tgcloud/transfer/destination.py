from __future__ import annotations

import asyncio
import time

from tgcloud.monitoring.observations import record_event
from tgcloud.transfer.ports import DestinationProbe


async def _probe_destination_once(self) -> None:
    lock = getattr(self, "_destination_probe_lock", None)
    if lock is None:
        lock = asyncio.Lock()
        self._destination_probe_lock = lock
    async with lock:
        failure_kind = ""
        try:
            probe_method = getattr(self.rclone, "probe", None)
            if callable(probe_method):
                probe = await asyncio.wait_for(probe_method(), 30)
            else:
                accessible = await asyncio.wait_for(self.rclone.healthy(), 30)
                probe = DestinationProbe(
                    accessible,
                    "unknown",
                    "目录探测通过，不代表可写"
                    if accessible else "目录访问失败，请核对凭据与网络",
                )
            self.destination_healthy = probe.accessible
            self.destination_scope = probe.scope
            self.destination_error = probe.detail
            failure_kind = getattr(probe, "failure_kind", "")
        except TimeoutError:
            self.destination_healthy = False
            self.destination_scope = "unknown"
            self.destination_error = "目录探测超时"
        except Exception:
            self.destination_healthy = False
            self.destination_scope = "unknown"
            self.destination_error = "目录探测异常，请查看本机最近日志"
            self.log.exception("目的端探测异常")
        self.destination_last_checked = time.time()
        self.destination_failure_kind = failure_kind
        if not self.destination_healthy:
            kind = "authentication" if failure_kind == "unauthorized" else failure_kind
            kind = kind if kind in {"authentication", "permission", "rate_limited", "server"} else "network"
            record_event(self, "destination_" + kind)


async def _destination_loop(self) -> None:
    while not self._stop.is_set():
        await self._probe_destination_once()
        await asyncio.sleep(self.settings.remote_health_interval)
