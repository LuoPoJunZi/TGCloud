"""Validate TGCloud local source entry manifests; never publish."""
from __future__ import annotations

import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def helper_files(root: Path) -> list[str]:
    paths = ["scripts/run_source.py", "src/tgcloud/__init__.py", "src/tgcloud/version.py", "src/tgcloud/config.py"]
    if ((root / "src/tgcloud/deployment/lifecycle/backup.py").is_file()
            or (root / "src/tgcloud/deployment/backup.py").is_file()
            or (root / "src/tgcloud/cli/naming.py").is_file()):
        paths.extend("src/tgcloud/" + name for name in (
            "persistence/__init__.py", "persistence/backup.py", "persistence/bundle.py",
            "domain/__init__.py", "domain/naming.py"))
    if (root / "src/tgcloud/persistence/paths.py").is_file():
        paths.append("src/tgcloud/persistence/paths.py")
    for directory in ("cli", "deployment", "addons"):
        for path in (root / "src/tgcloud" / directory).rglob("*.py"):
            if path.is_symlink() or path.absolute() != path.resolve():
                raise ValueError("helper source contains a link")
            paths.append(path.relative_to(root).as_posix())
    if (root / "src/tgcloud/compose/management.py").is_file():
        paths.extend(path.relative_to(root).as_posix() for path in (root / "src/tgcloud/compose").rglob("*.py"))
        paths.append("packaging/compose/docker-compose.clouddrive2.yml")
    for name in paths:
        path = root / name
        if not path.is_file() or path.is_symlink() or path.absolute() != path.resolve():
            raise ValueError("helper source missing or contains a link")
    return sorted(set(paths))


def runtime_files(root: Path) -> list[str]:
    paths = []
    for path in (root / "src/tgcloud").rglob("*.py"):
        if path.is_symlink() or path.absolute() != path.resolve():
            raise ValueError("runtime source contains a link")
        paths.append(path.relative_to(root).as_posix())
    return sorted(paths)


def render_entry(root: Path) -> str:
    text = (root / "install.sh").read_text(encoding="utf-8")
    local = text.partition("# TGCloud online bootstrap.")[0]
    for command in ("install", "lan"):
        if 'exec python3 -B "$script_root/scripts/run_source.py" ' + command + ' "$@"' not in local:
            raise ValueError("source entry does not dispatch " + command)
    return text


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--write-manifest", action="store_true")
    args = parser.parse_args()
    for name, files in (("bootstrap", helper_files(ROOT)), ("runtime", runtime_files(ROOT))):
        manifest = {"format": 1, "files": files}
        path = ROOT / f"packaging/{name}-manifest.json"
        if args.write_manifest:
            path.write_text(json.dumps(manifest, ensure_ascii=True, indent=2) + "\n", encoding="utf-8")
        elif json.loads(path.read_text(encoding="utf-8")) != manifest:
            raise ValueError(f"{name} manifest stale; regenerate after reviewing added source files")
    if args.directory:
        args.directory.mkdir(mode=0o700, parents=True, exist_ok=False)
        (args.directory / "install.sh").write_text(render_entry(ROOT), encoding="utf-8", newline="\n")
        print("CANDIDATE_ONLY: TGCloud source entry; no network installation or publication")
    print("BOOTSTRAP_MANIFEST=OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
