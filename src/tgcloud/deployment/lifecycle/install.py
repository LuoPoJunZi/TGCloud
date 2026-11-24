from __future__ import annotations

import hashlib
import json
import shlex
import tempfile
from contextlib import contextmanager
from dataclasses import dataclass, field, replace
from pathlib import Path
from typing import TYPE_CHECKING

from tgcloud.cli import host_cli, terminal
from tgcloud.cli.presentation import InstallerLog, OperationReport
from tgcloud.deployment import (
    architecture,
    configuration,
    instances,
    local_source,
    payload,
    preflight,
    releases,
    runtime,
)
from tgcloud.deployment.host import LocalSession, SafeLog, edition_lock
from tgcloud.deployment.lifecycle import upgrade
from tgcloud.deployment.ops import diagnostics, readiness
from tgcloud.deployment.proxy.manager import (
    DomainProxyManager,
    validate_domain,
    validate_email,
)
from tgcloud.deployment.proxy.state import (
    dns_diagnostic,
    nat_public_ipv4_candidate,
    public_endpoint_error,
)

if TYPE_CHECKING:
    from tgcloud.deployment.lifecycle.lan import Plan as LanPlan


@dataclass
class Plan:
    instance: instances.Instance
    action: str
    manager: DomainProxyManager
    config: dict[str, str] = field(default_factory=dict, repr=False)
    domain: str = ""
    email: str = ""
    configure_https: bool = False
    feedback: OperationReport | None = field(default=None, repr=False)


def native_summary(feedback: OperationReport, directory: Path) -> bool:
    if feedback.acceptance_tool is None:
        return False
    request = {
        "action": feedback.action, "mode": feedback.mode, "target": feedback.target,
        "completed": feedback.completed, "failed": bool(feedback.failure_stage),
        "interrupted": feedback.interrupted, "checks": feedback.checks,
    }
    try:
        raw = host_cli.checked_output("install-acceptance", "summary", str(directory), input_text=json.dumps(request),
                                      executable=feedback.acceptance_tool)
        if raw is None:
            return False
        result = json.loads(raw)
        if result["schema"] != 1 or result["outcome"] not in ("completed", "failed", "interrupted", "incomplete"):
            return False
        checks, observed = result["checks"], result["readiness"]
        if (not isinstance(checks, dict) or any(value not in ("ok", "failed", "unknown", "not_run", "na") for value in checks.values())
                or observed is not None and (not isinstance(observed, dict) or observed.keys() != readiness.LABELS.keys()
                                             or any(value not in readiness.STATES for value in observed.values()))):
            return False
        summary = result["outcome"], checks, result["actual_version"], observed, result["log_rotation"]
    except (KeyError, TypeError, ValueError, OSError, RuntimeError, TimeoutError):
        return False  # Safe to retry read-only observations using the existing Python observer.
    feedback.summary_outcome, feedback.checks, feedback.actual_version, feedback.readiness, feedback.log_rotation = summary
    return True


@contextmanager
def execution_feedback(plan, release, session, log, *, mode, source: Path | None = None):
    instance = plan.instance
    target = release.version if plan.action in {"install", "upgrade"} else instance.version
    feedback = OperationReport(instance.product.key, "TGCloud", instance.version, target, plan.action, mode)
    feedback.checks.update(base="not_run", config="unknown" if instance.present else "na",
                           backup="unknown" if plan.action == "upgrade" else "na", webdav="not_run")
    plan.feedback = feedback
    previous = log.operation if isinstance(log, InstallerLog) else None
    if isinstance(log, InstallerLog):
        log.operation = feedback
    try:
        if source is not None:
            feedback.acceptance_tool = host_cli.deployment_tool("install-acceptance", source)
        yield feedback
        feedback.completed = True
    except (KeyboardInterrupt, EOFError):
        feedback.interrupted = True
        feedback.failure_stage = feedback.stage
        raise
    except Exception:
        feedback.failure_stage = feedback.stage
        key = {"基础安装与升级": "base", "管理入口检查": "entry", "WebDAV 写入验收": "webdav"}.get(feedback.stage)
        if key:
            feedback.checks[key] = "failed"
        raise
    finally:
        try:
            if not feedback.interrupted and not native_summary(feedback, instance.directory) and feedback.checks.get("entry") == "ok":
                feedback.readiness = readiness.inspect(replace(instance, version=target), session)
                feedback.log_rotation = diagnostics.inspect_container_logs(instance, session)
        except (KeyboardInterrupt, EOFError):
            feedback.interrupted = True
            feedback.failure_stage = "业务就绪观察读取"
            raise
        finally:
            if isinstance(log, InstallerLog):
                log.operation = previous
                feedback.cli = log.cli
            try:
                feedback.render(log)
            except OSError:
                pass  # A closed terminal must not replace an installation's result.


