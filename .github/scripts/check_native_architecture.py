"""Opt-in native CI preflight: exact product images, no emulation or deployment."""
from __future__ import annotations

import argparse
import tempfile
from pathlib import Path

from tgcloud.deployment import architecture, payload
from tgcloud.deployment.host import LocalSession, SafeLog
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.deployment.proxy.manager import CERTBOT_IMAGE, NGINX_IMAGE

ROOT = Path(__file__).resolve().parents[2]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected", choices=("amd64", "arm64"), required=True)
    args = parser.parse_args()
    host = architecture.detect_host()
    if host.architecture != args.expected:
        raise ValueError("CI runner is not the requested native architecture")
    host.describe(print)
    session = LocalSession(SafeLog())
    architecture.check_docker(session)
    for product in PRODUCTS.values():
        with tempfile.TemporaryDirectory(prefix="tgcloud-native-ci-") as private:
            context = Path(private) / "payload"
            payload.build_payload(ROOT, product, context)
            (context / ".env").write_text("# isolated CloudDrive2 CI\n", encoding="utf-8")
            architecture.prepare_images(session, context / "docker-compose.yml", [product.key])
    for image in (NGINX_IMAGE, CERTBOT_IMAGE):
        architecture.ensure_image(session, image)
    print("NATIVE_IMAGE_PREFLIGHT=OK (metadata and pull only; runtime tests follow)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
