"""Synthetic instance snapshots and Docker service state."""
from __future__ import annotations

import shlex
import sqlite3
import tempfile
import unittest
from contextlib import closing
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch

from tgcloud.deployment import instances
from tgcloud.deployment.lifecycle import backup
from tgcloud.deployment.profiles import PRODUCTS


class Docker:
    def __init__(self, product):
        self.states = {product.bot_container: {"id": "a" * 64, "running": True},
                       product.storage_container: {"id": "b" * 64, "running": True}}
        self.calls = []
        self.fail = None

    def inspect(self, session, name):
        return dict(self.states[name])

    def run(self, command, **kwargs):
        words = shlex.split(command)
        verb, identity = words[1], words[-1]
        self.calls.append((verb, identity))
        if self.fail == (verb, identity):
            return 1, "not disclosed"
        state = next(item for item in self.states.values() if item["id"] == identity)
        state["running"] = verb == "start"
        return 0, ""


class SnapshotFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.root.chmod(0o700)

    def fixture(self, key="clouddrive2"):
        product = replace(PRODUCTS[key], install_dir=str(self.root / key))
        directory = Path(product.install_dir)
        for name in ("data", "config/rclone", "downloads", "logs", "clouddrive/config"):
            (directory / name).mkdir(parents=True, mode=0o700)
        for name in ("data/tgcloud.db", "data/bot.session"):
            with closing(sqlite3.connect(directory / name)) as conn, conn:
                conn.execute("CREATE TABLE tasks (state TEXT)")
                conn.execute("INSERT INTO tasks VALUES ('completed')")
        (directory / ".env").write_bytes(b"SYNTHETIC_BACKUP_FIXTURE=true\n")
        (directory / "docker-compose.yml").write_text("services: {}\n")
        (directory / "config/rclone/rclone.conf").write_text("[fixture]\n")
        gateway = directory / ("clouddrive/config")
        (gateway / "settings.json").write_text("{}")
        (directory / "tgcloud-release.json").write_text('{"tag":"v1.4.2"}')
        (directory / "downloads/keep.bin").write_bytes(b"keep original")
        instance = instances.Instance(product, directory, True, "1.4.2", True)
        docker = Docker(product)
        return instance, docker, self.root / (key + "-snapshots")

    def create(self, instance, docker, target):
        with patch.object(backup, "container", side_effect=docker.inspect):
            return backup.create(instance, docker, target)