def make_plan(
    instance: instances.Instance, release: releases.Release, source: Path, old_source: Path | None,
    session: LocalSession, log: SafeLog, ui: terminal.Terminal | None, *, configure_https: bool = False,
    proxy_options: dict | None = None,
) -> Plan:
    action = upgrade.prepare_plan(instance, release, source, old_source, session)
    manager = DomainProxyManager(
        session, instance.product, {"vps_host": "127.0.0.1", "install_dir": str(instance.directory)}, log,
        confirm_public_ipv4=(lambda domain, environment: terminal.confirm_nat_public_ipv4(ui, domain, environment, log))
        if ui is not None else None,
    )
    manager.installer_source = source
    manager.prepare()
    state = manager.read_state()
    report = manager.status(state)
    log("TGCloud HTTPS：" + report.message)
    plan = Plan(instance, action, manager, dict(instance.config), report.domain)
    needs_https = action == "install" or configure_https
    if action == "upgrade" and report.state != "healthy" and not configure_https:
        raise ValueError("已有 HTTPS 未通过；先用 --configure-https 显式修复，不在升级中隐式变更域名")
    if configure_https and action == "newer":
        raise ValueError("当前实例新于 latest；请使用匹配版本的源码维护 HTTPS，不使用旧 EXE 降级")
    if needs_https and report.state != "healthy":
        plan.configure_https = True
        if ui is not None:
            # Reuse an existing route, never offer an implicit domain switch.
            plan.domain = report.domain or ui.ask("管理页域名（A/AAAA 直指本机；Cloudflare 仅 DNS）", validate=validate_domain)
            old_route = state["domains"].get(instance.product.key, {})
            plan.email = old_route.get("email", "") or ui.ask("Let's Encrypt 邮箱（可留空）", validate=validate_email)
            from tgcloud.cli.proxy import select_entry
            select_entry(manager, ui, plan.domain, proxy_options)
            if any(item["domain"] == plan.domain for key, item in state["domains"].items() if key != instance.product.key):
                raise ValueError("该域名已由另一个实例使用")
            if instance.present:
                detection = manager.detect(plan.domain)
                if detection.state != "detected":
                    raise ValueError(detection.message)
            else:
                environment = manager.probe(plan.domain)
                if nat_public_ipv4_candidate(environment):
                    environment = manager.confirm_nat(plan.domain, environment)
                error = public_endpoint_error(environment, "127.0.0.1")
                if error:
                    raise ValueError("首次安装 HTTPS 前置检查未通过：" + error + "\n"
                                     + dns_diagnostic(environment, "127.0.0.1"))
    if action == "install" and ui is not None:
        log("\n── 磁盘与资源 ──")
        storage = preflight.choose_storage(instance, session, log, ui)
        log("\n── Telegram 与 WebDAV ──")
        plan.config = terminal.collect_config(ui, instance.product, storage=storage)
        log.register(plan.config)
    if action in {"install", "upgrade"} and (ui is not None or instance.present):
        preflight.resource_check(instance, plan.config, session, log)
    if action == "upgrade":
        preflight.check_install_docker(session, "upgrade")
    return plan


