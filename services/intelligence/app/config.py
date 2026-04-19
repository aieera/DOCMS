from __future__ import annotations
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="VAULTDMS_", env_file=".env", extra="ignore")

    service_name: str = "intelligence"
    service_version: str = "dev"
    log_level: str = "INFO"
    port: int = 8080

    celery_broker_url: str = "redis://redis:6379/1"
    celery_result_backend: str = "redis://redis:6379/2"
    redis_cache_url: str = "redis://redis:6379/3"

    nats_url: str = "nats://nats:4222"
    nats_stream: str = "VAULTDMS"

    s3_endpoint: str = "http://minio:9000"
    s3_access_key: str = ""
    s3_secret_key: str = ""
    s3_use_ssl: bool = False
    s3_region: str = "us-east-1"

    database_url: str = "postgresql://vaultdms:secret@postgres:5432/vaultdms"

    qdrant_url: str = "http://qdrant:6333"
    qdrant_collection: str = "dms_vectors"

    opensearch_url: str = "http://opensearch:9200"
    opensearch_username: str = ""
    opensearch_password: str = ""

    embedding_model: str = "sentence-transformers/all-MiniLM-L6-v2"
    embedding_dim: int = 384
    reranker_model: str = "cross-encoder/ms-marco-MiniLM-L-6-v2"
    classifier_model: str = "distilbert-base-uncased"

    default_llm_model: str = "gpt-4o-mini"
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
