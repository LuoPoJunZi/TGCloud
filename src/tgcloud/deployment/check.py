from __future__ import annotations

import argparse
import json
import os
import sys

from tgcloud import __version__
from tgcloud.config import Settings
from tgcloud.deployment.fingerprints import code_fingerprint

__all__ = ["code_fingerprint", "main", "mismatched_keys"]


def mismatched_keys(expected: dict[str, str], actual: dict[str, str]) -> list[str]:
    return sorted(key for key, value in expected.items()
                  if actual.get(key) != str(value))


def main() -> int:
    parser = argparse.ArgumentParser(description="核对运行配置和部署代码，不显示凭据")
    parser.add_argument("--expected-code")
    parser.add_argument("--validate-only", action="store_true")
    args = parser.parse_args()
    try:
        Settings.from_env(create_directories=False)
        if not args.validate_only:
            services = json.load(sys.stdin)["services"]
            service_name = os.getenv(
                "TGCLOUD_COMPOSE_SERVICE",
                "tgcloud-clouddrive2-bot",
            )
            desired = services[service_name]["environment"]
            changed = mismatched_keys(desired, dict(os.environ))
            if changed:
                print("TGCLOUD_CONFIG=MISMATCH_KEYS:" + ",".join(changed))
                return 1
            if not args.expected_code or code_fingerprint() != args.expected_code:
                print("TGCLOUD_CODE=MISMATCH")
                return 1
            print("TGCLOUD_CODE=OK")
        print(f"TGCLOUD_VERSION={__version__}")
        print("TGCLOUD_CONFIG=OK")
        return 0
    except Exception:  # noqa: BLE001 - never expose config values at this CLI boundary
        print("TGCLOUD_CONFIG=FAILED（配置格式或必填项错误；未输出配置值）", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