def apply_base(plan: Plan | LanPlan, release: releases.Release, source: Path, session: LocalSession, log: SafeLog) -> None:
    instance = plan.instance
    if plan.action not in {"install", "upgrade"}:
        return
    preflight.check_install_docker(session, "base")
    with tempfile.TemporaryDirectory(prefix="tgcloud-cli-payload-") as private:
        root = Path(private).resolve()
        payload_dir = root / "payload"
        native_payload = payload.build_install_payload(source, instance.product, payload_dir, release.digest)
        if isinstance(plan, Plan):
            # Only VPS plans can attach their payload to an existing HTTPS proxy.
            from tgcloud.deployment.proxy.external_entry import apply_payload_network
            apply_payload_network(plan.manager, payload_dir / "docker-compose.yml")
        marked = native_payload and local_source.native_source("mark", str(payload_dir), release.version, release.digest)
        if not marked:
            local_source.write_marker(payload_dir, local_source.marker_for(payload_dir, release))
        candidate = root / "config.env"
        preflight.write_candidate_config(candidate, plan.action, plan.config)
        helper = host_cli.deployment_tool("install-transaction", source)
        use_go = helper is not None
        if helper is not None:
            config_digest = hashlib.sha256(instance.raw_config).hexdigest() if instance.present else ""
            command = "exec " + shlex.join((str(helper), "install-transaction", str(instance.directory),
                                           str(payload_dir), str(candidate), plan.action, release.version,
                                           release.digest, config_digest))
        else:
            # Track the installer PID so it can still read the payload during recovery.
            command = ("INSTALL_DIR=" + shlex.quote(str(instance.directory)) + " exec bash "
                       + shlex.quote(str(payload_dir / "remote_install.sh")) + " "
                       + shlex.quote(str(payload_dir)) + " " + shlex.quote(str(candidate)))
        recovery = {"stream_recovery": True} if use_go else {}
        code, output = session.run(command, stream=log, timeout=2400, shutdown_grace=600, **recovery)
        if plan.feedback is not None:
            plan.feedback.observe_base(code, output)
        if code or "TGCLOUD_RESULT=SUCCESS" not in output.splitlines() or "TGCLOUD_BOT_HEALTH=healthy" not in output.splitlines():
            raise RuntimeError("基础安装/升级失败；请核对现有安装器的回退结果和备份，不假定已恢复")
    if use_go:
        if instance.present and configuration.regular_private(instance.directory / ".env") != instance.raw_config:
            if plan.feedback is not None:
                plan.feedback.checks["config"] = "failed"
            raise RuntimeError("升级后 .env 字节发生变化，停止后续操作；请核对备份")
        if instance.present and plan.feedback is not None:
            plan.feedback.checks["config"] = "ok"
        return
    architecture.check_container(session, instance.product.bot_container, require_running=True)
    architecture.check_container(session, instance.product.storage_container, require_running=True)
    if instance.present and configuration.regular_private(instance.directory / ".env") != instance.raw_config:
        if plan.feedback is not None:
            plan.feedback.checks["config"] = "failed"
        raise RuntimeError("升级后 .env 字节发生变化，停止后续操作；请核对备份")
    if instance.present and plan.feedback is not None:
        plan.feedback.checks["config"] = "ok"
    upgrade.record_release(instance.directory, release)


def verify_destination(plan: Plan | LanPlan, session: LocalSession, log: SafeLog, ui: terminal.Terminal) -> bool:
    if not ui.confirm("运行 WebDAV 写入验收？会创建并清理随机测试文件；请先完成云挂载/专用 WebDAV 用户配置"):
        log("TGCLOUD_CLI_WEBDAV=NOT_RUN；未把基础健康状态当作转存验收通过")
        return False
    feedback = plan.feedback
    directory = str(plan.instance.directory)
    if feedback is not None and feedback.acceptance_tool is not None:
        command = "exec " + shlex.join((str(feedback.acceptance_tool), "install-acceptance", "webdav",
                                        directory, feedback.target, "--yes"))
        code, output = session.run(command, stream=log, timeout=680, stream_recovery=True)
    else:
        directory = shlex.quote(directory)
        code, output = session.run("cd " + directory + " && INSTALL_DIR=" + directory + " bash ./manage.sh verify", stream=log, timeout=600)
    markers = output.splitlines()
    if code or "TGCLOUD_DESTINATION=OK" not in markers or any(line.startswith("TGCLOUD_DESTINATION=FAILED") for line in markers):
        raise RuntimeError("WebDAV 验收未通过；不自动重复验收或修改凭据，请查看脱敏日志")
    log("TGCLOUD_CLI_WEBDAV=OK；仍请在云存储官方客户端确认最终文件")
    return True


