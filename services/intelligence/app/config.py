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
    # Minimum confidence before a classification is written onto
    # documents.document_class. Below it the result still lands in
    # document_classifications as a suggestion for human review; the
    # document stays unclassified. Matches auto_tag's auto_apply_threshold
    # so "auto-apply" means the same thing across the intelligence surface.
    #
    # Do NOT lower this to make dashboards look classified — a wrong
    # document_class silently changes extraction profiles, routing rules
    # and retention.
    classify_auto_apply_threshold: float = 0.85

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
    # Per-request LLM budget, including streamed generations. 30s cut long
    # doc-QA answers mid-stream on slower models (litellm.Timeout fires
    # even while tokens are flowing) — the client saw a mid-sentence stop
    # with no error. 120s is generous headroom for a full answer while
    # still bounding a hung provider.
    llm_timeout_seconds: int = 120
    llm_max_concurrent_per_tenant: int = 10

    ocr_gpu: bool = False
    ocr_confidence_threshold: float = 0.7
    # When a Surya page comes back below this floor, re-run it through the
    # PaddleOCR fallback and keep whichever engine scored higher. Set <= 0 to
    # disable the fallback entirely.
    ocr_paddle_fallback_threshold: float = 0.6
    ocr_per_tenant_cap: int = 8
    # OCR engine selection. ocr_default_engine is the tenant-agnostic fallback
    # ("auto" | "printed" | "handwriting") used when neither the event nor a
    # per-tenant/doc-type override picks one. "auto" runs the printed engines
    # and, when a page lands below ocr_handwriting_threshold (and autodetect is
    # on), re-runs that page through TrOCR (ICR) and keeps whichever scores
    # higher — so handwriting-heavy scans get materially better text without a
    # manual flag. "handwriting" forces TrOCR; "printed" forces Surya/Paddle.
    ocr_default_engine: str = "auto"
    ocr_trocr_model: str = "microsoft/trocr-base-handwritten"
    ocr_handwriting_autodetect: bool = True
    ocr_handwriting_threshold: float = 0.65
    # Load the Surya detection + recognition weights when the OCR worker
    # comes up instead of on the first document. Measured cold-start on a
    # CPU worker is ~99s, which the first user's upload pays in full and
    # which dominated a 91.7s end-to-end for a 43 KB image. Preloading
    # moves it to worker boot. Only workers subscribed to the
    # `intelligence-ocr` queue preload; set false to restore lazy loading.
    ocr_preload_models: bool = True
    ocr_page_timeout_seconds: int = 90
    ocr_total_timeout_seconds: int = 1800  # 30 min hard abort
    ocr_max_retries: int = 3
    ocr_retry_base_seconds: float = 5.0
    ocr_retry_jitter: float = 0.2
    chunk_size_tokens: int = 512
    chunk_overlap_tokens: int = 64

    # RAG retrieval budget. `rag_top_k` chunks survive re-ranking and are
    # what the model actually sees; `rag_rerank_candidates` is the pool the
    # cross-encoder scores. 8 x 512-token chunks = ~4k tokens, comfortably
    # inside rag_context_max_tokens so a sane top-k is never silently
    # trimmed by the context budget.
    rag_top_k: int = 8
    rag_rerank_candidates: int = 20
    rag_context_max_tokens: int = 6000

    # Structured field extraction (Workstream "Activate structured field
    # extraction"). When the per-field regex coverage for a document falls
    # below this floor, fall back to the LLM extractor for the missing fields.
    extraction_enabled: bool = True
    extraction_llm_fallback_threshold: float = 0.8
    # Per-field confidence assigned to a clean regex hit vs an LLM-supplied
    # value. Regex on a labelled field is a strong-but-not-certain signal;
    # the LLM is broader but unverifiable, so neither is 1.0.
    extraction_regex_confidence: float = 0.72
    extraction_llm_confidence: float = 0.86

    # ADR 0104 Phase 2 — minimum cosine score for a clause-library match
    # against a document chunk.
    clause_match_threshold: float = 0.80


settings = Settings()
