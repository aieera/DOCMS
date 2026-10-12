"""content_blobs lookup for the preview worker (M-24).

The storage service records each blob's location and envelope metadata
(encrypted_dek, dek_nonce, kek_id) on its content_blobs row; the preview
worker needs them to decrypt the source before rendering. The query runs
in a tenant transaction (`app.current_tenant`), so RLS scopes it exactly
like the Go services' WithTenantTx.
"""
from __future__ import annotations

import logging
from typing import Optional

from app.config import settings

log = logging.getLogger(__name__)
_warned_no_db = False


def fetch_source_blob(tenant_id: str, content_blob_id: str) -> Optional[dict]:
    """Return {storage_bucket, storage_key, storage_region, encrypted_dek,
    dek_nonce, kek_id} for the blob, or None when there is no blob id, no
    database is configured, or no row is visible. DB errors propagate so
    the task retries rather than rendering ciphertext."""
    global _warned_no_db
    if not content_blob_id:
        return None
    if not settings.database_url:
        if not _warned_no_db:
            log.warning("SEDOC_DATABASE_URL not set: preview cannot read envelope "
                        "metadata and will fail on encrypted blobs")
            _warned_no_db = True
        return None
    import psycopg  # lazy: only the worker needs the driver

    with psycopg.connect(settings.database_url, connect_timeout=10) as conn:
        with conn.transaction():
            conn.execute("SELECT set_config('app.current_tenant', %s, true)", (tenant_id,))
            row = conn.execute(
                """SELECT storage_bucket, storage_key, storage_region,
                          encrypted_dek, dek_nonce, COALESCE(kek_id, '')
                     FROM content_blobs
                    WHERE tenant_id = %s AND id = %s""",
                (tenant_id, content_blob_id),
            ).fetchone()
    if row is None:
        return None
    return {
        "storage_bucket": row[0] or "",
        "storage_key": row[1] or "",
        "storage_region": row[2] or "",
        "encrypted_dek": bytes(row[3]) if row[3] else b"",
        "dek_nonce": bytes(row[4]) if row[4] else b"",
        "kek_id": row[5] or "",
    }
