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
        ui.ask.side_effect = ["3", "0", "0"]
        manager = management.Management(instance.product, Mock(), InstallerLog(SafeLog(messages.append)), ui)
        with patch.object(manager, "discover", return_value=instance):
            self.assertEqual(manager.run(), 0)
        text = "\n".join(messages)
        self.assertEqual(re.findall(r"(?<![\d.])v?\d+\.\d+\.\d+(?![\d.])", text), ["v" + VERSION, "v" + VERSION])
        self.assertIn("TGCloud版本：v" + VERSION, text)
        self.assertIn("Bot  系统状态：运行中", text)
        self.assertIn("\n── 业务功能设置 ──\n\n1. Bot 用户管理", text)
        for unwanted in ("Go 原生宿主模式", "CloudDrive2 ·", "安装状态：", "管理工具（当前进程）", "Bot 版本："):
            self.assertNotIn(unwanted, text)
        self.assertEqual(instance.version, "0.0.1")

    def test_installation_and_bot_states_are_distinct_in_all_startup_phases(self):
        from tgcloud.deployment.instances import Instance
        cases = ((False, False, False, "未安装"),
                 (True, False, True, "已初始化，待启动服务"),
                 (True, False, False, "未运行"),
                 (True, True, False, "运行中"))
        for present, running, initialized, bot in cases:
            with self.subTest(bot=bot):
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
                self.assertIn("TGCloud版本：v" + VERSION, text)
                self.assertIn("Bot  系统状态：" + bot, text)
                self.assertNotIn("安装状态：", text)
                self.assertNotIn("管理工具（当前进程）", text)

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

    def test_all_webdav_and_maintenance_markers_render_in_plain_chinese(self):
        messages = []
        log = InstallerLog(SafeLog(messages.append))
        for marker in (
            "TGCLOUD_WEBDAV_AUTH=OK",
            "TGCLOUD_WEBDAV_LIST=OK",
            "TGCLOUD_WEBDAV=OK",
            "TGCLOUD_WEBDAV_WRITE=OK",
            "TGCLOUD_WEBDAV_SIZE=OK",
            "TGCLOUD_UPLOAD=OK",
            "TGCLOUD_WEBDAV_MOVE=OK",
            "TGCLOUD_RENAME=OK",
            "TGCLOUD_WEBDAV_DELETE=OK",
            "TGCLOUD_DELETE=OK",
            "TGCLOUD_DESTINATION=OK",
            "TGCLOUD_STAGE=RENEW_DRY_RUN",
            "TGCLOUD_STAGE=PROXY_BACKUP",
            "TGCLOUD_PROXY_TASK_CLEANUP=OK",
            "TGCLOUD_PROXY_CHECK=HTTPS 管理页:失败",
            "TGCLOUD_UNINSTALL_CHECK=OK；只是预检，不是已经卸载",
            "TGCLOUD_UNINSTALL_RESULT=CANCELLED；未执行卸载",
            "TGCLOUD_UNINSTALL_RESULT=OK；所选受管运行资源及路由已移除",
            "TGCLOUD_BACKUP_PREFLIGHT=OK；仅结构和数据库通过，不代表服务恢复验收",
        ):
            log(marker)
        text = "\n".join(messages)
        for expected in (
            "WebDAV 账号认证：通过",
            "WebDAV 目标目录访问：通过",
            "WebDAV 账号与目录检查：通过",
            "WebDAV 测试文件写入：通过",
            "WebDAV 临时文件大小复验：通过",
            "文件上传与大小复验：通过",
            "WebDAV 改名与最终文件复验：通过",
            "文件改名检查：通过",
            "WebDAV 测试文件删除检查：通过",
            "临时文件清理：通过",
            "WebDAV 写入验收：通过",
            "── 执行证书续期演练 ──",
            "── 处理共享代理备份 ──",
            "一次性演练容器清理：通过",
            "[错误] HTTPS 入口检查（HTTPS 管理页）：失败",
            "卸载预检：通过（仅预检，尚未执行卸载）",
            "卸载结果：已取消（未执行卸载）",
            "卸载结果：已完成（所选受管运行资源及路由已移除）",
            "备份隔离预检：通过",
        ):
            self.assertIn(expected, text)
        self.assertNotIn("TGCLOUD_", text)
        self.assertNotIn("阶段未识别", text)
        self.assertNotIn("未识别的检查结果", text)

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
        for name in ("README.md", "README_EN.md"):
            text = (ROOT / name).read_text(encoding="utf-8")
            doc_blocks = re.findall(r"```bash[ \t]*\n(.*?)\n```", text, re.DOTALL)
            self.assertIn(command, doc_blocks, f"Compose download example in {name} is out of sync with docs/compose-guide.md")
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
            'rm() { printf "RM %s\\n" "$*"; }',
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
        self.assertRegex(result.stdout, r"(?s)SHA256 .*TAR .*RM ")
        self.assertIn(f"RM -f {archive} COMPOSE-SHA256SUMS.txt", result.stdout)

    def test_network_and_checksum_failures_stop_before_extraction(self):
        cases = ({"failed_download": "archive"},
                 {"failed_download": "checksum"},
                 {"checksum_failure": True})
        for options in cases:
            with self.subTest(options=options):
                result = self.run_command(**options)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("TAR ", result.stdout)
                self.assertNotIn("RM ", result.stdout)
                self.assertNotIn("INITIALIZATION_CALLED", result.stdout)

    def test_existing_deployment_directory_is_never_extracted_over(self):
        result = self.run_command(existing_instance=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("DOWNLOAD ", result.stdout)
        self.assertNotIn("TAR ", result.stdout)
        self.assertNotIn("RM ", result.stdout)
        self.assertNotIn("INITIALIZATION_CALLED", result.stdout)

    def test_removing_downloaded_assets_allows_compose_bundle_verification(self):
        from tests.fixtures.compose import ComposeFixture
        from tgcloud.compose import runtime

        fixture = ComposeFixture()
        fixture.setUp()
        try:
            candidate = fixture.candidate()
            archive = f"TGCloud-v{VERSION}-compose.tar.gz"
            checksum = "COMPOSE-SHA256SUMS.txt"
            (candidate / archive).write_bytes(b"fixture")
            (candidate / checksum).write_text("0" * 64 + "  " + archive + "\n", encoding="ascii")
            with self.assertRaisesRegex(ValueError, "unknown file in Compose deployment root"):
                runtime.metadata(candidate)
            (candidate / archive).unlink()
            (candidate / checksum).unlink()
            self.assertEqual(runtime.metadata(candidate)["version"], VERSION)
        finally:
            fixture.doCleanups()

    def test_loopback_examples_match_actual_default_instead_of_version_number(self):
        for name in ("docs/install.md", "docs/compose-guide.md"):
            text = (ROOT / name).read_text(encoding="utf-8")
            self.assertNotIn("127.0.0.2", text)
            self.assertIn("19798:127.0.0.1:19798", text)
            self.assertIn("http://127.0.0.1:19798", text)
