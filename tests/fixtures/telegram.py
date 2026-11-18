"""Synthetic Telegram messages, callback data and offline transport fixtures."""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
from pathlib import Path
from types import SimpleNamespace as NS
from unittest.mock import AsyncMock

from telethon.sessions import MemorySession
from telethon.tl import types
from telethon.tl.functions.upload import GetFileRequest

from tgcloud.listening.transport import ListeningClient


def message(
    mid=1,
    *,
    original="Ab83Kd91X.mp4",
    caption="城市漫游 第二季 第03集 1080P",
    kind="video",
    protected=False,
    chat=-1001,
    group=None,
    size=100,
):
    extension = Path(original).suffix
    return NS(
        id=mid,
        chat_id=chat,
        sender_id=42,
        grouped_id=group,
        out=False,
        media=True,
        document=NS(id=1000 + mid) if kind != "photo" else None,
        photo=NS(id=1000 + mid) if kind == "photo" else None,
        video=True if kind == "video" else None,
        audio=None,
        voice=None,
        entities=[],
        file=NS(
            name=original,
            size=size,
            ext=extension,
            mime_type="video/mp4"
            if kind == "video"
            else "image/jpeg"
            if kind == "photo"
            else "application/pdf",
        ),
        raw_text=caption,
        message=caption,
        noforwards=protected,
        date=datetime.now(timezone.utc) - timedelta(seconds=10),
    )


def event(actor, msg=None, text=""):
    msg = msg or NS(media=None, file=None, grouped_id=None)
    return NS(
        sender_id=actor,
        chat_id=actor,
        is_private=True,
        message=msg,
        raw_text=text,
        reply=AsyncMock(),
    )


def callback_data(button: object) -> bytes:
    """Read callback payloads in both layer 227 and layer 229 button types."""
    data = getattr(button, "data", None)
    if data is None:
        data = getattr(getattr(button, "type", None), "data", None)
    if not isinstance(data, bytes):
        raise TypeError("Expected an inline callback button with bytes data")
    return data


def client():
    session = MemorySession()
    session.set_dc(2, "127.0.0.1", 443)
    return ListeningClient(session, 1234, "a" * 32, request_retries=0,
                           raise_last_call_error=True, flood_sleep_threshold=0,
                           receive_updates=False)


def request():
    return GetFileRequest(types.InputDocumentFileLocation(
        id=41, access_hash=42, file_reference=b"synthetic", thumb_size=""),
        offset=0, limit=4096)
