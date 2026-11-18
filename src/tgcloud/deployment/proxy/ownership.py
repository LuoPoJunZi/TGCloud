"""Ownership of the shared Nginx and Certbot Compose project."""

from __future__ import annotations

import re

from tgcloud.deployment.ownership import (
    COMPOSE,
    MANAGED,
    ROLE,
    UninstallError,
    bind_map,
    require_labels,
)

from .certificates import PROXY_ROOT
from .rendering import PROXY_CERTBOT_CONTAINER, PROXY_NGINX_CONTAINER, certbot_bindings

PROXY_PROJECT = "tgcloud-proxy"
PROXY_NETWORK = PROXY_PROJECT + "_default"


class MissingCertbotStatusMount(UninstallError):
    """Ownership is verified and precisely the renewal-status bind is absent."""


def proxy_identity(name: str, item: dict, *, state: dict | None = None) -> None:
    labels = item.get("labels") or {}
    service = labels.get(COMPOSE + "service")
    expected_service = "nginx" if name == PROXY_NGINX_CONTAINER else "certbot"
    if (name not in {PROXY_NGINX_CONTAINER, PROXY_CERTBOT_CONTAINER}
            and (labels.get(COMPOSE + "oneoff") != "True" or item.get("running")
                 or not re.fullmatch(r"tgcloud-proxy-certbot-run-[a-z0-9]+", name))):
        raise UninstallError("存在未知或运行中的代理辅助容器；先完成维护，不强制删除")
    require_labels(item, {MANAGED: "true", ROLE: "reverse-proxy" if expected_service == "nginx" else "certificate-manager",
                          COMPOSE + "project": PROXY_PROJECT, COMPOSE + "service": expected_service,
                          COMPOSE + "project.working_dir": PROXY_ROOT,
                          COMPOSE + "project.config_files": PROXY_ROOT + "/docker-compose.yml"})
    expected_network = "host" if expected_service == "nginx" else PROXY_NETWORK
    if service != expected_service or item.get("network") != expected_network:
        raise UninstallError("共享代理容器身份不符")
    paths = {"/var/www/certbot": "/certbot/www", "/etc/letsencrypt": "/certbot/letsencrypt"}
    if expected_service == "nginx":
        paths.update({"/etc/nginx/nginx.conf": "/nginx/nginx.conf", "/etc/nginx/conf.d": "/nginx/conf.d"})
        if state and state.get("version") == 2:
            paths["/etc/nginx/external-auth"] = "/external-auth"
    expected = (certbot_bindings(PROXY_ROOT) if expected_service == "certbot"
                else {destination: PROXY_ROOT + suffix for destination, suffix in paths.items()})
    actual = bind_map(item)
    if actual != expected:
        # A missing renewal-status bind is configuration drift, not an unknown
        # user directory. Still refuse admission; only the confirmed HTTPS
        # repair flow may regenerate Compose and recreate the owned service.
        if expected_service == "certbot" and actual == {key: value for key, value in expected.items() if key != "/status"}:
            raise MissingCertbotStatusMount(
                "共享代理 Certbot 缺少续期状态挂载 /status；"
                "请先在 tgcloud → HTTPS 与管理 → 修复 HTTPS 中修复共享代理，再重试。"
                "仍需通过完整挂载校验，不自动修改容器。"
            )
        raise UninstallError("共享代理挂载含未知路径；停止")
