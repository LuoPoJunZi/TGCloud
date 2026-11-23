"""Synthetic no-progress transfers; no Telegram, WebDAV or real credentials."""
from __future__ import annotations

import asyncio
import json
import tempfile
import time
import unittest
from dataclasses import replace
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from tests.fixtures.readiness import ReadinessFixture
from tests.fixtures.transfer import FakeRclone, make_service, make_settings
from tgcloud.domain.errors import DownloadStalledError, safe_failure
from tgcloud.monitoring import readiness
from tgcloud.monitoring.diagnostics import collect
from tgcloud.persistence.sqlite import TaskDB
from tgcloud.transfer import watchdog


class GuardTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.clock = 0.0
        self.owner = SimpleNamespace(log=Mock())
        self.addCleanup(patch.stopall)
        patch.object(watchdog, "monotonic", side_effect=lambda: self.clock).start()
        patch.object(watchdog, "CHECK_SECONDS", 0.002).start()
        patch.object(watchdog, "CANCEL_SECONDS", 0.005).start()

    async def test_no_progress_cancels_and_joins_before_timeout_is_raised(self):
        stopped = asyncio.Event()
        guard = watchdog.DownloadGuard(self.owner, 1)
        async def hang():
            self.clock = 301
            try:
                await asyncio.Event().wait()
            finally:
                stopped.set()
        with self.assertRaises(DownloadStalledError):
            await asyncio.wait_for(guard.run(hang()), 1)
        self.assertTrue(stopped.is_set())
        self.assertEqual(self.owner._download_guards, {})
        self.assertIn("安全回收", safe_failure(DownloadStalledError()))

    async def test_long_total_duration_with_real_progress_is_not_timed_out(self):
        guard = watchdog.DownloadGuard(self.owner, 1)
        async def slow():
            for number in range(1, 6):
                self.clock += 299
                guard.advance(number)
                await asyncio.sleep(0.004)
            return "finished"
        self.assertEqual(await guard.run(slow()), "finished")
        self.assertGreater(self.clock, watchdog.IDLE_SECONDS)
        self.assertFalse(guard.stalled)

    async def test_duplicate_or_regressing_counters_do_not_hide_a_stall(self):
        guard = watchdog.DownloadGuard(self.owner, 1)
        async def hang():
            guard.advance(2)
            self.clock = 301
            for value in (2, 1, 0, True):
                guard.advance(value)
            await asyncio.Event().wait()
        with self.assertRaises(DownloadStalledError):
            await guard.run(hang())

    async def test_explicit_scoped_cooldown_is_honoured_but_not_unbounded(self):
        guard = watchdog.DownloadGuard(self.owner, 1)
        ready = asyncio.Event()
        finish = asyncio.Event()
        async def waiting():
            watchdog.note_wait(self.owner, 1, 60)
            self.clock = 350  # Only 290s idle after the explicitly reported wait.
            ready.set()
            await finish.wait()
            return "done"
        work = asyncio.create_task(guard.run(waiting()))
        await ready.wait()
        await asyncio.sleep(0.01)
        self.assertFalse(work.done())
        self.assertEqual(watchdog.observation(self.owner)["stalled"], 0)
        finish.set()
        self.assertEqual(await work, "done")
        guard = watchdog.DownloadGuard(self.owner, 2)
        async def never():
            for value in (True, float("inf"), -1, 61):
                guard.waiting(value)
            self.clock += 301
            await asyncio.Event().wait()
        with self.assertRaises(DownloadStalledError):
            await guard.run(never())

    async def test_downstream_backpressure_is_not_misclassified_as_source_idle(self):
        guard = watchdog.DownloadGuard(self.owner, 1)
        entered, release = asyncio.Event(), asyncio.Event()
        async def write(data):
            self.clock = 10000
            entered.set()
            await release.wait()
            return len(data)
        sink = SimpleNamespace(write=write, tell=lambda: 4, flush=Mock())
        wrapped = guard.stream(sink)
        async def transfer():
            return await wrapped.write(b"test")
        work = asyncio.create_task(guard.run(transfer()))
        await entered.wait()
        await asyncio.sleep(0.01)
        self.assertFalse(work.done())
        self.assertEqual(watchdog.observation(self.owner)["stalled"], 0)
        self.assertEqual(wrapped.tell(), 4)
        wrapped.flush()
        sink.flush.assert_called_once()
        release.set()
        self.assertEqual(await work, 4)

    async def test_timeout_and_repeated_cancellation_do_not_detach_old_writer(self):
        guard = watchdog.DownloadGuard(self.owner, 1)
        cancelling, release = asyncio.Event(), asyncio.Event()
        async def writer():
            self.clock = 301
            try:
                await asyncio.Event().wait()
            except asyncio.CancelledError:
                cancelling.set()
                await release.wait()  # Simulate an awaited SDK/file cleanup.
                return "cleanup done"
        work = asyncio.create_task(guard.run(writer()))
        await cancelling.wait()
        self.assertEqual(watchdog.observation(self.owner)["stalled"], 1)
        self.assertEqual(watchdog.observation(self.owner)["cancelling"], 1)
        work.cancel()
        await asyncio.sleep(0.01)
        work.cancel()
        await asyncio.sleep(0.01)
        self.assertFalse(work.done())
        self.assertIs(self.owner._download_guards[1], guard)
        release.set()
        with self.assertRaises(asyncio.CancelledError):
            await work
        self.assertEqual(self.owner._download_guards, {})

    async def test_normal_user_cancel_joins_cleanup_and_propagates(self):
        guard = watchdog.DownloadGuard(self.owner, 1)
        entered, stopped = asyncio.Event(), asyncio.Event()
        async def writer():
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                stopped.set()
        work = asyncio.create_task(guard.run(writer()))
        await entered.wait()
        work.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await work
        self.assertTrue(stopped.is_set())
        self.assertFalse(self.owner._download_guards)


class TransferGuardTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.settings = make_settings(self.root)
        self.db = TaskDB(self.settings.data_dir / "synthetic.db")
        self.addCleanup(self.db.close)
        self.service = make_service(self.settings, self.db)
        self.service.rclone = FakeRclone()
        self.service._notify = AsyncMock()
        self.clock = 0.0
        self.addCleanup(patch.stopall)
        patch.object(watchdog, "monotonic", side_effect=lambda: self.clock).start()
        patch.object(watchdog, "CHECK_SECONDS", 0.002).start()

    def task(self, message, **fields):
        task, _ = self.db.create_task(chat_id=1, message_id=message,
            sender_id=self.settings.allowed_user_id, file_name=f"synthetic-{message}.bin", file_size=4)
        self.db.update(task["id"], **fields)
        return self.db.get(task["id"])

    async def test_stuck_download_exhausts_bounded_attempts_then_next_task_can_finish(self):
        for number, edition in enumerate(("clouddrive2",)):
            with self.subTest(edition=edition):
                self.service.settings = replace(self.settings, storage_backend=edition, max_retries=1)
                stalled = self.task(10 + number * 2, state="reserved")
                following = self.task(11 + number * 2, state="reserved")
                cleaned = asyncio.Event()
                async def download(message, *, file, progress_callback, stalled=stalled, cleaned=cleaned):
                    if message.id == stalled["message_id"]:
                        Path(file).write_bytes(b"ab")
                        progress_callback(2, 4)
                        self.clock += 301
                        try:
                            await asyncio.Event().wait()
                        finally:
                            cleaned.set()
                    self.assertTrue(cleaned.is_set())
                    Path(file).write_bytes(b"abcd")
                    progress_callback(4, 4)
                    return file
                self.service._source = SimpleNamespace(
                    get_messages=AsyncMock(side_effect=lambda entity, *, ids: SimpleNamespace(id=ids, media=True)),
                    download_media=download)
                await asyncio.wait_for(self.service._download_one(stalled["id"]), 1)
                failed = self.db.get(stalled["id"])
                self.assertEqual((failed["state"], failed["download_retries"]), ("download_failed", 1))
                self.assertIn("无数据进度", failed["error"])
                self.assertFalse((self.settings.download_dir / f"{stalled['id']}.part").exists())
                await self.service._download_one(following["id"])
                completed = self.db.get(following["id"])
                self.assertEqual(completed["state"], "waiting_upload")
                self.assertEqual(Path(completed["local_path"]).read_bytes(), b"abcd")
                self.assertFalse(self.service._download_guards)
                self.assertEqual(self.db.count_states(("waiting_upload",)), number + 1)

    async def test_metadata_hang_is_bounded_and_preserves_task_for_manual_recovery(self):
        task = self.task(1, state="reserved")
        self.service.settings = replace(self.settings, max_retries=2)
        calls = 0
        async def get(*args, **kwargs):
            nonlocal calls
            calls += 1
            self.clock += 301
            await asyncio.Event().wait()
        self.service._source = SimpleNamespace(get_messages=get, download_media=AsyncMock())
        with patch("tgcloud.transfer.download.asyncio.sleep", new=AsyncMock()):
            await asyncio.wait_for(self.service._download_one(task["id"]), 1)
        saved = self.db.get(task["id"])
        self.assertEqual((calls, saved["state"], saved["failure_kind"]), (2, "download_failed", "network"))
        self.service._source.download_media.assert_not_awaited()
        self.assertEqual(self.db.count_states(("download_failed",)), 1)

    async def test_scheduler_releases_finished_guard_slot_and_starts_the_next_task(self):
        self.service.settings = replace(self.settings, max_retries=1)
        failed, following = self.task(1), self.task(2)
        self.service.destination_healthy = True
        self.service.snapshot = SimpleNamespace(sampled_at=time.time(), disk_free=10**12,
                                                filesystems=(), memory_available=10**10)
        self.service.download_window = SimpleNamespace(value=1)
        self.service.upload_window = SimpleNamespace(value=1)
        cleaned = asyncio.Event()
        async def download(message, *, file, progress_callback):
            if message.id == 1:
                Path(file).write_bytes(b"ab")
                progress_callback(2, 4)
                self.clock += 301
                try:
                    await asyncio.Event().wait()
                finally:
                    cleaned.set()
            self.assertTrue(cleaned.is_set())
            Path(file).write_bytes(b"abcd")
            progress_callback(4, 4)
            return file
        self.service._source = SimpleNamespace(
            get_messages=AsyncMock(side_effect=lambda entity, *, ids: SimpleNamespace(id=ids, media=True)),
            download_media=download)
        with patch("tgcloud.transfer.scheduler.shutil.disk_usage", return_value=SimpleNamespace(free=10**12)):
            await self.service._start_downloads()
            self.assertEqual(set(self.service.download_tasks), {failed["id"]})
            await asyncio.gather(*self.service.download_tasks.values())
            self.assertEqual(self.db.get(failed["id"])["state"], "download_failed")
            self.assertFalse(self.service.download_tasks)
            self.assertFalse(self.service._download_guards)
            await self.service._start_downloads()
            self.assertEqual(set(self.service.download_tasks), {following["id"]})
            await asyncio.gather(*self.service.download_tasks.values())
        self.assertEqual(self.db.get(following["id"])["state"], "waiting_upload")
        self.assertFalse(self.service.download_tasks)

    async def test_stream_stall_aborts_writer_without_deleting_remote_partial(self):
        for number, edition in enumerate(("clouddrive2",)):
            with self.subTest(edition=edition):
                self.service.settings = replace(self.settings, storage_backend=edition, max_retries=1)
                task = self.task(1 + number, state="reserved", transfer_mode="stream")
                storage = FakeRclone()
                aborted = AsyncMock()
                async def open_stream(path, size, storage=storage, aborted=aborted):
                    async def write(data):
                        storage.files[path] = data
                        return len(data)
                    return SimpleNamespace(write=write, abort=aborted)
                async def hang(message, *, file, progress_callback):
                    await file.write(b"ab")
                    progress_callback(2, 4)
                    self.clock += 301
                    await asyncio.Event().wait()
                storage.open_upload_stream = open_stream
                self.service.rclone = storage
                self.service._source = SimpleNamespace(get_messages=AsyncMock(return_value=SimpleNamespace(media=True)),
                                                       download_media=hang)
                await asyncio.wait_for(self.service._stream_one(task["id"]), 1)
                saved = self.db.get(task["id"])
                self.assertEqual((saved["state"], saved["failure_kind"]), ("download_failed", "network"))
                self.assertIn("无数据进度", saved["error"])
                self.assertTrue(saved["remote_path"])
                self.assertEqual(list(storage.files.values()), [b"ab"])
                self.assertFalse(storage.removed)
                aborted.assert_awaited_once()


