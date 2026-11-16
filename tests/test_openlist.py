"""Retired backend rejection: no deployment, cleanup, network or private data access."""
from __future__ import annotations

import base64
import os
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

from tgcloud.cli import vps
from tgcloud.compose import packaging
from tgcloud.config import Settings
from tgcloud.deployment import configuration, instances, payload, uninstall
from tgcloud.deployment.profiles import (
    CLOUDDRIVE2_PRODUCT,
    PRODUCTS,
    RETIRED_IDENTITIES,
    require_product,
)
from tgcloud.deployment.proxy import lan
from tgcloud.deployment.proxy.state import (
    empty_state,
    normalize_state,
    state_with_route,
)
from tgcloud.persistence.bundle import pack
from tgcloud.storage.policies import policy_for

ROOT = Path(__file__).resolve().parents[1]
RETIRED = replace(CLOUDDRIVE2_PRODUCT, key="openlist", install_dir="/opt/tgcloud-openlist")

class RetiredBackendTests(unittest.TestCase):
    def test_product_runtime_identities_are_isolated(self):
        self.assertEqual(set(PRODUCTS), {"clouddrive2"})
        self.assertIn("/opt/tgcloud-openlist", RETIRED_IDENTITIES["openlist"]["directories"])
        with self.assertRaises(ValueError):
            require_product(RETIRED)

    def test_product_release_identity_is_tgcloud_v1(self):
        self.assertEqual(CLOUDDRIVE2_PRODUCT.key, "clouddrive2")
        self.assertEqual(CLOUDDRIVE2_PRODUCT.bot_container, "tgcloud-clouddrive2-bot")
        self.assertEqual(CLOUDDRIVE2_PRODUCT.install_dir, "/opt/tgcloud-clouddrive2")

    def test_openlist_config_contains_neutral_keys_and_no_115_credentials(self):
        # Reject before reading any configuration values, including credentials.
        with self.assertRaises(ValueError):
            configuration.new_config(RETIRED, {})

    def test_openlist_resource_advice_does_not_require_clouddrive_fuse_or_memory(self):
        # There is no longer an alternate gateway that bypasses CD2 preflight.
        self.assertTrue(CLOUDDRIVE2_PRODUCT.requires_fuse)
        with self.assertRaises(ValueError):
            policy_for(SimpleNamespace(storage_backend="openlist"))

    def test_openlist_archive_replaces_only_backend_specific_payload(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            marker = root / "existing-user-data"
            marker.write_bytes(b"synthetic existing data")
            target = root / "candidate"
            with self.assertRaises(ValueError):
                payload.build_payload(ROOT, RETIRED, target)
            self.assertFalse(target.exists())
            self.assertEqual(marker.read_bytes(), b"synthetic existing data")

    def test_openlist_compose_is_private_pinned_and_restricted(self):
        self.assertFalse((ROOT / "packaging/compose/docker-compose.openlist.yml").exists())
        with self.assertRaises(ValueError):
            packaging.render(ROOT, "openlist", packaging.image_name("a" * 64), source_digest="a" * 64)

    def test_openlist_install_separates_deploy_from_destination_verification(self):
        for parser in (vps.parser(), lan.parser()):
            self.assertEqual(parser.parse_args([]).edition, "clouddrive2")
            for edition in ("openlist", "both", "all", "openlist;id"):
                with self.subTest(edition=edition), self.assertRaises(SystemExit):
                    parser.parse_args(["--edition", edition])

    def test_openlist_legacy_detection_precedes_mutation(self):
        session = Mock()
        with self.assertRaises(ValueError):
            instances.discover(RETIRED, set(), session, Mock())
        session.run.assert_not_called()

    def test_openlist_admin_reset_requires_explicit_confirmation(self):
        self.assertFalse((ROOT / "packaging/hooks/openlist/openlist_admin.sh").exists())
        session = Mock()
        with self.assertRaises(uninstall.UninstallError):
            uninstall.build_plan(("openlist",), session, purge_data=True, purge_backups=True)
        session.run.assert_not_called()

    def test_openlist_manage_supports_safe_desktop_operations(self):
        raw = dict(empty_state(), domains={"openlist": {"domain": "retired.example.test", "backend_port": 5244}})
        import json
        with self.assertRaises(ValueError):
            normalize_state(json.dumps(raw))
        with self.assertRaises(ValueError):
            state_with_route(empty_state(), RETIRED, "retired.example.test")

    def test_neutral_webdav_environment_loads_openlist_profile(self):
        for key in ("TGCLOUD_STORAGE_BACKEND", "TG115_STORAGE_BACKEND"):
            env = {"TELEGRAM_API_ID": "123", "ALLOWED_USER_ID": "456", key: "openlist"}
            with patch.dict(os.environ, env, clear=True), self.assertRaises(RuntimeError):
                Settings.from_env(create_directories=False)

    def test_legacy_tg115_environment_keys_remain_supported(self):
        encoded = lambda value: base64.b64encode(value.encode()).decode()
        env = {"TELEGRAM_API_ID": "123", "ALLOWED_USER_ID": "456",
               "TELEGRAM_API_HASH_B64": encoded("a" * 32), "BOT_TOKEN_B64": encoded("123456:" + "a" * 30),
               "WEBDAV_URL_B64": encoded(CLOUDDRIVE2_PRODUCT.webdav_url), "WEBDAV_USERNAME_B64": encoded("synthetic"),
               "WEBDAV_PASSWORD_B64": encoded("synthetic password"), "TG115_STORAGE_BACKEND": "clouddrive2",
               "TG115_RCLONE_REMOTE_NAME": "cd2"}
        with patch.dict(os.environ, env, clear=True):
            value = Settings.from_env(create_directories=False)
        self.assertEqual((value.storage_backend, value.rclone_remote_name), ("clouddrive2", "cd2"))

    def test_remote_name_cannot_be_injected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "retired.tar.gz"
            with self.assertRaises(ValueError):
                pack(root / "unread-existing-data", archive, "openlist", "0.0.1")
            self.assertFalse(archive.exists())
