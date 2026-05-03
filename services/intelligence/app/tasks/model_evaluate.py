"""Model evaluation (ADR 0060) — compares a candidate against the
current production model on the held-out test split, decides whether
to mark it as `candidate` (admin promotes manually), `production`
(when auto-promote is on AND it beats the threshold), or `retired`
(below threshold).

Triggered by: app.tasks.model_retrain (after training completes)
Persists to:  model_versions (status + eval_metrics)
Emits:        dms.model.evaluated.v1
              dms.model.promoted.v1 (when auto-promoted)
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import tempfile
import time
import uuid
from collections import defaultdict
from datetime import datetime, timezone
from typing import Any

from app.config import settings
from app.error_classifier import classify_error_reason
from app.intel_dedupe import (
    already_completed,
    mark_completed,
    mark_enqueued,
    mark_failed,
    publish_dlq,
)
from app.worker import celery_app
from app.db.pool import get_pool

log = logging.getLogger(__name__)

EVALUATED_SUBJECT = "dms.model.evaluated.v1"
PROMOTED_SUBJECT  = "dms.model.promoted.v1"
CONSUMER = "model_evaluate"


@celery_app.task(
    name="app.tasks.model_evaluate.evaluate",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 1},
    retry_backoff=True,
    soft_time_limit=600,
    time_limit=900,
)
def evaluate(
    self,
    tenant_id: str,
    version_id: str,
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id, version_id=version_id,
        event_id=event_id, correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 1,
    ))


async def _run_async(
    *, tenant_id, version_id, event_id, correlation_id, attempt, is_terminal,
) -> dict:
    start = time.monotonic()
    if not (tenant_id and version_id):
        return {"status": "skipped", "reason": "missing args"}
    try:
        if event_id and await already_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ):
            return {"status": "duplicate"}
        await mark_enqueued(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
            document_id=version_id, version_id=version_id,
        )

        cfg = await _load_config(tenant_id)
        candidate = await _load_version(tenant_id, version_id)
        if not candidate:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "skipped", "reason": "version row missing"}

        production = await _load_production_version(tenant_id, candidate["model_type"])
        test = await _fetch_test_split(tenant_id)
        if not test:
            # Empty test set — fall back to "any candidate is acceptable"
            # per ADR 0060 (cold start case).
            metrics = {"accuracy": 0.0, "precision": 0.0, "recall": 0.0,
                       "f1": 0.0, "per_class": {},
                       "test_count": 0, "compared_to": None}
            await _persist_eval(tenant_id, version_id, metrics, "candidate")
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "completed", "outcome": "candidate",
                    "reason": "no test data"}

        # Run candidate (and production, if any) over test set.
        cand_metrics = await asyncio.get_running_loop().run_in_executor(
            None, _eval_model_on_test, tenant_id, candidate, test,
        )
        prod_metrics = None
        if production:
            prod_metrics = await asyncio.get_running_loop().run_in_executor(
                None, _eval_model_on_test, tenant_id, production, test,
            )

        # Compare + decide.
        outcome = _decide(cand_metrics, prod_metrics, cfg)

        cand_metrics["compared_to"] = (
            production["version_tag"] if production else None
        )
        cand_metrics["test_count"] = len(test)

        await _persist_eval(tenant_id, version_id, cand_metrics, outcome)

        # Auto-promotion path.
        promoted = False
        if outcome == "promote":
            await _promote(tenant_id, version_id,
                           prev_id=production["id"] if production else None)
            promoted = True
            outcome = "production"
        await _emit_promotion_event(tenant_id, version_id, outcome,
                                     correlation_id) if promoted else None

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "model_evaluate.completed",
            extra={
                "tenant_id": tenant_id,
                "version_id": version_id,
                "outcome": outcome,
                "elapsed_ms": elapsed_ms,
                "accuracy": cand_metrics.get("accuracy"),
            },
        )
        return {
            "status": "completed",
            "outcome": outcome,
            "metrics": cand_metrics,
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        if is_terminal:
            reason = classify_error_reason(exc)
            try:
                await publish_dlq(
                    consumer=CONSUMER, reason=reason, tenant_id=tenant_id,
                    document_id=version_id, version_id=version_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=attempt, correlation_id=correlation_id,
                )
                await mark_failed(
                    tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                )
            except Exception:
                log.exception("model_evaluate terminal DLQ publish failed")
        raise


# ---- decision logic ------------------------------------------------------

def _decide(cand: dict, prod: dict | None, cfg: dict) -> str:
    """Return one of: 'candidate' | 'promote' | 'retired'."""
    if prod is None:
        # Cold start — accept anything above 0.5 accuracy.
        return "candidate" if cand.get("accuracy", 0.0) >= 0.5 else "retired"
    improvement = float(cand.get("accuracy", 0.0)) - float(prod.get("accuracy", 0.0))
    if improvement < cfg["min_accuracy_improvement"]:
        return "retired"
    if cfg["auto_promote_if_better"]:
        return "promote"
    return "candidate"


# ---- model loading + inference -------------------------------------------

def _eval_model_on_test(tenant_id: str, version_row: dict, test: list[dict]) -> dict:
    """Download model from S3, run on test set, compute metrics.
    Heavy imports inside the function so the module can be loaded
    without torch/transformers."""
    import torch
    from transformers import AutoModelForSequenceClassification, AutoTokenizer

    local = tempfile.mkdtemp(prefix=f"vaultdms-eval-{version_row['version_tag']}-")
    _download_artifacts(version_row["s3_artifact_path"], local)

    label_map_path = os.path.join(local, "label_map.json")
    if not os.path.exists(label_map_path):
        raise RuntimeError(f"label_map.json missing in {version_row['s3_artifact_path']}")
    with open(label_map_path) as f:
        lm = json.load(f)
    id_to_label = {int(k): v for k, v in lm["id_to_label"].items()}

    tokenizer = AutoTokenizer.from_pretrained(local)
    model = AutoModelForSequenceClassification.from_pretrained(local)
    model.eval()

    correct = 0
    per_class_tp: dict[str, int] = defaultdict(int)
    per_class_fp: dict[str, int] = defaultdict(int)
    per_class_fn: dict[str, int] = defaultdict(int)

    with torch.no_grad():
        for ex in test:
            enc = tokenizer(ex["text_content"], truncation=True,
                            padding="max_length", max_length=256,
                            return_tensors="pt")
            logits = model(**enc).logits
            pred_id = int(torch.argmax(logits, dim=-1).item())
            pred = id_to_label.get(pred_id, "UNKNOWN")
            true = ex["label"]
            if pred == true:
                correct += 1
                per_class_tp[true] += 1
            else:
                per_class_fp[pred] += 1
                per_class_fn[true] += 1

    accuracy = correct / max(1, len(test))
    classes = set(per_class_tp) | set(per_class_fp) | set(per_class_fn)
    per_class: dict[str, dict[str, float]] = {}
    macro_p = macro_r = macro_f = 0.0
    for c in classes:
        tp = per_class_tp.get(c, 0)
        fp = per_class_fp.get(c, 0)
        fn = per_class_fn.get(c, 0)
        p = tp / max(1, tp + fp)
        r = tp / max(1, tp + fn)
        f = 2 * p * r / max(1e-9, p + r)
        per_class[c] = {"precision": round(p, 4),
                        "recall":    round(r, 4),
                        "f1":        round(f, 4)}
        macro_p += p
        macro_r += r
        macro_f += f
    n = max(1, len(classes))
    return {
        "accuracy":  round(accuracy, 4),
        "precision": round(macro_p / n, 4),
        "recall":    round(macro_r / n, 4),
        "f1":        round(macro_f / n, 4),
        "per_class": per_class,
    }


def _download_artifacts(s3_path: str, local_dir: str) -> None:
    """Mirror s3://bucket/prefix/* into local_dir."""
    import boto3
    from botocore.client import Config

    if not s3_path.startswith("s3://"):
        raise ValueError(f"unexpected s3_path: {s3_path}")
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
            if not name:
                continue
            s3.download_file(bucket, key, os.path.join(local_dir, name))


