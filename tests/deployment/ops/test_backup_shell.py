"""Current CD2 backup/rollback faults using synthetic SQLite and fake Docker."""

from __future__ import annotations

import tarfile
import unittest

from tests.fixtures.backup_shell import (
    DIGEST,
    UPGRADE,
    BackupShellFixture,
)
from tests.fixtures.shell import BASH


@unittest.skipUnless(BASH, "Requires Bash; Linux CI runs the fault regressions")
class CloudDriveBackupTests(BackupShellFixture):
    def test_digest_image_is_reused_and_old_bot_is_restarted(self):
        result, calls = self.execute("rollback_install\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("tag old-gateway", calls)
        self.assertNotIn("tag old-bot", calls)
        self.assertIn(DIGEST, calls)
        self.assertNotIn("compose up -d clouddrive2", calls)
        self.assertIn("compose up -d --no-deps --force-recreate tgcloud-clouddrive2-bot", calls)

    def test_unavailable_or_mismatched_digest_stops_before_program_removal(self):
        original = self.database.read_bytes()
        for mode in ("missing-image", "wrong-image"):
            with self.subTest(mode=mode):
                (self.root / "calls").unlink(missing_ok=True)
                result, calls = self.execute("rollback_install\n", mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("compose rm", calls)
                self.assertNotIn("compose up", calls)
                self.assertEqual(self.database.read_bytes(), original)

    def test_mutable_gateway_tag_still_supports_rollback(self):
        result, calls = self.execute("rollback_install\n", before="OLD_IMAGE_TAG=old-bot:stable\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("tag old-bot old-bot:stable", calls)
        self.assertNotIn("compose up -d clouddrive2", calls)

    def test_invalid_or_missing_snapshot_preserves_database_and_sidecars(self):
        original = self.database.read_bytes()
        sidecars = self.preserve_sidecars()
        for path, data in sidecars.items():
            path.write_bytes(data)
        for data in (b"", b"partial backup", None):
            with self.subTest(snapshot=data):
                candidate = self.root / "invalid.db"
                candidate.unlink(missing_ok=True)
                if data is not None:
                    candidate.write_bytes(data)
                result, calls = self.execute("rollback_install\n", before='DATABASE_BACKUP="$test_root/invalid.db"\n')
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("TGCLOUD_DATABASE_CHECK=FAILED", result.stderr)
                self.assertNotIn("compose rm", calls)
                self.assertEqual(self.database.read_bytes(), original)
                for path, expected in sidecars.items():
                    self.assertEqual(path.read_bytes(), expected)

    def test_restore_copy_failure_preserves_original_database_and_sidecars(self):
        original = self.database.read_bytes()
        sidecars = self.preserve_sidecars()
        for path, data in sidecars.items():
            path.write_bytes(data)
        result, calls = self.execute(
            "rollback_install\n", "restore-copy-fail", 'DATABASE_BACKUP="$test_root/good.db"\nDATABASE_NAME=tgcloud.db\n',
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("compose rm", calls)
        self.assertEqual(self.database.read_bytes(), original)
        for path, expected in sidecars.items():
            self.assertEqual(path.read_bytes(), expected)
        self.assertFalse(list((self.installation / "data").glob(".rollback-*")))

    def test_bot_removal_failure_preserves_database_and_sidecars(self):
        original = self.database.read_bytes()
        sidecars = self.preserve_sidecars()
        for path, data in sidecars.items():
            path.write_bytes(data)
        result, calls = self.execute(
            "rollback_install\n", "cannot-stop-bot", 'DATABASE_BACKUP="$test_root/good.db"\nDATABASE_NAME=tgcloud.db\n',
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("compose up", calls)
        self.assertEqual(self.database.read_bytes(), original)
        for path, expected in sidecars.items():
            self.assertEqual(path.read_bytes(), expected)

    def test_valid_snapshot_restores_database_and_restarts_bot(self):
        for path, data in self.preserve_sidecars().items():
            path.write_bytes(data)
        result, calls = self.execute("rollback_install\n", before='DATABASE_BACKUP="$test_root/good.db"\nDATABASE_NAME=tgcloud.db\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_database(self.database, 2)
        self.assertFalse(any(path.exists() for path in self.preserve_sidecars()))
        self.assertFalse(list((self.installation / "data").glob(".rollback-*")))
        self.assertIn("compose up -d --no-deps --force-recreate tgcloud-clouddrive2-bot", calls)

    def test_failed_program_replacement_restores_files_with_older_go_tool(self):
        config = (self.installation / ".env").read_bytes()
        compose = (self.installation / "docker-compose.yml").read_bytes()
        result, calls = self.execute('apply_upgrade_program || fail "升级程序文件替换失败"\n', "go-apply-fail",
                                     'DATABASE_BACKUP="$test_root/good.db"\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("已自动恢复升级前版本", result.stderr)
        self.assertIn("compose up -d --no-deps --force-recreate", calls)
        self.assertEqual((self.installation / ".env").read_bytes(), config)
        self.assertEqual((self.installation / "docker-compose.yml").read_bytes(), compose)
        self.assert_database(self.database, 2)

    def test_failed_go_rollback_does_not_repeat_recovery_through_shell(self):
        original = self.database.read_bytes()
        result, calls = self.execute("rollback_install\n", "go-rollback-fail",
                                     'DATABASE_BACKUP="$test_root/good.db"\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("rollback-upgrade failed", calls)
        self.assertNotIn("compose rm", calls)
        self.assertEqual(self.database.read_bytes(), original)

    def test_upgrade_snapshot_is_published_only_after_valid_copy(self):
        result, _ = self.execute(UPGRADE + '\n[[ -n "$DATABASE_BACKUP" ]]\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        snapshots = list((self.root / "backups").glob("database-*.db"))
        self.assertEqual(len(snapshots), 1)
        self.assert_database(snapshots[0], 1)
        self.assertFalse(list((self.installation / "data").glob(".upgrade-*")))

    def test_failed_corrupt_or_interrupted_copy_does_not_restore_partial_snapshot(self):
        original = self.database.read_bytes()
        for mode in ("backup-copy-fail", "backup-copy-corrupt", "backup-copy-interrupt"):
            with self.subTest(mode=mode):
                (self.root / "calls").unlink(missing_ok=True)
                result, calls = self.execute(UPGRADE, mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("TGCLOUD_RESULT=FAILED", result.stderr)
                self.assertIn("已恢复原 Bot", result.stderr)
                self.assertIn("start tgcloud-clouddrive2-bot", calls)
                self.assertNotIn("compose rm", calls)
                self.assertEqual(self.database.read_bytes(), original)
                self.assertFalse(list((self.root / "backups").glob("database-*.db")))
                self.assertFalse(list((self.installation / "data").glob(".rollback-*")))
                self.assertFalse(list((self.installation / "data").glob(".upgrade-*")))

    def test_go_backup_failures_leave_existing_installation_in_place(self):
        original = self.database.read_bytes()
        config = (self.installation / ".env").read_bytes()
        for mode in ("go-config-fail", "go-snapshot-fail"):
            with self.subTest(mode=mode):
                (self.root / "calls").unlink(missing_ok=True)
                result, calls = self.execute(UPGRADE, mode)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("TGCLOUD_RESULT=FAILED", result.stderr)
                if mode == "go-config-fail":
                    self.assertNotIn("stop --time", calls)
                    self.assertFalse(list((self.root / "backups").glob("config-*.tar.gz")))
                else:
                    self.assertIn("start tgcloud-clouddrive2-bot", calls)
                    self.assertIn("TGCLOUD_DATABASE_BACKUP=FAILED", result.stderr)
                    self.assertNotIn("app.backup_database", calls)
                self.assertNotIn("compose rm", calls)
                self.assertFalse((self.root / "bot-stopped").exists())
                self.assertEqual((self.installation / ".env").read_bytes(), config)
                self.assertEqual(self.database.read_bytes(), original)
                self.assertFalse(list((self.root / "backups").glob("*.partial")))

    def test_instance_snapshot_includes_cd2_configuration_but_not_mounted_cloud_files(self):
        from tests.fixtures.snapshot import SnapshotFixture
        fixture = SnapshotFixture(methodName="runTest")
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        instance, docker, target = fixture.fixture()
        cloud_file = instance.directory / "clouddrive/mounts/remote-file"
        cloud_file.parent.mkdir(parents=True)
        cloud_file.write_bytes(b"must not copy mounted cloud data")
        archive = fixture.create(instance, docker, target)
        with tarfile.open(archive) as contents:
            names = contents.getnames()
        self.assertTrue(any(name.endswith("clouddrive/config/settings.json") for name in names))
        self.assertFalse(any("remote-file" in name for name in names))
        self.assertTrue(all(item["running"] for item in docker.states.values()))

    def test_instance_snapshot_failure_resumes_services_and_preserves_source(self):
        from unittest.mock import patch

        from tests.fixtures.snapshot import SnapshotFixture
        from tgcloud.persistence import bundle
        fixture = SnapshotFixture(methodName="runTest")
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        instance, docker, target = fixture.fixture()
        before = (instance.directory / ".env").read_bytes()
        with patch.object(bundle, "pack", side_effect=OSError("synthetic archive failure")), self.assertRaises(OSError):
            fixture.create(instance, docker, target)
        self.assertEqual((instance.directory / ".env").read_bytes(), before)
        self.assertTrue(all(item["running"] for item in docker.states.values()))
        self.assertFalse(list(target.glob("*.tar.gz")))


if __name__ == "__main__":
    unittest.main()
