"""Temporary Compose instance and shared HTTPS fixtures."""
from __future__ import annotations

import copy
import json
import os
import tempfile
import unittest
from contextlib import contextmanager
from pathlib import Path
from unittest.mock import Mock, patch

from tests.fixtures import ROOT as SOURCE
from tests.fixtures.compose import local_bundle_files, write_candidate
from tgcloud.compose import https, packaging, runtime
from tgcloud.deployment import configuration
from tgcloud.deployment.fingerprints import deployed_fingerprint
from tgcloud.deployment.host import SafeLog
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.deployment.proxy.rendering import render_compose
from tgcloud.version import VERSION

IMAGE = "ghcr.io/luopojunzi/tgcloud@sha256:" + "a" * 64


class ComposeHttpsFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name).resolve()
        self.proxy = self.directory / "proxy"
        self.messages = []
        self.log = SafeLog(self.messages.append)
        self.session = Mock()
        self.session.run.side_effect = self.command
        self.containers = {}
        self.locked = False
        self.commands = []
        @contextmanager
        def shared_lock(*args, **kwargs):
            self.assertFalse(self.locked)
            self.locked = True
            try:
                yield
            finally:
                self.locked = False
        self.session.hold_lock.side_effect = shared_lock
        self.addCleanup(patch.stopall)
        patch.object(https, "PROXY_ROOT", str(self.proxy)).start()
        # Windows runs these host-independent decision tests. POSIX owner/mode
        # enforcement is exercised independently below with exact stat values.
        def physical(path):
            if not path.is_absolute() or path.resolve() != path or path.is_symlink() or not path.is_dir():
                raise runtime.ComposeError("physical directory required")
        def owned(path, **_):
            if path.is_symlink() or path.resolve() != path or not path.is_file():
                raise runtime.ComposeError("regular owner file required")
            return path.read_bytes()
        self.physical = https.physical_directory
        self.owned = https.owned_file
        patch.object(https, "physical_directory", side_effect=physical).start()
        patch.object(https, "owned_file", side_effect=owned).start()
        patch.object(runtime, "private_config_file").start()
        patch.object(https.architecture, "check_docker").start()
        patch.object(https.architecture, "check_container").start()
        patch.object(https.architecture, "check_image").start()
        if os.name != "posix":
            strict_parse = https.parse_compose_registry
            def synthetic_registry(raw, **_):
                value = json.loads(raw)
                checked = copy.deepcopy(value)
                for entry in checked.get("instances", {}).values():
                    source = Path(entry["root"])
                    if not source.is_relative_to(self.directory):
                        raise ValueError("fixture outside temporary directory")
                    entry["root"] = "/temporary-fixture/" + source.relative_to(self.directory).as_posix()
                strict_parse(json.dumps(checked), physical=False)
                return value
            patch.object(https, "parse_compose_registry", side_effect=synthetic_registry).start()

    def command(self, command, **kwargs):
        self.commands.append(command)
        if command.startswith("docker ps "):
            return 0, "\n".join(item["id"] for item in self.containers.values())
        if command.startswith("docker container inspect --format "):
            return 0, "\n".join(json.dumps(item) for item in self.containers.values())
        if command.startswith("docker image inspect --format "):
            for item in self.containers.values():
                if command.endswith(item["image"]):
                    if "{{json .}}" in command:
                        info = json.loads(local_bundle_files()["compose-bundle.json"])
                        return 0, json.dumps({"Id": item["image_id"], "Config": {"Labels": {
                            packaging.PROJECT_LABEL: packaging.PROJECT_ID, packaging.SOURCE_LABEL: info["source_sha256"]}}})
                    return 0, json.dumps(item["image_id"])
        if command.startswith(("docker run --rm --pull never", "docker exec ")):
            return 0, json.dumps({"version": VERSION, "fingerprint": deployed_fingerprint(self.directory / "clouddrive2/build-context/app")})
        if command == "id -u":
            return 0, "0\n"
        return 0, ""

    def candidate(self, edition="clouddrive2"):
        product = PRODUCTS[edition]
        root = write_candidate(self.directory / edition)
        image = json.loads(local_bundle_files()["compose-bundle.json"])["image"]
        config = configuration.new_config(product, {"api_id": "123456", "api_hash": "a" * 32,
            "bot_token": "123456789:FAKE_COMPOSE_HTTPS_TEST_ONLY", "user_id": "123456789", "webdav_user": "tgcloud",
            "webdav_password": "FAKE_PASSWORD_COMPOSE_TEST", "target": "", "budget": "20", "reserve": "8",
            "timezone": "Asia/Shanghai", "admin_password": "FAKE_ADMIN_COMPOSE_TEST"})
        config.update(TGCLOUD_INSTALL_MODE="compose", TGCLOUD_BIND_IP="127.0.0.1")
        (root / ".env").write_text("".join(key + "=" + value + "\n" for key, value in config.items()), encoding="utf-8")
        (root / runtime.MARKER).write_text(json.dumps({"format": 2, "project": packaging.PROJECT_ID, "edition": edition,
                                                     "state": "ready", "initialized_version": VERSION}), encoding="utf-8")
        for child in ("data", "downloads", "logs", "config", "clouddrive/config", "clouddrive/mounts"):
            (root / child).mkdir(parents=True, exist_ok=True)
        gateway_image = next(line.split(": ", 1)[1] for line in
            (SOURCE / "packaging/compose" / ("docker-compose." + edition + ".yml")).read_text().splitlines()
            if line.startswith("    image: "))
        for index, (name, service, reference) in enumerate(((product.storage_container, edition, gateway_image),
                                                      (product.bot_container, product.bot_service, image)), 1):
            gateway = service == edition
            mounts = {"/" + child: str(root / child) for child in ("data", "downloads", "logs", "config")}
            if gateway:
                mounts = {"/Config": str(root / "clouddrive/config"), "/CloudNAS": str(root / "clouddrive/mounts")}
            port = str(product.management_port)
            self.containers[name] = {"id": str(index) * 64, "name": "/" + name, "running": gateway,
                "image": reference, "image_id": "sha256:" + str(index) * 64, "network": "test_default",
                "labels": {"com.tgcloud.install-mode": "compose", packaging.PROJECT_LABEL: packaging.PROJECT_ID, https.COMPOSE + "service": service,
                           https.COMPOSE + "project": "test", https.COMPOSE + "project.working_dir": str(root),
                           https.COMPOSE + "project.config_files": str(root / "docker-compose.yml")},
                "mounts": [{"Type": "bind", "Destination": target, "Source": source} for target, source in mounts.items()],
                "ports": {port + "/tcp": [{"HostIp": "127.0.0.1", "HostPort": port}]} if gateway else {},
                "bindings": {port + "/tcp": [{"HostIp": "127.0.0.1", "HostPort": port}]} if gateway else {}}
        instance = https.ComposeInstance(root, self.session, self.log, source=SOURCE)
        instance.validate()
        return instance, https.ComposeProxyManager(instance, Mock())

    def state(self, value, registry=None):
        (self.proxy / "state").mkdir(parents=True, exist_ok=True)
        (self.proxy / "state/domains.json").write_text(json.dumps(value), encoding="utf-8")
        (self.proxy / "docker-compose.yml").write_bytes(render_compose(
            tuple(sorted(item["domain"] for item in value["domains"].values())), external_auth=value["version"] == 2).encode())
        if registry is not None:
            (self.proxy / "state" / https.REGISTRY).write_text(json.dumps(registry), encoding="utf-8")

    def registry(self, instance, domain="panel.example.com"):
        return {"format": 1, "owner": https.OWNER, "project": packaging.PROJECT_ID,
                "instances": {instance.product.key: {"root": str(instance.root), "domain": domain}}}
