"""Independent CLI. Parsing/help never imports Bot or probes Docker."""
from __future__ import annotations

import argparse
import os
import signal
import subprocess  # nosec B404
import sys
from pathlib import Path

from tgcloud.cli.presentation import numbered_menu
from tgcloud.cli.terminal import Terminal
from tgcloud.deployment.configuration import plain

ACTIONS = ("install", "status", "check", "enable", "disable", "configure", "uninstall", "upgrade", "print-config", "users")
ACTION_LABELS = (
    "安装外部服务", "查看运行状态", "检查连接配置", "启用外部访问", "停用外部访问",
    "修改域名账号", "卸载外部服务", "升级维护工具", "查看连接信息", "管理外部账号",
)


class WebDAVTerminal(Terminal):
    """Add-on-only password hints/display; leave main installer prompts intact."""

    def ask(self, prompt: str, *, default: str = "", secret: bool = False, validate=plain) -> str:
        from tgcloud.addons.external_webdav.config import (
            INPUT_ERROR,
            PUBLIC_INPUT_ERRORS,
        )
        while True:
            value = super().ask(prompt, default=default, secret=secret, validate=lambda text: text)
            try:
                return validate(value)
            except ValueError as exc:
                # Reasons are fixed, reviewed strings; arbitrary validator
                # messages and rejected values never reach the terminal/log.
                reason = str(exc)
                if not secret and reason == "请填写要用于管理页的域名。":
                    reason = "请填写外部 WebDAV 域名。"
                hint = INPUT_ERROR if secret else (reason if reason in PUBLIC_INPUT_ERRORS
                        else "输入格式不正确，请按提示重新填写。")
                self.output.write(hint + "\n")
                self.output.flush()

    def show_password(self, value: str) -> None:
        from tgcloud.addons.external_webdav.config import password
        password(value)
        if not self.output.isatty():
            raise RuntimeError("密码仅允许向当前私有终端显示。")
        self.output.write("密码：" + value + "\n")
        self.output.flush()

    def confirm(self, prompt: str) -> bool:
        return self.ask(prompt + " [y/N]", validate=lambda value: value.strip().lower()) == "y"


def choose(ui, log, title: str, choices: tuple[tuple[str, str], ...], *, default: str = "", exit_label: str = "退出") -> str:
    if title:
        log(title)
    numbers = {}
    for index, (key, label) in enumerate(choices, 1):
        numbers[str(index)] = key
    log(numbered_menu((*((index, label) for index, (_, label) in enumerate(choices, 1)), (0, exit_label))))

    def selected(value: str) -> str:
        value = value.strip()
        if value == "0":
            return ""
        if value in numbers:
            return numbers[value]
        if value in numbers.values():
            return value  # Existing interactive names remain accepted.
        raise ValueError("请选择菜单中的序号。")

    prompt = "输入序号" + (f"（回车默认为 {default}）" if default else "")
    return ui.ask(prompt, default=default, validate=selected)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description="TGCloud 外部 WebDAV：独立只读 HTTPS 附加功能（不安装 Bot）")
    result.add_argument("action", nargs="?", choices=ACTIONS)
    result.add_argument("--edition", choices=("clouddrive2",), default="clouddrive2")
    result.add_argument("--install-dir", help="已有受管实例的实际安装目录（须通过容器身份校验）")
    result.add_argument("--sample-file", default="", help="读取验收可选：现有文件的未编码相对名称；仅 HEAD/OPTIONS/单字节 Range")
    result.add_argument("--runtime-check", action="store_true", help=argparse.SUPPRESS)
    result.add_argument("--from-manager", action="store_true", help=argparse.SUPPRESS)
    return result


def source_root() -> Path:
    return Path(os.environ.get("TGCLOUD_SOURCE_ROOT", Path(__file__).resolve().parents[4]))


def source_identity(root: Path) -> dict:
    from tgcloud.addons.external_webdav import releases
    return {"schema": 1, "owner": releases.OWNER, "project": releases.PROJECT_ID,
            "version": releases.core.source_version(root), "origin": releases.source_identity(root)}


