"""Bounded, read-only account-scope discovery; never infer a root from TARGET_PATH.

CloudDrive2's webdav_config.json is read only in the verified /Config bind.
Encrypted/unknown account layouts require one manual confirmation. Only verified
CloudDrive2 profiles may reach account configuration reads.
"""
from __future__ import annotations

import json
import os
import stat
from dataclasses import dataclass
from pathlib import Path

from tgcloud.addons.external_webdav import config
from tgcloud.deployment.profiles import require_product

LIMIT = 1024 * 1024


@dataclass(frozen=True)
class Root:
    path: str = ""
    source: str = ""
    reason: str = ""


def unique_object(pairs):
    result = {}
    for name, value in pairs:
        if name in result:
            raise ValueError("网关目录信息包含重复字段。")
        result[name] = value
    return result


def clouddrive2_root(instance) -> str:
    directory = Path(instance.directory) / "clouddrive/config"
    path = directory / "webdav_config.json"
    if path.resolve() != path or not path.is_file():
        raise ValueError("未找到可安全读取的 CloudDrive2 WebDAV 用户配置。")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0))
    with os.fdopen(descriptor, "rb") as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_size > LIMIT
                or (os.name == "posix" and (info.st_uid != 0 or info.st_mode & 0o022))):
            raise ValueError("CloudDrive2 WebDAV 配置归属、权限或大小无法核实。")
        content = stream.read(LIMIT + 1)
    if len(content) > LIMIT:
        raise ValueError("CloudDrive2 WebDAV 配置过大。")
    item = json.loads(content, object_pairs_hook=unique_object)
    if not isinstance(item, dict) or item.get("enabled") is not True:
        raise ValueError("CloudDrive2 WebDAV 配置格式未识别或服务未启用。")
    users = item.get("dav_users")
    if not isinstance(users, list) or len(users) > 4096:
        raise ValueError("CloudDrive2 WebDAV 用户格式未识别。")
    username = config.decode(instance.config, "USERNAME")
    matches = [user for user in users if isinstance(user, dict) and user.get("user_name") == username]
    if len(matches) != 1 or matches[0].get("enabled") is not True:
        raise ValueError("无法从 CloudDrive2 配置核实 Bot 用户；加密账号或未识别格式需首次补充目录。")
    return config.logical_path(matches[0].get("root_path"), allow_root=True)






def resolve(instance, product, previous=None) -> Root:
    require_product(product)
    try:
        root = clouddrive2_root(instance)
        return Root(root, "网关账号配置")
    except (ValueError, TypeError, OSError):
        # Raw backend JSON, error messages, token and configuration are private.
        # A previous confirmation is reusable only for this exact binding.
        if (previous and previous["instance_dir"] == str(instance.directory)
                and previous["binding"] == config.binding(instance.config)
                and previous["backend_image"] == instance.gateway_image):
            return Root(config.logical_path(previous["bot_root"], allow_root=True), "上次确认的账号根目录")
        return Root(reason="未能自动核实 Bot 账号根目录，请在管理页核对后首次补充一次。")


def management_address(instance, product, state) -> str:
    require_product(product)
    from tgcloud.deployment.proxy.state import validate_domain
    route = state.get("domains", {}).get(product.key)
    if route:
        return "https://" + validate_domain(route["domain"])
    from tgcloud.deployment.proxy import lan
    saved = lan.load_state(product)
    if saved and saved["install_dir"] == str(instance.directory):
        return f"http://{lan.validate_ip(saved['bind_ip'])}:{product.management_port}"
    return "未登记管理地址，请使用你原有的网关管理入口"
