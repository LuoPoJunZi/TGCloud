"""Human terminal layout and documented release-download flow, using fixtures only."""
from __future__ import annotations

import io
import os
import re
import subprocess
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import Mock, patch

from tests.fixtures import ROOT
from tests.fixtures.shell import BASH
from tgcloud.cli import management
from tgcloud.cli.presentation import (
    InstallerLog,
    display_width,
    page_heading,
    status_rows,
    terminal_text,
)
from tgcloud.compose import management as compose_management
from tgcloud.deployment.host import SafeLog
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.version import VERSION


class TerminalLayoutTests(unittest.TestCase):
    def test_short_heading_has_no_detached_product_name_and_status_keeps_bot_version(self):
        product = PRODUCTS["clouddrive2"]
        self.assertEqual(page_heading("安装与升级", product), "\n── 安装与升级 ──\n")
        self.assertIn("Bot 版本：v1.2.3", page_heading("查看运行状态", product, "1.2.3"))

    def test_main_menu_shows_tool_version_once_but_preserves_instance_version(self):
        from tgcloud.deployment.instances import Instance
        messages = []
        instance = Instance(PRODUCTS["clouddrive2"], Path("/opt/tgcloud"), True, "0.0.1", True)
        ui = Mock()
        ui.ask.return_value = "0"
        manager = management.Management(instance.product, Mock(), InstallerLog(SafeLog(messages.append)), ui)
        with patch.object(manager, "discover", return_value=instance):
            self.assertEqual(manager.run(), 0)
        text = "\n".join(messages)
        self.assertEqual(re.findall(r"(?<![\d.])v?\d+\.\d+\.\d+(?![\d.])", text), ["v" + VERSION])
        self.assertIn("Bot 状态：运行中", text)
        self.assertEqual(instance.version, "0.0.1")

    def test_installation_and_bot_states_are_distinct_in_all_startup_phases(self):
        from tgcloud.deployment.instances import Instance
        cases = ((False, False, False, "未安装", "未运行"),
                 (True, False, True, "已初始化，待启动服务", "未运行"),
                 (True, False, False, "已安装", "未运行"),
                 (True, True, False, "已安装", "运行中"))
        for present, running, initialized, installation, bot in cases:
            with self.subTest(installation=installation, bot=bot):
                messages = []
                instance = Instance(PRODUCTS["clouddrive2"], Path("/opt/tgcloud"), present, VERSION, running)
                if initialized:
                    instance.deployment_mode, instance.container_ids = "compose", (None, None)
                ui = Mock()
                ui.ask.return_value, ui.confirm.return_value = "0", False
                manager = management.Management(instance.product, Mock(), InstallerLog(SafeLog(messages.append)), ui)
                with patch.object(manager, "discover", return_value=instance):
                    self.assertEqual(manager.run(), 0)
                text = "\n".join(messages)
                self.assertIn("安装状态：" + installation, text)
                self.assertIn("Bot 状态：" + bot, text)
                self.assertNotIn("Bot 状态：未安装", text)

    def test_compose_defaults_follow_the_actual_deployment_disk(self):
        from types import SimpleNamespace

        from tests.fixtures.compose import UI
        from tgcloud.compose import runtime
        for total, free, budget, reserve in ((100, 90, "38", "20"), (200, 180, "80", "40")):
            with self.subTest(total=total), redirect_stdout(io.StringIO()) as output:
                config = runtime.collect(UI(), PRODUCTS["clouddrive2"],
                    disk=SimpleNamespace(total=total * 1024**3, free=free * 1024**3))
            self.assertEqual((config["LOCAL_TEMP_BUDGET_GB"], config["MIN_FREE_DISK_GB"]), (budget, reserve))
            self.assertIn("按实际部署磁盘计算", output.getvalue())
            self.assertIn("不检测宿主 CPU", output.getvalue())

    def test_https_result_is_structured_without_altering_raw_machine_output(self):
        raw = ("VPS 本机 https://cloud.example.test 自检通过；证书到期日 2027-01-01，剩余约 45 天。"
               "公网连通性仍需从你的电脑或外部网络单独验收。\n最近续期检查：尚无记录")
        messages = []
        InstallerLog(SafeLog(messages.append))(raw)
        text = "\n".join(messages)
        for line in ("管理地址：https://cloud.example.test", "本机 HTTPS：通过", "证书到期：2027-01-01",
                     "证书剩余：45 天", "公网访问：待电脑或外部网络验收。", "最近续期检查：尚无记录"):
            self.assertIn(line, text.splitlines())
        machine = []
        InstallerLog(SafeLog(machine.append), friendly=False)(raw)
        self.assertEqual(machine, [raw])

    def test_dns_failure_has_separate_reason_advice_records_and_ports(self):
        raw = ("TGCLOUD_CLI_RESULT=FAILED：首次安装 HTTPS 前置检查未通过："
               "VPS 当前没有解析到该域名的 A/AAAA 记录。请等待 DNS 生效；Cloudflare 首次签发时使用“仅 DNS”。\n"
               "DNS A：无；AAAA：无；当前 VPS：203.0.113.10\n80 端口：空闲；443 端口：空闲")
        messages = []
        InstallerLog(SafeLog(messages.append))(raw)
        text = "\n".join(messages)
        for line in ("[错误] 首次安装 HTTPS 前置检查未通过", "原因：VPS 当前没有解析到该域名的 A/AAAA 记录。",
                     "处理建议：请等待 DNS 生效", "DNS A：无", "DNS AAAA：无", "当前 VPS：203.0.113.10",
                     "80 端口：空闲", "443 端口：空闲"):
            self.assertIn(line, text.splitlines())
        self.assertNotIn("本地管理未启动", text)

    def test_chinese_wrapping_preserves_urls_paths_and_redacts_before_splitting(self):
        url = "https://cloud.example.test/path"
        path = "/root/TGCloud-Compose"
        text = terminal_text("检查提示：" + "这是需要分行显示的中文提示。" * 7 + " " + url + " " + path)
        self.assertTrue(all(display_width(line) <= 72 for line in text.splitlines()))
        self.assertIn(url, text)
        self.assertIn(path, text)
        secret = "私密凭据" * 30
        messages = []
        log = InstallerLog(SafeLog(messages.append))
        log.register({"TEST_SECRET": secret})
        log("检查说明：" + secret + "；完成")
        self.assertNotIn("私密", "\n".join(messages))
        self.assertIn("[REDACTED]", "\n".join(messages))

    def test_secret_redaction_does_not_discard_warning_classification(self):
        from tgcloud.cli.presentation import OperationReport
        messages = []
        log = InstallerLog(SafeLog(messages.append))
        log.register({"PASSWORD": "FAKE_PRIVATE_LAYOUT_PASSWORD"})
        report = OperationReport("clouddrive2", "TGCloud", "", VERSION, "install", "https")
        report.stage = "基础安装与升级"
        log.operation = report
        log("WARNING: password=FAKE_PRIVATE_LAYOUT_PASSWORD")
        self.assertEqual(report.alerts, {"unknown"})
        self.assertNotIn("FAKE_PRIVATE_LAYOUT_PASSWORD", "\n".join(messages))
        self.assertNotIn("FAKE_PRIVATE_LAYOUT_PASSWORD", repr(report))

    def test_long_chinese_sentences_break_at_clauses_without_isolated_punctuation(self):
        message = "将通过现有安装器安装 APT/Docker 依赖及固定镜像；配置只保存在本机私有文件。"
        rows = terminal_text(message).splitlines()
        self.assertEqual(rows, ["将通过现有安装器安装 APT/Docker 依赖及固定镜像；", "配置只保存在本机私有文件。"])
        self.assertFalse(any(row.strip() in "，。；！？" for row in rows))

    def test_status_rows_have_no_leading_space_or_column_padding(self):
        values = [("运行心跳", "通过"), ("Telegram 连接", "未通过"), ("WebDAV 最近目录探测", "冷却中")]
        self.assertEqual(status_rows(values), ["运行心跳：通过", "Telegram 连接：未通过", "WebDAV 最近目录探测：冷却中"])
        self.assertTrue(all(not row.startswith(" ") for row in status_rows(values)))

    def test_compose_export_label_is_six_characters_and_keeps_the_same_action(self):
        ui = Mock()
        ui.ask.return_value = "5"
        instance = Mock()
        manager = object.__new__(compose_management.ComposeManagement)
        manager.ui, manager.log = ui, Mock()
        manager.heading, manager.recheck, manager.proxy = Mock(), Mock(), Mock()
        manager.core = Mock()
        with patch.object(compose_management, "run_action") as action:
            self.assertTrue(manager.entry(instance))
        action.assert_called_once_with("export-existing", manager.core, manager.proxy.return_value, ui)
        self.assertIn("导出代理配置", manager.log.call_args.args[0])
        self.assertEqual(len("导出代理配置"), 6)

    def test_real_compose_initialization_plan_names_project_without_changing_backend(self):
        from tests.fixtures.compose import ComposeFixture
        fixture = ComposeFixture()
        fixture.setUp()
        try:
            candidate = fixture.candidate()
            with redirect_stdout(io.StringIO()) as output:
                self.assertTrue(fixture.initialize(candidate))
            text = output.getvalue()
            self.assertIn("── TGCloud Compose 初始化计划 ──", text)
            self.assertIn("任务暂存预算：32GB\n最少保留空间：40GB", text)
            self.assertNotIn("计划：CloudDrive2", text)
            from tgcloud.compose import runtime
            config = runtime.saved_config(candidate, "clouddrive2")
            self.assertEqual(config["TGCLOUD_STORAGE_BACKEND"], "clouddrive2")
            self.assertEqual(config["TGCLOUD_BIND_IP"], "127.0.0.1")
        finally:
            fixture.doCleanups()


