"""Build/verify one immutable source artifact; never deploy or publish anything."""
from __future__ import annotations

import argparse
import ast
import gzip
import hashlib
import io
import json
import re
import shutil
import subprocess  # nosec B404
import tarfile
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parents[2]
TAG = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:alpha|beta|rc)\.[1-9][0-9]*)?")
LIMIT = 100 * 1024 * 1024
MODERN_REQUIRED = {
    "install.sh", "install-lan.sh", "LICENSE", "pyproject.toml",
    "scripts/run_source.py", "src/tgcloud/__init__.py", "src/tgcloud/version.py",
    "src/tgcloud/app.py", "src/tgcloud/deployment/profiles.py",
    "src/tgcloud/deployment/fingerprints.py", "src/tgcloud/cli/commands.py",
    "requirements/bot.lock.txt", "requirements/dev.lock.txt", "packaging/Dockerfile", "THIRD_PARTY_NOTICES.md",
    "packaging/compose/docker-compose.clouddrive2.yml",
    "packaging/bootstrap-manifest.json", "packaging/runtime-manifest.json",
}
REQUIRED = MODERN_REQUIRED | {"packaging/external-webdav.json", "src/tgcloud/deployment/uninstall.py",
                              "src/tgcloud/addons/external_webdav/entry.py"}


def unified_layout(marker: bytes | None, *, has_addon: bool) -> bool:
    if marker is None or not has_addon:
        raise ValueError("incomplete external WebDAV release contract")
    value = json.loads(marker)
    if (not isinstance(value, dict) or type(value.get("api")) is not int
            or value != {"api": 2, "proxy_schema": 2, "owner": "tgcloud.external_webdav"}):
        raise ValueError("unsupported external WebDAV release contract")
    return True


def manifest_files(content: bytes, expected: set[str]) -> list[str]:
    manifest = json.loads(content)
    paths = manifest.get("files")
    if (manifest.get("format") != 1 or not isinstance(paths, list) or not paths
            or any(not isinstance(name, str) or not safe_path(name) for name in paths)
            or len(paths) != len(set(paths)) or set(paths) != expected):
        raise ValueError("invalid or stale source manifest")
    return paths


def helpers_in_tree(files: set[str]) -> set[str]:
    result = {"scripts/run_source.py", "src/tgcloud/__init__.py", "src/tgcloud/version.py", "src/tgcloud/config.py"} | {
        path for path in files if path.endswith(".py") and path.startswith(("src/tgcloud/cli/", "src/tgcloud/deployment/", "src/tgcloud/addons/"))
    }
    if {"src/tgcloud/deployment/backup.py", "src/tgcloud/cli/naming.py"} & files:
        result.update("src/tgcloud/" + name for name in (
            "persistence/__init__.py", "persistence/backup.py", "persistence/bundle.py",
            "domain/__init__.py", "domain/naming.py"))
    if "src/tgcloud/persistence/paths.py" in files:
        result.add("src/tgcloud/persistence/paths.py")
    if "src/tgcloud/compose/management.py" in files:
        result.update(path for path in files if path.startswith("src/tgcloud/compose/") and path.endswith(".py"))
        result.add("packaging/compose/docker-compose.clouddrive2.yml")
    return result


def git(root: Path, *args: str) -> bytes:
    executable = shutil.which("git")
    if executable is None:
        raise ValueError("Git is required")
    return subprocess.check_output([executable, "-C", str(root), *args], shell=False)  # nosec B603


def artifact_name(tag: str) -> str:
    if not TAG.fullmatch(tag):
        raise ValueError("invalid release tag")
    return f"TGCloud-{tag}-source.tar.gz"


def validate_pins(root: Path, commit: str) -> None:
    files = set(git(root, "ls-tree", "-r", "--name-only", commit).decode().splitlines())
    if not REQUIRED <= files:
        raise ValueError("source archive missing required TGCloud inputs")
    manifest_files(git(root, "show", f"{commit}:packaging/bootstrap-manifest.json"), helpers_in_tree(files))
    unified_layout(git(root, "show", f"{commit}:packaging/external-webdav.json"), has_addon=True)
    for name, command in (("install.sh", "install"), ("install-lan.sh", "lan")):
        text = git(root, "show", f"{commit}:{name}").decode("utf-8")
        expected = 'exec python3 -B "$script_root/scripts/run_source.py" ' + command + ' "$@"'
        if expected not in text or any(item in text for item in ("installer_commit", "https://", "http://", "curl", "wget")):
            raise ValueError("invalid local-source Linux entry")


def safe_path(name: str) -> bool:
    path = PurePosixPath(name)
    return bool(name) and not path.is_absolute() and not any(part in {"..", ".git"} for part in path.parts) and "\\" not in name and ":" not in name


