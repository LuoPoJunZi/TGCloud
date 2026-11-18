"""Synthetic Docker identities for managed HTTPS containers."""
from __future__ import annotations

from tgcloud.deployment.lifecycle import uninstall


def identifier(number):
    return f"{number:064x}"


def proxy_container(*, nginx=True, number=7):
    name = uninstall.PROXY_NGINX_CONTAINER if nginx else uninstall.PROXY_CERTBOT_CONTAINER
    paths = {"/var/www/certbot": "/certbot/www", "/etc/letsencrypt": "/certbot/letsencrypt"}
    if nginx:
        paths.update({"/etc/nginx/nginx.conf": "/nginx/nginx.conf", "/etc/nginx/conf.d": "/nginx/conf.d"})
    else:
        paths.update({"/var/lib/letsencrypt": "/certbot/lib", "/var/log/letsencrypt": "/certbot/log", "/status": "/certbot/status"})
    return {"id": identifier(number), "name": "/" + name, "running": True,
            "network": "host" if nginx else uninstall.PROXY_NETWORK,
            "labels": {uninstall.MANAGED: "true", uninstall.ROLE: "reverse-proxy" if nginx else "certificate-manager",
                       uninstall.COMPOSE + "project": uninstall.PROXY_PROJECT,
                       uninstall.COMPOSE + "service": "nginx" if nginx else "certbot",
                       uninstall.COMPOSE + "project.working_dir": uninstall.PROXY_ROOT,
                       uninstall.COMPOSE + "project.config_files": uninstall.PROXY_ROOT + "/docker-compose.yml"},
            "mounts": [{"Type": "bind", "Source": uninstall.PROXY_ROOT + suffix, "Destination": target} for target, suffix in paths.items()]}
