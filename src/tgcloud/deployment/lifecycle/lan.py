"""LAN installation and maintenance workflow."""

from __future__ import annotations

import argparse
import json
import sys
from contextlib import ExitStack, nullcontext
from dataclasses import dataclass, field
from pathlib import Path

from tgcloud.cli import host_cli, terminal
from tgcloud.cli.presentation import InstallerLog, OperationReport
from tgcloud.deployment import architecture, instances, local_source, preflight, runtime
from tgcloud.deployment.host import LocalSession, SafeLog, edition_lock
from tgcloud.deployment.lifecycle import install, upgrade
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.deployment.proxy import lan


def validate_environment(session: LocalSession, *, log=None) -> None:
    if not preflight.validate_install_host(log=log, lan=True):
        # Preserve the original host and iproute2 checks for older tools.
        architecture.check_environment()
        code, _ = session.run("command -v ip >/dev/null 2>&1", timeout=10)
        if code:
            raise ValueError("缺少 iproute2；请先人工安装，不在信息收集前安装软件")
    preflight.check_install_docker(session, "lan")


@dataclass
class Plan:
    instance: instances.Instance
    action: str
    address: str
    saved: dict | None
    config: dict[str, str] = field(default_factory=dict, repr=False)
    entry_running: bool = False
    entry_healthy: bool = False
    feedback: OperationReport | None = field(default=None, repr=False)


def native_entry(operation: str, product, *args: str, source: Path | None = None) -> dict | None:
    if source is not None:
        executable = host_cli.deployment_tool("lan-entry", source)
        if executable is None:
            return None
        output = host_cli.checked_output("lan-entry", operation, product.install_dir, lan.NGINX_IMAGE, *args, executable=executable)
    else:
        output = host_cli.read_install_check("lan-entry", operation, product.install_dir, lan.NGINX_IMAGE, *args)
    if output is None:
        return None
    result = json.loads(output)
    if not isinstance(result, dict):
        raise ValueError("无法读取 Go 内网入口检查结果")  # noqa: TRY004 - invalid helper output
    return result


def entry_state(product) -> dict | None:
    result = native_entry("state", product)
    if result is not None:
        return result["saved"]
    lan.reject_https_route(product)
    return lan.load_state(product)


def observe_python_entry(instance, address, session, log, *, planning=False, saved=None) -> tuple[bool, bool]:
    product = instance.product
    address = lan.validate_local_ip(address, session)
    inventory = instances.docker_inventory(session)
    lan.check_published_ports(product, session, inventory)
    entry = lan.inspect_lan(product, session, inventory)
    if planning and entry and saved is None:
        raise ValueError("同名内网容器存在但缺少受管记录；不接管")
    if entry and not instance.present:
        raise ValueError("只有内网入口、缺少完整 Bot/网关；停止，不重装")
    running = bool(entry and entry.get("running"))
    lan.check_port(product, address, running)
    if instance.present:
        lan.check_backend(instance, session, log)
    else:
        lan.check_port(product, "127.0.0.1", False)
    if running and planning:
        try:
            lan.check_entry(product, address)
            return running, True
        except (ValueError, OSError):
            pass
    return running, False


def make_plan(instance, release, source, baseline, session, log, ui=None, *, address="") -> Plan:
    product = instance.product
    saved = entry_state(product)
    if instance.present and saved is None:
        raise ValueError("已有实例不是本内网脚本创建；不转换 VPS/HTTPS 或接管已有网关")
    action = upgrade.prepare_plan(instance, release, source, baseline, session)
    if saved:
        if address and address != saved["bind_ip"]:
            raise ValueError("不隐式切换已保存的内网地址")
        address = saved["bind_ip"]
    elif not address:
        if ui is None:
            raise ValueError("首次只读预检请显式提供 --lan-ip；不会自动猜测地址")
        address = ui.ask("运行脚本的 Linux 内网 IPv4（虚拟机填虚拟机地址）", validate=lan.validate_ip)
    entry = native_entry("check", product, address, "existing" if instance.present else "fresh")
    if entry is None:
        running, healthy = observe_python_entry(instance, address, session, log, planning=True, saved=saved)
    else:
        if entry["saved"] != saved:
            raise ValueError("预检期间内网配置发生变化；停止")
        running, healthy = entry["running"], entry["healthy"]
    if running and not healthy:
        log("已有内网入口检查未通过；预览后需确认才尝试恢复入口，不重装 Bot。")
    plan = Plan(instance, action, address, saved, dict(instance.config), running, healthy)
    if action == "install" and ui is not None:
        storage = preflight.choose_storage(instance, session, log, ui)
        plan.config = terminal.collect_config(ui, product, storage=storage)
        log.register(plan.config)
    if action in {"install", "upgrade"}:
        preflight.resource_check(instance, plan.config, session, log)
    return plan


