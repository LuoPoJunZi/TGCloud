"""Offline CD2 local Compose configuration, source identity and build boundaries."""
from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import tarfile
import tempfile
import unittest
from functools import lru_cache
from pathlib import Path
from types import SimpleNamespace as NS
from unittest.mock import Mock, patch

from tgcloud.compose import packaging, releases, runtime
from tgcloud.deployment import configuration, instances, local_source, uninstall
from tgcloud.deployment.profiles import PRODUCTS

ROOT = Path(__file__).resolve().parents[1]


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


class ComposeTests(unittest.TestCase):
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
        with patch.object(runtime.shutil, "disk_usage", return_value=NS(free=100 * 1024**3)), patch.object(runtime.os, "chown", create=True):
            return runtime.setup(path, ui or UI())

    def test_cd2_keeps_native_gateway_bot_security_and_isolated_local_context(self):
        files = local_bundle_files()
        info = json.loads(files["compose-bundle.json"])
        text = files["docker-compose.yml"].decode()
        self.assertEqual(set(PRODUCTS), {"clouddrive2"})
        self.assertEqual(text.count("image: " + info["image"]), 2)
        self.assertEqual(text.count("pull_policy: never"), 2)
        self.assertEqual(text.count("context: ./build-context"), 2)
        self.assertNotIn("context: .\n", text)
        self.assertNotIn("ghcr.io", text)
        for token in ('user: "10001:10001"', "network_mode: none", "cap_drop:", "privileged: true", "/dev/fuse", "pid: host",
                      "com.tgcloud.install-mode: compose", "${TGCLOUD_BIND_IP:-127.0.0.1}", '"/tmp:rw,noexec,nosuid,size=64m"'):
            self.assertIn(token, text)
        self.assertNotIn("docker.sock", text)
        for forbidden in (".env", "data/", "downloads/", ".git/", "bot.session", "rclone.conf"):
            self.assertFalse(any(name.startswith("build-context/" + forbidden) for name in files))

    def test_one_archive_reproducible_and_checksums_cover_cd2_only(self):
        archives = packaging.build_bundles(ROOT, self.root / "one")
        other = packaging.build_bundles(ROOT, self.root / "two")
        self.assertEqual(len(archives), 1)
        self.assertEqual(archives[0].read_bytes(), other[0].read_bytes())
        self.assertIn(hashlib.sha256(archives[0].read_bytes()).hexdigest(), (self.root / "one/COMPOSE-SHA256SUMS.txt").read_text())
        with tarfile.open(archives[0]) as tar:
            members = list(tar)
            prefix = archives[0].name.removesuffix(".tar.gz") + "/"
            self.assertEqual({m.name.removeprefix(prefix) for m in members}, set(local_bundle_files()))
            self.assertEqual(next(m.mode for m in members if m.name.endswith("/https.sh")), 0o755)
        with self.assertRaises(FileExistsError):
            packaging.build_bundles(ROOT, self.root / "one")

    def test_foreign_image_commit_edition_and_legacy_render_are_rejected(self):
        digest = "a" * 64
        for image in ("latest", "ghcr.io/owner/image:v1", "tgcloud-local:" + "b" * 32):
            with self.assertRaises(ValueError):
                packaging.render(ROOT, "clouddrive2", image, source_digest=digest)
        for changes in ({"edition": "unsupported"}, {"legacy": True}):
            with self.assertRaises(ValueError):
                packaging.render(ROOT, changes.get("edition", "clouddrive2"), packaging.image_name(digest), source_digest=digest, legacy=changes.get("legacy", False))
        with self.assertRaises(ValueError):
            packaging.bundle_files(ROOT, commit="b" * 40, source_digest=digest)

    def test_initialization_idempotent_preserves_secrets_and_runtime_data(self):
        path = self.candidate()
        self.assertTrue(self.initialize(path))
        before = (path / ".env").read_bytes()
        (path / "data/retained.txt").write_text("synthetic existing data")
        ui = UI()
        self.assertFalse(self.initialize(path, ui))
        self.assertEqual(ui.questions, [])
        self.assertEqual((path / ".env").read_bytes(), before)
        self.assertTrue((path / "data/retained.txt").is_file())
        config = runtime.saved_config(path, "clouddrive2")
        self.assertEqual(runtime.validate_config(path, config, "clouddrive2").storage_backend, "clouddrive2")

    def test_interrupted_empty_layout_can_be_retried(self):
        path = self.candidate()
        (path / "config/rclone").mkdir(parents=True)
        (path / "clouddrive/config").mkdir(parents=True)
        self.assertTrue(self.initialize(path))

    def test_unknown_data_and_script_installations_are_never_adopted(self):
        for name in ("data", "app", "unsupported"):
            with self.subTest(name=name), tempfile.TemporaryDirectory(dir=self.root) as temp:
                path = write_candidate(Path(temp) / "bundle")
                (path / name).mkdir()
                (path / name / "retained").write_bytes(b"unchanged")
                with self.assertRaises(ValueError):
                    self.initialize(path)
                self.assertEqual((path / name / "retained").read_bytes(), b"unchanged")
                self.assertFalse((path / ".env").exists())

    def test_incomplete_private_configuration_is_not_regenerated(self):
        path = self.candidate()
        (path / ".env").write_bytes(b"synthetic=retained")
        with self.assertRaises(ValueError):
            self.initialize(path)
        self.assertEqual((path / ".env").read_bytes(), b"synthetic=retained")

    def test_disk_pressure_rejects_before_any_private_file(self):
        path = self.candidate()
        with patch.object(runtime.shutil, "disk_usage", return_value=NS(free=1)), self.assertRaises(ValueError):
            runtime.setup(path, UI())
        self.assertFalse((path / ".env").exists())

    def test_commit_failure_does_not_leave_a_new_config(self):
        path = self.candidate()
        original = runtime.write_exclusive
        def fail_marker(target, content):
            if target.name == runtime.MARKER:
                raise KeyboardInterrupt()
            original(target, content)
        with patch.object(runtime, "write_exclusive", side_effect=fail_marker), self.assertRaises(KeyboardInterrupt):
            self.initialize(path)
        self.assertFalse((path / ".env").exists())
        self.assertTrue(self.initialize(path))

    def test_configuration_and_metadata_mismatch_stop_safely(self):
        path = self.candidate()
        self.initialize(path)
        config = runtime.saved_config(path, "clouddrive2")
        for changes in ({"TGCLOUD_STORAGE_BACKEND": "unsupported"}, {"TGCLOUD_INSTALL_MODE": "script"}, {"TGCLOUD_BIND_IP": "0.0.0.0"}):
            with self.assertRaises(ValueError):
                runtime.validate_config(path, config | changes, "clouddrive2")
        metadata = json.loads((path / "compose-bundle.json").read_text())
        for changes in ({"version": "9.0.0"}, {"format": 3}, {"format": True}, {"edition": "unsupported"},
                        {"project": "TGCloud"}, {"source_sha256": "f" * 64}, {"image": "ghcr.io/owner/image:v1"}, {"commit": "a" * 40}):
            (path / "compose-bundle.json").write_text(json.dumps(metadata | changes))
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                runtime.metadata(path)

    def test_public_payload_changes_unknown_files_and_changed_sources_rejected(self):
        path = self.candidate()
        for relative in ("build-context/Dockerfile", "https-tools/src/tgcloud/version.py", "local-source/src/tgcloud/version.py", "docker-compose.yml"):
            target = path / relative
            original = target.read_bytes()
            target.write_bytes(original + b"\n# altered\n")
            with self.subTest(relative=relative), self.assertRaises(ValueError):
                runtime.metadata(path)
            target.write_bytes(original)
        extra = path / "build-context/.env"
        extra.write_text("SYNTHETIC=never include private data")
        with self.assertRaises(ValueError):
            runtime.metadata(path)
        extra.unlink()
        (path / "build-context/unregistered").mkdir()
        with self.assertRaises(ValueError):
            runtime.metadata(path)

    def test_candidate_copy_reverifies_and_never_downloads(self):
        path = self.candidate()
        copied = releases.candidate(path, self.root / "copied")
        self.assertEqual(runtime.metadata(copied)["source_sha256"], runtime.metadata(path)["source_sha256"])
        (path / "build-context/Dockerfile").write_bytes(b"FROM foreign\n")
        with self.assertRaises(ValueError):
            releases.candidate(path, self.root / "rejected")
        self.assertFalse((self.root / "rejected").exists())
        with self.assertRaises(ValueError):
            releases.download("clouddrive2", self.root)

    def test_loopback_and_private_ipv4_only(self):
        for value in ("127.0.0.1", "192.168.26.5", "10.0.0.8", "172.16.1.3"):
            self.assertEqual(runtime.lan_address(value), value)
        for value in ("0.0.0.0", "8.8.8.8", "169.254.1.2", "::1", "https://host"):
            with self.assertRaises(ValueError):
                runtime.lan_address(value)

    def test_host_cli_refuses_direct_compose_identity(self):
        product = PRODUCTS["clouddrive2"]
        with patch.object(instances.configuration, "checked_directory", return_value=self.root), patch.object(instances.configuration, "check_product_directory"), patch.object(instances, "inspect_container", return_value={"labels": {"com.tgcloud.install-mode": "compose"}}), self.assertRaisesRegex(ValueError, "Compose"):
            instances.discover(product, {product.bot_container, product.storage_container}, Mock(), Mock())

    def test_native_private_config_permissions_are_not_weakened(self):
        if os.name == "posix":
            for uid, mode in ((1001, 0o600), (0, 0o644)):
                with self.assertRaises(ValueError):
                    self.private_check(NS(stat=lambda uid=uid, mode=mode: NS(st_uid=uid, st_mode=mode)))
            self.private_check(NS(stat=lambda: NS(st_uid=0, st_mode=0o600)))

    def test_uninstaller_refuses_compose_even_if_standard_names_match(self):
        product = PRODUCTS["clouddrive2"]
        with self.assertRaisesRegex(uninstall.UninstallError, "Compose"):
            uninstall.core_identity(product, product.bot_container, {"labels": {"com.tgcloud.install-mode": "compose"}})

    def test_help_and_local_builder_use_standard_library(self):
        code = "import runpy,sys;sys.argv=['compose','--help'];runpy.run_module('tgcloud.compose',run_name='__main__')"
        result = subprocess.run([sys.executable, "-S", "-B", "-c", code], env=dict(os.environ, PYTHONPATH=str(ROOT / "src")), capture_output=True, text=True, timeout=20, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("setup", result.stdout)
        result = subprocess.run([sys.executable, "-I", "-S", "-B", str(ROOT / "scripts/build_compose.py"), "--help"], capture_output=True, text=True, timeout=20, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--output", result.stdout)