def perform(service, args) -> None:
    """One operation; explicit subcommands keep their one-shot behavior."""
    from tgcloud.cli.presentation import page_heading
    from tgcloud.version import VERSION

    log, ui = service.log, service.ui
    log(page_heading(ACTION_LABELS[ACTIONS.index(args.action)], service.product, VERSION))
    if getattr(args, "runtime_check", False):
        if args.action != "status":
            raise ValueError("维护代码校验仅支持只读状态操作。")
        item = service.record()
        if item is None:
            raise ValueError("外部 WebDAV 尚未登记。")
        _, state = service.preflight(item["instance_dir"], require_running=False)
        route = state.get("external_webdav", {}).get(service.product.key)
        if item["phase"] != "enabled" and route and route.get("enabled"):
            raise ValueError("外部登记状态与活动路由不一致，请先停用入口并核对。")
        log("维护代码与实例检查通过；未执行账号认证或启用访问。")
        return
    if args.action in {"install", "configure"}:
        def prepare_runtime():
            source = source_root()
            service.install_runtime(source, source_identity(source))

        service.configure(install_dir=args.install_dir, install=args.action == "install",
                          sample_file=args.sample_file, prepare_runtime=prepare_runtime)
    elif args.action == "users":
        from tgcloud.addons.external_webdav.users import menu
        menu(service, sample_file=args.sample_file)
    elif args.action == "upgrade":
        item = service.record()
        if item is None:
            raise ValueError("尚未安装；请先运行 install。")
        service.preflight(item["instance_dir"], require_running=False)
        log("升级仅更新离线维护代码，不要求输入外部密码；访问状态和后台账号保持原状。")
        source_path = Path(ui.ask("完整 TGCloud 本地源码绝对目录"))
        if not source_path.is_absolute() or source_path.resolve() != source_path:
            raise ValueError("源码目录必须为不经过链接的绝对路径")
        if ui.confirm("确认核验本地源码并更新离线维护工具？"):
            from tgcloud.addons.external_webdav.releases import supported
            from tgcloud.deployment import local_source
            from tgcloud.deployment.releases import source_version, version_tuple
            with local_source.snapshot(source_path) as source:
                root = source.root
                supported(root)
                identity = source_identity(root)
                if version_tuple(source_version(root)) < version_tuple(source_version(source_root())):
                    raise ValueError("拒绝自动降级附加功能代码。")
                # Execute the verified candidate, not just the old checker.
                # Candidate checks source/instance compatibility without account authentication.
                action = "status"
                result = subprocess.run(  # nosec B603
                    [sys.executable, "-B", str(root / "scripts/run_source.py"), "webdav", action,
                     "--edition", args.edition, "--install-dir", item["instance_dir"], "--runtime-check"], check=False,
                )
                if result.returncode:
                    raise ValueError("维护代码候选检查未通过；原离线工具保留，请核对实例和配置。")
                with service.locked():
                    service.assert_record(item)
                    service.preflight(item["instance_dir"], require_running=False)
                    service.install_runtime(root, identity)
                log("维护代码已更新，旧运行目录保留；外部账号认证及访问启用未重新验收。")
                if item["phase"] in {"pending", "needs_repair"}:
                    log("入口仍待配置，请选择“继续完成配置”。")
                service.management_hint(check=True)
    else:
        service.maintain(args.action, sample_file=args.sample_file)


