"""Installer UI regressions with fake operations and private data only."""
from __future__ import annotations

import asyncio
import io
import os
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from dataclasses import replace
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from tgcloud.cli import management, terminal, vps
from tgcloud.cli.presentation import InstallerLog, OperationReport
from tgcloud.deployment import configuration, instances
from tgcloud.deployment.host import SafeLog
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.storage import verify_destination as verifier
from tgcloud.version import VERSION

ROOT = Path(__file__).resolve().parents[1]


class SourceSelectionTests(unittest.TestCase):
    def manager(self, **options):
        ui = Mock()
        ui.ask.side_effect = AssertionError("source must not be requested again")
        product = PRODUCTS["clouddrive2"]
        instance = instances.Instance(product, Path(product.install_dir), False)
        return management.Management(product, Mock(), SafeLog(lambda _: None), ui,
                                     mode_hint="public", **options), instance, ui

    def test_running_source_ignores_cwd_and_environment_without_prompt(self):
        manager, instance, ui = self.manager()
        with tempfile.TemporaryDirectory() as temporary, patch.object(Path, "cwd", return_value=Path(temporary)), \
                patch.dict(os.environ, {"TGCLOUD_SOURCE_ROOT": temporary}), \
                patch.object(manager, "mode", return_value=None), patch.object(vps, "main", return_value=0) as execute:
            manager.upgrade(instance)
        self.assertEqual(execute.call_args.args[0], ["--edition", "clouddrive2", "--source-dir", str(ROOT),
                                                    "--install-dir", str(instance.directory)])
        ui.ask.assert_not_called()

    def test_partial_manager_does_not_fall_back_to_cwd(self):
        manager, instance, ui = self.manager()
        with tempfile.TemporaryDirectory() as temporary, patch.object(management, "SOURCE_ROOT", Path(temporary)), \
                patch.object(Path, "cwd", return_value=ROOT), patch.object(manager, "mode", return_value=None), \
                patch.object(vps, "main") as execute, self.assertRaisesRegex(ValueError, "完整 TGCloud 安装源码"):
            manager.upgrade(instance)
        execute.assert_not_called()
        ui.ask.assert_not_called()

    def test_explicit_source_works_from_a_partial_manager(self):
        manager, instance, ui = self.manager(source_dir=ROOT)
        with tempfile.TemporaryDirectory() as temporary, patch.object(management, "SOURCE_ROOT", Path(temporary)), \
                patch.object(manager, "mode", return_value=None), patch.object(vps, "main", return_value=0) as execute:
            manager.upgrade(instance)
        self.assertEqual(execute.call_args.args[0][3], str(ROOT))
        ui.ask.assert_not_called()

    def test_invalid_explicit_source_is_not_silently_replaced(self):
        with tempfile.TemporaryDirectory() as temporary:
            manager, instance, ui = self.manager(source_dir=Path(temporary))
            with patch.object(manager, "mode", return_value=None), patch.object(vps, "main") as execute, \
                    self.assertRaises(ValueError):
                manager.upgrade(instance)
        execute.assert_not_called()
        ui.ask.assert_not_called()

    def test_relative_source_is_rejected_before_any_installation(self):
        manager, instance, ui = self.manager(source_dir=Path("relative/source"))
        with patch.object(manager, "mode", return_value=None), patch.object(vps, "main") as execute, \
                self.assertRaises(ValueError):
            manager.upgrade(instance)
        execute.assert_not_called()
        ui.ask.assert_not_called()


class FailureMessageTests(unittest.TestCase):
    def test_running_operation_is_not_called_a_startup_failure(self):
        messages = []
        ui = Mock()
        with patch.object(management.preflight, "validate_host"), \
                patch.object(terminal, "Terminal", return_value=ui), patch.object(management, "LocalSession"), \
                patch.object(management, "SafeLog", return_value=SafeLog(messages.append)), \
                patch.object(management.Management, "run", side_effect=ValueError("DNS 前置检查未通过")):
            self.assertEqual(management.main([]), 1)
        self.assertIn("本次操作未完成：DNS 前置检查未通过", "\n".join(messages))
        self.assertNotIn("本地管理未启动", "\n".join(messages))
        ui.close.assert_called_once()

    def test_preflight_failure_still_reports_management_did_not_start(self):
        messages = []
        with patch.object(management.preflight, "validate_host", side_effect=ValueError("需要 root")), \
                patch.object(terminal, "Terminal") as ui, \
                patch.object(management, "SafeLog", return_value=SafeLog(messages.append)):
            self.assertEqual(management.main([]), 1)
        ui.assert_not_called()
        self.assertIn("本地管理未启动：需要 root", "\n".join(messages))


