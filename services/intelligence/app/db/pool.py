"""Async Postgres connection pool used by Celery tasks that need to persist
results (OCR text, extracted entities, etc). The pool is lazily created on
first use and then reused across tasks within the same worker process.

Celery's pool model (prefork) means each worker child keeps its own pool;
asyncpg connections are not shared across processes, which matches the
prefork boundary.
"""
from __future__ import annotations

import asyncio
import logging
from typing import Optional

import asyncpg

from app.config import settings

log = logging.getLogger(__name__)

_pool: Optional[asyncpg.Pool] = None
_pool_lock = asyncio.Lock()


async def get_pool() -> asyncpg.Pool:
    """Return the process-wide asyncpg pool, creating it on first call."""
    global _pool
    if _pool is not None:
        return _pool
    async with _pool_lock:
        if _pool is None:
            _pool = await asyncpg.create_pool(
                dsn=settings.database_url,
                min_size=1,
                max_size=4,
                command_timeout=30.0,
            )
            log.info("asyncpg pool opened")
    return _pool


async def close_pool() -> None:
    global _pool
    if _pool is not None:
        await _pool.close()
        _pool = None
