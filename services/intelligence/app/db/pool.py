"""Async Postgres connection pool used by Celery tasks that need to persist
results (OCR text, extracted entities, etc).

Celery worker model: --pool=solo runs every task in the SAME process.
Each task uses asyncio.run(...), which spins up a fresh event loop and
closes it on return. asyncpg.Pool internally pins itself to the loop
that created it, so caching the pool across asyncio.run() boundaries
gives "Event loop is closed" on the next task.

Strategy: key the cached pool by `id(loop)`. When a task starts a new
loop, get_pool() recreates the pool inside it. The previous loop's
pool is leaked (its connections will get GC'd when the old loop is
collected) — acceptable for a long-lived worker because we only ever
have one or two stale entries before they're swept.
"""
from __future__ import annotations

import asyncio
import logging
from typing import Optional

import asyncpg

from app.config import settings

log = logging.getLogger(__name__)

# (loop_id, pool) — keyed because module-global asyncio.Lock from a
# closed loop can't be reused either, so we lazy-init per loop.
_pool: Optional[asyncpg.Pool] = None
_pool_loop_id: Optional[int] = None


async def get_pool() -> asyncpg.Pool:
    """Return the asyncpg pool for the current event loop, creating it on
    first call (or whenever the loop changes — see module docstring)."""
    global _pool, _pool_loop_id
    loop = asyncio.get_running_loop()
    if _pool is not None and _pool_loop_id == id(loop):
        return _pool
    # Either first call or the previous pool was bound to a different
    # (now-closed) loop. Create fresh.
    _pool = await asyncpg.create_pool(
        dsn=settings.database_url,
        min_size=1,
        max_size=4,
        command_timeout=30.0,
    )
    _pool_loop_id = id(loop)
    log.info("asyncpg pool opened (loop_id=%s)", _pool_loop_id)
    return _pool


async def close_pool() -> None:
    global _pool, _pool_loop_id
    if _pool is not None:
        try:
            await _pool.close()
        except Exception:  # noqa: BLE001 — close is best-effort
            log.exception("close_pool failed")
        _pool = None
        _pool_loop_id = None