def preview(plan: Plan, release: local_source.LocalSource, log: SafeLog) -> None:
    labels = {"install": "首次安装", "upgrade": "保留配置升级", "current": "已是最新，不重建 Bot", "newer": "高于 latest，不降级"}
    log(f"TGCloud：{labels[plan.action]}；{plan.instance.version or '未安装'} → {release.version}")
    log("内网 HTTP 管理地址：" + f"http://{plan.address}:{plan.instance.product.management_port}")
    log("安装目录：" + str(plan.instance.directory))
    log("不申请证书、不操作共享 HTTPS、不占用 80/443；HTTP 不加密，请勿端口转发到公网。")
    if plan.instance.present:
        log("内网入口：" + ("本机检查通过" if plan.entry_healthy else "未就绪，需要确认后恢复；不会仅凭预检标记为可用"))
    if plan.action in {"install", "upgrade"}:
        log("复用正式安装器的 APT/Docker 依赖与备份/有限回退；可能更新系统软件包，所选 Bot 可能短暂停止。")
    log("已有 .env、Session、SQLite、下载和网关数据保留；不改另一实例，不接管第三方服务。")


def execute(plan: Plan, release, source, baseline, session, log, ui, *, verify=False, install_manager=False) -> None:
    instance = plan.instance
    product = instance.product
    with edition_lock(Path("/opt/tgcloud-cli-locks") / (product.key + ".lock")), install.execution_feedback(
        plan, release, session, log, mode="lan", source=source
    ) as feedback:
        current = upgrade.recheck_plan(instance, plan.action, release, source, baseline, session, log,
                                       install_dir=product.install_dir)
        observed = native_entry("check", product, plan.address, "existing" if current.present else "fresh")
        if observed is None:
            if lan.load_state(product) != plan.saved:
                raise ValueError("确认前后内网配置发生变化；停止")
            lan.reject_https_route(product)
            running, _ = observe_python_entry(current, plan.address, session, log)
        else:
            if observed["saved"] != plan.saved:
                raise ValueError("确认前后内网配置发生变化；停止")
            running = observed["running"]
        if plan.action in {"install", "upgrade"}:
            preflight.resource_check(instance, plan.config, session, log)
        else:
            instances.check_foreign_gateway(product, session)
        if install_manager:
            log.cli = runtime.install_release(source, release, log, sync_addons=False, instance_dir=instance.directory)
            feedback.acceptance_tool = host_cli.deployment_tool("install-acceptance", source)
        # Persist the explicit intent before base installation. If base succeeds
        # but Nginx fails, rerunning resumes the LAN entry, not a fresh reinstall.
        if native_entry("save", product, plan.address, source=source) is None:
            lan.save_state(product, plan.address)
        feedback.stage = "基础安装与升级"
        install.apply_base(plan, release, source, session, log)
        if plan.action in {"install", "upgrade"}:
            feedback.checks["base"] = "ok"
        if install_manager and isinstance(log.cli, dict) and log.cli.get("commands") == {"version": "ok", "listen": "ok"}:
            install.sync_addons_for_plan(feedback, source, release, log, instance.directory)
        feedback.stage = "管理入口检查"
        validate_environment(session)  # also validate a newly installed daemon
        current = instances.discover_for_install(product, session, log, install_dir=product.install_dir)
        if native_entry("activate", product, plan.address, source=source) is None:
            activate_python_entry(current, plan.address, running, session, log)
        feedback.checks["entry"] = "ok"
        feedback.stage = "安装后检查"
        log("TGCLOUD_LAN_BASE=OK；TGCLOUD_LAN_ENTRY=OK；HTTPS=NOT_APPLICABLE")
        log(f"TGCloud 内网管理页：http://{plan.address}:{product.management_port}")
        log("请先用局域网电脑的浏览器打开上面的地址，配置云存储，并创建与保存凭据一致的专用 WebDAV 用户；完成后再确认写入验收。")
        if plan.action == "install" or not plan.entry_healthy:
            feedback.stage = "后台账号设置"
            if plan.action == "install":
                terminal.show_setup_credentials(ui, product, current.config, log)
            else:
                terminal.show_credentials(ui, product, current.config, log)
        if verify or plan.action == "install":
            feedback.stage = "WebDAV 写入验收"
            verified = install.verify_destination(plan, session, log, ui)
            feedback.checks["webdav"] = "ok" if verified is True else "not_run"


