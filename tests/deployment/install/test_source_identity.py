"""TGCloud source identity and read-only preflight; no network or services."""
from __future__ import annotations

import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch

from tests.fixtures import ROOT
from tests.fixtures.local_project import installed
from tgcloud.deployment import local_source, releases


class SourceIdentityTests(unittest.TestCase):
    def test_valid_instance_has_current_project_version_and_file_manifest(self):
        with tempfile.TemporaryDirectory() as private:
            directory = Path(private).resolve() / "instance"
            installed(directory)
            identity = local_source.verify_installed(directory)
            self.assertEqual(identity["project"], "tgcloud")
            self.assertEqual(identity["version"], releases.source_version(ROOT))
            self.assertIn("docker-compose.yml", identity["files"])
            self.assertEqual(identity["state"], "ready")

    def test_source_digest_covers_names_content_and_file_set(self):
        values = {"a.py": b"value=1\n"}
        original = local_source.digest(values)
        for changed in ({"nested/a.py": values["a.py"]}, {"a.py": b"value=2\n"},
                        dict(values, **{"b.py": b"value=1\n"})):
            with self.subTest(files=tuple(changed)):
                self.assertNotEqual(original, local_source.digest(changed))

    def test_literal_version_does_not_import_candidate_code(self):
        with tempfile.TemporaryDirectory() as private:
            root = Path(private).resolve()
            path = root / "src/tgcloud/version.py"
            path.parent.mkdir(parents=True)
            path.write_text('VERSION = "0.0.1"\nraise RuntimeError("must not execute")\n')
            self.assertEqual(releases.source_version(root), "0.0.1")
            path.write_text('VERSION = discover_version()\n')
            with self.assertRaises(ValueError):
                releases.source_version(root)

    def test_http_failures_are_classified_without_remote_body_or_secret(self):
        for status in (404, 403, 429, 500):
            error = urllib.error.HTTPError("https://api.github.com/secret", status, "PRIVATE_BODY", {}, None)
            with self.subTest(status=status), patch.object(releases, "open_public", side_effect=error):
                with self.assertRaises(releases.ReleaseLookupError) as caught:
                    releases.ReleaseClient().json(releases.API + "/releases/latest")
                self.assertEqual(caught.exception.status, status)
                self.assertNotIn("PRIVATE_BODY", str(caught.exception))
                self.assertNotIn("/secret", str(caught.exception))
