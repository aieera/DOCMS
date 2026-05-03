"""DistilBERT fine-tuning per-tenant (ADR 0060).

Triggered by: app.tasks.training_collector when count threshold met
              (or POST /api/v1/admin/models/retrain)
Persists to:  model_versions (status='training' → 'evaluating')
Uploads to:   MinIO under models/{tenant}/{model_type}/{version}/
Emits:        dms.model.trained.v1
Dispatches:   model_evaluate

Long-running. Runs on the configured GPU queue (default
intelligence-gpu). Soft 1h limit, hard 1h5m. The training loop
itself is real (transformers Trainer); the test suite stubs it.

Requires the existing `transformers` and `torch` deps in
services/intelligence/requirements.txt — already there for the
embedder + classifier.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import tempfile
import time
import uuid
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

MODEL_TRAINED_SUBJECT = "dms.model.trained.v1"
CONSUMER = "model_retrain"

BASE_MODEL = "distilbert-base-uncased"
EPOCHS = 5
BATCH_SIZE = 16
LEARNING_RATE = 2e-5
WEIGHT_DECAY = 0.01
EARLY_STOPPING_PATIENCE = 2

# MinIO bucket where model artifacts live. Each version writes
# multiple files into models/{tenant}/{model_type}/{version}/.
MODELS_BUCKET_ENV = "VAULTDMS_MODELS_BUCKET"
DEFAULT_MODELS_BUCKET = "vaultdms-models"


@celery_app.task(
    name="app.tasks.model_retrain.retrain",
    bind=True,
    acks_late=True,
    autoretry_for=(Exception,),
    retry_kwargs={"max_retries": 1},
    retry_backoff=True,
    soft_time_limit=3600,
    time_limit=3900,
)
def retrain(
    self,
    tenant_id: str,
    model_type: str = "classification",
    event_id: str = "",
    correlation_id: str = "",
):
    return asyncio.run(_run_async(
        tenant_id=tenant_id, model_type=model_type,
        event_id=event_id, correlation_id=correlation_id,
        attempt=self.request.retries + 1,
        is_terminal=self.request.retries >= 1,
    ))


async def _run_async(
    *, tenant_id, model_type, event_id, correlation_id, attempt, is_terminal,
) -> dict:
    start = time.monotonic()

    if not tenant_id:
        return {"status": "skipped", "reason": "no tenant_id"}
    try:
        if event_id and await already_completed(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id
        ):
            return {"status": "duplicate", "event_id": event_id}
        await mark_enqueued(
            tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
            document_id=tenant_id, version_id=tenant_id,
        )

        # 1. Load training + validation examples (RLS-scoped).
        train, val, test = await _fetch_examples(tenant_id)
        if len(train) < 10:
            await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
            return {"status": "skipped", "reason": "insufficient training examples",
                    "train_count": len(train)}

        # 2. Compute next version tag.
        version_tag = await _next_version_tag(tenant_id, model_type)

        # 3. Insert model_versions row with status='training'.
        s3_path = _s3_path(tenant_id, model_type, version_tag)
        version_id = await _insert_version_row(
            tenant_id=tenant_id, model_type=model_type, version_tag=version_tag,
            s3_path=s3_path, training_count=len(train),
        )

        # 4. Train the model. The training function is real but
        #    isolated so the test suite can patch it; on a fresh
        #    tenant install with no GPU it'll still run on CPU
        #    (slowly).
        try:
            metrics = await asyncio.get_running_loop().run_in_executor(
                None, _do_finetune, train, val,
            )
        except Exception as exc:
            await _mark_version_failed(tenant_id, version_id, str(exc)[:1000])
            raise

        # 5. Upload artifacts to MinIO.
        try:
            await asyncio.get_running_loop().run_in_executor(
                None, _upload_artifacts, metrics["local_dir"], tenant_id,
                model_type, version_tag,
            )
        except Exception as exc:
            await _mark_version_failed(tenant_id, version_id,
                                        f"upload failed: {exc}"[:1000])
            raise

        # 6. Mark version as 'evaluating' + record training metrics.
        await _persist_training_done(
            tenant_id=tenant_id, version_id=version_id,
            training_metrics={
                "loss":       metrics.get("loss"),
                "accuracy":   metrics.get("accuracy"),
                "f1":         metrics.get("f1"),
                "epochs":     EPOCHS,
                "duration_s": metrics.get("duration_s"),
            },
            correlation_id=correlation_id,
        )

        # 7. Mark training examples as used.
        await _mark_examples_used(tenant_id, version_tag,
                                   [e["id"] for e in train + val + test])

        # 8. Dispatch evaluator.
        from app.tasks.model_evaluate import evaluate
        evaluate.apply_async(
            kwargs={
                "tenant_id": tenant_id,
                "version_id": version_id,
                "correlation_id": correlation_id,
                "event_id": str(uuid.uuid4()),
            },
            queue="intelligence",
        )

        await mark_completed(tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id)
        elapsed_ms = int((time.monotonic() - start) * 1000)
        log.info(
            "model_retrain.completed",
            extra={
                "tenant_id": tenant_id,
                "version_tag": version_tag,
                "training_count": len(train),
                "elapsed_ms": elapsed_ms,
            },
        )
        return {
            "status": "completed",
            "version_id": version_id,
            "version_tag": version_tag,
            "training_count": len(train),
            "elapsed_ms": elapsed_ms,
        }
    except Exception as exc:
        if is_terminal:
            reason = classify_error_reason(exc)
            try:
                await publish_dlq(
                    consumer=CONSUMER, reason=reason, tenant_id=tenant_id,
                    document_id=tenant_id, version_id=tenant_id,
                    event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                    attempts=attempt, correlation_id=correlation_id,
                )
                await mark_failed(
                    tenant_id=tenant_id, consumer=CONSUMER, event_id=event_id,
                    error=f"{type(exc).__name__}: {exc}",
                )
            except Exception:
                log.exception("model_retrain terminal DLQ publish failed")
        raise


# ---- training (real, but stub-friendly) ----------------------------------

def _do_finetune(train: list[dict], val: list[dict]) -> dict:
    """Fine-tune DistilBERT. Returns metrics + local_dir of saved
    artifacts. Heavy imports are inside the function so the module
    can be loaded without torch/transformers (e.g. in tests)."""
    start = time.monotonic()
    import torch
    from transformers import (
        AutoModelForSequenceClassification,
        AutoTokenizer,
        Trainer,
        TrainingArguments,
        EarlyStoppingCallback,
    )
    from torch.utils.data import Dataset

    labels_sorted = sorted({e["label"] for e in train + val})
    label_to_id = {lab: i for i, lab in enumerate(labels_sorted)}
    id_to_label = {i: lab for lab, i in label_to_id.items()}

    tokenizer = AutoTokenizer.from_pretrained(BASE_MODEL)

    class _DS(Dataset):
        def __init__(self, rows: list[dict]):
            self.rows = rows
        def __len__(self): return len(self.rows)
        def __getitem__(self, i):
            r = self.rows[i]
            enc = tokenizer(r["text_content"], truncation=True,
                             padding="max_length", max_length=256,
                             return_tensors="pt")
            return {
                "input_ids":      enc["input_ids"].squeeze(0),
                "attention_mask": enc["attention_mask"].squeeze(0),
                "labels":         torch.tensor(label_to_id[r["label"]]),
            }

    model = AutoModelForSequenceClassification.from_pretrained(
        BASE_MODEL, num_labels=len(labels_sorted),
        id2label=id_to_label, label2id=label_to_id,
    )

    out_dir = tempfile.mkdtemp(prefix="vaultdms-finetune-")
    args = TrainingArguments(
        output_dir=out_dir,
        num_train_epochs=EPOCHS,
        per_device_train_batch_size=BATCH_SIZE,
        per_device_eval_batch_size=BATCH_SIZE,
        learning_rate=LEARNING_RATE,
        weight_decay=WEIGHT_DECAY,
        warmup_ratio=0.1,
        evaluation_strategy="epoch" if val else "no",
        save_strategy="epoch" if val else "no",
        load_best_model_at_end=bool(val),
        metric_for_best_model="loss",
        greater_is_better=False,
        logging_steps=50,
        report_to=[],
    )
    trainer = Trainer(
        model=model, args=args,
        train_dataset=_DS(train),
        eval_dataset=_DS(val) if val else None,
        callbacks=[EarlyStoppingCallback(early_stopping_patience=EARLY_STOPPING_PATIENCE)] if val else [],
    )
    trainer.train()

    eval_loss = None
    eval_acc = None
    if val:
        eval_out = trainer.evaluate()
        eval_loss = float(eval_out.get("eval_loss", 0.0))

    final_dir = os.path.join(out_dir, "final")
    trainer.save_model(final_dir)
    tokenizer.save_pretrained(final_dir)
    with open(os.path.join(final_dir, "label_map.json"), "w") as f:
        json.dump({"id_to_label": id_to_label, "label_to_id": label_to_id}, f)

    return {
        "loss":       eval_loss,
        "accuracy":   eval_acc,    # populated by model_evaluate on the test set
        "f1":         None,
        "duration_s": int(time.monotonic() - start),
        "local_dir":  final_dir,
    }


# ---- MinIO upload --------------------------------------------------------

def _s3_path(tenant_id: str, model_type: str, version_tag: str) -> str:
    bucket = os.getenv(MODELS_BUCKET_ENV, DEFAULT_MODELS_BUCKET)
    return f"s3://{bucket}/models/{tenant_id}/{model_type}/{version_tag}/"


def _upload_artifacts(local_dir: str, tenant_id: str, model_type: str,
                       version_tag: str) -> None:
    """Upload every file in local_dir to s3://bucket/models/.../{version_tag}/.
    Uses the existing settings.s3_endpoint + creds."""
    import boto3
    from botocore.client import Config

    bucket = os.getenv(MODELS_BUCKET_ENV, DEFAULT_MODELS_BUCKET)
    prefix = f"models/{tenant_id}/{model_type}/{version_tag}/"
    s3 = boto3.client(
        "s3",
        endpoint_url=getattr(settings, "s3_endpoint", None) or os.getenv("AWS_ENDPOINT_URL"),
        aws_access_key_id=os.getenv("AWS_ACCESS_KEY_ID"),
        aws_secret_access_key=os.getenv("AWS_SECRET_ACCESS_KEY"),
        config=Config(signature_version="s3v4"),
        region_name=getattr(settings, "s3_region", None) or os.getenv("AWS_REGION", "us-east-1"),
    )
    # Best-effort bucket create.
    try:
        s3.head_bucket(Bucket=bucket)
    except Exception:
        try:
            s3.create_bucket(Bucket=bucket)
        except Exception:
            pass

    for name in os.listdir(local_dir):
        path = os.path.join(local_dir, name)
        if os.path.isfile(path):
            s3.upload_file(path, bucket, prefix + name)


# ---- DB helpers ----------------------------------------------------------

async def _fetch_examples(tenant_id: str) -> tuple[list[dict], list[dict], list[dict]]:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            rows = await conn.fetch(
                """
                SELECT id::text, text_content, label, split
                  FROM training_examples
                 WHERE tenant_id = $1
                """,
                tenant_id,
            )
    train, val, test = [], [], []
    for r in rows:
        rec = {"id": r["id"], "text_content": r["text_content"], "label": r["label"]}
        if r["split"] == "validation":
            val.append(rec)
        elif r["split"] == "test":
            test.append(rec)
        else:
            train.append(rec)
    return train, val, test


async def _next_version_tag(tenant_id: str, model_type: str) -> str:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            row = await conn.fetchrow(
                """
                SELECT version_tag FROM model_versions
                 WHERE tenant_id = $1 AND model_type = $2
                 ORDER BY created_at DESC LIMIT 1
                """,
                tenant_id, model_type,
            )
    if not row:
        return "v1.0.0"
    return _bump_patch(row["version_tag"])


def _bump_patch(tag: str) -> str:
    """v1.0.3 → v1.0.4. Falls back to vN+1 if not parseable."""
    if tag.startswith("v"):
        body = tag[1:]
    else:
        body = tag
    parts = body.split(".")
    try:
        nums = [int(p) for p in parts]
        nums[-1] += 1
        return "v" + ".".join(str(n) for n in nums)
    except (ValueError, IndexError):
        return "v" + str(int(time.time()))


async def _insert_version_row(
    *, tenant_id: str, model_type: str, version_tag: str,
    s3_path: str, training_count: int,
) -> str:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            new_id = uuid.uuid4()
            await conn.execute(
                """
                INSERT INTO model_versions
                    (tenant_id, id, model_type, version_tag, base_model,
                     s3_artifact_path, training_examples_count, status)
                VALUES ($1, $2, $3, $4, $5, $6, $7, 'training')
                """,
                tenant_id, new_id, model_type, version_tag, BASE_MODEL,
                s3_path, training_count,
            )
            return str(new_id)


async def _mark_version_failed(tenant_id: str, version_id: str, error: str) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE model_versions
                   SET status = 'failed', error_message = $3, updated_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, version_id, error,
            )


async def _persist_training_done(
    *, tenant_id: str, version_id: str, training_metrics: dict,
    correlation_id: str,
) -> None:
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE model_versions
                   SET status = 'evaluating',
                       training_metrics = $3::jsonb,
                       updated_at = NOW()
                 WHERE tenant_id = $1 AND id = $2
                """,
                tenant_id, version_id, json.dumps(training_metrics),
            )
            await conn.execute(
                """
                INSERT INTO outbox
                    (id, tenant_id, event_type, aggregate_type, aggregate_id,
                     payload, created_at)
                VALUES ($1, $2, $3, 'model_version', $4, $5::jsonb, NOW())
                """,
                uuid.uuid4(), tenant_id,
                MODEL_TRAINED_SUBJECT, version_id,
                json.dumps({
                    "tenant_id": tenant_id,
                    "version_id": version_id,
                    "training_metrics": training_metrics,
                    "correlation_id": correlation_id,
                    "emitted_at": datetime.now(timezone.utc).isoformat(),
                }),
            )


async def _mark_examples_used(tenant_id: str, version_tag: str, ids: list[str]) -> None:
    if not ids:
        return
    pool = await get_pool()
    async with pool.acquire() as conn:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_tenant', $1, true)", tenant_id
            )
            await conn.execute(
                """
                UPDATE training_examples
                   SET used_in_version = $3
                 WHERE tenant_id = $1 AND id = ANY($2::uuid[])
                """,
                tenant_id, ids, version_tag,
            )
