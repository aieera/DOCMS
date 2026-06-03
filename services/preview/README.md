# preview

Thumbnail + preview rendering (PDF to image, Office to PDF, video to
poster + hls) via Celery workers.

## Responsibilities

- Generate PDF thumbnails (poppler) on `dms.version.uploaded.v1`.
- Convert Office (docx/xlsx/pptx) to PDF via headless LibreOffice.
- Produce HLS playlists for video (ffmpeg) + a poster thumbnail.
- Write output to the `{tenant}-previews` S3 bucket.

## API surface

FastAPI:

- `POST /preview/generate` — force regeneration for a version
- `GET /preview/{version_id}?size={small|medium|large}` — serve
  cached preview
- `GET /healthz`, `GET /metrics`

NATS consumer: `dms.version.uploaded.v1` → Celery task enqueue.

## Dependencies

- **S3** preview bucket (`{region}-previews`).
- **Redis** (DB 2): Celery broker.
- **Postgres**: writes `previews` metadata rows (size, duration,
  thumb_key).
- **poppler**, **libreoffice**, **ffmpeg** system packages in the
  Docker image.

## Configuration

- `SEDOC_CELERY_BROKER_URL` (`redis://…/2`)
- `SEDOC_S3_ENDPOINT`, `_S3_ACCESS_KEY`, `_S3_SECRET_KEY`
- `SEDOC_DATABASE_URL`

## Running locally

```bash
make up
cd services/preview
pip install -r requirements.txt
uvicorn app.main:app --reload --port 8085
# Worker:
celery -A app.worker worker -Q preview,preview-video -l info
```

Via compose:

```bash
docker compose --profile app up -d preview-worker
```

## Testing

```bash
cd services/preview && pytest
```

## Deployment

`deploy/helm/sedoc/templates/preview/` — API + worker deployments,
service (API only), hpa for both, pdb, networkpolicy, servicemonitor.

## Metrics

- `preview_generation_seconds{engine=pdf|office|video}` histogram
- `preview_failures_total{reason}`

## Troubleshooting

**LibreOffice conversion hangs** — headless soffice is notorious for
stuck processes. Workers run with a Celery `task_time_limit` that
kills long-running tasks. If a specific document consistently hangs,
mark its preview as "unsupported" rather than retrying.

**Previews missing for older docs** — the preview service only runs
on `dms.version.uploaded.v1`. To backfill, republish the event or
call `POST /preview/generate` manually.
