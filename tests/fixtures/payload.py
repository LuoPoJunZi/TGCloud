"""Current TGCloud runtime contexts; synthetic tests only."""
from __future__ import annotations

import atexit
import tempfile
from functools import lru_cache
from pathlib import Path

from tests.fixtures import ROOT
from tgcloud.deployment.payload import build_payload
from tgcloud.deployment.profiles import PRODUCTS

_temporary = tempfile.TemporaryDirectory(prefix="tgcloud-test-contexts-")
atexit.register(_temporary.cleanup)


@lru_cache(maxsize=2)
def payload(edition: str) -> Path:
    destination = Path(_temporary.name) / edition
    build_payload(ROOT, PRODUCTS[edition], destination)
    return destination
