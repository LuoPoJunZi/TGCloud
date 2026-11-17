"""Installed isolated backup-preflight command compatibility."""
from tgcloud.persistence.bundle import main

if __name__ == "__main__":
    raise SystemExit(main())
