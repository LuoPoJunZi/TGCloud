"""Offline Compose bundle and initialization fixtures."""
from __future__ import annotations

import tempfile
import unittest
from functools import lru_cache
from pathlib import Path
from types import SimpleNamespace as NS
from unittest.mock import patch

from tests.fixtures import ROOT
from tgcloud.compose import packaging, runtime
from tgcloud.deployment import configuration, local_source


@lru_cache(maxsize=1)
def local_bundle_files():
    with local_source.snapshot(ROOT) as source:
        return packaging.bundle_files(source.root, source_digest=source.digest, version=source.version)


def write_candidate(target):
    packaging.write_bundle(target, local_bundle_files())
    return target


class UI:
    def __init__(self):
        self.questions = []
    def ask(self, prompt, default="", secret=False, validate=configuration.plain):
        self.questions.append(prompt)
        values = {"Telegram API ID": "123456", "Telegram API Hash（隐藏输入）": "a" * 32,
                  "Bot Token（隐藏输入）": "123456789:FAKE_COMPOSE_TOKEN_FOR_TESTS_ONLY",
                  "管理员 Telegram 用户 ID（仅一个）": "123456789"}
        return validate(values.get(prompt, default))
    def confirm(self, _):
        return True
    def private(self, *_):
        pass


class ComposeFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        original = runtime.private_config_file
        def fixture_private(path):
            info = path.stat()
            original(NS(stat=lambda: NS(st_uid=0, st_mode=info.st_mode)))
        permission = patch.object(runtime, "private_config_file", side_effect=fixture_private)
        permission.start()
        self.addCleanup(permission.stop)
        self.private_check = original

    def candidate(self):
        return write_candidate(self.root / "clouddrive2")

    def initialize(self, path, ui=None):
        with patch.object(runtime.shutil, "disk_usage", return_value=NS(total=200 * 1024**3, free=100 * 1024**3)), patch.object(runtime.os, "chown", create=True):
            return runtime.setup(path, ui or UI())