def activate_python_entry(instance, address, was_running, session, log) -> None:
    product = instance.product
    lan.check_backend(instance, session, log)
    entry_healthy = False
    if was_running:
        try:
            lan.check_entry(product, address)
            entry_healthy = True
        except (ValueError, OSError):
            pass
    if not entry_healthy:
        architecture.ensure_image(session, lan.NGINX_IMAGE)
        up = "up -d --no-deps --force-recreate lan" if was_running else "up -d lan"
        for arguments in ("config --quiet", "pull lan", "run --rm --no-deps lan -t", up):
            code, _ = session.run(lan.compose_command(product, arguments), stream=log, timeout=180)
            if code:
                raise RuntimeError("内网入口未就绪；基础数据和模式记录保留，请重新检查后续做，不要删除实例")
    entry = lan.inspect_lan(product, session, instances.docker_inventory(session))
    if not entry or not entry.get("running"):
        raise RuntimeError("内网入口容器未运行；不冒充成功")
    architecture.check_container(session, lan.container_name(product), require_running=True)
    lan.wait_for_entry(product, address)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description="TGCloud 独立局域网/NAS 向导；不改变 EXE/VPS HTTPS 模式")
    result.add_argument("--source-dir", type=Path, default=Path(__file__).resolve().parents[4], help="完整 TGCloud 本地源码目录")
    result.add_argument("--edition", choices=["clouddrive2"], default="clouddrive2", help="存储后端固定为 CloudDrive2")
    result.add_argument("--lan-ip", help="人工指定本机 RFC1918 IPv4；已有实例不隐式换地址")
    result.add_argument("--check", action="store_true", help="只读预检；首次需 --edition 和 --lan-ip")
    result.add_argument("--verify", action="store_true", help="另行确认 WebDAV 真写验收")
    result.add_argument("--show-credentials", action="store_true", help="仅在私有终端逐项确认显示已保存凭据")
    return result


