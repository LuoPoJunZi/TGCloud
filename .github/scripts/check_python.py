"""Shared local/CI checks; never build, deploy, or install dependencies."""

from __future__ import annotations

import argparse
import os
import subprocess  # nosec B404
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CLI_TESTS = (
    "tests.runtime.test_database_paths",
    "tests.deployment.compose.test_compose",
    "tests.deployment.compose.test_compose_access",
    "tests.deployment.compose.test_compose_https",
    "tests.deployment.compose.test_compose_https_bundle",
    "tests.deployment.compose.test_compose_proxy_dependencies",
    "tests.deployment.compose.test_compose_management",
    "tests.deployment.install.test_architecture",
    "tests.deployment.install.test_vps_installer",
    "tests.deployment.install.test_lan_installer",
    "tests.deployment.install.test_online_installer",
    "tests.deployment.install.test_uninstaller",
    "tests.deployment.proxy.test_proxy_host_compat",
    "tests.repository.test_repository",
    "tests.deployment.install.test_storage_wizard",
    "tests.repository.test_source_release",
    "tests.repository.test_layout_contracts",
    "tests.addons.external_webdav.test_feature",
    "tests.addons.external_webdav.test_http",
    "tests.addons.external_webdav.test_scope",
    "tests.deployment.cli.test_management",
    "tests.deployment.ops.test_family_management",
    "tests.addons.external_webdav.test_users",
    "tests.deployment.proxy.test_external_proxy",
    "tests.deployment.install.test_source_identity",
    "tests.deployment.ops.test_instance_backup",
    "tests.runtime.test_readiness",
    "tests.naming.test_naming_preview",
    "tests.deployment.cli.test_host_bundle",
    "tests.deployment.ops.test_feature_settings",
    "tests.deployment.install.test_install_feedback",
    "tests.deployment.install.test_installer_ui",
    "tests.deployment.cli.test_terminal_layout",
    "tests.deployment.ops.test_container_log_status",
    "tests.bot.test_notifications",
    "tests.deployment.ops.test_diagnostic_report",
    "tests.naming.test_classification",
    "tests.bot.test_member_quotas",
    "tests.runtime.test_progress_metrics",
    "tests.bot.test_listener_observations",
    "tests.deployment.ops.test_restore_rehearsal",
    "tests.deployment.cli.test_management_pages",
    "tests.repository.test_release_preparation",
    "tests.deployment.proxy.test_proxy_rendering",
    "tests.deployment.proxy.test_proxy_certbot_startup",
    "tests.deployment.proxy.test_webdav_entry",
    "tests.deployment.ops.test_listening_host",
    "tests.deployment.proxy.test_webdav_upgrade",
    "tests.deployment.ops.test_listening_login",
)
CLI_PATHS = (
    "src/tgcloud/compose", ".github/scripts/compose_release.py",
    "src/tgcloud/cli", "src/tgcloud/deployment", "src/tgcloud/addons", "src/tgcloud/__init__.py",
    "src/tgcloud/version.py", "src/tgcloud/config.py", "scripts",
    ".github/scripts/check_python.py", ".github/scripts/check_repository.py",
    ".github/scripts/source_release.py",
    "src/tgcloud/domain/naming.py",
    ".github/scripts/maintenance_preflight.py", "src/tgcloud/persistence/rehearsal.py",
    ".github/scripts/prepare_release.py", ".github/scripts/linux_validation.py",
    "src/tgcloud/persistence/backup.py", "src/tgcloud/persistence/bundle.py", "src/tgcloud/persistence/paths.py", "src/tgcloud/monitoring/readiness.py",
    "src/tgcloud/monitoring/progress.py", "src/tgcloud/monitoring/observations.py",
    *(module.replace(".", "/") + ".py" for module in CLI_TESTS if not module.startswith("tests.addons.")),
    "tests/__init__.py",
    "tests/fixtures",
    "tests/addons",
    "tests/integration/test_lan_gateway_docker.py",
)


def source_paths(scope: str) -> tuple[str, ...]:
    if scope == "cli":
        return CLI_PATHS
    return (
        *(path.name for path in sorted(ROOT.glob("*.py"))),
        ".github/scripts", "src", "packaging/compat", "scripts", "tests",
    )


def commands(task: str, scope: str) -> list[list[str]]:
    tasks = ("repository", "test", "lint", "compile") if task == "all" else (task,)
    result: list[list[str]] = []
    for selected in tasks:
        if selected == "repository":
            result.append([sys.executable, "-B", ".github/scripts/check_repository.py"])
        elif selected == "test":
            unittest = [sys.executable, "-B", "-m", "unittest"]
            if scope == "cli":
                result.extend([*unittest, module, "-v"] for module in CLI_TESTS)
            else:
                result.append([*unittest, "discover", "-s", "tests", "-t", ".", "-p", "test*.py", "-v"])
        elif selected == "lint":
            paths = CLI_PATHS if scope == "cli" else (".",)
            result.append([sys.executable, "-m", "ruff", "check", *paths])
        elif selected == "compile":
            # Keep development bytecode out of the payload/PyInstaller inputs.
            result.append([
                sys.executable, "-X", f"pycache_prefix={ROOT / 'build/check-pycache'}",
                "-m", "compileall", "-q", *source_paths(scope),
            ])
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("task", choices=("all", "repository", "test", "lint", "compile"))
    parser.add_argument("--scope", choices=("all", "cli"), default="all")
    args = parser.parse_args(argv)
    for command in commands(args.task, args.scope):
        print("+ " + " ".join(command), flush=True)
        # Commands use the current Python, fixed module names and validated choices;
        # no user-provided command text is executed by a shell.
        environment = dict(os.environ)
        environment["PYTHONPATH"] = str(ROOT / "src") + os.pathsep + environment.get("PYTHONPATH", "")
        result = subprocess.run(command, cwd=ROOT, check=False, shell=False, env=environment)  # nosec B603
        if result.returncode:
            return result.returncode
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
