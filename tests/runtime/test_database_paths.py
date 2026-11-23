"""TGCloud database identity and unrelated-data protection, without services."""
from __future__ import annotations

import io
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from contextlib import closing, redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch

from tests.fixtures import ROOT
from tests.fixtures import backup_shell as shell
from tgcloud.deployment import configuration
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.persistence import bundle, paths, rehearsal
from tgcloud.persistence.sqlite import TaskDB
from tgcloud.version import VERSION


class DatabaseNameTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.root.chmod(0o700)

    def database(self, name):
        path = self.root / name
        db = TaskDB(path)
        try:
            task, _ = db.create_task(chat_id=1, message_id=1, sender_id=1, file_name="synthetic.txt", file_size=4, media_key="synthetic:1")
            db.update(task["id"], state="completed")
        finally:
            db.close()
        return path

    def test_new_database_uses_project_name_without_creating_files_during_selection(self):
        self.assertEqual(paths.database_path(self.root), self.root / "tgcloud.db")
        self.assertEqual(list(self.root.iterdir()), [])
        with self.assertRaises(ValueError):
            paths.database_path(self.root, require_existing=True)

    def test_unrelated_database_is_rejected_without_renaming_or_empty_replacement(self):
        path = self.database("unrelated.db")
        before = path.read_bytes()
        for required in (False, True):
            with self.subTest(require_existing=required), self.assertRaisesRegex(ValueError, "其他数据库"):
                paths.database_path(self.root, require_existing=required)
        self.assertEqual(path.read_bytes(), before)
        self.assertFalse((self.root / "tgcloud.db").exists())

    def test_canonical_existing_database_remains_selected(self):
        path = self.database("tgcloud.db")
        self.assertEqual(paths.database_path(self.root, require_existing=True), path)
        self.assertFalse((self.root / "unrelated.db").exists())

    def test_both_names_are_ambiguous_and_untouched(self):
        for name in ("tgcloud.db", "unrelated.db"):
            self.database(name)
        before = {path.name: path.read_bytes() for path in self.root.iterdir()}
        with self.assertRaisesRegex(ValueError, "其他数据库"):
            paths.database_path(self.root)
        self.assertEqual(before, {path.name: path.read_bytes() for path in self.root.iterdir()})
        with self.assertRaises(ValueError):
            paths.database_entry(("data/tgcloud.db", "data/unrelated.db"))

    def test_orphan_sidecars_never_create_replacement_database(self):
        for name in paths.DATABASE_NAMES:
            for suffix in paths.SIDECARS:
                path = self.root / (name + suffix)
                path.write_bytes(b"synthetic")
                with self.subTest(name=path.name), self.assertRaises(ValueError):
                    paths.database_path(self.root)
                self.assertEqual(path.read_bytes(), b"synthetic")
                path.unlink()
        self.assertFalse(any((self.root / name).exists() for name in paths.DATABASE_NAMES))

    def test_hardlinks_and_nonfiles_are_rejected(self):
        target = self.root / "tgcloud.db"
        other = self.root / "alias"
        other.write_bytes(b"synthetic")
        os.link(other, target)
        with self.assertRaises(ValueError):
            paths.database_path(self.root)
        target.unlink()
        target.mkdir()
        with self.assertRaises(ValueError):
            paths.database_path(self.root)

    def test_symlink_rejected_even_if_other_database_exists(self):
        self.database("unrelated.db")
        link = self.root / "tgcloud.db"
        link.write_bytes(b"synthetic link stand-in")
        original = Path.is_symlink
        # Covers the same refusal where Windows disallows creating a real link.
        with patch.object(Path, "is_symlink", lambda path: path == link or original(path)), self.assertRaises(ValueError):
            paths.database_path(self.root)

    def test_standalone_selector_works_without_site_or_bot_dependencies(self):
        code = ROOT / "src/tgcloud/persistence/paths.py"
        result = subprocess.run([sys.executable, "-I", "-S", "-B", str(code), str(self.root)], capture_output=True, text=True, check=False, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "tgcloud.db")
        (self.root / "unrelated.db").write_bytes(b"synthetic")
        with redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(paths.main([str(self.root)]), 1)
        self.assertEqual(stdout.getvalue(), "")
        self.assertNotIn(str(self.root), stderr.getvalue())

    def test_generated_config_uses_current_backend_namespace(self):
        values = {"api_id":"123456", "api_hash":"a"*32, "bot_token":"123456789:SYNTHETIC_TEST_TOKEN_ONLY",
                  "user_id":"123456789", "webdav_user":"tgcloud", "webdav_password":"SYNTHETIC", "target":"",
                  "budget":"20", "reserve":"8", "timezone":"Asia/Shanghai", "admin_password":"SYNTHETIC"}
        for product in PRODUCTS.values():
            config = configuration.new_config(product, values)
            self.assertEqual(config["TGCLOUD_RCLONE_REMOTE_NAME"], "cd2")
            self.assertEqual(config["TGCLOUD_STORAGE_BACKEND"], product.key)

    def test_clouddrive2_backup_rehearsal_uses_current_database(self):
        for edition in PRODUCTS:
            for database_name in paths.DATABASE_NAMES:
                root = self.root / (edition + "-" + database_name)
                for name in ("data", "config", "clouddrive/config"):
                    (root / name).mkdir(parents=True)
                (root / ".env").write_text("SYNTHETIC=true\n")
                (root / "docker-compose.yml").write_text("services: {}\n")
                (root / "config/settings.conf").write_text("synthetic")
                gateway = root / ("clouddrive/config")
                (gateway / "settings.json").write_text("{}")
                with closing(sqlite3.connect(root / "data/bot.session")) as connection:
                    connection.execute("CREATE TABLE synthetic (value TEXT)")
                database = TaskDB(root / "data" / database_name)
                database.close()
                archive = self.root / (root.name + ".tar.gz")
                bundle.pack(root, archive, edition, VERSION)
                with bundle.verified_snapshot(archive, self.root, edition, VERSION) as (copy, _):
                    self.assertEqual(paths.database_path(copy / "data", require_existing=True).name, database_name)
                report = rehearsal.rehearse(archive, self.root, edition)
                self.assertEqual(report["tasks"], 0)
                self.assertEqual(report["session"], "not-loaded")


