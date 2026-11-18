"""Bash harness for isolated installer backup and rollback tests."""
from __future__ import annotations

import os
import sqlite3
import subprocess  # nosec B404
import sys
import tarfile
import tempfile
import unittest
from contextlib import closing
from pathlib import Path

from tests.fixtures import ROOT
from tests.fixtures.payload import payload
from tests.fixtures.shell import BASH, bash_path

PAYLOAD = payload("clouddrive2")


INSTALL_SCRIPT = (ROOT / "packaging/hooks/clouddrive2/remote_install.sh").read_text(encoding="utf-8")


PREFIX = INSTALL_SCRIPT.split('[[ "$(id -u)" -eq 0 ]]', 1)[0]


UPGRADE = 'if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then\n  BACKUP=' + INSTALL_SCRIPT.split(
    'if [[ -f "$INSTALL_DIR/docker-compose.yml" ]]; then\n  BACKUP=', 1,
)[1].split('\nlog "提交已预检的程序和配置"', 1)[0]


DIGEST = "tgcloud-local@sha256:" + "a" * 64


HARNESS = r'''
test_root="$(builtin cd "$1" && pwd -P)"
INSTALL_DIR="$test_root/installation"
BACKUP_DIR="$test_root/backups"
BACKUP="$test_root/config.tar.gz"
OLD_IMAGE_ID=old-bot
OLD_IMAGE_TAG="$TGCLOUD_TEST_DIGEST"
ROLLBACK_ARMED=true
DATABASE_BACKUP=""
DATABASE_NAME="${TGCLOUD_TEST_DB_NAME:-tgcloud.db}"
RELEASE_STAGE="$TGCLOUD_TEST_PAYLOAD"

# All Docker operations are simulated. SQLite and tar use real implementations.
python3() { "$TGCLOUD_TEST_PYTHON" "$@"; }
chown() { :; }
wait_unsupported_http() { :; }
wait_unsupported() { :; }
wait_container_healthy() { :; }
wait_healthy() { :; }
docker() {
  printf '%s\n' "$*" >> "$test_root/calls"
  case "$1" in
    image)
      [[ "$TGCLOUD_TEST_MODE" != missing-image ]] || return 1
      if [[ "$TGCLOUD_TEST_MODE" == wrong-image ]]; then printf 'different-image\n';
      else printf 'old-bot\n'; fi
      ;;
    tag)
      [[ "$3" != *@sha256:* ]] || return 1
      ;;
    inspect)
      if [[ "$3" == *'.Config.Image'* ]]; then
        if [[ "${@: -1}" == "$BOT_CONTAINER" ]]; then printf '%s\n' "$OLD_IMAGE_TAG";
        else printf '%s\n' "$OLD_IMAGE_TAG"; fi
      elif [[ "$3" == *'.State.Running'* ]]; then printf 'true\n';
      elif [[ "$3" == *'.Image'* ]]; then
        if [[ "${@: -1}" == "$BOT_CONTAINER" ]]; then printf 'old-bot\n';
        else printf 'old-bot\n'; fi
      else printf 'healthy\n'; fi
      ;;
    stop)
      if [[ "${@: -1}" == "$BOT_CONTAINER" ]]; then touch "$test_root/bot-stopped"; fi
      ;;
    start) rm -f -- "$test_root/gateway-stopped" ;;
    compose)
      if [[ "$2" == rm && "$TGCLOUD_TEST_MODE" == cannot-stop-bot ]]; then return 1; fi
      if [[ "$2" == stop ]]; then touch "$test_root/gateway-stopped"; fi
      if [[ "$2" == up ]]; then rm -f -- "$test_root/gateway-stopped"; fi
      ;;
  esac
  if [[ "$*" == *'app.backup_database'* ]]; then
    # Use the existing production SQLite snapshot helper for a real fixture DB.
    python3 - "$TGCLOUD_TEST_PAYLOAD" "$INSTALL_DIR${@: -2:1}" "$INSTALL_DIR${@: -1}" <<'PY'
import sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
from tgcloud.persistence.backup import backup_database
backup_database(Path(sys.argv[2]), Path(sys.argv[3]))
PY
  fi
  return 0
}
install() {
  local target="${@: -1}"
  # Git Bash cannot apply POSIX directory ownership/modes to Windows fixtures.
  if [[ "$1" == -d && "$TGCLOUD_TEST_WINDOWS" == 1 ]]; then return 0; fi
  if { [[ "$target" == "$BACKUP_DIR"/database-*.db && "$TGCLOUD_TEST_MODE" == backup-copy-* ]]; } \
    || { [[ "$target" == "$INSTALL_DIR"/data/.rollback-*.db && "$TGCLOUD_TEST_MODE" == restore-copy-fail ]]; }; then
    python3 - "$target" <<'PY'
import sys
from pathlib import Path
Path(sys.argv[1]).write_bytes(b"interrupted copy fixture")
PY
    case "$TGCLOUD_TEST_MODE" in
      backup-copy-interrupt) kill -TERM "$$" ;;
      backup-copy-corrupt) return 0 ;;
      *) return 1 ;;
    esac
  fi
  command install "$@"
}
tar() {
  if [[ "$*" == *'unsupported/data'* ]]; then
    [[ -f "$test_root/gateway-stopped" ]] || return 7
    if [[ "$TGCLOUD_TEST_MODE" == archive-fail ]]; then return 1; fi
  fi
  command tar "$@"
}
'''