def verify(directory: Path, tag: str) -> None:
    name = artifact_name(tag)
    if {path.name for path in directory.iterdir()} != {name, "SHA256SUMS.txt"}:
        raise ValueError("unexpected source release asset set")
    archive = directory / name
    if archive.is_symlink() or not archive.is_file() or archive.stat().st_size > LIMIT:
        raise ValueError("invalid source archive")
    data = archive.read_bytes()
    expected = hashlib.sha256(data).hexdigest() + "  " + name + "\n"
    if (directory / "SHA256SUMS.txt").read_text(encoding="ascii") != expected:
        raise ValueError("source archive SHA256 mismatch")
    files = {}
    total = 0
    prefix = f"TGCloud-{tag}/"
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as bundle:
        seen = set()
        for member in bundle:
            if member.name in seen or len(seen) >= 5000:
                raise ValueError("duplicate/too many source archive members")
            seen.add(member.name)
            if member.isdir() and member.name == prefix.rstrip("/"):
                continue
            if not member.name.startswith(prefix) or not safe_path(member.name) or not (member.isfile() or member.isdir()):
                raise ValueError("unsafe source archive member")
            if member.isdir():
                continue
            total += member.size
            if total > LIMIT:
                raise ValueError("source archive exceeds uncompressed limit")
            relative = member.name[len(prefix):]
            if relative.lower().endswith((".exe", ".pyc")) or any(part in {"build", "dist", ".venv", "__pycache__", "archive", "legacy", "history", "old-docs"} for part in PurePosixPath(relative).parts):
                raise ValueError("build output in source archive")
            files[relative] = bundle.extractfile(member).read()
    if not REQUIRED <= files.keys():
        raise ValueError("source archive missing required TGCloud inputs")
    unified_layout(files.get("packaging/external-webdav.json"), has_addon=True)
    if {"uninstall.sh", "install-webdav.sh"} & files.keys():
        raise ValueError("unexpected standalone entry in TGCloud source layout")
    runtime = {path for path in files if path.startswith("src/tgcloud/") and path.endswith(".py")}
    manifest_files(files["packaging/runtime-manifest.json"], runtime)
    manifest_files(files["packaging/bootstrap-manifest.json"], helpers_in_tree(set(files)))
    if "packaging/compose-release.json" in files:
        contract = json.loads(files["packaging/compose-release.json"])
        local = {"format": 4, "https_tools": 1, "mode": "compose", "edition": "clouddrive2",
                 "project": "tgcloud", "local_build": True}
        if (contract != local or type(contract.get("format")) is not int
                or type(contract.get("local_build")) is not bool or type(contract.get("https_tools")) is not int):
            raise ValueError("unsupported direct Compose contract")
        if not {"packaging/compose-https.sh", "src/tgcloud/compose/https.py", "src/tgcloud/compose/access.py",
                "src/tgcloud/compose/runtime.py", "src/tgcloud/compose/packaging.py",
                ".github/scripts/compose_release.py", ".github/workflows/compose-bundles.yml", "docs/compose-guide.md"} <= files.keys():
            raise ValueError("incomplete direct Compose source layout")
        launcher = files["packaging/compose-https.sh"]
        if (launcher.count(b"@HTTPS_MANIFEST_SHA256@") != 1
                or b"exec python3 -I -S -B" not in launcher or b"tgcloud.compose.https" not in launcher):
            raise ValueError("invalid direct Compose HTTPS launcher")
    # Verify all declared Edition payload inputs, not just a hand-maintained list.
    profile = "src/tgcloud/deployment/profiles.py"
    tree = ast.parse(files[profile].decode("utf-8"))
    payloads = {node.value for node in ast.walk(tree)
                if isinstance(node, ast.Constant) and isinstance(node.value, str)
                and node.value.startswith(("packaging/", "requirements/"))
                and (PurePosixPath(node.value).suffix or PurePosixPath(node.value).name == ".dockerignore")}
    editions = ("packaging/compose/docker-compose.clouddrive2.yml",)
    if not all(any(path.startswith(edition) for path in payloads) for edition in editions):
        raise ValueError("CloudDrive2 payload manifest is required")
    if not payloads.issubset(files):
        raise ValueError("missing Edition payload inputs: " + ", ".join(sorted(payloads - files.keys())))
    version_path = "src/tgcloud/version.py"
    versions = [ast.literal_eval(node.value) for node in ast.parse(files[version_path].decode("utf-8")).body
                if isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == "VERSION" for t in node.targets)]
    if versions != [tag[1:].split("-", 1)[0]]:
        raise ValueError("source archive version mismatch")
    for path, content in files.items():
        if path.endswith(".py"):
            ast.parse(content.decode("utf-8"), filename=path)


def build(root: Path, directory: Path, tag: str) -> None:
    name = artifact_name(tag)
    commit = git(root, "rev-parse", f"refs/tags/{tag}^{{commit}}").decode().strip()
    if git(root, "rev-parse", "HEAD").decode().strip() != commit:
        raise ValueError("checkout must match the release tag")
    if git(root, "diff", "HEAD", "--name-only"):
        raise ValueError("tracked source changes must be committed before packaging")
    validate_pins(root, commit)
    if directory.exists() and any(directory.iterdir()):
        raise ValueError("output directory must be empty; refusing to replace artifacts")
    archive = git(root, "archive", "--format=tar", f"--prefix=TGCloud-{tag}/", commit)
    if len(archive) > LIMIT:
        raise ValueError("source archive too large")
    data = gzip.compress(archive, mtime=0)
    directory.mkdir(parents=True, exist_ok=True)
    with (directory / name).open("xb") as writer:
        writer.write(data)
    with (directory / "SHA256SUMS.txt").open("x", encoding="ascii", newline="\n") as writer:
        writer.write(hashlib.sha256(data).hexdigest() + "  " + name + "\n")
    verify(directory, tag)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("build", "verify"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.action == "build":
            build(ROOT, args.directory, args.tag)
        else:
            verify(args.directory, args.tag)
        print("SOURCE_RELEASE_" + args.action.upper() + "=OK")
        return 0
    except (ValueError, OSError, SyntaxError, tarfile.TarError, subprocess.CalledProcessError) as exc:
        print("Source release validation failed: " + str(exc))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
