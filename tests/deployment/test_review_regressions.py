"""Regression coverage for the October 2026 source review (no production access)."""

from __future__ import annotations

import asyncio
import json
import os
import shlex
import shutil
import subprocess
import tempfile
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from tests.fixtures import ROOT
from tests.fixtures.proxy import TransactionManager, external_state
from tgcloud.app import TransferService
from tgcloud.deployment.profiles import CLOUDDRIVE2_PRODUCT
from tgcloud.deployment.proxy.manager import (
    DomainProxyManager,
    ProxyReport,
    empty_state,
    proxy_lock_command,
    state_with_route,
)
from tgcloud.storage.rclone import RcloneClient
from tgcloud.transfer.ports import DestinationProbe


class DestinationReviewTests(unittest.IsolatedAsyncioTestCase):
    @staticmethod
    def client():
        client = RcloneClient(SimpleNamespace(
            storage_backend="clouddrive2", cd2_target="", rclone_remote_name="cd2",
        ))
        client.ensure_config = AsyncMock()
        return client

    async def test_cloud_drive_stat_does_not_gain_parent_listing(self):
        client = self.client()
        client._run = AsyncMock(return_value=(4, "", "not found"))
        self.assertFalse(await client.exists("missing.bin"))
        self.assertEqual(client._run.await_count, 1)

    async def test_probe_classifies_auth_permission_and_rate_limit_without_echoing_output(self):
        for output, expected in (("401 Unauthorized", "unauthorized"), ("403 Forbidden", "permission"),
                                 ("429 Too Many Requests", "rate_limited")):
            with self.subTest(output=output):
                client = self.client()
                client._run = AsyncMock(return_value=(1, "", output + " secret-placeholder"))
                probe = await client.probe()
                self.assertFalse(probe.accessible)
                self.assertEqual(probe.failure_kind, expected)
                self.assertNotIn("secret-placeholder", probe.detail)
                client._run.assert_awaited_once_with(
                    "lsd", "cd2:", "--max-depth", "1", timeout=20, check=False,
                )

    async def test_root_fallback_preserves_rate_limit_reason(self):
        client = self.client()
        client.settings.cd2_target = "target"
        client._run = AsyncMock(side_effect=[(4, "", "not found"), (1, "", "429 Too Many Requests")])
        probe = await client.probe()
        self.assertEqual((probe.scope, probe.failure_kind), ("root_fallback", "rate_limited"))

    async def test_cloud_drive_probe_command_is_unchanged(self):
        client = self.client()
        client._run = AsyncMock(return_value=(0, "", ""))
        await client.probe()
        client._run.assert_awaited_once_with(
            "lsd", "cd2:", "--max-depth", "1", timeout=20, check=False,
        )

    async def test_cloud_drive_refresh_behavior_is_unchanged(self):
        service = TransferService.__new__(TransferService)
        service.settings = SimpleNamespace(storage_backend="clouddrive2", remote_health_interval=30)
        service.rclone = SimpleNamespace(probe=AsyncMock(return_value=DestinationProbe(True, "root", "ok")))
        service.log = Mock()
        await asyncio.gather(*(service._probe_destination_once() for _ in range(3)))
        self.assertEqual(service.rclone.probe.await_count, 3)


