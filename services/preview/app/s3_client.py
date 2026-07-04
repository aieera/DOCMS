"""Thin MinIO/S3 wrapper used by tasks and the REST layer."""
from __future__ import annotations

import logging
from typing import BinaryIO

import boto3
from botocore.client import Config

from app.config import settings

log = logging.getLogger(__name__)


def _client():
    return boto3.client(
        "s3",
        endpoint_url=settings.s3_endpoint,
        aws_access_key_id=settings.s3_access_key or None,
        aws_secret_access_key=settings.s3_secret_key or None,
        use_ssl=settings.s3_use_ssl,
        region_name=settings.s3_region,
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
    )


class S3:
    """Small facade — every call creates a fresh client to stay thread-safe
    across Celery prefork workers and FastAPI's thread pool."""

    def download_to(self, bucket: str, key: str, dest_path: str) -> None:
        _client().download_file(bucket, key, dest_path)

    def get_bytes(self, bucket: str, key: str) -> bytes:
        """Read an object fully into memory. Used by the watermark stamper to
        fetch the cached base page image before overlaying identity."""
        return _client().get_object(Bucket=bucket, Key=key)["Body"].read()

    def upload_file(self, src_path: str, bucket: str, key: str, content_type: str) -> None:
        _client().upload_file(
            Filename=src_path,
            Bucket=bucket,
            Key=key,
            ExtraArgs={"ContentType": content_type},
        )

    def upload_fileobj(self, fileobj: BinaryIO, bucket: str, key: str, content_type: str) -> None:
        _client().upload_fileobj(
            Fileobj=fileobj,
            Bucket=bucket,
            Key=key,
            ExtraArgs={"ContentType": content_type},
        )

    def presigned_get(self, bucket: str, key: str, ttl_seconds: int) -> str:
        return _client().generate_presigned_url(
            "get_object",
            Params={"Bucket": bucket, "Key": key},
            ExpiresIn=ttl_seconds,
        )

    def head(self, bucket: str, key: str) -> dict:
        return _client().head_object(Bucket=bucket, Key=key)

    def ensure_bucket(self, bucket: str) -> None:
        c = _client()
        try:
            c.head_bucket(Bucket=bucket)
        except Exception:
            c.create_bucket(Bucket=bucket)


s3 = S3()


def preview_bucket(region: str) -> str:
    return settings.preview_bucket_template.format(region=region)


def preview_key(tenant_id: str, document_id: str, version_id: str, filename: str) -> str:
    return f"{tenant_id}/{document_id}/{version_id}/{filename}"
