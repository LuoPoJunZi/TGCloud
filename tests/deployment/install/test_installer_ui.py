"""Installer UI regressions with fake operations and private data only."""
from __future__ import annotations

import asyncio
import io
import os
import tempfile
import unittest
from contextlib import ExitStack, redirect_stderr, redirect_stdout
from dataclasses import replace
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from tests.fixtures import ROOT
from tgcloud.cli import management, terminal, vps
from tgcloud.cli.presentation import InstallerLog, OperationReport
from tgcloud.deployment import configuration, instances
from tgcloud.deployment.host import SafeLog
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.storage import verify_destination as verifier
from tgcloud.version import VERSION


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
                patch.object(vps, "main") as execute, self.assertRaisesRegex(ValueError, r"完整 TGCloud 安装源码.*sudo bash install\.sh --lan"):
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
        report = OperationReport("clouddrive2", "TGCloud", "", VERSION, "install", "https")
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
        fresh = OperationReport("clouddrive2", "TGCloud", "", VERSION, "install", "https")
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


class ProjectPresentationTests(unittest.TestCase):
    def test_source_location_is_hidden_but_the_same_source_is_passed_to_installer(self):
        messages = []
        product = PRODUCTS["clouddrive2"]
        instance = instances.Instance(product, Path(product.install_dir), False)
        ui = Mock()
        ui.ask.side_effect = AssertionError("source must not be requested again")
        manager = management.Management(product, Mock(), SafeLog(messages.append), ui, mode_hint="public")
        with patch.object(manager, "mode", return_value=None), patch.object(vps, "main", return_value=0) as execute:
            manager.upgrade(instance)
        text = "\n".join(messages)
        self.assertIn("TGCloud 安装文件已准备就绪。", text)
        self.assertNotIn(str(ROOT), text)
        self.assertEqual(execute.call_args.args[0][3], str(ROOT))
        ui.ask.assert_not_called()

    def test_fresh_menu_uses_project_name_and_listener_label_has_six_characters(self):
        messages = []
        product = PRODUCTS["clouddrive2"]
        instance = instances.Instance(product, Path(product.install_dir), False)
        ui = Mock()
        ui.confirm.return_value = False
        manager = management.Management(product, Mock(), SafeLog(messages.append), ui)
        with patch.object(manager, "discover", return_value=instance), patch.object(manager, "upgrade") as execute:
            self.assertEqual(manager.run(), 0)
        text = "\n".join(messages)
        self.assertIn(f"TGCloud版本：v{VERSION}", text)
        self.assertIn("Bot  系统状态：未安装", text)
        self.assertNotIn("· 版本", text)
        self.assertNotIn("CloudDrive2 · 版本", text)
        ui.confirm.assert_called_once_with("TGCloud 尚未安装，是否进入安装向导？")
        execute.assert_not_called()
        label = {number: text for number, text, _ in management.SUBMENU_GROUPS["3"][1]}[2]
        self.assertEqual(label, "监听转存管理")
        self.assertEqual(len(label), 6)

    def test_project_branding_does_not_rename_backend_config_or_containers(self):
        product = PRODUCTS["clouddrive2"]
        config = configuration.new_config(product, {
            "api_id": "123456", "user_id": "123456789", "api_hash": "a" * 32,
            "bot_token": "123456789:FAKE_PREVIEW_ONLY_TOKEN_123456789", "budget": "20", "reserve": "8",
            "timezone": "Asia/Shanghai", "webdav_user": "tgcloud", "webdav_password": "FAKE_UI_TEST_PASSWORD",
            "target": "Telegram",
        })
        self.assertEqual(config["TGCLOUD_STORAGE_BACKEND"], "clouddrive2")
        self.assertEqual(config["TGCLOUD_DESTINATION_LABEL"], "CloudDrive2")
        self.assertEqual(config["TGCLOUD_RCLONE_REMOTE_NAME"], "cd2")
        self.assertEqual(product.bot_container, "tgcloud-clouddrive2-bot")
        self.assertEqual(product.storage_container, "tgcloud-clouddrive2")
        messages = []
        terminal.preview(SimpleNamespace(instance=instances.Instance(product, Path(product.install_dir), False),
                         action="install", config=config, configure_https=True, domain="cloud.example.test"),
                         SimpleNamespace(version=VERSION), SafeLog(messages.append))
        self.assertTrue(messages[0].startswith("TGCloud：首次安装"))

    def test_execution_report_identifies_the_project_without_changing_backend_identity(self):
        from tgcloud.deployment.lifecycle import install
        product = PRODUCTS["clouddrive2"]
        instance = instances.Instance(product, Path(product.install_dir), False)
        plan = SimpleNamespace(instance=instance, action="install", feedback=None)
        messages = []
        log = InstallerLog(SafeLog(messages.append))
        with install.execution_feedback(plan, SimpleNamespace(version=VERSION), Mock(), log, mode="https") as report:
            self.assertEqual(report.label, "TGCloud")
            self.assertEqual(report.edition, "clouddrive2")
        self.assertIn("TGCloud · 未安装", "\n".join(messages))

    def test_real_https_stages_render_chinese_without_promoting_success(self):
        from tgcloud.deployment.proxy.manager import DomainProxyManager
        from tgcloud.deployment.proxy.state import ProxyReport, empty_state
        messages, raw = [], []
        log = InstallerLog(SafeLog(messages.append))
        observation = OperationReport("clouddrive2", "TGCloud", "", VERSION, "install", "https")
        log.operation = observation
        def emit(line):
            raw.append(line)
            log(line)
        manager = DomainProxyManager(Mock(), PRODUCTS["clouddrive2"], {}, emit)
        with ExitStack() as stack:
            for name in ("_check_compose_owner", "_validate_environment", "_ensure_state_file", "_activate",
                         "_obtain_certificate", "_commit_state"):
                stack.enter_context(patch.object(manager, name))
            stack.enter_context(patch.object(manager, "read_state", return_value=empty_state()))
            stack.enter_context(patch.object(manager, "external_entry", return_value=None))
            stack.enter_context(patch.object(manager, "probe", return_value=Mock()))
            stack.enter_context(patch.object(manager, "_compose", return_value=(0, "")))
            stack.enter_context(patch.object(manager, "status", return_value=ProxyReport("cloud.example.test", "healthy", "", ())))
            manager._configure_locked("cloud.example.test", "")
        stages = [line for line in raw if line.startswith("TGCLOUD_STAGE=")]
        self.assertEqual(stages, ["TGCLOUD_STAGE=" + name for name in ("HTTPS_ENV", "ACME", "CERTIFICATE", "HTTPS_VERIFY")])
        text = "\n".join(messages)
        for title in ("检查 HTTPS 部署环境", "启动证书签发验证", "申请或复用 HTTPS 证书", "检查 HTTPS 管理入口"):
            self.assertIn("── " + title + " ──", text)
        self.assertNotIn("待核对", text)
        self.assertNotIn("TGCLOUD_", text)
        self.assertEqual(observation.checks, {})
        self.assertFalse(observation.completed)
        machine = []
        InstallerLog(SafeLog(machine.append), friendly=False)("\n".join(stages))
        self.assertEqual("\n".join(machine), "\n".join(stages))
        log("TGCLOUD_STAGE=UNRECOGNIZED_PRIVATE_STAGE")
        self.assertIn("[待核对]", messages[-1])
        self.assertNotIn("UNRECOGNIZED_PRIVATE_STAGE", messages[-1])
