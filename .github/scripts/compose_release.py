"""Build and verify a local-source Compose release asset; never publish images."""
from __future__ import annotations

import argparse
import hashlib
import re
import shutil
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "src"))

from tgcloud.compose import packaging, releases, runtime
from tgcloud.deployment import local_source
from tgcloud.deployment.releases import extract_source
from tgcloud.version import VERSION

TAG = re.compile(r"v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))(?:-(?:alpha|beta|rc)\.[1-9][0-9]*)?")


def tag_version(tag: str) -> str:
    match = TAG.fullmatch(tag)
    if match is None:
        raise ValueError("expected a semantic version tag")
    return match[1]


def artifact_name(tag: str) -> str:
    tag_version(tag)
    return f"TGCloud-{tag}-compose.tar.gz"


def verify(directory: Path, tag: str) -> dict:
    name = artifact_name(tag)
    if {p.name for p in directory.iterdir()} != {name, "COMPOSE-SHA256SUMS.txt"}:
        raise ValueError("unexpected Compose release asset set")
    archive = directory / name
    checksum = directory / "COMPOSE-SHA256SUMS.txt"
    if (archive.is_symlink() or not archive.is_file() or archive.stat().st_size > 100 * 1024**2
            or checksum.is_symlink() or not checksum.is_file()):
        raise ValueError("unsafe Compose release asset")
    expected = hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + name + "\n"
    if checksum.read_text(encoding="ascii") != expected:
        raise ValueError("Compose release checksum mismatch")
    with tempfile.TemporaryDirectory(prefix="tgcloud-compose-release-check-") as temporary:
        root = Path(temporary).resolve() / "bundle"
        extract_source(archive, root)
        info = runtime.metadata(root, expected_version=tag_version(tag))
        releases.verify_contents(root, info)
        return info


def build(source: Path, directory: Path, tag: str) -> dict:
    name = artifact_name(tag)
    if local_source.source_version(source) != tag_version(tag):
        raise ValueError("Compose release tag does not match source version")
    if directory.exists() or directory.is_symlink():
        raise FileExistsError("Compose release output must be a new directory")
    with tempfile.TemporaryDirectory(prefix="tgcloud-compose-release-build-") as temporary:
        archives = packaging.build_bundles(source, Path(temporary).resolve() / "candidate")
        directory.mkdir(parents=True, exist_ok=False)
        with archives[0].open("rb") as reader, (directory / name).open("xb") as writer:
            shutil.copyfileobj(reader, writer)
    with (directory / "COMPOSE-SHA256SUMS.txt").open("x", encoding="ascii", newline="\n") as writer:
        writer.write(hashlib.sha256((directory / name).read_bytes()).hexdigest() + "  " + name + "\n")
    return verify(directory, tag)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("bundle", "verify"))
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--tag", default="v" + VERSION)
    args = parser.parse_args(argv)
    try:
        info = build(ROOT, args.directory, args.tag) if args.action == "bundle" else verify(args.directory, args.tag)
        print(f"COMPOSE_RELEASE_{args.action.upper()}=OK version={info['version']} source={info['source_sha256']}")
        return 0
    except (ValueError, OSError, RuntimeError):
        print("Compose release validation failed; check the tag, public source and new output directory.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
