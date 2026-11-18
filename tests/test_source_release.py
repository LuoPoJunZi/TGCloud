"""Source release tests in disposable Git repositories; no real tags or network."""
from __future__ import annotations

import ast
import hashlib
import importlib.util
import io
import re
import tarfile
import tempfile
import unittest
from pathlib import Path

from tgcloud.deployment.payload import payload_sources
from tgcloud.deployment.profiles import PRODUCTS

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("source_release", ROOT / ".github/scripts/source_release.py")
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)
bootstrap_spec = importlib.util.spec_from_file_location("prepare_bootstrap", ROOT / ".github/scripts/prepare_bootstrap.py")
bootstrap = importlib.util.module_from_spec(bootstrap_spec)
bootstrap_spec.loader.exec_module(bootstrap)


class SourceReleaseTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Validate public release inputs in a disposable two-commit A/B history.
        # Copy only explicit public runtime/build inputs, never private archives.
        names = set(package.REQUIRED)
        for product in PRODUCTS.values():
            names.update(path.relative_to(ROOT).as_posix() for path in payload_sources(ROOT, product).values())
        cls.snapshot = {name: (ROOT / name).read_bytes() for name in names}
        cls.snapshot["install.sh"] = bootstrap.render_entry(ROOT).encode()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        for name, content in self.snapshot.items():
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
        # Isolate fixture history and user identity from the working repository.
        package.git(self.root, "init", "-q")
        package.git(self.root, "config", "user.email", "test@example.invalid")
        package.git(self.root, "config", "user.name", "Offline Test")
        package.git(self.root, "config", "commit.gpgsign", "false")
        package.git(self.root, "config", "tag.gpgsign", "false")
        package.git(self.root, "config", "core.autocrlf", "false")
        package.git(self.root, "config", "core.hooksPath", str(Path(self.temp.name) / "no-hooks"))
        # A detached maintenance process can recreate .git files during cleanup.
        # Fixture repositories are short-lived and need no background maintenance.
        package.git(self.root, "config", "gc.auto", "0")
        package.git(self.root, "config", "gc.autoDetach", "false")
        package.git(self.root, "config", "maintenance.auto", "false")
        self.commit()
        pin = package.git(self.root, "rev-parse", "HEAD").decode().strip()
        path = self.root / "install.sh"
        text = re.sub(r'^(installer_commit\s*=\s*)"[0-9a-f]{40}"$', lambda match: match[1] + f'"{pin}"', path.read_text(encoding="utf-8"), flags=re.MULTILINE)
        path.write_text(text, encoding="utf-8", newline="\n")
        tree = ast.parse((self.root / "src/tgcloud/version.py").read_text(encoding="utf-8"))
        version = next(node.value.value for node in tree.body if hasattr(node, "targets"))
        self.tag = "v" + version
        package.git(self.root, "tag", self.tag)
        self.output = Path(self.temp.name) / "artifacts"

    def commit(self):
        package.git(self.root, "add", "--all")
        package.git(self.root, "commit", "-qm", "fixture")

    def build(self):
        package.build(self.root, self.output, self.tag)

    def test_build_and_verify_exact_source_without_local_outputs(self):
        (self.root / "private-untracked.txt").write_text("FAKE_OFFLINE_SECRET", encoding="utf-8")
        self.build()
        package.verify(self.output, self.tag)
        with tarfile.open(self.output / package.artifact_name(self.tag)) as archive:
            names = archive.getnames()
            self.assertFalse(any("private-untracked" in name for name in names))
            self.assertIn(f"TGCloud-{self.tag}/install.sh", names)
            self.assertNotIn(f"TGCloud-{self.tag}/install-lan.sh", names)
        with self.assertRaisesRegex(ValueError, "refusing to replace"):
            self.build()

    def test_uncommitted_source_is_rejected(self):
        with (self.root / "src/tgcloud/deployment/resources.py").open("a", encoding="utf-8") as writer:
            writer.write("\n# changed\n")
        with self.assertRaisesRegex(ValueError, "committed"):
            self.build()
        self.assertFalse(self.output.exists())

    def test_local_source_updates_need_no_online_helper_pin(self):
        path = self.root / "src/tgcloud/deployment/resources.py"
        path.write_bytes(path.read_bytes() + b"\n# synthetic source revision\n")
        self.commit()
        commit = package.git(self.root, "rev-parse", "HEAD").decode().strip()
        package.validate_pins(self.root, commit)
        self.assertNotIn("installer_commit", (self.root / "install.sh").read_text())

    def test_wrong_checkout_or_invalid_tag_stops_before_output(self):
        (self.root / "extra.txt").write_text("fixture", encoding="utf-8")
        self.commit()
        with self.assertRaisesRegex(ValueError, "checkout must match"):
            self.build()
        with self.assertRaises(ValueError):
            package.build(self.root, self.output, "--help")
        self.assertFalse(self.output.exists())

    def test_corrupted_checksum_and_extra_asset_are_rejected(self):
        self.build()
        sums = self.output / "SHA256SUMS.txt"
        original = sums.read_bytes()
        sums.write_bytes(b"0" * 64 + b"\n")
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            package.verify(self.output, self.tag)
        sums.write_bytes(original)
        (self.output / "unexpected.exe").write_bytes(b"not-an-executable")
        with self.assertRaisesRegex(ValueError, "asset set"):
            package.verify(self.output, self.tag)

    def test_missing_payload_and_unsafe_archive_rejected_even_with_matching_checksum(self):
        self.build()
        path = self.output / package.artifact_name(self.tag)
        original = path.read_bytes()
        for case in ("missing", "unsafe", "desktop"):
            with self.subTest(case=case):
                result = io.BytesIO()
                with tarfile.open(fileobj=io.BytesIO(original), mode="r:gz") as source, tarfile.open(fileobj=result, mode="w:gz") as dest:
                    for member in source:
                        if case != "desktop" and member.name.endswith("/src/tgcloud/storage/rclone.py"):
                            continue
                        dest.addfile(member, source.extractfile(member) if member.isfile() else None)
                    if case == "unsafe":
                        member = tarfile.TarInfo(f"TGCloud-{self.tag}/../../outside")
                        dest.addfile(member, io.BytesIO())
                    if case == "desktop":
                        member = tarfile.TarInfo(f"TGCloud-{self.tag}/legacy/desktop/installer.py")
                        dest.addfile(member, io.BytesIO())
                path.write_bytes(result.getvalue())
                (self.output / "SHA256SUMS.txt").write_text(hashlib.sha256(result.getvalue()).hexdigest() + "  " + path.name + "\n", encoding="ascii")
                with self.assertRaisesRegex(ValueError, "unsafe|source manifest|build output"):
                    package.verify(self.output, self.tag)

    def test_unified_source_rejects_retired_entry_or_missing_management_core(self):
        self.build()
        path = self.output / package.artifact_name(self.tag)
        original = path.read_bytes()
        for case in ("uninstall.sh", "install-webdav.sh", "missing-core", "unknown-api"):
            with self.subTest(case=case):
                result = io.BytesIO()
                with tarfile.open(fileobj=io.BytesIO(original), mode="r:gz") as source, tarfile.open(fileobj=result, mode="w:gz") as dest:
                    for member in source:
                        if case == "missing-core" and member.name.endswith("/src/tgcloud/deployment/uninstall.py"):
                            continue
                        if case == "unknown-api" and member.name.endswith("/packaging/external-webdav.json"):
                            body = b'{"api":999,"proxy_schema":2,"owner":"tgcloud.external_webdav"}'
                            member.size = len(body)
                            dest.addfile(member, io.BytesIO(body))
                        else:
                            dest.addfile(member, source.extractfile(member) if member.isfile() else None)
                    if case.endswith(".sh"):
                        member = tarfile.TarInfo(f"TGCloud-{self.tag}/" + case)
                        dest.addfile(member, io.BytesIO())
                path.write_bytes(result.getvalue())
                (self.output / "SHA256SUMS.txt").write_text(hashlib.sha256(result.getvalue()).hexdigest() + "  " + path.name + "\n", encoding="ascii")
                with self.assertRaisesRegex(ValueError, "unexpected standalone|missing required|unsupported"):
                    package.verify(self.output, self.tag)

    def test_helper_manifest_cannot_omit_nested_code_or_escape_root(self):
        path = self.root / "packaging/bootstrap-manifest.json"
        original = path.read_bytes()
        for files in (["../outside.py"], ["scripts/run_source.py"]):
            with self.subTest(files=files):
                import json
                path.write_text(json.dumps({"format": 1, "files": files}), encoding="utf-8")
                self.commit()
                commit = package.git(self.root, "rev-parse", "HEAD").decode().strip()
                with self.assertRaisesRegex(ValueError, "source manifest"):
                    package.validate_pins(self.root, commit)
        path.write_bytes(original)

    def test_complete_source_dispatch_precedes_the_online_bootstrap(self):
        text = (ROOT / "install.sh").read_text(encoding="utf-8")
        local = text.partition("# TGCloud online bootstrap.")[0]
        for command in ("install", "lan"):
            self.assertIn('exec python3 -B "$script_root/scripts/run_source.py" ' + command + ' "$@"', local)
        for remote in ("installer_commit", "curl", "https://"):
            self.assertNotIn(remote, local)

    def test_download_only_entry_is_rejected_before_packaging(self):
        (self.root / "install.sh").write_text('#!/bin/bash\ncurl https://example.invalid/bootstrap\n', encoding="utf-8")
        self.commit()
        commit = package.git(self.root, "rev-parse", "HEAD").decode().strip()
        with self.assertRaisesRegex(ValueError, "invalid local-source Linux entry"):
            package.validate_pins(self.root, commit)

    def test_untrusted_online_source_cannot_pass_the_entry_contract(self):
        path = self.root / "install.sh"
        path.write_text(path.read_text(encoding="utf-8") + "\ncurl https://example.invalid/source\n", encoding="utf-8")
        self.commit()
        commit = package.git(self.root, "rev-parse", "HEAD").decode().strip()
        with self.assertRaisesRegex(ValueError, "invalid local-source"):
            package.validate_pins(self.root, commit)


