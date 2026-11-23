"""Transactional restart recovery for persisted tasks."""
from __future__ import annotations

import sqlite3
import time
from pathlib import Path

from tgcloud.domain.errors import needs_review
from tgcloud.domain.naming import safe_file_name


def recover_tasks(conn: sqlite3.Connection, download_dir: Path, *, task_id: int | None = None) -> dict[str, int]:
    """Reconcile interrupted in-flight tasks inside the caller's lock."""
    recovered = {"queued": 0, "waiting_upload": 0, "failed": 0}
    conn.execute("BEGIN IMMEDIATE")
    try:
        rows = conn.execute(
            """
            SELECT * FROM tasks
            WHERE state IN (
                'reserved', 'downloading', 'downloaded', 'waiting_upload',
                'uploading', 'verifying', 'finalizing', 'streaming',
                'upload_failed_retained', 'verification_failed_retained'
            )
            AND (? IS NULL OR id=?)
            """,
            (task_id, task_id),
        ).fetchall()
        for row in rows:
            if row["cancel_requested"]:
                conn.execute("DELETE FROM quota_slots WHERE task_id=?", (int(row["id"]),))
                continue
            if needs_review(dict(row)) or row["failure_kind"] in {"authentication", "permission", "rate_limited"}:
                # A restart must not retry a MOVE whose result is unknown.
                recovered["failed"] += 1
                continue
            row_task_id = int(row["id"])
            local_path = Path(row["local_path"]) if row["local_path"] else None
            if row["transfer_mode"] == "stream":
                conn.execute(
                    """
                    UPDATE tasks SET state='queued', local_path=NULL,
                        downloaded_bytes=0, upload_retries=0,
                        wait_reason='流式任务在服务重启后自动重新排队',
                        next_retry_at=0, updated_at=?
                    WHERE id=?
                    """,
                    (time.time(), row_task_id),
                )
                recovered["queued"] += 1
                continue
            if (
                local_path
                and local_path.is_file()
                and not str(local_path).endswith(".part")
                and local_path.stat().st_size == int(row["file_size"])
            ):
                conn.execute(
                    """
                    UPDATE tasks SET state='waiting_upload', error=NULL,
                        upload_retries=0, next_retry_at=0,
                        wait_reason='服务重启后恢复上传', updated_at=?
                    WHERE id=?
                    """,
                    (time.time(), row_task_id),
                )
                recovered["waiting_upload"] += 1
                continue
            expected_final = download_dir / (
                f"{row_task_id}-"
                f"{safe_file_name(row['file_name'], str(row_task_id))}"
            )
            if (
                expected_final.is_file()
                and expected_final.stat().st_size == int(row["file_size"])
            ):
                conn.execute(
                    """
                    UPDATE tasks SET state='waiting_upload',
                        local_path=?, downloaded_bytes=file_size,
                        error=NULL, upload_retries=0, next_retry_at=0,
                        wait_reason='已恢复下载完成但未登记的本地文件',
                        updated_at=?
                    WHERE id=?
                    """,
                    (str(expected_final), time.time(), row_task_id),
                )
                recovered["waiting_upload"] += 1
                continue
            if row["state"] in {
                "upload_failed_retained",
                "verification_failed_retained",
            }:
                conn.execute(
                    """
                    UPDATE tasks SET state='queued', local_path=NULL,
                        downloaded_bytes=0, download_retries=0,
                        upload_retries=0,
                        wait_reason='保留记录对应的本地文件已不存在，自动重新排队',
                        next_retry_at=0, updated_at=?
                    WHERE id=?
                    """,
                    (time.time(), row_task_id),
                )
                recovered["queued"] += 1
                continue
            part_path = download_dir / f"{row_task_id}.part"
            if part_path.exists():
                try:
                    part_path.unlink()
                except OSError:
                    conn.execute(
                        """
                        UPDATE tasks SET state='download_failed',
                            error='重启后无法清理不完整文件', updated_at=?
                        WHERE id=?
                        """,
                        (time.time(), row_task_id),
                    )
                    recovered["failed"] += 1
                    continue
            conn.execute(
                """
                UPDATE tasks SET state='queued', local_path=NULL,
                    downloaded_bytes=0, download_retries=CASE WHEN read_identity LIKE 'listener:%'
                        THEN download_retries ELSE 0 END,
                    upload_retries=0,
                    wait_reason='本地文件不完整或不存在，服务重启后重新排队',
                    updated_at=?
                WHERE id=?
                """,
                (time.time(), row_task_id),
            )
            recovered["queued"] += 1
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise
    return recovered
