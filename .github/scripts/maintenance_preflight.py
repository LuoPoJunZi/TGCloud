"""Read-only validation of the complete TGCloud source tree; never publish."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(ROOT / "src"))

import prepare_bootstrap
import release_metadata

from tgcloud.deployment import local_source


def check_manifests(root: Path) -> None:
    for name, expected in (("bootstrap", prepare_bootstrap.helper_files(root)),
                           ("runtime", prepare_bootstrap.runtime_files(root))):
        actual = json.loads((root / f"packaging/{name}-manifest.json").read_text(encoding="utf-8"))
        if actual != {"format": 1, "files": expected}:
            raise ValueError("source manifest mismatch")


def check_helpers(root: Path) -> None:
    release_metadata.linux_entries(root, release_metadata.source_version(root))


def inspect(root: Path = ROOT) -> dict:
    checks = []
    def run(key, label, operation, failure):
        try:
            operation()
        except Exception:  # noqa: BLE001 - do not echo configuration or private paths
            checks.append({"key": key, "label": label, "state": "blocked", "detail": failure})
        else:
            checks.append({"key": key, "label": label, "state": "passed"})
    run("identity", "版本与发布文档", lambda: release_metadata.release_metadata(root, "v" + release_metadata.source_version(root)),
        "核对唯一版本、CHANGELOG 与 Release Notes。")
    run("manifests", "源码载荷清单", lambda: check_manifests(root), "核对完整源码清单。")
    run("source", "本地源码身份", lambda: local_source.digest(local_source.content(root)), "检查源码完整性及路径。")
    run("helpers", "本地安装入口", lambda: check_helpers(root), "入口必须使用完整本地源码。")
    return {"scope": "local-static-preflight-only", "checks": checks,
            "local_checks_passed": all(item["state"] == "passed" for item in checks),
            "not_checked": ["remote-ci", "release-assets", "real-vps", "telegram-transfer", "production-restore"]}


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="TGCloud 本地源码只读预检")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args(argv)
    report = inspect()
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        for item in report["checks"]:
            print(item["label"] + "：" + ("通过" if item["state"] == "passed" else "待处理"))
    return 0 if report["local_checks_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
