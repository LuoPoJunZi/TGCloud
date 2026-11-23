from __future__ import annotations

import re
import shutil
from pathlib import Path

from tgcloud.deployment.fingerprints import source_files
from tgcloud.deployment.profiles import ProductProfile, require_product


def payload_sources(root: Path, product: ProductProfile, *, strict: bool = True) -> dict[str, Path]:
    require_product(product)
    if root.is_symlink() or root.absolute() != root.resolve():
        raise ValueError("source root contains links")
    root = root.resolve()
    if not (root / "src/tgcloud/version.py").is_file():
        raise ValueError("完整 TGCloud 源码缺少 src/tgcloud/version.py")
    result = {}
    for relative in product.required_payload:
        source = root / relative
        if not source.is_file() or source.is_symlink() or source.absolute() != source.resolve():
            if strict:
                raise ValueError("稳定版本缺少必要部署资源：" + relative)
            continue
        if relative.startswith("packaging/compat/"):
            target = relative.removeprefix("packaging/compat/")
        elif relative.startswith("packaging/compose/"):
            target = "docker-compose.yml"
        elif relative == "requirements/bot.lock.txt":
            target = "requirements.txt"
        else:
            target = Path(relative).name
        if target in result:
            raise ValueError("duplicate deployment target: " + target)
        result[target] = source
    for source in source_files(root / "src/tgcloud"):
        target = "app/tgcloud/" + source.relative_to(root / "src/tgcloud").as_posix()
        if target in result:
            raise ValueError("duplicate Python target")
        result[target] = source
    return result


def build_payload(root: Path, product: ProductProfile, destination: Path) -> None:
    sources = payload_sources(root, product)
    destination.mkdir(mode=0o700)
    for relative, source in sources.items():
        target = destination / relative
        # Only manifest-selected public source enters this Docker context.
        # COPY preserves its modes, so UID 10001 needs readable files and
        # traversable subdirectories. The enclosing private directory stays
        # 0700; candidate credentials remain outside the context at 0600.
        parent = destination
        for part in Path(relative).parts[:-1]:
            parent = parent / part
            parent.mkdir(mode=0o755, exist_ok=True)
            parent.chmod(0o755)
        shutil.copyfile(source, target)
        target.chmod(0o644)
    for name in ("LICENSE", "NOTICE", "README.md"):
        if (root / name).is_file():
            shutil.copyfile(root / name, destination / name)
            (destination / name).chmod(0o644)


def build_install_payload(root: Path, product: ProductProfile, destination: Path, digest: str) -> bool:
    """Use the native generator only for one-click deployments."""
    from tgcloud.cli import host_cli
    from tgcloud.deployment import local_source

    if host_cli.supports("install-source"):
        root = root.absolute()
        names = local_source.source_names(root)
        mapping = {name: path.relative_to(root).as_posix() for name, path in payload_sources(root, product).items()}
        mapping.update({name: name for name in ("LICENSE", "NOTICE", "README.md") if name in names})
        result = local_source.native_source("payload", str(root), str(destination), digest,
                                            selection={"files": names, "payload": mapping})
        if result is not None:
            if local_source.source_names(root) != names:
                raise ValueError("生成部署包期间源码文件清单发生变化，请重新检查")
            return True
    build_payload(root, product, destination)
    return False


def gateway_image(source: Path, product: ProductProfile) -> str:
    text = payload_sources(source, product)["docker-compose.yml"].read_text(encoding="utf-8")
    images = re.findall(r"(?m)^    image: ([^\r\n]+)$", text)
    if len(images) != 1 or not re.fullmatch(r"[A-Za-z0-9_./:-]+@sha256:[0-9a-f]{64}", images[0]):
        raise ValueError("网关镜像不是可识别的固定 digest")
    return images[0]
