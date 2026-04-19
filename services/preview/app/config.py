"""Runtime settings loaded from environment variables."""
from __future__ import annotations

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="VAULTDMS_", env_file=".env", extra="ignore")

    # --- Service identity ---------------------------------------------------
    service_name: str = "preview"
    service_version: str = "dev"
    log_level: str = "INFO"
    port: int = 8080

    # --- Broker / cache -----------------------------------------------------
    celery_broker_url: str = "redis://redis:6379/1"
    celery_result_backend: str = "redis://redis:6379/2"
    redis_cache_url: str = "redis://redis:6379/3"

    # --- NATS --------------------------------------------------------------
    nats_url: str = "nats://nats:4222"
    nats_stream: str = "VAULTDMS"
    nats_consumer_durable: str = "preview-svc"
    nats_subject_uploaded: str = "dms.version.uploaded.v1"
    nats_subject_preview_ready: str = "dms.version.preview_ready.v1"

    # --- Object storage ----------------------------------------------------
    s3_endpoint: str = "http://minio:9000"
    s3_access_key: str = ""
    s3_secret_key: str = ""
    s3_use_ssl: bool = False
    s3_region: str = "us-east-1"
    # Buckets follow dms-{region}-{tier} — previews live in a tier of their own.
    preview_bucket_template: str = "dms-{region}-previews"
    # Presign TTL for REST redirects.
    preview_presign_ttl_seconds: int = 300

    # --- Processing limits -------------------------------------------------
    max_file_size_bytes: int = 500 * 1024 * 1024
    libreoffice_timeout_seconds: int = 60
    ffmpeg_timeout_seconds: int = 120
    preview_cache_ttl_seconds: int = 24 * 3600
    pdf_preview_pages: int = 10
    pdf_preview_width: int = 1200
    pdf_preview_dpi: int = 150
    image_preview_max_dim: int = 2000
    thumbnail_size: int = 256


settings = Settings()