class ProtocolPresentationTests(unittest.TestCase):
    def test_chinese_display_does_not_replace_raw_success_evidence(self):
        messages = []
        log = InstallerLog(SafeLog(messages.append))
        report = OperationReport("clouddrive2", "CloudDrive2", "", VERSION, "install", "https")
        log.operation = report
        raw = "\n".join(("TGCLOUD_STAGE=GATEWAY", "TGCLOUD_STAGE=BOT", "TGCLOUD_STAGE=HEALTH",
                         "TGCLOUD_CODE=OK", "TGCLOUD_VERSION=" + VERSION, "TGCLOUD_BOT_HEALTH=healthy",
                         "TGCLOUD_RESULT=SUCCESS", "TGCLOUD_INSTALL_DIR=/opt/tgcloud")) + "\n"
        log(raw)
        report.observe_base(0, raw)
        text = "\n".join(messages)
        for expected in ("启动 CloudDrive2 网关", "构建并启动 TGCloud Bot", "检查 Bot 基础健康",
                         "运行代码校验：通过", "安装目录：/opt/tgcloud"):
            self.assertIn(expected, text)
        self.assertNotIn("TGCLOUD_", text)
        self.assertEqual(report.checks["code"], "ok")
        self.assertEqual(report.checks["health"], "ok")
        fresh = OperationReport("clouddrive2", "CloudDrive2", "", VERSION, "install", "https")
        fresh.observe_base(0, text)
        self.assertNotEqual(fresh.checks.get("code"), "ok")

    def test_machine_mode_keeps_the_original_markers(self):
        messages = []
        raw = "TGCLOUD_STAGE=BOT\nTGCLOUD_CODE=OK\nTGCLOUD_RESULT=SUCCESS\n"
        InstallerLog(SafeLog(messages.append), friendly=False)(raw)
        self.assertEqual("\n".join(messages) + "\n", raw)

    def test_failures_unknown_results_and_lan_compound_markers(self):
        messages = []
        log = InstallerLog(SafeLog(messages.append))
        log("TGCLOUD_CODE=MISMATCH\nTGCLOUD_CONFIG=FAILED\nTGCLOUD_UNRECOGNIZED=FAKE_PRIVATE_VALUE\nTGCLOUD_CLI_RESULT=UNRECOGNIZED")
        text = "\n".join(messages)
        self.assertIn("未通过", text)
        self.assertIn("[待核对]", text)
        self.assertNotIn("FAKE_PRIVATE_VALUE", text)
        self.assertNotIn("TGCLOUD_", text)
        log("TGCLOUD_LAN_BASE=OK；TGCLOUD_LAN_ENTRY=OK；HTTPS=NOT_APPLICABLE")
        self.assertIn("公网 HTTPS 不适用", messages[-1])
        self.assertNotIn("TGCLOUD_", messages[-1])


class WebDAVPresentationTests(unittest.TestCase):
    def render_verification(self, *, fail=False):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            settings = SimpleNamespace(data_dir=root, storage_backend="clouddrive2", destination_label="CloudDrive2")
            client = SimpleNamespace(**{name: AsyncMock() for name in
                ("verify_authentication", "prepare_destination", "upload", "move", "remove")},
                remote_size=AsyncMock(return_value=256), exists=AsyncMock(return_value=False))
            if fail:
                client.verify_authentication.side_effect = RuntimeError("认证失败")
            output = io.StringIO()
            with patch.object(verifier.Settings, "from_env", return_value=settings), \
                    patch.object(verifier, "RcloneClient", return_value=client), \
                    redirect_stdout(output), redirect_stderr(output):
                if fail:
                    code = verifier.main()
                else:
                    code = asyncio.run(verifier.async_main())
            self.assertEqual(list(root.iterdir()), [])
        messages = []
        InstallerLog(SafeLog(messages.append))(output.getvalue())
        return code, "\n".join(messages)

    def test_actual_verifier_success_protocol_has_no_unknown_ui_results(self):
        code, text = self.render_verification()
        self.assertEqual(code, 0)
        for label in ("WebDAV 账号认证：通过", "WebDAV 测试文件写入：通过", "WebDAV 改名与最终文件复验：通过",
                      "WebDAV 测试文件删除检查：通过", "WebDAV 写入验收：通过"):
            self.assertIn(label, text)
        self.assertNotIn("TGCLOUD_", text)
        self.assertNotIn("未识别", text)

    def test_actual_authentication_failure_keeps_the_failure_explanation(self):
        code, text = self.render_verification(fail=True)
        self.assertEqual(code, 1)
        self.assertIn("WebDAV 账号认证：未通过", text)
        self.assertIn("认证失败", text)
        self.assertNotIn("TGCLOUD_", text)
        self.assertNotIn("未识别", text)


class DirectorySafetyTests(unittest.TestCase):
    def test_orphaned_previous_data_prevents_empty_replacement(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            previous, current = root / "previous", root / "current"
            (previous / "data").mkdir(parents=True)
            database = previous / "data/tgcloud.db"
            database.write_bytes(b"synthetic database identity")
            product = replace(PRODUCTS["clouddrive2"], install_dir=str(current))
            session = Mock()
            with patch.object(instances, "PREVIOUS_INSTALL_DIR", previous), \
                    patch.object(configuration, "checked_directory", return_value=current), \
                    patch.object(configuration, "check_product_directory"), \
                    self.assertRaisesRegex(ValueError, "不移动或覆盖数据"):
                instances.discover(product, set(), session, SafeLog(lambda _: None))
            session.run.assert_not_called()
            self.assertEqual(database.read_bytes(), b"synthetic database identity")
            self.assertFalse(current.exists())

    def test_empty_previous_directory_allows_a_fresh_install(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            previous, current = root / "previous", root / "current"
            previous.mkdir()
            product = replace(PRODUCTS["clouddrive2"], install_dir=str(current))
            with patch.object(instances, "PREVIOUS_INSTALL_DIR", previous), \
                    patch.object(configuration, "checked_directory", return_value=current), \
                    patch.object(configuration, "check_product_directory"):
                result = instances.discover(product, set(), Mock(), SafeLog(lambda _: None))
            self.assertFalse(result.present)
            self.assertFalse(current.exists())
