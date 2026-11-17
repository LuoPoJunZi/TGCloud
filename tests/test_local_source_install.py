"""CD2 source installation boundaries; no host, Docker, Telegram or network use."""
from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from tgcloud.cli import vps
from tgcloud.deployment import instances, local_source, payload, releases, upgrade
from tgcloud.deployment.profiles import CLOUDDRIVE2_PRODUCT
from tgcloud.deployment.proxy import lan
from tgcloud.version import VERSION

ROOT = Path(__file__).resolve().parents[1]


class LocalSourceInstallTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()

    def test_source_snapshot_uses_only_explicit_public_inputs_and_detects_changes(self):
        values = local_source.content(ROOT)
        self.assertNotIn(".env", values)
        self.assertFalse(any(name.startswith((".git/", "data/", "downloads/", "legacy/")) for name in values))
        self.assertNotIn("src/tgcloud/storage/unsupported.py", values)
        self.assertIn("src/tgcloud/deployment/local_source.py", values)
        self.assertIn("docs/install.md", values)
        changed = dict(values, **{"README.md": values["README.md"] + b"\nchanged\n"})
        self.assertNotEqual(local_source.digest(values), local_source.digest(changed))

    def installed(self, *, state="ready"):
        directory = self.root / "installed"
        payload.build_payload(ROOT, CLOUDDRIVE2_PRODUCT, directory)
        source = local_source.LocalSource(ROOT, VERSION, "a" * 64)
        local_source.write_marker(directory, local_source.marker_for(directory, source, state=state))
        return directory, source

    def test_exact_source_identity_and_same_version_edit_upgrade(self):
        directory, source = self.installed()
        identity = local_source.verify_installed(directory)
        current = instances.Instance(CLOUDDRIVE2_PRODUCT, directory, True, VERSION, source_digest=identity["source_sha256"])
        self.assertEqual(upgrade.action_for(current, source), "current")
        changed = local_source.LocalSource(ROOT, VERSION, "b" * 64)
        self.assertEqual(upgrade.action_for(current, changed), "upgrade")
        upgrade.assert_unmodified(current, ROOT)

    def test_pending_marker_is_not_adopted_until_successfully_activated(self):
        directory, source = self.installed(state="pending")
        with self.assertRaises(ValueError):
            local_source.verify_installed(directory)
        upgrade.record_release(directory, source)
        self.assertEqual(local_source.verify_installed(directory)["state"], "ready")

    def test_unknown_instance_rejection_does_not_read_or_modify_credentials(self):
        directory = self.root / "old-instance"
        directory.mkdir()
        private = directory / ".env"
        private.write_bytes(b"SYNTHETIC=preserve-exactly\r\n")
        with self.assertRaises(ValueError):
            local_source.verify_installed(directory)
        self.assertEqual(private.read_bytes(), b"SYNTHETIC=preserve-exactly\r\n")
        self.assertEqual({item.name for item in directory.iterdir()}, {".env"})

    def test_changed_code_and_extra_code_are_rejected(self):
        directory, _ = self.installed()
        code = directory / "app/tgcloud/version.py"
        original = code.read_bytes()
        code.write_bytes(original + b"\n# synthetic modification\n")
        with self.assertRaises(ValueError):
            local_source.verify_installed(directory)
        code.write_bytes(original)
        (directory / "app/extra.py").write_bytes(b"# synthetic extra code\n")
        with self.assertRaises(ValueError):
            local_source.verify_installed(directory)

    def test_wrong_project_and_path_traversal_cannot_be_activated(self):
        directory, source = self.installed()
        original = json.loads((directory / local_source.MARKER).read_bytes())
        for value in (dict(original, project="old-project"), dict(original, files=original["files"] | {"../private": "f" * 64})):
            local_source.write_marker(directory, value)
            with self.assertRaises(ValueError):
                upgrade.record_release(directory, source)

    def test_all_install_parsers_default_to_cd2_without_gateway_choice(self):
        from tgcloud.addons.external_webdav import cli
        for parser in (vps.parser(), lan.parser(), cli.parser()):
            self.assertEqual(parser.parse_args([]).edition, "clouddrive2")
            self.assertNotIn("unsupported", parser.format_help().lower())
            self.assertNotIn("both", parser.format_help().lower())

    def test_public_check_uses_real_local_snapshot_without_release_downloads_or_terminal(self):
        instance = instances.Instance(CLOUDDRIVE2_PRODUCT, self.root / "new-instance", False)
        with (patch.object(vps.preflight, "validate_host"), patch.object(vps.architecture, "check_docker"),
              patch.object(vps.instances, "docker_inventory", return_value=set()),
              patch.object(vps.instances, "discover", return_value=instance),
              patch.object(vps.install, "make_plan", return_value=Mock()) as plan,
              patch.object(vps.terminal, "preview"), patch.object(vps.terminal, "Terminal") as terminal,
              patch.object(releases, "ReleaseClient", side_effect=AssertionError("must stay offline")) as client):
            self.assertEqual(vps.main(["--check", "--source-dir", str(ROOT)]), 0)
        source = plan.call_args.args[1]
        self.assertEqual((source.version, source.kind), (VERSION, "local-source"))
        self.assertEqual(len(source.digest), 64)
        terminal.assert_not_called()
        client.assert_not_called()

    def test_lan_check_uses_local_snapshot_without_online_lookup(self):
        instance = instances.Instance(CLOUDDRIVE2_PRODUCT, self.root / "new-instance", False)
        with (patch.object(lan, "validate_environment"), patch.object(lan.instances, "docker_inventory", return_value=set()),
              patch.object(lan.instances, "discover", return_value=instance), patch.object(lan, "load_state", return_value=None),
              patch.object(lan, "make_plan", return_value=Mock()) as plan, patch.object(lan, "preview"),
              patch.object(lan.terminal, "Terminal") as terminal,
              patch.object(releases, "ReleaseClient", side_effect=AssertionError("must stay offline")) as client):
            self.assertEqual(lan.main(["--check", "--lan-ip", "192.168.1.10", "--source-dir", str(ROOT)]), 0)
        self.assertEqual(plan.call_args.args[1].kind, "local-source")
        terminal.assert_not_called()
        client.assert_not_called()
