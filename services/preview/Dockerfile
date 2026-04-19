FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libreoffice-core libreoffice-writer libreoffice-calc libreoffice-impress \
    poppler-utils ffmpeg libmagic1 \
    fonts-dejavu fonts-liberation \
    libcairo2 libpango-1.0-0 libpangoft2-1.0-0 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app/ app/

RUN useradd -m appuser && chown -R appuser:appuser /app
USER appuser

# Default command runs the API (exposes /healthz, see app/main.py).
# Override with `celery -A app.worker worker` to run as a worker; the
# compose entry for preview-worker does that and supplies its own
# celery-inspect-based healthcheck.
HEALTHCHECK --interval=10s --timeout=3s --retries=5 --start-period=30s \
    CMD python -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://localhost:8080/healthz', timeout=2).status == 200 else 1)" \
    || exit 1
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8080"]
