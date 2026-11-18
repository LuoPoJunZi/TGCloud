from __future__ import annotations

from pathlib import Path

from tgcloud.deployment import instances, releases


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


def record_release(directory: Path, release: releases.Release) -> None:
    """Activate the candidate identity only after successful installation."""
    from tgcloud.deployment.local_source import verify_installed, write_marker
    value = verify_installed(directory, expected_version=release.version, allow_pending=True)
    if value["source_sha256"] != release.digest:
        raise ValueError("安装来源与预检快照不一致")
    value["state"] = "ready"
    write_marker(directory, value)
