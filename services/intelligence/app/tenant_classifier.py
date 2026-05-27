"""Per-tenant classifier model loader (ADR 0060).

When a tenant has a `production` row in `model_versions`, classify
loads it from MinIO (cached locally + in an LRU on disk) and uses
it. Otherwise returns (None, 0.0, "") so the caller falls through to
the default classifier.

The cache key is `(tenant_id, version_tag)` — promoting a new
version invalidates the previous cache entry naturally because the
new version_tag differs.
"""
from __future__ import annotations

import asyncio
import functools
import json
import logging
import os
import tempfile

from app.config import settings
from app.db.pool import get_pool

log = logging.getLogger(__name__)

# Cap the loaded-model cache. Each model is ~50 MB on disk plus
# ~250 MB resident once loaded; 10 tenants is ~3 GB.
LRU_CACHE_SIZE = 10


def classify_with_tenant_model(tenant_id: str, text: str) -> tuple[str | None, float, str]:
    """Returns (label, confidence, model_tag) or (None, 0.0, "") when
    the tenant has no production model."""
    info = _resolve_production_version(tenant_id)
    if info is None:
        return None, 0.0, ""
    version_tag, s3_path = info
    runner = _load_runner(tenant_id, version_tag, s3_path)
    if runner is None:
        return None, 0.0, ""
    label, conf = runner(text)
    return label, conf, version_tag


def _resolve_production_version(tenant_id: str) -> tuple[str, str] | None:
    """Look up the production model_version for this tenant. Sync
    wrapper around the async pool — we're called from a Celery task
    that's already running an asyncio loop in many places, but the
    classify task is sync, so use a fresh loop here."""
    return asyncio.run(_resolve_async(tenant_id))


async def _resolve_async(tenant_id: str) -> tuple[str, str] | None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT version_tag, s3_artifact_path
                  FROM model_versions
                 WHERE tenant_id = $1
                   AND model_type = 'classification'
                   AND status = 'production'
                """,
                tenant_id,
            )
    if not row:
        return None
    return row["version_tag"], row["s3_artifact_path"]


@functools.lru_cache(maxsize=LRU_CACHE_SIZE)
def _load_runner(tenant_id: str, version_tag: str, s3_path: str):
    """Download + load the model. The cache is keyed by version_tag
    so promoting a new model evicts the old entry naturally on
    next call."""
    try:
        local = tempfile.mkdtemp(prefix=f"vaultdms-tenant-{tenant_id[:8]}-")
        _download(s3_path, local)
        return _build_runner(local)
    except Exception:
        log.exception("tenant model load failed (tenant=%s tag=%s)",
                      tenant_id, version_tag)
        return None


def _build_runner(local_dir: str):
    """Return a closure that classifies a single string. Heavy
    imports inside so the module loads without torch."""
    import torch
    from transformers import AutoModelForSequenceClassification, AutoTokenizer

    label_map_path = os.path.join(local_dir, "label_map.json")
    with open(label_map_path) as f:
        lm = json.load(f)
    id_to_label = {int(k): v for k, v in lm["id_to_label"].items()}
    tokenizer = AutoTokenizer.from_pretrained(local_dir)
    model = AutoModelForSequenceClassification.from_pretrained(local_dir)
    model.eval()

    def run(text: str) -> tuple[str, float]:
        with torch.no_grad():
            enc = tokenizer(text, truncation=True, padding="max_length",
                            max_length=256, return_tensors="pt")
            logits = model(**enc).logits
            probs = torch.softmax(logits, dim=-1).squeeze(0)
            top = int(torch.argmax(probs).item())
            return id_to_label.get(top, "Other"), float(probs[top].item())
    return run


def _download(s3_path: str, local_dir: str) -> None:
    import boto3
    from botocore.client import Config

    if not s3_path.startswith("s3://"):
        return
    rest = s3_path[len("s3://"):]
    bucket, _, prefix = rest.partition("/")
    s3 = boto3.client(
        "s3",
        endpoint_url=getattr(settings, "s3_endpoint", None) or os.getenv("AWS_ENDPOINT_URL"),
        aws_access_key_id=os.getenv("AWS_ACCESS_KEY_ID"),
        aws_secret_access_key=os.getenv("AWS_SECRET_ACCESS_KEY"),
        config=Config(signature_version="s3v4"),
        region_name=getattr(settings, "s3_region", None) or os.getenv("AWS_REGION", "us-east-1"),
    )
    paginator = s3.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            name = os.path.basename(key)
            if name:
                s3.download_file(bucket, key, os.path.join(local_dir, name))


def evict_tenant_cache(tenant_id: str) -> int:
    """Drop every cached entry for this tenant. Called by the
    promotion event handler so newly-promoted models get loaded
    on next request."""
    # functools.lru_cache doesn't let us evict by partial key; the
    # cleanest approach is to clear the whole cache. The other 9
    # entries reload from S3 on next use.
    _load_runner.cache_clear()
    return 1
