"""LAN installation and maintenance workflow."""

from __future__ import annotations

import argparse
import re
import sys
from contextlib import nullcontext
from dataclasses import dataclass, field
from pathlib import Path

from tgcloud.cli import terminal
from tgcloud.cli.presentation import InstallerLog, OperationReport
from tgcloud.deployment import architecture, instances, local_source, payload, preflight
from tgcloud.deployment.host import LocalSession, SafeLog, edition_lock
from tgcloud.deployment.lifecycle import install, upgrade
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.deployment.proxy import lan


def validate_environment(session: LocalSession, *, log=None) -> None:
    preflight.validate_host(log=log)
    # Do not install onto a remote daemon, or inherit arbitrary Compose inputs.
    architecture.check_environment()
    code, _ = session.run("command -v ip >/dev/null 2>&1", timeout=10)
    if code:
        raise ValueError("缺少 iproute2；请先人工安装，不在信息收集前安装软件")
    info = architecture.check_docker(session, allow_missing=True)
    if info is None:
        return
    version = info.get("version", "")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:[-+].*)?", version) or int(version.split(".")[0]) < 28:
        raise ValueError("内网模式要求 Docker Engine 28+，避免旧版回环端口被同网段直接访问；请人工升级，不重启其他容器")


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


def make_plan(instance, release, source, baseline, session, log, ui=None, *, address="") -> Plan:
    product = instance.product
    lan.reject_https_route(product)
    saved = lan.load_state(product)
    if instance.present:
        if saved is None:
            raise ValueError("已有实例不是本内网脚本创建；不转换 VPS/HTTPS 或接管已有网关")
        if baseline is None:
            raise ValueError("缺少原版本正式源码基线")
        upgrade.assert_unmodified(instance, baseline)
    action = upgrade.action_for(instance, release)
    if action in {"install", "upgrade"}:
        architecture.require_payload(source)
        payload.payload_sources(source, product)
        instances.check_foreign_gateway(product, session)
        if action == "upgrade" and payload.gateway_image(source, product) != instance.gateway_image:
            raise ValueError("云网关镜像变化需要人工迁移验收；不自动升级数据库")
    if saved:
        if address and address != saved["bind_ip"]:
            raise ValueError("不隐式切换已保存的内网地址")
        address = saved["bind_ip"]
    elif not address:
        if ui is None:
            raise ValueError("首次只读预检请显式提供 --lan-ip；不会自动猜测地址")
        address = ui.ask("运行脚本的 Linux 内网 IPv4（虚拟机填虚拟机地址）", validate=lan.validate_ip)
    address = lan.validate_local_ip(address, session)
    inventory = instances.docker_inventory(session)
    lan.check_published_ports(product, session, inventory)
    entry = lan.inspect_lan(product, session, inventory)
    if entry and saved is None:
        raise ValueError("同名内网容器存在但缺少受管记录；不接管")
    if entry and not instance.present:
        raise ValueError("只有内网入口、缺少完整 Bot/网关；停止，不重装")
    running = bool(entry and entry.get("running"))
    lan.check_port(product, address, running)
    if not instance.present:
        # The original installer checks the fixed port across all listeners.
        # Detect a loopback conflict before collecting any credentials.
        lan.check_port(product, "127.0.0.1", False)
    plan = Plan(instance, action, address, saved, dict(instance.config), running)
    if instance.present:
        lan.check_backend(instance, session, log)
    if running:
        try:
            lan.check_entry(product, address)
            plan.entry_healthy = True
        except (ValueError, OSError):
            log("已有内网入口检查未通过；预览后需确认才尝试恢复入口，不重装 Bot。")
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