@unittest.skipUnless(shell.BASH, "Requires Bash for isolated upgrade and rollback fixtures")
class CanonicalShellTests(unittest.TestCase):
    def fixture(self):
        fixture = shell.BackupShellFixture(methodName="runTest")
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        return fixture

    def test_clouddrive_upgrade_snapshot_uses_canonical_name(self):
        fixture = self.fixture()
        result, _ = fixture.execute(shell.UPGRADE)
        self.assertEqual(result.returncode, 0, result.stderr)
        archives = list((fixture.root / "backups").glob("database-*.db"))
        self.assertEqual(len(archives), 1)
        fixture.assert_database(archives[0], 1)
        self.assertFalse((fixture.installation / "data/unrelated.db").exists())

    def test_each_edition_rollback_preserves_canonical_identity(self):
        for edition in PRODUCTS:
            fixture = self.fixture()
            script = (ROOT / "packaging/hooks" / edition / "remote_install.sh").read_text(encoding="utf-8")
            prefix = script.split('[[ "$(id -u)" -eq 0 ]]', 1)[0]
            before = 'DATABASE_BACKUP="$test_root/good.db"\nDATABASE_NAME=tgcloud.db\n'
            with patch.object(shell, "PREFIX", prefix):
                result, _ = fixture.execute("rollback_install\n", before=before)
            self.assertEqual(result.returncode, 0, result.stderr)
            fixture.assert_database(fixture.database, 2)
            self.assertFalse((fixture.installation / "data/unrelated.db").exists())


if __name__ == "__main__":
    unittest.main()
