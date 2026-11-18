"""Bash discovery and path conversion for shell regressions."""
from __future__ import annotations

import os
import shutil
from pathlib import Path

git = shutil.which("git")

git_bash = Path(git).resolve().parents[1] / "bin/bash.exe" if git else None

BASH = os.getenv("TGCLOUD_TEST_BASH") or (
    str(git_bash) if os.name == "nt" and git_bash and git_bash.is_file()
    else shutil.which("bash") if os.name != "nt" else None
)

def bash_path(path: Path) -> str:
    value = path.as_posix()
    return "/" + value[0].lower() + value[2:] if os.name == "nt" else value