class ProgressReadinessTests(unittest.TestCase):
    def setUp(self):
        self.fixture = ReadinessFixture()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.service = self.fixture.service
        self.service.download_window = SimpleNamespace(value=1)
        self.service.upload_window = SimpleNamespace(value=1)
        self.service.download_tasks = {1: object()}

    def test_stalled_transfer_fails_business_readiness_without_failing_process_heartbeat(self):
        guard = watchdog.DownloadGuard(self.service, 1)
        guard.at -= 301
        guard.until = guard.at
        self.service._download_guards = {1: guard}
        report = self.fixture.report()
        self.assertFalse(report["ready"])
        self.assertEqual(report["checks"]["heartbeat"], "ok")
        self.assertEqual(report["checks"]["queue"], "failed")
        self.assertEqual(report["transfers"]["stalled"], 1)
        self.assertEqual(report["queue_wait"], "download_stall")
        self.assertNotIn("task_id", str(report["transfers"]))

    def test_normal_busy_slot_is_ready_and_has_explicit_wait_reason(self):
        report = self.fixture.report()
        self.assertTrue(report["ready"])
        self.assertEqual(report["queue_wait"], "download_slots")
        self.assertIn("download_busy", report["admission"]["blocked_by"])

    def test_old_snapshot_remains_readable_and_malformed_new_observations_do_not_pass(self):
        self.fixture.report()
        path = self.fixture.root / "readiness.json"
        original = path.read_text()
        old = json.loads(original)
        old.pop("transfers")
        old.pop("queue_wait")
        path.write_text(json.dumps(old))
        self.assertTrue(readiness.read(self.fixture.root, now=time.time())["ready"])
        for bad in ({}, {"active": "PRIVATE_CONTENT"}):
            value = json.loads(original)
            value["transfers"] = bad
            path.write_text(json.dumps(value))
            report = readiness.read(self.fixture.root, now=time.time())
            self.assertFalse(report["ready"])
            self.assertNotIn("PRIVATE_CONTENT", str(report))

    def test_diagnostic_waiting_summary_reuses_observed_slot_and_preserves_other_causes(self):
        from tgcloud.deployment.ops import readiness as host
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            db = TaskDB(root / "tgcloud.db")
            try:
                for number in (1, 2):
                    db.create_task(chat_id=1, message_id=number, sender_id=1, file_name="private.bin", file_size=4)
                db.update(2, next_retry_at=time.time() + 500)
                for reason in ("download_slots", "download_stall", None):
                    snapshot = self.fixture.report() | {"queue_wait": reason}
                    with patch("tgcloud.monitoring.diagnostics.readiness.read", return_value=snapshot):
                        report = collect(root, environment={})
                    self.assertEqual(report["waiting"], {reason or "other": 1, "retry": 1})
                    self.assertNotIn("private.bin", str(report))
                self.assertIn("等待下载名额", "".join(host.waiting_lines({"download_slots": 2})))
                self.assertIn("停滞", "".join(host.transfer_lines({"active": 1, "stalled": 1, "cancelling": 1,
                    "oldest_idle_seconds": 301, "timeout_seconds": 300})))
            finally:
                db.close()