def sync_addons_for_plan(
    feedback: OperationReport, source: Path, release: releases.Release, log: SafeLog, instance_dir: Path,
) -> None:
    """Keep base upgrade and entry verification independent from optional add-on sync."""
    previous_stage = feedback.stage
    feedback.stage = "附加工具同步"
    try:
        if runtime.sync_installed_addons(source, release, log, instance_dir=instance_dir):
            feedback.addon_synced = release.version
    except (ValueError, RuntimeError, OSError) as exc:
        reason = str(exc) if isinstance(exc, (ValueError, RuntimeError)) else "文件系统或权限异常"
        feedback.addon_sync_error = reason
        log("[待核对] 外部 WebDAV 附加工具同步未完成：" + reason + "；基础服务已完成，继续检查管理入口，稍后可在 tgcloud 菜单 2.3 核对。")
    feedback.stage = previous_stage


def execute_plan(
    plan: Plan, release: releases.Release, source: Path, old_source: Path | None,
    session: LocalSession, log: SafeLog, ui: terminal.Terminal, *, verify: bool = False, install_manager: bool = False,
) -> None:
    instance = plan.instance
    with edition_lock(Path("/opt/tgcloud-cli-locks") / (instance.product.key + ".lock")), execution_feedback(
        plan, release, session, log, mode="https", source=source
    ) as feedback:
        upgrade.recheck_plan(instance, plan.action, release, source, old_source, session, log)
        plan.manager.recheck_install_entry(plan.domain, present=instance.present, configure=plan.configure_https)
        if plan.action in {"install", "upgrade"}:
            preflight.resource_check(instance, plan.config, session, log)
        if install_manager:
            log.cli = runtime.install_release(source, release, log, sync_addons=False, instance_dir=instance.directory)
            feedback.acceptance_tool = host_cli.deployment_tool("install-acceptance", source)
        feedback.stage = "基础安装与升级"
        apply_base(plan, release, source, session, log)
        if plan.action in {"install", "upgrade"}:
            feedback.checks["base"] = "ok"
        if install_manager and isinstance(log.cli, dict) and log.cli.get("commands") == {"version": "ok", "listen": "ok"}:
            sync_addons_for_plan(feedback, source, release, log, instance.directory)
        feedback.stage = "管理入口检查"
        if plan.configure_https:
            plan.manager.configure(plan.domain, plan.email)
        report = plan.manager.status()
        log(report.message)
        if report.state != "healthy":
            raise RuntimeError("基础服务可能已安装，但 HTTPS 尚未通过；用 --configure-https 显式续做，不要重装/删除数据")
        feedback.checks["entry"] = "ok"
        feedback.stage = "安装后检查"
        log("TGCLOUD_CLI_BASE_HTTPS=OK；公网访问仍需外部网络检查")
        if plan.action == "install" or plan.configure_https:
            feedback.stage = "后台账号设置"
            log("请在 https://" + report.domain + " 配置自己的云存储，再建立专用 WebDAV 用户。")
            if plan.action == "install":
                terminal.show_setup_credentials(ui, instance.product, plan.config, log)
            else:
                terminal.show_credentials(ui, instance.product, plan.config, log)
        if verify or plan.action == "install":
            feedback.stage = "WebDAV 写入验收"
            verified = verify_destination(plan, session, log, ui)
            feedback.checks["webdav"] = "ok" if verified is True else "not_run"
