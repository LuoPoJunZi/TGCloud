"""Read literal TGCloud versions without importing candidate source code."""
from __future__ import annotations

import ast
from pathlib import Path


def literal_version(root: Path) -> str:
    for name in ("src/tgcloud/version.py", "app/tgcloud/version.py"):
        path = root / name
        if path.is_symlink():
            raise ValueError("version file is linked")
        if not path.is_file():
            continue
        tree = ast.parse(path.read_text(encoding="utf-8"))
        values = [node.value.value for node in tree.body
                  if isinstance(node, ast.Assign) and isinstance(node.value, ast.Constant)
                  and any(isinstance(target, ast.Name) and target.id == "VERSION" for target in node.targets)]
        if len(values) != 1 or not isinstance(values[0], str):
            raise ValueError("invalid literal version source")
        return values[0]
    raise ValueError("TGCloud version source missing")
