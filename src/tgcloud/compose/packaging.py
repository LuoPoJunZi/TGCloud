"""Deterministic CD2-only Compose bundles built from a local public snapshot."""
from __future__ import annotations

import gzip
import hashlib
import io
import json
import re
import tarfile
from pathlib import Path

from tgcloud.deployment import local_source, payload
from tgcloud.deployment.profiles import PRODUCTS
from tgcloud.deployment.runtime import files as host_files
from tgcloud.version import VERSION

IMAGE = re.compile(r"tgcloud-local:[a-f0-9]{32}")
PROJECT_ID = "tgcloud"
EDITION = "clouddrive2"
PROJECT_LABEL = "com.tgcloud.project"
SOURCE_LABEL = "com.tgcloud.source-sha256"

# Compose reads .env on the host; only Bot settings enter the Bot process.
BOT_ENVIRONMENT = (
    "TELEGRAM_API_ID", "TELEGRAM_API_HASH_B64", "BOT_TOKEN_B64", "ALLOWED_USER_ID",
    "WEBDAV_URL_B64", "WEBDAV_USERNAME_B64", "WEBDAV_PASSWORD_B64", "WEBDAV_TARGET_PATH_B64",
    "CD2_WEBDAV_URL_B64", "CD2_WEBDAV_USERNAME_B64", "CD2_WEBDAV_PASSWORD_B64", "CD2_TARGET_PATH_B64",
    "TGCLOUD_STORAGE_BACKEND", "TG115_STORAGE_BACKEND", "TGCLOUD_RCLONE_REMOTE_NAME", "TG115_RCLONE_REMOTE_NAME",
    "LOCAL_TEMP_BUDGET_GB", "MIN_FREE_DISK_GB", "CONTROL_INTERVAL_SECONDS",
    "CPU_TARGET_LOW_PERCENT", "CPU_TARGET_HIGH_PERCENT", "CPU_PRESSURE_PERCENT",
    "MEMORY_SOFT_MIN_MB", "MEMORY_HARD_MIN_MB", "RAMP_UP_STEP", "RAMP_DOWN_FACTOR",
    "MAX_RETRIES", "REMOTE_HEALTH_INTERVAL_SECONDS", "TZ",
)
BOT_DEFAULTS = {
    "LOCAL_TEMP_BUDGET_GB": "20", "MIN_FREE_DISK_GB": "8", "CONTROL_INTERVAL_SECONDS": "3",
    "CPU_TARGET_LOW_PERCENT": "60", "CPU_TARGET_HIGH_PERCENT": "80", "CPU_PRESSURE_PERCENT": "90",
    "MEMORY_SOFT_MIN_MB": "1024", "MEMORY_HARD_MIN_MB": "512", "RAMP_UP_STEP": "1",
    "RAMP_DOWN_FACTOR": "0.5", "MAX_RETRIES": "3", "REMOTE_HEALTH_INTERVAL_SECONDS": "30",
    "TZ": "Asia/Shanghai",
}


def hashes(files: dict[str, bytes]) -> dict[str, str]:
    return {name: hashlib.sha256(body).hexdigest() for name, body in sorted(files.items())}


def image_name(digest: str) -> str:
    if not isinstance(digest, str) or not re.fullmatch(r"[a-f0-9]{64}", digest):
        raise ValueError("invalid local source digest")
    return "tgcloud-local:" + digest[:32]


def https_tools(root: Path) -> dict[str, bytes]:
    """The host-only, stdlib runtime is self-contained and independently pinned."""
    selected = host_files(root)
    required = {"src/tgcloud/compose/https.py", "src/tgcloud/compose/management.py",
                "packaging/compose/docker-compose.clouddrive2.yml"}
    if not required <= selected.keys():
        raise ValueError("Compose host helper source missing")
    content = (json.dumps({"format": 1, "files": hashes(selected)}, indent=2) + "\n").encode()
    template = (root / "packaging/compose-https.sh").read_bytes().replace(b"\r\n", b"\n")
    placeholder = b"@HTTPS_MANIFEST_SHA256@"
    if template.count(placeholder) != 1:
        raise ValueError("unexpected HTTPS launcher template")
    launcher = template.replace(placeholder, hashlib.sha256(content).hexdigest().encode())
    result = {"https.sh": launcher,
              "manage.sh": launcher.replace(b"tgcloud.compose.https", b"tgcloud.compose.management"),
              "https-tools/manifest.json": content}
    result.update({"https-tools/" + name: body for name, body in sorted(selected.items())})
    return result


