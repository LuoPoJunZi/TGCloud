"""Host-only tests run without Telethon and must never launch a real Docker command."""


import contextlib
import io
import subprocess
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import mock_open, patch

from tests.fixtures import ROOT
from tgcloud.deployment.ops.listening import main


class HostListeningTests(unittest.TestCase):
    def test_help_from_other_directory_without_site_packages(self):
        code = (
            "import sys;sys.path.insert(0,"
            + repr(str(ROOT / "src"))
            + ");from tgcloud.cli.commands import main;main(['listen','--help'])"
        )
        result = subprocess.run(
            [sys.executable, "-I", "-S", "-B", "-c", code],
            cwd=ROOT.parent,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--edition", result.stdout)
        self.assertNotIn("ModuleNotFoundError", result.stderr)

    def test_verified_existing_container_is_only_touched_identity(self):
        for edition, redirected in ((e, r) for e in ("clouddrive2",) for r in (False, True)):
            with self.subTest(edition=edition, redirected=redirected), contextlib.ExitStack() as stack:
                stack.enter_context(
                    patch("tgcloud.deployment.preflight.validate_host")
                )
                checked = stack.enter_context(
                    patch("tgcloud.deployment.architecture.check_docker")
                )
                stack.enter_context(
                    patch("tgcloud.deployment.architecture.check_environment")
                )
                stack.enter_context(
                    patch(
                        "tgcloud.deployment.ops.listening.shutil.which",
                        return_value="/usr/bin/docker",
                    )
                )
                stack.enter_context(
                    patch("tgcloud.deployment.ops.listening.os.isatty", side_effect=lambda fd, redirected=redirected: not redirected or fd == 77)
                )
                opened = mock_open()
                opened.return_value.fileno.return_value = 77
                opened.return_value.__exit__.side_effect = None
                opened.return_value.__exit__.return_value = False
                stack.enter_context(patch("builtins.open", opened))
                stack.enter_context(
                    patch(
                        "tgcloud.deployment.host.edition_lock",
                        return_value=contextlib.nullcontext(),
                    )
                )
                stack.enter_context(
                    patch(
                        "tgcloud.deployment.instances.docker_inventory",
                        return_value=set(),
                    )
                )
                discovery = stack.enter_context(
                    patch(
                        "tgcloud.deployment.instances.discover",
                        return_value=SimpleNamespace(present=True, running=True),
                    )
                )
                runner = stack.enter_context(
                    patch(
                        "tgcloud.deployment.ops.listening.subprocess.run",
                        return_value=SimpleNamespace(returncode=0),
                    )
                )
                self.assertEqual(
                    main(
                        [
                            "login",
                            "--edition",
                            edition,
                            "--install-dir",
                            "/opt/custom-fixture",
                        ]
                    ),
                    0,
                )
                checked.assert_called_once()
                self.assertEqual(
                    discovery.call_args.kwargs["install_dir"], "/opt/custom-fixture"
                )
                args = runner.call_args.args[0]
                self.assertEqual(
                    args,
                    [
                        "/usr/bin/docker",
                        "exec",
                        "-it",
                        f"tgcloud-{edition}-bot",
                        "python",
                        "-B",
                        "-m",
                        "tgcloud.listening.account",
                        "login",
                    ],
                )
                self.assertNotIn("capture_output", runner.call_args.kwargs)
                if redirected:
                    opened.assert_called_once_with("/dev/tty", "r+b", buffering=0)
                    for stream in ("stdin", "stdout", "stderr"):
                        self.assertIs(runner.call_args.kwargs[stream], opened.return_value)
                    opened.return_value.__exit__.assert_called_once()
                else:
                    opened.assert_not_called()

    def test_invalid_or_partial_instance_never_executes_login(self):
        with (
            patch(
                "tgcloud.deployment.preflight.validate_host",
                side_effect=ValueError("unsupported"),
            ),
            patch("tgcloud.deployment.ops.listening.subprocess.run") as run,
            contextlib.redirect_stdout(io.StringIO()),
        ):
            self.assertEqual(main(["login", "--edition", "clouddrive2"]), 1)
            run.assert_not_called()


class ExplicitStableReleaseTests(unittest.TestCase):
    def test_both_installers_pass_exact_stable_tag_without_writes_in_check_mode(self):
        from unittest.mock import Mock

        from tgcloud.cli import vps
        from tgcloud.deployment import architecture, instances, releases
        from tgcloud.deployment.lifecycle import lan as lan_installer
        from tgcloud.deployment.profiles import PRODUCTS
        from tgcloud.deployment.proxy import lan
        for module in (vps, lan_installer):
            for edition in PRODUCTS:
                with self.subTest(mode=module.__name__, edition=edition), contextlib.ExitStack() as stack:
                    product = PRODUCTS[edition]
                    instance = instances.Instance(product, Path(product.install_dir), False)
                    client = Mock(resolve=Mock(return_value=releases.Release("v1.4.9", "b" * 40)),
                                  download=Mock(return_value=ROOT))
                    stack.enter_context(patch.object(instances, "docker_inventory", return_value=set()))
                    stack.enter_context(patch.object(instances, "discover", return_value=instance))
                    stack.enter_context(patch.object(releases, "ReleaseClient", return_value=client))
                    stack.enter_context(patch.object(architecture, "check_docker"))
                    stack.enter_context(patch.object(module, "SafeLog", return_value=lambda _line: None))
                    terminal = stack.enter_context(patch.object(module.terminal, "Terminal"))
                    if module is vps:
                        stack.enter_context(patch.object(vps.preflight, "validate_host"))
                        stack.enter_context(patch.object(vps.install, "make_plan", return_value=Mock()))
                        stack.enter_context(patch.object(vps.terminal, "preview"))
                        execute = stack.enter_context(patch.object(vps.install, "execute_plan"))
                    else:
                        stack.enter_context(patch.object(lan_installer, "validate_environment"))
                        stack.enter_context(patch.object(lan, "load_state", return_value=None))
                        stack.enter_context(patch.object(lan_installer, "make_plan", return_value=Mock()))
                        stack.enter_context(patch.object(lan_installer, "preview"))
                        execute = stack.enter_context(patch.object(lan_installer, "execute"))
                    self.assertEqual(module.main(["--edition", edition, "--source-dir", str(ROOT), "--check"]), 0)
                    client.resolve.assert_not_called()
                    client.download.assert_not_called()
                    terminal.assert_not_called()
                    execute.assert_not_called()

    def test_explicit_nonstable_release_remains_rejected_before_network(self):
        from tgcloud.deployment.releases import ReleaseClient
        client = ReleaseClient()
        with patch.object(client, "json") as network:
            for tag in ("main", "v1.5.0-rc.1", "v1.4.9;echo bad", "v01.4.9"):
                with self.subTest(tag=tag), self.assertRaises(ValueError):
                    client.resolve(tag)
            network.assert_not_called()
