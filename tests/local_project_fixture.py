"""Synthetic TGCloud sources/markers shared by offline regression fixtures."""
import hashlib
import json
import os
import shlex
import subprocess
import sys
import tempfile
from functools import lru_cache
from pathlib import Path

from tgcloud.deployment import local_source, payload, runtime
from tgcloud.deployment.profiles import CLOUDDRIVE2_PRODUCT
from tgcloud.version import VERSION

ROOT = Path(__file__).resolve().parents[1]


@lru_cache(maxsize=1)
def source():
    return local_source.LocalSource(ROOT, VERSION, local_source.digest(local_source.content(ROOT)))


def installed(directory):
    """Build and mark only new synthetic program files, before adding private data."""
    payload.build_payload(ROOT, CLOUDDRIVE2_PRODUCT, directory)
    identity = source()
    local_source.write_marker(directory, local_source.marker_for(directory, identity, state="ready"))
    return identity


def host_source(directory):
    """A complete offline host runtime; source identity is explicit and local."""
    values = runtime.files(ROOT)
    for name, body in values.items():
        path = directory / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(body)
        path.chmod(0o600)
    identity = {"schema": 1, "owner": "tgcloud.cli", "version": VERSION,
                "origin": source().origin,
                "files": {name: hashlib.sha256(body).hexdigest() for name, body in values.items()}}
    (directory / "installation.json").write_text(json.dumps(identity), encoding="utf-8")
    for path in (directory, *directory.rglob("*")):
        path.chmod(0o700 if path.is_dir() else 0o600)
    return source().origin


def check_shell_entry(test, bash, script, command, arguments):
    """Execute the real local Shell entry with a recording child in a private tree."""
    def bash_path(path):
        value = path.as_posix()
        return "/" + value[0].lower() + value[2:] if os.name == "nt" else value

    for exit_code in (0, 7):
        with tempfile.TemporaryDirectory(prefix="tgcloud-local-entry-") as private:
            root = Path(private).resolve()
            target = root / script
            target.write_bytes((ROOT / script).read_bytes())
            source = root / "scripts/run_source.py"
            source.parent.mkdir()
            source.write_text("import json,os,sys\nprint(json.dumps({'args':sys.argv[1:],'stdin':sys.stdin.read()}))\nsys.exit(int(os.environ['FIXTURE_EXIT']))\n", encoding="utf-8")
            marker = root / "src/tgcloud/cli/commands.py"
            marker.parent.mkdir(parents=True)
            marker.write_bytes(b"# synthetic entry marker\n")
            binaries = root / "bin"
            binaries.mkdir()
            wrapper = binaries / "python3"
            wrapper.write_text("#!/bin/sh\nexec " + shlex.quote(bash_path(Path(sys.executable))) + ' "$@"\n', encoding="utf-8", newline="\n")
            wrapper.chmod(0o755)
            environment = dict(os.environ, PATH=str(binaries) + os.pathsep + os.environ.get("PATH", ""), FIXTURE_EXIT=str(exit_code))
            result = subprocess.run([bash, bash_path(target), *arguments], cwd=root.parent,
                                    input="synthetic stdin\n", text=True, capture_output=True,
                                    timeout=20, check=False, env=environment)
            test.assertEqual(result.returncode, exit_code, result.stderr)
            record = json.loads(result.stdout)
            test.assertEqual(record, {"args": [command, *arguments], "stdin": "synthetic stdin\n"})
            marker.unlink()
            rejected = subprocess.run([bash, bash_path(target), *arguments], cwd=root.parent,
                                      text=True, capture_output=True, timeout=20, check=False, env=environment)
            test.assertEqual(rejected.returncode, 2, rejected.stderr)
            test.assertFalse((root / "data").exists())
