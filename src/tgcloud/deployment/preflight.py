from __future__ import annotations

import json
import os
import re
import shutil
import struct
from dataclasses import asdict
from pathlib import Path

from tgcloud.cli import host_cli, terminal
from tgcloud.deployment import architecture, configuration, instances
from tgcloud.deployment.host import LocalSession, SafeLog
from tgcloud.deployment.profiles import ProductProfile
from tgcloud.deployment.resources import (
    GIB,
    StorageAdvice,
    StorageAssessment,
    StoragePlan,
    assess_storage_choice,
    build_probe_command,
    parse_probe_output,
    recommend_storage,
)


def validate_host(*, log=None) -> None:
    host = architecture.detect_host()
    if os.geteuid() != 0:
        raise ValueError("请在 Linux VPS 以 root 运行；Windows 用户可通过 SSH 登录后运行脚本")
    if shutil.which("bash") is None:
        raise ValueError("缺少 Bash")
    architecture.check_environment()
    if log:
        host.describe(log)


def native_preflight(*args: str, input_data=None) -> dict | None:
    if not host_cli.supports("installer-preflight"):
        return None
    output = host_cli.checked_output("installer-preflight", *args,
                                     input_text=None if input_data is None else json.dumps(input_data, ensure_ascii=False))
    if output is None:
        return None
    result = json.loads(output)
    if not isinstance(result, dict) or not result:
        raise ValueError("Go 返回的一键预检结果无法核实")
    return result


def validate_install_host(*, log=None, lan: bool = False) -> bool:
    result = native_preflight("host", "lan" if lan else "public")
    if result is None:
        validate_host(log=log)
        return False
    if struct.calcsize("P") != 8:
        raise ValueError("需要 64 位 Linux 用户态与 Python；不支持 32 位用户态")
    host = architecture.NativeHost(**result)
    if log:
        host.describe(log)
    return True


def check_install_docker(session: LocalSession, mode: str) -> None:
    if native_preflight("docker", mode) is not None:
        return
    if mode == "upgrade":
        code, _ = session.run("docker compose version >/dev/null 2>&1", timeout=15)
        if code:
            raise ValueError("升级要求现有 Docker Compose 可用；不在升级中自动安装依赖")
        return
    info = architecture.check_standalone_docker(session, allow_missing=True)
    if mode == "lan" and info is not None:
        version = info.get("version", "")
        if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:[-+].*)?", version) or int(version.split(".")[0]) < 28:
            raise ValueError("内网模式要求 Docker Engine 28+，避免旧版回环端口被同网段直接访问；请人工升级，不重启其他容器")


def write_candidate_config(path: Path, action: str, config: dict[str, str]) -> None:
    entries = list(config.items()) if action == "install" else []
    if native_preflight("write-config", str(path), action, input_data=entries) is not None:
        return
    content = ("TGCLOUD_REDEPLOY_APPLY_CONFIG=false\n" if action == "upgrade" else
               "".join(key + "=" + value + "\n" for key, value in entries))
    with path.open("x", encoding="utf-8") as writer:
        path.chmod(0o600)
        writer.write(content)


def probe_resources(instance: instances.Instance, session: LocalSession):
    install_dir = str(instance.directory)
    backup_dir = instance.product.backup_dir
    try:
        result = host_cli.invoke("resources", install_dir, backup_dir)
        if result is not None and result[0] == 0:
            return parse_probe_output(result[1])
    except (OSError, TimeoutError, ValueError):
        # Incomplete Go observations never feed the recommendation or write guard.
        pass
    code, output = session.run(
        build_probe_command(install_dir, backup_dir), timeout=45,
    )
    if code:
        raise ValueError("资源预检未完整执行")
    return parse_probe_output(output)


def storage_options(product: ProductProfile) -> dict:
    return {
        "managed_clouddrive": True, "managed_storage": True,
        "requires_fuse": product.requires_fuse,
        "minimum_memory_mb": 1800,
    }


def storage_advice(resources, product: ProductProfile) -> StorageAdvice:
    result = native_preflight("storage", "recommend",
                              input_data={"resources": asdict(resources), "requires_fuse": product.requires_fuse})
    if result is None:
        return recommend_storage(resources, **storage_options(product))
    return StorageAdvice(result["performance_profile"], result["preferred_key"],
                         StoragePlan(**result["balanced"]), StoragePlan(**result["stream_first"]))


