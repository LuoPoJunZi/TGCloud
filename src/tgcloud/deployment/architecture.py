"""Native Linux AMD64/ARM64 checks shared by the CLI and deployment hooks."""
from __future__ import annotations

import argparse
import json
import os
import platform
import re
import shlex
import struct
import subprocess  # nosec B404
import sys
from dataclasses import dataclass
from pathlib import Path

ALIASES = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
FORBIDDEN_ENV = (
    "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
    "DOCKER_DEFAULT_PLATFORM", "BUILDX_BUILDER", "BUILDKIT_HOST", "COMPOSE_BAKE",
    "COMPOSE_FILE", "COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES", "COMPOSE_ENV_FILES",
)
DOCKER_INFO = "docker info --format " + shlex.quote(
    '{"os":{{json .OSType}},"arch":{{json .Architecture}},'
    '"version":{{json .ServerVersion}},"security":{{json .SecurityOptions}}}',
)


def normalize_architecture(value: str) -> str:
    if not isinstance(value, str) or value.strip().lower() not in ALIASES:
        raise ValueError("不支持此 CPU 架构；仅支持 Linux AMD64 / ARM64")
    return ALIASES[value.strip().lower()]


@dataclass(frozen=True)
class NativeHost:
    architecture: str
    machine: str
    name: str

    @property
    def docker_platform(self) -> str:
        return "linux/" + self.architecture

    def describe(self, log) -> None:
        label = self.architecture.upper()
        log("TGCloud 环境检测")
        log("操作系统：" + self.name)
        log(f"CPU 架构：{label} ({self.machine})")
        log("Docker 平台：" + self.docker_platform)
        log("架构兼容：支持；部署方式：原生 " + label)


