"""PII redaction — detect candidates, then apply to PDF via PyMuPDF."""
from __future__ import annotations

import logging
import os
import shutil
import tempfile

import fitz

from app.config import settings
from app.tasks.ner import detect_entities
from app.worker import celery_app

log = logging.getLogger(__name__)


def _s3():
    import boto3
    from botocore.client import Config
    return boto3.client(
        "s3", endpoint_url=settings.s3_endpoint,
        aws_access_key_id=settings.s3_access_key or None,
        aws_secret_access_key=settings.s3_secret_key or None,
        use_ssl=settings.s3_use_ssl,
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
    )


@celery_app.task(name="app.tasks.redact.detect_redaction_candidates", bind=True)
def detect_redaction_candidates(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    text: str,
    entity_types: list[str] | None = None,
):
    result = detect_entities.apply(
        args=[tenant_id, document_id, version_id, text]
    ).get(timeout=120)

    candidates = result.get("entities", [])
    if entity_types:
        allowed = set(t.upper() for t in entity_types)
        candidates = [e for e in candidates if e["entity_type"].upper() in allowed]

    return {
        "tenant_id": tenant_id,
        "document_id": document_id,
        "version_id": version_id,
        "candidates": candidates,
        "count": len(candidates),
    }


@celery_app.task(name="app.tasks.redact.apply_redactions", bind=True)
def apply_redactions(
    self,
    tenant_id: str,
    document_id: str,
    version_id: str,
    storage_bucket: str,
    storage_key: str,
    entities: list[dict],
    output_bucket: str | None = None,
):
    workdir = tempfile.mkdtemp(prefix="redact-")
    src = os.path.join(workdir, "source.pdf")
    out = os.path.join(workdir, "redacted.pdf")

    try:
        _s3().download_file(storage_bucket, storage_key, src)
        doc = fitz.open(src)

        for entity in entities:
            value = entity.get("entity_value", "")
            if not value:
                continue
            for page in doc:
                areas = page.search_for(value)
                for rect in areas:
                    page.add_redact_annot(rect, fill=(0, 0, 0))
            for page in doc:
                page.apply_redactions()

        doc.save(out)
        doc.close()

        bucket = output_bucket or storage_bucket
        redacted_key = storage_key.rsplit("/", 1)[0] + "/redacted.pdf"
        _s3().upload_file(out, bucket, redacted_key, ExtraArgs={"ContentType": "application/pdf"})

        return {
            "status": "completed",
            "tenant_id": tenant_id,
            "document_id": document_id,
            "version_id": version_id,
            "redacted_key": redacted_key,
            "redacted_bucket": bucket,
            "entities_redacted": len(entities),
        }
    finally:
        shutil.rmtree(workdir, ignore_errors=True)
