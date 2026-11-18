"""Synthetic Bot observations for host and watchdog checks."""
from __future__ import annotations

import os
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace

from tgcloud.monitoring import readiness


class ReadinessFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.settings = SimpleNamespace(data_dir=self.root, storage_backend="clouddrive2", control_interval=3,
                                        min_free_disk_bytes=100, local_budget_bytes=1000, memory_hard_min_bytes=100,
                                        bot_token="SYNTHETIC_NEVER_EXPORT")
        self.service = SimpleNamespace(settings=self.settings, client=SimpleNamespace(is_connected=lambda: True),
                                       snapshot=SimpleNamespace(disk_free=10000, filesystems=(), memory_available=10000),
                                       destination_scope="target", destination_last_checked=time.time(),
                                       _destination_ready=lambda: True, _destination_next_probe_at=0,
                                       db=SimpleNamespace(is_paused=lambda: False, used_local_bytes=lambda: 10),
                                       download_tasks={}, upload_tasks={})
        # A known past instant avoids filesystem/clock rounding at now == mtime.
        observed = time.time() - 1
        for name in ("heartbeat", "resource-heartbeat"):
            path = self.root / name
            path.touch()
            os.utime(path, (observed, observed))

    def report(self):
        readiness.publish(self.service, {"queued": 2})
        return readiness.read(self.root, now=time.time())