def detect_host() -> NativeHost:
    if platform.system() != "Linux":
        raise ValueError("仅支持 Linux（Debian / Ubuntu）原生 AMD64 / ARM64")
    machine = platform.machine()
    architecture = normalize_architecture(machine)
    if struct.calcsize("P") != 8:
        raise ValueError("需要 64 位 Linux 用户态与 Python；不支持 32 位用户态")
    release = Path("/etc/os-release").read_text(encoding="utf-8")
    values = {}
    for line in release.splitlines():
        key, separator, value = line.partition("=")
        if separator and key in {"ID", "PRETTY_NAME"}:
            parts = shlex.split(value)
            if len(parts) != 1 or any(ord(char) < 32 for char in parts[0]):
                raise ValueError("Linux 系统信息格式异常")
            values[key] = parts[0]
    if values.get("ID") not in {"ubuntu", "debian"}:
        raise ValueError("仅支持 Debian/Ubuntu；不猜测其他发行版的安装方式")
    try:
        # Fixed Debian package-manager query, no shell or user command text.
        result = subprocess.run(  # nosec B603 B607
            ["dpkg", "--print-architecture"], capture_output=True, text=True,
            timeout=10, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise ValueError("无法核对 dpkg 用户态架构；未执行安装") from None
    if result.returncode or result.stdout.strip() not in {"amd64", "arm64"}:
        raise ValueError("需要 Debian/Ubuntu 的 amd64 或 arm64 用户态")
    if result.stdout.strip() != architecture:
        raise ValueError("内核与用户态架构不一致；不自动使用 QEMU 或跨架构部署")
    return NativeHost(architecture, machine, values.get("PRETTY_NAME", values["ID"]))


def check_environment() -> None:
    if any(os.environ.get(key) for key in FORBIDDEN_ENV):
        raise ValueError("只允许本机默认 Docker/Compose；拒绝远端 daemon、平台与构建器覆盖")


def require_payload(source: Path) -> None:
    if normalize_architecture(platform.machine()) == "arm64":
        module = source / "src/tgcloud/deployment/architecture.py"
        if not module.is_file() or module.is_symlink():
            raise ValueError("目标稳定 Release 尚未包含 ARM64 原生部署检查；请等待兼容版本，不安装 main/预发布业务代码")


def check_docker(session, *, allow_missing: bool = False) -> dict | None:
    check_environment()
    expected = normalize_architecture(platform.machine())
    code, _ = session.run("command -v docker >/dev/null 2>&1", timeout=10)
    if code:
        if allow_missing and code == 1:
            return None
        raise ValueError("Docker 不可用；不自动重启或接管其他 daemon")
    code, output = session.run("docker context inspect --format '{{json .Endpoints.docker.Host}}'", timeout=15)
    try:
        endpoint = json.loads(output) if not code else None
    except (ValueError, TypeError):
        endpoint = None
    if endpoint != "unix:///var/run/docker.sock":
        raise ValueError("Docker 不可用或不是本机默认 daemon；拒绝远端/自定义 context")
    code, output = session.run(DOCKER_INFO, timeout=20)
    try:
        info = json.loads(output) if not code else None
        if not isinstance(info, dict) or info.get("os") != "linux" or not isinstance(info.get("version", ""), str):
            raise ValueError
        actual = normalize_architecture(info.get("arch"))
        security = info.get("security") or []
        if not isinstance(security, list) or any(not isinstance(item, str) or "rootless" in item.lower() for item in security):
            raise ValueError
    except (ValueError, TypeError):
        raise ValueError("Docker 平台/权限信息无效；需要本机 Linux rootful daemon") from None
    if actual != expected:
        raise ValueError("Docker daemon 与宿主 CPU 架构不一致；禁止模拟或跨架构部署")
    return {"architecture": actual, "platform": "linux/" + actual, "version": info.get("version", "")}


def check_standalone_docker(session, *, allow_missing: bool = False) -> dict | None:
    """Use the installed helper during standalone planning, never a candidate executable."""
    from tgcloud.cli import host_cli

    check_environment()
    args = ("preflight", "--allow-missing") if allow_missing else ("preflight",)
    try:
        result = host_cli.invoke(*args)
    except (OSError, TimeoutError):
        result = None
    if result is not None and result[0] == 1:
        raise ValueError(result[1].strip() or "Docker 宿主预检未通过")
    if result is not None and result[0] == 0:
        try:
            info = json.loads(result[1])
        except (ValueError, TypeError):
            pass
        else:
            expected = normalize_architecture(platform.machine())
            if info is None and allow_missing:
                return None
            if (isinstance(info, dict) and info.get("architecture") == expected and info.get("platform") == "linux/" + expected
                    and isinstance(info.get("version"), str)):
                return {"architecture": expected, "platform": "linux/" + expected, "version": info["version"]}
    return check_docker(session, allow_missing=allow_missing)


def _reference(value: str) -> str:
    if (not isinstance(value, str) or len(value) > 300 or value.startswith("-")
            or "://" in value or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_./:@-]*", value)):
        raise ValueError("镜像/容器引用格式不安全")
    return shlex.quote(value)


def check_image(session, reference: str) -> None:
    template = '{"os":{{json .Os}},"arch":{{json .Architecture}}}'
    code, output = session.run("docker image inspect --format " + shlex.quote(template) + " " + _reference(reference), timeout=20)
    try:
        info = json.loads(output) if not code else None
        if (not isinstance(info, dict) or info.get("os") != "linux"
                or normalize_architecture(info.get("arch")) != normalize_architecture(platform.machine())):
            raise ValueError
    except (ValueError, TypeError):
        raise ValueError("镜像缺失或不是宿主原生平台；未允许 QEMU/异架构容器启动") from None


def ensure_image(session, reference: str) -> None:
    """After confirmation only: select the native child of an immutable index."""
    _reference(reference)
    try:
        check_image(session, reference)
        return
    except ValueError:
        pass
    native = "linux/" + normalize_architecture(platform.machine())
    code, _ = session.run("docker pull --platform " + native + " " + _reference(reference), timeout=600)
    if code:
        raise ValueError("无法取得原生 " + native + " 镜像；不回退到其他架构或 latest")
    check_image(session, reference)


def check_container(session, name: str, *, require_running: bool = False) -> None:
    template = ('{"image":{{json .Image}},"running":{{json .State.Running}},'
                '"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}null{{end}}}')
    code, output = session.run("docker container inspect --format " + shlex.quote(template) + " " + _reference(name), timeout=20)
    try:
        info = json.loads(output) if not code else None
        if (not isinstance(info, dict) or not re.fullmatch(r"sha256:[a-f0-9]{64}", info.get("image", ""))
                or (require_running and (info.get("running") is not True or info.get("health") == "unhealthy"))):
            raise ValueError
    except (ValueError, TypeError):
        raise ValueError("容器运行状态不可验证；不会标记部署成功") from None
    check_image(session, info["image"])


def prepare_images(session, compose: Path, services: list[str], *, standalone: bool = False) -> None:
    """Pull native candidates before stopping any old Bot or touching its data."""
    check_docker(session)
    from tgcloud.deployment.configuration import parse_config
    # Compose can also consume these overrides from the project's .env, not
    # only its parent process. Never source the file or echo its contents.
    values = parse_config((compose.parent / ".env").read_bytes())
    if any(values.get(key) for key in FORBIDDEN_ENV):
        raise ValueError("候选 .env 包含 Docker/Compose 平台或构建器覆盖；未替换现有实例")
    code, output = session.run("docker compose -f " + shlex.quote(str(compose)) + " config --format json", timeout=30)
    try:
        config = json.loads(output) if not code else None
        entries = config["services"]
        images = [entries[service]["image"] for service in services]
        native = "linux/" + normalize_architecture(platform.machine())
        if any(entry.get("platform", native) != native for entry in entries.values()):
            raise ValueError
        dockerfile = (compose.parent / "Dockerfile").read_text(encoding="utf-8")
        base = re.findall(r"(?m)^FROM ([^\s]+)\s*$", dockerfile)
        if len(base) != 1:
            raise ValueError
    except (AttributeError, KeyError, ValueError, TypeError, OSError):
        raise ValueError("候选 Compose/Dockerfile 架构输入不可验证；未替换现有实例") from None
    references = [*base, *images]
    for image in references:
        _reference(image)
    from tgcloud.cli import host_cli
    use_go = standalone and host_cli.supports("prepare-image")
    for image in references:
        if use_go and re.search(r"@sha256:[a-f0-9]{64}\Z", image):
            result = host_cli.invoke("prepare-image", image)
            if result is not None:
                if result[0] != 0:
                    raise ValueError(result[1].strip() or "Go 候选镜像准备失败")
                continue
        ensure_image(session, image)


def build_candidate(session, stage: Path, tag: str, host: NativeHost) -> None:
    """Build and validate the standalone release before starting the upgrade transaction."""
    from tgcloud.cli import host_cli

    _reference(tag)
    stage = stage.absolute()
    if host_cli.supports("build-candidate"):
        result = host_cli.invoke("build-candidate", str(stage), tag, inherit_output=True)
        if result is not None:
            if result[0] != 0:
                raise ValueError("候选 Bot 未通过构建或配置预检；现有实例保留")
            return

    # Inherit the shell installer's process group and log stream as before.
    build = ["docker", "build", "--platform", host.docker_platform, "--tag", tag, str(stage)]
    if subprocess.run(build, check=False).returncode:  # nosec B603
        raise ValueError("候选 Bot 镜像构建失败；现有实例保留")
    check_image(session, tag)
    # Docker creates a private tmpfs inside the read-only candidate container.
    validate = ["docker", "run", "--rm", "--label", "com.tgcloud.candidate=" + tag,
                "--read-only", "--network", "none", "--tmpfs", "/tmp",  # nosec B108
                "--entrypoint", "python", "--env-file", str(stage / ".env"), tag,
                "-m", "app.deployment_check", "--validate-only"]
    if subprocess.run(validate, check=False).returncode:  # nosec B603
        raise ValueError("候选 Bot 配置校验失败；现有实例保留")


def dispatch_standalone_command(command: str, *arguments: str, inherit_output: bool = False) -> int:
    from tgcloud.cli import host_cli

    if host_cli.supports(command):
        result = host_cli.invoke(command, *arguments, inherit_output=inherit_output)
        if result is not None:
            if result[0]:
                if inherit_output:
                    return 1
                raise ValueError(result[1].strip() or "Go 升级操作失败")
            return 0
    # The installer uses the previous path only if no operation was dispatched.
    return 3


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="仅核对 Linux AMD64/ARM64 原生部署；不配置 QEMU")
    parser.add_argument("action", choices=("host", "docker", "image", "container", "prepare", "build-candidate",
                                           "backup-config", "snapshot-database", "commit-upgrade-backup",
                                           "quiesce-upgrade", "resume-upgrade", "apply-upgrade", "rollback-upgrade",
                                           "deploy-bot", "accept-bot", "report-install", "start-gateway",
                                           "repair-gateway-network", "restore-gateway-network", "cleanup-install",
                                           "check-install-resources", "prepare-dependencies", "check-install-host", "install-layout",
                                           "fresh-install", "stage-upgrade", "prepare-install-config",
                                           "preflight-candidate", "check-install-compose"))
    parser.add_argument("reference", nargs="?")
    parser.add_argument("--allow-missing", action="store_true")
    parser.add_argument("--service", action="append", default=[])
    parser.add_argument("--standalone", action="store_true")
    parser.add_argument("--candidate-tag")
    parser.add_argument("--backup-path")
    parser.add_argument("--snapshot-name")
    parser.add_argument("--stage-path")
    parser.add_argument("--config-path")
    parser.add_argument("--source-path")
    parser.add_argument("--apply-config", choices=("true", "false"), default="false")
    parser.add_argument("--restore-db", default="")
    parser.add_argument("--old-image-id", default="")
    parser.add_argument("--old-image-tag", default="")
    parser.add_argument("--check-gateway", action="store_true")
    parser.add_argument("--config-partial", default="")
    parser.add_argument("--database-partial", default="")
    args = parser.parse_args(argv)
    upgrade_backup = args.action in {"backup-config", "snapshot-database", "commit-upgrade-backup"}
    upgrade_program = args.action in {"apply-upgrade", "rollback-upgrade", "quiesce-upgrade", "resume-upgrade"}
    gateway_network = args.action in {"repair-gateway-network", "restore-gateway-network"}
    try:
        host = detect_host()
        check_environment()
        if args.action == "host":
            print(host.docker_platform)
            return 0
        from tgcloud.deployment.host import LocalSession, SafeLog
        session = LocalSession(SafeLog())
        if args.action == "docker":
            check_docker(session, allow_missing=args.allow_missing)
        elif args.reference and args.action == "image":
            check_image(session, args.reference)
        elif args.reference and args.action == "container":
            check_container(session, args.reference, require_running=True)
        elif args.reference and args.action == "prepare" and args.service:
            prepare_images(session, Path(args.reference), args.service, standalone=args.standalone)
        elif args.reference and args.action == "build-candidate" and args.candidate_tag:
            build_candidate(session, Path(args.reference), args.candidate_tag, host)
        elif args.reference and args.backup_path and args.action == "backup-config":
            return dispatch_standalone_command(args.action, args.reference, args.backup_path)
        elif (args.reference and args.backup_path and args.action == "snapshot-database"
              and args.candidate_tag and args.snapshot_name):
            return dispatch_standalone_command(args.action, args.reference, args.candidate_tag,
                                              args.snapshot_name, args.backup_path)
        elif args.reference and args.backup_path and args.action == "commit-upgrade-backup":
            return dispatch_standalone_command(args.action, args.reference, args.backup_path, inherit_output=True)
        elif args.reference and args.stage_path and args.action in {"quiesce-upgrade", "resume-upgrade"}:
            return dispatch_standalone_command(args.action, args.reference, args.stage_path, inherit_output=True)
        elif args.reference and args.stage_path and args.action == "apply-upgrade":
            return dispatch_standalone_command(args.action, args.reference, args.stage_path, args.apply_config)
        elif args.reference and args.backup_path and args.action == "rollback-upgrade":
            return dispatch_standalone_command(args.action, args.reference, args.backup_path,
                                              args.restore_db, args.old_image_id, args.old_image_tag)
        elif args.reference and args.action in {"deploy-bot", "report-install", "start-gateway", "check-install-resources"}:
            return dispatch_standalone_command(args.action, args.reference, inherit_output=True)
        elif args.reference and args.stage_path and gateway_network:
            return dispatch_standalone_command(args.action, args.reference, args.stage_path, inherit_output=True)
        elif args.reference and args.action == "accept-bot":
            gateway = ("--check-gateway",) if args.check_gateway else ()
            return dispatch_standalone_command(args.action, args.reference, *gateway, inherit_output=True)
        elif args.reference and args.action == "cleanup-install":
            return dispatch_standalone_command(args.action, args.reference, args.stage_path or "", args.candidate_tag or "",
                                              args.config_partial, args.database_partial, args.snapshot_name or "",
                                              args.restore_db, inherit_output=True)
        elif args.action == "prepare-dependencies" and args.reference in {"install", "upgrade"}:
            return dispatch_standalone_command(args.action, args.reference, inherit_output=True)
        elif args.reference and args.config_path and args.action == "check-install-host":
            return dispatch_standalone_command(args.action, args.reference, args.config_path, inherit_output=True)
        elif args.reference and args.action in {"preflight-candidate", "check-install-compose"}:
            return dispatch_standalone_command(args.action, args.reference, inherit_output=True)
        elif args.reference and args.config_path and args.action == "prepare-install-config":
            return dispatch_standalone_command(args.action, args.reference, args.config_path, inherit_output=True)
        elif args.reference and args.source_path and args.action == "stage-upgrade":
            return dispatch_standalone_command(args.action, args.reference, args.source_path, inherit_output=True)
        elif args.reference and args.action == "fresh-install":
            if args.source_path and args.config_path:
                return dispatch_standalone_command(args.action, "stage", args.reference, args.source_path,
                                                   args.config_path, inherit_output=True)
            if args.stage_path:
                return dispatch_standalone_command(args.action, "publish", args.reference, args.stage_path, inherit_output=True)
            raise ValueError("缺少新装部署包、候选目录或配置路径")
        elif args.reference and args.action == "install-layout":
            if args.config_path:
                if not args.stage_path:
                    raise ValueError("缺少候选配置暂存目录")
                arguments = ("prepare", args.reference, args.stage_path, args.config_path)
            else:
                arguments = ("finalize", args.reference, *((args.stage_path,) if args.stage_path else ()))
            return dispatch_standalone_command(args.action, *arguments, inherit_output=True)
        else:
            raise ValueError("缺少受管镜像、容器或候选服务参数")
        return 0
    except ValueError as exc:
        label = ("升级备份失败" if upgrade_backup else "升级程序操作失败" if upgrade_program else
                 "原生架构检查失败")
        print(label + "：" + str(exc), file=sys.stderr)
        return 1
    except (OSError, RuntimeError):
        # The execution boundary's errors can contain command output. Only the
        # validated, static errors above are suitable for a terminal message.
        if args.action == "deploy-bot":
            message = "正式 Bot 部署执行异常；请核对上方日志和本机 Docker。"
        elif args.action == "accept-bot":
            message = "Bot 验收执行异常；请核对上方日志和本机 Docker。"
        elif args.action == "report-install":
            message = "安装已通过验收，但无法生成部署摘要。"
        elif args.action == "start-gateway" or gateway_network:
            message = "CloudDrive2 容器或网络操作异常，请核对本机 Docker。"
        elif args.action == "cleanup-install":
            message = "安装暂存资源清理执行异常。"
        elif args.action == "prepare-dependencies":
            message = "宿主依赖准备执行异常，请核对上方 APT 与 Docker 日志。"
        elif args.action == "check-install-resources":
            message = "安装前资源检查执行异常，请核对内存和安装目录的磁盘信息。"
        elif args.action == "check-install-host":
            message = "宿主复核执行异常，请检查 Docker 状态、端口和磁盘。"
        elif args.action == "install-layout":
            message = "持久化目录或安装配置操作异常，请检查磁盘、路径和权限。"
        elif args.action == "fresh-install":
            message = "新装暂存或发布操作异常，请检查磁盘、路径和权限。"
        elif args.action == "stage-upgrade":
            message = "升级部署包暂存异常，请检查磁盘、路径和权限。"
        elif args.action in {"prepare-install-config", "preflight-candidate", "check-install-compose"}:
            message = "部署配置或镜像预检执行异常，请核对上方日志。"
        elif upgrade_backup or upgrade_program:
            message = "升级执行异常；请核对磁盘、目录权限和本机 Docker。"
        else:
            message = "原生架构检查失败；请核对 64 位用户态、本机 Docker 平台和原生镜像。"
        print(message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