def main(argv=None, *, friendly=None) -> int:
    arguments = list(sys.argv[1:] if argv is None else argv)
    args = parser().parse_args(arguments)
    friendly = not arguments if friendly is None else friendly
    log, ui = InstallerLog(SafeLog(), friendly=friendly), None
    planner = ExitStack()
    try:
        if args.check and (args.verify or args.show_credentials):
            raise ValueError("--check 不能与写入验收或凭据显示组合")
        if args.show_credentials and args.verify:
            raise ValueError("凭据显示必须单独使用")
        session = LocalSession(log)
        planner.enter_context(host_cli.planning_tool())
        validate_environment(session, log=log)
        product = PRODUCTS[args.edition]
        instance = instances.discover_for_install(product, session, log, install_dir=product.install_dir)
        if args.show_credentials:
            saved = entry_state(product)
            if not saved or not instance.present:
                raise ValueError("没有本脚本创建的完整内网实例；不生成凭据")
            if ui is None:
                ui = terminal.Terminal()
            terminal.show_credentials(ui, product, instance.config, log)
            log("TGCLOUD_LAN_RESULT=NO_CHANGE；未修改配置或密码")
            return 0
        with local_source.snapshot(args.source_dir, host_cli=True) as release:
            source = release.root
            baseline = source
            log("TGCloud 正式源码：v" + release.version + "；SHA256：" + release.digest)
            if not args.check and ui is None:
                ui = terminal.Terminal()
                ui.friendly = friendly
            plan = make_plan(instance, release, source, baseline, session, log, ui, address=args.lan_ip or "")
            preview(plan, release, log)
            if args.check:
                if instance.present and not plan.entry_healthy:
                    raise ValueError("基础预检完成，但内网管理入口尚未通过；未执行恢复或重建")
                log("TGCLOUD_LAN_CHECK=OK；只读预检，不等于安装/外部访问/WebDAV/转存通过")
                return 0
            manage_runtime = friendly or runtime.COMMAND.exists()
            if plan.action == "newer" or (plan.action == "current" and plan.entry_healthy and not args.verify):
                if friendly:
                    runtime.check_install_destination()
                    if ui.confirm("实例无需安装/升级；是否仅安装本地管理命令 tgcloud？不重启容器"):
                        with runtime.lifecycle_lock():
                            upgrade.verify_confirmed_source(release, args.source_dir)
                            upgrade.recheck_plan(instance, plan.action, release, source, baseline, session, log,
                                                 install_dir=product.install_dir)
                            can_sync = plan.action == "current" and instance.present and instance.source_digest == release.digest
                            log.cli = runtime.install_release(source, release, log, sync_addons=False, instance_dir=instance.directory)
                            if can_sync and isinstance(log.cli, dict) and log.cli.get("commands") == {"version": "ok", "listen": "ok"}:
                                try:
                                    runtime.sync_installed_addons(source, release, log, instance_dir=instance.directory)
                                except (ValueError, RuntimeError, OSError) as exc:
                                    reason = str(exc) if isinstance(exc, (ValueError, RuntimeError)) else "文件系统或权限异常"
                                    log("[待核对] 外部 WebDAV 附加工具同步未完成：" + reason + "；可在 tgcloud 菜单 2.3 核对。")
                log("TGCLOUD_LAN_RESULT=NO_CHANGE；不重建、不降级")
                return 0
            if manage_runtime:
                runtime.check_install_destination()
            if friendly:
                log("将安装本地管理命令 tgcloud；仅用于可信局域网，不自动切换为公网 HTTPS。")
            if not ui.confirm("确认上述内网 HTTP 计划？仅用于可信局域网，禁止公网转发；所选 Bot 可能短暂停止"):
                log("TGCLOUD_LAN_RESULT=CANCELLED；未修改实例或内网状态")
                return 0
            with runtime.lifecycle_lock() if manage_runtime else nullcontext():
                upgrade.verify_confirmed_source(release, args.source_dir)
                execute(plan, release, source, baseline, session, log, ui, verify=args.verify,
                        install_manager=manage_runtime)
            log("TGCLOUD_LAN_RESULT=BASE_LAN_OK；WebDAV 与真实转存以独立验收为准")
        return 0
    except KeyboardInterrupt:
        log("TGCLOUD_LAN_RESULT=INTERRUPTED；核对原安装器回退与入口状态，不假定已恢复")
        return 130
    except (ValueError, RuntimeError, TimeoutError, OSError) as exc:
        log("TGCLOUD_LAN_RESULT=FAILED：" + str(exc))
        return 1
    except Exception:  # noqa: BLE001 - never expose configuration in a terminal traceback
        log("TGCLOUD_LAN_RESULT=FAILED：检查/操作异常；未输出配置，请核对实例后重试")
        return 1
    finally:
        planner.close()
        if ui is not None:
            ui.close()
