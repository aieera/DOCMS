# intelligence

Python + Celery workers for OCR, classification, NER, embeddings, RAG.
FastAPI front door for ad-hoc requests (`/intelligence/ask`).

## Responsibilities

- OCR (Surya primary, PyMuPDF fast-path for PDFs that already have
  text). Writes to `ocr_results`, emits
  `dms.version.ocr_completed.v1` — see
  [remediation 04a](../../docs/audit/remediation/04a-ocr-pipeline.md).
- Document classification via distilbert.
- Named-entity recognition (spaCy + regex fallback for common PII).
- Sentence embeddings (all-MiniLM-L6-v2) → Qdrant.
- RAG-backed `/intelligence/ask` via LiteLLM (model selectable per
  tenant).
- Redact-pass that identifies PII for manual review.

## API surface

FastAPI:

- `POST /intelligence/ask` — RAG Q&A
- `POST /intelligence/summarize`
- `POST /intelligence/redact/detect`
- `GET /healthz`, `GET /metrics` (Prometheus)

NATS consumers:

- `dms.version.uploaded.v1` → enqueue `process_ocr` Celery task
- `dms.version.ocr_completed.v1` → fan out to classify / NER /
  embed / duplicate-detect tasks

## Dependencies

- **Postgres** tables: `ocr_results`, `extraction_results`,
  `entities`, `document_chunks`, `document_fingerprints`.
- **Qdrant** collection `dms_vectors` (dim 384).
- **Redis** (DB 1): Celery broker + result backend.
- **S3** (dev: MinIO) for downloading version content.
- **LLM providers** via LiteLLM (OpenAI, Anthropic, Ollama, …).

## Configuration

Uses its own `pydantic-settings`-based config (`app/config.py`):

- `VAULTDMS_DATABASE_URL`, `_NATS_URL`, `_QDRANT_URL`, `_CELERY_BROKER_URL`
- `VAULTDMS_S3_ENDPOINT`, `_S3_ACCESS_KEY`, `_S3_SECRET_KEY`
- `VAULTDMS_DEFAULT_LLM_MODEL` (default `gpt-4o-mini`)
- `VAULTDMS_OCR_CONFIDENCE_THRESHOLD` (default 0.7)

## Running locally

```bash
make up
cd services/intelligence
pip install -r requirements.txt
# API server
uvicorn app.main:app --reload --port 8080
# Celery worker (separate terminal)
celery -A app.worker worker -Q intelligence,intelligence-ocr -l info
```

Via compose:

```bash
docker compose --profile app up -d intelligence-worker
```

## Testing

```bash
cd services/intelligence && pytest
# OCR pipeline tests (some need Docker for testcontainers):
pytest tests/test_ocr_pipeline.py -v
```

## Deployment

`deploy/helm/vaultdms/templates/intelligence/` — deployment (API +
worker), service, hpa (API HPA + worker HPA), pdb, networkpolicy,
servicemonitor. In prod the worker HPA should be driven by queue
depth via KEDA; CPU is the stopgap.

## Metrics

Exported at `GET /metrics` (Prometheus format):

- `ocr_pages_processed_total{engine,language}`
- `ocr_processing_seconds{engine}` (histogram)
- `ocr_errors_total{error_type}`
- `ocr_publish_failed_total`

## Troubleshooting

**OCR runs but search never sees the text** — the
`dms.version.ocr_completed.v1` publish is failing. Check
`ocr_publish_failed_total` and the Celery task logs. The
ocr_results DB row is always written first, so the text can be
recovered by re-publishing:
`SELECT id FROM ocr_results WHERE version_id = ...`.

**Surya OOMs on large scans** — bump `celery.resources.limits.memory`
or drop DPI in `_ocr_pdf_pages`.
