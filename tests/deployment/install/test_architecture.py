"""Offline native-platform contracts; never use a real daemon or cloud account."""
from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from contextlib import ExitStack
from pathlib import Path
from unittest.mock import Mock, call, patch

from tests.fixtures import ROOT
from tgcloud.deployment import architecture as arch

IMAGE = "sha256:" + "a" * 64


class HostTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(patch.object(arch.platform, "system", return_value="Linux"))
        self.machine = self.stack.enter_context(patch.object(arch.platform, "machine", return_value="aarch64"))
        self.bits = self.stack.enter_context(patch.object(arch.struct, "calcsize", return_value=8))
        self.release = self.stack.enter_context(patch.object(Path, "read_text", return_value='ID=ubuntu\nPRETTY_NAME="Ubuntu 26.04.1 LTS"\n'))
        self.dpkg = self.stack.enter_context(patch.object(arch.subprocess, "run", return_value=Mock(returncode=0, stdout="arm64\n")))

    def test_only_two_native_architectures_and_aliases(self):
        for raw, expected in (("x86_64", "amd64"), ("amd64", "amd64"), ("AMD64", "amd64"), ("aarch64", "arm64"), ("arm64", "arm64")):
            with self.subTest(raw=raw):
                self.assertEqual(arch.normalize_architecture(raw), expected)
                self.machine.return_value = raw
                self.dpkg.return_value.stdout = expected + "\n"
                host = arch.detect_host()
                self.assertEqual(host.docker_platform, "linux/" + expected)
                lines = []
                host.describe(lines.append)
                self.assertIn("部署方式：原生 " + expected.upper(), "\n".join(lines))
        for raw in ("armv7l", "i686", "riscv64", "ppc64le", "", None):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                arch.normalize_architecture(raw)

    def test_mixed_or_32bit_userspace_os_and_dpkg_failure_stop(self):
        self.bits.return_value = 4
        with self.assertRaisesRegex(ValueError, "64 位"):
            arch.detect_host()
        self.bits.return_value = 8
        self.dpkg.return_value.stdout = "amd64"
        with self.assertRaisesRegex(ValueError, "不一致"):
            arch.detect_host()
        self.dpkg.return_value.stdout = "armhf"
        with self.assertRaises(ValueError):
            arch.detect_host()
        self.dpkg.side_effect = subprocess.TimeoutExpired("dpkg", 10)
        with self.assertRaisesRegex(ValueError, "dpkg"):
            arch.detect_host()
        self.release.return_value = "ID=alpine\n"
        with self.assertRaisesRegex(ValueError, "Debian/Ubuntu"):
            arch.detect_host()
        with patch.object(arch.platform, "system", return_value="Windows"), self.assertRaises(ValueError):
            arch.detect_host()

    def test_os_release_is_data_not_executed(self):
        self.release.return_value = 'ID=ubuntu\nPRETTY_NAME="$(touch /tmp/not-executed)"\n'
        self.assertIn("$(touch", arch.detect_host().name)
        self.dpkg.assert_called_once()
        self.assertEqual(self.dpkg.call_args.args[0], ["dpkg", "--print-architecture"])


class DockerTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(patch.dict(os.environ, {}, clear=True))
        self.stack.enter_context(patch.object(arch.platform, "machine", return_value="aarch64"))
        self.session = Mock()
        self.commands = []
        self.info = {"os": "linux", "arch": "aarch64", "security": [], "version": "29.0.0"}
        self.endpoint = "unix:///var/run/docker.sock"
        self.image = {"os": "linux", "arch": "arm64"}
        self.container = {"image": IMAGE, "running": True, "health": "healthy"}
        self.session.run.side_effect = self.query

    def query(self, command, **kwargs):
        self.commands.append(command)
        if command.startswith("command -v docker"):
            return 0, ""
        if command.startswith("docker context inspect"):
            return 0, json.dumps(self.endpoint)
        if command == arch.DOCKER_INFO:
            return 0, json.dumps(self.info)
        if command.startswith("docker image inspect"):
            return 0, json.dumps(self.image)
        if command.startswith("docker container inspect"):
            return 0, json.dumps(self.container)
        if command.startswith("docker pull --platform linux/arm64 "):
            self.image["arch"] = "arm64"
            return 0, ""
        raise AssertionError("Unexpected diagnostic: " + command)

    def test_native_daemon_and_image_and_actual_container(self):
        self.assertEqual(arch.check_docker(self.session)["platform"], "linux/arm64")
        arch.check_image(self.session, IMAGE)
        arch.check_container(self.session, "test-only", require_running=True)
        self.assertTrue(any(IMAGE in cmd for cmd in self.commands))

    def test_amd64_daemon_uses_same_rules_and_rejects_arm_image(self):
        with patch.object(arch.platform, "machine", return_value="x86_64"):
            self.info["arch"] = "amd64"
            self.image["arch"] = "amd64"
            self.assertEqual(arch.check_docker(self.session)["platform"], "linux/amd64")
            arch.check_container(self.session, "test-only", require_running=True)
            self.image["arch"] = "arm64"
            with self.assertRaises(ValueError):
                arch.check_image(self.session, IMAGE)

    def test_remote_rootless_wrong_platform_and_overrides_rejected(self):
        self.endpoint = "tcp://remote:2375"
        with self.assertRaises(ValueError):
            arch.check_docker(self.session)
        self.endpoint = "unix:///var/run/docker.sock"
        for key, value in (("arch", "x86_64"), ("arch", "armv7l"), ("os", "windows"), ("security", ["name=rootless"])):
            saved = self.info.copy()
            self.info[key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                arch.check_docker(self.session)
            self.info = saved
        for key in arch.FORBIDDEN_ENV:
            with self.subTest(key=key), patch.dict(os.environ, {key: "untrusted"}), self.assertRaises(ValueError):
                arch.check_docker(self.session)

    def test_absent_docker_is_not_an_inaccessible_daemon(self):
        self.session.run.side_effect = None
        self.session.run.return_value = (1, "")
        self.assertIsNone(arch.check_docker(self.session, allow_missing=True))
        self.session.run.return_value = (126, "")
        with self.assertRaises(ValueError):
            arch.check_docker(self.session, allow_missing=True)

    def test_standalone_preflight_preserves_go_rejections_and_older_helper_fallback(self):
        info = {"architecture": "arm64", "platform": "linux/arm64", "version": "29.0.0"}
        with patch("tgcloud.cli.host_cli.invoke") as go:
            go.return_value = (0, json.dumps(info))
            self.assertEqual(arch.check_standalone_docker(self.session, allow_missing=True), info)
            go.assert_called_once_with("preflight", "--allow-missing")
            go.return_value = (1, "拒绝远端 daemon")
            with self.assertRaisesRegex(ValueError, "拒绝远端 daemon"):
                arch.check_standalone_docker(self.session, allow_missing=True)
            self.session.run.assert_not_called()
            go.return_value = (2, "旧工具用法")
            self.assertEqual(arch.check_standalone_docker(self.session, allow_missing=True), info)
            self.assertIn(arch.DOCKER_INFO, self.commands)

    def test_wrong_cached_image_is_replaced_only_by_native_child(self):
        self.image["arch"] = "amd64"
        with self.assertRaises(ValueError):
            arch.check_image(self.session, IMAGE)
        arch.ensure_image(self.session, IMAGE)
        self.assertIn("docker pull --platform linux/arm64 " + IMAGE, self.commands)
        self.assertFalse(any("latest" in cmd or "qemu" in cmd for cmd in self.commands))
        self.container["health"] = "unhealthy"
        with self.assertRaises(ValueError):
            arch.check_container(self.session, "test-only", require_running=True)
        self.container["health"] = "healthy"
        self.image["arch"] = "amd64"
        with self.assertRaises(ValueError):
            arch.check_container(self.session, "test-only")

    def test_untrusted_references_never_reach_shell(self):
        for value in ("-bad", "$(id)", "image;id", "https://untrusted/", "bad\nname", ""):
            with self.subTest(value=value), self.assertRaises(ValueError):
                arch.ensure_image(self.session, value)
        self.assertEqual(self.commands, [])

    def test_unparseable_daemon_and_image_fail_closed(self):
        self.session.run.side_effect = lambda *_args, **_kwargs: (0, "not-json PRIVATE")
        for function in (arch.check_docker, lambda s: arch.check_image(s, IMAGE), lambda s: arch.check_container(s, "test")):
            with self.subTest(function=function), self.assertRaises(ValueError) as failure:
                function(self.session)
            self.assertNotIn("PRIVATE", str(failure.exception))


class CandidateTests(unittest.TestCase):
    def test_bad_daemon_stops_before_payload_or_release_record(self):
        from tgcloud.deployment.host import SafeLog
        from tgcloud.deployment.lifecycle import install
        plan = Mock(action="upgrade")
        with patch.object(arch, "check_docker", side_effect=ValueError("wrong native daemon")), patch.object(install.payload, "build_payload") as build, patch.object(install.upgrade, "record_release") as record:
            with self.assertRaises(ValueError):
                install.apply_base(plan, Mock(), ROOT, Mock(), SafeLog())
            build.assert_not_called()
            record.assert_not_called()

    def test_native_ci_and_release_bundles_are_packaged_once_without_emulation(self):
        tests = (ROOT / ".github/workflows/tests.yml").read_text(encoding="utf-8")
        cli = (ROOT / ".github/workflows/vps-installer.yml").read_text(encoding="utf-8")
        release = (ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        for text in (tests, cli, release):
            self.assertIn("ubuntu-24.04-arm", text)
            self.assertIn("ubuntu-24.04", text)
            self.assertNotIn("setup-qemu", text)
        self.assertIn('python: ["3.10", "3.13"]', cli)
        steps = {
            block.splitlines()[0]: block
            for block in release.split("\n      - name: ")[1:]
        }
        package_steps = {
            "Package immutable source once and verify bootstrap pins and SHA256": "source_release.py build",
            "Upload validated source package": "build/source-release/SHA256SUMS.txt",
            "Package and verify the local-source Compose deployment": "compose_release.py bundle",
            "Upload validated Compose package": "build/compose-release/COMPOSE-SHA256SUMS.txt",
        }
        for name, command_or_asset in package_steps.items():
            with self.subTest(step=name):
                self.assertIn("if: matrix.arch == 'amd64'", steps[name])
                self.assertIn(command_or_asset, steps[name])
        self.assertIn("needs.linux-validation.result == 'success'", release)
        self.assertIn("fromJSON(needs.prepare.outputs.native_matrix)", release)
        self.assertIn('TGCLOUD_TEST_NATIVE_DOCKER: "1"', tests)
        self.assertIn("check_native_architecture.py --expected", release)

    def test_arm_requires_stable_release_with_native_hooks_without_main_fallback(self):
        with tempfile.TemporaryDirectory() as private, patch.object(arch.platform, "machine", return_value="arm64"):
            with self.assertRaisesRegex(ValueError, "稳定 Release"):
                arch.require_payload(Path(private))
            arch.require_payload(ROOT)
            with patch.object(arch.platform, "machine", return_value="amd64"):
                arch.require_payload(Path(private))  # Historical AMD64 payloads remain eligible.

    def test_prepare_checks_compose_platform_and_base_before_candidate_build(self):
        with tempfile.TemporaryDirectory() as private, patch.object(arch.platform, "machine", return_value="arm64"), patch.object(arch, "check_docker"), patch.object(arch, "ensure_image") as ensure:
            compose = Path(private) / "docker-compose.yml"
            compose.with_name(".env").write_text("", encoding="utf-8")
            compose.with_name("Dockerfile").write_text("FROM example/base@" + IMAGE + "\n", encoding="utf-8")
            session = Mock()
            services = {"gateway": {"image": "example/gateway@" + IMAGE}}
            session.run.return_value = 0, json.dumps({"services": services})
            arch.prepare_images(session, compose, ["gateway"])
            self.assertEqual(ensure.call_count, 2)
            self.assertFalse(any("stop" in call.args[0] for call in session.run.call_args_list))
            for entries in ({"gateway": {**services["gateway"], "platform": "linux/amd64"}}, {"gateway": None}):
                ensure.reset_mock()
                session.run.return_value = 0, json.dumps({"services": entries})
                with self.assertRaises(ValueError):
                    arch.prepare_images(session, compose, ["gateway"])
                ensure.assert_not_called()
            for key in ("DOCKER_DEFAULT_PLATFORM", "BUILDX_BUILDER", "COMPOSE_ENV_FILES"):
                compose.with_name(".env").write_text(key + "=wrong-platform\n", encoding="utf-8")
                session.run.reset_mock()
                with self.subTest(key=key), self.assertRaisesRegex(ValueError, "覆盖"):
                    arch.prepare_images(session, compose, ["gateway"])
                session.run.assert_not_called()
                ensure.assert_not_called()

    def test_hooks_share_detector_and_check_candidates_before_stopping_old_bot(self):
        for edition in ("clouddrive2",):
            text = (ROOT / f"packaging/hooks/{edition}/remote_install.sh").read_text(encoding="utf-8")
            self.assertIn("-m tgcloud.deployment.architecture", text)
            self.assertNotIn("uname -m", text)
            self.assertLess(text.index("native_check prepare"), text.index('docker stop --time 30 "$BOT_CONTAINER"'))
            self.assertLess(text.index("native_check build-candidate"), text.index('docker stop --time 30 "$BOT_CONTAINER"'))
            self.assertLess(text.index('native_check container "$BOT_CONTAINER"'), text.index("TGCLOUD_RESULT=SUCCESS"))
            self.assertIn('BUILDX_BUILDER=default docker compose build', text)

    def test_standalone_candidate_pull_fallback_is_only_before_go_dispatch(self):
        base, gateway = "example/base@" + IMAGE, "example/gateway@" + IMAGE
        session = Mock()
        session.run.return_value = (0, json.dumps({"services": {"clouddrive2": {"image": gateway}}}))
        with tempfile.TemporaryDirectory() as private, patch.object(arch, "check_docker"), patch(
            "tgcloud.cli.host_cli.invoke"
        ) as invoke, patch.object(arch, "ensure_image") as fallback:
            compose = Path(private) / "docker-compose.yml"
            compose.with_name(".env").touch()
            compose.with_name("Dockerfile").write_text("FROM " + base + "\n", encoding="utf-8")
            invoke.return_value = (2, "旧工具")
            arch.prepare_images(session, compose, ["clouddrive2"], standalone=True)
            self.assertEqual(fallback.call_args_list, [call(session, base), call(session, gateway)])

            invoke.reset_mock()
            fallback.reset_mock()
            invoke.side_effect = [(0, ""), (2, "候选镜像准备异常")]
            with self.assertRaisesRegex(ValueError, "候选镜像准备异常"):
                arch.prepare_images(session, compose, ["clouddrive2"], standalone=True)
            self.assertEqual(invoke.call_args_list, [call("prepare-image", "--supported"), call("prepare-image", base)])
            fallback.assert_not_called()

    def test_candidate_build_fallback_only_before_go_dispatch(self):
        stage = ROOT / "fixture-stage"
        tag = "tgcloud-clouddrive2-candidate:123-456"
        session = Mock()
        host = arch.NativeHost("amd64", "x86_64", "Debian")
        with patch("tgcloud.cli.host_cli.invoke") as invoke, patch.object(
            arch.subprocess, "run", return_value=Mock(returncode=0)
        ) as run, patch.object(arch, "check_image") as check:
            invoke.return_value = (2, "旧工具")
            arch.build_candidate(session, stage, tag, host)
            check.assert_called_once_with(session, tag)
            first, second = [item.args[0] for item in run.call_args_list]
            self.assertEqual(first, ["docker", "build", "--platform", "linux/amd64", "--tag", tag, str(stage)])
            self.assertIn(str(stage / ".env"), second)
            self.assertEqual(second[-3:], ["-m", "app.deployment_check", "--validate-only"])

            run.reset_mock()
            invoke.side_effect = [(0, ""), (0, ""), (0, ""), (1, "")]
            arch.build_candidate(session, stage, tag, host)
            with self.assertRaisesRegex(ValueError, "现有实例保留"):
                arch.build_candidate(session, stage, tag, host)
            run.assert_not_called()

    def test_upgrade_backup_distinguishes_unavailable_helper_from_failure(self):
        with patch("tgcloud.cli.host_cli.invoke") as invoke:
            invoke.return_value = (2, "")
            self.assertEqual(arch.dispatch_standalone_command("backup-config", "source", "target"), 3)
            invoke.assert_called_once_with("backup-config", "--supported")
            invoke.side_effect = [(0, ""), (3, "配置备份失败")]
            with self.assertRaisesRegex(ValueError, "配置备份失败"):
                arch.dispatch_standalone_command("backup-config", "source", "target")

    def test_running_go_bot_deployment_failure_never_selects_fallback(self):
        with patch("tgcloud.cli.host_cli.invoke") as invoke:
            invoke.return_value = (2, "")
            self.assertEqual(arch.dispatch_standalone_command("deploy-bot", "/opt/tgcloud", inherit_output=True), 3)
            invoke.assert_called_once_with("deploy-bot", "--supported")
            invoke.reset_mock()
            invoke.side_effect = [(0, ""), (3, "")]
            self.assertEqual(arch.dispatch_standalone_command("deploy-bot", "/opt/tgcloud", inherit_output=True), 1)
            self.assertEqual(invoke.call_args_list, [
                call("deploy-bot", "--supported"),
                call("deploy-bot", "/opt/tgcloud", inherit_output=True),
            ])

    def test_host_cli_invoke_keeps_stderr_out_of_json_stdout(self):
        from tgcloud.cli import host_cli

        fake_proc = Mock()
        fake_proc.__enter__ = Mock(return_value=fake_proc)
        fake_proc.__exit__ = Mock(return_value=False)
        fake_proc.returncode = 0
        fake_proc.communicate.return_value = ('{"ok": true}\n', "warning: noisy stderr\n")
        with patch("tgcloud.cli.host_cli.subprocess.Popen", return_value=fake_proc) as popen:
            result = host_cli.invoke("status", "--json", executable=Path("/opt/tgcloud-cli/tgcloud-host"))
        self.assertEqual(result, (0, '{"ok": true}\n'))
        self.assertEqual(popen.call_args.kwargs["stderr"], host_cli.subprocess.PIPE)

        fake_proc.returncode = 1
        fake_proc.communicate.return_value = ("", "fatal error\n")
        with patch("tgcloud.cli.host_cli.subprocess.Popen", return_value=fake_proc):
            failed = host_cli.invoke("status", "--json", executable=Path("/opt/tgcloud-cli/tgcloud-host"))
        self.assertEqual(failed, (1, "fatal error\n"))
