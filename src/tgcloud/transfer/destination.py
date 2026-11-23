from __future__ import annotations

import asyncio
import time

from tgcloud.monitoring.observations import record_event
from tgcloud.transfer.ports import DestinationProbe


async def _probe_destination_once(self, *, respect_cooldown: bool = False) -> None:
    lock = getattr(self, "_destination_probe_lock", None)
    if lock is None:
        lock = asyncio.Lock()
        self._destination_probe_lock = lock
    async with lock:
        if respect_cooldown and float(getattr(self, "_destination_next_probe_at", 0.0) or 0.0) > time.monotonic():
            return
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
        except (TimeoutError, asyncio.TimeoutError):
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
        if self.destination_healthy:
            self._destination_rate_limit_failures = 0
            self._destination_next_probe_at = 0.0
        else:
            if failure_kind == "rate_limited":
                failures = int(getattr(self, "_destination_rate_limit_failures", 0) or 0) + 1
                self._destination_rate_limit_failures = failures
                cooldown = min(600, max(60, 60 * (2 ** (failures - 1))))
                self._destination_next_probe_at = time.monotonic() + cooldown
            elif failure_kind in {"unauthorized", "permission"}:
                self._destination_rate_limit_failures = 0
                interval = float(getattr(getattr(self, "settings", None), "remote_health_interval", 30) or 30)
                self._destination_next_probe_at = time.monotonic() + max(60.0, interval)
            else:
                self._destination_rate_limit_failures = 0
            kind = "authentication" if failure_kind == "unauthorized" else failure_kind
            kind = kind if kind in {"authentication", "permission", "rate_limited", "server"} else "network"
            record_event(self, "destination_" + kind)


async def _destination_loop(self) -> None:
    while not self._stop.is_set():
        await self._probe_destination_once(respect_cooldown=True)
        await asyncio.sleep(self.settings.remote_health_interval)