# ---- DB helpers ----------------------------------------------------------

async def _load_config(tenant_id: str) -> dict:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT auto_promote_if_better, min_accuracy_improvement
                  FROM active_learning_config WHERE tenant_id = $1
                """,
                tenant_id,
            )
    if not row:
        return {"auto_promote_if_better": False, "min_accuracy_improvement": 0.02}
    return {
        "auto_promote_if_better":   row["auto_promote_if_better"],
        "min_accuracy_improvement": float(row["min_accuracy_improvement"]),
    }


async def _load_version(tenant_id: str, version_id: str) -> dict | None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT id::text, model_type, version_tag, s3_artifact_path
                  FROM model_versions
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, version_id,
            )
    return dict(row) if row else None


async def _load_production_version(tenant_id: str, model_type: str) -> dict | None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT id::text, model_type, version_tag, s3_artifact_path
                  FROM model_versions
                 WHERE tenant_id = $1 AND model_type = $2 AND status = 'production'
                """,
                tenant_id, model_type,
            )
    return dict(row) if row else None


async def _fetch_test_split(tenant_id: str) -> list[dict]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT text_content, label FROM training_examples
                 WHERE tenant_id = $1 AND split = 'test'
                """,
                tenant_id,
            )
    return [{"text_content": r["text_content"], "label": r["label"]} for r in rows]


async def _persist_eval(tenant_id: str, version_id: str, metrics: dict,
                         outcome: str) -> None:
    """Outcome of 'promote' is stored as 'evaluating' here — _promote
    flips it to 'production' atomically with the previous-prod retire."""
    new_status = {"candidate": "candidate", "retired": "retired",
                  "promote": "evaluating"}.get(outcome, "candidate")
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE model_versions
                   SET eval_metrics = $3::jsonb,
                       status = $4,
                       updated_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, version_id, json.dumps(metrics), new_status,
            )
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'model_version', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                EVALUATED_SUBJECT, version_id,
                json.dumps({
                    "tenant_id": tenant_id, "version_id": version_id,
                    "outcome": outcome, "metrics": metrics,
                    "emitted_at": datetime.now(timezone.utc).isoformat(),
                }),
            )


async def _promote(tenant_id: str, version_id: str, prev_id: str | None) -> None:
    """Atomic swap — retire previous production AND mark new one
    production in one tx. The partial unique index on
    status='production' would reject any non-atomic attempt."""
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            if prev_id:
                await conn.execute(
                    """
                    UPDATE model_versions
                       SET status = 'retired', retired_at = NOW(), updated_at = NOW()
                     WHERE tenant_id = $1 AND id = $2 AND status = 'production'
                    """,
                    tenant_id, prev_id,
                )
            await conn.execute(
                """
                UPDATE model_versions
                   SET status = 'production',
                       promoted_at = NOW(),
                       updated_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, version_id,
            )


async def _emit_promotion_event(tenant_id: str, version_id: str,
                                 outcome: str, correlation_id: str) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'model_version', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                PROMOTED_SUBJECT, version_id,
                json.dumps({
                    "tenant_id": tenant_id, "version_id": version_id,
                    "outcome": outcome, "auto_promoted": True,
                    "correlation_id": correlation_id,
                    "emitted_at": datetime.now(timezone.utc).isoformat(),
                }),
            )
