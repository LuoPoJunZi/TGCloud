"""Shared validation of Docker labels and bind mounts."""

COMPOSE = "com.docker.compose."
MANAGED = "com.tgcloud.managed"
ROLE = "com.tgcloud.role"


class UninstallError(ValueError):
    """Only static, credential-free diagnostics created by this module."""


def bind_map(item: dict) -> dict:
    result = {}
    for mount in item.get("mounts", []):
        if mount.get("Type") == "tmpfs":
            continue
        destination = mount.get("Destination")
        if mount.get("Type") != "bind" or destination in result:
            raise UninstallError("容器使用了未知挂载；停止，不清理命名卷或外部数据")
        result[destination] = mount.get("Source")
    return result


def require_labels(item: dict, expected: dict) -> None:
    labels = item.get("labels") or {}
    if any(labels.get(key) != value for key, value in expected.items()):
        raise UninstallError("同名资源归属不符；停止，不接管其他项目")