def execute(plan: Plan, release, source, baseline, session, log, ui, *, verify=False) -> None:
    instance = plan.instance
    product = instance.product
    with edition_lock(Path("/opt/tgcloud-cli-locks") / (product.key + ".lock")), install.execution_feedback(
        plan, release, session, log, mode="lan"
    ) as feedback:
        current = instances.discover(product, instances.docker_inventory(session), session, log, install_dir=product.install_dir)
        if (current.present != instance.present or current.raw_config != instance.raw_config
                or current.version != instance.version or current.gateway_image != instance.gateway_image or current.source_digest != instance.source_digest):
            raise ValueError("确认前后实例发生变化；停止，重新预检")
        if lan.load_state(product) != plan.saved:
            raise ValueError("确认前后内网配置发生变化；停止")
        lan.reject_https_route(product)
        lan.validate_local_ip(plan.address, session)
        inventory = instances.docker_inventory(session)
        lan.check_published_ports(product, session, inventory)
        entry = lan.inspect_lan(product, session, inventory)
        lan.check_port(product, plan.address, bool(entry and entry.get("running")))
        if current.present:
            upgrade.assert_unmodified(current, baseline)
            lan.check_backend(current, session, log)
        else:
            lan.check_port(product, "127.0.0.1", False)
        instances.check_foreign_gateway(product, session)
        if plan.action in {"install", "upgrade"}:
            preflight.resource_check(instance, plan.config, session, log)
        # Persist the explicit intent before base installation. If base succeeds
        # but Nginx fails, rerunning resumes the LAN entry, not a fresh reinstall.
        lan.save_state(product, plan.address)
        feedback.stage = "基础安装与升级"
        install.apply_base(plan, release, source, session, log)
        if plan.action in {"install", "upgrade"}:
            feedback.checks["base"] = "ok"
        feedback.stage = "管理入口检查"
        validate_environment(session)  # also validate a newly installed daemon
        current = instances.discover(product, instances.docker_inventory(session), session, log, install_dir=product.install_dir)
        lan.check_backend(current, session, log)
        entry_healthy = False
        if entry and entry.get("running"):
            try:
                lan.check_entry(product, plan.address)
                entry_healthy = True
            except (ValueError, OSError):
                pass  # Repair only the already validated, managed LAN entry.
        if not entry_healthy:
            architecture.ensure_image(session, lan.NGINX_IMAGE)
            for arguments in ("config --quiet", "pull lan", "run --rm --no-deps lan -t", "up -d lan"):
                code, _ = session.run(lan.compose_command(product, arguments), stream=log, timeout=180)
                if code:
                    raise RuntimeError("内网入口未就绪；基础数据和模式记录保留，请重新检查后续做，不要删除实例")
        entry = lan.inspect_lan(product, session, instances.docker_inventory(session))
        if not entry or not entry.get("running"):
            raise RuntimeError("内网入口容器未运行；不冒充成功")
        architecture.check_container(session, lan.container_name(product), require_running=True)
        lan.wait_for_entry(product, plan.address)
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
    try:
        if args.check and (args.verify or args.show_credentials):
            raise ValueError("--check 不能与写入验收或凭据显示组合")
        if args.show_credentials and args.verify:
            raise ValueError("凭据显示必须单独使用")
        session = LocalSession(log)
        validate_environment(session, log=log)
        product = PRODUCTS[args.edition]
        instance = instances.discover(product, instances.docker_inventory(session), session, log, install_dir=product.install_dir)
        saved = lan.load_state(product)
        if args.show_credentials:
            lan.reject_https_route(product)
            if not saved or not instance.present:
                raise ValueError("没有本脚本创建的完整内网实例；不生成凭据")
            if ui is None:
                ui = terminal.Terminal()
            terminal.show_credentials(ui, product, instance.config, log)
            log("TGCLOUD_LAN_RESULT=NO_CHANGE；未修改配置或密码")
            return 0
        with local_source.snapshot(args.source_dir) as release:
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
            from tgcloud.deployment import runtime
            manage_runtime = friendly or runtime.COMMAND.exists()
            if plan.action == "newer" or (plan.action == "current" and plan.entry_healthy and not args.verify):
                if friendly:
                    runtime.check_destination()
                    if ui.confirm("实例无需安装/升级；是否仅安装本地管理命令 tgcloud？不重启容器"):
                        with runtime.lifecycle_lock():
                            current = instances.discover(product, instances.docker_inventory(session), session, log, install_dir=product.install_dir)
                            if not current.present or current.raw_config != instance.raw_config or current.version != instance.version:
                                raise ValueError("确认期间实例已移除或变化；不重新创建管理命令")
                            log.cli = runtime.install_release(source, release, log)
                log("TGCLOUD_LAN_RESULT=NO_CHANGE；不重建、不降级")
                return 0
            if manage_runtime:
                runtime.check_destination()
            if friendly:
                log("将安装本地管理命令 tgcloud；仅用于可信局域网，不自动切换为公网 HTTPS。")
            if not ui.confirm("确认上述内网 HTTP 计划？仅用于可信局域网，禁止公网转发；所选 Bot 可能短暂停止"):
                log("TGCLOUD_LAN_RESULT=CANCELLED；未修改实例或内网状态")
                return 0
            with runtime.lifecycle_lock() if manage_runtime else nullcontext():
                if manage_runtime:
                    log.cli = runtime.install_release(source, release, log)
                execute(plan, release, source, baseline, session, log, ui, verify=args.verify)
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
        if ui is not None:
            ui.close()