def storage_assessment(resources, product: ProductProfile, budget: float, reserve: float) -> StorageAssessment:
    result = native_preflight("storage", "assess", input_data={
        "resources": asdict(resources), "requires_fuse": product.requires_fuse,
        "budget_gb": budget, "reserve_gb": reserve,
    })
    if result is None:
        return assess_storage_choice(resources, budget_gb=budget, reserve_gb=reserve, **storage_options(product))
    return StorageAssessment(**result)


def choose_storage(instance: instances.Instance, session: LocalSession, log: SafeLog, ui: terminal.Terminal) -> tuple[str, str]:
    """First-install advice only. Never rewrite an existing instance's settings."""
    if instance.present:
        raise ValueError("已有实例保留磁盘配置，不进入首次安装推荐")
    resources = probe_resources(instance, session)
    advice = storage_advice(resources, instance.product)
    log(f"安装目录所在磁盘：总计 {resources.storage_total_bytes / GIB:.1f}GB；可用 {resources.storage_available_bytes / GIB:.1f}GB")
    log(f"CPU {resources.cpu_cores} 核；内存 {resources.memory_total_bytes / GIB:.1f}GB；可用 {resources.memory_available_bytes / GIB:.1f}GB")
    if resources.docker_root_detected and not resources.docker_same_filesystem:
        log(f"Docker 使用独立文件系统，可用 {resources.docker_storage_available_bytes / GIB:.1f}GB；与安装目录分别校验")
    log("性能档位：" + advice.performance_profile)
    log("以下预算与保留空间由本次资源探测计算，执行前会再次校验。")
    log("任务预算是暂存额度，不是磁盘分区，也不限制网关全部缓存。")
    plans = {"1": advice.stream_first, "2": advice.balanced}
    for key, plan in plans.items():
        state = "（推荐）" if plan.key == advice.preferred_key else ""
        if not plan.safe:
            state = "（不可用：" + plan.reason + "）"
        log(f"[{key}] {plan.label}：任务预算 {plan.budget_gb}GB，最少保留 {plan.reserve_gb}GB{state}")
    log("[3] 手动设置（仍需通过容量、内存、FUSE 和 Docker 文件系统检查）")
    default = next((key for key, plan in plans.items() if plan.key == advice.preferred_key), "3")

    def choice(value: str) -> str:
        if value not in {"1", "2", "3"}:
            raise ValueError("请选择 1、2 或 3")
        return value

    while True:
        selected = ui.ask("存储方案", default=default, validate=choice)
        if selected == "3":
            suggested = advice.preferred
            budget = ui.ask("本地任务预算 GB", default=str(suggested.budget_gb) if suggested else "", validate=configuration.size_gb)
            reserve = ui.ask("磁盘最少保留 GB", default=str(suggested.reserve_gb) if suggested else "", validate=configuration.size_gb)
        else:
            plan = plans[selected]
            if not plan.safe:
                log("该方案不可用：" + plan.reason)
                if not ui.confirm("是否重新选择存储方案？"):
                    raise ValueError("已停止安装，未写入配置")
                continue
            budget, reserve = str(plan.budget_gb), str(plan.reserve_gb)
        assessment = storage_assessment(resources, instance.product, float(budget), float(reserve))
        if float(reserve) >= 8 and assessment.safe:
            log(f"已选择任务预算 {budget}GB / 最少保留 {reserve}GB；执行前将重新探测并校验")
            return budget, reserve
        log("存储配置不可用：" + (assessment.reason if float(reserve) >= 8 else "最少保留空间不能低于 8GB"))
        if not ui.confirm("是否重新选择存储方案？"):
            raise ValueError("已停止安装，未写入配置")


def resource_check(instance: instances.Instance, config: dict[str, str], session: LocalSession, log: SafeLog) -> None:
    resources = probe_resources(instance, session)
    assessment = storage_assessment(
        resources, instance.product, float(configuration.size_gb(config.get("LOCAL_TEMP_BUDGET_GB", "20"))),
        float(configuration.size_gb(config.get("MIN_FREE_DISK_GB", "8"))),
    )
    log(assessment.reason)
    if not assessment.safe:
        raise ValueError("资源不足：" + assessment.reason + "；不会自动降低预算或修改已有配置")
