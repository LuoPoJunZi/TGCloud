"""The main menu launches verified independent code, never its partial helper."""
from __future__ import annotations

import hashlib
import json
import os
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import nullcontext
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

from tests.fixtures import ROOT
from tests.fixtures.local_project import host_source
from tests.fixtures.local_project import source as project_source
from tgcloud.addons.external_webdav import entry, releases
from tgcloud.deployment import configuration, runtime
from tgcloud.deployment.instances import Instance
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.version import VERSION


class EntryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="tgcloud-webdav-entry-")
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name).resolve()
        self.directory.chmod(0o700)
        self.root = self.directory / "addon state"
        self.command = self.directory / "tgcloud-webdav"
        self.instance = Instance(PRODUCTS["clouddrive2"], self.directory / "custom-instance", True)
        self.instance.directory.mkdir(mode=0o700)
        self.instance.source_digest = project_source().digest
        self.ui, self.messages = Mock(), []
        self.ui.confirm.return_value = True
        # CI fixtures belong to the test user, not root. Simulate only that
        # ownership; preserve all real mode/path checks. Test the root policy
        # separately below rather than relaxing production validation.
        original = entry._check_ownership

        def fixture_owner(info, **options):
            if os.name == "posix" and info.st_uid == os.getuid():
                info = SimpleNamespace(st_uid=0, st_mode=info.st_mode)
            return original(info, **options)

        self.addCleanup(patch.stopall)
        patch.object(entry.config, "ROOT", self.root).start()
        patch.object(entry, "_check_ownership", side_effect=fixture_owner).start()
        patch.object(entry.os, "geteuid", return_value=0, create=True).start()
        checked = configuration.checked_directory
        patch.object(configuration, "checked_directory", side_effect=lambda value:
                     self.instance.directory if value == str(self.instance.directory) else checked(value)).start()

    def source(self, destination=None, *, legacy=False):
        root = destination or self.directory / "source"
        host_source(root)
        if legacy:
            (root / releases.FEATURE).write_text(json.dumps({"api": 1, "proxy_schema": 2, "owner": releases.OWNER}))
            (root / "install-webdav.sh").write_bytes(b"#!/bin/sh\n")
        return root

    def bundle(self, *, legacy=False):
        source = self.source(legacy=legacy)
        checksum = hashlib.sha256()
        for relative in runtime.files(source):
            checksum.update(relative.encode() + b"\0" + (source / relative).read_bytes())
        digest = checksum.hexdigest()
        bundle = self.root / "runtime" / digest
        bundle.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
        self.root.chmod(0o700)
        shutil.move(str(source), bundle)
        identity = {"schema": 1, "owner": releases.OWNER, "project": "tgcloud", "version": VERSION,
                    "origin": project_source().origin, "code_sha256": digest}
        if legacy:
            identity = {"source": "old-project", "version": "1.4.6", "code_sha256": digest}
        (bundle / "installation.json").write_text(json.dumps(identity))
        (bundle / "installation.json").chmod(0o600)
        self.command.write_text(entry.HEADER + "exec python3 -B " + shlex.quote(str(bundle / "scripts/run_source.py")) + ' webdav "$@"\n')
        self.command.chmod(0o700)
        return bundle

    def verify(self):
        return entry.installed(launcher=self.command, root=self.root)

    def test_historical_bundle_is_rejected_without_executing_old_code(self):
        self.bundle(legacy=True)
        with patch.object(entry.subprocess, "Popen") as execute, self.assertRaisesRegex(ValueError, "不属于此独立项目"):
            self.verify()
        execute.assert_not_called()

    def test_original_bundle_cannot_be_adopted_by_removing_old_shell_input(self):
        bundle = self.bundle(legacy=True)
        (bundle / "install-webdav.sh").unlink()
        with self.assertRaisesRegex(ValueError, "不属于此独立项目"):
            self.verify()

    def test_missing_launcher_returns_uninstalled(self):
        self.assertIsNone(self.verify())

    def test_changed_nested_code_is_rejected(self):
        bundle = self.bundle()
        (bundle / "src/tgcloud/addons/external_webdav/cli.py").write_bytes(b"# replaced\n")
        with self.assertRaisesRegex(ValueError, "来源清单不一致"):
            self.verify()

    def test_added_or_removed_code_changes_digest(self):
        bundle = self.bundle()
        path = bundle / "src/tgcloud/addons/external_webdav/extra.py"
        path.write_bytes(b"# unexpected\n")
        path.chmod(0o600)
        with self.assertRaisesRegex(ValueError, "来源清单不一致"):
            self.verify()
        path.unlink()
        (bundle / "src/tgcloud/addons/external_webdav/cli.py").unlink()
        with self.assertRaisesRegex(ValueError, "来源清单不一致|缺失|未知资源"):
            self.verify()

    def test_unknown_file_or_empty_directory_is_rejected(self):
        bundle = self.bundle()
        extra = bundle / ".env"
        extra.write_bytes(b"not-a-real-secret")
        extra.chmod(0o600)
        with self.assertRaisesRegex(ValueError, "未知资源"):
            self.verify()
        extra.unlink()
        (bundle / "unknown").mkdir(mode=0o700)
        with self.assertRaisesRegex(ValueError, "未知资源"):
            self.verify()

    def test_unknown_launcher_is_never_executed_or_overwritten(self):
        self.bundle()
        self.command.write_text("#!/bin/sh\nexit 0\n")
        with self.assertRaisesRegex(ValueError, "受管入口"):
            self.verify()

    def test_launcher_cannot_choose_another_runtime_root(self):
        bundle = self.bundle()
        self.command.write_text(entry.HEADER + "exec python3 -B " + shlex.quote(str(self.directory / bundle.name / "scripts/run_source.py")) + ' webdav "$@"\n')
        with self.assertRaisesRegex(ValueError, "受管布局"):
            self.verify()

    def test_symlink_launcher_is_rejected(self):
        self.bundle()
        original = Path.is_symlink
        with patch.object(Path, "is_symlink", lambda path: path == self.command or original(path)), \
                self.assertRaisesRegex(ValueError, "链接"):
            self.verify()

    def test_identity_digest_is_required_without_executing_old_code(self):
        bundle = self.bundle()
        (bundle / "installation.json").write_text(json.dumps({"code_sha256": "0" * 64}))
        with patch.object(entry.subprocess, "Popen") as execute, self.assertRaisesRegex(ValueError, "运行清单"):
            self.verify()
        execute.assert_not_called()

    def test_existing_runtime_is_offline_and_passes_selected_instance(self):
        bundle = self.bundle(legacy=False)
        installed = entry.installed
        process = Mock()
        process.wait.return_value = 0
        with patch.object(entry, "installed", side_effect=lambda: installed(launcher=self.command, root=self.root)), \
                patch.object(entry.releases, "download") as download, \
                patch.object(entry.subprocess, "Popen", return_value=process) as execute, \
                patch.dict(os.environ, {"TGCLOUD_SOURCE_ROOT": "not-the-independent-code"}):
            entry.open_menu(self.instance, self.ui, self.messages.append)
        download.assert_not_called()
        self.ui.confirm.assert_not_called()
        arguments = execute.call_args.args[0]
        self.assertEqual(arguments, [sys.executable, "-B", str(bundle / "scripts/run_source.py"), "webdav",
                                     "--edition", "clouddrive2", "--install-dir", str(self.instance.directory)])
        self.assertEqual(execute.call_args.kwargs["env"]["TGCLOUD_MANAGER_MENU"], "1")
        self.assertNotIn("TGCLOUD_SOURCE_ROOT", execute.call_args.kwargs["env"])
        self.assertNotIn("start_new_session", execute.call_args.kwargs)

    def test_declined_first_download_has_no_side_effect(self):
        self.ui.confirm.return_value = False
        with patch.object(entry, "installed", return_value=None), patch.object(entry.releases, "download") as download, \
                patch.object(entry.subprocess, "Popen") as execute:
            entry.open_menu(self.instance, self.ui, self.messages.append)
        download.assert_not_called()
        execute.assert_not_called()
        self.assertIn("不会自动启用", "\n".join(self.messages))

    def test_first_open_copies_complete_same_source_without_network(self):
        from tgcloud.addons.external_webdav.service import Service
        installed = entry.installed
        service = Service(self.instance.product, Mock(), self.messages.append, self.ui, root=self.root)
        service.locked = lambda: nullcontext()
        service.record = Mock(return_value=None)
        service.preflight = Mock(return_value=(self.instance, {}))
        original = service.install_runtime
        service.install_runtime = lambda source, identity: original(source, identity, launcher=self.command)
        previous = os.umask(0o022)
        try:
            with patch.object(entry, "installed", side_effect=lambda: installed(launcher=self.command, root=self.root)), patch("tgcloud.addons.external_webdav.service.Service", return_value=service), patch.object(entry.config, "canonical"), patch.object(entry, "_run") as execute, patch.object(entry.releases, "download") as download:
                entry.open_menu(self.instance, self.ui, self.messages.append)
        finally:
            os.umask(previous)
        bundle = self.verify()
        self.assertIsNotNone(bundle)
        self.assertEqual(json.loads((bundle / "installation.json").read_text())["origin"], project_source().origin)
        execute.assert_called_once_with(bundle, self.instance, self.messages.append)
        download.assert_not_called()
        service.session.run.assert_not_called()

    def test_untrusted_source_environment_cannot_replace_the_verified_source(self):
        helper = self.directory / "partial-main"
        helper.mkdir()
        with patch.dict(os.environ, {"TGCLOUD_SOURCE_ROOT": str(helper)}), patch.object(entry, "installed", return_value=None), patch.object(entry.releases, "download") as download, patch.object(entry, "_run") as execute:
            self.ui.confirm.return_value = False
            entry.open_menu(self.instance, self.ui, self.messages.append)
        download.assert_not_called()
        execute.assert_not_called()
        self.assertFalse(self.command.exists())

    def test_unknown_installed_command_prevents_download_fallback(self):
        with patch.object(entry, "installed", side_effect=ValueError("unknown command")), \
                patch.object(entry.releases, "download") as download, patch.object(entry, "_run") as execute, \
                self.assertRaisesRegex(ValueError, "unknown command"):
            entry.open_menu(self.instance, self.ui, self.messages.append)
        self.ui.confirm.assert_not_called()
        download.assert_not_called()
        execute.assert_not_called()

    def test_child_exit_returns_without_reopening_the_old_runtime(self):
        previous = self.bundle()
        process = Mock()
        process.wait.return_value = 0
        with patch.object(entry, "installed", return_value=previous), patch.object(entry.subprocess, "Popen", return_value=process) as execute:
            entry.open_menu(self.instance, self.ui, self.messages.append)
        execute.assert_called_once()
        process.wait.assert_called_once_with()

    def test_same_version_different_source_still_requires_confirmation(self):
        previous = self.bundle()
        marker = previous / "installation.json"
        value = json.loads(marker.read_text())
        value["origin"]["source_sha256"] = "b" * 64
        marker.write_text(json.dumps(value))
        self.ui.confirm.return_value = False
        with patch.object(entry, "installed", return_value=previous), patch.object(entry, "_run") as execute:
            entry.open_menu(self.instance, self.ui, self.messages.append)
        self.ui.confirm.assert_called_once()
        execute.assert_not_called()
        self.assertEqual(json.loads(marker.read_text()), value)

    def test_other_project_runtime_stops_before_prompt_or_execution(self):
        self.bundle(legacy=True)
        installed = entry.installed
        with patch.object(entry, "installed", side_effect=lambda: installed(launcher=self.command, root=self.root)), patch.object(entry, "_run") as execute, self.assertRaises(ValueError):
            entry.open_menu(self.instance, self.ui, self.messages.append)
        self.ui.confirm.assert_not_called()
        execute.assert_not_called()

    def test_main_instance_source_mismatch_stops_before_install_or_credentials(self):
        self.instance.source_digest = "b" * 64
        with patch.object(entry, "installed", return_value=None), patch.object(entry, "_run") as execute, self.assertRaisesRegex(ValueError, "来源不同"):
            entry.open_menu(self.instance, self.ui, self.messages.append)
        execute.assert_not_called()
        self.ui.confirm.assert_not_called()

    def test_unrecognized_old_marker_never_replaces_current_command(self):
        previous = self.bundle(legacy=True)
        before = self.command.read_bytes()
        installed = entry.installed
        with patch.object(entry, "installed", side_effect=lambda: installed(launcher=self.command, root=self.root)), patch.object(entry, "_run") as execute, self.assertRaisesRegex(ValueError, "不属于此独立项目"):
            entry.open_menu(self.instance, self.ui, self.messages.append)
        self.assertTrue(previous.exists())
        self.assertEqual(self.command.read_bytes(), before)
        execute.assert_not_called()

    def test_failed_or_concurrent_update_never_replaces_current_command(self):
        previous = self.bundle()
        before = self.command.read_bytes()
        # A recognized prior source can be updated after confirmation, never before.
        value = json.loads((previous / "installation.json").read_text())
        value["origin"]["source_sha256"] = "b" * 64
        (previous / "installation.json").write_text(json.dumps(value))
        for changed in (False, True):
            service = Mock()
            service.locked.return_value = nullcontext()
            service.record.return_value = None
            service.preflight.return_value = (self.instance, {})
            service.install_runtime.side_effect = OSError("synthetic write failure")
            with patch.object(entry, "installed", side_effect=[previous, self.directory if changed else previous]), patch("tgcloud.addons.external_webdav.service.Service", return_value=service), patch.object(entry, "_run") as execute, self.assertRaises((OSError, ValueError)):
                entry.open_menu(self.instance, self.ui, self.messages.append)
            self.assertEqual(self.command.read_bytes(), before)
            execute.assert_not_called()
            if changed:
                service.install_runtime.assert_not_called()

    def test_interrupt_terminates_and_reaps_only_the_child(self):
        process = Mock()
        process.wait.side_effect = [KeyboardInterrupt, 130]
        process.poll.return_value = None
        with patch.object(entry.subprocess, "Popen", return_value=process), self.assertRaises(KeyboardInterrupt):
            entry._run(self.directory, self.instance, self.messages.append)
        process.terminate.assert_called_once_with()
        process.kill.assert_not_called()
        self.assertEqual(process.wait.call_count, 2)

    def test_unresponsive_child_is_killed_and_reaped(self):
        process = Mock()
        process.poll.return_value = None
        process.wait.side_effect = [subprocess.TimeoutExpired("test-child", 150), -9]
        entry._stop(process)
        process.terminate.assert_called_once_with()
        process.kill.assert_called_once_with()
        self.assertEqual(process.wait.call_count, 2)

    def test_absent_instance_and_invalid_edition_never_download(self):
        with patch.object(entry, "installed") as installed:
            self.instance.present = False
            with self.assertRaisesRegex(ValueError, "先安装"):
                entry.open_menu(self.instance, self.ui, self.messages.append)
        installed.assert_not_called()

    def test_record_for_another_custom_directory_never_opens_or_downloads(self):
        record = self.root / self.instance.product.key / "config.json"
        record.parent.mkdir(parents=True)
        record.write_text("{}")
        with patch.object(entry.config, "ROOT", self.root), \
                patch.object(entry.config, "read_record", return_value={"edition": "clouddrive2", "instance_dir": "/opt/another-instance"}), \
                patch.object(entry, "installed") as installed, patch.object(entry.releases, "download") as download, \
                self.assertRaisesRegex(ValueError, "目录不一致"):
            entry.open_menu(self.instance, self.ui, self.messages.append)
        installed.assert_not_called()
        download.assert_not_called()

    def test_verified_real_host_only_bundle_can_show_help_without_bot_dependencies(self):
        source = self.directory / "real-addon"
        for relative in releases.host_files(ROOT):
            target = source / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, target)
        result = subprocess.run([sys.executable, "-I", "-B", str(source / "scripts/run_source.py"), "webdav", "--help"],
                                cwd=self.directory, capture_output=True, text=True, encoding="utf-8", check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("外部 WebDAV", result.stdout)
        self.assertFalse((source / "src/tgcloud/telegram").exists())


class OwnershipPolicyTests(unittest.TestCase):
    def test_root_and_private_permissions_remain_required(self):
        with patch.object(entry.os, "name", "posix"):
            for uid, mode, private in ((1000, 0o600, True), (0, 0o640, True), (0, 0o622, False)):
                with self.subTest(uid=uid, mode=mode, private=private), self.assertRaises(ValueError):
                    entry._check_ownership(SimpleNamespace(st_uid=uid, st_mode=mode), private=private)
            entry._check_ownership(SimpleNamespace(st_uid=0, st_mode=0o600), private=True)
            entry._check_ownership(SimpleNamespace(st_uid=0, st_mode=0o755))


if __name__ == "__main__":
    unittest.main()
