"""Immutable Edition identities and payload manifests for Linux deployment."""

from __future__ import annotations

from dataclasses import dataclass

from tgcloud.version import VERSION as RELEASE_VERSION

SHARED_PAYLOAD_FILES = (
    "packaging/hooks/clouddrive2/backup_retention.sh",
    "packaging/hooks/clouddrive2/preserve_runtime_config.sh",
    "packaging/.dockerignore", "packaging/Dockerfile", "requirements/bot.lock.txt",
    "packaging/compat/app/__init__.py", "packaging/compat/app/main.py",
    "packaging/compat/app/deployment_check.py", "packaging/compat/app/healthcheck.py",
    "packaging/compat/app/backup_database.py", "packaging/compat/app/backup_bundle.py",
    "packaging/compat/app/verify_destination.py",
)


@dataclass(frozen=True)
class ProductProfile:
    key: str
    display_name: str
    app_title: str
    app_version: str
    executable_name: str
    install_dir: str
    backup_dir: str
    webdav_url: str
    webdav_username: str
    webdav_target: str
    management_port: int
    bot_service: str
    bot_container: str
    storage_container: str
    docker_network: str
    requires_fuse: bool
    managed_toggle: bool
    payload_variant: str
    required_payload: tuple[str, ...]



CLOUDDRIVE2_PRODUCT = ProductProfile(
    key="clouddrive2",
    display_name="CloudDrive2",
    app_title="TGCloud · CloudDrive2",
    app_version=RELEASE_VERSION,
    executable_name="TGCloud-CloudDrive2-Deployer",
    install_dir="/opt/tgcloud",
    backup_dir="/opt/tgcloud-backups",
    webdav_url="http://tgcloud-clouddrive2:19798/dav",
    webdav_username="",
    webdav_target="",
    management_port=19798,
    bot_service="tgcloud-clouddrive2-bot",
    bot_container="tgcloud-clouddrive2-bot",
    storage_container="tgcloud-clouddrive2",
    docker_network="tgcloud-clouddrive2-net",
    requires_fuse=True,
    managed_toggle=True,
    payload_variant="packaging/hooks/clouddrive2",
    required_payload=SHARED_PAYLOAD_FILES
    + (
        "packaging/hooks/clouddrive2/remote_install.sh",
        "packaging/hooks/clouddrive2/manage.sh",
        "packaging/hooks/clouddrive2/repair_clouddrive_network.sh",
        "packaging/compose/docker-compose.clouddrive2.yml",
    ),
)


PRODUCTS = {CLOUDDRIVE2_PRODUCT.key: CLOUDDRIVE2_PRODUCT}

def require_product(product: ProductProfile) -> None:
    if product.key not in PRODUCTS:
        raise ValueError("TGCloud 仅支持 CloudDrive2。")