class SourceContractRejectionTests(unittest.TestCase):
    def candidate(self, directory, files):
        tag = "v0.0.1"
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w:gz") as archive:
            for name, body in files.items():
                member = tarfile.TarInfo(f"TGCloud-{tag}/" + name)
                member.size = len(body)
                archive.addfile(member, io.BytesIO(body))
        body = output.getvalue()
        name = package.artifact_name(tag)
        (directory / name).write_bytes(body)
        (directory / "SHA256SUMS.txt").write_text(hashlib.sha256(body).hexdigest() + "  " + name + "\n", encoding="ascii")
        return tag

    def test_unsupported_contract_is_rejected_even_with_matching_checksum(self):
        with tempfile.TemporaryDirectory() as private:
            directory = Path(private)
            files = {name: b"" for name in package.REQUIRED}
            files["packaging/external-webdav.json"] = b'{"api":999,"proxy_schema":2,"owner":"tgcloud.external_webdav"}'
            tag = self.candidate(directory, files)
            with self.assertRaisesRegex(ValueError, "unsupported"):
                package.verify(directory, tag)

    def test_unrecognized_source_layout_is_rejected_before_code_execution(self):
        for location in ("app/version.py", "other_package/version.py"):
            with self.subTest(location=location), tempfile.TemporaryDirectory() as private:
                directory = Path(private)
                tag = self.candidate(directory, {location: b'VERSION="0.0.1"\nraise RuntimeError("must not run")'})
                with self.assertRaisesRegex(ValueError, "missing required"):
                    package.verify(directory, tag)


if __name__ == "__main__":
    unittest.main()