class BackupShellFixture(unittest.TestCase):
    def setUp(self):
        self.private = tempfile.TemporaryDirectory(prefix="tgcloud-backup-test-")
        self.addCleanup(self.private.cleanup)
        self.root = Path(self.private.name)
        self.installation = self.root / "installation"
        (self.installation / "data").mkdir(parents=True)
        (self.root / "backups").mkdir()
        helper = self.installation / "app/tgcloud/persistence/paths.py"
        helper.parent.mkdir(parents=True, exist_ok=True)
        helper.write_bytes((ROOT / "src/tgcloud/persistence/paths.py").read_bytes())
        self.database = self.installation / "data/tgcloud.db"
        self.snapshot = self.root / "good.db"
        self.make_database(self.database, 1)
        self.make_database(self.snapshot, 2)
        (self.installation / "docker-compose.yml").write_text("# fixture\n", encoding="utf-8")
        (self.installation / ".env").write_text("# private fixture\n", encoding="utf-8")
        with tarfile.open(self.root / "config.tar.gz", "w:gz") as archive:
            for name in ("docker-compose.yml", ".env"):
                archive.add(self.installation / name, arcname=name)

    @staticmethod
    def make_database(path, value):
        with closing(sqlite3.connect(path)) as database:
            database.execute("CREATE TABLE fixture(value INTEGER)")
            database.execute("INSERT INTO fixture VALUES (?)", (value,))
            database.commit()

    def execute(self, action, mode="ok", before=""):
        # Only repository shell code and fixed test scenarios, never user input.
        result = subprocess.run(  # nosec B603
            [BASH, "-s", "--", bash_path(self.root)],
            input=PREFIX + HARNESS + before + action,
            env=os.environ | {
                "TGCLOUD_TEST_PYTHON": bash_path(Path(sys.executable)),
                "TGCLOUD_TEST_DB_NAME": self.database.name,
                "TGCLOUD_TEST_PAYLOAD": bash_path(PAYLOAD),
                "TGCLOUD_TEST_DIGEST": DIGEST,
                "TGCLOUD_TEST_MODE": mode,
                "TGCLOUD_TEST_WINDOWS": "1" if os.name == "nt" else "0",
                "PYTHONIOENCODING": "utf-8",
            },
            capture_output=True, text=True, encoding="utf-8", timeout=30, check=False,
            **({"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}),
        )
        calls = self.root / "calls"
        return result, calls.read_text(encoding="utf-8") if calls.exists() else ""

    def assert_database(self, path, value):
        with closing(sqlite3.connect(path)) as database:
            self.assertEqual(database.execute("PRAGMA quick_check").fetchall(), [("ok",)])
            self.assertEqual(database.execute("SELECT value FROM fixture").fetchone(), (value,))

    def preserve_sidecars(self):
        return {self.installation / f"data/tgcloud.db-{kind}": kind.encode() for kind in ("wal", "shm")}

    def gateway_fixture(self):
        for name in ("data.db", "config.json", "log/log.log", "temp/upload-part", "temporary-keep/user-file"):
            path = self.installation / "unsupported/data" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"fixture")
