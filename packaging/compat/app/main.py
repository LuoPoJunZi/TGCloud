"""Compatibility for python -m app.main; no duplicate task implementation."""
import asyncio

from tgcloud.app import async_main

if __name__ == "__main__":
    asyncio.run(async_main())
