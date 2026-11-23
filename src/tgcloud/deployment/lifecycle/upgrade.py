from __future__ import annotations

import hashlib
import json
from pathlib import Path

from tgcloud.cli import host_cli
from tgcloud.deployment import (
    architecture,
    instances,
    local_source,
    payload,
    releases,
    runtime,
)


def assert_unmodified(instance: instances.Instance, old_source: Path) -> None:
    from tgcloud.deployment.local_source import verify_installed
    identity = verify_installed(instance.directory, expected_version=instance.version)
    if identity["source_sha256"] != instance.source_digest:
        raise ValueError("实例来源在预检后发生变化；停止覆盖")


def action_for(instance: instances.Instance, release: releases.Release) -> str:
    if not instance.present:
        return "install"
    current, target = releases.version_tuple(instance.version), releases.version_tuple(release.version)
    if current > target:
        return "newer"
    if current == target:
        return "current" if instance.source_digest == release.digest else "upgrade"
    if current[0] != target[0]:
        raise ValueError("跨主版本升级需要人工核对迁移说明")
    return "upgrade"


def native_plan(instance: instances.Instance, release, source: Path) -> str | None:
    digest = getattr(release, "digest", None)
    if not isinstance(digest, str):
        return None
    arguments = (str(instance.directory), "true" if instance.present else "false", instance.version,
                 instance.source_digest, hashlib.sha256(instance.raw_config).hexdigest() if instance.present else "",
                 instance.gateway_image, str(source), release.version, digest)
    output = host_cli.read_install_check("plan-install", *arguments)
    if output is None:
        return None
    try:
        action = json.loads(output)
        if action not in ("install", "upgrade", "current", "newer"):
            raise ValueError
    except (TypeError, ValueError):
        raise ValueError("Go 返回的安装计划无法核实") from None
    return action


def prepare_plan(instance: instances.Instance, release, source: Path, baseline: Path | None, session) -> str:
    selected = native_plan(instance, release, source)
    action = selected if selected is not None else action_for(instance, release)
    if action in {"install", "upgrade"}:
        architecture.require_payload(source)
        payload.payload_sources(source, instance.product)
    if instance.present:
        if baseline is None:
            raise ValueError("缺少原版本正式源码基线，无法安全升级")
        if selected is None:
            assert_unmodified(instance, baseline)
            if action == "upgrade" and payload.gateway_image(source, instance.product) != instance.gateway_image:
                raise ValueError("云网关镜像变化需要人工迁移验收；不自动升级数据库")
    if selected is None and action in {"install", "upgrade"}:
        instances.check_foreign_gateway(instance.product, session)
    return action


def recheck_plan(instance: instances.Instance, action: str, release, source: Path,
                 baseline: Path | None, session, log, *, install_dir: str | None = None) -> instances.Instance:
    selected = native_plan(instance, release, source)
    if selected is not None:
        if selected != action:
            raise ValueError("确认期间安装计划已变更，停止并重新预检")
        verify_snapshot(source, release)
        return instance
    current = instances.discover(instance.product, instances.docker_inventory(session), session, log,
                                 install_dir=install_dir or str(instance.directory))
    if (current.present != instance.present or current.raw_config != instance.raw_config
            or current.version != instance.version or current.gateway_image != instance.gateway_image
            or current.source_digest != instance.source_digest):
        raise ValueError("确认前后实例发生变化，配置或来源已变更；停止，重新预检")
    if current.present:
        assert_unmodified(current, baseline)
    if action in {"install", "upgrade"}:
        instances.check_foreign_gateway(instance.product, session)
    return current


def verify_snapshot(source: Path, release) -> None:
    local_source.verify_digest(source, release.digest)


def verify_confirmed_source(release: local_source.LocalSource, original: Path) -> None:
    verify_snapshot(release.root, release)
    verify_snapshot(original, release)
    if runtime.packaged_host_cli(release.root) != runtime.packaged_host_cli(original.absolute()):
        raise ValueError("确认期间部署源码中的 Go 工具已变更，停止并重新预检")


def record_release(directory: Path, release: releases.Release) -> None:
    """Activate the candidate identity only after successful installation."""
    from tgcloud.deployment.local_source import verify_installed, write_marker
    value = verify_installed(directory, expected_version=release.version, allow_pending=True)
    if value["source_sha256"] != release.digest:
        raise ValueError("安装来源与预检快照不一致")
    value["state"] = "ready"
    write_marker(directory, value)
