"""Duplicate detection via MinHash (Jaccard similarity)."""
from __future__ import annotations

import hashlib
import logging
import time

import redis as redispy
from datasketch import MinHash

from app.config import settings
from app.worker import celery_app

log = logging.getLogger(__name__)


def _shingles(text: str, k: int = 5) -> set[str]:
    words = text.lower().split()
    if len(words) < k:
        return {" ".join(words)}
    return {" ".join(words[i:i + k]) for i in range(len(words) - k + 1)}


def _minhash(shingles: set[str], num_perm: int = 128) -> MinHash:
    m = MinHash(num_perm=num_perm)
    for s in shingles:
        m.update(s.encode("utf-8"))
    return m


def _simhash(text: str) -> int:
    tokens = text.lower().split()
    v = [0] * 64
    for token in tokens:
        h = int(hashlib.md5(token.encode()).hexdigest(), 16)
        for i in range(64):
            if h & (1 << i):
                v[i] += 1
            else:
                v[i] -= 1
    result = 0
    for i in range(64):
        if v[i] > 0:
            result |= (1 << i)
    return result


def _hamming(a: int, b: int) -> int:
    return bin(a ^ b).count("1")


@celery_app.task(
    name="app.tasks.duplicate.detect_duplicates",
    bind=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 1},
)
def detect_duplicates(self, tenant_id: str, document_id: str, version_id: str, text: str):
    start = time.monotonic()
    shings = _shingles(text)
    mh = _minhash(shings)
    sh = _simhash(text)

    r = redispy.Redis.from_url(settings.redis_cache_url)
    key_prefix = f"minhash:{tenant_id}:"
    simhash_prefix = f"simhash:{tenant_id}:"

    duplicates = []
    try:
        existing_keys = r.keys(f"{key_prefix}*")
        for ek in existing_keys[:500]:
            other_id = ek.decode().replace(key_prefix, "")
            if other_id == document_id:
                continue
            raw = r.get(ek)
            if not raw:
                continue
            other_mh = MinHash(num_perm=128)
            other_mh.hashvalues = __import__("numpy").frombuffer(raw, dtype="uint64")
            jaccard = mh.jaccard(other_mh)
            if jaccard > 0.8:
                duplicates.append({"document_id": other_id, "similarity": round(jaccard, 3), "method": "minhash"})

        existing_sh = r.keys(f"{simhash_prefix}*")
        for esk in existing_sh[:500]:
            other_id = esk.decode().replace(simhash_prefix, "")
            if other_id == document_id:
                continue
            other_sh = int(r.get(esk) or 0)
            dist = _hamming(sh, other_sh)
            if dist <= 5:
                sim = 1.0 - dist / 64.0
                already = any(d["document_id"] == other_id for d in duplicates)
                if not already:
                    duplicates.append({"document_id": other_id, "similarity": round(sim, 3), "method": "simhash"})

        r.set(f"{key_prefix}{document_id}", mh.hashvalues.tobytes(), ex=90 * 86400)
        r.set(f"{simhash_prefix}{document_id}", str(sh), ex=90 * 86400)
    finally:
        r.close()

    elapsed_ms = int((time.monotonic() - start) * 1000)
    return {
        "status": "completed",
        "tenant_id": tenant_id,
        "document_id": document_id,
        "version_id": version_id,
        "duplicates": duplicates,
        "duplicate_count": len(duplicates),
        "processing_time_ms": elapsed_ms,
    }
