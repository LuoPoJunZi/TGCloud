"""Generate a reviewable release plan; never edit sources, pins, tags or remotes."""
from __future__ import annotations

import argparse
import difflib
import json
import re
from pathlib import Path

from release_metadata import CORE, source_version

ROOT = Path(__file__).resolve().parents[2]


def replace_one(text: str, pattern: str, replacement: str) -> str:
    result, count = re.subn(pattern, replacement, text, flags=re.MULTILINE)
    if count != 1:
        raise ValueError("expected one public version field")
    return result


def prepare(root: Path, version: str) -> tuple[dict, str]:
    current = source_version(root)
    if not re.fullmatch(CORE, version) or tuple(map(int, version.split('.'))) <= tuple(map(int, current.split('.'))):
        raise ValueError("target must be a newer stable semantic version")
    changes = {}
    def read(name):
        path = root / name
        if path.is_symlink() or path.resolve() != path.absolute():
            raise ValueError("linked release input is not supported")
        return path.read_text(encoding="utf-8")
    name = "src/tgcloud/version.py"
    changes[name] = replace_one(read(name), r'^VERSION = "[0-9.]+"$', 'VERSION = "' + version + '"')
    name = "hostcli/internal/hostutil/version.go"
    changes[name] = replace_one(read(name), r'^var Version = "[0-9.]+"$', 'var Version = "' + version + '"')
    name = "install.sh"
    entry = read(name)
    if 'script_root=' in entry and "installer_version" not in entry:
        if 'exec python3 -B "$script_root/scripts/run_source.py" install "$@"' not in entry:
            raise ValueError("invalid local-source Linux entry")
    else:
        changes[name] = replace_one(entry, r'^installer_version\s*=\s*"[0-9.]+"$', 'installer_version = "' + version + '"')
    patch = "".join("".join(difflib.unified_diff(read(name).splitlines(True), value.splitlines(True),
                                             fromfile="a/" + name, tofile="b/" + name))
                    for name, value in changes.items())
    return {"format": 1, "current": current, "target": version, "source_modified": False,
            "mechanical_files": list(changes), "manual_review": [
                "CHANGELOG.md: describe actual changes and compatibility",
                "RELEASE_NOTES.md: exact target identity and real validation evidence",
                "README.md / README_EN.md / docs: current instructions and download links",
                "review and regenerate bootstrap/runtime source manifests",
            ], "release_gates": [
                "review candidate patch; run local regression and maintenance_preflight.py",
                "review and commit current source only after explicit authorization",
                "verify branch CI; authorize an immutable tag and native release validation separately",
                "download and verify actual release assets and their documentation",
            ], "not_checked": ["remote-ci", "published-helper", "release-assets", "real-vps"]}, patch


def write_candidates(root: Path, directory: Path, report: dict, patch: str) -> None:
    directory = directory if directory.is_absolute() else root / directory
    build = root / "build"
    if not directory.absolute().is_relative_to(build.absolute()) or directory.absolute() == build.absolute():
        raise ValueError("candidate directory must be a new child of build")
    if directory.absolute() != directory.resolve() or directory.exists():
        raise ValueError("candidate directory exists or follows a link")
    directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    for name, content in (("plan.json", json.dumps(report, ensure_ascii=False, indent=2) + "\n"),
                          ("version.patch", patch)):
        with (directory / name).open("x", encoding="utf-8", newline="\n") as handle:
            handle.write(content)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="生成发布审阅计划与版本补丁；不改源码、pin，不提交或发布")
    parser.add_argument("--version", required=True, help="计划的新版本，须高于当前版本；不实际提升版本")
    parser.add_argument("--directory", type=Path, help="可选：仅写入 build 下全新的候选目录")
    args = parser.parse_args(argv)
    try:
        report, patch = prepare(ROOT, args.version)
        if args.directory:
            write_candidates(ROOT, args.directory, report, patch)
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0
    except (ValueError, OSError, KeyError):
        print("发布计划生成失败：核对版本、公开元数据与候选目录；未修改源码或 pin。")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
