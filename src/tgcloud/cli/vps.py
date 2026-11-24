from __future__ import annotations

import argparse
import sys
from contextlib import ExitStack, nullcontext
from pathlib import Path
from zoneinfo import ZoneInfoNotFoundError

from tgcloud.cli import host_cli, terminal
from tgcloud.cli.presentation import InstallerLog
from tgcloud.deployment import (
    instances,
    local_source,
    preflight,
    runtime,
)
from tgcloud.deployment.host import LocalSession, SafeLog
from tgcloud.deployment.lifecycle import install, upgrade
from tgcloud.deployment.profiles import PRODUCTS


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description="TGCloud 本地源码安装 / 保留配置升级")
    result.add_argument("--source-dir", type=Path, default=Path(__file__).resolve().parents[3], help="完整 TGCloud 本地源码目录")
    result.add_argument("--edition", choices=["clouddrive2"], default="clouddrive2", help="存储后端固定为 CloudDrive2")
    result.add_argument("--install-dir", help="仅支持单实例的 /opt 下目录；不搬迁现有实例")
    result.add_argument("--check", action="store_true", help="只读实例/版本/HTTPS检查；不收集秘密，不执行安装")
    result.add_argument("--proxy-mode", choices=("managed", "external_docker"), help="显式选择 HTTPS 入口；不隐式迁移已有模式")
    result.add_argument("--nginx-container", help="明确授权复用的外部 Docker Nginx 容器")
    result.add_argument("--authorize-proxy", action="store_true", help="授权修改目标容器专属站点；仍需最终计划确认")
    result.add_argument("--configure-https", action="store_true", help="显式续做/修复 HTTPS；已有域名不切换")
    result.add_argument("--verify", action="store_true", help="另行确认 WebDAV 写入验收；默认不会在升级中运行")
    result.add_argument("--show-credentials", action="store_true", help="仅经确认在私有终端显示已有凭据；不安装、升级或改密")
    return result


def main(argv: list[str] | None = None, *, friendly: bool | None = None) -> int:
    arguments = list(sys.argv[1:] if argv is None else argv)
    args = parser().parse_args(arguments)
    friendly = not arguments if friendly is None else friendly
    log = InstallerLog(SafeLog(), friendly=friendly)
    ui = None
    planner = ExitStack()
    try:
        if args.show_credentials and (args.check or args.configure_https or args.verify):
            raise ValueError("--show-credentials 必须单独使用，不能与检查、HTTPS 修复或验收组合")
        planner.enter_context(host_cli.planning_tool())
        preflight.validate_install_host(log=log)
        session = LocalSession(log)
        preflight.check_install_docker(session, "base")
        selected_instances = [instances.discover_for_install(PRODUCTS["clouddrive2"], session, log, install_dir=args.install_dir)]
        if args.show_credentials:
            if not all(item.present for item in selected_instances):
                raise ValueError("尚无完整实例，不生成凭据、不启动安装")
            if ui is None:
                ui = terminal.Terminal()
            for instance in selected_instances:
                terminal.show_credentials(ui, instance.product, instance.config, log)
            log("TGCLOUD_CLI_RESULT=NO_CHANGE；凭据查看结束，未修改实例或配置")
            return 0
        with local_source.snapshot(args.source_dir, host_cli=True) as release:
            source = release.root
            log("TGCloud 正式源码：v" + release.version + "；SHA256：" + release.digest)
            plans = []
            for instance in selected_instances:
                old_source = source
                if not args.check and ui is None:
                    ui = terminal.Terminal()
                    ui.friendly = friendly
                extra = {}
                if args.proxy_mode or args.nginx_container or args.authorize_proxy:
                    extra["proxy_options"] = {"proxy_mode": args.proxy_mode, "nginx_container": args.nginx_container,
                                              "authorize_proxy": args.authorize_proxy}
                plan = install.make_plan(instance, release, source, old_source, session, log, ui,
                                         configure_https=args.configure_https, **extra)
                terminal.preview(plan, release, log)
                plans.append((plan, old_source))
            if args.check:
                log("TGCLOUD_CLI_CHECK=OK；只读预检，不等于部署/WebDAV/公网验收通过")
                return 0
            actionable = [(plan, baseline) for plan, baseline in plans if plan.action in {"install", "upgrade"} or plan.configure_https or args.verify]
            manage_runtime = friendly or runtime.COMMAND.exists()
            if not actionable:
                if friendly:
                    runtime.check_install_destination()
                    if ui.confirm("实例无需安装/升级；是否仅安装本地管理命令 tgcloud？不重启容器"):
                        with runtime.lifecycle_lock():
                            upgrade.verify_confirmed_source(release, args.source_dir)
                            for plan, baseline in plans:
                                upgrade.recheck_plan(plan.instance, plan.action, release, source, baseline, session, log)
                            can_sync = all(
                                plan.action == "current" and plan.instance.present and plan.instance.source_digest == release.digest
                                for plan, _ in plans
                            )
                            target_dir = plans[0][0].instance.directory if len(plans) == 1 else None
                            log.cli = runtime.install_release(source, release, log, sync_addons=False, instance_dir=target_dir)
                            if can_sync and isinstance(log.cli, dict) and log.cli.get("commands") == {"version": "ok", "listen": "ok"}:
                                try:
                                    runtime.sync_installed_addons(source, release, log, instance_dir=target_dir)
                                except (ValueError, RuntimeError, OSError) as exc:
                                    reason = str(exc) if isinstance(exc, (ValueError, RuntimeError)) else "文件系统或权限异常"
                                    log("[待核对] 外部 WebDAV 附加工具同步未完成：" + reason + "；可在 tgcloud 菜单 2.3 核对。")
                log("TGCLOUD_CLI_RESULT=NO_CHANGE")
                return 0
            if manage_runtime:
                runtime.check_install_destination()
            if friendly:
                log("\n── 执行确认 ──\n将安装本地管理命令 tgcloud；备份含敏感配置，仅保存在本机。请勿同时运行其他安装器。")
            else:
                log("备份含敏感配置，仅保存在 VPS；失败可能需人工恢复。请勿同时运行其他安装器。")
            if not ui.confirm("确认执行上述所选计划？所选 Bot 可能短暂中断"):
                log("TGCLOUD_CLI_RESULT=CANCELLED；未执行安装/升级/HTTPS变更")
                return 0
            with runtime.lifecycle_lock() if manage_runtime else nullcontext():
                upgrade.verify_confirmed_source(release, args.source_dir)
                for plan, baseline in actionable:
                    install.execute_plan(plan, release, source, baseline, session, log, ui, verify=args.verify,
                                         install_manager=manage_runtime)
            log("TGCLOUD_CLI_RESULT=BASE_HTTPS_OK；WebDAV 是否验收请以独立结果为准")
        return 0
    except KeyboardInterrupt:
        log("TGCLOUD_CLI_RESULT=INTERRUPTED；请核对安装器回退日志；不假定状态已恢复")
        return 130
    except (ValueError, RuntimeError, TimeoutError, ZoneInfoNotFoundError) as exc:
        log("TGCLOUD_CLI_RESULT=FAILED：" + str(exc))
        return 1
    except Exception:  # noqa: BLE001 - secret-safe terminal boundary
        # Never dump a traceback containing parsed configuration or raw API data.
        log("TGCLOUD_CLI_RESULT=FAILED：检查/操作发生异常；未输出配置或凭据，请核对实例状态后重试")
        return 1
    finally:
        planner.close()
        if ui is not None:
            ui.close()


def interrupted(_signum: int, _frame) -> None:
    raise KeyboardInterrupt
