"""Build the CD2 standalone Compose package from this local public source only."""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from tgcloud.compose.packaging import build_bundles


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Build a CD2 local-source Compose package; no Docker or publication")
    parser.add_argument("--output", required=True, type=Path, help="new output directory; never overwritten")
    args = parser.parse_args(argv)
    try:
        archives = build_bundles(ROOT, args.output.absolute())
    except (ValueError, OSError):
        print("Local Compose build failed; verify public source inputs and use a new output directory.", file=sys.stderr)
        return 1
    for archive in archives:
        print(archive)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
