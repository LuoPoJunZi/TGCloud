"""Stdlib-only src-layout, fingerprint and immutable bootstrap regressions."""
from __future__ import annotations

import hashlib
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from local_project_fixture import host_source, installed

from tgcloud.deployment.compatibility import literal_version
from tgcloud.deployment.fingerprints import (
    V2_PREFIX,
    deployed_fingerprint,
    fingerprint,
)
from tgcloud.deployment.instances import Instance
from tgcloud.deployment.payload import build_payload, payload_sources
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.deployment.upgrade import assert_unmodified
from tgcloud.version import VERSION

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("layout_bootstrap", ROOT / ".github/scripts/prepare_bootstrap.py")
bootstrap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bootstrap)


class FingerprintTests(unittest.TestCase):
    def setUp(self):
        self.private = tempfile.TemporaryDirectory(prefix="tgcloud-identity-")
        self.addCleanup(self.private.cleanup)
        self.root = Path(self.private.name).resolve()

    def write(self, name, content=b"VALUE = 1\n"):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    def test_v2_tracks_nested_changes_additions_and_deletions(self):
        self.write("app/__init__.py")
        nested = self.write("app/a/module.py")
        first = fingerprint(self.root / "app")
        self.assertTrue(first.startswith(V2_PREFIX))
        nested.write_bytes(b"VALUE = 2\n")
        second = fingerprint(self.root / "app")
        self.assertNotEqual(first, second)
        added = self.write("app/b/module.py")
        third = fingerprint(self.root / "app")
        self.assertNotEqual(second, third)
        added.unlink()
        self.assertEqual(fingerprint(self.root / "app"), second)

    def test_same_basename_is_identified_by_full_relative_path(self):
        first = self.write("app/a/same.py", b"ONE = 1\n")
        second = self.write("app/b/same.py", b"TWO = 2\n")
        old = fingerprint(self.root / "app")
        a, b = first.read_bytes(), second.read_bytes()
        first.write_bytes(b)
        second.write_bytes(a)
        self.assertNotEqual(old, fingerprint(self.root / "app"))

    def test_legacy_algorithm_matches_flat_historical_manifest_exactly(self):
        self.write("app/z.py", b"Z = 2\n")
        self.write("app/a.py", b"A = 1\n")
        manifest = "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  app/{path.name}\n"
                           for path in sorted((self.root / "app").glob("*.py")))
        legacy = hashlib.sha256(manifest.encode()).hexdigest()
        self.assertEqual(fingerprint(self.root / "app", legacy=True), legacy)
        self.assertEqual(deployed_fingerprint(self.root / "app"), legacy)
        self.assertNotEqual(fingerprint(self.root / "app"), legacy)

    def test_missing_protocol_marker_does_not_trust_unknown_nested_instance(self):
        self.write("app/unknown/module.py")
        with self.assertRaisesRegex(ValueError, "nested or unknown"):
            deployed_fingerprint(self.root / "app")
        self.write("app/tgcloud/deployment/fingerprints.py")
        self.assertEqual(deployed_fingerprint(self.root / "app"), fingerprint(self.root / "app"))

    def test_symlink_guard_with_real_link_or_mocked_windows_metadata(self):
        self.write("app/a.py")
        target = self.write("outside.py")
        link = self.root / "app/link.py"
        try:
            link.symlink_to(target)
        except OSError:
            # Windows can deny link creation: exercise the guard using mocked
            # metadata without presenting this as a real link integration test.
            link.write_bytes(b"# synthetic link metadata fixture\n")
            original = Path.is_symlink
            with patch.object(Path, "is_symlink", lambda path: path == link or original(path)), self.assertRaisesRegex(ValueError, "links"):
                fingerprint(self.root / "app")
            return
        with self.assertRaisesRegex(ValueError, "links"):
            fingerprint(self.root / "app")


