"""Offline manager bundles work without private archives or Bot dependencies."""
from __future__ import annotations

import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import nullcontext
from pathlib import Path
from unittest.mock import Mock, patch

from local_project_fixture import source

from tgcloud.deployment import runtime
from tgcloud.version import VERSION

ROOT = Path(__file__).resolve().parents[1]


class HostBundleTests(unittest.TestCase):
    def test_expanded_offline_bundle_keeps_all_script_help_entries_without_bot_dependencies(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary).resolve()
            root, command = directory / "manager", directory / "bin/tgcloud"
            with patch.object(runtime, "publication_lock", return_value=nullcontext()):
                marker = runtime.install(source=ROOT, root=root, command=command)
            bundle = next((root / "runtime").iterdir())
            self.assertEqual(runtime.verify(bundle), marker)
            self.assertIn("src/tgcloud/compose/management.py", marker["files"])
            for entry in ("install", "lan", "manage", "listen", "webdav", "uninstall", "compose"):
                with self.subTest(command=entry):
                    result = subprocess.run([sys.executable, "-I", "-S", "-B",
                        str(bundle / "src/tgcloud/deployment/runtime.py"), entry, "--help"],
                        cwd=directory, capture_output=True, text=True, encoding="utf-8", timeout=20, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("usage:", result.stdout)
            self.assertEqual(runtime.verify(bundle), marker)
            self.assertFalse((directory / "data").exists())

    def test_new_clean_bundle_imports_backup_and_runs_preview_without_site_packages(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary).resolve()
            content = runtime.files(ROOT)
            self.assertIn("src/tgcloud/persistence/bundle.py", content)
            self.assertIn("src/tgcloud/domain/naming.py", content)
            target = directory / "clean-runtime"
            for name, body in content.items():
                path = target / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(body)
            # No .venv, packaging resources, legacy, archives or instance data.
            result = subprocess.run([sys.executable, "-S", "-B", str(target / "scripts/run_source.py"), "naming-preview",
                                     "--input", str(ROOT / "tests/fixtures/naming-preview.json"), "--json"],
                                    cwd=directory, capture_output=True, text=True, timeout=15, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["source"], "caption")
            code = "import sys;sys.path.insert(0," + repr(str(target / "src")) + ");import tgcloud.deployment.backup,tgcloud.deployment.readiness;assert 'telethon' not in sys.modules;assert 'psutil' not in sys.modules"
            result = subprocess.run([sys.executable, "-S", "-B", "-c", code], cwd=directory,
                                    capture_output=True, text=True, timeout=15, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_version_without_bot_dependencies_or_current_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            result = subprocess.run([sys.executable, "-I", "-S", "-B", str(ROOT / "scripts/run_source.py"), "--version"],
                                    cwd=temporary, capture_output=True, text=True, timeout=15, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "TGCloud CLI v" + VERSION)

    def test_installer_does_not_report_cli_success_without_version_and_commands(self):
        release = source()
        for result in (
            {"version": "0.0.0", "matches": False, "state": "failed", "commands": {}},
            {"version": VERSION, "matches": True, "state": "failed", "commands": {"listen": "failed"}},
        ):
            output = Mock()
            with (
                patch.object(runtime, "install") as install,
                patch.object(runtime, "installed_status", return_value=result),
                self.assertRaisesRegex(RuntimeError, "未开始升级 Bot"),
            ):
                runtime.install_release(ROOT, release, output)
            install.assert_called_once_with(source=ROOT)
            self.assertNotIn("本地管理命令已安装", str(output.call_args_list))

    def test_original_project_manager_is_rejected_without_executing_old_code(self):
        git = shutil.which("git")
        self.assertIsNotNone(git)
        commit = "42e2c372823a085c36a7a7597ce775441598aaf9"

        def blob(name):
            return subprocess.check_output([git, "-C", str(ROOT), "show", commit + ":" + name])

        names = json.loads(blob("packaging/bootstrap-manifest.json"))["files"]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            root.chmod(0o700)
            hashes = {}
            for name in names:
                body = blob(name)
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(body)
                hashes[name] = hashlib.sha256(body).hexdigest()
            marker = {"schema": 1, "owner": "tgcloud.cli", "version": "1.4.2",
                      "origin": {"kind": "local-source"}, "files": hashes}
            (root / "installation.json").write_text(json.dumps(marker))
            with patch.object(runtime.runpy, "run_path") as execute, self.assertRaisesRegex(ValueError, "不接管旧工具"):
                runtime.verify(root)
            execute.assert_not_called()
            self.assertFalse((root / "src/tgcloud/persistence").exists())