class RemoteLockTests(unittest.TestCase):


    @unittest.skipUnless(os.name != "nt" and shutil.which("flock"), "live flock test requires Linux")
    def test_real_flock_is_exclusive_and_released_on_ssh_eof(self):
        with tempfile.TemporaryDirectory() as temp:
            with patch("tgcloud.deployment.proxy.manager.PROXY_ROOT", temp):
                command = shlex.split(proxy_lock_command())[2]
            holder = subprocess.Popen(
                ["bash", "-c", command], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            try:
                import select

                self.assertTrue(select.select([holder.stdout], [], [], 5)[0])
                self.assertEqual(holder.stdout.readline(), b"TGCLOUD_REMOTE_LOCK=ACQUIRED\n")
                holder.stdin.write(b"unused-sudo-line\n")
                holder.stdin.flush()
                blocked = subprocess.run(["bash", "-c", command], input=b"", capture_output=True, timeout=5, check=False)
                self.assertEqual(blocked.returncode, 75)
                holder.stdin.close()
                self.assertEqual(holder.wait(timeout=5), 0)
                freed = subprocess.run(["bash", "-c", command], input=b"TGCLOUD_REMOTE_LOCK_RELEASE\n", capture_output=True, timeout=5, check=False)
                self.assertEqual(freed.returncode, 0, freed.stderr)
            finally:
                if holder.poll() is None:
                    holder.kill()
                    holder.wait(timeout=5)
                holder.stdout.close()
                holder.stderr.close()
                if not holder.stdin.closed:
                    holder.stdin.close()


class ProxyReviewTests(unittest.TestCase):
    def test_concurrent_editions_preserve_both_routes(self):
        shared = {"state": external_state("files.example.com")}
        lock = threading.Lock()
        start = threading.Barrier(2)

        class Manager(TransactionManager):
            def __init__(self, product):
                super().__init__(product)

                @contextmanager
                def held(*args, **kwargs):
                    with lock:
                        yield

                self.session = SimpleNamespace(hold_lock=held)

            def read_state(self):
                state = json.loads(json.dumps(shared["state"]))
                time.sleep(0.01)  # Widen the lost-update window if locking regresses.
                return state

            def _commit_state(self, state):
                shared["state"] = json.loads(json.dumps(state))

        def configure(product, domain):
            start.wait(timeout=5)
            return Manager(product).configure(domain, "")

        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(configure, p, d) for p, d in (
                (CLOUDDRIVE2_PRODUCT, "cd.example.com"), (CLOUDDRIVE2_PRODUCT, "cd.example.com"),
            )]
            self.assertTrue(all(f.result(timeout=10).state == "healthy" for f in futures))
        self.assertEqual(set(shared["state"]["domains"]), {"clouddrive2"})
        self.assertEqual(shared["state"]["external_webdav"]["clouddrive2"]["domain"], "files.example.com")

    @staticmethod
    def both_routes():
        state = external_state("ol.example.com")
        return state_with_route(state, CLOUDDRIVE2_PRODUCT, "cd.example.com")

    def test_remove_syncs_certbot_and_checks_retained_routes_before_commit(self):
        manager = TransactionManager()
        manager.current = self.both_routes()
        manager._verify_remaining_routes = Mock()
        manager.remove()
        self.assertIn("up -d certbot", manager.compose_calls)
        manager._verify_remaining_routes.assert_called_once()
        self.assertEqual(manager.commits[-1]["domains"], {})
        self.assertEqual(set(manager.commits[-1]["external_webdav"]), {"clouddrive2"})

    def test_remove_retained_check_failure_restores_both_routes(self):
        manager = TransactionManager()
        manager.current = self.both_routes()
        manager._verify_remaining_routes = Mock(side_effect=RuntimeError("retained route failed"))
        with self.assertRaisesRegex(RuntimeError, "retained route failed"):
            manager.remove()
        self.assertFalse(manager.commits)
        self.assertEqual(manager.activations[-1][0], self.both_routes())
        self.assertIn("up -d certbot", manager.compose_calls)

    def test_remove_certbot_update_failure_rolls_back_without_commit(self):
        manager = TransactionManager()
        manager.current = self.both_routes()
        manager._compose = Mock(side_effect=[(1, "failed"), (0, "")])
        manager._verify_remaining_routes = Mock()
        with self.assertRaisesRegex(RuntimeError, "续期容器更新失败"):
            manager.remove()
        self.assertFalse(manager.commits)
        manager._verify_remaining_routes.assert_not_called()
        self.assertEqual(manager.activations[-1][0], self.both_routes())
        self.assertEqual(manager._compose.call_count, 2)

    def test_busy_lock_prevents_state_reads_and_configuration_changes(self):
        manager = TransactionManager()
        manager.session.hold_lock = Mock(side_effect=RuntimeError("lock busy"))
        manager.read_state = Mock()
        for action in (lambda: manager.configure("ol.example.com", ""), manager.remove):
            with self.assertRaisesRegex(RuntimeError, "lock busy"):
                action()
        manager.read_state.assert_not_called()
        self.assertFalse(manager.activations)
        self.assertFalse(manager.commits)

    def test_remaining_route_check_uses_its_own_product(self):
        manager = DomainProxyManager(Mock(), CLOUDDRIVE2_PRODUCT, {}, Mock())
        state = state_with_route(empty_state(), CLOUDDRIVE2_PRODUCT, "cd.example.com")
        checked = []

        def status(owner, target):
            checked.append(owner.product.key)
            return ProxyReport("cd.example.com", "healthy", "ok", ())

        with patch.object(DomainProxyManager, "status", status):
            manager._verify_remaining_routes(state)
        self.assertEqual(checked, ["clouddrive2"])

    def test_state_commit_uses_same_directory_atomic_rename(self):
        session = Mock()
        session.run.return_value = (0, "")
        manager = DomainProxyManager(session, CLOUDDRIVE2_PRODUCT, {}, Mock())
        manager._upload_files = Mock(return_value="/tmp/tgcloud-proxy-" + "a" * 32)
        manager._commit_state(empty_state())
        command = session.run.call_args_list[0].args[0]
        self.assertIn("/state/.domains-", command)
        self.assertIn("mv -f --", command)
        self.assertNotIn("install -m 600 " + manager._upload_files.return_value + "/domains.json /opt/tgcloud-proxy/state/domains.json", command)

    def test_committed_state_survives_cleanup_errors(self):
        for failure in (OSError("cleanup failed"), (1, "cleanup failed")):
            with self.subTest(failure=type(failure).__name__):
                session = Mock()
                session.run.side_effect = [(0, ""), failure, (0, "")]
                log = Mock()
                manager = DomainProxyManager(session, CLOUDDRIVE2_PRODUCT, {}, log)
                manager._upload_files = Mock(return_value="/tmp/tgcloud-proxy-" + "a" * 32)
                manager._commit_state(empty_state())
                self.assertEqual(session.run.call_count, 3)
                log.assert_called_once()
                self.assertIn("状态提交结果保持不变", log.call_args.args[0])

    def test_release_waits_for_same_tag_linux_validation_without_second_exe_build(self):
        source = ROOT
        workflow = (source / ".github/workflows/release.yml").read_text(encoding="utf-8")
        linux = workflow.split("  linux-validation:", 1)[1].split("  release:", 1)[0]
        release = workflow.split("  release:", 1)[1]
        self.assertIn("ref: ${{ env.RELEASE_TAG }}", linux)
        self.assertIn(".github/scripts/linux_validation.py test", linux)
        self.assertIn("shellcheck rclone", linux)
        self.assertIn("docker info", linux)
        self.assertIn("- linux-validation", release)
        self.assertIn("needs.linux-validation.result == 'success'", release)
        self.assertNotIn("./build.ps1", release)