class SourceLayoutTests(unittest.TestCase):
    def test_static_version_adapters_never_execute_old_or_new_code(self):
        for name, variable in (("src/tgcloud/version.py", "VERSION"),
                               ("app/tgcloud/version.py", "VERSION"),
                               ("app/version.py", "VERSION"), ("app/__init__.py", "__version__")):
            with self.subTest(path=name), tempfile.TemporaryDirectory() as private:
                root = Path(private)
                path = root / name
                path.parent.mkdir(parents=True)
                path.write_text(f'{variable} = "1.3.0"\nraise RuntimeError("not executed")\n', encoding="utf-8")
                self.assertEqual(literal_version(root), "1.3.0")
                path.write_text(f'{variable} = dynamic_version()\n', encoding="utf-8")
                with self.assertRaises(ValueError):
                    literal_version(root)

    def test_both_editions_share_identical_core_and_keep_runtime_contract(self):
        cd2 = payload_sources(ROOT, PRODUCTS["clouddrive2"])
        core = {name for name in cd2 if name.startswith("app/")}
        self.assertEqual(set(PRODUCTS), {"clouddrive2"})
        self.assertFalse(any("openlist" in name for name in cd2))
        for name in core:
            self.assertTrue(cd2[name].is_file(), name)
        for name in ("app/tgcloud/transfer/streaming.py", "app/tgcloud/persistence/sqlite.py",
                     "app/deployment_check.py", "app/backup_database.py", "app/healthcheck.py"):
            self.assertIn(name, core)
        dockerfile = (ROOT / "packaging/Dockerfile").read_text(encoding="utf-8")
        self.assertIn("USER 10001:10001", dockerfile)
        self.assertIn('"tgcloud.app"', dockerfile)
        application = (ROOT / "src/tgcloud/app.py").read_text(encoding="utf-8")
        client = (ROOT / "src/tgcloud/telegram/client.py").read_text(encoding="utf-8")
        self.assertIn('database_path(settings.data_dir)', application)
        self.assertIn('settings.data_dir / "bot"', client)

    def test_runtime_host_and_direct_fingerprint_agree_without_bot_dependencies(self):
        with tempfile.TemporaryDirectory() as private:
            context = Path(private).resolve() / "context"
            build_payload(ROOT, PRODUCTS["clouddrive2"], context)
            expected = deployed_fingerprint(context / "app")
            code = ("import sys; sys.path[:0]=" + repr([str(context), str(context / "app")]) + "; "
                    "from app.deployment_check import code_fingerprint; print(code_fingerprint()); "
                    "assert not any(m in sys.modules for m in ('telethon','cryptg','psutil'))")
            result = subprocess.run([sys.executable, "-I", "-S", "-c", code], cwd=private,
                                    capture_output=True, text=True, timeout=20, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), expected)
            direct = subprocess.run([sys.executable, "-I", "-S", str(context / "app/tgcloud/deployment/fingerprints.py"),
                                     "--root", str(context / "app")], cwd=private, capture_output=True, text=True, timeout=20, check=False)
            self.assertEqual(direct.returncode, 0, direct.stderr)
            self.assertEqual(direct.stdout.strip(), expected)

    def test_current_marker_detects_changed_missing_and_extra_program_bytes(self):
        with tempfile.TemporaryDirectory() as private:
            context = Path(private).resolve() / "context"
            identity = installed(context)
            instance = Instance(PRODUCTS["clouddrive2"], context, True, VERSION, source_digest=identity.digest)
            assert_unmodified(instance, ROOT)
            path = context / "app/tgcloud/domain/naming.py"
            original = path.read_bytes()
            for content in (original + b"\n# changed\n", original.replace(b"\r\n", b"\n") if b"\r\n" in original else original.replace(b"\n", b"\r\n")):
                path.write_bytes(content)
                with self.assertRaises(ValueError):
                    assert_unmodified(instance, ROOT)
            path.unlink()
            with self.assertRaises((ValueError, OSError)):
                assert_unmodified(instance, ROOT)
            path.write_bytes(original)
            extra = context / "app/tgcloud/domain/unknown.py"
            extra.write_bytes(b"# unknown code\n")
            with self.assertRaisesRegex(ValueError, "额外或缺失"):
                assert_unmodified(instance, ROOT)
            extra.unlink()
            assert_unmodified(instance, ROOT)


class BootstrapLayoutTests(unittest.TestCase):
    def test_manifests_match_all_actual_nested_sources(self):
        for name, paths in (("bootstrap", bootstrap.helper_files(ROOT)), ("runtime", bootstrap.runtime_files(ROOT))):
            manifest = json.loads((ROOT / f"packaging/{name}-manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest, {"format": 1, "files": paths})

    def test_candidates_preserve_complete_local_entry_without_online_fallback(self):
        for name, command in (("install.sh", "install"), ("install-lan.sh", "lan")):
            original = (ROOT / name).read_text(encoding="utf-8")
            candidate = bootstrap.render_entry(ROOT, name)
            self.assertEqual(candidate, original)
            self.assertIn('exec python3 -B "$script_root/scripts/run_source.py" ' + command + ' "$@"', candidate)
            self.assertNotIn("installer_commit", candidate)
            self.assertNotIn("curl", candidate)

    def test_complete_local_helper_bundle_runs_without_checkout_or_bot_packages(self):
        with tempfile.TemporaryDirectory() as private:
            root = Path(private) / "host-runtime"
            host_source(root)
            for command in ("install", "lan", "uninstall"):
                result = subprocess.run([sys.executable, "-I", "-S", "-B", "-X", "utf8",
                    str(root / "scripts/run_source.py"), command, "--help"], cwd=private,
                    capture_output=True, text=True, encoding="utf-8", timeout=20, check=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("--edition", result.stdout)

    def test_cli_help_and_imports_work_outside_checkout_without_site_packages(self):
        with tempfile.TemporaryDirectory() as private:
            for command, option in (("install", "--configure-https"), ("lan", "--lan-ip"), ("uninstall", "--edition")):
                with self.subTest(command=command):
                    result = subprocess.run([sys.executable, "-I", "-S", "-X", "utf8", str(ROOT / "scripts/run_source.py"), command, "--help"],
                                            cwd=private, capture_output=True, text=True, encoding="utf-8", timeout=20, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn(option, result.stdout)
            code = ("import sys; sys.path.insert(0," + repr(str(ROOT / "src")) + "); "
                    "import tgcloud.cli.vps, tgcloud.deployment.proxy.lan, tgcloud.deployment.uninstall; "
                    "assert not any(n.split('.')[0] in {'telethon','cryptg','psutil','PySide6','paramiko'} for n in sys.modules)")
            result = subprocess.run([sys.executable, "-I", "-S", "-c", code], cwd=private,
                                    capture_output=True, text=True, timeout=20, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
