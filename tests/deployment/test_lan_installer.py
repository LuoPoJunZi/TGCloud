"""Offline LAN boundaries. Never contacts a NAS, VPS, Docker or cloud account."""

from __future__ import annotations

import ast
import io
import json
import os
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import ExitStack, nullcontext, redirect_stdout
from dataclasses import replace
from pathlib import Path
from unittest.mock import MagicMock, Mock, patch

import tgcloud.cli.terminal as tgdep_terminal
import tgcloud.deployment.instances as tgdep_instances
import tgcloud.deployment.lifecycle.install as tgdep_install
import tgcloud.deployment.lifecycle.upgrade as tgdep_upgrade
import tgcloud.deployment.payload as tgdep_payload
import tgcloud.deployment.preflight as tgdep_preflight
import tgcloud.deployment.releases as tgdep_releases
from tests.fixtures import ROOT
from tests.fixtures.shell import BASH, bash_path
from tgcloud.cli import commands, management
from tgcloud.cli.presentation import terminal_text
from tgcloud.deployment import local_source, runtime
from tgcloud.deployment.host import SafeLog
from tgcloud.deployment.lifecycle import lan as lan_installer
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.deployment.proxy import lan
from tgcloud.deployment.resources import GIB, VpsResources

IP = "192.168.26.5"
RELEASE = local_source.LocalSource(ROOT, PRODUCTS["clouddrive2"].app_version, "a" * 64)


def interface(address=IP, *, name="eth0", flags=None, prefix=24):
    return {"ifname": name, "flags": flags if flags is not None else ["UP"],
            "addr_info": [{"family": "inet", "local": address, "prefixlen": prefix}]}


def instance(product, *, present=True, version=None):
    return tgdep_instances.Instance(product, Path(product.install_dir), present,
                        version or (product.app_version if present else ""), present,
                        "gateway@sha256:" + "b" * 64 if present else "",
                        {"BOT_TOKEN": "FAKE_OFFLINE_SECRET", "TGCLOUD_STORAGE_BACKEND": product.key} if present else {},
                        b"BOT_TOKEN=FAKE_OFFLINE_SECRET\n" if present else b"", "a" * 64 if present else "")


def inspected(product, *, running=True):
    directory = lan.LAN_ROOT / product.key
    return {"image": lan.NGINX_IMAGE, "running": running, "network": "host", "readonly": True,
            "privileged": False, "user": "101:101", "entrypoint": ["nginx"], "command": ["-g", "daemon off;"],
            "cap_add": None, "cap_drop": ["ALL"], "security": ["no-new-privileges:true"],
            "tmpfs": {"/tmp": "rw,noexec,nosuid,mode=1777,size=64m"},
            "labels": {"com.tgcloud.managed": "true", "com.tgcloud.role": "lan-management",
                       "com.tgcloud.edition": product.key, "com.docker.compose.project": lan.container_name(product),
                       "com.docker.compose.service": "lan", "com.docker.compose.project.working_dir": str(directory),
                       "com.docker.compose.project.config_files": str(directory / "compose.json")},
            "mounts": [{"Type": "bind", "Source": str(directory / "nginx.conf"),
                        "Destination": "/etc/nginx/nginx.conf", "RW": False}]}


class AddressEnvironmentTests(unittest.TestCase):
    def test_explicit_rfc1918_only_and_no_commands_urls_or_implicit_guess(self):
        for value in (IP, "10.1.2.3", "172.16.0.5", "172.31.255.254"):
            self.assertEqual(lan.validate_ip(value), value)
        for value in ("127.0.0.1", "0.0.0.0", "8.8.8.8", "172.15.0.5", "172.32.0.5", "100.64.0.5",
                      "169.254.1.2", "192.168.026.5", "192.168.26.5:19798", "http://" + IP, "::1",
                      "files.example.test", "" , " " + IP, IP + ";id", None, 3232235525):
            with self.subTest(value=value), self.assertRaises(ValueError):
                lan.validate_ip(value)

    def test_manual_address_must_be_on_enabled_host_interface(self):
        session = Mock()
        session.run.return_value = (0, json.dumps([interface("10.0.0.2"), interface()]))
        self.assertEqual(lan.validate_local_ip(IP, session), IP)
        for item in (interface(name="docker0"), interface(name="br-a1"), interface(name="veth123"),
                     interface(flags=[]), interface("192.168.26.0"), interface("192.168.26.255")):
            session.run.return_value = (0, json.dumps([item]))
            with self.subTest(interface=item), self.assertRaises(ValueError):
                lan.validate_local_ip(item["addr_info"][0]["local"], session)
        session.run.return_value = (1, "")
        with self.assertRaisesRegex(ValueError, "iproute2"):
            lan.validate_local_ip(IP, session)

    def test_invalid_address_is_rejected_before_reading_interfaces(self):
        session = Mock()
        with self.assertRaises(ValueError):
            lan.validate_local_ip("0.0.0.0", session)
        session.run.assert_not_called()

    def test_environment_accepts_absent_or_default_rootful_docker_without_installing(self):
        for responses in ([(0, ""), (1, "")], [(0, ""), (0, ""), (0, '"unix:///var/run/docker.sock"'), (0, '{"os":"linux","arch":"' + lan_installer.architecture.normalize_architecture(lan_installer.architecture.platform.machine()) + '","version":"28.0.0","security":[]}')]):
            session = Mock()
            session.run.side_effect = responses
            with patch.object(tgdep_preflight, "validate_host") as host, patch.dict(os.environ, {}, clear=True):
                lan_installer.validate_environment(session)
            host.assert_called_once()
            self.assertFalse(any("apt" in call.args[0] for call in session.run.call_args_list))

    def test_remote_daemon_compose_injection_missing_tool_rootless_and_nondefault_context_rejected(self):
        native = lan_installer.architecture.normalize_architecture(lan_installer.architecture.platform.machine())
        for name in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CERT_PATH", "COMPOSE_FILE", "COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES"):
            session = Mock()
            with self.subTest(variable=name), patch.object(tgdep_preflight, "validate_host"), patch.dict(os.environ, {name: "external"}, clear=True), self.assertRaises(ValueError):
                lan_installer.validate_environment(session)
            session.run.assert_not_called()
        for responses in ([(1, "")], [(0, ""), (0, ""), (0, '"tcp://external:2375"')],
                          [(0, ""), (0, ""), (0, '"unix:///var/run/docker.sock"'), (0, json.dumps({"os": "linux", "arch": native, "version": "28.0.0", "security": ["name=rootless"]}))],
                          [(0, ""), (0, ""), (0, '"unix:///var/run/docker.sock"'), (0, json.dumps({"os": "linux", "arch": native, "version": "27.5.1", "security": []}))]):
            session = Mock()
            session.run.side_effect = responses
            with self.subTest(responses=responses), patch.object(tgdep_preflight, "validate_host"), patch.dict(os.environ, {}, clear=True), self.assertRaises(ValueError):
                lan_installer.validate_environment(session)


class StateTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.temp = self.stack.enter_context(tempfile.TemporaryDirectory())
        self.root = Path(self.temp).resolve()
        self.stack.enter_context(patch.object(lan, "LAN_ROOT", self.root / "lan"))
        self.stack.enter_context(patch.object(lan, "HTTPS_STATE", self.root / "domains.json"))
        # Linux CI is not root. Simulate only fixture ownership, retaining real
        # permission bits, path resolution and file IO. Production requires root.
        if os.name == "posix" and os.getuid() != 0:
            real_stat = Path.stat

            def root_fixture_stat(path, *args, **kwargs):
                result = real_stat(path, *args, **kwargs)
                if path.is_relative_to(self.root):
                    fields = list(result)
                    fields[4] = 0
                    return os.stat_result(fields)
                return result

            self.stack.enter_context(patch.object(Path, "stat", root_fixture_stat))

    def test_state_is_separate_canonical_and_round_trips_both_editions(self):
        for product in PRODUCTS.values():
            with self.subTest(edition=product.key):
                self.assertIsNone(lan.load_state(product))
                lan.save_state(product, IP)
                self.assertEqual(lan.load_state(product), lan.state_for(product, IP))
                files = lan.render_files(product, IP)
                self.assertEqual(set(files), lan.FILES)
                for name, content in files.items():
                    self.assertEqual((lan.LAN_ROOT / product.key / name).read_text(), content)
                lan.save_state(product, IP)
                with self.assertRaisesRegex(ValueError, "切换"):
                    lan.save_state(product, "192.168.26.6")
        self.assertEqual({path.name for path in lan.LAN_ROOT.iterdir()}, set(PRODUCTS))

    def test_missing_extra_modified_oversize_or_boolean_schema_fail_without_overwrite(self):
        product = PRODUCTS["clouddrive2"]
        for corruption in ("extra", "missing", "config", "large", "schema"):
            with self.subTest(corruption=corruption):
                lan.save_state(product, IP)
                directory = lan.LAN_ROOT / product.key
                if corruption == "extra":
                    (directory / "custom.txt").write_text("user file")
                elif corruption == "missing":
                    (directory / "compose.json").unlink()
                elif corruption == "config":
                    (directory / "nginx.conf").write_text("user nginx config")
                elif corruption == "large":
                    (directory / "nginx.conf").write_text("a" * (128 * 1024 + 1))
                else:
                    value = lan.state_for(product, IP)
                    value["schema"] = True
                    (directory / "state.json").write_text(json.dumps(value))
                before = {path.name: path.read_bytes() for path in directory.iterdir()}
                with self.assertRaises(ValueError):
                    lan.load_state(product)
                with self.assertRaises(ValueError):
                    lan.save_state(product, IP)
                self.assertEqual(before, {path.name: path.read_bytes() for path in directory.iterdir()})
                shutil.rmtree(directory)  # only this test's private fixture

    def test_other_edition_https_does_not_block_selected_edition_but_selected_does(self):
        self.root.joinpath("domains.json").write_text(json.dumps({"domains": {"unsupported": {"domain": "files.example.test"}}}))
        self.root.joinpath("domains.json").chmod(0o600)
        lan.reject_https_route(PRODUCTS["clouddrive2"])
        self.root.joinpath("domains.json").write_text(json.dumps({"domains": {"clouddrive2": {"domain": "files.example.test"}}}))
        with self.assertRaisesRegex(ValueError, "HTTPS"):
            lan.reject_https_route(PRODUCTS["clouddrive2"])

    def test_unparseable_https_state_fails_closed(self):
        for content in ('{}', '[]', '{"domains": []}', 'bad json'):
            with self.subTest(content=content):
                lan.HTTPS_STATE.write_text(content)
                lan.HTTPS_STATE.chmod(0o600)
                with self.assertRaises(ValueError):
                    lan.reject_https_route(PRODUCTS["clouddrive2"])

    @unittest.skipUnless(os.name == "posix", "POSIX file permissions")
    def test_private_permissions_and_nonroot_ownership_fail_closed(self):
        product = PRODUCTS["clouddrive2"]
        lan.save_state(product, IP)
        directory = lan.LAN_ROOT / product.key
        self.assertEqual(directory.stat().st_mode & 0o777, 0o700)
        self.assertEqual((directory / "state.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual((directory / "nginx.conf").stat().st_mode & 0o777, 0o644)
        directory.chmod(0o755)
        with self.assertRaisesRegex(ValueError, "700"):
            lan.load_state(product)
        directory.chmod(0o700)
        (directory / "nginx.conf").chmod(0o666)
        with self.assertRaisesRegex(ValueError, "不安全"):
            lan.load_state(product)
        fake = Mock()
        fake.resolve.return_value = fake
        fake.is_symlink.return_value = False
        fake.is_dir.return_value = True
        fake.stat.return_value.st_uid = 1000
        fake.stat.return_value.st_mode = 0o700
        with self.assertRaisesRegex(ValueError, "root"):
            lan.private_directory(fake)

    def test_symlink_is_not_followed_or_deleted(self):
        product = PRODUCTS["clouddrive2"]
        lan.save_state(product, IP)
        target = self.root / "user.txt"
        target.write_text("preserve me")
        path = lan.LAN_ROOT / product.key / "nginx.conf"
        path.unlink()
        try:
            path.symlink_to(target)
        except OSError:
            self.skipTest("Creating symlinks requires OS privileges")
        with self.assertRaisesRegex(ValueError, "链接"):
            lan.load_state(product)
        self.assertEqual(target.read_text(), "preserve me")


class GatewayTests(unittest.TestCase):
    def test_composition_keeps_loopback_backend_and_binds_only_explicit_lan_ip(self):
        for product in PRODUCTS.values():
            with self.subTest(edition=product.key):
                files = lan.render_files(product, IP)
                compose = json.loads(files["compose.json"])["services"]["lan"]
                self.assertEqual(compose["network_mode"], "host")
                self.assertNotIn("ports", compose)
                self.assertEqual(compose["user"], "101:101")
                self.assertTrue(compose["read_only"])
                self.assertEqual(compose["cap_drop"], ["ALL"])
                nginx = files["nginx.conf"]
                self.assertIn(f"listen {IP}:{product.management_port};", nginx)
                self.assertIn(f"proxy_pass http://127.0.0.1:{product.management_port};", nginx)
                self.assertIn("location ~* ^/dav(?:/|$) { return 403; }", nginx)
                self.assertIn("access_log off;", nginx)
                for forbidden in ("listen 80", "listen 443", "0.0.0.0", "ssl_certificate", "certbot"):
                    self.assertNotIn(forbidden, "\n".join(files.values()))
        self.assertNotIn("clouddrive2", lan.container_name(PRODUCTS["clouddrive2"]))

    def test_inspection_requires_exact_managed_identity_mounts_and_sandbox(self):
        for product in PRODUCTS.values():
            session = Mock()
            canonical = inspected(product)
            session.run.return_value = (0, json.dumps(canonical))
            self.assertEqual(lan.inspect_lan(product, session, {lan.container_name(product)}), canonical)
            for key, value in (("image", "other/nginx:latest"), ("network", "bridge"), ("readonly", False),
                               ("privileged", True), ("user", "root"), ("cap_add", ["NET_ADMIN"]),
                               ("cap_drop", []), ("security", []), ("tmpfs", {}), ("labels", {}), ("mounts", [])):
                session.run.return_value = (0, json.dumps({**canonical, key: value}))
                with self.subTest(edition=product.key, altered=key), self.assertRaisesRegex(ValueError, "不接管"):
                    lan.inspect_lan(product, session, {lan.container_name(product)})

    def test_absent_proxy_is_not_inspected_and_inspect_error_fails(self):
        product, session = PRODUCTS["clouddrive2"], Mock()
        self.assertIsNone(lan.inspect_lan(product, session, set()))
        session.run.assert_not_called()
        session.run.return_value = (1, "")
        with self.assertRaises(ValueError):
            lan.inspect_lan(product, session, {lan.container_name(product)})

    def test_fixed_port_conflict_does_not_change_port_or_stop_external_container(self):
        with patch.object(lan.socket, "socket") as socket:
            socket.return_value.__enter__.return_value.bind.side_effect = OSError("in use")
            with self.assertRaisesRegex(ValueError, "更换端口"):
                lan.check_port(PRODUCTS["clouddrive2"], IP, False)
            socket.return_value.__enter__.return_value.bind.assert_called_once_with((IP, 19798))
        with patch.object(lan.socket, "socket") as socket:
            lan.check_port(PRODUCTS["clouddrive2"], IP, True)
            socket.assert_not_called()

    def test_backend_published_address_must_remain_loopback(self):
        for product in PRODUCTS.values():
            item, session = instance(product), Mock()
            port = str(product.management_port)
            session.run.side_effect = [(0, json.dumps({port + "/tcp": [{"HostIp": "127.0.0.1", "HostPort": port}]})), (0, "ready")]
            lan.check_backend(item, session, SafeLog(lambda _line: None))
            self.assertIn("manage.sh ready", session.run.call_args.args[0])
            for host in ("0.0.0.0", IP, "::"):
                session.run.side_effect = None
                session.run.return_value = (0, json.dumps({port + "/tcp": [{"HostIp": host, "HostPort": port}]}))
                with self.subTest(host=host), self.assertRaisesRegex(ValueError, "回环"):
                    lan.check_backend(item, session, SafeLog(lambda _line: None))

    def test_nat_only_port_owner_is_checked_before_installation(self):
        for product in PRODUCTS.values():
            session = Mock()
            lan.check_published_ports(product, session, set())
            session.run.assert_not_called()
            session.run.return_value = (0, f"foreign|127.0.0.1:{product.management_port}->{product.management_port}/tcp")
            with self.assertRaisesRegex(ValueError, "其他容器"):
                lan.check_published_ports(product, session, {"foreign"})
            session.run.return_value = (0, f"{product.storage_container}|127.0.0.1:{product.management_port}->{product.management_port}/tcp")
            lan.check_published_ports(product, session, {product.storage_container})
            session.run.return_value = (1, "")
            with self.assertRaisesRegex(ValueError, "无法核对"):
                lan.check_published_ports(product, session, {"foreign"})

    def test_local_management_and_dav_checks_require_our_response_marker(self):
        product = PRODUCTS["clouddrive2"]
        with patch.object(lan, "request_status", side_effect=[(200, product.key), (403, product.key), (403, product.key), (403, product.key)]) as probe:
            lan.check_entry(product, IP)
        self.assertEqual([(call.args[2], call.args[3]) for call in probe.call_args_list],
                         [("/", "GET"), ("/dav", "GET"), ("/dav/", "PROPFIND"), ("/DAV/", "PROPFIND")])
        for response in ((200, ""), (500, product.key), (403, product.key)):
            with patch.object(lan, "request_status", return_value=response), self.assertRaises(ValueError):
                lan.check_entry(product, IP)
        with patch.object(lan, "request_status", side_effect=[(200, product.key), (200, product.key)]), self.assertRaises(ValueError):
            lan.check_entry(product, IP)

    def test_startup_probes_are_bounded_and_never_repeat_writes(self):
        with patch.object(lan, "check_entry", side_effect=[OSError("starting"), None]) as probe, patch.object(lan.time, "sleep") as sleep:
            lan.wait_for_entry(PRODUCTS["clouddrive2"], IP)
            self.assertEqual(probe.call_count, 2)
            sleep.assert_called_once_with(0.5)
        with patch.object(lan, "check_entry", side_effect=ValueError("blocked")), patch.object(lan.time, "monotonic", side_effect=[0, 21]), self.assertRaisesRegex(RuntimeError, "未通过"):
            lan.wait_for_entry(PRODUCTS["clouddrive2"], IP)

    def test_http_probe_does_not_use_environment_proxy_follow_redirects_or_echo_body(self):
        response = MagicMock()
        response.__enter__.return_value = response
        response.code = 200
        response.headers.get.return_value = "unsupported"
        opener = Mock()
        opener.open.return_value = response
        with patch.object(lan.urllib.request, "build_opener", return_value=opener) as build:
            self.assertEqual(lan.request_status(PRODUCTS["clouddrive2"], IP, "/"), (200, "unsupported"))
        self.assertEqual(build.call_args.args[0].proxies, {})
        self.assertIsInstance(build.call_args.args[1], lan.NoRedirect)
        self.assertEqual(opener.open.call_args.args[0].full_url, f"http://{IP}:19798/")
        response.read.assert_not_called()
        self.assertIsNone(lan.NoRedirect().redirect_request(None, None, 302, "", {}, "https://external.test/"))


class PlanExecutionTests(unittest.TestCase):
    def assert_readonly_feedback(self, session, offset=0):
        calls = session.run.call_args_list[offset:]
        self.assertEqual(len(calls), 5)
        self.assertEqual(calls[0].args, ("docker exec " + self.product.bot_container + " python -B -m tgcloud.monitoring.readiness",))
        self.assertEqual(calls[0].kwargs, {"timeout": 20})
        expected = (self.product.bot_container, self.product.storage_container,
                    "tgcloud-proxy-nginx", "tgcloud-proxy-certbot")
        for call, name in zip(calls[1:], expected):
            args = shlex.split(call.args[0])
            self.assertEqual(args[:4], ["docker", "container", "inspect", "--format"])
            self.assertEqual(len(args), 6)
            self.assertEqual(args[-1], name)
            self.assertEqual(call.kwargs, {"timeout": 10})
            self.assertNotIn(".Config.Env", args[4])
            self.assertNotIn(".Config.Cmd", args[4])

    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.messages = []
        self.log = SafeLog(self.messages.append)
        self.product = PRODUCTS["clouddrive2"]
        self.instance = instance(self.product)
        self.patches = {}
        self.stack.enter_context(patch.object(lan_installer, "validate_environment"))
        self.stack.enter_context(patch.object(lan_installer.architecture, "ensure_image"))
        self.stack.enter_context(patch.object(lan_installer.architecture, "check_container"))
        mocks = {"load_state": lan.state_for(self.product, IP), "reject_https_route": None,
                 "validate_local_ip": IP, "inspect_lan": None, "check_port": None,
                 "check_backend": None, "check_entry": None, "wait_for_entry": None, "save_state": None}
        for name, value in mocks.items():
            self.patches[name] = self.stack.enter_context(patch.object(lan, name, return_value=value))
        targets = {"assert_unmodified": tgdep_upgrade, "payload_sources": tgdep_payload,
                   "check_foreign_gateway": tgdep_instances, "resource_check": tgdep_preflight,
                   "show_credentials": tgdep_terminal, "verify_destination": tgdep_install}
        for name, target in targets.items():
            self.patches[name] = self.stack.enter_context(patch.object(target, name))
        self.patches["docker_inventory"] = self.stack.enter_context(patch.object(tgdep_instances, "docker_inventory", return_value=set()))
        self.patches["gateway_image"] = self.stack.enter_context(patch.object(tgdep_payload, "gateway_image", return_value=self.instance.gateway_image))
        self.patches["choose_storage"] = self.stack.enter_context(patch.object(tgdep_preflight, "choose_storage", return_value=("8", "8")))

    def plan(self, **kwargs):
        return lan_installer.make_plan(self.instance, RELEASE, ROOT, ROOT, Mock(), self.log, **kwargs)

    def test_existing_nonlan_instance_is_never_converted(self):
        self.patches["load_state"].return_value = None
        with self.assertRaisesRegex(ValueError, "不转换"):
            self.plan()
        self.patches["assert_unmodified"].assert_not_called()

    def test_https_legacy_partial_or_modified_instance_is_not_auto_fixed(self):
        self.patches["reject_https_route"].side_effect = ValueError("已有 HTTPS")
        with self.assertRaisesRegex(ValueError, "HTTPS"):
            self.plan()
        self.patches["reject_https_route"].side_effect = None
        self.patches["assert_unmodified"].side_effect = ValueError("手改程序")
        with self.assertRaisesRegex(ValueError, "手改"):
            self.plan()
        self.patches["save_state"].assert_not_called()

    def test_upgrade_preserves_raw_config_and_saved_address_without_prompt(self):
        self.instance.version = "0.0.0"
        ui = Mock()
        plan = self.plan(ui=ui)
        self.assertEqual(plan.action, "upgrade")
        self.assertEqual(plan.address, IP)
        self.assertEqual(plan.config, self.instance.config)
        self.patches["choose_storage"].assert_not_called()
        ui.ask.assert_not_called()
        self.patches["resource_check"].assert_called_once()
        with self.assertRaisesRegex(ValueError, "切换"):
            self.plan(address="192.168.26.6")
        self.patches["save_state"].assert_not_called()

    def test_gateway_digest_migration_is_not_silently_applied(self):
        self.instance.version = "0.0.0"
        self.patches["gateway_image"].return_value = "different@sha256:" + "c" * 64
        with self.assertRaisesRegex(ValueError, "人工迁移"):
            self.plan()

    def test_final_capacity_loss_does_not_save_lan_state_or_deploy(self):
        self.instance.version = "0.0.0"
        plan = self.plan()
        self.patches["resource_check"].side_effect = ValueError("资源不足")
        with patch.object(lan_installer, "edition_lock", return_value=nullcontext()), patch.object(tgdep_instances, "discover", return_value=self.instance), patch.object(tgdep_install, "apply_base") as apply, self.assertRaisesRegex(ValueError, "资源不足"):
            lan_installer.execute(plan, RELEASE, ROOT, ROOT, Mock(), self.log, Mock())
        apply.assert_not_called()
        self.patches["save_state"].assert_not_called()

    def test_new_install_needs_explicit_ip_and_collects_no_secrets_on_check(self):
        self.instance = instance(self.product, present=False)
        self.patches["load_state"].return_value = None
        with self.assertRaisesRegex(ValueError, "--lan-ip"):
            self.plan()
        with patch.object(tgdep_terminal, "collect_config") as collect:
            plan = self.plan(address=IP)
        collect.assert_not_called()
        self.assertEqual(plan.action, "install")
        self.assertEqual(plan.config, {})
        self.patches["resource_check"].assert_called_once()
        self.patches["check_port"].assert_any_call(self.product, "127.0.0.1", False)
        self.patches["save_state"].assert_not_called()

    def test_manual_ip_preflight_precedes_credentials_and_default_install_confirmation(self):
        self.instance = instance(self.product, present=False)
        self.patches["load_state"].return_value = None
        ui = Mock()
        ui.ask.return_value = IP
        with patch.object(tgdep_terminal, "collect_config", return_value={"BOT_TOKEN": "FAKE_OFFLINE_SECRET"}) as collect:
            plan = self.plan(ui=ui)
        ui.ask.assert_called_once()
        collect.assert_called_once()
        self.assertEqual(collect.call_args.kwargs, {"storage": ("8", "8")})
        self.assertEqual(plan.address, IP)
        self.patches["validate_local_ip"].side_effect = ValueError("not local")
        with patch.object(tgdep_terminal, "collect_config") as collect, self.assertRaises(ValueError):
            self.plan(ui=ui)
        collect.assert_not_called()

    def test_orphan_proxy_is_not_adopted_for_new_install(self):
        self.instance = instance(self.product, present=False)
        self.patches["load_state"].return_value = None
        self.patches["inspect_lan"].return_value = inspected(self.product)
        with self.assertRaisesRegex(ValueError, "不接管"):
            self.plan(address=IP)

    def test_current_newer_and_broken_entry_plans_do_not_rebuild_before_confirmation(self):
        self.patches["inspect_lan"].return_value = inspected(self.product)
        self.assertTrue(self.plan().entry_healthy)
        self.instance.version = f"{int(self.product.app_version.split('.')[0]) + 1}.0.0"
        self.assertEqual(self.plan().action, "newer")
        self.patches["check_entry"].side_effect = OSError("refused")
        self.assertFalse(self.plan().entry_healthy)
        self.patches["save_state"].assert_not_called()

    def execute_fixture(self, plan, *, current=None, session=None):
        session = session or Mock(run=Mock(return_value=(0, "")))
        current = current or self.instance
        self.patches["load_state"].return_value = plan.saved
        self.patches["inspect_lan"].side_effect = [None, inspected(self.product)]
        self.stack.enter_context(patch.object(lan_installer, "edition_lock", return_value=nullcontext()))
        self.discover = self.stack.enter_context(patch.object(tgdep_instances, "discover", return_value=current))
        apply = self.stack.enter_context(patch.object(tgdep_install, "apply_base"))
        return session, apply

    def test_execute_resumes_only_owned_entry_after_base_and_never_modifies_https(self):
        plan = self.plan()
        session, apply = self.execute_fixture(plan)
        lan_installer.execute(plan, RELEASE, ROOT, ROOT, session, self.log, Mock())
        apply.assert_called_once_with(plan, RELEASE, ROOT, session, self.log)
        self.assertTrue(all(call.kwargs.get("install_dir") == self.product.install_dir for call in self.discover.call_args_list))
        self.patches["save_state"].assert_called_once_with(self.product, IP)
        self.patches["wait_for_entry"].assert_called_once()
        commands = [call.args[0] for call in session.run.call_args_list]
        self.assertEqual(len(commands), 9)
        self.assertIn("run --rm --no-deps lan -t", commands[2])
        self.assertTrue(all("compose.json" in command for command in commands[:4]))
        self.assert_readonly_feedback(session, offset=4)
        self.assertFalse(any("certbot" in command or "tgcloud-proxy" in command for command in commands[:4]))
        self.patches["verify_destination"].assert_not_called()
        self.assertIn("HTTPS=NOT_APPLICABLE", "\n".join(self.messages))

    def test_confirmed_config_or_state_race_stops_before_writing(self):
        for kind in ("config", "state"):
            with self.subTest(kind=kind):
                plan = self.plan()
                current = instance(self.product)
                if kind == "config":
                    current.raw_config = b"changed"
                session, apply = self.execute_fixture(plan, current=current)
                if kind == "state":
                    self.patches["load_state"].return_value = {"changed": True}
                with self.assertRaisesRegex(ValueError, "变化"):
                    lan_installer.execute(plan, RELEASE, ROOT, ROOT, session, self.log, Mock())
                apply.assert_not_called()
                self.patches["save_state"].assert_not_called()

    def test_proxy_failure_keeps_mode_record_and_never_claims_success(self):
        plan = self.plan()
        session, apply = self.execute_fixture(plan, session=Mock(run=Mock(return_value=(1, "failed"))))
        with self.assertRaisesRegex(RuntimeError, "保留"):
            lan_installer.execute(plan, RELEASE, ROOT, ROOT, session, self.log, Mock())
        apply.assert_called_once()
        self.patches["save_state"].assert_called_once()
        self.assertNotIn("TGCLOUD_LAN_BASE=OK", "\n".join(self.messages))
        self.patches["verify_destination"].assert_not_called()

    def test_healthy_entry_revalidated_without_restart_and_verify_requires_explicit_flow(self):
        plan = self.plan()
        plan.entry_healthy = True
        session, apply = self.execute_fixture(plan)
        self.patches["inspect_lan"].side_effect = None
        self.patches["inspect_lan"].return_value = inspected(self.product)
        lan_installer.execute(plan, RELEASE, ROOT, ROOT, session, self.log, Mock(), verify=True)
        self.assert_readonly_feedback(session)
        self.patches["show_credentials"].assert_not_called()
        self.patches["verify_destination"].assert_called_once()
        apply.assert_called_once()

    def test_management_url_is_shown_before_credentials_and_verify_for_both_editions(self):
        for product in PRODUCTS.values():
            for action in ("install", "current"):
                with self.subTest(edition=product.key, action=action):
                    self.product = product
                    before = instance(product, present=action != "install")
                    after = instance(product)
                    saved = lan.state_for(product, IP) if before.present else None
                    plan = lan_installer.Plan(before, action, IP, saved, dict(after.config))
                    session, _apply = self.execute_fixture(plan, current=before)
                    self.discover.side_effect = [before, after]
                    self.messages.clear()
                    url_line = f"TGCloud 内网管理页：http://{IP}:{product.management_port}"

                    def show_credentials(*_args, expected_url=url_line):
                        self.assertIn(expected_url, self.messages)
                        self.messages.append("OFFLINE_CREDENTIALS_PROMPT")

                    def verify(*_args, expected_url=url_line):
                        self.assertIn(expected_url, self.messages)
                        self.assertIn("OFFLINE_CREDENTIALS_PROMPT", self.messages)
                        self.messages.append("OFFLINE_VERIFY_PROMPT")

                    self.patches["show_credentials"].side_effect = show_credentials
                    self.patches["verify_destination"].side_effect = verify
                    lan_installer.execute(plan, RELEASE, ROOT, ROOT, session, self.log, Mock(), verify=action == "current")
                    self.assertLess(self.messages.index(url_line), self.messages.index("OFFLINE_VERIFY_PROMPT"))
                    self.assertIn("完成后再确认写入验收", "\n".join(self.messages))
                    self.assertNotIn("FAKE_OFFLINE_SECRET", "\n".join(self.messages))


class FreshLanInstallFlowTests(unittest.TestCase):
    def run_installation(self, directory):
        product = replace(PRODUCTS["clouddrive2"], install_dir=str(directory))
        fresh = instance(product, present=False)
        deployed = fresh
        lan_state = None
        containers = set()
        events = []
        output = io.StringIO()
        token = "123456789:FAKE_LAN_INSTALL_TEST_TOKEN_123"
        password = "FAKE_LAN_INSTALL_PASSWORD"
        answers = iter(("y", IP, "", "12345678", "a" * 32, token, "123456789", "", password,
                        "Telegram/Downloads", "y", "y", "7", "0", "0"))
        ui = Mock(spec=tgdep_terminal.Terminal)
        resources = VpsResources(
            "amd64", 2, 4 * GIB, 3 * GIB, 0, "ext4", 200 * GIB, 180 * GIB, 100000, 90000,
            False, 0, 0, 0, True, False, 0, 0, 0, 0, True,
        )

        def ask(prompt, *, default="", secret=False, validate=str):
            value = next(answers)
            if prompt.startswith("确认上述内网 HTTP 计划？"):
                events.append("confirmed")
            if default and not secret and "回车默认" not in prompt:
                prompt += f"（回车默认 {default}）"
            print(terminal_text(prompt) + ": " + ("〔输入不回显〕" if secret else value or "〔回车〕"))
            return validate(value or default)

        def save_state(selected, address):
            nonlocal lan_state
            self.assertEqual(events, ["confirmed"])
            lan_state = lan.state_for(selected, address)

        def run(command, *, timeout, stream=None):
            nonlocal deployed
            if command.startswith("INSTALL_DIR="):
                self.assertEqual(events, ["confirmed"])
                self.assertEqual(lan_state["bind_ip"], IP)
                target, interpreter, hook, bundle, candidate = shlex.split(command)
                bundle = Path(bundle)
                self.assertEqual((target, interpreter, Path(hook), timeout),
                                 ("INSTALL_DIR=" + str(directory), "bash", bundle / "remote_install.sh", 2400))
                self.assertEqual(Path(hook).read_bytes(),
                                 (ROOT / "packaging/hooks/clouddrive2/remote_install.sh").read_bytes())
                self.assertEqual((bundle / "docker-compose.yml").read_bytes(),
                                 (ROOT / "packaging/compose/docker-compose.clouddrive2.yml").read_bytes())
                marker = local_source.verify_installed(bundle, expected_version=product.app_version, allow_pending=True)
                raw = Path(candidate).read_bytes()
                config = tgdep_install.configuration.parse_config(raw)
                self.assertEqual(config["BOT_TOKEN_B64"], tgdep_install.configuration.encoded(token))
                self.assertEqual(config["LOCAL_TEMP_BUDGET_GB"], "80")
                self.assertEqual(config["MIN_FREE_DISK_GB"], "40")
                shutil.copytree(bundle, directory)
                (directory / ".env").write_bytes(raw)
                containers.update((product.bot_container, product.storage_container))
                deployed = replace(fresh, present=True, running=True, version=product.app_version,
                                   config=config, raw_config=raw, source_digest=marker["source_sha256"])
                events.append("base")
                result = ("TGCLOUD_STAGE=GATEWAY\nTGCLOUD_STAGE=BOT\nTGCLOUD_STAGE=HEALTH\n"
                          "TGCLOUD_CODE=OK\nTGCLOUD_BOT_HEALTH=healthy\nTGCLOUD_RESULT=SUCCESS\n"
                          f"TGCLOUD_VERSION={product.app_version}\n")
                stream(result)
                return 0, result
            if command == "command -v docker >/dev/null 2>&1":
                return (0 if containers else 1), ""
            if command.endswith(" bash ./manage.sh ready"):
                self.assertIn(product.bot_container, containers)
                events.append("ready")
                return 0, ""
            if command.endswith(" bash ./manage.sh verify"):
                self.assertIn(lan.container_name(product), containers)
                events.append("webdav")
                stream("TGCLOUD_DESTINATION=OK")
                return 0, "TGCLOUD_DESTINATION=OK\n"
            if "PortBindings" in command:
                return 0, '{"19798/tcp":[{"HostIp":"127.0.0.1","HostPort":"19798"}]}'
            if command.startswith("docker container inspect "):
                name = shlex.split(command)[-1]
                if "LogConfig" in command and name not in containers:
                    return 1, ""
                self.assertIn(name, containers)
                if "LogConfig" in command:
                    service = {product.bot_container: product.bot_service, product.storage_container: product.key}[name]
                    return 0, json.dumps({"driver": "json-file", "size": "10m", "files": "3",
                                          "directory": str(directory), "service": service})
                if "ReadonlyRootfs" in command:
                    return 0, json.dumps(inspected(product))
                return 0, json.dumps({"image": "sha256:" + "b" * 64, "running": True, "health": "healthy"})
            if command.startswith("docker inspect ") and ".State.Status" in command:
                self.assertIn(shlex.split(command)[-1], containers)
                return 0, "running healthy"
            if command == "docker exec " + product.bot_container + " python -B -m tgcloud.monitoring.readiness":
                self.assertIn(product.bot_container, containers)
                return 0, json.dumps({"schema": 1, "version": deployed.version, "ready": True,
                                      "checks": dict.fromkeys(tgdep_install.readiness.LABELS, "ok")})
            for arguments in ("config --quiet", "pull lan", "run --rm --no-deps lan -t", "up -d lan"):
                if command == lan.compose_command(product, arguments):
                    events.append(arguments)
                    if arguments == "up -d lan":
                        containers.add(lan.container_name(product))
                    return 0, ""
            raise AssertionError("unexpected install command: " + command)

        session = Mock(run=Mock(side_effect=run))
        ui.ask.side_effect = ask
        confirm = tgdep_terminal.Terminal.confirm
        ui.confirm.side_effect = lambda prompt: confirm(ui, prompt)
        ui.private.side_effect = lambda label, value: print(label + "：" + ("〔已遮盖〕" if value == password else value))

        def request_status(selected, address, path, _method):
            self.assertEqual((selected, address), (product, IP))
            self.assertIn(lan.container_name(product), containers)
            return (200 if path == "/" else 403), product.key

        host = tgdep_install.architecture.NativeHost("amd64", "x86_64", "Ubuntu 24.04 LTS")

        def describe(_session, *, log=None):
            if log is not None:
                host.describe(log)

        with ExitStack() as stack:
            stack.enter_context(redirect_stdout(output))
            stack.enter_context(patch.dict(PRODUCTS, {product.key: product}))
            for target, names in (
                (management.preflight, ("validate_host",)),
                (management.uninstall, ("validate_environment",)),
                (tgdep_install.architecture, ("check_docker", "check_image", "ensure_image")),
                (tgdep_instances, ("check_foreign_gateway",)),
                (lan, ("check_port",)),
                (runtime, ("check_destination",)),
                (commands.signal, ("signal",)),
            ):
                for name in names:
                    stack.enter_context(patch.object(target, name))
            for target, values in (
                (management, {"LocalSession": session}),
                (tgdep_terminal, {"Terminal": ui}),
                (tgdep_preflight, {"probe_resources": resources}),
                (runtime, {"lifecycle_lock": nullcontext(), "install_release": {
                    "version": product.app_version, "matches": True, "state": "ok",
                }}),
                (lan_installer, {"LocalSession": session, "edition_lock": nullcontext()}),
                (lan, {"validate_local_ip": IP}),
            ):
                for name, value in values.items():
                    stack.enter_context(patch.object(target, name, return_value=value))
            for target, name, effect in (
                (tgdep_instances, "discover", lambda *_args, **_kwargs: deployed),
                (tgdep_instances, "docker_inventory", lambda _session: set(containers)),
                (lan, "load_state", lambda _product: lan_state),
                (lan, "save_state", save_state),
                (lan_installer, "validate_environment", describe),
                (lan, "request_status", request_status),
            ):
                stack.enter_context(patch.object(target, name, side_effect=effect))
            stack.enter_context(patch.object(lan, "HTTPS_STATE", directory.parent / "unused-https.json"))
            with patch("tgcloud.deployment.proxy.external_entry.apply_payload_network") as external_network:
                self.assertEqual(commands.main(["lan"]), 0, output.getvalue())
                external_network.assert_not_called()
            self.assertEqual(commands.main([]), 0, output.getvalue())

        self.assertEqual(events, ["confirmed", "base", "ready", "config --quiet", "pull lan",
                                  "run --rm --no-deps lan -t", "up -d lan", "webdav"])
        self.assertEqual(local_source.verify_installed(directory)["state"], "ready")
        return output.getvalue()

    def test_confirmed_fresh_lan_install_reaches_healthy_bot_and_management_menu(self):
        with tempfile.TemporaryDirectory() as temporary:
            text = self.run_installation(Path(temporary).resolve() / "tgcloud")
        for expected in (
            "安装状态：未安装",
            "构建并启动 TGCloud Bot",
            "Bot 基础健康：通过",
            "TGCloud 内网管理页：http://" + IP + ":19798",
            "本机内网入口：通过",
            "WebDAV 写入与大小复验：通过",
            "安装状态：已安装\nBot 状态：运行中",
            "16. 监听转存管理",
            "Bot 健康：通过（容器检查）",
            "入口检查：本机通过",
        ):
            self.assertIn(expected, text)
        self.assertNotIn("本次操作未完成", text)
        self.assertNotIn("FAKE_LAN_INSTALL_", text)


class MainBootstrapTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.product = PRODUCTS["clouddrive2"]
        self.instance = instance(self.product)
        self.stack.enter_context(patch.object(local_source, "snapshot", side_effect=lambda path: nullcontext(RELEASE)))
        self.plan = lan_installer.Plan(self.instance, "current", IP, lan.state_for(self.product, IP), dict(self.instance.config), True, True)
        self.messages = []
        self.log = SafeLog(self.messages.append)
        self.ui = Mock(confirm=Mock(return_value=False))
        self.stack.enter_context(patch.object(lan_installer, "validate_environment"))
        self.stack.enter_context(patch.object(tgdep_instances, "docker_inventory", return_value=set()))
        self.stack.enter_context(patch.object(tgdep_instances, "discover", return_value=self.instance))
        self.stack.enter_context(patch.object(lan, "load_state", return_value=self.plan.saved))
        self.stack.enter_context(patch.object(lan_installer, "SafeLog", return_value=self.log))
        client = Mock(resolve=Mock(return_value=RELEASE), download=Mock(return_value=ROOT))
        self.client = self.stack.enter_context(patch.object(tgdep_releases, "ReleaseClient", return_value=client))
        self.terminal = self.stack.enter_context(patch.object(tgdep_terminal, "Terminal", return_value=self.ui))
        self.stack.enter_context(patch.object(lan_installer, "make_plan", return_value=self.plan))
        self.execute = self.stack.enter_context(patch.object(lan_installer, "execute"))

    def test_check_does_not_open_terminal_write_config_or_apply(self):
        self.assertEqual(lan_installer.main(["--edition", "clouddrive2", "--check"]), 0)
        self.terminal.assert_not_called()
        self.execute.assert_not_called()
        self.assertIn("TGCLOUD_LAN_CHECK=OK", "\n".join(self.messages))

    def test_check_fails_if_existing_entry_is_unhealthy_instead_of_claiming_pass(self):
        self.plan.entry_healthy = False
        self.assertEqual(lan_installer.main(["--edition", "clouddrive2", "--check"]), 1)
        self.execute.assert_not_called()
        self.assertNotIn("TGCLOUD_LAN_CHECK=OK", "\n".join(self.messages))

    def test_check_rejects_verify_or_credentials_before_environment(self):
        for extra in ("--verify", "--show-credentials"):
            with self.subTest(extra=extra):
                self.assertEqual(lan_installer.main(["--edition", "clouddrive2", "--check", extra]), 1)
        self.execute.assert_not_called()
        self.client.assert_not_called()
        self.terminal.assert_not_called()

    def test_current_and_newer_do_not_install_or_restart(self):
        for action in ("current", "newer"):
            self.plan.action = action
            with self.subTest(action=action):
                self.assertEqual(lan_installer.main(["--edition", "clouddrive2"]), 0)
        self.execute.assert_not_called()
        self.ui.confirm.assert_not_called()

    def test_default_confirmation_declines_and_closes_terminal(self):
        self.plan.action = "upgrade"
        self.assertEqual(lan_installer.main(["--edition", "clouddrive2"]), 0)
        self.execute.assert_not_called()
        self.ui.confirm.assert_called_once()
        self.ui.close.assert_called_once()
        self.assertIn("CANCELLED", "\n".join(self.messages))

    def test_confirmation_yes_runs_only_the_selected_edition(self):
        self.plan.action = "upgrade"
        self.ui.confirm.return_value = True
        self.assertEqual(lan_installer.main(["--edition", "clouddrive2"]), 0)
        self.execute.assert_called_once()
        self.assertEqual(self.execute.call_args.args[0].instance.product.key, "clouddrive2")

    def test_credentials_view_never_resolves_release_or_installs(self):
        with patch.object(lan, "reject_https_route"), patch.object(tgdep_terminal, "show_credentials") as show:
            self.assertEqual(lan_installer.main(["--edition", "clouddrive2", "--show-credentials"]), 0)
        show.assert_called_once()
        self.client.assert_not_called()
        self.execute.assert_not_called()

    def test_errors_redact_registered_credentials_and_interrupt_does_not_claim_rollback(self):
        self.log.register(self.instance.config)
        self.plan.action = "upgrade"
        self.ui.confirm.return_value = True
        self.execute.side_effect = RuntimeError("FAKE_OFFLINE_SECRET")
        self.assertEqual(lan_installer.main(["--edition", "clouddrive2"]), 1)
        self.assertNotIn("FAKE_OFFLINE_SECRET", "\n".join(self.messages))
        self.execute.side_effect = KeyboardInterrupt
        self.assertEqual(lan_installer.main(["--edition", "clouddrive2"]), 130)
        self.assertIn("不假定已恢复", "\n".join(self.messages))

    def test_local_lan_entry_is_stdlib_only_and_has_no_download_fallback(self):
        text = (ROOT / "install.sh").read_text(encoding="utf-8").partition("# TGCloud online bootstrap.")[0]
        self.assertIn('exec python3 -B "$script_root/scripts/run_source.py" lan "$@"', text)
        self.assertNotIn("installer_commit", text)
        self.assertNotIn("curl", text)
        ast.parse((ROOT / "scripts/run_source.py").read_text(encoding="utf-8"))

    def test_lan_uses_complete_public_source_snapshot_without_online_resolution(self):
        from tgcloud.deployment import local_source
        with patch.object(tgdep_releases, "ReleaseClient") as online, local_source.snapshot(ROOT) as source:
            names = set(local_source.content(source.root))
            self.assertIn("src/tgcloud/deployment/proxy/lan.py", names)
            self.assertIn("src/tgcloud/deployment/lifecycle/lan.py", names)
            self.assertNotIn(".env", names)
            self.assertFalse(any("unsupported" in name for name in names))
        online.assert_not_called()

    def test_local_entry_rejects_incomplete_source_without_online_fallback(self):
        from tests.fixtures.local_project import check_shell_entry
        content = (ROOT / "install.sh").read_text(encoding="utf-8").partition("# TGCloud online bootstrap.")[0]
        self.assertNotRegex(content, r"https?://|curl|wget|installer_commit")
        check_shell_entry(self, BASH, "lan", ["--check"])

    def test_local_lan_entry_preserves_terminal_input_arguments_and_exit(self):
        from tests.fixtures.local_project import check_shell_entry
        check_shell_entry(self, BASH, "lan", ["--lan-ip", IP, "--check"])

    @unittest.skipUnless(BASH, "Bash unavailable")
    def test_local_lan_entry_from_another_working_directory_forwards_arguments_and_exit(self):
        for code in (0, 7):
            with self.subTest(code=code), tempfile.TemporaryDirectory(prefix="tgcloud lan shell ") as private:
                directory = Path(private).resolve()
                tools = directory / "bin"
                tools.mkdir()
                python = tools / "python3"
                python.write_text('#!/usr/bin/env bash\nif [[ "$1" == -c ]]; then exit 0; fi\nprintf "%s\\n" "$@" > "$FIXTURE_ARGS"\nexit "$FIXTURE_EXIT"\n',
                                  encoding="utf-8", newline="\n")
                python.chmod(0o700)
                arguments = ["--edition", "clouddrive2", "--lan-ip", IP, "--check"]
                command = "PATH=" + shlex.quote(bash_path(tools)) + ':"$PATH" bash ' + shlex.quote(bash_path(ROOT / "install.sh"))
                command += " --lan " + " ".join(map(shlex.quote, arguments))
                output = directory / "arguments"
                result = subprocess.run([BASH, "-c", command], cwd=directory,
                                        env={**os.environ, "FIXTURE_ARGS": bash_path(output), "FIXTURE_EXIT": str(code)},
                                        capture_output=True, text=True, timeout=15, check=False)
                self.assertEqual(result.returncode, code, result.stderr)
                self.assertEqual(output.read_text(encoding="utf-8").splitlines(),
                                 ["-B", bash_path(ROOT / "scripts/run_source.py"), "lan", *arguments])

    def test_lan_entry_imports_only_public_linux_helpers(self):
        tree = ast.parse((ROOT / "src/tgcloud/deployment/proxy/lan.py").read_text(encoding="utf-8"))
        gateway_imports = {node.module or "" for node in tree.body if isinstance(node, ast.ImportFrom)}
        installer_tree = ast.parse((ROOT / "src/tgcloud/deployment/lifecycle/lan.py").read_text(encoding="utf-8"))
        installer_imports = {node.module or "" for node in ast.walk(installer_tree) if isinstance(node, ast.ImportFrom)}
        self.assertIn("tgcloud.deployment.profiles", gateway_imports)
        self.assertIn("tgcloud.deployment.proxy.rendering", gateway_imports)
        self.assertNotIn("tgcloud.deployment.lifecycle", gateway_imports)
        self.assertIn("tgcloud.deployment.proxy", installer_imports)
        self.assertNotIn("tgcloud.cli.vps", gateway_imports | installer_imports)
        self.assertFalse(any(name.startswith(("legacy", "PySide6", "paramiko"))
                             for name in gateway_imports | installer_imports))

    def test_help_and_shell_syntax_do_not_require_qt_root_or_docker(self):
        result = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/run_source.py"), "lan", "--help"], cwd=ROOT,
                                env={**os.environ, "PYTHONIOENCODING": "utf-8"}, capture_output=True, text=True, encoding="utf-8", timeout=15, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--lan-ip", result.stdout)
        bash = shutil.which("bash")
        if not bash and os.name == "nt":
            git = shutil.which("git")
            bash = str(Path(git).parents[1] / "bin/bash.exe") if git else None
        if bash:
            result = subprocess.run([bash, "-n", str(ROOT / "install.sh")], capture_output=True, text=True, timeout=15, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