def interactive(service, args, *, operation=None) -> None:
    """Result pages stay visible; refreshing never repeats a mutation."""
    from tgcloud.cli.presentation import page_heading
    from tgcloud.version import VERSION

    while True:
        service.log(page_heading("外部 WebDAV 管理", service.product, VERSION))
        record = service.record()
        repair = bool(record and record["phase"] in {"pending", "needs_repair"})
        labels = tuple("继续完成配置" if repair and key == "configure" else label for key, label in zip(ACTIONS, ACTION_LABELS))
        action = choose(service.ui, service.log, "请选择操作：", tuple(zip(ACTIONS, labels)),
                        default="6" if repair else "2" if record else "1",
                        exit_label="返回上级" if os.environ.get("TGCLOUD_MANAGER_MENU") == "1" else "退出")
        if not action:
            return
        args.action = action
        while True:
            (operation or perform)(service, args)
            if action in {"users", "uninstall", "upgrade"}:
                # Uninstall/upgrade can remove or replace the executing runtime.
                # Do not continue reading old code after either operation.
                if action != "users":
                    return
                break
            refresh = action in {"status", "check"}
            choices = (("refresh", "重新检查" if action == "check" else "刷新状态"),) if refresh else ()
            if not choose(service.ui, service.log, "", choices, default="0", exit_label="返回上级"):
                break


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    os.umask(0o077)
    from tgcloud.cli.commands import interrupted
    for name in ("SIGTERM", "SIGHUP"):
        if hasattr(signal, name):
            signal.signal(getattr(signal, name), interrupted)
    from tgcloud.addons.external_webdav import discovery
    from tgcloud.addons.external_webdav.service import Service
    from tgcloud.deployment.host import LocalSession, SafeLog
    from tgcloud.deployment.profiles import PRODUCTS

    log, ui = SafeLog(), None
    try:
        session = LocalSession(log)
        if getattr(args, "from_manager", False):
            if args.action is not None or args.sample_file or getattr(args, "runtime_check", False):
                raise ValueError("管理入口模式不支持直接指定子命令或附加参数。")
            from tgcloud.addons.external_webdav import entry
            from tgcloud.deployment import instances

            discovery.host(session, log=log)
            ui = Terminal()
            ui.friendly = True
            instance = instances.discover(
                PRODUCTS[args.edition],
                instances.docker_inventory(session),
                session,
                log,
                install_dir=args.install_dir,
            )
            entry.open_menu(instance, ui, log)
            return 0
        # Help has already exited above. Offline status still needs local Linux
        # root/private records, but never contacts GitHub, Docker or the gateway.
        if args.action not in {"status", "print-config"} or getattr(args, "runtime_check", False):
            discovery.host(session, log=log)
        elif os.name != "posix" or os.geteuid() != 0:
            raise ValueError("本地状态仅允许 Linux root 读取。")
        if args.edition is None or args.action is None or args.action not in {"status", "print-config"}:
            ui = WebDAVTerminal()
            from tgcloud.version import VERSION
            if os.environ.get("TGCLOUD_MANAGER_MENU") != "1":
                log("TGCloud 外部 WebDAV v" + VERSION)
                log("独立只读文件访问，不改变 Bot 的上传配置。")
        service = Service(PRODUCTS[args.edition], session, log, ui)
        if ui is not None and os.environ.get("TGCLOUD_MANAGER_MENU") != "1":
            log("当前网关：" + PRODUCTS[args.edition].display_name)
        if args.action is None:
            interactive(service, args)
        else:
            perform(service, args)
        return 0
    except (KeyboardInterrupt, EOFError):
        log("操作中断；已撤销的入口不会退回匿名或 Bot 凭据，请运行 status／configure 核对。")
        return 130
    except (ValueError, RuntimeError, OSError) as exc:
        log("EXTERNAL_WEBDAV=FAILED：" + (str(exc) if isinstance(exc, (ValueError, RuntimeError)) else type(exc).__name__))
        return 1
    except Exception as exc:  # noqa: BLE001 - never print raw response/traceback containing credentials
        log("EXTERNAL_WEBDAV=FAILED：未确认的运行错误（" + type(exc).__name__ + "），请核对本地状态；未声称回退成功。")
        return 1
    finally:
        if ui is not None:
            ui.close()


if __name__ == "__main__":
    from tgcloud.cli.commands import interrupted
    signal.signal(signal.SIGTERM, interrupted)
    raise SystemExit(main())
