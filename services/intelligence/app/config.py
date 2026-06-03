from __future__ import annotations
import os as _os
from pydantic_settings import BaseSettings, SettingsConfigDict

# Back-compat: the env prefix was renamed VAULTDMS_ -> SEDOC_. Mirror any
# legacy VAULTDMS_* vars onto their SEDOC_* names when unset so pre-rename
# environments keep working. Remove once everything injects SEDOC_* directly.
for _k, _v in list(_os.environ.items()):
    if _k.startswith("VAULTDMS_"):
        _os.environ.setdefault("SEDOC_" + _k[len("VAULTDMS_"):], _v)


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="SEDOC_", env_file=".env", extra="ignore")

    service_name: str = "intelligence"
    service_version: str = "dev"
    log_level: str = "INFO"
    port: int = 8080

    celery_broker_url: str = "redis://redis:6379/1"
    celery_result_backend: str = "redis://redis:6379/2"
    redis_cache_url: str = "redis://redis:6379/3"

    nats_url: str = "nats://nats:4222"
    nats_stream: str = "SEDOC"

    s3_endpoint: str = "http://minio:9000"
    s3_access_key: str = ""
    s3_secret_key: str = ""
    s3_use_ssl: bool = False
    s3_region: str = "us-east-1"

    database_url: str = "postgresql://sedoc:secret@postgres:5432/sedoc"

    qdrant_url: str = "http://qdrant:6333"
    qdrant_collection: str = "dms_vectors"

    opensearch_url: str = "http://opensearch:9200"
    opensearch_username: str = ""
    opensearch_password: str = ""

    embedding_model: str = "sentence-transformers/all-MiniLM-L6-v2"
    embedding_dim: int = 384
    reranker_model: str = "cross-encoder/ms-marco-MiniLM-L-6-v2"
    classifier_model: str = "distilbert-base-uncased"

    # Default model used by RAG (Doc Q&A) and other litellm-routed paths
    # when no per-tenant override exists in Redis (llm_config:{tenant_id}).
    # Pydantic-settings reads SEDOC_DEFAULT_LLM_MODEL from env, so an
    # operator can override per-deployment without touching code.
    #
    # The "anthropic/" prefix forces litellm to use the Messages API
    # (/v1/messages) instead of the deprecated text-completion endpoint —
    # litellm 1.16.0 (pinned in requirements.txt) routes bare "claude-*"
    # IDs to /v1/complete, which Anthropic deprecated in 2024 and now
    # rejects with "endpoint deprecated". Explicit provider prefix
    # bypasses the legacy router.
    default_llm_model: str = "anthropic/claude-haiku-4-5"
    llm_timeout_seconds: int = 30
    llm_max_concurrent_per_tenant: int = 10

    ocr_gpu: bool = False
    ocr_confidence_threshold: float = 0.7
    ocr_per_tenant_cap: int = 8
    ocr_page_timeout_seconds: int = 90
    ocr_total_timeout_seconds: int = 1800  # 30 min hard abort
    ocr_max_retries: int = 3
    ocr_retry_base_seconds: float = 5.0
    ocr_retry_jitter: float = 0.2
    chunk_size_tokens: int = 512
    chunk_overlap_tokens: int = 64


settings = Settings()
