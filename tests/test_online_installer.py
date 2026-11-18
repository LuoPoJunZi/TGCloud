"""Standalone online entry: synthetic downloads, no host or Docker changes."""
from __future__ import annotations

import hashlib
import io
import json
import os
import runpy
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = (ROOT / "install.sh").read_text(encoding="utf-8")
VALIDATOR = SCRIPT.split("python3 -c '\n", 1)[1].split("\n' \"$tgcloud_work\"", 1)[0]
VERSION = runpy.run_path(str(ROOT / "src/tgcloud/version.py"))["VERSION"]
TAG = "v" + VERSION
ASSET = "TGCloud-" + TAG + "-source.tar.gz"


def bash_path(path):
    value = Path(path).as_posix()
    return "/" + value[0].lower() + value[2:] if os.name == "nt" else value


class OnlineInstallerTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="tgcloud-online-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.downloads = self.root / "downloads"
        self.downloads.mkdir()

    def archive(self, *, version=VERSION, extra=()):
        bodies = {
            "scripts/run_source.py": b'import json,sys; print("FIXTURE_WIZARD="+json.dumps({"args":sys.argv[1:],"stdin":sys.stdin.read()})); sys.exit(7)\n',
            "src/tgcloud/cli/commands.py": b"# synthetic fixture only\n",
            "src/tgcloud/version.py": ("VERSION = " + repr(version) + "\n").encode(),
            "LICENSE": b"synthetic test license\n",
        }
        with tarfile.open(self.downloads / ASSET, "w:gz") as bundle:
            for name, body in bodies.items():
                member = tarfile.TarInfo("TGCloud-" + TAG + "/" + name)
                member.size = len(body)
                bundle.addfile(member, io.BytesIO(body))
            for member, body in extra:
                bundle.addfile(member, io.BytesIO(body) if member.isfile() else None)
        self.checksum()

    def checksum(self):
        body = (self.downloads / ASSET).read_bytes()
        (self.downloads / "SHA256SUMS.txt").write_text(
            hashlib.sha256(body).hexdigest() + "  " + ASSET + "\n", encoding="ascii")

    def validate(self):
        return subprocess.run([sys.executable, "-B", "-c", VALIDATOR, str(self.downloads), TAG],
                              capture_output=True, text=True, encoding="utf-8", check=False, timeout=15)

    def test_verified_source_extracts_without_executing_downloaded_code(self):
        self.archive()
        result = self.validate()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("FIXTURE_WIZARD=", result.stdout)
        self.assertTrue((self.downloads / "source/LICENSE").is_file())

    def test_tampered_source_stops_before_extraction(self):
        self.archive()
        with (self.downloads / ASSET).open("ab") as writer:
            writer.write(b"tampered")
        self.assertEqual(self.validate().returncode, 2)
        self.assertFalse((self.downloads / "source").exists())

    def test_checksum_name_cannot_select_another_file(self):
        self.archive()
        (self.downloads / "SHA256SUMS.txt").write_text("a" * 64 + "  ../outside\n", encoding="ascii")
        self.assertEqual(self.validate().returncode, 2)
        self.assertFalse((self.downloads / "source").exists())

    def test_links_traversal_and_duplicates_are_rejected_with_valid_checksums(self):
        for name, kind in (("TGCloud-" + TAG + "/../outside", tarfile.REGTYPE),
                           ("TGCloud-" + TAG + "/link", tarfile.SYMTYPE),
                           ("TGCloud-" + TAG + "/scripts/run_source.py", tarfile.REGTYPE)):
            with self.subTest(name=name, kind=kind):
                shutil.rmtree(self.downloads / "source", ignore_errors=True)
                member = tarfile.TarInfo(name)
                member.type, member.linkname = kind, "../../outside"
                member.size = 1 if kind == tarfile.REGTYPE else 0
                self.archive(extra=((member, b"x"),))
                self.assertEqual(self.validate().returncode, 2)
                self.assertFalse((self.downloads / "outside").exists())

    def test_wrong_source_version_is_rejected_without_running_wizard(self):
        self.archive(version="9.9.9")
        result = self.validate()
        self.assertEqual(result.returncode, 2)
        self.assertNotIn("FIXTURE_WIZARD=", result.stdout)

    def shell(self):
        candidates = (shutil.which("bash"), "F:/Program Files/Git/bin/bash.exe", "C:/Program Files/Git/bin/bash.exe")
        for candidate in candidates:
            if candidate and Path(candidate).is_file() and "system32" not in candidate.lower():
                return candidate
        self.fail("Bash is required to validate the standalone online entry")

    def launch(self, *arguments, failed_download=False, stream_entry=False):
        self.archive()
        binaries = self.root / "bin"
        binaries.mkdir()
        scripts = {
            "id": "#!/bin/sh\nprintf '0\\n'\n",
            "python3": "#!/bin/sh\nexec " + shlex.quote(bash_path(sys.executable)) + ' "$@"\n',
            "curl": """#!/usr/bin/env bash
set -eu
if [[ "${TGCLOUD_FIXTURE_FAIL:-}" == 1 ]]; then exit 22; fi
output=''
while (($#)); do
  if [[ "$1" == -o ]]; then output="$2"; shift 2; else url="$1"; shift; fi
done
printf '%s\\n' "$url" >> "$TGCLOUD_FIXTURE_URLS"
if [[ "$url" == https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh ]]; then
  cat "$TGCLOUD_FIXTURE_ENTRY"
  exit 0
fi
cp "$TGCLOUD_FIXTURE_DOWNLOADS/${url##*/}" "$output"
""",
        }
        for name, content in scripts.items():
            path = binaries / name
            path.write_text(content, encoding="utf-8", newline="\n")
            path.chmod(0o755)
        temporary = self.root / "temporary"
        temporary.mkdir()
        # Run a copied entry outside any source checkout; all downloads are synthetic.
        entry = self.root / "standalone.sh"
        entry.write_text(SCRIPT, encoding="utf-8", newline="\n")
        environment = dict(os.environ, PATH=str(binaries) + os.pathsep + os.environ.get("PATH", ""),
                           TMPDIR=bash_path(temporary), PYTHONIOENCODING="utf-8",
                           TGCLOUD_FIXTURE_FAIL="1" if failed_download else "0",
                           TGCLOUD_FIXTURE_URLS=bash_path(self.root / "urls"),
                           TGCLOUD_FIXTURE_DOWNLOADS=bash_path(self.downloads),
                           TGCLOUD_FIXTURE_ENTRY=bash_path(entry))
        entry_argument = (
            "<(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)"
            if stream_entry else shlex.quote(bash_path(entry))
        )
        command = "export PATH=" + shlex.quote(bash_path(binaries)) + ':"$PATH"; exec bash ' + entry_argument + " " + shlex.join(arguments)
        result = subprocess.run([self.shell(), "-c", command], input="terminal answer\n",
                                capture_output=True, text=True, encoding="utf-8", errors="replace",
                                env=environment, check=False, timeout=30)
        self.assertEqual(list(temporary.iterdir()), [], "only this launch staging must be cleaned")
        return result

    def test_standalone_lan_forwards_terminal_arguments_and_exit_code(self):
        result = self.launch("--lan", "--check", "space value")
        self.assertEqual(result.returncode, 7, result.stderr)
        record = json.loads(next(line.split("=", 1)[1] for line in result.stdout.splitlines()
                                 if line.startswith("FIXTURE_WIZARD=")))
        self.assertEqual(record, {"args": ["lan", "--check", "space value"], "stdin": "terminal answer\n"})
        self.assertTrue(all("/releases/download/" + TAG + "/" in url
                            for url in (self.root / "urls").read_text().splitlines()))

    def test_failed_download_never_opens_wizard(self):
        result = self.launch(failed_download=True)
        self.assertEqual(result.returncode, 22, result.stderr)
        self.assertNotIn("FIXTURE_WIZARD=", result.stdout)

    def test_standalone_public_entry_uses_the_existing_install_wizard(self):
        result = self.launch("--check")
        self.assertEqual(result.returncode, 7, result.stderr)
        record = json.loads(next(line.split("=", 1)[1] for line in result.stdout.splitlines()
                                 if line.startswith("FIXTURE_WIZARD=")))
        self.assertEqual(record["args"], ["install", "--check"])
        self.assertIn("TGCloud " + TAG, result.stdout)
        self.assertIn("TGCloud 安装文件校验通过，正在启动向导。", result.stdout)
        self.assertNotIn("· CloudDrive2", result.stdout)

    def test_documented_process_substitution_preserves_terminal_and_arguments(self):
        result = self.launch("--check", "space value", stream_entry=True)
        self.assertEqual(result.returncode, 7, result.stderr)
        record = json.loads(next(line.split("=", 1)[1] for line in result.stdout.splitlines()
                                 if line.startswith("FIXTURE_WIZARD=")))
        self.assertEqual(record, {"args": ["install", "--check", "space value"], "stdin": "terminal answer\n"})
        urls = (self.root / "urls").read_text().splitlines()
        self.assertEqual(urls[0], "https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh")
        self.assertEqual(len(urls), 3)