def render(root: Path, edition: str, image: str, *, source_digest: str, legacy: bool = False) -> str:
    if edition != EDITION or legacy or image != image_name(source_digest):
        raise ValueError("only the CD2 local-source Compose contract is supported")
    product = PRODUCTS[EDITION]
    text = (root / "packaging/compose/docker-compose.clouddrive2.yml").read_text(encoding="utf-8")
    build = "    build:\n      context: .\n      dockerfile: Dockerfile\n"
    if text.count(build) != 1:
        raise ValueError("unexpected managed Compose build definition")
    local_build = ("    image: " + image + "\n    pull_policy: never\n    build:\n"
                   "      context: ./build-context\n      dockerfile: Dockerfile\n      labels:\n"
                   f"        {PROJECT_LABEL}: {PROJECT_ID}\n        {SOURCE_LABEL}: {source_digest}\n")
    text = text.replace(build, local_build)
    defaults = dict(BOT_DEFAULTS, TGCLOUD_STORAGE_BACKEND=EDITION, TGCLOUD_RCLONE_REMOTE_NAME="cd2")
    injection = "".join(f"      {key}: ${{{key}:-{defaults.get(key, '')}}}\n" for key in BOT_ENVIRONMENT)
    text = text.replace("    env_file:\n      - .env\n    environment:\n", "    environment:\n" + injection)
    text = text.replace('"127.0.0.1:', '"${TGCLOUD_BIND_IP:-127.0.0.1}:')
    bot_header = "  " + product.bot_service + ":\n"
    text = text.replace(bot_header, bot_header + '    pids_limit: ${TGCLOUD_BOT_PIDS_LIMIT:-256}\n'
                        + '    mem_limit: ${TGCLOUD_BOT_MEMORY_LIMIT:-0}\n'
                        + '    cpus: ${TGCLOUD_BOT_CPUS:-0}\n')
    text = re.sub(r"(?m)^(  [a-z0-9-]+:)\n", r'\1\n    labels:\n      com.tgcloud.install-mode: compose\n'
                  f'      {PROJECT_LABEL}: {PROJECT_ID}\n', text)
    tools = f"""  setup:
{local_build}    profiles: [tools]
    labels:
      com.tgcloud.install-mode: compose
      {PROJECT_LABEL}: {PROJECT_ID}
    user: "0:0"
    command: ["python", "-m", "tgcloud.compose", "setup"]
    working_dir: /workspace
    network_mode: none
    read_only: true
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE, FOWNER]
    security_opt: ["no-new-privileges:true"]
    tmpfs: ["/tmp:rw,noexec,nosuid,size=64m"]
    volumes:
      - ./:/workspace
      - ./backups:/backups
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
    restart: "no"

"""
    return text.replace("\nnetworks:\n", "\n" + tools + "networks:\n")


def bundle_files(root: Path, edition: str = EDITION, image: str | None = None,
                 commit: str = "", *, source_digest: str, version: str = VERSION) -> dict[str, bytes]:
    """Only an already verified local snapshot may supply these build inputs."""
    if commit or edition != EDITION or image not in {None, image_name(source_digest)}:
        raise ValueError("official images, commits and other Editions are not supported")
    image = image_name(source_digest)
    source = local_source.content(root)
    context_files = {
        "build-context/" + target: source[path.relative_to(root).as_posix()]
        for target, path in payload.payload_sources(root, PRODUCTS[EDITION]).items()
    }
    for name in ("LICENSE", "NOTICE", "README.md"):
        if name in source:
            context_files["build-context/" + name] = source[name]
    files = {
        "docker-compose.yml": render(root, edition, image, source_digest=source_digest).encode(),
        ".env.example": b"# Run the local setup service to generate the private .env. Never store secrets here.\nTGCLOUD_BIND_IP=127.0.0.1\n",
        "README.md": (root / "docs/compose-guide.md").read_bytes(),
        "LICENSE": (root / "LICENSE").read_bytes(),
        **https_tools(root), **context_files,
        **{"local-source/" + name: body for name, body in sorted(source.items())},
    }
    metadata = {"format": 4, "https_tools": 1, "mode": "compose", "edition": EDITION,
                "version": version, "image": image, "source_sha256": source_digest,
                "project": PROJECT_ID, "files": hashes(files)}
    files["compose-bundle.json"] = (json.dumps(metadata, indent=2) + "\n").encode()
    return files


def write_bundle(destination: Path, files: dict[str, bytes]) -> None:
    destination.mkdir(parents=True, exist_ok=False)
    for name, body in sorted(files.items()):
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(body)
        target.chmod(0o755 if name in {"https.sh", "manage.sh"} else 0o644)


def build_bundles(root: Path, output: Path, image: str | None = None, commit: str = "") -> list[Path]:
    if image is not None or commit:
        raise ValueError("local source builds do not accept an image or commit override")
    if output.exists() or output.is_symlink():
        raise FileExistsError("output must be a new directory")
    with local_source.snapshot(root) as source:
        files = bundle_files(source.root, source_digest=source.digest, version=source.version)
        output.mkdir(parents=True, exist_ok=False)
        write_bundle(output / EDITION, files)
        name = f"TGCloud-v{source.version}-compose-{source.digest[:12]}"
        archive = output / (name + ".tar.gz")
        with archive.open("xb") as out, gzip.GzipFile(fileobj=out, mode="wb", filename="", mtime=0) as zipped, tarfile.open(fileobj=zipped, mode="w") as tar:
            for relative, content in sorted(files.items()):
                info = tarfile.TarInfo(name + "/" + relative)
                info.size, info.mtime = len(content), 0
                info.mode = 0o755 if relative in {"https.sh", "manage.sh"} else 0o644
                tar.addfile(info, io.BytesIO(content))
        (output / "COMPOSE-SHA256SUMS.txt").write_text(
            hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n", encoding="ascii")
        return [archive]
