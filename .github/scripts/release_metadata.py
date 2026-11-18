"""Validate release identity using the Bot/deployer version source (stdlib only)."""

from __future__ import annotations

import argparse
import ast
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CORE = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
TAG = re.compile(rf"v({CORE})(?:-(alpha|beta|rc)\.([1-9][0-9]*))?")


def source_version(root: Path) -> str:
    path = root / "src/tgcloud/version.py"
    if path.is_symlink() or not path.is_file():
        raise ValueError("TGCloud version source missing or linked")
    module = ast.parse(path.read_text(encoding="utf-8"))
    values = [node.value.value for node in module.body
              if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "VERSION" for target in node.targets)
              and isinstance(node.value, ast.Constant) and isinstance(node.value.value, str)]
    if len(values) != 1 or not re.fullmatch(CORE, values[0]):
        raise ValueError("invalid single version source")
    return values[0]


def linux_entry(root: Path) -> None:
    value = json.loads((root / "packaging/external-webdav.json").read_text(encoding="utf-8"))
    if value != {"api": 2, "proxy_schema": 2, "owner": "tgcloud.external_webdav"}:
        raise ValueError("unsupported TGCloud external WebDAV contract")

    text = (root / "install.sh").read_text(encoding="utf-8")
    local, separator, online = text.partition("# TGCloud online bootstrap.")
    dispatches = ('exec python3 -B "$script_root/scripts/run_source.py" ' + command + ' "$@"'
                  for command in ("install", "lan"))
    # Validate both local modes and restrict the fallback to TGCloud release assets.
    if (not all(dispatch in local for dispatch in dispatches) or not separator
            or re.findall(r"https?://[^\s\"']+", online) != [
                "https://github.com/LuoPoJunZi/TGCloud/releases/download/${tgcloud_tag}"]
            or len(re.findall(r"(?m)^curl -fL ", online)) != 2
            or any(item in local for item in ("installer_commit", "https://", "http://", "curl", "wget"))):
        raise ValueError("invalid local-source Linux entry: install.sh")


def release_metadata(root: Path, tag: str) -> dict[str, str]:
    match = TAG.fullmatch(tag)
    version = source_version(root)
    if not match or match[1] != version:
        raise ValueError("release tag must match the source version")
    changelog = (root / "CHANGELOG.md").read_text(encoding="utf-8")
    if not re.search(rf"^## TGCloud v{re.escape(version)}\s*$", changelog, re.MULTILINE):
        raise ValueError("current version missing from CHANGELOG")
    notes = (root / "RELEASE_NOTES.md").read_text(encoding="utf-8")
    versions = re.findall(rf"TGCloud (v{CORE}(?:-(?:alpha|beta|rc)\.[1-9][0-9]*)?)", notes)
    if not versions or versions[0] != tag:
        raise ValueError("Release Notes identity must match the exact release tag")
    linux_entry(root)
    title = f"TGCloud v{version}"
    if match[2]:
        title += f" {match[2].upper()}{match[3]}"
    return {"release_tag": tag, "release_title": title,
            "release_artifact": f"TGCloud-{tag}-source",
            "is_prerelease": "true" if match[2] else "false"}


def main() -> int:
    parser = argparse.ArgumentParser(description="校验脚本版本和发布文档；不会创建 Tag 或 Release")
    parser.add_argument("--tag")
    parser.add_argument("--github-output", type=Path)
    args = parser.parse_args()
    try:
        version = source_version(ROOT)
        metadata = release_metadata(ROOT, args.tag) if args.tag else {"version": version}
        if args.github_output:
            with args.github_output.open("a", encoding="utf-8") as handle:
                for key, value in metadata.items():
                    handle.write(f"{key}={value}\n")
        print(json.dumps(metadata, ensure_ascii=True))
        return 0
    except (ValueError, SyntaxError, OSError) as exc:
        print(f"Release metadata validation failed: {exc}")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