class ComposeDownloadDocumentationTests(unittest.TestCase):
    def command(self):
        guide = (ROOT / "docs/compose-guide.md").read_text(encoding="utf-8")
        blocks = re.findall(r"```bash[ \t]*\n(.*?)\n```", guide, re.DOTALL)
        command = next((block for block in blocks
                        if "/releases/download/" in block and "COMPOSE-SHA256SUMS.txt" in block), None)
        self.assertIsNotNone(command, "Compose download example missing from the deployment guide")
        return command

    def run_command(self, *, failed_download="", checksum_failure=False, existing_instance=False):
        command = self.command()
        self.assertIsNotNone(BASH, "Bash is required to verify documented deployment commands")
        prefix = "\n".join((
            'mkdir() { printf "MKDIR %s\\n" "$*"; return ' + ("1" if existing_instance else "0") + '; }',
            'cd() { printf "CD %s\\n" "$*"; }',
            (
                'curl() { printf "DOWNLOAD %s\\n" "$*"; '
                'case "$TGCLOUD_TEST_FAILED_DOWNLOAD:$*" in '
                'archive:*.tar.gz*|checksum:*COMPOSE-SHA256SUMS.txt*) return 22 ;; esac; }'
            ),
            'sha256sum() { printf "SHA256 %s\\n" "$*"; return ' + ("1" if checksum_failure else "0") + '; }',
            'tar() { printf "TAR %s\\n" "$*"; }',
            'bash() { printf "INITIALIZATION_CALLED\\n"; }',
        ))
        return subprocess.run([BASH, "-c", prefix + "\n" + command], capture_output=True, text=True,
                              encoding="utf-8", timeout=20, check=False,
                              env=dict(os.environ, TGCLOUD_TEST_FAILED_DOWNLOAD=failed_download))

    def test_pinned_stable_assets_are_verified_before_extraction(self):
        online = (ROOT / "install.sh").read_text(encoding="utf-8")
        tag, = re.findall(r"^tgcloud_tag=(v(?:[0-9]+\.){2}[0-9]+)$", online, re.MULTILINE)
        archive = f"TGCloud-{tag}-compose.tar.gz"
        base = f"https://github.com/LuoPoJunZi/TGCloud/releases/download/{tag}"
        result = self.run_command()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(f"{base}/{archive}", result.stdout)
        self.assertIn(f"{base}/COMPOSE-SHA256SUMS.txt", result.stdout)
        self.assertRegex(result.stdout, r"(?s)SHA256 .*TAR ")

    def test_network_and_checksum_failures_stop_before_extraction(self):
        cases = ({"failed_download": "archive"},
                 {"failed_download": "checksum"},
                 {"checksum_failure": True})
        for options in cases:
            with self.subTest(options=options):
                result = self.run_command(**options)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("TAR ", result.stdout)
                self.assertNotIn("INITIALIZATION_CALLED", result.stdout)

    def test_existing_deployment_directory_is_never_extracted_over(self):
        result = self.run_command(existing_instance=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("DOWNLOAD ", result.stdout)
        self.assertNotIn("TAR ", result.stdout)
        self.assertNotIn("INITIALIZATION_CALLED", result.stdout)

    def test_loopback_examples_match_actual_default_instead_of_version_number(self):
        for name in ("docs/install.md", "docs/compose-guide.md"):
            text = (ROOT / name).read_text(encoding="utf-8")
            self.assertNotIn("127.0.0.2", text)
            self.assertIn("19798:127.0.0.1:19798", text)
            self.assertIn("http://127.0.0.1:19798", text)
