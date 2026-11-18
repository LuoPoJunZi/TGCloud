from __future__ import annotations

# The tests insert the CloudDrive2 payload path before importing the shared app.
import asyncio
import sys
import tempfile
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from tests.fixtures.payload import payload
from tests.fixtures.transfer import FakeRclone, make_service, make_settings

PAYLOAD = payload("clouddrive2")
sys.path.insert(0, str(PAYLOAD))

from tgcloud.app import TransferService
from tgcloud.monitoring.resources import ResourceSnapshot
from tgcloud.persistence.sqlite import TaskDB


class EndToEndSimulationTests(unittest.TestCase):
    def test_one_file_happy_path_from_telegram_to_clouddrive(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            payload = b"video-payload" * 100
            task, _ = db.create_task(
                chat_id=1,
                message_id=10,
                sender_id=settings.allowed_user_id,
                file_name="示例视频.mp4",
                file_size=len(payload),
            )
            db.update(task["id"], state="reserved")
            service = make_service(settings, db)
            notifications: list[str] = []

            class FakeTelegram:
                async def get_messages(self, *_: object, **__: object):
                    return SimpleNamespace(media=True)

                async def download_media(
                    self,
                    _: object,
                    *,
                    file: str,
                    progress_callback,
                ) -> str:
                    Path(file).write_bytes(payload)
                    progress_callback(len(payload), len(payload))
                    return file

            async def notify(message: str) -> None:
                notifications.append(message)

            service.client = FakeTelegram()  # type: ignore[assignment]
            service.rclone = FakeRclone()  # type: ignore[assignment]
            service._notify = notify  # type: ignore[method-assign]

            asyncio.run(service._download_one(task["id"]))
            downloaded = db.get(task["id"])
            self.assertEqual(downloaded["state"], "waiting_upload")
            self.assertTrue(Path(downloaded["local_path"]).is_file())

            asyncio.run(service._upload_one(task["id"]))
            completed = db.get(task["id"])
            self.assertEqual(completed["state"], "completed")
            self.assertEqual(completed["remote_path"], "示例视频.mp4")
            self.assertFalse(Path(downloaded["local_path"]).exists())
            self.assertEqual(
                service.rclone.files,  # type: ignore[attr-defined]
                {"示例视频.mp4": payload},
            )
            self.assertTrue(any("下载完成" in text for text in notifications))
            completion = next(
                text for text in notifications if "CloudDrive2 已接收" in text
            )
            self.assertIn("Bot 传输已完成", completion)
            self.assertIn("WebDAV 远端大小已经复验", completion)
            self.assertNotIn("/confirm", completion)
            self.assertNotIn("正在后台上传到 115", completion)
            db.close()

    def test_large_file_streams_without_local_complete_file(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            payload = b"streamed-video-payload"
            task, _ = db.create_task(
                chat_id=1,
                message_id=20,
                sender_id=settings.allowed_user_id,
                file_name="大文件.mp4",
                file_size=len(payload),
            )
            db.update(
                task["id"],
                state="reserved",
                transfer_mode="stream",
            )
            service = make_service(settings, db)

            class FakeTelegram:
                async def get_messages(self, *_: object, **__: object):
                    return SimpleNamespace(media=True)

                async def download_media(
                    self,
                    _: object,
                    *,
                    file,
                    progress_callback,
                ) -> None:
                    for chunk in (payload[:7], payload[7:]):
                        await file.write(chunk)
                        progress_callback(file.tell(), len(payload))

            service.client = FakeTelegram()  # type: ignore[assignment]
            service.rclone = FakeRclone()  # type: ignore[assignment]
            service._notify = AsyncMock()  # type: ignore[method-assign]

            asyncio.run(service._download_one(task["id"]))

            completed = db.get(task["id"])
            self.assertEqual(completed["state"], "completed")
            self.assertEqual(completed["transfer_mode"], "stream")
            self.assertIsNone(completed["local_path"])
            self.assertEqual(
                service.rclone.files,  # type: ignore[attr-defined]
                {"大文件.mp4": payload},
            )
            self.assertEqual(db.used_local_bytes(), 0)
            self.assertEqual(list(settings.download_dir.iterdir()), [])
            db.close()

    def test_cancel_after_remote_move_removes_final_remote_before_local(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            payload = b"data"
            task, _ = db.create_task(
                chat_id=1,
                message_id=21,
                sender_id=settings.allowed_user_id,
                file_name="video.mp4",
                file_size=len(payload),
            )
            local = settings.download_dir / "1-video.mp4"
            local.write_bytes(payload)
            db.update(
                task["id"],
                state="waiting_upload",
                local_path=str(local),
            )
            service = make_service(settings, db)
            final_wait = asyncio.Event()

            class PausingRclone(FakeRclone):
                async def remote_size(self, remote_path: str) -> int:
                    if remote_path == "video.mp4":
                        final_wait.set()
                        await asyncio.Event().wait()
                    return await super().remote_size(remote_path)

            async def scenario() -> None:
                service.rclone = PausingRclone()  # type: ignore[assignment]
                service._notify = AsyncMock()  # type: ignore[method-assign]
                running = asyncio.create_task(service._upload_one(task["id"]))
                service.upload_tasks[task["id"]] = running
                await asyncio.wait_for(final_wait.wait(), timeout=2)
                event = SimpleNamespace(reply=AsyncMock())
                await service._command_cancel(
                    event, ["/cancel", str(task["id"])]
                )

            asyncio.run(scenario())

            self.assertEqual(db.get(task["id"])["state"], "cancelled")
            self.assertFalse(local.exists())
            self.assertEqual(
                service.rclone.files,  # type: ignore[attr-defined]
                {},
            )
            db.close()

    def test_stream_restart_with_verified_remote_skips_redownload(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            payload = b"already-remote"
            task, _ = db.create_task(
                chat_id=1,
                message_id=22,
                sender_id=settings.allowed_user_id,
                file_name="large.mp4",
                file_size=len(payload),
            )
            db.update(
                task["id"],
                state="queued",
                transfer_mode="stream",
                remote_path="large.mp4",
            )
            reserved, _ = db.reserve(
                task["id"], budget_bytes=1024, current_free_bytes=2048,
                minimum_free_bytes=512,
            )
            self.assertTrue(reserved)
            service = make_service(settings, db)
            remote = FakeRclone()
            remote.files["large.mp4"] = payload
            service.rclone = remote  # type: ignore[assignment]
            service.client = Mock()
            service._notify = AsyncMock()  # type: ignore[method-assign]

            asyncio.run(service._download_one(task["id"]))

            self.assertEqual(db.get(task["id"])["state"], "completed")
            service.client.get_messages.assert_not_called()
            db.close()

    def test_cancel_fails_closed_when_remote_file_cannot_be_removed(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            payload = b"data"
            task, _ = db.create_task(
                chat_id=1,
                message_id=23,
                sender_id=settings.allowed_user_id,
                file_name="video.mp4",
                file_size=len(payload),
            )
            local = settings.download_dir / "1-video.mp4"
            local.write_bytes(payload)
            db.update(
                task["id"],
                state="waiting_upload",
                local_path=str(local),
                remote_path="video.mp4",
            )
            service = make_service(settings, db)

            class StubbornRemote:
                async def exists(self, _: str) -> bool:
                    return True

                async def remove(self, _: str) -> None:
                    return None

            service.rclone = StubbornRemote()  # type: ignore[assignment]
            event = SimpleNamespace(reply=AsyncMock())

            asyncio.run(
                service._command_cancel(
                    event, ["/cancel", str(task["id"])]
                )
            )

            self.assertEqual(db.get(task["id"])["state"], "waiting_upload")
            self.assertTrue(local.exists())
            self.assertIn("取消中止", event.reply.await_args.args[0])
            db.close()

    def test_transient_telegram_failure_retries_without_manual_resend(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            payload = b"retry-success"
            task, _ = db.create_task(
                chat_id=1,
                message_id=11,
                sender_id=settings.allowed_user_id,
                file_name="retry.bin",
                file_size=len(payload),
            )
            db.update(task["id"], state="reserved")
            service = make_service(settings, db)

            class FlakyTelegram:
                def __init__(self) -> None:
                    self.calls = 0

                async def get_messages(self, *_: object, **__: object):
                    return SimpleNamespace(media=True)

                async def download_media(
                    self,
                    _: object,
                    *,
                    file: str,
                    progress_callback,
                ) -> str:
                    self.calls += 1
                    if self.calls == 1:
                        raise ConnectionError("temporary Telegram outage")
                    Path(file).write_bytes(payload)
                    progress_callback(len(payload), len(payload))
                    return file

            client = FlakyTelegram()
            service.client = client  # type: ignore[assignment]
            service._notify = AsyncMock()  # type: ignore[method-assign]

            async def no_wait(_: float) -> None:
                return None

            with patch("tgcloud.app.asyncio.sleep", new=no_wait):
                asyncio.run(service._download_one(task["id"]))
            recovered = db.get(task["id"])
            self.assertEqual(client.calls, 2)
            self.assertEqual(recovered["state"], "waiting_upload")
            self.assertEqual(recovered["download_retries"], 1)
            db.close()

    def test_exhausted_download_keeps_safe_cause_and_never_logs_raw_exception(self) -> None:
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        settings = make_settings(Path(temp.name))
        db = TaskDB(settings.data_dir / "tasks.db")
        self.addCleanup(db.close)
        task, _ = db.create_task(chat_id=1, message_id=12,
            sender_id=settings.allowed_user_id, file_name="synthetic.mp4", file_size=100)
        db.update(task["id"], state="reserved")
        service = make_service(settings, db)
        service.client = SimpleNamespace(get_messages=AsyncMock(return_value=SimpleNamespace(media=True)),
            download_media=AsyncMock(side_effect=TimeoutError("PRIVATE_TOKEN https://private.invalid/file.mp4")))
        service._notify = AsyncMock()
        with patch("tgcloud.transfer.download.asyncio.sleep", new=AsyncMock()):
            asyncio.run(service._download_one(task["id"]))
        saved = db.get(task["id"])
        self.assertEqual(saved["state"], "download_failed")
        self.assertEqual(saved["failure_kind"], "network")
        self.assertIn("下载重试次数已经用完；", saved["error"])
        self.assertIn("传输连接", saved["error"])
        self.assertNotIn("PRIVATE_TOKEN", saved["error"])
        self.assertNotIn("PRIVATE_TOKEN", str(service.log.warning.call_args_list))
        self.assertNotIn("private.invalid", str(service.log.warning.call_args_list))
        self.assertEqual(service.client.download_media.await_count, settings.max_retries)


    def test_stream_exhaustion_preserves_safe_destination_cause_and_recorded_path(self) -> None:
        from tgcloud.storage.rclone import RcloneError
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        settings = make_settings(Path(temp.name))
        db = TaskDB(settings.data_dir / "tasks.db")
        self.addCleanup(db.close)
        task, _ = db.create_task(chat_id=1, message_id=13,
            sender_id=settings.allowed_user_id, file_name="synthetic.mp4", file_size=100)
        db.update(task["id"], state="reserved", transfer_mode="stream")
        service = make_service(settings, db)
        service.client = SimpleNamespace(get_messages=AsyncMock(return_value=SimpleNamespace(media=True)))
        service._notify = AsyncMock()
        service.rclone = FakeRclone()
        service.rclone.open_upload_stream = AsyncMock(side_effect=RcloneError(
            "503 PRIVATE_TOKEN https://private.invalid/secret"))
        with patch("tgcloud.transfer.streaming.asyncio.sleep", new=AsyncMock()):
            asyncio.run(service._download_one(task["id"]))
        saved = db.get(task["id"])
        self.assertEqual(saved["state"], "download_failed")
        self.assertEqual(saved["failure_kind"], "server")
        self.assertIn("存储服务暂时异常", saved["error"])
        self.assertNotIn("PRIVATE_TOKEN", saved["error"])
        self.assertNotIn("private.invalid", str(service.log.warning.call_args_list))
        self.assertTrue(saved["remote_path"])
        self.assertEqual(service.rclone.removed, [])
        self.assertEqual(service.rclone.open_upload_stream.await_count, settings.max_retries)

    def test_unauthorized_sender_cannot_create_task(self) -> None:
        service = TransferService.__new__(TransferService)
        service.settings = SimpleNamespace(allowed_user_id=123)
        service.log = Mock()
        service.db = Mock()
        event = SimpleNamespace(sender_id=999)
        asyncio.run(service._on_message(event))
        service.db.create_task.assert_not_called()

    def test_confirm_command_records_manual_115_confirmation(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            task, _ = db.create_task(
                chat_id=1,
                message_id=12,
                sender_id=settings.allowed_user_id,
                file_name="visible.mp4",
                file_size=100,
            )
            db.update(
                task["id"],
                state="completed",
                remote_path="visible.mp4",
                uploaded_bytes=100,
            )
            service = make_service(settings, db)
            event = SimpleNamespace(reply=AsyncMock())

            asyncio.run(
                service._command_confirm(event, ["/confirm", f"#{task['id']}"])
            )

            self.assertEqual(db.get(task["id"])["state"], "confirmed")
            reply = event.reply.await_args.args[0]
            self.assertIn("云存储官方端已由你确认", reply)
            self.assertIn("人工确认", reply)
            db.close()

    def test_confirm_command_refuses_active_transfer(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            task, _ = db.create_task(
                chat_id=1,
                message_id=13,
                sender_id=settings.allowed_user_id,
                file_name="active.mp4",
                file_size=100,
            )
            db.update(task["id"], state="uploading")
            service = make_service(settings, db)
            event = SimpleNamespace(reply=AsyncMock())

            asyncio.run(
                service._command_confirm(event, ["/confirm", str(task["id"])])
            )

            self.assertEqual(db.get(task["id"])["state"], "uploading")
            self.assertIn("还不能确认", event.reply.await_args.args[0])
            db.close()

    def test_status_omits_historical_completed_tasks(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            settings = make_settings(Path(temp))
            db = TaskDB(settings.data_dir / "tasks.db")
            try:
                task, _ = db.create_task(
                    chat_id=1,
                    message_id=14,
                    sender_id=settings.allowed_user_id,
                    file_name="history.mp4",
                    file_size=100,
                )
                db.update(task["id"], state="completed", remote_path="history.mp4")
                confirmed, _ = db.create_task(
                    chat_id=1,
                    message_id=15,
                    sender_id=settings.allowed_user_id,
                    file_name="legacy.mp4",
                    file_size=100,
                )
                db.update(
                    confirmed["id"], state="completed", remote_path="legacy.mp4"
                )
                db.confirm_115(confirmed["id"])
                service = make_service(settings, db)
                service.destination_healthy = True
                service.destination_scope = "target"
                service.snapshot = ResourceSnapshot(
                    10,
                    2 * 1024**3,
                    0,
                    60 * 1024**3,
                    0,
                    time.time(),
                )
                service.download_window = SimpleNamespace(value=3)
                service.upload_window = SimpleNamespace(value=2)

                text = service._format_status()

                self.assertIn("⏳ 0　⬇️ 0　⬆️ 0　❌ 0", text)
                self.assertEqual(text.count("0 B/s"), 3)
                self.assertNotIn("Bot 完成 2", text)
                self.assertNotIn("并发窗口", text)
                self.assertNotIn("/confirm", text)
                self.assertNotIn("115", text)
            finally:
                db.close()


class QueueAndResourceSimulationTests(unittest.TestCase):
    def test_concurrent_reservations_cannot_exceed_twenty_gb(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            db = TaskDB(Path(temp) / "tasks.db")
            gb = 1024**3
            tasks = [
                db.create_task(
                    chat_id=1,
                    message_id=index,
                    sender_id=9,
                    file_name=f"{index}.bin",
                    file_size=gb,
                )[0]
                for index in range(1, 41)
            ]

            def reserve(task_id: int) -> bool:
                ok, _ = db.reserve(
                    task_id,
                    budget_bytes=20 * gb,
                    current_free_bytes=50 * gb,
                    minimum_free_bytes=20 * gb,
                )
                return ok

            with ThreadPoolExecutor(max_workers=20) as executor:
                results = list(
                    executor.map(reserve, (int(task["id"]) for task in tasks))
                )
            self.assertEqual(sum(results), 20)
            self.assertEqual(db.used_local_bytes(), 20 * gb)
            db.close()

    def test_one_hundred_mixed_files_finish_in_automatic_batches(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            db = TaskDB(root / "tasks.db")
            mb = 1024**2
            gb = 1024**3
            sizes = (5 * mb, 100 * mb, 3 * gb, 4 * gb)
            for index in range(100):
                db.create_task(
                    chat_id=1,
                    message_id=index + 1,
                    sender_id=9,
                    file_name=f"{index + 1}.bin",
                    file_size=sizes[index % len(sizes)],
                )

            rounds = 0
            while db.counts().get("queued", 0):
                admitted: list[int] = []
                after_id = 0
                while True:
                    batch = db.list_states(
                        ("queued",),
                        limit=17,
                        ready_only=True,
                        after_id=after_id,
                    )
                    if not batch:
                        break
                    after_id = int(batch[-1]["id"])
                    for task in batch:
                        ok, _ = db.reserve(
                            int(task["id"]),
                            budget_bytes=20 * gb,
                            current_free_bytes=50 * gb,
                            minimum_free_bytes=20 * gb,
                        )
                        if ok:
                            admitted.append(int(task["id"]))
                self.assertTrue(admitted, "队列不应永久卡住")
                for task_id in admitted:
                    db.update(task_id, state="completed")
                rounds += 1
                self.assertLess(rounds, 100)

            self.assertEqual(db.counts(), {"completed": 100})
            self.assertGreater(rounds, 1)
            db.close()

    def test_small_files_after_two_hundred_blocked_large_files_are_not_starved(
        self,
    ) -> None:
        async def scenario() -> None:
            with tempfile.TemporaryDirectory() as temp:
                settings = make_settings(Path(temp))
                db = TaskDB(settings.data_dir / "tasks.db")
                gb = 1024**3
                mb = 1024**2
                for index in range(200):
                    db.create_task(
                        chat_id=1,
                        message_id=index + 1,
                        sender_id=9,
                        file_name=f"large-{index}.bin",
                        file_size=4 * gb,
                    )
                for index in range(5):
                    db.create_task(
                        chat_id=1,
                        message_id=1000 + index,
                        sender_id=9,
                        file_name=f"small-{index}.bin",
                        file_size=mb,
                    )
                service = make_service(settings, db)
                service.snapshot = ResourceSnapshot(
                    cpu_percent=10,
                    memory_available=2 * gb,
                    swap_used=0,
                    disk_free=settings.min_free_disk_bytes + 10 * mb,
                    network_bytes_per_second=10 * mb,
                    sampled_at=time.time(),
                )
                service.destination_healthy = True
                service.download_window = SimpleNamespace(value=5)
                service._sample_fresh = Mock(return_value=True)  # type: ignore[method-assign]
                service._destination_ready = Mock(return_value=True)  # type: ignore[method-assign]

                async def hold(_: int) -> None:
                    await asyncio.Event().wait()

                service._download_one = hold  # type: ignore[method-assign]
                try:
                    # This scenario controls capacity through the snapshot. Keep
                    # the live check consistent, including on small CI tmpfs.
                    with patch("tgcloud.transfer.scheduler.shutil.disk_usage", return_value=SimpleNamespace(
                        free=service.snapshot.disk_free,
                    )):
                        await service._start_downloads()
                    self.assertEqual(
                        sorted(service.download_tasks), list(range(201, 206))
                    )
                finally:
                    running_tasks = list(service.download_tasks.values())
                    for running in running_tasks:
                        running.cancel()
                    await asyncio.gather(*running_tasks, return_exceptions=True)
                    db.close()

        asyncio.run(scenario())

    def test_destination_outage_starts_no_new_downloads(self) -> None:
        service = TransferService.__new__(TransferService)
        service.snapshot = SimpleNamespace(disk_free=50 * 1024**3)
        service.destination_healthy = False
        service.download_window = SimpleNamespace(value=20)
        service.download_tasks = {}
        service.db = Mock()
        asyncio.run(service._start_downloads())
        service.db.list_states.assert_not_called()

    def test_disk_emergency_cancels_partial_download_and_requeues(self) -> None:
        async def scenario() -> None:
            with tempfile.TemporaryDirectory() as temp:
                settings = make_settings(Path(temp))
                db = TaskDB(settings.data_dir / "tasks.db")
                task, _ = db.create_task(
                    chat_id=1,
                    message_id=1,
                    sender_id=9,
                    file_name="large.bin",
                    file_size=10,
                )
                ok, _ = db.reserve(
                    task["id"],
                    budget_bytes=settings.local_budget_bytes,
                    current_free_bytes=50 * 1024**3,
                    minimum_free_bytes=settings.min_free_disk_bytes,
                )
                self.assertTrue(ok)
                service = make_service(settings, db)
                started = asyncio.Event()

                class SlowTelegram:
                    async def get_messages(self, *_: object, **__: object):
                        return SimpleNamespace(media=True)

                    async def download_media(
                        self,
                        _: object,
                        *,
                        file: str,
                        progress_callback,
                    ) -> str:
                        Path(file).write_bytes(b"12345")
                        progress_callback(5, 10)
                        started.set()
                        await asyncio.Event().wait()
                        return file

                notifications: list[str] = []

                async def notify(message: str) -> None:
                    notifications.append(message)

                service.client = SlowTelegram()  # type: ignore[assignment]
                service._notify = notify  # type: ignore[method-assign]
                running = asyncio.create_task(service._download_one(task["id"]))
                service.download_tasks = {task["id"]: running}
                await started.wait()
                service.snapshot = ResourceSnapshot(
                    cpu_percent=20,
                    memory_available=2 * 1024**3,
                    swap_used=0,
                    disk_free=settings.min_free_disk_bytes,
                    network_bytes_per_second=0,
                    sampled_at=0,
                )
                await service._enforce_disk_emergency()
                recovered = db.get(task["id"])
                self.assertEqual(recovered["state"], "queued")
                self.assertIn("磁盘触及安全线", recovered["wait_reason"])
                self.assertFalse((settings.download_dir / f"{task['id']}.part").exists())
                self.assertTrue(any("重新排队" in text for text in notifications))
                db.close()

        asyncio.run(scenario())


if __name__ == "__main__":
    unittest.main()
