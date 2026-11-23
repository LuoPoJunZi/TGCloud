"""Installed maintenance command compatibility."""
from tgcloud.deployment.ops.check import code_fingerprint, main

__all__ = ["code_fingerprint", "main"]

if __name__ == "__main__":
    raise SystemExit(main())
