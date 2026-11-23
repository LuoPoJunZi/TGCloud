"""Dispatch to the verified standalone manager or the installer's bundled Go tool."""
from __future__ import annotations

import hashlib
import os
import subprocess  # nosec B404
import tempfile
from contextlib import contextmanager
from contextvars import ContextVar
from pathlib import Path

from tgcloud.deployment import runtime

_planner: ContextVar[tuple[Path, str] | None] = ContextVar("tgcloud_installer_planner", default=None)
_PREFER_BUNDLED = {"install-source", "manager-runtime", "installer-preflight", "resources", "lan-entry", "https-entry"}
_BOOTSTRAP_COMMANDS = _PREFER_BUNDLED | {"discover-install", "plan-install"}


@contextmanager
def planning_tool():
    """Make the Go binary shipped with the running installer available before deployment."""
    packaged = runtime.packaged_host_cli(Path(__file__).resolve().parents[3])
    if packaged is None:
        yield
        return
    with tempfile.TemporaryDirectory(prefix="tgcloud-plan-") as directory:
        executable = Path(directory) / runtime.HOST_CLI
        executable.write_bytes(packaged[1])
        executable.chmod(0o500)
        token = _planner.set((executable, hashlib.sha256(packaged[1]).hexdigest()))
        try:
            yield
        finally:
            _planner.reset(token)


def bundled_tool(source: Path | None = None) -> Path | None:
    staged = _planner.get()
    if staged is None:
        return None
    path, expected = staged
    if source is not None:
        packaged = runtime.packaged_host_cli(source)
        if packaged is None or hashlib.sha256(packaged[1]).hexdigest() != expected:
            return None
    return runtime.verified_host_cli(path.parent, expected)


def deployment_tool(command: str, source: Path) -> Path | None:
    bundled = bundled_tool(source)
    if bundled is not None and supports(command, executable=bundled):
        return bundled
    installed = runtime.installed_host_cli()
    return installed if installed is not None and supports(command, executable=installed) else None


def invoke(*args: str, lock_fd: int | None = None, inherit_output: bool = False,
           input_text: str | None = None, executable: Path | None = None) -> tuple[int, str] | None:
    if executable is None:
        bundled = bundled_tool() if args[0] in _BOOTSTRAP_COMMANDS else None
        if args[0] in _PREFER_BUNDLED and bundled is not None:
            executable = bundled
        else:
            executable = runtime.installed_host_cli() or bundled
    if executable is None:
        return None
    env = os.environ.copy()
    env.pop("TGCLOUD_HOST_LOCK_FD", None)
    if lock_fd is not None:
        env["TGCLOUD_HOST_LOCK_FD"] = str(lock_fd)
    try:
        process = subprocess.Popen(  # nosec B603
            [str(executable), *args], env=env,
            pass_fds=() if lock_fd is None else (lock_fd,),
            stdin=subprocess.DEVNULL if input_text is None else subprocess.PIPE,
            stdout=None if inherit_output else subprocess.PIPE,
            stderr=None if inherit_output else subprocess.STDOUT,
            text=True, encoding="utf-8", errors="replace",
        )
    except OSError:
        # No child was dispatched; the Python implementation can safely run.
        return None
    with process:
        try:
            timeouts = {"status": 45, "check": 65, "logs": 105, "preflight": 45, "resources": 50,
                        "prepare-image": 690, "preflight-candidate": 1350, "build-candidate": 2600,
                        "backup-config": 2500, "snapshot-database": 2500, "commit-upgrade-backup": 2500,
                        "quiesce-upgrade": 210, "resume-upgrade": 270,
                        "apply-upgrade": 2500, "rollback-upgrade": 3000, "deploy-bot": 2700, "accept-bot": 420,
                        "start-gateway": 360, "repair-gateway-network": 300, "prepare-dependencies": 1800,
                        "install-layout": 2500, "discover-install": 200, "plan-install": 200, "install-source": 200,
                        "manager-runtime": 180, "installer-preflight": 65, "lan-entry": 1100, "https-entry": 225, "install-acceptance": 140}
            timeout = 10 if args[1:] == ("--supported",) else timeouts.get(args[0], 135)
            output, _ = process.communicate(input=input_text, timeout=timeout)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate()
            raise TimeoutError("Go 管理命令超时，请核对实际运行状态") from None
        except KeyboardInterrupt:
            process.kill()
            process.communicate()
            raise
    return process.returncode, output or ""


def supports(command: str, *, executable: Path | None = None) -> bool:
    try:
        options = {} if executable is None else {"executable": executable}
        result = invoke(command, "--supported", **options)
    except (OSError, TimeoutError):
        return False
    return result is not None and result[0] == 0


def read_install_check(command: str, *args: str) -> str | None:
    if not supports(command):
        return None
    return checked_output(command, *args)


def checked_output(command: str, *args: str, input_text: str | None = None, executable: Path | None = None) -> str | None:
    options = {}
    if input_text is not None:
        options["input_text"] = input_text
    if executable is not None:
        options["executable"] = executable
    result = invoke(command, *args, **options)
    if result is None:
        return None
    code, output = result
    if code != 0:
        raise ValueError(output.strip() or "Go 安装检查或暂存失败")
    return output
